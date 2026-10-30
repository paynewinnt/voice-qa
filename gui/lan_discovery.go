package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"voice-qa/internal/adb"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func (a *App) GetLANNetworks() ([]adb.LANNetwork, error) {
	return adb.LocalNetworks()
}

func (a *App) startLANOperation(requestID string) (context.Context, func(), error) {
	a.lanMu.Lock()
	defer a.lanMu.Unlock()
	if requestID == "" || len(requestID) > 100 {
		return nil, nil, fmt.Errorf("局域网任务编号无效")
	}
	if requestID == a.lanCanceledRequestID {
		return nil, nil, context.Canceled
	}
	if a.lanCancel != nil {
		return nil, nil, fmt.Errorf("局域网扫描或连接仍在执行，请先停止当前任务")
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	a.lanRequestID, a.lanCancel = requestID, cancel
	return ctx, func() {
		cancel()
		a.lanMu.Lock()
		a.lanRequestID, a.lanCancel = "", nil
		a.lanMu.Unlock()
	}, nil
}

func (a *App) cancelLANOperation(requestID string) bool {
	a.lanMu.Lock()
	defer a.lanMu.Unlock()
	if a.lanCancel == nil {
		if requestID != "" {
			// Wails may dispatch a close/cancel before the start call reaches Go.
			a.lanCanceledRequestID = requestID
			return true
		}
		return false
	}
	if requestID != "" && a.lanRequestID != requestID {
		return false
	}
	a.lanCancel()
	return true
}

func (a *App) CancelLANOperation(requestID string) bool {
	if requestID == "" || len(requestID) > 100 {
		return false
	}
	return a.cancelLANOperation(requestID)
}

func (a *App) ScanLANDevices(requestID string, options adb.LANScanOptions) adb.LANScanResult {
	ctx, finish, err := a.startLANOperation(requestID)
	if err != nil {
		return adb.LANScanResult{Message: err.Error(), Canceled: errors.Is(err, context.Canceled)}
	}
	defer finish()
	a.lanMu.Lock()
	a.lanCandidates = make(map[string]bool)
	a.lanMu.Unlock()
	result := adb.DiscoverLAN(ctx, options, func(progress adb.LANScanProgress) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "lan-scan-progress", map[string]interface{}{"requestID": requestID, "progress": progress})
		}
	})
	a.lanMu.Lock()
	for _, device := range result.Devices {
		if device.CanConnect {
			a.lanCandidates[device.Address] = true
		}
	}
	a.lanMu.Unlock()
	return result
}

type LANConnectItem struct {
	Address string `json:"address"`
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type LANConnectResult struct {
	Message  string           `json:"message"`
	Canceled bool             `json:"canceled"`
	Items    []LANConnectItem `json:"items"`
}

func (a *App) ConnectLANDevices(requestID string, addresses []string) LANConnectResult {
	ctx, finish, err := a.startLANOperation(requestID)
	if err != nil {
		return LANConnectResult{Message: err.Error(), Canceled: errors.Is(err, context.Canceled)}
	}
	defer finish()
	a.lanMu.Lock()
	targets, err := lanConnectTargets(addresses, a.lanCandidates)
	a.lanMu.Unlock()
	if err != nil {
		return LANConnectResult{Message: err.Error()}
	}
	return connectLANBatch(ctx, targets, adb.ConnectContext, func(item LANConnectItem) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "lan-connect-progress", map[string]interface{}{"requestID": requestID, "item": item})
		}
	})
}

func lanConnectTargets(addresses []string, candidates map[string]bool) ([]string, error) {
	if len(addresses) == 0 || len(addresses) > 64 {
		return nil, fmt.Errorf("请选择 1 到 64 台可连接设备")
	}
	seen := make(map[string]bool)
	var targets []string
	for _, address := range addresses {
		address = strings.TrimSpace(address)
		if !candidates[address] {
			return nil, fmt.Errorf("%s 不在本次扫描的可连接结果中，请重新扫描", address)
		}
		if !seen[address] {
			seen[address] = true
			targets = append(targets, address)
		}
	}
	return targets, nil
}

func connectLANBatch(ctx context.Context, targets []string, connect func(context.Context, string) (string, error), progress func(LANConnectItem)) LANConnectResult {
	jobs := make(chan string)
	items := make(chan LANConnectItem, 4)
	var workers sync.WaitGroup
	for i := 0; i < min(4, len(targets)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for address := range jobs {
				if ctx.Err() != nil {
					return
				}
				message, err := connect(ctx, address)
				if err != nil {
					message = err.Error()
				}
				items <- LANConnectItem{Address: address, Success: err == nil, Message: message}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, address := range targets {
			select {
			case jobs <- address:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { workers.Wait(); close(items) }()
	result := LANConnectResult{Items: []LANConnectItem{}}
	successes := 0
	for item := range items {
		result.Items = append(result.Items, item)
		if item.Success {
			successes++
		}
		if progress != nil {
			progress(item)
		}
	}
	result.Canceled = ctx.Err() != nil
	result.Message = fmt.Sprintf("连接完成：%d/%d 台成功", successes, len(targets))
	if result.Canceled {
		result.Message = fmt.Sprintf("连接已停止：%d/%d 台成功，已处理 %d 台", successes, len(targets), len(result.Items))
	}
	return result
}
