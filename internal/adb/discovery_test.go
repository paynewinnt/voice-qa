package adb

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testLANNetwork() LANNetwork {
	return LANNetwork{ID: "wifi", Name: "Wi-Fi", IP: "192.168.50.10", CIDR: "192.168.50.0/24", ScanCIDR: "192.168.50.0/24"}
}

func TestLANScanRestrictsScope(t *testing.T) {
	for _, cidr := range []string{"0.0.0.0/0", "8.8.8.0/24", "192.168.50.0/21", "192.168.51.0/24", "not-a-network", "::1/128"} {
		if _, err := lanScanHosts(testLANNetwork(), LANScanOptions{CIDR: cidr, Port: 5555}); err == nil {
			t.Fatalf("unsafe scope accepted: %s", cidr)
		}
	}
	for _, port := range []int{-1, 0, 65536} {
		if _, err := lanScanHosts(testLANNetwork(), LANScanOptions{CIDR: "192.168.50.0/24", Port: port}); err == nil {
			t.Fatalf("invalid port accepted: %d", port)
		}
	}
	hosts, err := lanScanHosts(testLANNetwork(), LANScanOptions{CIDR: "192.168.50.10/24", Port: 5555})
	if err != nil || len(hosts) != 253 {
		t.Fatalf("hosts=%d err=%v", len(hosts), err)
	}
	for _, host := range hosts {
		if host.String() == "192.168.50.10" || strings.HasSuffix(host.String(), ".0") || strings.HasSuffix(host.String(), ".255") {
			t.Fatalf("local/network/broadcast address included: %s", host)
		}
	}
}

func TestLANParsesWindowsMacAndLinuxNeighbors(t *testing.T) {
	output := `接口: 192.168.50.10 --- 0xe
192.168.50.1  12-34-56-78-9A-BC 动态
? (192.168.50.2) at a:b:c:d:e:f on en0 ifscope [ethernet]
192.168.50.3 dev wlan0 lladdr 02:11:22:33:44:55 STALE
192.168.50.4 (incomplete)
192.168.50.255 ff-ff-ff-ff-ff-ff static
224.0.0.22 01-00-5e-00-00-16 static
192.168.50.5 00-00-00-00-00-00 invalid
999.1.1.1 12-34-56-78-9A-BC invalid`
	devices := parseLANNeighbors(output)
	if len(devices) != 3 || devices[1].MAC != "0a:0b:0c:0d:0e:0f" {
		t.Fatalf("bad neighbors: %+v", devices)
	}
	for _, device := range devices {
		if device.CanConnect || device.State != "neighbor" {
			t.Fatal("ARP cache must not imply ADB availability")
		}
	}
}

func TestLANMdnsNeverConnectsToPairingPort(t *testing.T) {
	devices := parseADBMdns(`List of discovered mdns services
adb-tv _adb._tcp. 192.168.50.2:5555
adb-phone _adb-tls-pairing._tcp. 192.168.50.3:37000
adb-phone _adb-tls-connect._tcp. 192.168.50.3:38000
printer _ipp._tcp. 192.168.50.4:631
public _adb._tcp. 8.8.8.8:5555
invalid _adb._tcp. 192.168.50.5:99999`)
	if len(devices) != 3 || devices[1].CanConnect || devices[1].Address != "" || devices[2].Address != "192.168.50.3:38000" {
		t.Fatalf("bad services: %+v", devices)
	}
	merged := mergeLANDevice(devices[2], devices[1])
	if merged.Address != "192.168.50.3:38000" || !merged.CanConnect {
		t.Fatal("pairing announcement replaced connect endpoint")
	}
}

func TestLANDiscoveryMergesSourcesAndBoundsParallelism(t *testing.T) {
	var active, maximum atomic.Int32
	neighborReads := 0
	deps := lanDiscoveryDeps{
		neighbors: func(context.Context, LANNetwork) (string, error) {
			neighborReads++
			output := "192.168.50.2 12-34-56-78-9a-bc dynamic\n192.168.51.5 12-34-56-78-9a-bc dynamic"
			if neighborReads > 1 {
				output += "\n192.168.50.9 12-34-56-78-9a-bd dynamic"
			}
			return output, nil
		},
		mdns: func(context.Context) (string, error) {
			return "adb-tv _adb-tls-connect._tcp. 192.168.50.2:38000\nself _adb._tcp. 192.168.50.10:5555", nil
		},
		probe: func(ctx context.Context, localIP, address string) (bool, bool) {
			if localIP != "192.168.50.10" {
				t.Error("probe not bound to selected interface")
			}
			n := active.Add(1)
			for {
				previous := maximum.Load()
				if previous >= n || maximum.CompareAndSwap(previous, n) {
					break
				}
			}
			defer active.Add(-1)
			time.Sleep(time.Millisecond)
			return address == "192.168.50.2:5555", address == "192.168.50.3:5555"
		},
	}
	var progress []LANScanProgress
	result := discoverLAN(context.Background(), testLANNetwork(), LANScanOptions{CIDR: "192.168.50.0/24", Port: 5555}, func(p LANScanProgress) { progress = append(progress, p) }, deps)
	if !result.Success || result.Scanned != 253 || len(result.Devices) != 3 || maximum.Load() > 64 || maximum.Load() < 2 {
		t.Fatalf("result=%+v parallel=%d", result, maximum.Load())
	}
	device := result.Devices[0]
	if device.Address != "192.168.50.2:38000" || device.Name != "adb-tv" || device.MAC == "" || len(device.Sources) != 3 {
		t.Fatalf("merge lost evidence: %+v", device)
	}
	if progress[len(progress)-1].Scanned != result.Total || active.Load() != 0 {
		t.Fatal("unfinished scan or worker leak")
	}
}

func TestLANDiscoveryCancellationAndPartialCapabilities(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var probes atomic.Int32
	deps := lanDiscoveryDeps{
		neighbors: func(context.Context, LANNetwork) (string, error) { return "", errors.New("ARP unavailable") },
		mdns:      func(context.Context) (string, error) { return "", errors.New("mDNS unavailable") },
		probe:     func(context.Context, string, string) (bool, bool) { probes.Add(1); cancel(); return false, false },
	}
	result := discoverLAN(ctx, testLANNetwork(), LANScanOptions{CIDR: "192.168.50.0/24", Port: 5555}, nil, deps)
	if result.Success || !result.Canceled || probes.Load() > 64 || len(result.Warnings) != 2 {
		t.Fatalf("cancellation not bounded: %+v probes=%d", result, probes.Load())
	}
}

func TestLANPortProbeUsesLoopbackOnly(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	open, alive := probeLANPort(context.Background(), "127.0.0.1", address)
	if !open || !alive {
		t.Fatal("open listener not discovered")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	open, alive = probeLANPort(context.Background(), "127.0.0.1", address)
	if open || !alive {
		t.Fatal("refused connection not recognized")
	}
}

func TestLANPrefersWifiOverVirtualAdapters(t *testing.T) {
	if lanInterfacePriority("Wi-Fi") <= lanInterfacePriority("Ethernet") || lanInterfacePriority("WLAN") <= lanInterfacePriority("vEthernet (Default Switch)") {
		t.Fatal("unexpected interface preference")
	}
}
