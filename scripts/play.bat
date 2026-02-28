@echo off
chcp 65001 >nul
echo ==========================================
echo   文本语音自动化测试工具 - 播放模式
echo ==========================================
echo.

cd /d "%~dp0"

if not exist "tts.exe" (
    echo 错误: 找不到 tts.exe
    pause
    exit /b 1
)

echo 检查 ADB 设备连接...
adb\adb.exe devices

echo.
echo 开始播放模式...
echo 流程: 播放语音 -^> logcat记录 -^> 截图 -^> 停止记录
echo.

tts.exe -play

echo.
echo ==========================================
echo 播放完成！
echo ==========================================
pause
