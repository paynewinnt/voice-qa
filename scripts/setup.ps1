# Text-to-Speech Windows 安装脚本
# 下载 Piper TTS 引擎

$ErrorActionPreference = "Stop"

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectDir = Split-Path -Parent $ScriptDir

# 如果是从 dist 目录运行，则 ProjectDir 就是当前目录
if (Test-Path "$ScriptDir\tts.exe") {
    $ProjectDir = $ScriptDir
}

$PiperDir = Join-Path $ProjectDir "piper"
$ModelsDir = Join-Path $ProjectDir "models"

# Piper 版本和下载地址
$PiperVersion = "2023.11.14-2"
$PiperUrl = "https://github.com/rhasspy/piper/releases/download/$PiperVersion/piper_windows_amd64.zip"

# 中文模型
$ModelName = "zh_CN-huayan-medium"
$ModelUrl = "https://huggingface.co/rhasspy/piper-voices/resolve/main/zh/zh_CN/huayan/medium/zh_CN-huayan-medium.onnx"
$ConfigUrl = "https://huggingface.co/rhasspy/piper-voices/resolve/main/zh/zh_CN/huayan/medium/zh_CN-huayan-medium.onnx.json"

Write-Host "=== Text-to-Speech Windows 安装程序 ===" -ForegroundColor Cyan
Write-Host ""

# 创建目录
if (-not (Test-Path $PiperDir)) {
    New-Item -ItemType Directory -Path $PiperDir | Out-Null
}
if (-not (Test-Path $ModelsDir)) {
    New-Item -ItemType Directory -Path $ModelsDir | Out-Null
}

# 下载 Piper
$PiperExe = Join-Path $PiperDir "piper.exe"
if (-not (Test-Path $PiperExe)) {
    Write-Host "[1/3] 下载 Piper TTS 引擎..." -ForegroundColor Yellow
    $TempZip = Join-Path $env:TEMP "piper_windows.zip"

    try {
        # 使用 .NET WebClient 下载（支持重定向）
        $webClient = New-Object System.Net.WebClient
        $webClient.DownloadFile($PiperUrl, $TempZip)

        Write-Host "      解压中..." -ForegroundColor Gray
        Expand-Archive -Path $TempZip -DestinationPath $ProjectDir -Force
        Remove-Item $TempZip -Force

        Write-Host "      Piper 下载完成" -ForegroundColor Green
    }
    catch {
        Write-Host "      下载失败: $_" -ForegroundColor Red
        Write-Host "      请手动下载: $PiperUrl" -ForegroundColor Yellow
        exit 1
    }
}
else {
    Write-Host "[1/3] Piper 已存在，跳过" -ForegroundColor Gray
}

# 下载模型
$ModelFile = Join-Path $ModelsDir "$ModelName.onnx"
if (-not (Test-Path $ModelFile)) {
    Write-Host "[2/3] 下载中文语音模型..." -ForegroundColor Yellow

    try {
        $webClient = New-Object System.Net.WebClient
        $webClient.DownloadFile($ModelUrl, $ModelFile)
        Write-Host "      模型下载完成" -ForegroundColor Green
    }
    catch {
        Write-Host "      下载失败: $_" -ForegroundColor Red
        exit 1
    }
}
else {
    Write-Host "[2/3] 模型已存在，跳过" -ForegroundColor Gray
}

# 下载模型配置
$ConfigFile = Join-Path $ModelsDir "$ModelName.onnx.json"
if (-not (Test-Path $ConfigFile)) {
    Write-Host "[3/3] 下载模型配置..." -ForegroundColor Yellow

    try {
        $webClient = New-Object System.Net.WebClient
        $webClient.DownloadFile($ConfigUrl, $ConfigFile)
        Write-Host "      配置下载完成" -ForegroundColor Green
    }
    catch {
        Write-Host "      下载失败: $_" -ForegroundColor Red
        exit 1
    }
}
else {
    Write-Host "[3/3] 配置已存在，跳过" -ForegroundColor Gray
}

Write-Host ""
Write-Host "=== 安装完成 ===" -ForegroundColor Cyan
Write-Host ""
Write-Host "测试命令:" -ForegroundColor White
Write-Host "  .\tts.exe -text `"你好世界`" -output hello.wav" -ForegroundColor Yellow
Write-Host ""

# 测试运行
$TtsExe = Join-Path $ProjectDir "tts.exe"
if (Test-Path $TtsExe) {
    $TestOutput = Join-Path $ProjectDir "test.wav"
    Write-Host "正在测试..." -ForegroundColor Gray

    try {
        & $TtsExe -text "安装成功" -output $TestOutput 2>&1 | Out-Null
        if (Test-Path $TestOutput) {
            Write-Host "测试成功! 生成了 test.wav" -ForegroundColor Green
        }
    }
    catch {
        Write-Host "测试失败: $_" -ForegroundColor Red
    }
}
