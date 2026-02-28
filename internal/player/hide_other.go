//go:build !windows

package player

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
