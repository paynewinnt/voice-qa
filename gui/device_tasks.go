package main

import (
	"fmt"
	"strings"
	"sync"
)

func (a *App) acquireDeviceTasks(serials []string, task string) (func(), error) {
	a.deviceTaskMu.Lock()
	defer a.deviceTaskMu.Unlock()
	unique := make(map[string]bool)
	for _, serial := range serials {
		serial = strings.TrimSpace(serial)
		if serial == "" {
			return nil, fmt.Errorf("请先选择设备")
		}
		if owner := a.deviceTasks[serial]; owner != "" {
			return nil, fmt.Errorf("设备 %s 正在%s，请等待当前任务完成", serial, owner)
		}
		unique[serial] = true
	}
	if a.deviceTasks == nil {
		a.deviceTasks = make(map[string]string)
	}
	for serial := range unique {
		a.deviceTasks[serial] = task
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			a.deviceTaskMu.Lock()
			defer a.deviceTaskMu.Unlock()
			for serial := range unique {
				delete(a.deviceTasks, serial)
			}
		})
	}, nil
}

func (a *App) beginStartupTask(serial, task string) (func(), error) {
	release, err := a.acquireDeviceTasks([]string{serial}, task)
	if err != nil {
		return nil, err
	}
	a.cmdMu.Lock()
	if a.perfBusy {
		a.cmdMu.Unlock()
		release()
		return nil, fmt.Errorf("当前已有启动时间测试在运行")
	}
	a.perfBusy = true
	a.perfLastStop = PerfettoStopResult{}
	a.cmdMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.cmdMu.Lock()
			a.perfBusy = false
			a.cmdMu.Unlock()
			release()
		})
	}, nil
}
