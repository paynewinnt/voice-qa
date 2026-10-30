package adb

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

func ConnectContext(ctx context.Context, address string) (string, error) {
	return connectADB(ctx, address, func(ctx context.Context, args ...string) (string, error) {
		cmd := CommandContext(ctx, args...)
		HideWindow(cmd)
		cmd.WaitDelay = time.Second
		output, err := cmd.CombinedOutput()
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		return strings.TrimSpace(string(output)), err
	})
}

func connectADB(parent context.Context, address string, run func(context.Context, ...string) (string, error)) (string, error) {
	address = strings.TrimSpace(address)
	if net.ParseIP(address) != nil || !strings.Contains(address, ":") {
		address = net.JoinHostPort(address, "5555")
	}
	host, portText, err := net.SplitHostPort(address)
	port, portErr := strconv.Atoi(portText)
	if err != nil || host == "" || strings.HasPrefix(host, "-") || strings.ContainsAny(host, " \r\n\t/") || portErr != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("请输入有效的设备 IP/主机名及端口")
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	output, err := run(ctx, "connect", address)
	if err != nil {
		return "", fmt.Errorf("连接 %s 失败：%s: %w", address, output, err)
	}
	connected := false
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		connected = connected || line == "connected to "+address || line == "already connected to "+address
	}
	if !connected {
		return "", fmt.Errorf("连接 %s 失败：%s；无线调试可能需要先配对或在设备上确认授权", address, output)
	}
	stateCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	state, err := run(stateCtx, "-s", address, "get-state")
	if err != nil {
		return "", fmt.Errorf("%s 尚未就绪（%s），请检查调试授权、配对及网络连接: %w", address, strings.TrimSpace(state), err)
	}
	if apkDeviceState(state) != "device" {
		return "", fmt.Errorf("%s 尚未就绪（%s）：请检查调试授权、配对及网络连接", address, strings.TrimSpace(state))
	}
	return "已连接到 " + address, nil
}
