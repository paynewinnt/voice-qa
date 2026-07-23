package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	sourceDir := flag.String("source", "", "directory to add as the archive root")
	outputPath := flag.String("output", "", "zip archive path")
	flag.Parse()
	if err := createZip(*sourceDir, *outputPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func createZip(sourceDir, outputPath string) error {
	if strings.TrimSpace(sourceDir) == "" || strings.TrimSpace(outputPath) == "" {
		return fmt.Errorf("source and output are required")
	}
	sourceDir, err := filepath.Abs(sourceDir)
	if err != nil {
		return fmt.Errorf("resolve source directory: %w", err)
	}
	info, err := os.Stat(sourceDir)
	if err != nil {
		return fmt.Errorf("read source directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory: %s", sourceDir)
	}
	outputPath, err = filepath.Abs(outputPath)
	if err != nil {
		return fmt.Errorf("resolve output path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	tempFile, err := os.CreateTemp(filepath.Dir(outputPath), ".voice-qa-zip-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary archive: %w", err)
	}
	tempPath := tempFile.Name()
	cleanup := func() {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
	}

	archive := zip.NewWriter(tempFile)
	baseDir := filepath.Dir(sourceDir)
	err = filepath.WalkDir(sourceDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not supported: %s", path)
		}
		relativePath, err := filepath.Rel(baseDir, path)
		if err != nil {
			return err
		}
		archiveName := filepath.ToSlash(relativePath)
		if entry.IsDir() {
			archiveName += "/"
		}
		header, err := zip.FileInfoHeader(entryInfo)
		if err != nil {
			return err
		}
		header.Name = archiveName
		header.NonUTF8 = false
		if entry.IsDir() {
			_, err = archive.CreateHeader(header)
			return err
		}
		header.Method = zip.Deflate
		writer, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		_ = archive.Close()
		cleanup()
		return fmt.Errorf("add files to archive: %w", err)
	}
	if err := archive.Close(); err != nil {
		cleanup()
		return fmt.Errorf("finalize archive: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close archive: %w", err)
	}
	if err := os.Remove(outputPath); err != nil && !os.IsNotExist(err) {
		cleanup()
		return fmt.Errorf("replace existing archive: %w", err)
	}
	if err := os.Rename(tempPath, outputPath); err != nil {
		cleanup()
		return fmt.Errorf("install archive: %w", err)
	}
	if err := os.Chmod(outputPath, 0644); err != nil {
		return fmt.Errorf("set archive permissions: %w", err)
	}
	return nil
}
