@echo off
chcp 65001 >nul
cd /d "%~dp0"
cd ..

echo ========================================
echo Piper TTS 调试工具
echo ========================================
echo.

echo [1] 系统信息...
echo     当前目录: %CD%
systeminfo | findstr /B /C:"OS Name" /C:"OS Version"
echo.

echo [2] 检查 piper 目录...
if exist "piper\piper.exe" (
    echo     piper.exe 存在
    echo     文件大小:
    for %%A in ("piper\piper.exe") do echo     %%~zA bytes
) else (
    echo     错误: piper.exe 不存在!
    goto :end
)

echo.
echo [3] 检查 DLL 文件...
for %%f in (piper\espeak-ng.dll piper\onnxruntime.dll piper\onnxruntime_providers_shared.dll piper\piper_phonemize.dll) do (
    if exist "%%f" (
        echo     %%f 存在
    ) else (
        echo     错误: %%f 不存在!
    )
)

echo.
echo [4] 检查模型文件...
if exist "models\zh_CN-huayan-medium.onnx" (
    echo     模型文件存在
    for %%A in ("models\zh_CN-huayan-medium.onnx") do echo     文件大小: %%~zA bytes
) else (
    echo     警告: zh_CN-huayan-medium.onnx 不存在
    echo     检查其他模型...
    dir /b models\*.onnx 2>nul
)

echo.
echo [5] 检查 espeak-ng-data 目录...
if exist "piper\espeak-ng-data" (
    echo     espeak-ng-data 目录存在
) else (
    echo     错误: espeak-ng-data 目录不存在!
)

echo.
echo [6] 检查 VC++ 运行库...
reg query "HKLM\SOFTWARE\Microsoft\VisualStudio\14.0\VC\Runtimes\x64" /v Version 2>nul
if %ERRORLEVEL% NEQ 0 (
    echo     警告: 可能缺少 Visual C++ Redistributable
    echo     请安装: https://aka.ms/vs/17/release/vc_redist.x64.exe
)

echo.
echo ========================================
echo [7] 测试 piper.exe 版本信息...
echo ========================================
piper\piper.exe --version 2>&1
echo 返回码: %ERRORLEVEL%

echo.
echo ========================================
echo [8] 直接运行 piper.exe 测试...
echo ========================================
echo.
echo 命令: echo 测试 ^| piper\piper.exe --model models\zh_CN-huayan-medium.onnx --output_file test_debug.wav
echo.

echo 测试 | piper\piper.exe --model models\zh_CN-huayan-medium.onnx --output_file test_debug.wav 2>&1

echo.
echo ========================================
echo 返回码: %ERRORLEVEL%
echo ========================================

if %ERRORLEVEL% EQU 0 (
    echo 成功! 检查 test_debug.wav 文件
    if exist "test_debug.wav" (
        for %%A in ("test_debug.wav") do echo 生成文件大小: %%~zA bytes
    )
) else (
    echo.
    echo 执行失败，错误码: %ERRORLEVEL%
    echo.
    echo 可能的原因和解决方案:
    echo.
    echo 1. 缺少 Visual C++ Redistributable 2015-2022:
    echo    下载: https://aka.ms/vs/17/release/vc_redist.x64.exe
    echo.
    echo 2. CPU 不支持 AVX2 指令集 (较老的 CPU):
    echo    piper 需要支持 AVX2 的 CPU (2013年后的 Intel/AMD 处理器)
    echo.
    echo 3. 杀毒软件拦截:
    echo    尝试将 piper 目录添加到杀毒软件白名单
    echo.
    echo 4. 以管理员身份运行:
    echo    右键点击此脚本，选择"以管理员身份运行"
    echo.
    echo 5. 路径中包含特殊字符:
    echo    尝试将程序移动到简单路径，如 C:\tts\
)

:end
echo.
echo 按任意键退出...
pause >nul
