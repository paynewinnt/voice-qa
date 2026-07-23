package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateTrustedUpdatePath(t *testing.T) {
	dir := t.TempDir()
	trusted := filepath.Join(dir, "voice-qa.zip")
	if err := os.WriteFile(trusted, []byte("zip"), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := validateTrustedUpdatePath(trusted, trusted)
	if err != nil {
		t.Fatalf("validateTrustedUpdatePath() error = %v", err)
	}
	if got != trusted {
		t.Fatalf("validateTrustedUpdatePath() = %q, want %q", got, trusted)
	}

	other := filepath.Join(dir, "other.zip")
	if err := os.WriteFile(other, []byte("zip"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateTrustedUpdatePath(other, trusted); err == nil {
		t.Fatal("validateTrustedUpdatePath() accepted an untrusted file")
	}
}

func TestReplaceUpdateArchive(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "package.zip.download")
	target := filepath.Join(dir, "package.zip")
	if err := os.WriteFile(source, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := replaceUpdateArchive(source, target); err != nil {
		t.Fatalf("replaceUpdateArchive() error = %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new" {
		t.Fatalf("target content = %q, want new", content)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still exists after replacement: %v", err)
	}
}

func TestWindowsUpdateScriptPreservesDataAndRollsBack(t *testing.T) {
	for _, required := range []string{
		"'output', 'config.json', 'text.txt'",
		"function Restore-Update",
		"Expand-Archive",
		"Get-CimInstance Win32_Process",
		"$rootExecutables.Count -ne 1",
		"Start-Process",
		"Remove-Item -LiteralPath $ZipPath",
	} {
		if !strings.Contains(windowsUpdateScript, required) {
			t.Fatalf("windowsUpdateScript does not contain %q", required)
		}
	}
}
