package tts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGetAvailableVoicesForGUI(t *testing.T) {
	voices := GetAvailableVoices()
	if len(voices) == 0 {
		t.Fatal("GetAvailableVoices returned no voices")
	}

	seen := make(map[string]struct{}, len(voices))
	engines := make(map[string]bool)
	for _, voice := range voices {
		if voice.ID == "" || voice.Name == "" {
			t.Fatalf("voice has missing fields: %+v", voice)
		}
		if voice.Engine != "edge" && voice.Engine != "piper" {
			t.Fatalf("voice %q has unsupported engine %q", voice.ID, voice.Engine)
		}
		if !strings.HasPrefix(voice.ID, voice.Engine+":") {
			t.Fatalf("voice ID %q does not match engine %q", voice.ID, voice.Engine)
		}
		if _, ok := seen[voice.ID]; ok {
			t.Fatalf("duplicate voice ID %q", voice.ID)
		}
		seen[voice.ID] = struct{}{}
		engines[voice.Engine] = true
	}

	if !engines["edge"] || !engines["piper"] {
		t.Fatalf("voice engines = %v, want edge and piper", engines)
	}
}

func TestCanceledContextDoesNotStartEngine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, engine := range []Engine{NewEdge(""), &Piper{}} {
		if err := SynthesizeContext(ctx, engine, "text", filepath.Join(t.TempDir(), "out.wav")); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled synthesis = %v", err)
		}
	}
}

func TestPiperCancellationTerminatesRunningProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Piper uses POSIX shell")
	}
	dir := t.TempDir()
	model := filepath.Join(dir, "model.onnx")
	if err := os.WriteFile(model, []byte("model"), 0600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(dir, "piper")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf started > \"$PIPER_TEST_STARTED\"\nexec /bin/sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(dir, "started")
	t.Setenv("PIPER_TEST_STARTED", started)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := &Piper{ModelPath: model, ExePath: executable}
	done := make(chan error, 1)
	go func() { done <- engine.SynthesizeContext(ctx, "hello", filepath.Join(dir, "out.wav")) }()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Piper process did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Piper process did not stop")
	}
}
