//go:build windows

package main

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

func waitForUpdateProcessExit(pid int, timeout time.Duration) error {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return nil
		}
		return fmt.Errorf("打开主程序进程失败: %w", err)
	}
	defer windows.CloseHandle(handle)

	waitMilliseconds := uint32(timeout / time.Millisecond)
	event, err := windows.WaitForSingleObject(handle, waitMilliseconds)
	if err != nil {
		return fmt.Errorf("等待主程序退出失败: %w", err)
	}
	switch event {
	case windows.WAIT_OBJECT_0:
		return nil
	case uint32(windows.WAIT_TIMEOUT):
		return fmt.Errorf("等待主程序退出超时")
	default:
		return fmt.Errorf("等待主程序退出返回未知状态: %d", event)
	}
}

func updateProcessRunning(pid int) (bool, error) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return false, nil
		}
		return false, err
	}
	defer windows.CloseHandle(handle)
	event, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return false, err
	}
	return event == uint32(windows.WAIT_TIMEOUT), nil
}
