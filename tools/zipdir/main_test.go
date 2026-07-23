package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateZipMarksUnicodeNamesAsUTF8(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "package")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "语音播测工具.exe"), []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "package.zip")
	if err := createZip(source, archivePath); err != nil {
		t.Fatalf("createZip() error = %v", err)
	}

	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		if file.Name == "package/语音播测工具.exe" {
			if file.Flags&0x800 == 0 {
				t.Fatalf("unicode entry flags = %#x, UTF-8 flag is not set", file.Flags)
			}
			return
		}
	}
	t.Fatal("unicode executable entry not found")
}
