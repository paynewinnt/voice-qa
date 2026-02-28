//go:build !windows

package adb

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
