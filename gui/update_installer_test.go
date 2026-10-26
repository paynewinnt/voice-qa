package main

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
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

func TestNativeUpdaterReplacesProgramAndPreservesUserData(t *testing.T) {
	dir := t.TempDir()
	installDir := filepath.Join(dir, "installed")
	mustWriteUpdateTestFile(t, filepath.Join(installDir, "语音播测工具.exe"), "old-exe")
	mustWriteUpdateTestFile(t, filepath.Join(installDir, "adb", "adb.exe"), "old-adb")
	mustWriteUpdateTestFile(t, filepath.Join(installDir, "config.json"), "user-config")
	mustWriteUpdateTestFile(t, filepath.Join(installDir, "text.txt"), "user-text")
	mustWriteUpdateTestFile(t, filepath.Join(installDir, "output", "user-result.txt"), "user-result")
	mustWriteUpdateTestFile(t, filepath.Join(installDir, "用户说明.txt"), "keep-me")
	mustWriteUpdateTestFile(t, filepath.Join(installDir, "models", "custom.onnx"), "user-model")
	mustWriteUpdateTestFile(t, filepath.Join(installDir, "models", "builtin.onnx"), "old-model")

	zipPath := filepath.Join(dir, "update.zip")
	writeNativeUpdateTestArchive(t, zipPath, "tts-gui-windows-amd64-vnext", map[string]string{
		"语音播测工具.exe":          "new-exe",
		"adb/adb.exe":         "new-adb",
		"config.json":         "default-config",
		"text.txt":            "default-text",
		"output/example.txt":  "default-output",
		"README.txt":          "new-readme",
		"models/builtin.onnx": "new-model",
	})

	startCount := 0
	options := nativeUpdaterOptions{
		ZipPath:    zipPath,
		InstallDir: installDir,
		ExeName:    "语音播测工具.exe",
		ParentPID:  42,
		HelperPath: filepath.Join(os.TempDir(), nativeUpdaterFilePrefix+"test.exe"),
	}
	hooks := nativeUpdaterHooks{
		waitForProcessExit: func(int, time.Duration) error { return nil },
		startApplication: func(executablePath, workingDirectory, helperPath string) (int, error) {
			startCount++
			if executablePath != filepath.Join(installDir, options.ExeName) {
				t.Fatalf("started executable = %q", executablePath)
			}
			if workingDirectory != installDir {
				t.Fatalf("working directory = %q", workingDirectory)
			}
			return 1234, nil
		},
		processRunning: func(int) (bool, error) { return true, nil },
		sleep:          func(time.Duration) {},
	}
	if err := runNativeUpdater(options, hooks); err != nil {
		t.Fatalf("runNativeUpdater() error = %v", err)
	}
	if startCount != 1 {
		t.Fatalf("application start count = %d, want 1", startCount)
	}

	assertUpdateTestFile(t, filepath.Join(installDir, "语音播测工具.exe"), "new-exe")
	assertUpdateTestFile(t, filepath.Join(installDir, "adb", "adb.exe"), "new-adb")
	assertUpdateTestFile(t, filepath.Join(installDir, "README.txt"), "new-readme")
	assertUpdateTestFile(t, filepath.Join(installDir, "config.json"), "user-config")
	assertUpdateTestFile(t, filepath.Join(installDir, "text.txt"), "user-text")
	assertUpdateTestFile(t, filepath.Join(installDir, "output", "user-result.txt"), "user-result")
	assertUpdateTestFile(t, filepath.Join(installDir, "用户说明.txt"), "keep-me")
	assertUpdateTestFile(t, filepath.Join(installDir, "models", "custom.onnx"), "user-model")
	assertUpdateTestFile(t, filepath.Join(installDir, "models", "builtin.onnx"), "new-model")
	if _, err := os.Stat(filepath.Join(installDir, "output", "example.txt")); !os.IsNotExist(err) {
		t.Fatalf("packaged output unexpectedly replaced user output: %v", err)
	}
	if _, err := os.Stat(zipPath); !os.IsNotExist(err) {
		t.Fatalf("installed update archive still exists: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(installDir, ".voice-qa-update-backup-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("update backup was not cleaned: %v", backups)
	}
}

func TestNativeUpdaterRollsBackWhenNewApplicationCannotStart(t *testing.T) {
	dir := t.TempDir()
	installDir := filepath.Join(dir, "installed")
	mustWriteUpdateTestFile(t, filepath.Join(installDir, "语音播测工具.exe"), "old-exe")
	mustWriteUpdateTestFile(t, filepath.Join(installDir, "adb", "adb.exe"), "old-adb")
	mustWriteUpdateTestFile(t, filepath.Join(installDir, "models", "custom.onnx"), "user-model")
	mustWriteUpdateTestFile(t, filepath.Join(installDir, "models", "builtin.onnx"), "old-model")

	zipPath := filepath.Join(dir, "update.zip")
	writeNativeUpdateTestArchive(t, zipPath, "tts-gui-windows-amd64-vnext", map[string]string{
		"语音播测工具.exe":                    "new-exe",
		"adb/adb.exe":                   "new-adb",
		"models/builtin.onnx":           "new-model",
		"models/new.onnx":               "new-extra-model",
		"new-directory/nested/file.txt": "new-file",
	})

	startCount := 0
	options := nativeUpdaterOptions{
		ZipPath:    zipPath,
		InstallDir: installDir,
		ExeName:    "语音播测工具.exe",
		ParentPID:  42,
		HelperPath: filepath.Join(os.TempDir(), nativeUpdaterFilePrefix+"test.exe"),
	}
	hooks := nativeUpdaterHooks{
		waitForProcessExit: func(int, time.Duration) error { return nil },
		startApplication: func(string, string, string) (int, error) {
			startCount++
			if startCount == 1 {
				return 0, fmt.Errorf("simulated launch failure")
			}
			return 5678, nil
		},
		processRunning: func(int) (bool, error) { return true, nil },
		sleep:          func(time.Duration) {},
	}
	if err := runNativeUpdater(options, hooks); err == nil {
		t.Fatal("runNativeUpdater() succeeded after simulated launch failure")
	}
	if startCount != 2 {
		t.Fatalf("application start count = %d, want new launch plus old restart", startCount)
	}
	assertUpdateTestFile(t, filepath.Join(installDir, "语音播测工具.exe"), "old-exe")
	assertUpdateTestFile(t, filepath.Join(installDir, "adb", "adb.exe"), "old-adb")
	assertUpdateTestFile(t, filepath.Join(installDir, "models", "custom.onnx"), "user-model")
	assertUpdateTestFile(t, filepath.Join(installDir, "models", "builtin.onnx"), "old-model")
	for _, name := range []string{"models/new.onnx", "new-directory"} {
		if _, err := os.Stat(filepath.Join(installDir, name)); !os.IsNotExist(err) {
			t.Fatalf("new path remains after rollback: %s (%v)", name, err)
		}
	}
	if _, err := os.Stat(zipPath); err != nil {
		t.Fatalf("failed update archive should remain for diagnostics: %v", err)
	}
}

func TestExtractNativeUpdateArchiveRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "malicious.zip")
	archiveFile, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(archiveFile)
	writer, err := archive.Create("package/../../outside.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("unsafe")); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}

	if _, _, err := extractNativeUpdateArchive(zipPath); err == nil {
		t.Fatal("extractNativeUpdateArchive() accepted a path traversal entry")
	}
	if _, err := os.Stat(filepath.Join(dir, "outside.txt")); !os.IsNotExist(err) {
		t.Fatalf("path traversal created an outside file: %v", err)
	}
}

func TestNativeUpdaterPreservesConflictingUserPath(t *testing.T) {
	dir := t.TempDir()
	installDir := filepath.Join(dir, "installed")
	mustWriteUpdateTestFile(t, filepath.Join(installDir, "voice.exe"), "old-exe")
	mustWriteUpdateTestFile(t, filepath.Join(installDir, "a.txt"), "old-file")
	mustWriteUpdateTestFile(t, filepath.Join(installDir, "models"), "user-file-not-directory")
	archivePath := filepath.Join(dir, "update.zip")
	writeNativeUpdateTestArchive(t, archivePath, "next", map[string]string{
		"voice.exe": "new-exe", "a.txt": "new-file", "models/builtin.onnx": "new-model",
	})
	if _, err := installNativeUpdateArchive(archivePath, installDir, "voice.exe", nil); err == nil || !strings.Contains(err.Error(), "类型冲突") {
		t.Fatalf("conflicting user path accepted: %v", err)
	}
	assertUpdateTestFile(t, filepath.Join(installDir, "voice.exe"), "old-exe")
	assertUpdateTestFile(t, filepath.Join(installDir, "a.txt"), "old-file")
	assertUpdateTestFile(t, filepath.Join(installDir, "models"), "user-file-not-directory")
}

func writeNativeUpdateTestArchive(t *testing.T, archivePath, rootName string, files map[string]string) {
	t.Helper()
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(archiveFile)
	if _, err := archive.Create(rootName + "/"); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		writer, err := archive.Create(rootName + "/" + strings.TrimPrefix(filepath.ToSlash(name), "/"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustWriteUpdateTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func assertUpdateTestFile(t *testing.T, path, expected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(content) != expected {
		t.Fatalf("%s content = %q, want %q", path, content, expected)
	}
}
