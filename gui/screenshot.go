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

	cmd := command(ctx, "-s", serial, "exec-out", "screencap", "-p")
	var stderr bytes.Buffer
	cmd.Stdout = file
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	hideConsoleWindow(cmd)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return "", fmt.Errorf("截取设备全屏失败: %w；%s", err, strings.TrimSpace(stderr.String()))
	}
	if err := validateScreenshotPNG(file); err != nil {
		return "", fmt.Errorf("设备返回的截图不是完整 PNG: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	closed = true
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("保存截图文件失败: %w", err)
	}
	return file.Name(), nil
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
