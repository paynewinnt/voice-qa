package tts

import (
	"strings"
	"testing"
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
