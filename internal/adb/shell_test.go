package adb

import (
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestShellArgsPreservesRemoteArgumentBoundaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell to simulate the Android shell")
	}
	want := []string{"am", "start", "-d", "aiquestion://open_page?scene=weather&city=hangzhou", "", "space value", "it's quoted", `$(printf injected); "hello"`, `a\b`}
	args := ShellArgs(append([]string{"printf", "%s\\000"}, want...)...)
	if len(args) != 2 || args[0] != "shell" {
		t.Fatalf("invalid adb shell args: %q", args)
	}
	output, err := exec.Command("/bin/sh", "-c", args[1]).Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("remote arguments = %q, want %q", got, want)
	}
}
