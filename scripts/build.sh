#!/bin/bash

# 构建脚本 - 支持 Linux 和 Windows 交叉编译
# 生成可移植的部署包

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
DIST_DIR="$PROJECT_DIR/dist"

cd "$PROJECT_DIR"

# 清理旧构建
rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

# 版本信息 (格式: YYYY.MMDD.HHMM)
VERSION="${VERSION:-$(date +%Y.%m%d.%H%M)}"
APP_NAME="tts"

echo "=== 构建 Text-to-Speech v$VERSION ==="

# 构建 Linux amd64
build_linux() {
    echo "构建 Linux amd64..."
    local OUTPUT_DIR="$DIST_DIR/${APP_NAME}-linux-amd64"
    mkdir -p "$OUTPUT_DIR"/{ttsengine,models,output}

    GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "$OUTPUT_DIR/$APP_NAME" ./cmd

    # 复制 piper 到 ttsengine 目录 (Linux)
    if [ -d "$PROJECT_DIR/bin/piper" ]; then
        cp -r "$PROJECT_DIR/bin/piper"/* "$OUTPUT_DIR/ttsengine/"
    fi

    # 复制模型
    cp "$PROJECT_DIR/models"/*.onnx "$OUTPUT_DIR/models/" 2>/dev/null || true
    cp "$PROJECT_DIR/models"/*.json "$OUTPUT_DIR/models/" 2>/dev/null || true

    # 创建启动脚本
    cat > "$OUTPUT_DIR/run.sh" << 'EOF'
#!/bin/bash
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
"$SCRIPT_DIR/tts" "$@"
EOF
    chmod +x "$OUTPUT_DIR/run.sh"

    # 打包
    (cd "$DIST_DIR" && tar -czf "${APP_NAME}-linux-amd64-v${VERSION}.tar.gz" "${APP_NAME}-linux-amd64")
    echo "  -> $DIST_DIR/${APP_NAME}-linux-amd64-v${VERSION}.tar.gz"
}

# 下载 Windows adb
download_adb_windows() {
    local ADB_DIR="$PROJECT_DIR/bin/adb-windows"
    if [ -f "$ADB_DIR/adb.exe" ]; then
        echo "adb 已存在，跳过下载"
        return 0
    fi

    echo "下载 Windows adb..."
    mkdir -p "$ADB_DIR"
    local ADB_URL="https://dl.google.com/android/repository/platform-tools-latest-windows.zip"
    local ADB_ZIP="$ADB_DIR/platform-tools.zip"

    curl -L -o "$ADB_ZIP" "$ADB_URL"
    unzip -q "$ADB_ZIP" -d "$ADB_DIR"
    mv "$ADB_DIR/platform-tools"/* "$ADB_DIR/"
    rmdir "$ADB_DIR/platform-tools"
    rm "$ADB_ZIP"
    echo "adb 下载完成"
}

# 下载 Windows ffmpeg
download_ffmpeg_windows() {
    local FFMPEG_DIR="$PROJECT_DIR/bin/ffmpeg-windows"
    if [ -f "$FFMPEG_DIR/ffmpeg.exe" ]; then
        echo "ffmpeg 已存在，跳过下载"
        return 0
    fi

    echo "下载 Windows ffmpeg..."
    mkdir -p "$FFMPEG_DIR"
    local FFMPEG_URL="https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl.zip"
    local FFMPEG_ZIP="$FFMPEG_DIR/ffmpeg.zip"

    curl -L -o "$FFMPEG_ZIP" "$FFMPEG_URL"
    unzip -q "$FFMPEG_ZIP" -d "$FFMPEG_DIR"
    mv "$FFMPEG_DIR"/ffmpeg-master-latest-win64-gpl/bin/ffmpeg.exe "$FFMPEG_DIR/"
    rm -rf "$FFMPEG_DIR"/ffmpeg-master-latest-win64-gpl
    rm "$FFMPEG_ZIP"
    echo "ffmpeg 下载完成"
}

# 构建 Windows amd64
build_windows() {
    echo "构建 Windows amd64..."
    local OUTPUT_DIR="$DIST_DIR/${APP_NAME}-windows-amd64"
    mkdir -p "$OUTPUT_DIR"/{models,output}

    GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o "$OUTPUT_DIR/${APP_NAME}.exe" ./cmd

    # 复制 Windows 版 piper 到 ttsengine 目录
    if [ -d "$PROJECT_DIR/bin/piper-windows/piper" ]; then
        mkdir -p "$OUTPUT_DIR/ttsengine"
        cp -r "$PROJECT_DIR/bin/piper-windows/piper"/* "$OUTPUT_DIR/ttsengine/"
    fi

    # 下载并复制 adb
    download_adb_windows
    if [ -d "$PROJECT_DIR/bin/adb-windows" ]; then
        mkdir -p "$OUTPUT_DIR/adb"
        cp "$PROJECT_DIR/bin/adb-windows/adb.exe" "$OUTPUT_DIR/adb/"
        cp "$PROJECT_DIR/bin/adb-windows/AdbWinApi.dll" "$OUTPUT_DIR/adb/"
        cp "$PROJECT_DIR/bin/adb-windows/AdbWinUsbApi.dll" "$OUTPUT_DIR/adb/"
    fi

    # 下载并复制 ffmpeg
    download_ffmpeg_windows
    if [ -f "$PROJECT_DIR/bin/ffmpeg-windows/ffmpeg.exe" ]; then
        mkdir -p "$OUTPUT_DIR/ffmpeg"
        cp "$PROJECT_DIR/bin/ffmpeg-windows/ffmpeg.exe" "$OUTPUT_DIR/ffmpeg/"
    fi

    # 复制模型
    cp "$PROJECT_DIR/models"/*.onnx "$OUTPUT_DIR/models/" 2>/dev/null || true
    cp "$PROJECT_DIR/models"/*.json "$OUTPUT_DIR/models/" 2>/dev/null || true

    # 创建默认配置文件
    cat > "$OUTPUT_DIR/config.json" << 'EOF'
{
  "text_file": "text.txt",
  "output_dir": "output",
  "model_file": "",
  "voice_id": "edge:zh-CN-YunjianNeural",
  "template": [
    {"type": "silence", "seconds": 1},
    {"type": "voice", "text": "小翼管家"},
    {"type": "silence", "seconds": 2},
    {"type": "voice", "text": "$MAIN"},
    {"type": "silence", "seconds": 18},
    {"type": "voice", "text": "小翼管家"},
    {"type": "silence", "seconds": 2},
    {"type": "voice", "text": "返回首页"},
    {"type": "silence", "seconds": 5}
  ],
  "screenshot_before_end": 12,
  "enable_video_recording": false,
  "recording_start_delay": 5,
  "recording_end_before_end": 5,
  "filename_max_length": 30
}
EOF

    # 创建示例文本文件
    cat > "$OUTPUT_DIR/text.txt" << 'EOF'
我要看电视
我要看中央三套
上个台
下个台
换个台
换个频道
换频道
换台
快退一小时
后退十分钟
快进三十秒
返回首页
我要看遍地书香第五集
EOF

    # 创建启动脚本 (使用 printf 确保 CRLF 换行符)
    printf '@echo off\r\ncd /d "%%~dp0"\r\ntts.exe %%*\r\npause\r\n' > "$OUTPUT_DIR/run.bat"

    printf '@echo off\r\ncd /d "%%~dp0"\r\necho 根据 config.json 和 text.txt 生成语音...\r\ntts.exe\r\npause\r\n' > "$OUTPUT_DIR/generate.bat"

    printf '@echo off\r\ncd /d "%%~dp0"\r\necho 播放模式: 播放语音 + logcat记录 + 截图\r\necho 请确保 adb 设备已连接\r\ntts.exe -play\r\npause\r\n' > "$OUTPUT_DIR/play.bat"

    # 创建说明文件
    cat > "$OUTPUT_DIR/使用文档.txt" << 'EOF'
================================================================================
                        Text-to-Speech 文本转语音工具
                              Windows 使用文档
================================================================================

一、部署说明
--------------------------------------------------------------------------------
    将压缩包解压到任意目录即可使用，无需安装 Python 或其他开发环境。

    目录结构:
    ├── tts.exe          # 主程序 (命令行版本)
    ├── config.json      # 配置文件
    ├── text.txt         # 文本文件 (每行一条)
    ├── generate.bat     # 批量生成语音
    ├── play.bat         # 播放模式
    ├── run.bat          # 命令行模式
    ├── ttsengine/       # 离线TTS引擎 (已包含)
    ├── models/          # 离线语音模型 (已包含)
    ├── ffmpeg/          # 音频处理工具 (已包含)
    ├── adb/             # ADB工具 (已包含)
    └── output/          # 输出目录 (wav/log/png)

    【注意】使用 Piper 离线语音时，首次运行需安装 VC++ 运行库:
    下载地址: https://aka.ms/vs/17/release/vc_redist.x64.exe


二、语音引擎说明
--------------------------------------------------------------------------------

    本工具支持两种语音引擎:

    1. Edge TTS (在线，推荐)
       - 需要联网
       - 音质更好，支持多种声音
       - 默认使用: 云健 (zh-CN-YunjianNeural) 播音男声

    2. Piper (离线)
       - 无需联网
       - 需要安装 VC++ 运行库
       - 声音: 中文女声

    通过 config.json 中的 voice_id 切换:
       "voice_id": "edge:zh-CN-YunjianNeural"   # Edge 云健男声
       "voice_id": "edge:zh-CN-XiaoxiaoNeural"  # Edge 晓晓女声
       "voice_id": "piper:zh_CN-huayan-medium"  # Piper 离线女声


三、使用方式
--------------------------------------------------------------------------------

    【方式1】双击 generate.bat
        - 根据 config.json 和 text.txt 批量生成语音
        - 输出到 output/ 目录

    【方式2】双击 play.bat
        - 播放已生成的语音
        - 同时记录 adb logcat 日志
        - 在结束前自动截图
        - 需要先连接 adb 设备

    【方式3】命令行
        # 单条生成
        tts.exe -text "你好世界" -output hello.wav

        # 批量生成（简单模式，无前后缀）
        tts.exe -file text.txt -output output -simple

        # 重新生成默认配置
        tts.exe -init


四、配置文件说明 (config.json)
--------------------------------------------------------------------------------

    基础配置:
    ─────────────────────────────────────────────────────────────────
    参数                      说明                          默认值
    ─────────────────────────────────────────────────────────────────
    text_file                 输入文本文件                  text.txt
    output_dir                输出目录                      output
    voice_id                  语音引擎ID                    edge:zh-CN-YunjianNeural
    screenshot_before_end     结束前截图(秒)                12
    filename_max_length       文件名最大字符数              30

    语音模板配置 (template):
    ─────────────────────────────────────────────────────────────────
    模板是一个数组，定义语音的完整结构。每个元素可以是:

    - 静音: {"type": "silence", "seconds": 秒数}
    - 语音: {"type": "voice", "text": "文本内容"}
    - 主文本: {"type": "voice", "text": "$MAIN"}  (必须包含)

    默认模板示例:
    "template": [
        {"type": "silence", "seconds": 1},
        {"type": "voice", "text": "小翼管家"},
        {"type": "silence", "seconds": 2},
        {"type": "voice", "text": "$MAIN"},
        {"type": "silence", "seconds": 18},
        {"type": "voice", "text": "小翼管家"},
        {"type": "silence", "seconds": 2},
        {"type": "voice", "text": "返回首页"},
        {"type": "silence", "seconds": 5}
    ]


五、生成的语音结构 (默认模板)
--------------------------------------------------------------------------------

    [静音1秒] → [小翼管家] → [静音2秒] → [文本内容] → [静音18秒]
             → [小翼管家] → [静音2秒] → [返回首页] → [静音5秒]

    可通过修改 config.json 中的 template 数组自定义结构。


六、输出文件命名
--------------------------------------------------------------------------------

    格式: 序号 + 文本内容.wav

    例如 text.txt 内容:
        我要看电视
        换个台

    生成:
        output/
        ├── 0001我要看电视.wav
        ├── 0002换个台.wav


七、播放模式说明 (play.bat)
--------------------------------------------------------------------------------

    播放模式需要连接 Android 设备，用于:
    1. 播放语音文件
    2. 记录设备 logcat 日志
    3. 在语音结束前指定秒数截图

    adb 工具已内置在 adb/ 目录，无需额外安装。

    使用前准备:
    1. 连接 Android 设备
       - USB 连接并开启 USB 调试
       - 或使用 adb connect <IP>:<端口> 无线连接

    2. 验证连接
       - 命令行进入本目录，执行: adb\adb.exe devices
       - 应显示已连接的设备


八、可用声音列表
--------------------------------------------------------------------------------

    Edge TTS (在线):
    ─────────────────────────────────────────────────────────────────
    edge:zh-CN-YunjianNeural     云健 (播音男声) [默认]
    edge:zh-CN-YunxiNeural       云希 (标准男声)
    edge:zh-CN-YunyangNeural     云扬 (新闻男声)
    edge:zh-CN-YunxiaNeural      云夏 (少年男声)
    edge:zh-CN-XiaoxiaoNeural    晓晓 (标准女声)
    edge:zh-CN-XiaoyiNeural      晓伊 (甜美女声)

    Piper (离线):
    ─────────────────────────────────────────────────────────────────
    piper:zh_CN-huayan-medium    中文女声


九、常见问题
--------------------------------------------------------------------------------

    Q: Edge TTS 连接失败
    A: 检查网络连接是否正常，确保能访问外网

    Q: piper 执行失败 (exit status 0xc0000409)
    A: 缺少 Visual C++ 运行库。请下载安装:
       https://aka.ms/vs/17/release/vc_redist.x64.exe

    Q: 提示 'adb' 不是内部或外部命令
    A: adb 已内置在 adb/ 目录，程序会自动使用

    Q: 生成的语音有问题
    A: 检查 text.txt 编码是否为 UTF-8 (支持带BOM)

    Q: 如何修改语音结构
    A: 编辑 config.json 中的 template 数组，自由添加/删除/调整语音和静音片段

    Q: 如何切换语音
    A: 修改 config.json 中的 voice_id，参考"可用声音列表"

================================================================================
EOF

    # 打包
    (cd "$DIST_DIR" && zip -rq "${APP_NAME}-windows-amd64-v${VERSION}.zip" "${APP_NAME}-windows-amd64")
    echo "  -> $DIST_DIR/${APP_NAME}-windows-amd64-v${VERSION}.zip"
}

# 构建 Windows GUI 版本
build_windows_gui() {
    echo "构建 Windows GUI 版本..."
    local OUTPUT_DIR="$DIST_DIR/${APP_NAME}-gui-windows-amd64"
    mkdir -p "$OUTPUT_DIR"/{models,output,adb,ffmpeg}

    # 检查 wails 是否安装
    if ! command -v wails &> /dev/null; then
        echo "  wails 未安装，跳过 GUI 构建"
        return 0
    fi

    # 构建 GUI
    cd "$PROJECT_DIR/gui"
    wails build -platform windows/amd64 -o "文本语音自动化测试工具.exe" -clean

    # 复制 GUI 可执行文件
    cp "$PROJECT_DIR/gui/build/bin/文本语音自动化测试工具.exe" "$OUTPUT_DIR/"

    cd "$PROJECT_DIR"

    # 复制 Windows 版 piper 到 ttsengine 目录
    if [ -d "$PROJECT_DIR/bin/piper-windows/piper" ]; then
        mkdir -p "$OUTPUT_DIR/ttsengine"
        cp -r "$PROJECT_DIR/bin/piper-windows/piper"/* "$OUTPUT_DIR/ttsengine/"
    fi

    # 复制 adb
    if [ -d "$PROJECT_DIR/bin/adb-windows" ]; then
        cp "$PROJECT_DIR/bin/adb-windows/adb.exe" "$OUTPUT_DIR/adb/"
        cp "$PROJECT_DIR/bin/adb-windows/AdbWinApi.dll" "$OUTPUT_DIR/adb/"
        cp "$PROJECT_DIR/bin/adb-windows/AdbWinUsbApi.dll" "$OUTPUT_DIR/adb/"
    fi

    # 下载并复制 ffmpeg
    download_ffmpeg_windows
    if [ -f "$PROJECT_DIR/bin/ffmpeg-windows/ffmpeg.exe" ]; then
        cp "$PROJECT_DIR/bin/ffmpeg-windows/ffmpeg.exe" "$OUTPUT_DIR/ffmpeg/"
    fi

    # 构建命令行工具 tts.exe
    echo "  构建命令行工具 tts.exe..."
    GOOS=windows GOARCH=amd64 go build -o "$OUTPUT_DIR/tts.exe" ./cmd/

    # 复制 bat 文件
    cp "$PROJECT_DIR/scripts/generate.bat" "$OUTPUT_DIR/"
    cp "$PROJECT_DIR/scripts/play.bat" "$OUTPUT_DIR/"

    # 复制模型
    cp "$PROJECT_DIR/models"/*.onnx "$OUTPUT_DIR/models/" 2>/dev/null || true
    cp "$PROJECT_DIR/models"/*.json "$OUTPUT_DIR/models/" 2>/dev/null || true

    # 创建默认配置文件
    cat > "$OUTPUT_DIR/config.json" << 'EOF'
{
  "text_file": "text.txt",
  "output_dir": "output",
  "model_file": "",
  "voice_id": "edge:zh-CN-YunjianNeural",
  "template": [
    {"type": "silence", "seconds": 1},
    {"type": "voice", "text": "小翼管家"},
    {"type": "silence", "seconds": 2},
    {"type": "voice", "text": "$MAIN"},
    {"type": "silence", "seconds": 18},
    {"type": "voice", "text": "小翼管家"},
    {"type": "silence", "seconds": 2},
    {"type": "voice", "text": "返回首页"},
    {"type": "silence", "seconds": 5}
  ],
  "screenshot_before_end": 12,
  "enable_video_recording": false,
  "recording_start_delay": 5,
  "recording_end_before_end": 5,
  "filename_max_length": 30
}
EOF

    # 创建示例文本文件
    cat > "$OUTPUT_DIR/text.txt" << 'EOF'
我要看电视
我要看中央三套
上个台
下个台
换个台
换个频道
换频道
换台
快退一小时
后退十分钟
快进三十秒
返回首页
我要看遍地书香第五集
EOF

    # 复制 README
    if [ -f "$PROJECT_DIR/scripts/README.txt" ]; then
        cp "$PROJECT_DIR/scripts/README.txt" "$OUTPUT_DIR/"
    fi

    # 打包
    (cd "$DIST_DIR" && zip -rq "${APP_NAME}-gui-windows-amd64-v${VERSION}.zip" "${APP_NAME}-gui-windows-amd64")
    echo "  -> $DIST_DIR/${APP_NAME}-gui-windows-amd64-v${VERSION}.zip"
}

# 执行构建
case "${1:-all}" in
    linux)
        build_linux
        ;;
    windows)
        build_windows
        ;;
    gui)
        build_windows_gui
        ;;
    all)
        build_linux
        build_windows
        build_windows_gui
        ;;
    *)
        echo "用法: $0 [linux|windows|gui|all]"
        exit 1
        ;;
esac

echo ""
echo "=== 构建完成 ==="
ls -lh "$DIST_DIR"/*.{tar.gz,zip} 2>/dev/null || true
