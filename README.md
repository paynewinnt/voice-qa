# Voice QA — Android 语音自动化测试工具

面向 Android 设备的语音指令自动化测试工具，集 TTS 音频生成、播放测试、应用性能测试和设备管理于一体。

当前仓库以 Linux CLI 和 Windows GUI/打包为主，不提供 macOS 运行支持。

## 功能特性

- **TTS 音频生成**
  - 多 TTS 引擎：Edge TTS（在线，微软神经网络语音，默认）+ Piper TTS（离线）
  - 多种声音可选（播音男声、标准女声等）
  - 批量文本转语音，可配置语音模板（前缀、后缀、静音间隔）
- **播放模式测试**：播放语音 + adb logcat 记录 + 自动截图 + 视频录制 + 断言验证
- **应用性能测试**：冷启动时间采集，支持 Component 候选下拉、`am start -W`、可选 Perfetto trace、logcat 时间线、批量统计和聚合报告
- **设备管理**：ADB 连接/断开、多设备 APK 安装、手动录屏
- Windows GUI 播放链路按选定设备执行，避免多设备误打
- Windows GUI 每次播放生成独立会话目录，历史报告、日志、截图、录屏不互相覆盖
- Windows GUI 生成阶段持久化 `manifest.json`，播放阶段优先按 `wav -> 原文` 映射断言
- 增量保存测试报告（中途停止也能保存已完成的结果）
- 图形界面版本（Windows GUI）
- 支持平台：Linux / Windows

## 项目结构

```
voice-qa/
├── cmd/                    # 命令行程序入口
│   └── main.go
├── gui/                    # GUI 程序 (Wails)
│   ├── main.go
│   ├── app.go
│   └── frontend/dist/      # Wails 嵌入前端页面（当前仓库无独立前端源码）
├── internal/
│   ├── audio/              # WAV 音频处理
│   ├── config/             # 配置文件管理
│   ├── player/             # 音频播放
│   ├── tts/                # TTS 引擎 (Edge TTS / Piper)
│   ├── adb/                # ADB 操作（logcat、截图）
│   └── perfetto/           # 启动时间 trace、logcat 分析和报告生成
├── bin/
│   ├── piper/              # Linux Piper 引擎
│   ├── piper-windows/      # Windows Piper 引擎
│   └── adb-windows/        # Windows ADB 工具
├── models/                 # 语音模型
├── scripts/
│   ├── build.sh            # 构建脚本
│   └── download.sh         # 下载 Piper 和模型
├── installer/              # Windows 安装包 (Inno Setup)
│   └── setup.iss
├── config.json             # 配置文件
├── text.txt                # 输入文本
└── output/                 # 输出目录
```

## 快速开始

### 1. 下载依赖

`download.sh` 用于在 Linux 开发环境下载 Piper 和中文模型；macOS 不在支持范围内。

```bash
# 下载 Piper 引擎和中文模型
./scripts/download.sh
```

### 2. 构建

```bash
# 构建所有版本（Linux CLI + Windows GUI）
./scripts/build.sh all

# 仅构建 Linux
./scripts/build.sh linux

# 仅构建 Windows GUI 版
./scripts/build.sh gui
```

Windows 下可直接使用 PowerShell / bat 脚本：

```powershell
.\scripts\build.ps1 -Target gui
.\scripts\build.ps1 -Target gui -InstallWails
```

```bat
scripts\build.bat -Target gui
```

构建产物在 `dist/` 目录，版本号格式为 `YYYY.MMDD.HHMM`：
- `tts-linux-amd64-v2025.1222.1723.tar.gz`
- `tts-gui-windows-amd64-v2025.1222.1723.zip`

> 注意：`gui/frontend/dist/index.html` 虽然位于 `dist` 目录下，但它是 Wails 通过 `go:embed all:frontend/dist` 嵌入的 GUI 页面。当前仓库没有独立前端源码，因此该文件必须纳入版本控制；否则其它电脑克隆后无法复现 Windows GUI 构建。

### 3. 使用

```bash
# 生成默认配置文件 config.json
./tts -init

# 批量生成（读取 config.json 中的 text_file，输出到 output_dir）
./tts

# 批量生成（指定文本文件和输出目录）
./tts -file text.txt -output output/

# 批量生成（简单模式，不加模板前后缀）
./tts -file text.txt -output output/ -simple

# 单条生成（带模板）
./tts -text "你好世界" -output hello.wav

# 单条生成（简单模式）
./tts -text "你好世界" -output hello.wav -simple

# 播放模式（使用 config.json 中的 output_dir，需连接 adb 设备）
./tts -play

# 播放模式（指定音频目录）
./tts -play -playdir ./scene1/

# 指定配置文件
./tts -config my-config.json
```

**所有选项：**

| 选项 | 说明 |
|------|------|
| `-init` | 生成默认配置文件 `config.json` |
| `-config` | 指定配置文件路径（默认自动查找 `config.json`） |
| `-text` | 要转换的文本（单条模式） |
| `-file` | 输入文本文件路径（批量模式，按行处理） |
| `-output` | 输出路径（单条为文件路径，批量为目录） |
| `-model` | Piper 模型文件路径（默认自动查找） |
| `-simple` | 简单模式，不使用模板，直接合成纯文本 |
| `-play` | 播放模式，播放音频并同步 logcat/截图/断言 |
| `-playdir` | 播放模式的音频目录（默认使用 `output_dir`） |

## 配置文件

`config.json` 配置项说明：

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `text_file` | 输入文本文件路径 | `text.txt` |
| `output_dir` | 输出目录 | `output` |
| `model_file` | Piper 模型文件路径（留空自动查找） | `""` |
| `voice_id` | 声音 ID，格式 `engine:voice` | `edge:zh-CN-YunjianNeural` |
| `template` | 语音模板序列，见下方说明 | 见下方示例 |
| `screenshot_before_end` | 播放结束前多少秒截图（秒） | `12` |
| `enable_video_recording` | 是否启用视频录制 | `false` |
| `recording_start_delay` | 音频开始后多少秒开始录制（秒） | `5` |
| `recording_end_before_end` | 音频结束前多少秒停止录制（秒） | `5` |
| `filename_max_length` | 输出文件名最大字符数 | `30` |

### template 语音模板

`template` 是一个有序片段数组，定义语音文件的完整结构。每个片段有两种类型：

| 字段 | 说明 |
|------|------|
| `{"type": "voice", "text": "文本"}` | 合成一段语音，`$MAIN` 为主文本占位符 |
| `{"type": "silence", "seconds": 1}` | 插入指定秒数的静音 |

默认模板示例：

```json
"template": [
  {"type": "silence", "seconds": 1},
  {"type": "voice",   "text": "小翼管家"},
  {"type": "silence", "seconds": 2},
  {"type": "voice",   "text": "$MAIN"},
  {"type": "silence", "seconds": 18},
  {"type": "voice",   "text": "小翼管家"},
  {"type": "silence", "seconds": 2},
  {"type": "voice",   "text": "返回首页"},
  {"type": "silence", "seconds": 5}
]
```

生成的语音文件结构：

```
[静音1秒] → [小翼管家] → [静音2秒] → [主文本] → [静音18秒]
         → [小翼管家] → [静音2秒] → [返回首页] → [静音5秒]
```

## 输出文件

以下目录结构说明针对 Windows GUI 默认行为。

生成的音频文件命名格式为：`序号 + 文本内容.wav`

生成阶段会在输出目录维护 `manifest.json`，用于记录 `wav -> 原文` 映射，播放模式会优先读取它来做断言。

播放阶段不会再把 `log/png/mp4/report` 直接铺在输出目录根下，而是为每次执行创建独立会话目录 `playtest-*`。

```
output/
├── 0001我要看电视.wav
├── 0002我要打电话.wav
├── manifest.json
├── recordings/
│   └── recording-20260604-095600.mp4
├── perf/
│   └── batch-20260604-094209-1780537329580392600/
│       ├── run-01/
│       │   └── report.txt
│       ├── run-02/
│       ├── summary.json
│       └── aggregate_report.txt
├── playtest-20260422-153045-1713771045123456000/
│   ├── 0001我要看电视.log
│   ├── 0001我要看电视.png
│   ├── 0001我要看电视.mp4   # 启用视频录制时生成
│   ├── 0002我要打电话.log
│   └── test_report.txt
└── ...
```

## 播放模式

播放模式用于自动化测试，需要连接 Android 设备。以下流程说明针对 Windows GUI。

GUI 下多设备场景需要先选择目标设备，播放链路中的 logcat、截图、录屏、停止清理都会绑定到该设备。

每条语音的执行流程：

1. 启动 logcat 录制
2. 播放 WAV 文件
3. 在语音结束前 N 秒自动截图（由 `screenshot_before_end` 控制）
4. 可选：在播放期间录制设备屏幕视频（由 `enable_video_recording` 控制）
5. 停止 logcat 录制，并从完整日志中裁剪出本次播放窗口
6. 断言验证：检查 logcat 中是否包含预期的 `"query":"<文本>"` 字段
7. 将日志、截图、录屏、报告写入本次 `playtest-*` 会话目录
8. 增量保存测试报告（中途中断也保留已完成结果）

CLI 的 `./tts -play` 目前仍沿用传统行为：
- 直接扫描播放目录中的 `.wav` 文件
- 在播放目录根下输出 `.log`、`.png` 和 `test_report.txt`
- 不维护 `manifest.json` 和 `playtest-*` 会话目录

```bash
# 确保 adb 设备已连接
adb devices

# 运行播放模式（使用 config.json 中的 output_dir）
./tts -play

# 指定音频目录
./tts -play -playdir ./scene1/
```

测试报告示例：
```
================== 测试执行报告 ==================
执行时间: 2025-12-22 17:23:45
执行状态: 已完成
总计: 4, 通过: 3, 失败: 1

------------------ 详细结果 ------------------
[1] 我要看电视
    状态: PASS
    断言: 找到匹配: "query":"我要看电视"
...
```

## 启动时间测试

Windows GUI 的「启动时间测试」用于采集 Android 应用冷启动数据。典型流程：

1. 在「设备管理」中连接并选择目标设备。
2. 选择应用包名。选择后 GUI 会自动解析可能的 Activity Component，并优先填入 launcher Activity。
3. 在「完整 Component 名称」下拉框中选择候选项，或在输入框中手动填写完整值，例如 `com.example/.MainActivity`。
4. 根据需要执行 `force-stop`、`pm clear`、返回首页或停止第三方应用。
5. 执行单次启动计时，或设置次数、间隔和采集时长后执行批量启动测试。
6. 批量完成后点击「生成聚合测试报告」，会在批量结果目录生成 `aggregate_report.txt`。

核心产物：

| 文件 | 说明 |
|------|------|
| `output/perf/launch-*` | 单次启动测试结果目录 |
| `output/perf/batch-*` | 批量启动测试结果目录 |
| `run-*/report.txt` | 单轮报告 |
| `summary.json` | 批量测试机器可读汇总 |
| `aggregate_report.txt` | 批量测试聚合报告，包含结论、测试链路、核心数据、逐轮明细和名词解释 |

Perfetto 是可选能力。未勾选「启用 Perfetto」时，启动测试只依赖 `am start -W` 和 logcat；设备不支持 Perfetto 不应阻塞基础启动计时。

## Windows GUI 版本

GUI 版本基于 Wails 框架开发，提供完整图形界面，包含 6 个功能标签页：

| 标签页 | 功能 |
|--------|------|
| **设备管理** | ADB 设备连接/断开（支持 IP 连接），多设备 APK 安装，手动录屏到 `output/recordings` |
| **启动时间测试** | 应用冷启动计时（毫秒精度），支持 Component 候选下拉、命令式应用控制、可选 Perfetto trace、logcat 时间线、单次/批量统计和聚合报告 |
| **生成语音** | 文本列表管理，批量 TTS 生成，实时进度（支持中途停止） |
| **播放模式** | 选择目标设备后执行播放测试，自动采集 logcat / 截图 / 录屏并输出独立会话目录（支持中途停止） |
| **配置设置** | 语音引擎选择，模板可视化编辑，视频录制参数配置 |
| **软件更新** | 从局域网 `latest.json` 检查版本、下载 zip、校验 SHA256 并打开更新目录 |

```
tts-gui-windows-amd64/
├── 语音播测工具.exe  # GUI 主程序
├── config.json                # 配置文件
├── text.txt                   # 文本文件（每行一条）
├── ttsengine/                 # Piper 离线 TTS 引擎
├── models/                    # 语音模型
├── adb/                       # ADB 工具
├── ffmpeg/                    # 视频录制工具
└── output/                    # 输出目录（wav、manifest、recordings、perf、playtest-*）
```

## Windows 安装包

使用 Inno Setup 构建安装包：

```bash
# 在 Windows 上使用 ISCC 编译
ISCC installer/setup.iss
```

安装包版本号自动生成，格式：`YYYY.MMDD.HHMM`

## Windows GUI 局域网更新

Windows GUI 支持从局域网文件服务手动检查更新、下载更新包并打开下载目录。默认版本清单地址为：

```text
http://172.16.15.15/latest.json
```

GUI 默认只负责下载，不会在运行中覆盖自身。下载完成后需要关闭程序，手动解压 zip；更新包会保存到当前程序目录的上一级，便于新版本目录与旧版本目录同级放置。完整 IIS 部署、`latest.json` 格式、SHA256 校验和排障步骤见 [Windows GUI 局域网更新指南](docs/windows-gui-lan-update.md)。

## 技术栈

- **语言**: Go 1.21+
- **TTS 引擎**:
  - [Edge TTS](https://github.com/rany2/edge-tts) (在线微软神经网络语音，默认)
  - [Piper](https://github.com/rhasspy/piper) (离线神经网络语音合成)
- **语音模型**: zh_CN-huayan-medium (Piper 中文离线模型)
- **音频格式**: WAV (22050Hz, 16bit, Mono)
- **GUI 框架**: [Wails](https://wails.io/) v2 (Go + WebView2)
- **安装包**: [Inno Setup](https://jrsoftware.org/isinfo.php)

## 可用声音

| Voice ID | 名称 | 性别 | 引擎 |
|----------|------|------|------|
| `edge:zh-CN-YunjianNeural` | 云健 (播音男声) | 男 | Edge TTS |
| `edge:zh-CN-XiaoxiaoNeural` | 晓晓 (标准女声) | 女 | Edge TTS |
| `edge:zh-CN-XiaoyiNeural` | 晓伊 (甜美女声) | 女 | Edge TTS |
| `edge:zh-CN-YunxiNeural` | 云希 (标准男声) | 男 | Edge TTS |
| `edge:zh-CN-YunxiaNeural` | 云夏 (少年男声) | 男 | Edge TTS |
| `edge:zh-CN-YunyangNeural` | 云扬 (新闻男声) | 男 | Edge TTS |
| `edge:zh-CN-liaoning-XiaobeiNeural` | 晓北 (东北女声) | 女 | Edge TTS |
| `edge:zh-CN-shaanxi-XiaoniNeural` | 晓妮 (陕西女声) | 女 | Edge TTS |
| `piper:zh_CN-huayan-medium` | Piper 中文女声 | 女 | Piper |

## 常见问题

### Q: piper 执行失败 (exit status 0xc0000409)

这个错误通常由以下原因导致：

**1. 路径中包含中文或特殊字符（最常见）**

将程序移动到纯英文路径，例如：
```
C:\tts\
D:\tools\tts\
```

避免使用包含中文的路径：
```
❌ C:\Users\张三\Desktop\语音工具\
❌ D:\我的文档\tts\
```

**2. 缺少 Visual C++ Redistributable 运行库**

下载安装 VC++ 2015-2022 运行库：
```
https://aka.ms/vs/17/release/vc_redist.x64.exe
```

**3. CPU 不支持 AVX2 指令集**

Piper 使用的 onnxruntime 需要 AVX2 指令集支持（2013年后的 Intel/AMD 处理器）。较老的 CPU 可能不支持。

### Q: 提示 'adb' 不是内部或外部命令

adb 工具已内置在 `adb/` 目录，程序会自动使用。如仍有问题，检查 `adb/` 目录是否完整。

### Q: 生成的语音有问题

检查 `text.txt` 文件编码是否为 UTF-8。

### Q: 当前支持哪些平台？

- Linux：支持 CLI 构建和运行
- Windows：支持 GUI 打包和使用
- macOS：当前不提供运行支持

### Q: 如何修改语音内容结构

优先编辑 `config.json` 中的 `template` 数组。

旧版 `prefix_text`、`suffix_text1`、`suffix_text2` 字段仍保留兼容，但新配置和 GUI 编辑都以 `template` 为准。

## License

MIT
