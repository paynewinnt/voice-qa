@echo off
chcp 65001 >nul
echo ==========================================
echo   Voice QA - Generate Audio
echo ==========================================
echo.

cd /d "%~dp0"

if not exist "tts.exe" (
    echo Error: tts.exe not found
    pause
    exit /b 1
)

echo Generating audio...
echo.

tts.exe

echo.
echo ==========================================
echo Generate completed
echo ==========================================
pause
