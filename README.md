# Voice QA — Android 语音自动化测试工具

面向 Android 设备的语音指令自动化测试工具，集 TTS 音频生成、播放测试、应用性能测试和设备管理于一体。

当前仓库以 Linux CLI 和 Windows GUI/打包为主，不提供 macOS 运行支持。

## 功能特性

- **TTS 音频生成**
  - 多 TTS 引擎：Edge TTS（在线，微软神经网络语音，默认）+ Piper TTS（离线）
  - 多种声音可选（播音男声、标准女声等）
  - 批量文本转语音，可配置语音模板（前缀、后缀、静音间隔）
- **播放模式测试**：播放语音 + adb logcat 记录 + 自动截图 + 视频录制 + 断言验证
- **应用性能测试**：保留冷启动时间、可选 Perfetto trace 和聚合统计，并新增多设备前台交互性能采集，覆盖 CPU、PSS、线程、帧、稳定性、AudioFocus、网络和完整证据报告
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
│   ├── perfetto/           # 启动时间 trace、logcat 分析和报告生成
│   └── performance/        # 综合性能采集、指标解析、阈值判定和 Markdown 报告
├── bin/                    # 构建时自动下载的本地工具缓存（不提交）
│   ├── piper/              # Linux Piper 引擎
│   ├── piper-windows/      # Windows Piper 引擎
│   └── adb-windows/        # Windows ADB 工具
├── models/                 # 构建时自动下载的语音模型缓存（不提交）
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
- `latest.json`（Windows GUI 更新清单，包含版本、详细说明、最近一年变更记录、zip 文件名和 SHA256）

> 注意：`gui/frontend/dist/index.html` 虽然位于 `dist` 目录下，但它是 Wails 通过 `go:embed all:frontend/dist` 嵌入的 GUI 页面。当前仓库没有独立前端源码，因此该文件必须纳入版本控制；否则其它电脑克隆后无法复现 Windows GUI 构建。

### 3. 构建依赖自动下载

Windows GUI 打包会在缺少资源时自动下载并缓存到本地 `bin/`、`models/`，然后再放进最终 zip。`bin/`、`models/`、`dist/` 都是构建产物或三方二进制缓存，不提交到 Git。

默认下载源：

| 文件 | 用途 |
|------|------|
| `platform-tools-latest-windows.zip` | Windows ADB |
| `ffmpeg-master-latest-win64-gpl.zip` | Windows ffmpeg |
| `piper_windows_amd64.zip` | Windows Piper 离线 TTS 引擎 |
| `zh_CN-huayan-medium.onnx` | Piper 中文模型 |
| `zh_CN-huayan-medium.onnx.json` | Piper 中文模型配置 |

打包平台能访问外网时，直接运行构建命令即可，脚本会从官方源补齐缺失文件。

如果官方源访问不稳定，可以把以上 5 个文件上传到一个打包平台可访问的公网地址，例如对象存储、公网文件服务器或公司制品库。目录下文件名保持不变：

```text
https://example.com/voice-qa-assets/platform-tools-latest-windows.zip
https://example.com/voice-qa-assets/ffmpeg-master-latest-win64-gpl.zip
https://example.com/voice-qa-assets/piper_windows_amd64.zip
https://example.com/voice-qa-assets/zh_CN-huayan-medium.onnx
https://example.com/voice-qa-assets/zh_CN-huayan-medium.onnx.json
```

然后按下面方式指定资产根地址：

```powershell
.\scripts\build.ps1 -Target gui -AssetBaseUrl https://example.com/voice-qa-assets
```

也可以用环境变量，适合 CI 或固定构建机：

```powershell
$env:VOICE_QA_ASSET_BASE_URL = "https://example.com/voice-qa-assets"
.\scripts\build.ps1 -Target gui
```

需要确认本机缓存是否齐全但不希望脚本联网时，加 `-SkipDownload`；资源缺失会直接失败。

### 4. 使用

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

GUI 每完成一条语音，都会在输出目录原子更新 `manifest.json`，记录 `wav -> 原文` 映射，播放模式优先读取它来做断言。点击停止会取消当前 Edge 网络请求或 Piper 合成进程，不再生成后续模板片段；已完成的语音及映射会保留。本次生成使用开始时的配置快照，运行中修改配置只影响后续任务。

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

### 局域网设备发现

「设备管理 → 设备连接 → 局域网发现」会读取本机已启用的私有 IPv4 网卡，优先展示 Wi-Fi 网卡，并自动扫描当前地址所在的 `/24` 网段。可切换网卡、修改扫描网段及 ADB 端口（默认 `5555`）；扫描范围必须在所选网卡的实际子网内，单次不超过 1024 个地址，不扫描公网。

发现结合本机邻居表（ARP）、限定并发的 TCP 端口探测及 `adb mdns services`。结果显示 IP、调试端口、名称（如果服务提供）、MAC（如果邻居表提供）和发现来源，可筛选、多选、全选并连接；最多 4 台并行连接，每台最多 15 秒，成功后刷新各模块设备列表，并在已连接列表勾选本次成功连接的设备。扫描、连接均可停止，关闭弹窗也会取消未完成任务。

邻居缓存不代表设备仍在线，端口开放不代表一定是 ADB；只有 `adb connect` 后确认设备状态为 `device` 才计为连接成功。无线调试配对端口不会用于普通连接，Android 无线调试仍可能需要先配对或在设备上授权。访客 Wi-Fi 隔离、防火墙、设备休眠、非默认调试端口及 IPv6-only 设备可能导致遗漏，因此发现结果不是路由器上全部 Wi-Fi 终端的权威清单。Windows/macOS 使用系统 ARP 表，Linux 使用 `ip neigh`；某种发现方式不可用时会显示警告并保留其他结果，不自动修改防火墙、路由或设备调试设置。

### APK 覆盖安装

设备管理中的 APK 安装使用保留数据覆盖方式，不会自动卸载应用、执行 `pm clear` 或强制降级。安装前检查目标是否在线；网络设备离线时只重连当前目标，不重启全局 ADB 服务。

安装使用 `adb -s <serial> install --streaming --no-incremental -r package.apk`，直接将 APK 流传给 Android Package Manager，不再先 `push` 到设备再执行 `pm install`，也不会在失败后回退到暂存安装。设备上不额外创建工具管理的 APK 文件；Android 安装器自身管理的安装会话、应用文件和存储空间需求仍然存在。ADB 或设备不支持流式安装时，会明确报错。

为兼容 Windows 中文路径，本机仍会准备英文文件名临时副本，正常返回、失败或取消后均尝试删除，清理失败会显示警告，原始 APK 不变。开始流式安装前还会尝试删除旧版工具的固定缓存 `/data/local/tmp/voice-qa-apk-cache.apk`，最多等待 10 秒；删除失败会保留警告，不会删除其他 APK 或应用数据。不再需要设备提供 `stat`、`sha256sum`，也不再保留 APK 以供重试复用。

GUI 显示安装阶段，并每 5 秒更新已耗时。ADB 的流式安装不提供可靠的字节进度，因此不显示虚构的百分比、传输速度或剩余时间，也不因长时间没有命令输出就判定传输停滞。连接检查每步最多 15 秒，流式传输和安装合计最多 30 分钟，完整安装任务最多 40 分钟。只有命令正常退出且收到独立的 `Success` 行才计为成功；超时、取消或连接中断时不自动重试，而是提示结果不确定，需先恢复连接并查询应用版本。实际速度仍取决于网络、ADB 通道和设备存储，流式安装并不能消除网络瓶颈。

同版本覆盖不需要先卸载，但新旧 APK 必须兼容：签名不一致或 `versionCode` 低于已安装版本时，工具会明确提示并保留原应用数据。显示的 `versionName` 相同不代表 `versionCode` 一致。存储不足、CPU 架构不匹配、APK 解析失败和设备安装限制也会分别显示原因。

## 性能测试

Windows GUI 的「性能测试」包含启动性能和应用综合性能两部分。

### 启动性能

用于采集 Android 应用冷启动数据。典型流程：

1. 在「设备管理」中连接并选择目标设备。
2. 选择应用包名。选择后 GUI 会自动解析可能的 Activity Component，并优先填入 launcher Activity。
3. 在「完整 Component 名称」下拉框中选择候选项，或在输入框中手动填写完整值，例如 `com.example/.MainActivity`。
4. 需要通过 Deep Link 启动时，启用「Intent / Deep Link 启动」，填写 Action、Category 和 Data URI。工具会按独立参数执行，例如 `am start -W -n com.example/.SplashActivity -a android.intent.action.VIEW -c android.intent.category.BROWSABLE -d "example://open_page"`。
5. 单次计时可勾选「启动前 force-stop（冷启动）」直接执行完整冷启动链路；批量测试继续使用批量区的 `force-stop`、`pm clear` 选项。
6. 执行单次启动计时，或设置次数、间隔和采集时长后执行批量启动测试。
7. 在测试记录中勾选参与统计的记录，选择 TotalTime、手动(ms) 或两者，再计算平均值或生成聚合报告。单次和批量记录均可参与；不同设备、Component、Intent 参数及 force-stop / pm clear 方式分别统计，不计算跨组平均值。

手动(ms)以实际发出 `am start` 的后端时间为起点，以用户点击停止后后端收到停止请求的时间为终点，不包含启动前的准备过程。仅成功启动并由用户主动停止的记录计入手动统计，批量采集、自动超时、失败和无法确认停止方式的旧记录不计入；同时选择两种指标且存在有效手动记录时，报告以手动(ms)为主要结论。Deep Link 的 `&`、空格、引号等参数由工具统一进行 Android shell 转义，无需手动添加转义符。

核心产物：

| 文件 | 说明 |
|------|------|
| `output/perf/launch-*` | 单次启动测试结果目录 |
| `output/perf/batch-*` | 批量启动测试结果目录 |
| `run-*/report.txt` | 单轮报告 |
| `summary.json` | 批量测试机器可读汇总 |
| `aggregate_report.txt` | 批量测试聚合报告，包含结论、测试链路、核心数据、逐轮明细和名词解释 |

Perfetto 是可选能力。未勾选「启用 Perfetto」时，启动测试只依赖 `am start -W` 和 logcat；设备不支持 Perfetto 不应阻塞基础启动计时。

### 应用综合性能

综合性能测试位于启动测试记录下方，适合对已经运行并位于前台的 APK 执行可重复业务交互采样：

1. 在每台目标设备上提前打开应用，并停留在相同的可操作前台页面。
2. 填写包名，勾选一台或多台设备；多设备按顺序测试，避免采集相互干扰。
3. 每行填写一个 `名称, X, Y` 场景；使用 `-1,-1` 时仅采样当前页面，不发送点击。
4. 设置轮数、交互后等待时间和建议阈值，可选关联当前启动测试记录。
5. 点击「开始综合性能测试」。运行中可点击「停止测试并保存」，工具会保留已经采集的结果和证据。

综合性能测试不会执行 `am force-stop`、`pm clear`、`am start` 或 `monkey`，因此不会主动改变应用启动状态。每个样本采集目标进程的 `top` CPU、`/proc/<pid>/stat` 区间 CPU、PSS、线程、RSS、PID 和前台状态；测试窗口还会采集 `gfxinfo`、连续 logcat、崩溃/ANR/AudioFocus、UID 网络增量、截图、UI XML、设备和 APK 元数据。

每次交互前及采样后均核对目标进程和前台包名；目标离开前台或进程变化时停止后续点击并保留当前证据。CPU/PSS 有效样本不足、零渲染帧或缺少帧 P90 时，不会判定通过。验证启动入口、启动计时、综合性能测试、应用控制和 APK 安装按设备互斥；自定义 ADB 命令和外部工具仍需由操作者避免干扰测试。关联启动数据仅纳入当前包名及所选设备的成功记录，并按启动条件分组。

核心产物位于 `output/performance/performance-*`：

| 文件 | 说明 |
|------|------|
| `performance_report.md` | 13 个章节的综合性能报告，包含总体结论、核心数据、场景分解、启动数据、问题建议、证据和限制 |
| `protocol.json` | 包名、设备、场景、轮数、等待时间、禁用命令和阈值 |
| `combined-results.json` | 全部设备结构化结果、错误和最终判定 |
| `device-*/samples.csv` | 每次场景交互的逐样本指标 |
| `device-*/summary.json` | 单设备结构化汇总 |
| `device-*/*.txt|png|xml` | meminfo、gfxinfo、logcat、系统信息、前后截图和 UI 证据 |

## Windows GUI 版本

GUI 版本基于 Wails 框架开发，提供完整图形界面，包含 6 个功能标签页：

| 标签页 | 功能 |
|--------|------|
| **设备管理** | ADB 设备连接/断开（支持 IP 连接），多设备 APK 安装，手动录屏到 `output/recordings`，按 APK 包名查询/清空/保存 logcat，分析 `com.zjdx.neuralnexus` 心跳和悬浮弹窗记录 |
| **性能测试** | 启动性能计时与聚合报告；多设备综合性能场景采样，覆盖 CPU、PSS、线程、渲染、稳定性、AudioFocus、网络、截图/UI 证据和 Markdown 综合报告 |
| **生成语音** | 文本列表管理，批量 TTS 生成，实时进度（支持中途停止） |
| **播放模式** | 选择目标设备后执行播放测试，自动采集 logcat / 截图 / 录屏并输出独立会话目录（支持中途停止） |
| **配置设置** | 语音引擎选择，模板可视化编辑，视频录制参数配置 |
| **软件更新** | 从公网云服务器检查版本、显示下载进度、校验 SHA256、原地更新并自动重启 |

```
tts-gui-windows-amd64/
├── 语音播测工具.exe  # GUI 主程序
├── config.json                # 配置文件
├── text.txt                   # 文本文件（每行一条）
├── ttsengine/                 # Piper 离线 TTS 引擎
├── models/                    # 语音模型
├── adb/                       # ADB 工具
├── ffmpeg/                    # 视频录制工具
└── output/                    # 输出目录（wav、manifest、recordings、perf、performance、playtest-*）
```

## Windows 安装包

使用 Inno Setup 构建安装包：

```bash
# 在 Windows 上使用 ISCC 编译
ISCC installer/setup.iss
```

安装包版本号自动生成，格式：`YYYY.MMDD.HHMM`

## Windows GUI 公网更新

Windows GUI 从独立公网云服务器检查更新。固定版本清单地址为：

```text
https://124.223.218.142/voice-qa/latest.json
```

执行 `./scripts/build.sh gui` 或 `scripts\build.bat -Target gui` 后，`dist/` 会同时生成 GUI zip 和 `latest.json`。发布前先提交并推送对应源码，再部署更新文件：

```bash
./scripts/deploy-update.sh
```

本次版本的详细说明维护在 `release/release-notes.txt`，已发布版本的初始历史维护在 `release/update-history.json`。构建脚本会将本次版本加入 `latest.json.history`，按版本去重并只保留最近一年；部署脚本还会合并服务器现有清单中的记录，避免连续发布时丢失历史。

服务器使用 Nginx 提供静态下载，并通过 Let’s Encrypt 的短周期 IP 证书启用 HTTPS。可追踪的服务配置位于 `deploy/nginx/` 和 `deploy/systemd/`；续期定时器每天检查两次，证书更新后自动热加载 Nginx。部署脚本默认连接 `root@124.223.218.142`，可通过 `VOICE_QA_UPDATE_HOST` 和 `VOICE_QA_UPDATE_DIR` 覆盖目标。

服务器目录 `/srv/voice-qa/` 中必须同时存在以下两个文件：

```text
latest.json
tts-gui-windows-amd64-v<version>.zip
```

`latest.json` 中的 `url` 是当前 zip 文件名，GUI 会相对于清单地址解析为同一服务器目录下的 zip。`history` 仅包含版本、日期和变更说明，不包含旧安装包地址；GUI 的“历史变更”按钮用于查看这些记录，不提供历史版本下载。部署脚本会先上传并校验新 zip，再替换 `latest.json`，最后删除服务器目录中的其他 zip，因此云服务器始终只保留最新安装包，同时避免客户端读到尚未上传完成的版本。发布后可在任意公网电脑验证：

```bash
curl -fL https://124.223.218.142/voice-qa/latest.json
```

GUI 下载时显示实时进度，完成 SHA256 校验后会复制当前 EXE 并启动独立的 Windows 原生更新模式，不依赖 PowerShell。原生更新器等待 GUI 完整退出后备份并覆盖程序文件，遇到短暂文件占用时自动重试；它会保留 `output/`、`config.json`、`text.txt` 和不属于新包的用户文件，成功后删除下载的 zip 并自动重启原路径下的 EXE。安装失败会回滚并重新打开旧版本，详细记录写入 `output/update-install.log`。启动更新前还会检查当前程序目录是否可写；如果程序位于需要管理员权限的目录，应先整体移动到普通用户可写目录。大文件下载不设置固定总时限，只要持续收到数据就会继续；若连续 5 分钟没有任何下载数据，程序才会中止并提示检查网络。局域网 IIS 仍可作为手动填写的自定义更新源，见 [Windows GUI 局域网更新指南](docs/windows-gui-lan-update.md)。

更新目录采用逐文件合并：仅替换新包携带的同名文件，保留 `models/`、`adb/` 等子目录中额外添加的文件；遇到文件与目录类型冲突或符号链接时中止并回滚，不直接删除用户路径。

本地回归检查：`go test ./...`、`go vet ./...`；前端交互回归使用 `node --test gui/frontend/tests/regression.test.cjs`，无需安装前端依赖。

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
