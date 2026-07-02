package main

import "testing"

func TestAdbTargetArgDetection(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{name: "serial", args: []string{"-s", "device-1", "shell", "id"}, want: true},
		{name: "transport id", args: []string{"-t", "3", "shell", "id"}, want: true},
		{name: "usb shortcut", args: []string{"-d", "shell", "id"}, want: true},
		{name: "emulator shortcut", args: []string{"-e", "shell", "id"}, want: true},
		{name: "no target", args: []string{"shell", "id"}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasAdbTargetArg(tc.args); got != tc.want {
				t.Fatalf("hasAdbTargetArg(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestShouldAutoTargetAdbCommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{name: "shell is device scoped", args: []string{"shell", "pm", "list", "packages", "-3"}, want: true},
		{name: "install is device scoped", args: []string{"install", "app.apk"}, want: true},
		{name: "devices is global", args: []string{"devices"}, want: false},
		{name: "connect is global", args: []string{"connect", "127.0.0.1:5555"}, want: false},
		{name: "explicit serial still classifies command", args: []string{"-s", "device-1", "shell", "id"}, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAutoTargetAdbCommand(tc.args); got != tc.want {
				t.Fatalf("shouldAutoTargetAdbCommand(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
