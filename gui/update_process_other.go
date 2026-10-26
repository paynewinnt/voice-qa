//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

func waitForUpdateProcessExit(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		running, err := updateProcessRunning(pid)
		if err != nil || !running {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待主程序退出超时")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func updateProcessRunning(pid int) (bool, error) {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, nil
	}
	err = process.Signal(syscall.Signal(0))
	if err == nil {
		return true, nil
	}
	return false, nil
}
