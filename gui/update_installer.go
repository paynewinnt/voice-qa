package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type UpdateInstallResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

func (a *App) setDownloadedUpdatePath(path string) {
	a.updateMu.Lock()
	a.downloadedUpdatePath = path
	a.updateMu.Unlock()
}

func (a *App) trustedDownloadedUpdatePath() string {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	return a.downloadedUpdatePath
}

func replaceUpdateArchive(sourcePath, targetPath string) error {
	if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除旧更新包失败: %w", err)
	}
	return os.Rename(sourcePath, targetPath)
}

func normalizeExistingUpdatePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("更新包路径为空")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("解析更新包路径失败: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("读取更新包失败: %w", err)
	}
	if !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(absPath), ".zip") {
		return "", fmt.Errorf("更新包必须是 zip 文件")
	}
	return filepath.Clean(absPath), nil
}

func validateTrustedUpdatePath(requestedPath, trustedPath string) (string, error) {
	requested, err := normalizeExistingUpdatePath(requestedPath)
	if err != nil {
		return "", err
	}
	trusted, err := normalizeExistingUpdatePath(trustedPath)
	if err != nil {
		return "", fmt.Errorf("没有可安装的已校验更新包")
	}
	pathsMatch := requested == trusted
	if goruntime.GOOS == "windows" {
		pathsMatch = strings.EqualFold(requested, trusted)
	}
	if !pathsMatch {
		return "", fmt.Errorf("更新包不是本次下载并校验的文件")
	}
	return requested, nil
}

func ensureInstallDirectoryWritable(installDir string) error {
	file, err := os.CreateTemp(installDir, ".voice-qa-update-write-check-*")
	if err != nil {
		return fmt.Errorf("程序目录不可写，请将程序移到普通目录后重试: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("检查程序目录写权限失败: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("检查程序目录写权限失败: %w", err)
	}
	return nil
}

func createNativeUpdaterCopy(executablePath string) (string, error) {
	file, err := os.CreateTemp("", nativeUpdaterFilePrefix+"*.exe")
	if err != nil {
		return "", fmt.Errorf("创建原生更新程序失败: %w", err)
	}
	updaterPath := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(updaterPath)
		return "", fmt.Errorf("创建原生更新程序失败: %w", err)
	}

	source, err := os.Open(executablePath)
	if err != nil {
		_ = os.Remove(updaterPath)
		return "", fmt.Errorf("读取当前程序失败: %w", err)
	}
	destination, err := os.OpenFile(updaterPath, os.O_WRONLY|os.O_TRUNC, 0700)
	if err != nil {
		_ = source.Close()
		_ = os.Remove(updaterPath)
		return "", fmt.Errorf("写入原生更新程序失败: %w", err)
	}
	_, copyErr := io.Copy(destination, source)
	sourceCloseErr := source.Close()
	destinationCloseErr := destination.Close()
	if copyErr != nil {
		_ = os.Remove(updaterPath)
		return "", fmt.Errorf("复制原生更新程序失败: %w", copyErr)
	}
	if sourceCloseErr != nil {
		_ = os.Remove(updaterPath)
		return "", fmt.Errorf("关闭当前程序文件失败: %w", sourceCloseErr)
	}
	if destinationCloseErr != nil {
		_ = os.Remove(updaterPath)
		return "", fmt.Errorf("关闭原生更新程序失败: %w", destinationCloseErr)
	}
	return updaterPath, nil
}

func (a *App) InstallUpdateAndRestart(updatePath string) UpdateInstallResult {
	if goruntime.GOOS != "windows" {
		return UpdateInstallResult{Success: false, Message: "自动安装更新仅支持 Windows"}
	}
	if a.ctx == nil {
		return UpdateInstallResult{Success: false, Message: "应用尚未完成初始化"}
	}

	trustedPath, err := validateTrustedUpdatePath(updatePath, a.trustedDownloadedUpdatePath())
	if err != nil {
		return UpdateInstallResult{Success: false, Message: err.Error()}
	}
	executablePath, err := os.Executable()
	if err != nil {
		return UpdateInstallResult{Success: false, Message: fmt.Sprintf("获取程序路径失败: %v", err)}
	}
	executablePath, err = filepath.Abs(executablePath)
	if err != nil {
		return UpdateInstallResult{Success: false, Message: fmt.Sprintf("解析程序路径失败: %v", err)}
	}
	installDir := filepath.Dir(executablePath)
	if err := ensureInstallDirectoryWritable(installDir); err != nil {
		return UpdateInstallResult{Success: false, Message: err.Error()}
	}

	updaterPath, err := createNativeUpdaterCopy(executablePath)
	if err != nil {
		return UpdateInstallResult{Success: false, Message: err.Error()}
	}
	cmd := exec.Command(
		updaterPath,
		nativeUpdateModeFlag,
		"-zip", trustedPath,
		"-install-dir", installDir,
		"-exe-name", filepath.Base(executablePath),
		"-parent-pid", strconv.Itoa(os.Getpid()),
	)
	cmd.Dir = installDir
	hideConsoleWindow(cmd)
	if err := cmd.Start(); err != nil {
		_ = os.Remove(updaterPath)
		return UpdateInstallResult{Success: false, Message: fmt.Sprintf("启动原生更新程序失败: %v", err)}
	}
	_ = cmd.Process.Release()
	a.setDownloadedUpdatePath("")

	ctx := a.ctx
	go func() {
		time.Sleep(500 * time.Millisecond)
		runtime.Quit(ctx)
	}()
	return UpdateInstallResult{Success: true, Message: "原生更新程序已启动，应用即将自动重启"}
}
