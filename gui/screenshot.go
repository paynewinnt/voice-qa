package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"voice-qa/internal/adb"
)

type ScreenshotResult struct {
	Success         bool   `json:"success"`
	Message         string `json:"message"`
	Path            string `json:"path"`
	Serial          string `json:"serial"`
	ClipboardCopied bool   `json:"clipboardCopied"`
}

// CaptureDeviceScreenshot saves the selected device's full display in the output directory.
func (a *App) CaptureDeviceScreenshot(serial string) ScreenshotResult {
	return a.captureDeviceScreenshot(serial, a.resolveAdbDevice, func(ctx context.Context, serial, dir string) (string, error) {
		return saveDeviceScreenshot(ctx, serial, dir, adb.CommandContext)
	}, copyScreenshotToClipboard)
}

func (a *App) captureDeviceScreenshot(serial string, resolve func(string) (string, error), capture func(context.Context, string, string) (string, error), copyImage func(context.Context, string) error) ScreenshotResult {
	a.cmdMu.Lock()
	if a.screenshotCancel != nil {
		a.cmdMu.Unlock()
		return ScreenshotResult{Message: "正在截图，请稍候"}
	}
	// Keep screenshots on the recording target even if the device list refreshes.
	if a.manualRecorder != nil && a.manualRecordSerial != "" {
		serial = a.manualRecordSerial
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	a.screenshotCancel = cancel
	a.cmdMu.Unlock()
	defer func() {
		cancel()
		a.cmdMu.Lock()
		a.screenshotCancel = nil
		a.cmdMu.Unlock()
	}()
	if err := ctx.Err(); err != nil {
		return ScreenshotResult{Message: err.Error()}
	}
	resolved, err := resolve(strings.TrimSpace(serial))
	if err != nil {
		return ScreenshotResult{Message: err.Error()}
	}
	path, err := capture(ctx, resolved, a.resolvedOutputDir())
	if err != nil {
		return ScreenshotResult{Message: err.Error(), Serial: resolved}
	}
	result := ScreenshotResult{Success: true, Path: path, Serial: resolved}
	if err := copyImage(ctx, path); err != nil {
		result.Message = "截图已保存，复制失败：" + err.Error() + "；" + path
	} else {
		result.ClipboardCopied = true
		result.Message = "截图已保存并复制：" + path
	}
	return result
}

func saveDeviceScreenshot(ctx context.Context, serial, dir string, command func(context.Context, ...string) *exec.Cmd) (savedPath string, resultErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(serial) == "" {
		return "", fmt.Errorf("请先选择截图目标设备")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("创建截图目录失败: %w", err)
	}
	file, err := os.CreateTemp(dir, "screenshot-"+time.Now().Format("20060102-150405")+"-*.png")
	if err != nil {
		return "", fmt.Errorf("创建截图文件失败: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			if err := file.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("关闭截图文件失败: %w", err))
			}
		}
		if savedPath == "" {
			if err := os.Remove(file.Name()); err != nil && !os.IsNotExist(err) {
				resultErr = errors.Join(resultErr, fmt.Errorf("清理未完成截图失败 %s: %w", file.Name(), err))
			}
		}
	}()

	var attempts []error
	for _, args := range [][]string{
		{"-s", serial, "exec-out", "screencap", "-p"},
		{"-s", serial, "shell", "-T", "screencap", "-p"},
	} {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := file.Truncate(0); err != nil {
			return "", fmt.Errorf("重置截图文件失败: %w", err)
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return "", fmt.Errorf("定位截图文件失败: %w", err)
		}
		cmd := command(ctx, args...)
		var stderr bytes.Buffer
		cmd.Stdout = file
		cmd.Stderr = &stderr
		cmd.WaitDelay = 2 * time.Second
		hideConsoleWindow(cmd)
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			attempts = append(attempts, fmt.Errorf("截取设备全屏失败 (%s): %w；stderr=%q", strings.Join(args[2:], " "), err, stderr.Bytes()[:min(stderr.Len(), 160)]))
			return "", errors.Join(attempts...)
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return "", fmt.Errorf("读取截图文件失败: %w", err)
		}
		const maxBytes = 256 * 1024 * 1024
		data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
		if err != nil {
			return "", fmt.Errorf("读取截图文件失败: %w", err)
		}
		if len(data) > maxBytes {
			return "", fmt.Errorf("截图数据超过 256 MB，已停止处理")
		}
		clean, err := normalizeScreenshotPNG(data)
		if err != nil {
			attempts = append(attempts, fmt.Errorf("设备返回的截图不是完整 PNG (%s, %d 字节): %w；stdout 前缀=%q；stderr=%q", strings.Join(args[2:], " "), len(data), err, data[:min(len(data), 96)], stderr.Bytes()[:min(stderr.Len(), 160)]))
			continue
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !bytes.Equal(clean, data) {
			if err := file.Truncate(0); err != nil {
				return "", fmt.Errorf("重写截图文件失败: %w", err)
			}
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				return "", fmt.Errorf("定位截图文件失败: %w", err)
			}
			if _, err := file.Write(clean); err != nil {
				return "", fmt.Errorf("保存截图文件失败: %w", err)
			}
		}
		closed = true
		if err := file.Close(); err != nil {
			return "", fmt.Errorf("保存截图文件失败: %w", err)
		}
		return file.Name(), nil
	}
	return "", errors.Join(attempts...)
}

func normalizeScreenshotPNG(data []byte) ([]byte, error) {
	start := bytes.Index(data, []byte("\x89PNG\r\n\x1a\n"))
	translated := bytes.Index(data, []byte("\x89PNG\r\r\n\x1a\r\n"))
	// Only undo terminal newline translation when its signature is present.
	if translated >= 0 && (start < 0 || translated < start) {
		data = bytes.ReplaceAll(data[translated:], []byte("\r\n"), []byte("\n"))
		start = 0
	}
	if start < 0 {
		return nil, fmt.Errorf("未找到 PNG 文件头")
	}
	reader := bytes.NewReader(data[start:])
	if err := validateScreenshotPNG(reader); err != nil {
		return nil, err
	}
	// Decode consumes through IEND, leaving any trailing device diagnostics out.
	return data[start : len(data)-reader.Len()], nil
}

func validateScreenshotPNG(file io.ReadSeeker) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	info, err := png.DecodeConfig(file)
	if err != nil {
		return err
	}
	// Bound decoder memory before validating all PNG chunks, including the image data.
	if info.Width > 16384 || info.Height > 16384 || int64(info.Width)*int64(info.Height) > 64*1024*1024 {
		return fmt.Errorf("截图尺寸异常: %d x %d", info.Width, info.Height)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = png.Decode(file)
	return err
}
