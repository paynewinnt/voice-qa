@echo off
chcp 65001 >nul
echo ==========================================
echo   Voice QA - Play Mode
echo ==========================================
echo.

cd /d "%~dp0"

if not exist "tts.exe" (
    echo Error: tts.exe not found
    pause
    exit /b 1
)

echo Checking ADB devices...
adb\adb.exe devices

echo.
echo Starting play mode...
echo Flow: play audio -^> logcat -^> screenshot -^> stop record
echo.

tts.exe -play

echo.
echo ==========================================
echo Play mode completed
echo ==========================================
pause
