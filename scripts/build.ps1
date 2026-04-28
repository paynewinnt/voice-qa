param(
    [ValidateSet("gui")]
    [string]$Target = "gui",

    [string]$Version = (Get-Date -Format "yyyy.MMdd.HHmm"),

    [switch]$SkipClean,

    [switch]$SkipDownload,

    [switch]$InstallWails,

    [switch]$Help
)

$ErrorActionPreference = "Stop"

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectDir = Split-Path -Parent $ScriptDir
$DistDir = Join-Path $ProjectDir "dist"
$ToolRoot = Join-Path $ProjectDir ".build-tools"
$AppName = "tts"

function Decode-Utf8Base64([string]$Value) {
    return [System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String($Value))
}

$TextXiaoKao = Decode-Utf8Base64 "5bCP57+8566h5a62"
$TextBackHome = Decode-Utf8Base64 "6L+U5Zue6aaW6aG1"
$GuiExeName = Decode-Utf8Base64 "6K+t6Z+z5pKt5rWL5bel5YW3LmV4ZQ=="
$SampleText = Decode-Utf8Base64 "5oiR6KaB55yL55S16KeGCuaIkeimgeeci+S4reWkruS4ieWllwrkuIrkuKrlj7AK5LiL5Liq5Y+wCuaNouS4quWPsArmjaLkuKrpopHpgZMK5o2i6Z+z5LmQ5Y+wCuaNouWPsArlv6vpgIDkuIDlsI/ml7YK5ZCO6YCA5Y2B5YiG6ZKfCuW/q+i/m+S4ieWNgeenkgrov5Tlm57pppbpobUK5oiR6KaB55yL6YGN5Zyw5Lmm6aaZ56ys5LqU6ZuGCg=="

function Show-Usage {
    @"
Usage:
  powershell -ExecutionPolicy Bypass -File .\scripts\build.ps1 [-Target gui] [-Version yyyy.MMdd.HHmm] [-SkipClean] [-SkipDownload] [-InstallWails]

Examples:
  .\scripts\build.ps1
  .\scripts\build.ps1 -Target gui
  .\scripts\build.ps1 -Target gui -Version 2026.0422.1200
"@
}

if ($Help) {
    Show-Usage
    exit 0
}

function Write-Step([string]$Message) {
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Ensure-Directory([string]$Path) {
    New-Item -ItemType Directory -Force -Path $Path | Out-Null
}

function Write-Utf8File([string]$Path, [string]$Content, [bool]$WithBom = $true) {
    [System.IO.File]::WriteAllText($Path, $Content, [System.Text.UTF8Encoding]::new($WithBom))
}

function Copy-DirectoryContents([string]$SourceDir, [string]$DestinationDir) {
    if (-not (Test-Path $SourceDir)) {
        return
    }
    Ensure-Directory $DestinationDir
    Copy-Item -Path (Join-Path $SourceDir "*") -Destination $DestinationDir -Recurse -Force
}

function Copy-Glob([string]$Pattern, [string]$DestinationDir) {
    $items = Get-ChildItem -Path $Pattern -ErrorAction SilentlyContinue
    if ($null -eq $items) {
        return
    }
    Ensure-Directory $DestinationDir
    foreach ($item in $items) {
        Copy-Item -LiteralPath $item.FullName -Destination $DestinationDir -Force
    }
}

function Download-File([string]$Url, [string]$OutputPath) {
    Write-Step "Downloading $Url"
    Invoke-WebRequest -Uri $Url -OutFile $OutputPath
}

function Ensure-AdbWindows {
    $adbDir = Join-Path $ProjectDir "bin\adb-windows"
    $adbExe = Join-Path $adbDir "adb.exe"
    if (Test-Path $adbExe) {
        return
    }
    if ($SkipDownload) {
        throw "Missing $adbExe and -SkipDownload was specified."
    }

    Ensure-Directory $adbDir
    $zipPath = Join-Path $adbDir "platform-tools.zip"
    $extractDir = Join-Path $adbDir "platform-tools-extract"

    Download-File "https://dl.google.com/android/repository/platform-tools-latest-windows.zip" $zipPath
    if (Test-Path $extractDir) {
        Remove-Item -LiteralPath $extractDir -Recurse -Force
    }
    Expand-Archive -LiteralPath $zipPath -DestinationPath $extractDir -Force
    Copy-DirectoryContents (Join-Path $extractDir "platform-tools") $adbDir
    Remove-Item -LiteralPath $extractDir -Recurse -Force
    Remove-Item -LiteralPath $zipPath -Force
}

function Ensure-FfmpegWindows {
    $ffmpegDir = Join-Path $ProjectDir "bin\ffmpeg-windows"
    $ffmpegExe = Join-Path $ffmpegDir "ffmpeg.exe"
    if (Test-Path $ffmpegExe) {
        return
    }
    if ($SkipDownload) {
        throw "Missing $ffmpegExe and -SkipDownload was specified."
    }

    Ensure-Directory $ffmpegDir
    $zipPath = Join-Path $ffmpegDir "ffmpeg.zip"
    $extractDir = Join-Path $ffmpegDir "ffmpeg-extract"

    Download-File "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl.zip" $zipPath
    if (Test-Path $extractDir) {
        Remove-Item -LiteralPath $extractDir -Recurse -Force
    }
    Expand-Archive -LiteralPath $zipPath -DestinationPath $extractDir -Force

    $downloadedExe = Get-ChildItem -Path $extractDir -Recurse -Filter "ffmpeg.exe" | Select-Object -First 1
    if ($null -eq $downloadedExe) {
        throw "ffmpeg.exe was not found in the downloaded archive."
    }

    Copy-Item -LiteralPath $downloadedExe.FullName -Destination $ffmpegExe -Force
    Remove-Item -LiteralPath $extractDir -Recurse -Force
    Remove-Item -LiteralPath $zipPath -Force
}

function New-ReleaseConfig([string]$OutputDir) {
    $configPath = Join-Path $OutputDir "config.json"
    $config = @'
{
  "text_file": "text.txt",
  "output_dir": "output",
  "model_file": "",
  "voice_id": "edge:zh-CN-YunjianNeural",
  "template": [
    {"type": "silence", "seconds": 1},
    {"type": "voice", "text": "__XIAOKAO__"},
    {"type": "silence", "seconds": 2},
    {"type": "voice", "text": "$MAIN"},
    {"type": "silence", "seconds": 18},
    {"type": "voice", "text": "__XIAOKAO__"},
    {"type": "silence", "seconds": 2},
    {"type": "voice", "text": "__BACKHOME__"},
    {"type": "silence", "seconds": 5}
  ],
  "screenshot_before_end": 12,
  "enable_video_recording": false,
  "recording_start_delay": 5,
  "recording_end_before_end": 5,
  "filename_max_length": 30
}
'@
    $config = $config.Replace("__XIAOKAO__", $TextXiaoKao).Replace("__BACKHOME__", $TextBackHome)
    Write-Utf8File $configPath $config $true
}

function New-ReleaseText([string]$OutputDir) {
    $textPath = Join-Path $OutputDir "text.txt"
    Write-Utf8File $textPath $SampleText $true
}

function New-RunBat([string]$OutputDir) {
    @'
@echo off
cd /d "%~dp0"
tts.exe %*
pause
'@ | Set-Content -LiteralPath (Join-Path $OutputDir "run.bat") -Encoding ASCII
}

function New-UsageDoc([string]$OutputDir) {
    @'
================================================================================
                           Voice QA GUI Build
================================================================================

Files:
  GUI executable    desktop application in this folder
  config.json      config file
  text.txt         input text file
  ttsengine\       offline piper engine
  models\          voice models
  ffmpeg\          ffmpeg
  adb\             adb tools
  output\          generated output

Notes:
  Start the GUI by running the GUI executable in this folder.
  The package no longer includes a standalone command line release.
'@ | Set-Content -LiteralPath (Join-Path $OutputDir "USAGE.txt") -Encoding ASCII
}

function Compress-Release([string]$FolderPath, [string]$ArchivePath) {
    if (Test-Path $ArchivePath) {
        Remove-Item -LiteralPath $ArchivePath -Force
    }
    Compress-Archive -LiteralPath $FolderPath -DestinationPath $ArchivePath -CompressionLevel Optimal
}

function New-StagingOutputDir([string]$BaseName) {
    $stagingRoot = Join-Path $ToolRoot ("staging\" + $Version)
    $outputDir = Join-Path $stagingRoot $BaseName
    if (Test-Path $outputDir) {
        Remove-Item -LiteralPath $outputDir -Recurse -Force
    }
    Ensure-Directory $outputDir
    return $outputDir
}

function Try-SyncToDist([string]$SourceDir, [string]$DestinationDir) {
    try {
        if (Test-Path $DestinationDir) {
            Remove-Item -LiteralPath $DestinationDir -Recurse -Force
        }
        Ensure-Directory (Split-Path -Parent $DestinationDir)
        Copy-Item -LiteralPath $SourceDir -Destination $DestinationDir -Recurse -Force
    }
    catch {
        Write-Warning "Failed to update expanded dist directory: $DestinationDir"
        Write-Warning "The zip package was still generated successfully."
    }
}

function Clear-TargetOutputs {
    $paths = New-Object System.Collections.Generic.List[string]

    $paths.Add((Join-Path $DistDir "$AppName-windows-amd64"))
    $paths.Add((Join-Path $DistDir "$AppName-gui-windows-amd64"))
    Get-ChildItem -Path $DistDir -Filter "$AppName-windows-amd64-v*.zip" -ErrorAction SilentlyContinue | ForEach-Object {
        $paths.Add($_.FullName)
    }
    Get-ChildItem -Path $DistDir -Filter "$AppName-gui-windows-amd64-v*.zip" -ErrorAction SilentlyContinue | ForEach-Object {
        $paths.Add($_.FullName)
    }

    foreach ($path in $paths | Select-Object -Unique) {
        if (-not (Test-Path $path)) {
            continue
        }
        try {
            Remove-Item -LiteralPath $path -Recurse -Force
        }
        catch {
            throw "Failed to remove '$path'. Close any running executable using that path, or rerun with -SkipClean."
        }
    }
}

function Get-WailsCommand {
    $localWails = Join-Path $ToolRoot "gopath\bin\wails.exe"
    if (Test-Path $localWails) {
        return $localWails
    }

    $command = Get-Command "wails" -ErrorAction SilentlyContinue
    if ($command) {
        return $command.Source
    }

    $goPath = (go env GOPATH).Trim()
    if ($goPath) {
        $candidate = Join-Path $goPath "bin\wails.exe"
        if (Test-Path $candidate) {
            return $candidate
        }
    }

    if ($InstallWails) {
        Write-Step "Installing Wails CLI"
        $toolGoPath = Join-Path $ToolRoot "gopath"
        $toolGoCache = Join-Path $ToolRoot "gocache"
        Ensure-Directory $toolGoPath
        Ensure-Directory $toolGoCache

        $previousGoPath = $env:GOPATH
        $previousGoCache = $env:GOCACHE
        try {
            $env:GOPATH = $toolGoPath
            $env:GOCACHE = $toolGoCache
            go install github.com/wailsapp/wails/v2/cmd/wails@v2.11.0
        }
        finally {
            $env:GOPATH = $previousGoPath
            $env:GOCACHE = $previousGoCache
        }

        if (Test-Path $localWails) {
            return $localWails
        }

        $command = Get-Command "wails" -ErrorAction SilentlyContinue
        if ($command) {
            return $command.Source
        }
    }

    throw "Wails CLI not found. Install it with 'go install github.com/wailsapp/wails/v2/cmd/wails@v2.11.0' or rerun with -InstallWails."
}

function Build-WindowsCli {
    throw "The standalone Windows CLI release has been removed. Use -Target gui."
}

function Build-WindowsGui {
    Write-Step "Building windows GUI release"
    $folderName = "$AppName-gui-windows-amd64"
    $outputDir = New-StagingOutputDir $folderName
    Ensure-Directory $outputDir
    Ensure-Directory (Join-Path $outputDir "models")
    Ensure-Directory (Join-Path $outputDir "output")
    Ensure-Directory (Join-Path $outputDir "adb")
    Ensure-Directory (Join-Path $outputDir "ffmpeg")

    $wails = Get-WailsCommand
    Push-Location (Join-Path $ProjectDir "gui")
    try {
        & $wails build -platform windows/amd64 -o $GuiExeName -clean -ldflags "-X main.appVersion=$Version"
    }
    finally {
        Pop-Location
    }

    Copy-Item -LiteralPath (Join-Path $ProjectDir ("gui\build\bin\" + $GuiExeName)) -Destination (Join-Path $outputDir $GuiExeName) -Force

    Copy-DirectoryContents (Join-Path $ProjectDir "bin\piper-windows\piper") (Join-Path $outputDir "ttsengine")

    Ensure-AdbWindows
    Copy-Item -LiteralPath (Join-Path $ProjectDir "bin\adb-windows\adb.exe") -Destination (Join-Path $outputDir "adb\adb.exe") -Force
    Copy-Item -LiteralPath (Join-Path $ProjectDir "bin\adb-windows\AdbWinApi.dll") -Destination (Join-Path $outputDir "adb\AdbWinApi.dll") -Force
    Copy-Item -LiteralPath (Join-Path $ProjectDir "bin\adb-windows\AdbWinUsbApi.dll") -Destination (Join-Path $outputDir "adb\AdbWinUsbApi.dll") -Force

    Ensure-FfmpegWindows
    Copy-Item -LiteralPath (Join-Path $ProjectDir "bin\ffmpeg-windows\ffmpeg.exe") -Destination (Join-Path $outputDir "ffmpeg\ffmpeg.exe") -Force

    Copy-Glob (Join-Path $ProjectDir "models\*.onnx") (Join-Path $outputDir "models")
    Copy-Glob (Join-Path $ProjectDir "models\*.json") (Join-Path $outputDir "models")

    New-ReleaseConfig $outputDir
    New-ReleaseText $outputDir
    New-UsageDoc $outputDir

    $archivePath = Join-Path $DistDir "$folderName-v$Version.zip"
    Compress-Release $outputDir $archivePath
    Try-SyncToDist $outputDir (Join-Path $DistDir $folderName)
    Write-Host "  -> $archivePath"
}

Push-Location $ProjectDir
try {
    if (-not $SkipClean) {
        Write-Step "Cleaning target outputs"
        Clear-TargetOutputs
    }
    Ensure-Directory $DistDir

    Write-Host "=== Building Text-to-Speech v$Version ===" -ForegroundColor Green

    Build-WindowsGui

    Write-Host ""
    Write-Host "=== Build Completed ===" -ForegroundColor Green
    Get-ChildItem -Path $DistDir -File -Filter "*.zip" | Select-Object Name, Length
}
finally {
    Pop-Location
}
