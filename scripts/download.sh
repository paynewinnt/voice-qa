#!/bin/bash

# Piper TTS 下载脚本
# 下载 piper 二进制文件和中文语音模型

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
MODELS_DIR="$PROJECT_DIR/models"
BIN_DIR="$PROJECT_DIR/bin"

mkdir -p "$MODELS_DIR" "$BIN_DIR"

# Piper 版本
PIPER_VERSION="2023.11.14-2"

# 检测系统架构
ARCH=$(uname -m)
OS=$(uname -s | tr '[:upper:]' '[:lower:]')

if [ "$OS" = "linux" ]; then
    if [ "$ARCH" = "x86_64" ]; then
        PIPER_ARCH="linux_x86_64"
    elif [ "$ARCH" = "aarch64" ]; then
        PIPER_ARCH="linux_aarch64"
    else
        echo "不支持的架构: $ARCH"
        exit 1
    fi
elif [ "$OS" = "darwin" ]; then
    if [ "$ARCH" = "x86_64" ]; then
        PIPER_ARCH="macos_x64"
    elif [ "$ARCH" = "arm64" ]; then
        PIPER_ARCH="macos_aarch64"
    else
        echo "不支持的架构: $ARCH"
        exit 1
    fi
else
    echo "不支持的操作系统: $OS"
    exit 1
fi

echo "=== 下载 Piper TTS ==="
PIPER_URL="https://github.com/rhasspy/piper/releases/download/${PIPER_VERSION}/piper_${PIPER_ARCH}.tar.gz"
echo "下载地址: $PIPER_URL"

if [ ! -f "$BIN_DIR/piper/piper" ]; then
    echo "正在下载 piper..."
    curl -L "$PIPER_URL" -o /tmp/piper.tar.gz
    tar -xzf /tmp/piper.tar.gz -C "$BIN_DIR"
    rm /tmp/piper.tar.gz
    echo "piper 下载完成"
else
    echo "piper 已存在，跳过下载"
fi

# 下载 Windows 版 piper (用于交叉编译打包)
echo ""
echo "=== 下载 Windows 版 Piper TTS ==="
PIPER_WIN_URL="https://github.com/rhasspy/piper/releases/download/${PIPER_VERSION}/piper_windows_amd64.zip"
if [ ! -f "$BIN_DIR/piper-windows/piper/piper.exe" ]; then
    echo "正在下载 Windows piper..."
    mkdir -p "$BIN_DIR/piper-windows"
    curl -L "$PIPER_WIN_URL" -o /tmp/piper_windows.zip
    unzip -o /tmp/piper_windows.zip -d "$BIN_DIR/piper-windows"
    rm /tmp/piper_windows.zip
    echo "Windows piper 下载完成"
else
    echo "Windows piper 已存在，跳过下载"
fi

echo ""
echo "=== 下载中文语音模型 ==="
# 中文模型 - huayan (女声，效果较好)
MODEL_NAME="zh_CN-huayan-medium"
MODEL_URL="https://huggingface.co/rhasspy/piper-voices/resolve/main/zh/zh_CN/huayan/medium/zh_CN-huayan-medium.onnx"
CONFIG_URL="https://huggingface.co/rhasspy/piper-voices/resolve/main/zh/zh_CN/huayan/medium/zh_CN-huayan-medium.onnx.json"

if [ ! -f "$MODELS_DIR/${MODEL_NAME}.onnx" ]; then
    echo "正在下载模型: $MODEL_NAME"
    curl -L "$MODEL_URL" -o "$MODELS_DIR/${MODEL_NAME}.onnx"
    curl -L "$CONFIG_URL" -o "$MODELS_DIR/${MODEL_NAME}.onnx.json"
    echo "模型下载完成"
else
    echo "模型已存在，跳过下载"
fi

echo ""
echo "=== 下载完成 ==="
echo "Linux Piper 路径: $BIN_DIR/piper/piper"
echo "Windows Piper 路径: $BIN_DIR/piper-windows/piper/piper.exe"
echo "模型路径: $MODELS_DIR/${MODEL_NAME}.onnx"
echo ""
echo "测试命令 (Linux):"
echo "  echo '你好世界' | $BIN_DIR/piper/piper --model $MODELS_DIR/${MODEL_NAME}.onnx --output_file test.wav"
