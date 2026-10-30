package adb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type LANNetwork struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IP       string `json:"ip"`
	CIDR     string `json:"cidr"`
	ScanCIDR string `json:"scanCIDR"`
}

type LANDevice struct {
	IP         string   `json:"ip"`
	Address    string   `json:"address"`
	Port       int      `json:"port"`
	Name       string   `json:"name"`
	MAC        string   `json:"mac"`
	State      string   `json:"state"`
	Sources    []string `json:"sources"`
	CanConnect bool     `json:"canConnect"`
}

type LANScanOptions struct {
	NetworkID string `json:"networkID"`
	CIDR      string `json:"cidr"`
	Port      int    `json:"port"`
}

type LANScanProgress struct {
	Scanned int        `json:"scanned"`
	Total   int        `json:"total"`
	Device  *LANDevice `json:"device,omitempty"`
}

type LANScanResult struct {
	Success  bool        `json:"success"`
	Canceled bool        `json:"canceled"`
	Message  string      `json:"message"`
	Devices  []LANDevice `json:"devices"`
	Scanned  int         `json:"scanned"`
	Total    int         `json:"total"`
	Warnings []string    `json:"warnings"`
}

func LocalNetworks() ([]LANNetwork, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("读取本机网卡失败: %w", err)
	}
	var networks []LANNetwork
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("读取网卡 %s 地址失败: %w", iface.Name, err)
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() {
				continue
			}
			scanPrefix := prefix
			if prefix.Bits() < 24 {
				scanPrefix = netip.PrefixFrom(prefix.Addr(), 24)
			}
			networks = append(networks, LANNetwork{
				ID: strconv.Itoa(iface.Index) + ":" + prefix.Addr().String(), Name: iface.Name,
				IP: prefix.Addr().String(), CIDR: prefix.Masked().String(), ScanCIDR: scanPrefix.Masked().String(),
			})
		}
	}
	sort.SliceStable(networks, func(i, j int) bool {
		return lanInterfacePriority(networks[i].Name) > lanInterfacePriority(networks[j].Name)
	})
	return networks, nil
}

func lanInterfacePriority(name string) int {
	name = strings.ToLower(name)
	for _, marker := range []string{"virtual", "vethernet", "vmware", "vbox", "docker", "bridge", "vpn", "tailscale", "utun"} {
		if strings.Contains(name, marker) {
			return -1
		}
	}
	for _, marker := range []string{"wi-fi", "wifi", "wlan", "无线"} {
		if strings.Contains(name, marker) {
			return 2
		}
	}
	if name == "en0" {
		return 1
	}
	return 0
}

func lanScanHosts(network LANNetwork, options LANScanOptions) ([]netip.Addr, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(options.CIDR))
	if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() || prefix.Bits() < 22 {
		return nil, fmt.Errorf("请选择本机私有 IPv4 网段，单次最多 1024 个地址（/22 或更小范围）")
	}
	if options.Port < 1 || options.Port > 65535 {
		return nil, fmt.Errorf("ADB 端口必须在 1 到 65535 之间")
	}
	local, err := netip.ParsePrefix(network.CIDR)
	if err != nil {
		return nil, fmt.Errorf("本机网段无效")
	}
	prefix = prefix.Masked()
	var hosts []netip.Addr
	count := 1 << (32 - prefix.Bits())
	ip := prefix.Addr()
	for i := 0; i < count; i++ {
		if !local.Contains(ip) {
			return nil, fmt.Errorf("扫描范围必须位于所选网卡的 %s 内", network.CIDR)
		}
		if ip.String() != network.IP && (prefix.Bits() >= 31 || (i != 0 && i != count-1)) {
			hosts = append(hosts, ip)
		}
		ip = ip.Next()
	}
	return hosts, nil
}

type lanDiscoveryDeps struct {
	neighbors func(context.Context, LANNetwork) (string, error)
	mdns      func(context.Context) (string, error)
	probe     func(context.Context, string, string) (bool, bool)
}

func DiscoverLAN(ctx context.Context, options LANScanOptions, progress func(LANScanProgress)) LANScanResult {
	networks, err := LocalNetworks()
	if err != nil {
		return LANScanResult{Message: err.Error()}
	}
	for _, network := range networks {
		if network.ID == options.NetworkID {
			ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
			defer cancel()
			return discoverLAN(ctx, network, options, progress, lanDiscoveryDeps{readLANNeighbors, readADBMdns, probeLANPort})
		}
	}
	return LANScanResult{Message: "所选网卡已不可用，请刷新网卡列表"}
}

func discoverLAN(ctx context.Context, network LANNetwork, options LANScanOptions, progress func(LANScanProgress), deps lanDiscoveryDeps) LANScanResult {
	hosts, err := lanScanHosts(network, options)
	if err != nil {
		return LANScanResult{Message: err.Error()}
	}
	result := LANScanResult{Total: len(hosts), Devices: []LANDevice{}}
	allowed := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		allowed[host.String()] = true
	}
	found := make(map[string]LANDevice)
	emit := func(device *LANDevice) {
		if progress != nil {
			progress(LANScanProgress{Scanned: result.Scanned, Total: result.Total, Device: device})
		}
	}
	merge := func(device LANDevice) {
		if !allowed[device.IP] {
			return
		}
		current := mergeLANDevice(found[device.IP], device)
		found[device.IP] = current
		emit(&current)
	}
	readNeighbors := func() {
		output, err := deps.neighbors(ctx, network)
		if err != nil {
			warning := "邻居表读取失败，部分未开放 ADB 的设备可能不可见：" + err.Error()
			if len(result.Warnings) == 0 || result.Warnings[0] != warning {
				result.Warnings = append(result.Warnings, warning)
			}
			return
		}
		for _, device := range parseLANNeighbors(output) {
			merge(device)
		}
	}
	emit(nil)
	readNeighbors()
	if output, err := deps.mdns(ctx); err != nil {
		result.Warnings = append(result.Warnings, "无线调试发现不可用，仍扫描指定 ADB 端口："+err.Error())
	} else {
		for _, device := range parseADBMdns(output) {
			merge(device)
		}
	}
	type probeResult struct {
		ip          string
		open, alive bool
	}
	jobs := make(chan netip.Addr)
	results := make(chan probeResult, 64)
	var workers sync.WaitGroup
	for i := 0; i < min(64, len(hosts)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ip := range jobs {
				if ctx.Err() != nil {
					return
				}
				open, alive := deps.probe(ctx, network.IP, net.JoinHostPort(ip.String(), strconv.Itoa(options.Port)))
				select {
				case results <- probeResult{ip.String(), open, alive}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, ip := range hosts {
			select {
			case jobs <- ip:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { workers.Wait(); close(results) }()
	for probe := range results {
		result.Scanned++
		if probe.open {
			merge(LANDevice{IP: probe.ip, Address: net.JoinHostPort(probe.ip, strconv.Itoa(options.Port)), Port: options.Port, State: "port-open", CanConnect: true, Sources: []string{"TCP"}})
		} else if probe.alive {
			merge(LANDevice{IP: probe.ip, State: "reachable", Sources: []string{"TCP"}})
		} else if result.Scanned%8 == 0 {
			emit(nil)
		}
	}
	if ctx.Err() == nil {
		// The sweep populates ARP even when the target port is closed/filtered.
		readNeighbors()
	}
	for _, device := range found {
		result.Devices = append(result.Devices, device)
	}
	sort.Slice(result.Devices, func(i, j int) bool {
		return netip.MustParseAddr(result.Devices[i].IP).Less(netip.MustParseAddr(result.Devices[j].IP))
	})
	result.Success = ctx.Err() == nil
	result.Canceled = errors.Is(ctx.Err(), context.Canceled)
	switch {
	case result.Canceled:
		result.Message = "扫描已停止，保留已发现设备"
	case ctx.Err() != nil:
		result.Message = "扫描达到时限，保留已发现设备"
	default:
		result.Message = fmt.Sprintf("扫描完成：发现 %d 台设备", len(result.Devices))
	}
	emit(nil)
	return result
}

func mergeLANDevice(old, next LANDevice) LANDevice {
	// An ADB service announcement is stronger evidence than an arbitrary open port.
	rank := map[string]int{"neighbor": 0, "reachable": 1, "pairing": 2, "port-open": 3, "advertised": 4}
	merged := old
	if old.IP == "" || rank[next.State] > rank[old.State] {
		merged = next
	}
	if old.Name != "" {
		merged.Name = old.Name
	}
	if next.Name != "" {
		merged.Name = next.Name
	}
	if old.MAC != "" {
		merged.MAC = old.MAC
	}
	if next.MAC != "" {
		merged.MAC = next.MAC
	}
	merged.Sources = nil
	for _, source := range append(append([]string{}, old.Sources...), next.Sources...) {
		present := false
		for _, existing := range merged.Sources {
			present = present || source == existing
		}
		if !present {
			merged.Sources = append(merged.Sources, source)
		}
	}
	return merged
}

var lanIPv4Pattern = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
var lanMACPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{1,2}(?:[:-][0-9a-f]{1,2}){5}\b`)

func parseLANNeighbors(output string) []LANDevice {
	var devices []LANDevice
	for _, line := range strings.Split(output, "\n") {
		ip, err := netip.ParseAddr(lanIPv4Pattern.FindString(line))
		if err != nil || !ip.Is4() || !ip.IsPrivate() {
			continue
		}
		parts := strings.Split(strings.ReplaceAll(lanMACPattern.FindString(line), "-", ":"), ":")
		if len(parts) != 6 {
			continue
		}
		for i, part := range parts {
			if len(part) == 1 {
				parts[i] = "0" + part
			}
		}
		mac, err := net.ParseMAC(strings.Join(parts, ":"))
		if err != nil || mac[0]&1 != 0 || mac.String() == "00:00:00:00:00:00" {
			continue
		}
		devices = append(devices, LANDevice{IP: ip.String(), MAC: mac.String(), State: "neighbor", Sources: []string{"邻居表"}})
	}
	return devices
}

func parseADBMdns(output string) []LANDevice {
	var devices []LANDevice
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		host, portText, err := net.SplitHostPort(fields[2])
		ip, ipErr := netip.ParseAddr(host)
		port, portErr := strconv.Atoi(portText)
		if err != nil || ipErr != nil || !ip.Is4() || !ip.IsPrivate() || portErr != nil || port < 1 || port > 65535 {
			continue
		}
		device := LANDevice{IP: host, Port: port, Name: fields[0], Sources: []string{"mDNS"}}
		switch strings.TrimSuffix(fields[1], ".") {
		case "_adb._tcp", "_adb-tls-connect._tcp":
			device.State, device.Address, device.CanConnect = "advertised", fields[2], true
		case "_adb-tls-pairing._tcp":
			device.State = "pairing"
		default:
			continue
		}
		devices = append(devices, device)
	}
	return devices
}

func readLANNeighbors(parent context.Context, network LANNetwork) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(ctx, "arp.exe", "-a", "-N", network.IP)
	case "darwin":
		cmd = exec.CommandContext(ctx, "/usr/sbin/arp", "-an", "-i", network.Name)
	default:
		cmd = exec.CommandContext(ctx, "ip", "-4", "neigh", "show", "dev", network.Name)
	}
	HideWindow(cmd)
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
	}
	return string(output), nil
}

func readADBMdns(parent context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	cmd := CommandContext(ctx, "mdns", "services")
	HideWindow(cmd)
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
	}
	return string(output), nil
}

func probeLANPort(parent context.Context, localIP, address string) (open, alive bool) {
	ctx, cancel := context.WithTimeout(parent, 450*time.Millisecond)
	defer cancel()
	dialer := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(localIP)}}
	connection, err := dialer.DialContext(ctx, "tcp4", address)
	if err == nil {
		_ = connection.Close()
		return true, true
	}
	// Connection refusal is also a response from a live host (Windows uses WSAECONNREFUSED).
	return false, errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.Errno(10061))
}
