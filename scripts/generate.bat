@echo off
chcp 65001 >nul
echo ==========================================
echo   文本语音自动化测试工具 - 批量生成语音
echo ==========================================
echo.

cd /d "%~dp0"

if not exist "tts.exe" (
    echo 错误: 找不到 tts.exe
    pause
    exit /b 1
)

echo 开始批量生成语音...
echo.

tts.exe

echo.
echo ==========================================
echo 生成完成！
echo ==========================================
pause
