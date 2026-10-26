package adb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	apkConnectionTimeout = 15 * time.Second
	apkTransferTimeout   = 5 * time.Minute
	apkInstallTimeout    = 3 * time.Minute
)

type apkCommandRunner func(context.Context, string, ...string) (string, error)

// InstallAPK performs a data-preserving replacement without streaming the APK
// into Package Manager. No uninstall, data clearing, or downgrade is attempted.
func InstallAPK(ctx context.Context, serial, apkPath string, progress func(string)) (string, error) {
	return installAPK(ctx, serial, apkPath, progress, runAPKCommand)
}

func runAPKCommand(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := CommandContext(ctx, args...)
	// ADB only receives an ASCII basename; Windows handles the Unicode cwd.
	if !filepath.IsAbs(cmd.Path) {
		absolute, err := filepath.Abs(cmd.Path)
		if err != nil {
			return "", err
		}
		cmd.Path = absolute
	}
	cmd.Dir = dir
	cmd.WaitDelay = 2 * time.Second
	HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return strings.TrimSpace(string(output)), err
}

func installAPK(ctx context.Context, serial, apkPath string, progress func(string), runner apkCommandRunner) (message string, resultErr error) {
	serial = strings.TrimSpace(serial)
	if serial == "" {
		return "", fmt.Errorf("请先选择安装目标设备")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	emit := func(message string) {
		if progress != nil {
			progress(message)
		}
	}
	run := func(parent context.Context, timeout time.Duration, dir string, args ...string) (string, error) {
		commandCtx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()
		output, err := runner(commandCtx, dir, args...)
		if commandCtx.Err() != nil {
			err = commandCtx.Err()
		}
		return strings.TrimSpace(output), err
	}
	check := func() (string, error) { return run(ctx, apkConnectionTimeout, "", "-s", serial, "get-state") }
	ensureOnline := func() error {
		emit("检查设备连接（最多 15 秒）")
		state, err := check()
		if err == nil && apkDeviceState(state) == "device" {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if strings.Contains(strings.ToLower(state), "unauthorized") {
			return fmt.Errorf("设备未授权，请在设备上确认 USB/无线调试授权：%s", state)
		}
		if _, _, parseErr := net.SplitHostPort(serial); parseErr != nil {
			return apkStageError("设备未就绪，请检查 USB 连接并刷新设备列表", state, err)
		}
		emit("设备离线或不可用，正在重新连接当前网络设备")
		// Do not kill the shared ADB server or disconnect unrelated devices.
		disconnected, disconnectErr := run(ctx, apkConnectionTimeout, "", "disconnect", serial)
		if disconnectErr != nil {
			return apkStageError("断开失效连接失败", disconnected, disconnectErr)
		}
		connected, connectErr := run(ctx, apkConnectionTimeout, "", "connect", serial)
		if connectErr != nil {
			return apkStageError("重新连接设备失败", connected, connectErr)
		}
		state, err = check()
		if apkDeviceState(state) == "offline" || strings.Contains(state, "device offline") {
			emit("等待目标设备恢复在线（最多 15 秒）")
			waitOutput, waitErr := run(ctx, apkConnectionTimeout, "", "-s", serial, "wait-for-device")
			if waitErr != nil {
				return apkStageError("等待设备恢复在线失败", waitOutput, waitErr)
			}
			state, err = check()
		}
		if err != nil || apkDeviceState(state) != "device" {
			return apkStageError("设备仍未就绪；请检查网络、ADB 端口和调试授权", connected+"\n"+state, err)
		}
		return nil
	}
	if err := ensureOnline(); err != nil {
		return "", err
	}

	emit("准备 APK 临时副本，保留原始文件")
	dir, err := os.MkdirTemp("", "voice-qa-apk-")
	if err != nil {
		return "", fmt.Errorf("创建 APK 临时目录失败: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			emit(fmt.Sprintf("警告：本机临时目录清理失败 %s: %v", dir, err))
		}
	}()
	if err := copyInstallAPK(ctx, apkPath, filepath.Join(dir, "package.apk")); err != nil {
		return "", fmt.Errorf("准备 APK 失败: %w", err)
	}
	remote := "/data/local/tmp/" + filepath.Base(dir) + ".apk"
	defer func() {
		// Only clean this operation's staging file, even after cancellation.
		output, err := run(context.Background(), 10*time.Second, "", append([]string{"-s", serial}, ShellArgs("rm", "-f", remote)...)...)
		if err != nil {
			warning := fmt.Sprintf("设备临时 APK 清理失败，路径 %s：%s", remote, apkStageError("清理", output, err))
			emit("警告：" + warning)
			if resultErr == nil {
				message += "；" + warning
			}
		}
	}()

	for attempt := 0; attempt < 2; attempt++ {
		emit("正在传输 APK（最多 5 分钟）")
		output, pushErr := run(ctx, apkTransferTimeout, dir, "-s", serial, "push", "package.apk", remote)
		if pushErr == nil {
			break
		}
		if attempt == 0 && ctx.Err() == nil && apkTransportError(output) {
			emit("APK 传输连接中断，检查目标设备后仅重试传输一次")
			if err := ensureOnline(); err != nil {
				return "", fmt.Errorf("%v；%w", apkStageError("APK 传输失败", output, pushErr), err)
			}
			continue
		}
		return "", apkStageError("APK 传输失败", output, pushErr)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	emit("正在保留数据覆盖安装（pm install -r，最多 3 分钟）")
	output, installErr := run(ctx, apkInstallTimeout, "", append([]string{"-s", serial}, ShellArgs("pm", "install", "-r", remote)...)...)
	if installErr == nil && apkInstallSucceeded(output) {
		return "安装成功（保留数据覆盖安装，未卸载旧应用）", nil
	}
	if installErr == nil {
		installErr = fmt.Errorf("未收到 Package Manager 的 Success 标记")
	}
	detail := apkStageError("覆盖安装失败", output, installErr)
	hint := apkInstallHint(output)
	if errors.Is(installErr, context.DeadlineExceeded) || apkTransportError(output) {
		hint = "安装返回超时或连接中断，设备端安装结果尚不确定，请恢复连接并查询应用版本后再决定是否重试；未自动卸载或清理应用数据"
	}
	if hint != "" {
		return "", fmt.Errorf("%s\n%w", hint, detail)
	}
	return "", detail
}

func copyInstallAPK(ctx context.Context, source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.Create(target)
	if err != nil {
		return err
	}
	defer output.Close()
	buffer := make([]byte, 128*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := input.Read(buffer)
		if n > 0 {
			written, writeErr := output.Write(buffer[:n])
			if writeErr != nil {
				return writeErr
			}
			if written != n {
				return io.ErrShortWrite
			}
			total += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if total == 0 {
		return fmt.Errorf("APK 文件为空")
	}
	if err := output.Chmod(0644); err != nil {
		return err
	}
	return output.Close()
}

func apkDeviceState(output string) string {
	for _, line := range strings.Split(output, "\n") {
		switch state := strings.TrimSpace(line); state {
		case "device", "offline", "unauthorized", "recovery", "bootloader", "sideload":
			return state
		}
	}
	return ""
}

func apkStageError(stage, output string, err error) error {
	if strings.TrimSpace(output) == "" {
		output = "ADB 未返回详细输出"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s：操作超时；%s: %w", stage, output, err)
	}
	if err != nil {
		return fmt.Errorf("%s：%s: %w", stage, output, err)
	}
	return fmt.Errorf("%s：%s", stage, output)
}

func apkTransportError(output string) bool {
	output = strings.ToLower(output)
	if strings.Contains(output, "device '") && strings.Contains(output, "' not found") {
		return true
	}
	for _, marker := range []string{"device offline", "device not found", "no devices/emulators found", "transport error", "connection reset", "connection closed", "broken pipe", "protocol fault", "failed to read", "failed to write"} {
		if strings.Contains(output, marker) {
			return true
		}
	}
	return false
}

func apkInstallSucceeded(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "Failure [") || strings.Contains(line, "INSTALL_FAILED_") || strings.Contains(line, "INSTALL_PARSE_FAILED_") {
			return false
		}
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "Success" {
			return true
		}
	}
	return false
}

func apkInstallHint(output string) string {
	switch {
	case strings.Contains(output, "UPDATE_INCOMPATIBLE"), strings.Contains(output, "INCONSISTENT_CERTIFICATES"), strings.Contains(output, "SHARED_USER_INCOMPATIBLE"):
		return "新旧 APK 签名或共享用户不兼容，无法保留数据覆盖；请使用与已安装应用相同签名的 APK，工具不会自动卸载"
	case strings.Contains(output, "VERSION_DOWNGRADE"):
		return "新 APK 的 versionCode 低于已安装版本，即使显示的版本名称相同也会被拒绝；请提供 versionCode 不低于已安装版本的同签名 APK，未自动降级或卸载"
	case strings.Contains(output, "INSUFFICIENT_STORAGE"):
		return "设备存储空间不足，请先释放空间后重试；未清理应用数据"
	case strings.Contains(output, "NO_MATCHING_ABIS"), strings.Contains(output, "CPU_ABI_INCOMPATIBLE"):
		return "APK 的 CPU 架构与设备不兼容，请使用适配此设备的安装包"
	case strings.Contains(output, "OLDER_SDK"):
		return "设备 Android 版本低于 APK 的最低要求，请使用兼容安装包"
	case strings.Contains(output, "USER_RESTRICTED"), strings.Contains(output, "INSTALL_FAILED_ABORTED"):
		return "设备系统策略或用户确认阻止了安装，请检查设备上的安装授权提示"
	case strings.Contains(output, "INSTALL_PARSE_FAILED_"), strings.Contains(output, "INSTALL_FAILED_INVALID_APK"):
		return "APK 无法解析或签名无效，请确认安装包完整且已正确签名"
	case strings.Contains(output, "INSTALL_FAILED_ALREADY_EXISTS"):
		return "设备拒绝了保留数据覆盖安装（已使用 -r），请检查安装策略及原始错误；未自动卸载"
	}
	return ""
}
