package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"voice-qa/internal/adb"
	"voice-qa/internal/config"
)

func screenshotFixture(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 4, 3))
	img.Set(1, 1, color.NRGBA{R: 32, G: 96, B: 160, A: 255})
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func screenshotCommand(t *testing.T, data []byte, mode string) func(context.Context, ...string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, args ...string) *exec.Cmd {
		t.Helper()
		if !reflect.DeepEqual(args, []string{"-s", "192.0.2.11:5555", "exec-out", "screencap", "-p"}) {
			t.Fatalf("unexpected screenshot command: %q", args)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 30*time.Second {
			t.Fatal("screenshot has no bounded deadline")
		}
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestScreenshotCommandHelper$")
		cmd.Env = append(os.Environ(), "VOICE_QA_SCREENSHOT_HELPER="+mode, "VOICE_QA_SCREENSHOT_DATA="+base64.StdEncoding.EncodeToString(data))
		return cmd
	}
}

func TestScreenshotCommandHelper(t *testing.T) {
	mode := os.Getenv("VOICE_QA_SCREENSHOT_HELPER")
	if mode == "" {
		return
	}
	if mode == "block" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	data, err := base64.StdEncoding.DecodeString(os.Getenv("VOICE_QA_SCREENSHOT_DATA"))
	if err != nil {
		os.Exit(2)
	}
	_, _ = os.Stdout.Write(data)
	if mode == "error" {
		_, _ = os.Stderr.WriteString("adb: device offline")
		os.Exit(1)
	}
	_, _ = os.Stderr.WriteString("daemon diagnostic must not corrupt PNG data")
	os.Exit(0)
}

func TestSaveDeviceScreenshotPreservesFullPNGAndUsesUniqueNames(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "中文 输出")
	data := screenshotFixture(t)
	var previous string
	for range 2 {
		path, err := saveDeviceScreenshot(ctx, "192.0.2.11:5555", dir, screenshotCommand(t, data, "ok"))
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(path) != dir || filepath.Ext(path) != ".png" || !strings.HasPrefix(filepath.Base(path), "screenshot-") || path == previous {
			t.Fatalf("unexpected path: %s", path)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("PNG changed or unreadable: %v", err)
		}
		previous = path
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 2 {
		t.Fatalf("saved files: %d %v", len(files), err)
	}
}

func TestSaveDeviceScreenshotFailureRemovesIncompleteFile(t *testing.T) {
	data := screenshotFixture(t)
	for _, tc := range []struct {
		name, mode, message string
		data                []byte
	}{
		{name: "empty", mode: "ok", message: "完整 PNG"},
		{name: "text", mode: "ok", message: "完整 PNG", data: []byte("not a screenshot")},
		{name: "truncated", mode: "ok", message: "完整 PNG", data: data[:len(data)-12]},
		{name: "command error", mode: "error", message: "device offline", data: data},
		{name: "timeout", mode: "block", message: "deadline exceeded", data: data},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			previous := filepath.Join(dir, "previous.png")
			if err := os.WriteFile(previous, data, 0600); err != nil {
				t.Fatal(err)
			}
			timeout := 5 * time.Second
			if tc.mode == "block" {
				timeout = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			path, err := saveDeviceScreenshot(ctx, "192.0.2.11:5555", dir, screenshotCommand(t, tc.data, tc.mode))
			if path != "" || err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("unexpected result: %s %v", path, err)
			}
			if tc.mode == "block" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost deadline error: %v", err)
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 1 || files[0].Name() != "previous.png" {
				t.Fatalf("incomplete file left behind: %v %v", files, err)
			}
			got, err := os.ReadFile(previous)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatal("previous screenshot modified")
			}
		})
	}
}

func TestSaveDeviceScreenshotRejectsCanceledAndUnwritableTarget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := filepath.Join(t.TempDir(), "output")
	if _, err := saveDeviceScreenshot(ctx, "device", dir, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("canceled capture created a directory")
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := saveDeviceScreenshot(context.Background(), "device", dir, nil); err == nil {
		t.Fatal("unwritable directory accepted")
	}
}

func TestValidateScreenshotRejectsOversizedPNG(t *testing.T) {
	data := screenshotFixture(t)
	binary.BigEndian.PutUint32(data[16:20], 32768)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	if err := validateScreenshotPNG(bytes.NewReader(data)); err == nil || !strings.Contains(err.Error(), "尺寸异常") {
		t.Fatalf("unbounded PNG decode: %v", err)
	}
}

func TestCaptureDeviceScreenshotResolvesDeviceAndConfiguredOutput(t *testing.T) {
	for _, tc := range []struct {
		name, selected string
		devices        []string
		ok             bool
	}{
		{name: "unique", devices: []string{"device-a"}, ok: true},
		{name: "explicit", selected: " device-b ", devices: []string{"device-a", "device-b"}, ok: true},
		{name: "multiple", devices: []string{"device-a", "device-b"}},
		{name: "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := NewApp()
			app.cfg = config.DefaultConfig()
			app.cfg.OutputDir = filepath.Join(t.TempDir(), "自定义输出")
			called := false
			result := app.captureDeviceScreenshot(tc.selected, func(serial string) (string, error) {
				if serial != "" {
					return serial, nil
				}
				return resolveAdbDeviceFromList(tc.devices)
			}, func(ctx context.Context, serial, dir string) (string, error) {
				called = true
				want := tc.devices[0]
				if tc.selected != "" {
					want = strings.TrimSpace(tc.selected)
				}
				if serial != want || dir != app.cfg.OutputDir {
					t.Fatalf("wrong screenshot target: %s %s", serial, dir)
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 30*time.Second {
					t.Fatal("missing deadline")
				}
				return filepath.Join(dir, "screenshot.png"), nil
			}, func(context.Context, string) error { return nil })
			if result.Success != tc.ok || called != tc.ok || app.screenshotCancel != nil {
				t.Fatalf("bad result: %+v called=%v", result, called)
			}
			if tc.ok && (result.Path == "" || !strings.Contains(result.Message, result.Path) || !result.ClipboardCopied) {
				t.Fatal("saved path missing")
			}
		})
	}
}

func TestCaptureDeviceScreenshotKeepsRecordingTargetAndBlocksDuplicates(t *testing.T) {
	app := NewApp()
	app.cfg = config.DefaultConfig()
	app.manualRecorder = &adb.VideoRecorder{}
	app.manualRecordSerial = "recording-device"
	started := make(chan struct{})
	finished := make(chan ScreenshotResult, 1)
	resolve := func(serial string) (string, error) { return serial, nil }
	go func() {
		finished <- app.captureDeviceScreenshot("other-device", resolve, func(ctx context.Context, serial, dir string) (string, error) {
			if serial != "recording-device" {
				t.Errorf("recording target changed: %s", serial)
			}
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		}, nil)
	}()
	<-started
	duplicate := app.captureDeviceScreenshot("other-device", resolve, nil, nil)
	if duplicate.Success || !strings.Contains(duplicate.Message, "正在截图") {
		t.Fatalf("duplicate allowed: %+v", duplicate)
	}
	app.cmdMu.Lock()
	cancel := app.screenshotCancel
	app.cmdMu.Unlock()
	cancel()
	result := <-finished
	if result.Success || !strings.Contains(result.Message, "canceled") || app.screenshotCancel != nil {
		t.Fatalf("cancel did not release capture: %+v", result)
	}
	if app.manualRecorder == nil || app.manualRecordSerial != "recording-device" {
		t.Fatal("screenshot changed recording state")
	}
}

func TestScreenshotClipboardFailurePreservesSavedPNG(t *testing.T) {
	app := NewApp()
	app.cfg = config.DefaultConfig()
	app.cfg.OutputDir = t.TempDir()
	data := screenshotFixture(t)
	result := app.captureDeviceScreenshot("device", func(serial string) (string, error) { return serial, nil },
		func(ctx context.Context, serial, dir string) (string, error) {
			path := filepath.Join(dir, "screenshot.png")
			return path, os.WriteFile(path, data, 0600)
		}, func(ctx context.Context, path string) error {
			if _, err := os.Stat(path); err != nil {
				t.Fatal("clipboard called before saving the PNG")
			}
			return errors.New("clipboard busy")
		})
	if !result.Success || result.ClipboardCopied || !strings.Contains(result.Message, "复制失败") || !strings.Contains(result.Message, "clipboard busy") {
		t.Fatalf("wrong partial success: %+v", result)
	}
	got, err := os.ReadFile(result.Path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("saved PNG was lost after clipboard failure")
	}
}

func TestScreenshotDoesNotCopyFailedCapture(t *testing.T) {
	app := NewApp()
	result := app.captureDeviceScreenshot("device", func(serial string) (string, error) { return serial, nil },
		func(context.Context, string, string) (string, error) { return "", errors.New("offline") },
		func(context.Context, string) error { t.Error("copied failed screenshot"); return nil })
	if result.Success || result.ClipboardCopied {
		t.Fatalf("capture failure accepted: %+v", result)
	}
}
