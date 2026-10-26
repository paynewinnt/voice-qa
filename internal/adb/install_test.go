package adb

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type installFake struct {
	t                      *testing.T
	states                 []string
	calls                  []string
	dir, remote            string
	pushFailures           int
	installOutput          string
	installErr, cleanupErr error
	cancelPush             context.CancelFunc
}

func (f *installFake) run(ctx context.Context, dir string, args ...string) (string, error) {
	f.t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > apkTransferTimeout {
		f.t.Fatal("ADB call lacks a bounded deadline")
	}
	joined := strings.Join(args, " ")
	for _, forbidden := range []string{"uninstall", "kill-server", "pm clear", "'clear'", "'install' '-d'"} {
		if strings.Contains(joined, forbidden) {
			f.t.Fatalf("destructive command: %s", joined)
		}
	}
	if args[0] == "disconnect" || args[0] == "connect" {
		if len(args) != 2 || args[1] != "192.0.2.1:5555" {
			f.t.Fatalf("wrong reconnect target: %q", args)
		}
		f.calls = append(f.calls, args[0])
		return "connected", nil
	}
	if len(args) < 3 || args[0] != "-s" {
		f.t.Fatalf("missing explicit target: %q", args)
	}
	switch args[2] {
	case "get-state":
		f.calls = append(f.calls, "state")
		state := "device"
		if len(f.states) > 0 {
			state = f.states[0]
			f.states = f.states[1:]
		}
		if state != "device" {
			return state, errors.New("exit status 1")
		}
		return state, nil
	case "wait-for-device":
		f.calls = append(f.calls, "wait")
		return "", nil
	case "push":
		f.calls = append(f.calls, "push")
		if len(args) != 5 || args[3] != "package.apk" {
			f.t.Fatalf("APK source path not isolated: %q", args)
		}
		f.dir, f.remote = dir, args[4]
		if !strings.HasPrefix(f.remote, "/data/local/tmp/voice-qa-apk-") || !strings.HasSuffix(f.remote, ".apk") {
			f.t.Fatalf("unsafe remote staging path: %s", f.remote)
		}
		content, err := os.ReadFile(filepath.Join(dir, args[3]))
		if err != nil || string(content) != "apk fixture" {
			f.t.Fatalf("bad staged APK: %s %v", content, err)
		}
		if f.cancelPush != nil {
			f.cancelPush()
			return "", context.Canceled
		}
		if f.pushFailures > 0 {
			f.pushFailures--
			return "adb: device offline", errors.New("exit status 1")
		}
		return "1 file pushed", nil
	case "shell":
		if reflect.DeepEqual(args[2:], ShellArgs("pm", "install", "-r", f.remote)) {
			f.calls = append(f.calls, "install")
			return f.installOutput, f.installErr
		}
		if reflect.DeepEqual(args[2:], ShellArgs("rm", "-f", f.remote)) {
			f.calls = append(f.calls, "cleanup")
			if ctx.Err() != nil {
				f.t.Fatal("cleanup inherited canceled context")
			}
			return "", f.cleanupErr
		}
	}
	f.t.Fatalf("unexpected command: %q", args)
	return "", errors.New("unexpected command")
}

func installFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "同版本 带空格_signed.apk")
	if err := os.WriteFile(path, []byte("apk fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertInstallCalls(t *testing.T, f *installFake, want []string) {
	t.Helper()
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls=%v want=%v", f.calls, want)
	}
	if f.dir != "" {
		if _, err := os.Stat(f.dir); !os.IsNotExist(err) {
			t.Fatalf("local staging not cleaned: %v", err)
		}
	}
}

func TestInstallAPKReplacesSameVersionWithoutUninstall(t *testing.T) {
	f := &installFake{t: t, installOutput: "Success\r\n"}
	source := installFixture(t)
	var stages []string
	message, err := installAPK(context.Background(), "192.0.2.1:5555", source, func(s string) { stages = append(stages, s) }, f.run)
	if err != nil || !strings.Contains(message, "保留数据") {
		t.Fatalf("install: %s %v", message, err)
	}
	assertInstallCalls(t, f, []string{"state", "push", "install", "cleanup"})
	if len(stages) != 4 {
		t.Fatalf("missing phase progress: %v", stages)
	}
	if data, err := os.ReadFile(source); err != nil || string(data) != "apk fixture" {
		t.Fatal("original APK modified")
	}
}

func TestInstallAPKReconnectsOnlyOfflineNetworkTarget(t *testing.T) {
	f := &installFake{t: t, states: []string{"offline", "offline", "device"}, installOutput: "Success"}
	_, err := installAPK(context.Background(), "192.0.2.1:5555", installFixture(t), nil, f.run)
	if err != nil {
		t.Fatal(err)
	}
	assertInstallCalls(t, f, []string{"state", "disconnect", "connect", "state", "wait", "state", "push", "install", "cleanup"})
}

func TestInstallAPKDoesNotReconnectUnauthorizedOrUSBDevice(t *testing.T) {
	for _, state := range []string{"unauthorized", "offline"} {
		t.Run(state, func(t *testing.T) {
			serial := "usb-device"
			if state == "unauthorized" {
				serial = "192.0.2.1:5555"
			}
			f := &installFake{t: t, states: []string{state}}
			if _, err := installAPK(context.Background(), serial, installFixture(t), nil, f.run); err == nil {
				t.Fatal("invalid device accepted")
			}
			assertInstallCalls(t, f, []string{"state"})
		})
	}
}

func TestInstallAPKRetriesInterruptedTransferOnce(t *testing.T) {
	for _, failures := range []int{1, 2} {
		f := &installFake{t: t, pushFailures: failures, installOutput: "Success"}
		_, err := installAPK(context.Background(), "192.0.2.1:5555", installFixture(t), nil, f.run)
		want := []string{"state", "push", "state", "push"}
		if failures == 1 {
			want = append(want, "install")
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil {
			t.Fatal("second transfer failure accepted")
		}
		assertInstallCalls(t, f, append(want, "cleanup"))
	}
}

func TestInstallAPKPreservesPackageManagerDiagnostics(t *testing.T) {
	for _, tc := range []struct{ output, hint string }{
		{"Failure [INSTALL_FAILED_UPDATE_INCOMPATIBLE: signatures do not match]", "签名"},
		{"Failure [INSTALL_FAILED_VERSION_DOWNGRADE]", "versionCode"},
		{"Failure [INSTALL_FAILED_INSUFFICIENT_STORAGE]", "存储空间"},
		{"Failure [INSTALL_FAILED_NO_MATCHING_ABIS]", "CPU 架构"},
		{"Failure [INSTALL_PARSE_FAILED_NO_CERTIFICATES]", "签名无效"},
		{"Failure [INSTALL_FAILED_USER_RESTRICTED]", "安装授权"},
		{"Failure [INSTALL_FAILED_ALREADY_EXISTS]", "已使用 -r"},
		{"vendor-specific install failure", "vendor-specific"},
	} {
		t.Run(tc.output, func(t *testing.T) {
			f := &installFake{t: t, installOutput: tc.output}
			_, err := installAPK(context.Background(), "192.0.2.1:5555", installFixture(t), nil, f.run)
			if err == nil || !strings.Contains(err.Error(), tc.hint) || !strings.Contains(err.Error(), tc.output) {
				t.Fatalf("lost diagnosis: %v", err)
			}
			assertInstallCalls(t, f, []string{"state", "push", "install", "cleanup"})
		})
	}
}

func TestInstallAPKDoesNotRetryUncertainInstall(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, errors.New("exit status 1")} {
		f := &installFake{t: t, installErr: err, installOutput: "error: closed; connection closed"}
		_, got := installAPK(context.Background(), "192.0.2.1:5555", installFixture(t), nil, f.run)
		if got == nil || !strings.Contains(got.Error(), "结果尚不确定") {
			t.Fatalf("uncertain install: %v", got)
		}
		assertInstallCalls(t, f, []string{"state", "push", "install", "cleanup"})
	}
}

func TestInstallAPKCancellationStillCleansStagingFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &installFake{t: t, cancelPush: cancel}
	_, err := installAPK(ctx, "192.0.2.1:5555", installFixture(t), nil, f.run)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	assertInstallCalls(t, f, []string{"state", "push", "cleanup"})
}

func TestInstallAPKCleanupFailureDoesNotHideSuccessfulInstall(t *testing.T) {
	f := &installFake{t: t, installOutput: "Success", cleanupErr: errors.New("offline")}
	message, err := installAPK(context.Background(), "192.0.2.1:5555", installFixture(t), nil, f.run)
	if err != nil || !strings.Contains(message, "安装成功") || !strings.Contains(message, "清理失败") {
		t.Fatalf("cleanup status: %s %v", message, err)
	}
}

func TestInstallAPKRequiresUnambiguousSuccess(t *testing.T) {
	for _, output := range []string{"", "no Success marker", "Failure [Success]", "Success\nFailure [INSTALL_FAILED_INVALID_APK]"} {
		if apkInstallSucceeded(output) {
			t.Fatalf("false success: %q", output)
		}
	}
}

func TestDeviceStateIgnoresDaemonStartupMessages(t *testing.T) {
	if got := apkDeviceState("* daemon not running; starting now at tcp:5037\n* daemon started successfully\ndevice\n"); got != "device" {
		t.Fatalf("state=%q", got)
	}
	if got := apkDeviceState("error: device 'fake' not found"); got != "" {
		t.Fatalf("false online state: %q", got)
	}
}

func TestRunAPKCommandUsesWorkingDirectoryAndHonorsDeadline(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	oldPath := adbPath
	adbPath = executable
	t.Cleanup(func() { adbPath = oldPath })
	t.Setenv("VOICE_QA_APK_COMMAND_HELPER", "1")
	dir := filepath.Join(t.TempDir(), "中文 临时目录")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.apk"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := runAPKCommand(ctx, dir, "-test.run=^TestAPKCommandHelper$", "--", "package.apk")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Contents string
		Args     []string
	}
	if err := json.Unmarshal([]byte(output), &data); err != nil {
		t.Fatal(err)
	}
	if data.Contents != "data" || !reflect.DeepEqual(data.Args, []string{"package.apk"}) {
		t.Fatalf("working directory/arguments not preserved: %s", output)
	}
	shortCtx, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	start := time.Now()
	_, err = runAPKCommand(shortCtx, dir, "-test.run=^TestAPKCommandHelper$", "--", "block")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout failed: %v (%s)", err, time.Since(start))
	}
}

func TestAPKCommandHelper(t *testing.T) {
	if os.Getenv("VOICE_QA_APK_COMMAND_HELPER") != "1" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 1 && args[0] == "block" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	content, err := os.ReadFile(args[0])
	if err != nil {
		os.Exit(2)
	}
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		Contents string
		Args     []string
	}{string(content), args})
	os.Exit(0)
}
