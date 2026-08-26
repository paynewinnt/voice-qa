package adb

import (
	"reflect"
	"testing"
)

func TestParseArgsKeepsQuotedShellPipelineTogether(t *testing.T) {
	command := `shell "dumpsys package com.example.app | grep -E 'versionCode=|versionName='"`
	want := []string{
		"shell",
		"dumpsys package com.example.app | grep -E 'versionCode=|versionName='",
	}

	got, err := ParseArgs(command)
	if err != nil {
		t.Fatalf("ParseArgs() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseArgs() = %#v, want %#v", got, want)
	}
}
