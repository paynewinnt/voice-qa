package main

import (
	"fmt"
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

const windowsUpdateScript = `param(
    [Parameter(Mandatory = $true)][string]$ZipPath,
    [Parameter(Mandatory = $true)][string]$InstallDir,
    [Parameter(Mandatory = $true)][string]$ExeName,
    [Parameter(Mandatory = $true)][int]$ParentProcessId
)

$ErrorActionPreference = 'Stop'
$stageDir = Join-Path $env:TEMP ('voice-qa-update-stage-' + [guid]::NewGuid().ToString('N'))
$installParent = Split-Path -Parent $InstallDir
$backupDir = Join-Path $installParent ('.voice-qa-update-backup-' + [guid]::NewGuid().ToString('N'))
$logDir = Join-Path $InstallDir 'output'
$logPath = Join-Path $logDir 'update-install.log'
$managedNames = New-Object 'System.Collections.Generic.List[string]'
$preservedNames = @('output', 'config.json', 'text.txt')
$exitCode = 0

function Write-UpdateLog([string]$Message) {
    try {
        New-Item -ItemType Directory -Path $logDir -Force | Out-Null
        Add-Content -LiteralPath $logPath -Value ('[{0}] {1}' -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $Message) -Encoding UTF8
    } catch {
    }
}

function Restore-Update {
    foreach ($name in @($managedNames)) {
        $target = Join-Path $InstallDir $name
        if (Test-Path -LiteralPath $target) {
            Remove-Item -LiteralPath $target -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
    if (Test-Path -LiteralPath $backupDir -PathType Container) {
        Get-ChildItem -LiteralPath $backupDir -Force | ForEach-Object {
            Move-Item -LiteralPath $_.FullName -Destination (Join-Path $InstallDir $_.Name) -Force
        }
        Remove-Item -LiteralPath $backupDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

try {
    Write-UpdateLog 'Waiting for the running application to exit.'
    $deadline = [DateTime]::UtcNow.AddSeconds(120)
    while ($null -ne (Get-Process -Id $ParentProcessId -ErrorAction SilentlyContinue)) {
        if ([DateTime]::UtcNow -ge $deadline) {
            throw 'Timed out waiting for the running application to exit.'
        }
        Start-Sleep -Milliseconds 300
    }

    $installPrefix = [IO.Path]::GetFullPath($InstallDir).TrimEnd('\') + '\'
    Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
        $_.ExecutablePath -and $_.ExecutablePath.StartsWith($installPrefix, [StringComparison]::OrdinalIgnoreCase)
    } | ForEach-Object {
        Write-UpdateLog ('Stopping residual process ' + $_.Name + ' (' + $_.ProcessId + ').')
        Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue
    }
    Start-Sleep -Milliseconds 500

    New-Item -ItemType Directory -Path $stageDir -Force | Out-Null
    Expand-Archive -LiteralPath $ZipPath -DestinationPath $stageDir -Force
    $roots = @(Get-ChildItem -LiteralPath $stageDir -Force)
    if ($roots.Count -ne 1 -or -not $roots[0].PSIsContainer) {
        throw 'The update archive must contain exactly one top-level directory.'
    }

    $sourceRoot = $roots[0].FullName
    $sourceExe = Join-Path $sourceRoot $ExeName
    if (-not (Test-Path -LiteralPath $sourceExe -PathType Leaf)) {
        $rootExecutables = @(Get-ChildItem -LiteralPath $sourceRoot -File -Filter '*.exe')
        if ($rootExecutables.Count -ne 1) {
            throw ('The update archive does not contain ' + $ExeName + ' or exactly one root executable.')
        }
        $sourceExe = $rootExecutables[0].FullName
    }

    New-Item -ItemType Directory -Path $backupDir -Force | Out-Null
    foreach ($item in Get-ChildItem -LiteralPath $sourceRoot -Force) {
        $targetName = $item.Name
        if ($item.FullName.Equals($sourceExe, [StringComparison]::OrdinalIgnoreCase)) {
            $targetName = $ExeName
        }
        $target = Join-Path $InstallDir $targetName
        if ($preservedNames -contains $targetName) {
            if (-not (Test-Path -LiteralPath $target)) {
                Copy-Item -LiteralPath $item.FullName -Destination $target -Recurse -Force
            }
            continue
        }

        $managedNames.Add($targetName)
        if (Test-Path -LiteralPath $target) {
            Move-Item -LiteralPath $target -Destination (Join-Path $backupDir $targetName) -Force
        }
        Copy-Item -LiteralPath $item.FullName -Destination $target -Recurse -Force
    }

    $installedExe = Join-Path $InstallDir $ExeName
    if (-not (Test-Path -LiteralPath $installedExe -PathType Leaf)) {
        throw 'The updated executable was not installed.'
    }

    $newProcess = Start-Process -FilePath $installedExe -WorkingDirectory $InstallDir -PassThru
    Start-Sleep -Milliseconds 800
    if ($newProcess.HasExited) {
        throw 'The updated application exited immediately after launch.'
    }

    Remove-Item -LiteralPath $backupDir -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $ZipPath -Force -ErrorAction SilentlyContinue
    Write-UpdateLog 'Update installed successfully.'
} catch {
    $exitCode = 1
    Write-UpdateLog ('Update failed: ' + $_.Exception.Message)
    try {
        Restore-Update
        $restoredExe = Join-Path $InstallDir $ExeName
        if (Test-Path -LiteralPath $restoredExe -PathType Leaf) {
            Start-Process -FilePath $restoredExe -WorkingDirectory $InstallDir | Out-Null
        }
    } catch {
        Write-UpdateLog ('Rollback failed: ' + $_.Exception.Message)
    }
} finally {
    Remove-Item -LiteralPath $stageDir -Recurse -Force -ErrorAction SilentlyContinue
}

Start-Sleep -Milliseconds 500
Remove-Item -LiteralPath $PSCommandPath -Force -ErrorAction SilentlyContinue
exit $exitCode
`

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

func writeWindowsUpdateScript() (string, error) {
	file, err := os.CreateTemp("", "voice-qa-updater-*.ps1")
	if err != nil {
		return "", fmt.Errorf("创建更新脚本失败: %w", err)
	}
	path := file.Name()
	if _, err := file.WriteString(windowsUpdateScript); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("写入更新脚本失败: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("关闭更新脚本失败: %w", err)
	}
	return path, nil
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

	scriptPath, err := writeWindowsUpdateScript()
	if err != nil {
		return UpdateInstallResult{Success: false, Message: err.Error()}
	}
	installDir := filepath.Dir(executablePath)
	cmd := exec.Command(
		"powershell.exe",
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-WindowStyle", "Hidden",
		"-File", scriptPath,
		"-ZipPath", trustedPath,
		"-InstallDir", installDir,
		"-ExeName", filepath.Base(executablePath),
		"-ParentProcessId", strconv.Itoa(os.Getpid()),
	)
	if err := cmd.Start(); err != nil {
		_ = os.Remove(scriptPath)
		return UpdateInstallResult{Success: false, Message: fmt.Sprintf("启动更新程序失败: %v", err)}
	}
	_ = cmd.Process.Release()
	a.setDownloadedUpdatePath("")

	ctx := a.ctx
	go func() {
		time.Sleep(500 * time.Millisecond)
		runtime.Quit(ctx)
	}()
	return UpdateInstallResult{Success: true, Message: "更新程序已启动，应用即将自动重启"}
}
