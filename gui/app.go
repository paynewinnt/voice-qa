package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"voice-qa/internal/adb"
	"voice-qa/internal/audio"
	"voice-qa/internal/config"
	"voice-qa/internal/player"
	"voice-qa/internal/tts"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const defaultModel = "zh_CN-huayan-medium.onnx"

// App struct
type App struct {
	ctx              context.Context
	cfg              *config.Config
	ttsEngine        tts.Engine // TTS 引擎接口
	generateCmd      *exec.Cmd  // 当前生成进程
	playCmd          *exec.Cmd  // 当前播放进程
	playDir          string     // 播放目录（默认为 output）
	playDevice       string     // 播放模式目标设备序列号
	activePlayDevice string
	cmdMu            sync.Mutex // 保护 generateCmd 和 playCmd 的互斥锁
}

// NewApp 创建新的 App 实例
func NewApp() *App {
	return &App{}
}

// startup 在应用启动时调用
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// 加载配置
	cfg, err := config.LoadOrCreate("")
	if err != nil {
		cfg = config.DefaultConfig()
	}
	a.cfg = cfg

	// 初始化 TTS 引擎
	a.initTTSEngine()
}

// shutdown 在应用关闭时清理后台任务和播放资源
func (a *App) shutdown(ctx context.Context) {
	a.cmdMu.Lock()
	genCancel := generateCancel
	playCancel := playCancel
	a.cmdMu.Unlock()

	if genCancel != nil {
		genCancel()
	}
	if playCancel != nil {
		playCancel()
	}

	player.StopAllPlayback()
	adb.Shutdown()
}

func (a *App) initTTSEngine() {
	voiceID := a.cfg.VoiceID
	if voiceID == "" {
		// 默认使用 Piper
		modelName := a.cfg.ModelFile
		if modelName == "" {
			modelName = defaultModel
		}
		modelPath := tts.FindModelPath(modelName)
		a.ttsEngine = tts.NewPiper(modelPath)
	} else {
		a.ttsEngine = tts.CreateEngine(voiceID)
	}
}

// GetConfig 获取当前配置
func (a *App) GetConfig() *config.Config {
	return a.cfg
}

// GetVoices 获取可用的声音列表
func (a *App) GetVoices() []tts.VoiceInfo {
	return tts.GetAvailableVoices()
}

// SetVoice 设置当前声音
func (a *App) SetVoice(voiceID string) {
	a.cfg.VoiceID = voiceID
	a.initTTSEngine()
}

// SaveConfig 保存配置
func (a *App) SaveConfig(cfg *config.Config) error {
	// 验证模板必须包含 $MAIN
	if len(cfg.Template) > 0 {
		hasMain := false
		for _, seg := range cfg.Template {
			if seg.Type == "voice" && seg.Text == "$MAIN" {
				hasMain = true
				break
			}
		}
		if !hasMain {
			return fmt.Errorf("模板必须包含 $MAIN（主文本）片段")
		}
	}

	nextCfg := cloneConfig(cfg)

	// 保存到文件
	configPath := config.FindConfigFile()
	if configPath == "" {
		if exePath, err := os.Executable(); err == nil {
			configPath = filepath.Join(filepath.Dir(exePath), "config.json")
		} else {
			configPath = "config.json"
		}
	}

	if err := nextCfg.Save(configPath); err != nil {
		return err
	}

	a.cfg = nextCfg
	a.initTTSEngine()
	return nil
}

func cloneConfig(cfg *config.Config) *config.Config {
	if cfg == nil {
		return config.DefaultConfig()
	}

	cloned := *cfg
	if cfg.Template != nil {
		cloned.Template = append([]config.TemplateSegment(nil), cfg.Template...)
	}
	return &cloned
}

// GetPlayDir 获取当前播放目录
func (a *App) GetPlayDir() string {
	if a.playDir == "" {
		return a.cfg.OutputDir
	}
	return a.playDir
}

// SetPlayDir 设置播放目录
func (a *App) SetPlayDir(dir string) {
	a.playDir = dir
}

// GetPlayDevice 获取当前播放目标设备
func (a *App) GetPlayDevice() string {
	a.cmdMu.Lock()
	defer a.cmdMu.Unlock()
	return a.playDevice
}

// SetPlayDevice 设置当前播放目标设备
func (a *App) SetPlayDevice(serial string) {
	a.cmdMu.Lock()
	a.playDevice = strings.TrimSpace(serial)
	a.cmdMu.Unlock()
}

// SelectPlayDir 选择播放目录
func (a *App) SelectPlayDir() (string, error) {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择播放目录",
	})
	if err != nil {
		return "", err
	}
	if dir != "" {
		a.playDir = dir
	}
	return dir, nil
}

// GetSubDirs 获取指定目录下的子目录列表
func (a *App) GetSubDirs(baseDir string) ([]string, error) {
	if baseDir == "" {
		baseDir = "."
	}

	// 获取绝对路径
	baseDir = resolveRelativeToExe(baseDir)

	var dirs []string

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return dirs, nil
	}

	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			dirs = append(dirs, entry.Name())
		}
	}

	return dirs, nil
}

// GetTextList 获取文本列表
func (a *App) GetTextList() ([]string, error) {
	textFile := a.cfg.TextFile
	if textFile == "" {
		textFile = "text.txt"
	}

	// 查找文件
	if !filepath.IsAbs(textFile) {
		absPath := resolveRelativeToExe(textFile)
		if _, err := os.Stat(absPath); err == nil {
			textFile = absPath
		}
	}

	data, err := os.ReadFile(textFile)
	if err != nil {
		return []string{}, nil
	}

	// 处理 UTF-8 BOM (Windows 记事本可能添加)
	content := string(data)
	content = strings.TrimPrefix(content, "\xef\xbb\xbf")

	lines := strings.Split(content, "\n")
	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result, nil
}

// SaveTextList 保存文本列表
func (a *App) SaveTextList(texts []string) error {
	textFile := a.cfg.TextFile
	if textFile == "" {
		textFile = "text.txt"
	}

	// 查找文件路径
	textFile = resolveRelativeToExe(textFile)

	content := strings.Join(texts, "\n")
	return os.WriteFile(textFile, []byte(content), 0644)
}

// GenerateResult 生成结果
type GenerateResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	File    string `json:"file"`
}

// GenerateSingle 生成单条语音
func (a *App) GenerateSingle(text string, simple bool) GenerateResult {
	outputDir := a.resolvedOutputDir()

	// 创建输出目录
	os.MkdirAll(outputDir, 0755)

	// 生成文件名
	safeName := sanitizeFileName(truncate(text, a.cfg.FileNameMaxLength))
	outputFile := filepath.Join(outputDir, safeName+".wav")

	var err error
	if simple {
		err = a.ttsEngine.Synthesize(text, outputFile)
	} else {
		err = a.generateFullAudio(text, outputFile)
	}

	if err != nil {
		return GenerateResult{Success: false, Message: err.Error()}
	}

	if err := upsertPlayManifestEntry(outputDir, playManifestEntry{
		Index:   0,
		Text:    strings.TrimSpace(text),
		WavFile: filepath.Base(outputFile),
	}); err != nil {
		return GenerateResult{
			Success: false,
			Message: fmt.Sprintf("生成成功，但写入 manifest 失败: %v", err),
			File:    outputFile,
		}
	}

	return GenerateResult{Success: true, Message: "生成成功", File: outputFile}
}

// GenerateBatch 批量生成语音
func (a *App) GenerateBatch(texts []string, simple bool) []GenerateResult {
	var results []GenerateResult
	manifestEntries := make([]playManifestEntry, 0, len(texts))

	outputDir := a.resolvedOutputDir()

	// 创建输出目录
	os.MkdirAll(outputDir, 0755)

	for i, text := range texts {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}

		// 发送进度事件
		runtime.EventsEmit(a.ctx, "generate-progress", map[string]interface{}{
			"current": i + 1,
			"total":   len(texts),
			"text":    text,
		})

		safeName := sanitizeFileName(truncate(text, a.cfg.FileNameMaxLength))
		outputFile := filepath.Join(outputDir, fmt.Sprintf("%04d%s.wav", i+1, safeName))

		var err error
		if simple {
			err = a.ttsEngine.Synthesize(text, outputFile)
		} else {
			err = a.generateFullAudio(text, outputFile)
		}

		if err != nil {
			results = append(results, GenerateResult{Success: false, Message: err.Error(), File: outputFile})
		} else {
			manifestEntries = append(manifestEntries, playManifestEntry{
				Index:   i + 1,
				Text:    text,
				WavFile: filepath.Base(outputFile),
			})
			results = append(results, GenerateResult{Success: true, Message: "生成成功", File: outputFile})
		}
	}

	if len(manifestEntries) > 0 {
		_ = writePlayManifest(outputDir, manifestEntries)
	}

	return results
}

// generateFullAudio 根据模板生成完整音频
func (a *App) generateFullAudio(text, outputPath string) error {
	// 创建临时目录
	tmpDir, err := os.MkdirTemp("", "tts-")
	if err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// 获取模板
	template := a.cfg.GetTemplate()

	// 用于存储生成的语音文件
	voiceFiles := make(map[string]string)
	voiceIndex := 0

	// 第一遍：生成所有语音文件
	for _, seg := range template {
		if seg.Type == "voice" {
			voiceText := seg.Text
			if voiceText == "$MAIN" {
				voiceText = text
			}

			voiceFile := filepath.Join(tmpDir, fmt.Sprintf("voice_%d.wav", voiceIndex))
			if err := a.ttsEngine.Synthesize(voiceText, voiceFile); err != nil {
				return fmt.Errorf("生成语音失败 [%s]: %w", voiceText, err)
			}
			voiceFiles[seg.Text] = voiceFile
			voiceIndex++
		}
	}

	// 第二遍：构建音频片段序列
	var segments []audio.AudioSegment

	for _, seg := range template {
		switch seg.Type {
		case "silence":
			if seg.Seconds > 0 {
				segments = append(segments, audio.AudioSegment{SilenceSeconds: seg.Seconds})
			}
		case "voice":
			if voiceFile, ok := voiceFiles[seg.Text]; ok {
				segments = append(segments, audio.AudioSegment{FilePath: voiceFile})
			}
		}
	}

	return audio.ConcatWAVFiles(segments, outputPath)
}

// CheckAdbDevice 检查 ADB 设备
func (a *App) CheckAdbDevice() error {
	return adb.CheckDevice()
}

// PlayResult 播放结果
type PlayResult struct {
	Success    bool   `json:"success"`
	Message    string `json:"message"`
	WavFile    string `json:"wavFile"`
	LogFile    string `json:"logFile"`
	Screenshot string `json:"screenshot"`
}

// PlaySingle 播放单条语音
func (a *App) PlaySingle(wavFile string) PlayResult {
	// 获取音频时长
	duration, err := player.GetWAVDuration(wavFile)
	if err != nil {
		return PlayResult{Success: false, Message: fmt.Sprintf("获取音频时长失败: %v", err)}
	}

	// 生成日志和截图文件名
	baseName := strings.TrimSuffix(wavFile, ".wav")
	logFile := baseName + ".log"
	pngFile := baseName + ".png"

	// 启动 logcat
	recorder, err := adb.StartLogcat(logFile)
	if err != nil {
		return PlayResult{Success: false, Message: fmt.Sprintf("启动logcat失败: %v", err)}
	}

	// 计算截图时间
	screenshotTime := duration - a.cfg.ScreenshotBeforeEnd
	if screenshotTime < 0 {
		screenshotTime = duration / 2
	}

	// 截图定时器
	screenshotDone := make(chan bool, 1)
	go func() {
		// 等待截图时间后再截图
		time.Sleep(time.Duration(screenshotTime * float64(time.Second)))
		adb.Screenshot(pngFile)
		screenshotDone <- true
	}()

	// 播放（阻塞直到播放完成）
	if err := player.Play(wavFile); err != nil {
		recorder.Stop()
		return PlayResult{Success: false, Message: fmt.Sprintf("播放失败: %v", err)}
	}

	// 等待截图完成
	select {
	case <-screenshotDone:
	case <-time.After(5 * time.Second):
		// 截图超时，继续
	}

	// 停止 logcat 记录
	recorder.Stop()

	return PlayResult{
		Success:    true,
		Message:    "播放完成",
		WavFile:    wavFile,
		LogFile:    logFile,
		Screenshot: pngFile,
	}
}

// SelectFile 选择文件
func (a *App) SelectFile(title string, filters []string) (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: title,
		Filters: []runtime.FileFilter{
			{DisplayName: "文本文件", Pattern: "*.txt"},
			{DisplayName: "所有文件", Pattern: "*.*"},
		},
	})
}

// SelectDirectory 选择目录
func (a *App) SelectDirectory(title string) (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: title,
	})
}

// OpenDirectory 打开目录
func (a *App) OpenDirectory(path string) error {
	path = resolveRelativeToExe(path)

	// 确保目录存在
	os.MkdirAll(path, 0755)

	// Windows 使用 explorer 打开目录
	if goruntime.GOOS == "windows" {
		cmd := exec.Command("explorer", path)
		return cmd.Start()
	}

	// 其他系统使用默认方式
	runtime.BrowserOpenURL(a.ctx, "file://"+path)
	return nil
}

// OpenTextFile 打开文本文件进行编辑
func (a *App) OpenTextFile() error {
	textFile := a.cfg.TextFile
	if textFile == "" {
		textFile = "text.txt"
	}

	// 获取绝对路径
	textFile = resolveRelativeToExe(textFile)

	// 如果文件不存在，创建空文件
	if _, err := os.Stat(textFile); os.IsNotExist(err) {
		os.WriteFile(textFile, []byte(""), 0644)
	}

	// Windows 使用 notepad 打开文件
	if goruntime.GOOS == "windows" {
		cmd := exec.Command("notepad.exe", textFile)
		return cmd.Start()
	}

	// 其他系统使用默认程序
	runtime.BrowserOpenURL(a.ctx, "file://"+textFile)
	return nil
}

// OpenTestReport 打开测试报告文件
func (a *App) OpenTestReport() error {
	reportFile := latestPlayReportForDir(a.resolvedPlayDir())
	if reportFile == "" {
		return fmt.Errorf("测试报告不存在，请先执行播放模式")
	}

	// Windows 使用 notepad 打开文件
	if goruntime.GOOS == "windows" {
		cmd := exec.Command("notepad.exe", reportFile)
		return cmd.Start()
	}

	// 其他系统使用默认程序
	runtime.BrowserOpenURL(a.ctx, "file://"+reportFile)
	return nil
}

// AdbResult ADB 操作结果
type AdbResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// ConnectAdb 连接 ADB 设备
func (a *App) ConnectAdb(ip string) AdbResult {
	result, err := adb.Connect(ip)
	if err != nil {
		return AdbResult{Success: false, Message: err.Error()}
	}
	return AdbResult{Success: true, Message: result}
}

// DisconnectDevice 断开指定 ADB 设备
func (a *App) DisconnectDevice(serial string) AdbResult {
	cmd := adb.Command("disconnect", serial)
	hideConsoleWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return AdbResult{Success: false, Message: fmt.Sprintf("断开失败: %s %v", string(output), err)}
	}
	return AdbResult{Success: true, Message: fmt.Sprintf("已断开 %s", serial)}
}

// DisconnectAllDevices 断开所有 ADB 设备
func (a *App) DisconnectAllDevices() AdbResult {
	cmd := adb.Command("disconnect")
	hideConsoleWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return AdbResult{Success: false, Message: fmt.Sprintf("断开失败: %s %v", string(output), err)}
	}
	return AdbResult{Success: true, Message: "已断开所有设备"}
}

// SelectApkFiles 打开文件对话框选择 APK 文件（支持多选）
func (a *App) SelectApkFiles() ([]string, error) {
	return runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择 APK 文件",
		Filters: []runtime.FileFilter{
			{DisplayName: "APK 文件 (*.apk)", Pattern: "*.apk"},
			{DisplayName: "所有文件", Pattern: "*.*"},
		},
	})
}

// InstallApkResult 安装 APK 结果
type InstallApkResult struct {
	Serial  string `json:"serial"`
	ApkFile string `json:"apkFile"`
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// InstallApk 安装 APK 到指定设备
func (a *App) InstallApk(serial, apkPath string) InstallApkResult {
	baseName := filepath.Base(apkPath)
	cmd := adb.Command("-s", serial, "install", "-r", apkPath)
	hideConsoleWindow(cmd)
	output, err := cmd.CombinedOutput()
	outStr := strings.TrimSpace(string(output))
	if err != nil {
		return InstallApkResult{Serial: serial, ApkFile: baseName, Success: false, Message: fmt.Sprintf("%s: %v", outStr, err)}
	}
	if strings.Contains(outStr, "Success") {
		return InstallApkResult{Serial: serial, ApkFile: baseName, Success: true, Message: "安装成功"}
	}
	return InstallApkResult{Serial: serial, ApkFile: baseName, Success: false, Message: outStr}
}

// AdbCommandResult ADB 命令执行结果
type AdbCommandResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Command string `json:"command"`
	Output  string `json:"output"`
}

// RunAdbCommand 执行 adb 命令，adb 仅使用程序目录或当前目录下的 adb/adb.exe
func (a *App) RunAdbCommand(serial, commandText string) AdbCommandResult {
	commandText = strings.TrimSpace(commandText)
	if commandText == "" {
		return AdbCommandResult{Success: false, Message: "请输入 ADB 命令"}
	}

	args, err := adb.ParseArgs(commandText)
	if err != nil {
		return AdbCommandResult{Success: false, Message: err.Error()}
	}
	if len(args) == 0 {
		return AdbCommandResult{Success: false, Message: "请输入有效的 ADB 命令"}
	}

	if strings.EqualFold(args[0], "adb") {
		args = args[1:]
	}
	if len(args) == 0 {
		return AdbCommandResult{Success: false, Message: "请输入 adb 后面的命令参数"}
	}

	if serial != "" && !hasAdbSerialArg(args) {
		args = append([]string{"-s", serial}, args...)
	}

	cmd := adb.Command(args...)
	hideConsoleWindow(cmd)
	output, err := cmd.CombinedOutput()
	outStr := strings.TrimSpace(string(output))
	displayCmd := "adb " + strings.Join(args, " ")

	if err != nil {
		message := err.Error()
		if outStr != "" {
			message = outStr
		}
		return AdbCommandResult{
			Success: false,
			Message: message,
			Command: displayCmd,
			Output:  outStr,
		}
	}

	if outStr == "" {
		outStr = "(无输出)"
	}

	return AdbCommandResult{
		Success: true,
		Message: "命令执行成功",
		Command: displayCmd,
		Output:  outStr,
	}
}

func hasAdbSerialArg(args []string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-s" && strings.TrimSpace(args[i+1]) != "" {
			return true
		}
	}
	return false
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen])
}

func sanitizeFileName(s string) string {
	var result strings.Builder
	for _, r := range s {
		if isPunctuation(r) {
			result.WriteRune('_')
		} else if r != '\n' && r != '\r' {
			result.WriteRune(r)
		}
	}
	return result.String()
}

func isPunctuation(r rune) bool {
	punctuations := `，。！？、；：""''（）【】《》·…—～,.!?;:"'()[]<>/-\|*@#$%^&+=` + "`~"
	return strings.ContainsRune(punctuations, r)
}

// legacyRunGenerate 保留旧版入口，转发到当前 GUI 内置批量生成流程。
func (a *App) legacyRunGenerate() {
	go a.runGenerateInBackground()
}

// legacyRunGenerateInBackground 保留旧版后台生成实现，供兼容路径使用。
func (a *App) legacyRunGenerateInBackground() {
	// 获取 exe 所在目录
	exePath, err := os.Executable()
	if err != nil {
		runtime.EventsEmit(a.ctx, "generate-output", map[string]interface{}{
			"type":    "error",
			"message": fmt.Sprintf("获取程序路径失败: %v", err),
		})
		return
	}
	exeDir := filepath.Dir(exePath)

	// tts.exe 路径
	ttsExe := filepath.Join(exeDir, "tts.exe")
	if goruntime.GOOS != "windows" {
		ttsExe = filepath.Join(exeDir, "tts")
	}

	if _, err := os.Stat(ttsExe); os.IsNotExist(err) {
		runtime.EventsEmit(a.ctx, "generate-output", map[string]interface{}{
			"type":    "error",
			"message": "找不到 tts 命令行工具",
		})
		return
	}

	// 运行 tts.exe
	cmd := exec.Command(ttsExe)
	cmd.Dir = exeDir
	hideConsoleWindow(cmd) // 隐藏控制台窗口
	a.cmdMu.Lock()
	a.generateCmd = cmd // 保存进程引用
	a.cmdMu.Unlock()

	// 获取输出管道
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		runtime.EventsEmit(a.ctx, "generate-output", map[string]interface{}{
			"type":    "error",
			"message": fmt.Sprintf("获取输出失败: %v", err),
		})
		return
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		runtime.EventsEmit(a.ctx, "generate-output", map[string]interface{}{
			"type":    "error",
			"message": fmt.Sprintf("获取错误输出失败: %v", err),
		})
		return
	}

	// 启动命令
	if err := cmd.Start(); err != nil {
		runtime.EventsEmit(a.ctx, "generate-output", map[string]interface{}{
			"type":    "error",
			"message": fmt.Sprintf("启动失败: %v", err),
		})
		return
	}

	runtime.EventsEmit(a.ctx, "generate-output", map[string]interface{}{
		"type":    "start",
		"message": "开始批量生成语音...",
	})

	// 读取输出并推送到前端
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			runtime.EventsEmit(a.ctx, "generate-output", map[string]interface{}{
				"type":    "output",
				"message": line,
			})
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			runtime.EventsEmit(a.ctx, "generate-output", map[string]interface{}{
				"type":    "error",
				"message": line,
			})
		}
	}()

	// 等待完成
	err = cmd.Wait()
	a.cmdMu.Lock()
	a.generateCmd = nil // 清除进程引用
	a.cmdMu.Unlock()
	if err != nil {
		runtime.EventsEmit(a.ctx, "generate-output", map[string]interface{}{
			"type":    "done",
			"message": fmt.Sprintf("执行完成(有错误): %v", err),
		})
	} else {
		runtime.EventsEmit(a.ctx, "generate-output", map[string]interface{}{
			"type":    "done",
			"message": "批量生成完成！",
		})
	}
}

// legacyRunPlay 保留旧版入口，转发到当前 GUI 内置播放流程。
func (a *App) legacyRunPlay() {
	go a.runPlayInBackground()
}

// legacyRunPlayInBackground 保留旧版后台播放实现，供兼容路径使用。
func (a *App) legacyRunPlayInBackground() {
	// 获取 exe 所在目录
	exePath, err := os.Executable()
	if err != nil {
		runtime.EventsEmit(a.ctx, "play-output", map[string]interface{}{
			"type":    "error",
			"message": fmt.Sprintf("获取程序路径失败: %v", err),
		})
		return
	}
	exeDir := filepath.Dir(exePath)

	// tts.exe 路径
	ttsExe := filepath.Join(exeDir, "tts.exe")
	if goruntime.GOOS != "windows" {
		ttsExe = filepath.Join(exeDir, "tts")
	}

	if _, err := os.Stat(ttsExe); os.IsNotExist(err) {
		runtime.EventsEmit(a.ctx, "play-output", map[string]interface{}{
			"type":    "error",
			"message": "找不到 tts 命令行工具",
		})
		return
	}

	// 运行 tts.exe -play [-playdir dir]
	args := []string{"-play"}
	if a.playDir != "" {
		args = append(args, "-playdir", a.playDir)
	}
	cmd := exec.Command(ttsExe, args...)
	cmd.Dir = exeDir
	hideConsoleWindow(cmd) // 隐藏控制台窗口
	a.cmdMu.Lock()
	a.playCmd = cmd // 保存进程引用
	a.cmdMu.Unlock()

	// 获取输出管道
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		runtime.EventsEmit(a.ctx, "play-output", map[string]interface{}{
			"type":    "error",
			"message": fmt.Sprintf("获取输出失败: %v", err),
		})
		return
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		runtime.EventsEmit(a.ctx, "play-output", map[string]interface{}{
			"type":    "error",
			"message": fmt.Sprintf("获取错误输出失败: %v", err),
		})
		return
	}

	// 启动命令
	if err := cmd.Start(); err != nil {
		runtime.EventsEmit(a.ctx, "play-output", map[string]interface{}{
			"type":    "error",
			"message": fmt.Sprintf("启动失败: %v", err),
		})
		return
	}

	runtime.EventsEmit(a.ctx, "play-output", map[string]interface{}{
		"type":    "start",
		"message": "开始播放模式...",
	})

	// 读取输出并推送到前端
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			runtime.EventsEmit(a.ctx, "play-output", map[string]interface{}{
				"type":    "output",
				"message": line,
			})
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			runtime.EventsEmit(a.ctx, "play-output", map[string]interface{}{
				"type":    "error",
				"message": line,
			})
		}
	}()

	// 等待完成
	err = cmd.Wait()
	a.cmdMu.Lock()
	a.playCmd = nil // 清除进程引用
	a.cmdMu.Unlock()
	if err != nil {
		runtime.EventsEmit(a.ctx, "play-output", map[string]interface{}{
			"type":    "done",
			"message": fmt.Sprintf("执行完成(有错误): %v", err),
		})
	} else {
		runtime.EventsEmit(a.ctx, "play-output", map[string]interface{}{
			"type":    "done",
			"message": "播放模式完成！",
		})
	}
}

// legacyStopPlay 保留旧版停止播放实现，供兼容路径使用。
func (a *App) legacyStopPlay() error {
	a.cmdMu.Lock()
	cmd := a.playCmd
	a.cmdMu.Unlock()

	if cmd != nil && cmd.Process != nil {
		runtime.EventsEmit(a.ctx, "play-output", map[string]interface{}{
			"type":    "output",
			"message": "正在停止，等待资源清理...",
		})

		// 1. 停止音频播放
		player.StopAllPlayback()

		// 2. 停止所有 adb 相关进程（screenrecord、logcat 等）
		// 这会优雅地停止 screenrecord 确保 MP4 文件正确结束
		adb.StopAllAdbProcesses()

		// 3. 终止播放进程
		err := cmd.Process.Kill()
		a.cmdMu.Lock()
		a.playCmd = nil
		a.cmdMu.Unlock()
		if err != nil {
			return fmt.Errorf("停止失败: %w", err)
		}
		runtime.EventsEmit(a.ctx, "play-output", map[string]interface{}{
			"type":    "done",
			"message": "已停止播放",
		})
	}
	return nil
}

// ===== 性能测试相关方法 =====

// GetConnectedDevices 获取已连接的 ADB 设备列表
func (a *App) GetConnectedDevices() ([]string, error) {
	cmd := adb.Command("devices")
	hideConsoleWindow(cmd)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("执行 adb devices 失败: %w", err)
	}

	var devices []string
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of") || strings.HasPrefix(line, "*") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[1] == "device" {
			devices = append(devices, parts[0])
		}
	}
	return devices, nil
}

// GetDevicePackages 获取设备上的所有包名
func (a *App) GetDevicePackages(serial string) ([]string, error) {
	cmd := adb.Command("-s", serial, "shell", "pm", "list", "packages")
	hideConsoleWindow(cmd)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("获取包列表失败: %w", err)
	}

	var packages []string
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		pkg := strings.TrimPrefix(line, "package:")
		pkg = strings.TrimSpace(pkg)
		if pkg != "" {
			packages = append(packages, pkg)
		}
	}
	return packages, nil
}

// PerfPrepColdStart 冷启动准备：force-stop + pm clear
func (a *App) PerfPrepColdStart(serial, packageName string) AdbResult {
	// force-stop
	cmd1 := adb.Command("-s", serial, "shell", "am", "force-stop", packageName)
	hideConsoleWindow(cmd1)
	if out, err := cmd1.CombinedOutput(); err != nil {
		return AdbResult{Success: false, Message: fmt.Sprintf("force-stop 失败: %s %v", string(out), err)}
	}

	// pm clear
	cmd2 := adb.Command("-s", serial, "shell", "pm", "clear", packageName)
	hideConsoleWindow(cmd2)
	if out, err := cmd2.CombinedOutput(); err != nil {
		return AdbResult{Success: false, Message: fmt.Sprintf("pm clear 失败: %s %v", string(out), err)}
	}

	return AdbResult{Success: true, Message: "force-stop 和 pm clear 完成"}
}

// PerfForceStopAll 停止所有第三方应用（包括前台正在运行的）
func (a *App) PerfForceStopAll(serial string) AdbResult {
	// 获取所有第三方包并逐个 force-stop
	listCmd := adb.Command("-s", serial, "shell", "pm", "list", "packages", "-3")
	hideConsoleWindow(listCmd)
	listOutput, err := listCmd.Output()
	if err != nil {
		// 回退方案
		cmd2 := adb.Command("-s", serial, "shell", "am", "kill-all")
		hideConsoleWindow(cmd2)
		cmd2.CombinedOutput()
		return AdbResult{Success: true, Message: "已执行 am kill-all（获取第三方包列表失败）"}
	}

	stopped := 0
	lines := strings.Split(string(listOutput), "\n")
	for _, line := range lines {
		pkg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "package:"))
		if pkg == "" {
			continue
		}
		stopCmd := adb.Command("-s", serial, "shell", "am", "force-stop", pkg)
		hideConsoleWindow(stopCmd)
		stopCmd.Run()
		stopped++
	}

	return AdbResult{Success: true, Message: fmt.Sprintf("已 force-stop %d 个第三方应用", stopped)}
}

// PerfGoHome 模拟按两次返回键回到首页
func (a *App) PerfGoHome(serial string) AdbResult {
	// 按两次返回键
	for i := 0; i < 2; i++ {
		cmd := adb.Command("-s", serial, "shell", "input", "keyevent", "KEYCODE_BACK")
		hideConsoleWindow(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			return AdbResult{Success: false, Message: fmt.Sprintf("第%d次返回键失败: %s %v", i+1, string(out), err)}
		}
		if i == 0 {
			time.Sleep(300 * time.Millisecond)
		}
	}
	return AdbResult{Success: true, Message: "已按两次返回键"}
}

// PerfLaunchResult 性能启动结果
type PerfLaunchResult struct {
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	Output    string `json:"output"`
	Timestamp string `json:"timestamp"` // 启动命令发出时的精确时间
}

// PerfLaunchActivity 启动 Activity 并返回 am start -W 的输出
func (a *App) PerfLaunchActivity(serial, component string) PerfLaunchResult {
	// 记录启动命令发出的精确时间
	startTime := time.Now()
	timestamp := startTime.Format("2006-01-02 15:04:05.000")

	cmd := adb.Command("-s", serial, "shell", "am", "start", "-W", "-n", component)
	hideConsoleWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return PerfLaunchResult{
			Success:   false,
			Message:   fmt.Sprintf("启动失败: %v", err),
			Output:    string(output),
			Timestamp: timestamp,
		}
	}

	return PerfLaunchResult{
		Success:   true,
		Message:   "启动成功",
		Output:    string(output),
		Timestamp: timestamp,
	}
}

// legacyStopGenerate 保留旧版停止生成实现，供兼容路径使用。
func (a *App) legacyStopGenerate() error {
	a.cmdMu.Lock()
	cmd := a.generateCmd
	a.cmdMu.Unlock()

	if cmd != nil && cmd.Process != nil {
		err := cmd.Process.Kill()
		a.cmdMu.Lock()
		a.generateCmd = nil
		a.cmdMu.Unlock()
		if err != nil {
			return fmt.Errorf("停止失败: %w", err)
		}
		runtime.EventsEmit(a.ctx, "generate-output", map[string]interface{}{
			"type":    "done",
			"message": "已停止生成",
		})
	}
	return nil
}

var (
	generateCancel context.CancelFunc
	playCancel     context.CancelFunc
)

func (a *App) emitTaskEvent(topic, eventType, message string) {
	runtime.EventsEmit(a.ctx, topic, map[string]interface{}{
		"type":    eventType,
		"message": message,
	})
}

func (a *App) emitGenerateOutput(message string) {
	a.emitTaskEvent("generate-output", "output", message)
}

func (a *App) emitGenerateError(message string) {
	a.emitTaskEvent("generate-output", "error", message)
}

func (a *App) emitGenerateDone(message string) {
	a.emitTaskEvent("generate-output", "done", message)
}

func (a *App) emitPlayOutput(message string) {
	a.emitTaskEvent("play-output", "output", message)
}

func (a *App) emitPlayError(message string) {
	a.emitTaskEvent("play-output", "error", message)
}

func (a *App) emitPlayDone(message string) {
	a.emitTaskEvent("play-output", "done", message)
}

func (a *App) beginGenerateTask(cancel context.CancelFunc) bool {
	a.cmdMu.Lock()
	defer a.cmdMu.Unlock()
	if generateCancel != nil {
		return false
	}
	generateCancel = cancel
	return true
}

func (a *App) finishGenerateTask() {
	a.cmdMu.Lock()
	generateCancel = nil
	a.cmdMu.Unlock()
}

func (a *App) beginPlayTask(cancel context.CancelFunc) bool {
	a.cmdMu.Lock()
	defer a.cmdMu.Unlock()
	if playCancel != nil {
		return false
	}
	playCancel = cancel
	return true
}

func (a *App) finishPlayTask() {
	a.cmdMu.Lock()
	playCancel = nil
	a.activePlayDevice = ""
	a.cmdMu.Unlock()
}

func resolveRelativeToExe(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	if exePath, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exePath), path)
	}
	return path
}

func (a *App) resolvedOutputDir() string {
	outputDir := a.cfg.OutputDir
	if outputDir == "" {
		outputDir = "output"
	}
	return resolveRelativeToExe(outputDir)
}

func (a *App) resolvedPlayDir() string {
	if a.playDir != "" {
		return resolveRelativeToExe(a.playDir)
	}
	return a.resolvedOutputDir()
}

func (a *App) resolvePlayDevice() (string, error) {
	a.cmdMu.Lock()
	serial := strings.TrimSpace(a.playDevice)
	a.cmdMu.Unlock()
	if serial != "" {
		return serial, nil
	}

	devices, err := a.GetConnectedDevices()
	if err != nil {
		return "", err
	}

	switch len(devices) {
	case 0:
		return "", fmt.Errorf("未检测到可用设备")
	case 1:
		a.SetPlayDevice(devices[0])
		return devices[0], nil
	default:
		return "", fmt.Errorf("检测到多台设备，请先选择目标设备")
	}
}

type playManifestEntry struct {
	Index   int    `json:"index"`
	Text    string `json:"text"`
	WavFile string `json:"wav_file"`
}

type playManifest struct {
	GeneratedAt string              `json:"generated_at"`
	Entries     []playManifestEntry `json:"entries"`
}

func writePlayManifest(outputDir string, entries []playManifestEntry) error {
	data, err := json.MarshalIndent(playManifest{
		GeneratedAt: time.Now().Format(time.RFC3339),
		Entries:     entries,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, "manifest.json"), data, 0644)
}

func readPlayManifest(wavDir string) (*playManifest, error) {
	data, err := os.ReadFile(filepath.Join(wavDir, "manifest.json"))
	if err != nil {
		return nil, err
	}

	var manifest playManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func loadPlayManifest(wavDir string) map[string]string {
	manifest, err := readPlayManifest(wavDir)
	if err != nil || manifest == nil {
		return nil
	}

	lookup := make(map[string]string, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if strings.TrimSpace(entry.WavFile) == "" || strings.TrimSpace(entry.Text) == "" {
			continue
		}
		lookup[filepath.Join(wavDir, entry.WavFile)] = entry.Text
	}
	return lookup
}

func upsertPlayManifestEntry(outputDir string, entry playManifestEntry) error {
	manifest, err := readPlayManifest(outputDir)
	if err != nil || manifest == nil {
		manifest = &playManifest{}
	}

	replaced := false
	for i := range manifest.Entries {
		if manifest.Entries[i].WavFile == entry.WavFile {
			manifest.Entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		manifest.Entries = append(manifest.Entries, entry)
	}
	return writePlayManifest(outputDir, manifest.Entries)
}

func createPlaySessionDir(playDir string) (string, string, error) {
	sessionDir := filepath.Join(playDir, fmt.Sprintf("playtest-%s-%d", time.Now().Format("20060102-150405"), time.Now().UnixNano()))
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return "", "", err
	}
	return sessionDir, filepath.Join(sessionDir, "test_report.txt"), nil
}

func latestPlayReportForDir(playDir string) string {
	entries, err := os.ReadDir(playDir)
	if err == nil {
		var latestPath string
		var latestTime time.Time
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "playtest-") {
				continue
			}
			reportPath := filepath.Join(playDir, entry.Name(), "test_report.txt")
			info, err := os.Stat(reportPath)
			if err != nil {
				continue
			}
			if latestPath == "" || info.ModTime().After(latestTime) {
				latestPath = reportPath
				latestTime = info.ModTime()
			}
		}
		if latestPath != "" {
			return latestPath
		}
	}

	legacyReport := filepath.Join(playDir, "test_report.txt")
	if _, err := os.Stat(legacyReport); err == nil {
		return legacyReport
	}
	return ""
}

func buildPlayPlan(texts []string, wavDir string, maxLen int) ([]string, map[string]string) {
	if manifest, err := readPlayManifest(wavDir); err == nil && manifest != nil && len(manifest.Entries) > 0 {
		wavFiles := make([]string, 0, len(manifest.Entries))
		lookup := make(map[string]string, len(manifest.Entries))
		for _, entry := range manifest.Entries {
			if strings.TrimSpace(entry.WavFile) == "" {
				continue
			}
			wavFile := filepath.Join(wavDir, entry.WavFile)
			if _, err := os.Stat(wavFile); err != nil {
				continue
			}
			wavFiles = append(wavFiles, wavFile)
			if strings.TrimSpace(entry.Text) != "" {
				lookup[wavFile] = entry.Text
			}
		}
		if len(wavFiles) > 0 {
			return wavFiles, lookup
		}
	}

	lookup := buildPlayTextLookup(texts, wavDir, maxLen)
	entries, err := os.ReadDir(wavDir)
	if err != nil {
		return nil, lookup
	}

	var wavFiles []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".wav") {
			continue
		}
		wavFiles = append(wavFiles, filepath.Join(wavDir, entry.Name()))
	}
	sort.Strings(wavFiles)
	return wavFiles, lookup
}

type playTestResult struct {
	Text       string
	LogFile    string
	PngFile    string
	WavFile    string
	Passed     bool
	AssertInfo string
}

func buildPlayTextLookup(texts []string, wavDir string, maxLen int) map[string]string {
	lookup := loadPlayManifest(wavDir)
	if lookup == nil {
		lookup = make(map[string]string)
	}
	for i, line := range texts {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\xef\xbb\xbf"))
		if line == "" {
			continue
		}
		safeName := sanitizeFileName(truncate(line, maxLen))
		wavPath := filepath.Join(wavDir, fmt.Sprintf("%04d%s.wav", i+1, safeName))
		if _, exists := lookup[wavPath]; !exists {
			lookup[wavPath] = line
		}
	}
	return lookup
}

func extractTextFromFileName(fileName string) string {
	base := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	if len(base) > 4 {
		prefix := base[:4]
		allDigit := true
		for _, r := range prefix {
			if r < '0' || r > '9' {
				allDigit = false
				break
			}
		}
		if allDigit {
			return base[4:]
		}
	}
	return base
}

func assertLogContent(logFile, expectedQuery string) (bool, string) {
	data, err := os.ReadFile(logFile)
	if err != nil {
		return false, fmt.Sprintf("无法读取日志文件: %v", err)
	}

	content := string(data)
	expectedPattern := fmt.Sprintf(`"query":"%s"`, expectedQuery)
	if strings.Contains(content, expectedPattern) {
		return true, fmt.Sprintf("找到匹配: %s", expectedPattern)
	}
	if strings.Contains(content, `"nlpResult"`) {
		return false, fmt.Sprintf("日志包含 nlpResult 但未找到 query=\"%s\"", expectedQuery)
	}
	return false, "日志中未找到 nlpResult 相关内容"
}

func trimLogBeforeMarker(logFile, marker string) error {
	if strings.TrimSpace(marker) == "" {
		return nil
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")
	start := -1
	for i, line := range lines {
		if strings.Contains(line, marker) {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return fmt.Errorf("日志中未找到起始标记")
	}

	trimmed := strings.Join(lines[start:], "\n")
	return os.WriteFile(logFile, []byte(trimmed), 0644)
}

func savePlayTestReport(reportFile string, results []playTestResult, passCount, failCount int, complete bool) {
	var sb strings.Builder
	sb.WriteString("================== 测试执行报告 ==================\n")
	sb.WriteString(fmt.Sprintf("执行时间: %s\n", time.Now().Format("2006-01-02 15:04:05")))
	if complete {
		sb.WriteString("执行状态: 已完成\n")
	} else {
		sb.WriteString("执行状态: 执行中/已中断\n")
	}
	sb.WriteString(fmt.Sprintf("总计: %d, 通过: %d, 失败: %d\n\n", passCount+failCount, passCount, failCount))
	sb.WriteString("------------------ 详细结果 ------------------\n")

	for i, r := range results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		sb.WriteString(fmt.Sprintf("\n[%d] %s\n", i+1, r.Text))
		sb.WriteString(fmt.Sprintf("    状态: %s\n", status))
		sb.WriteString(fmt.Sprintf("    断言: %s\n", r.AssertInfo))
		sb.WriteString(fmt.Sprintf("    日志: %s\n", filepath.Base(r.LogFile)))
		sb.WriteString(fmt.Sprintf("    截图: %s\n", filepath.Base(r.PngFile)))
	}

	sb.WriteString("\n==================================================\n")
	_ = os.WriteFile(reportFile, []byte(sb.String()), 0644)
}

func (a *App) playWithArtifacts(ctx context.Context, serial, wavFile, logFile, pngFile, mp4File string) error {
	duration, err := player.GetWAVDuration(wavFile)
	if err != nil {
		return fmt.Errorf("获取音频时长失败: %w", err)
	}

	logRecorder, err := adb.StartLogcatForDevice(serial, logFile)
	if err != nil {
		return fmt.Errorf("启动 logcat 失败: %w", err)
	}

	logMarker := fmt.Sprintf("voice-qa-play-%d", time.Now().UnixNano())
	trimLog := func() error { return nil }
	if err := adb.WriteLogMarker(serial, "voice-qa", logMarker); err != nil {
		a.emitPlayOutput(fmt.Sprintf("日志标记写入失败，将保留完整日志: %v", err))
	} else {
		trimLog = func() error {
			return trimLogBeforeMarker(logFile, logMarker)
		}
	}

	stopLogcat := func() error {
		if err := logRecorder.Stop(); err != nil {
			return err
		}
		return trimLog()
	}

	time.Sleep(500 * time.Millisecond)

	screenshotTime := duration - a.cfg.ScreenshotBeforeEnd
	if screenshotTime < 0 {
		screenshotTime = duration / 2
	}

	screenshotDone := make(chan struct{})
	go func() {
		defer close(screenshotDone)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(screenshotTime * float64(time.Second))):
		}

		if err := adb.ScreenshotForDevice(serial, pngFile); err != nil {
			a.emitPlayError(fmt.Sprintf("截图失败: %v", err))
			return
		}
		a.emitPlayOutput(fmt.Sprintf("截图完成: %s", filepath.Base(pngFile)))
	}()

	videoDone := make(chan struct{})
	if a.cfg.EnableVideoRecording {
		go func() {
			defer close(videoDone)

			recordingDuration := duration - a.cfg.RecordingStartDelay - a.cfg.RecordingEndBeforeEnd
			if recordingDuration <= 0 {
				a.emitPlayOutput("音频过短，已跳过视频录制")
				return
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(a.cfg.RecordingStartDelay * float64(time.Second))):
			}

			recorder, err := adb.StartVideoRecordingForDevice(serial, mp4File, int(recordingDuration)+5)
			if err != nil {
				a.emitPlayError(fmt.Sprintf("启动录制失败: %v", err))
				return
			}
			a.emitPlayOutput(fmt.Sprintf("开始录制: %s", filepath.Base(mp4File)))

			select {
			case <-ctx.Done():
			case <-time.After(time.Duration(recordingDuration * float64(time.Second))):
			}

			if err := recorder.Stop(); err != nil {
				a.emitPlayError(fmt.Sprintf("停止录制失败: %v", err))
				return
			}
			a.emitPlayOutput(fmt.Sprintf("录制完成: %s", filepath.Base(mp4File)))
		}()
	} else {
		close(videoDone)
	}

	audioCmd, err := player.PlayAsync(wavFile)
	if err != nil {
		if stopErr := stopLogcat(); stopErr != nil {
			return fmt.Errorf("播放失败: %w；停止日志采集失败: %v", err, stopErr)
		}
		return fmt.Errorf("播放失败: %w", err)
	}

	waitAudio := make(chan error, 1)
	go func() {
		waitAudio <- audioCmd.Wait()
	}()

	select {
	case err = <-waitAudio:
		if err != nil {
			stopErr := stopLogcat()
			<-screenshotDone
			<-videoDone
			if stopErr != nil {
				return fmt.Errorf("播放失败: %w；停止日志采集失败: %v", err, stopErr)
			}
			return fmt.Errorf("播放失败: %w", err)
		}
	case <-ctx.Done():
		player.StopAllPlayback()
		adb.StopAllAdbProcessesForDevice(serial)
		<-waitAudio
		if stopErr := stopLogcat(); stopErr != nil {
			<-screenshotDone
			<-videoDone
			return fmt.Errorf("停止日志采集失败: %w", stopErr)
		}
		<-screenshotDone
		<-videoDone
		return ctx.Err()
	}

	<-screenshotDone
	<-videoDone

	select {
	case <-ctx.Done():
		if stopErr := stopLogcat(); stopErr != nil {
			return fmt.Errorf("停止日志采集失败: %w", stopErr)
		}
		return ctx.Err()
	case <-time.After(2 * time.Second):
	}

	if err := stopLogcat(); err != nil {
		return fmt.Errorf("停止日志采集失败: %w", err)
	}
	return nil
}

// RunGenerate starts batch generation directly in the GUI process.
func (a *App) RunGenerate() {
	go a.runGenerateInBackground()
}

func (a *App) runGenerateInBackground() {
	ctx, cancel := context.WithCancel(context.Background())
	if !a.beginGenerateTask(cancel) {
		a.emitGenerateError("当前已有批量生成任务在运行")
		a.emitGenerateDone("批量生成未启动")
		return
	}
	defer a.finishGenerateTask()

	texts, err := a.GetTextList()
	if err != nil {
		a.emitGenerateError(fmt.Sprintf("读取文本失败: %v", err))
		a.emitGenerateDone("批量生成失败")
		return
	}
	if len(texts) == 0 {
		a.emitGenerateError("text.txt 中没有可生成的文本")
		a.emitGenerateDone("批量生成未启动")
		return
	}

	outputDir := a.resolvedOutputDir()
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		a.emitGenerateError(fmt.Sprintf("创建输出目录失败: %v", err))
		a.emitGenerateDone("批量生成失败")
		return
	}

	a.emitTaskEvent("generate-output", "start", "开始批量生成语音...")
	a.emitGenerateOutput(fmt.Sprintf("共 %d 条文本，输出目录: %s", len(texts), outputDir))

	successCount := 0
	failCount := 0
	manifestEntries := make([]playManifestEntry, 0, len(texts))

	for i, text := range texts {
		if ctx.Err() != nil {
			a.emitGenerateDone("已停止生成")
			return
		}

		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}

		a.emitGenerateOutput(fmt.Sprintf("[%d/%d] %s", i+1, len(texts), truncate(text, 30)))
		safeName := sanitizeFileName(truncate(text, a.cfg.FileNameMaxLength))
		outputFile := filepath.Join(outputDir, fmt.Sprintf("%04d%s.wav", i+1, safeName))

		if err := a.generateFullAudio(text, outputFile); err != nil {
			failCount++
			a.emitGenerateError(fmt.Sprintf("生成失败: %s -> %v", filepath.Base(outputFile), err))
			continue
		}

		successCount++
		manifestEntries = append(manifestEntries, playManifestEntry{
			Index:   i + 1,
			Text:    text,
			WavFile: filepath.Base(outputFile),
		})
		a.emitGenerateOutput(fmt.Sprintf("生成完成 -> %s", filepath.Base(outputFile)))
	}

	if ctx.Err() != nil {
		a.emitGenerateDone("已停止生成")
		return
	}

	if len(manifestEntries) > 0 {
		if err := writePlayManifest(outputDir, manifestEntries); err != nil {
			a.emitGenerateError(fmt.Sprintf("写入 manifest 失败: %v", err))
		}
	}

	a.emitGenerateDone(fmt.Sprintf("批量生成完成，成功 %d 条，失败 %d 条", successCount, failCount))
}

// RunPlay starts play mode directly in the GUI process.
func (a *App) RunPlay() {
	go a.runPlayInBackground()
}

func (a *App) runPlayInBackground() {
	ctx, cancel := context.WithCancel(context.Background())
	if !a.beginPlayTask(cancel) {
		a.emitPlayError("当前已有播放任务在运行")
		a.emitPlayDone("播放未启动")
		return
	}
	defer a.finishPlayTask()

	serial, err := a.resolvePlayDevice()
	if err != nil {
		a.emitPlayError(err.Error())
		a.emitPlayDone("播放未启动")
		return
	}
	a.cmdMu.Lock()
	a.activePlayDevice = serial
	a.cmdMu.Unlock()

	wavDir := a.resolvedPlayDir()
	texts, _ := a.GetTextList()
	wavFiles, textLookup := buildPlayPlan(texts, wavDir, a.cfg.FileNameMaxLength)
	if len(wavFiles) == 0 {
		a.emitPlayError("播放目录中没有可播放的 wav 文件")
		a.emitPlayDone("播放未启动")
		return
	}
	sessionDir, reportFile, err := createPlaySessionDir(wavDir)
	if err != nil {
		a.emitPlayError(fmt.Sprintf("创建播放结果目录失败: %v", err))
		a.emitPlayDone("播放失败")
		return
	}

	a.emitTaskEvent("play-output", "start", "开始播放模式...")
	a.emitPlayOutput(fmt.Sprintf("目标设备: %s", serial))
	a.emitPlayOutput(fmt.Sprintf("共 %d 个 wav 文件，目录: %s", len(wavFiles), wavDir))
	a.emitPlayOutput(fmt.Sprintf("结果目录: %s", sessionDir))

	results := make([]playTestResult, 0, len(wavFiles))
	passCount := 0
	failCount := 0

	for i, wavFile := range wavFiles {
		if ctx.Err() != nil {
			savePlayTestReport(reportFile, results, passCount, failCount, false)
			a.emitPlayDone("已停止播放")
			return
		}

		baseName := strings.TrimSuffix(filepath.Base(wavFile), ".wav")
		logFile := filepath.Join(sessionDir, baseName+".log")
		pngFile := filepath.Join(sessionDir, baseName+".png")
		mp4File := filepath.Join(sessionDir, baseName+".mp4")

		originalText := textLookup[wavFile]
		if originalText == "" {
			originalText = extractTextFromFileName(filepath.Base(wavFile))
		}

		result := playTestResult{
			Text:    originalText,
			LogFile: logFile,
			PngFile: pngFile,
			WavFile: wavFile,
		}

		a.emitPlayOutput(fmt.Sprintf("[%d/%d] %s", i+1, len(wavFiles), truncate(originalText, 30)))
		err := a.playWithArtifacts(ctx, serial, wavFile, logFile, pngFile, mp4File)
		if err != nil {
			if ctx.Err() != nil {
				savePlayTestReport(reportFile, results, passCount, failCount, false)
				a.emitPlayDone("已停止播放")
				return
			}

			result.Passed = false
			result.AssertInfo = fmt.Sprintf("播放失败: %v", err)
			results = append(results, result)
			failCount++
			savePlayTestReport(reportFile, results, passCount, failCount, false)
			a.emitPlayError(result.AssertInfo)
			continue
		}

		passed, assertInfo := assertLogContent(logFile, originalText)
		result.Passed = passed
		result.AssertInfo = assertInfo
		results = append(results, result)
		if passed {
			passCount++
			a.emitPlayOutput(fmt.Sprintf("断言通过: %s", filepath.Base(logFile)))
		} else {
			failCount++
			a.emitPlayError(fmt.Sprintf("断言失败: %s", assertInfo))
		}
		savePlayTestReport(reportFile, results, passCount, failCount, false)
	}

	savePlayTestReport(reportFile, results, passCount, failCount, true)
	a.emitPlayDone(fmt.Sprintf("播放模式完成，成功 %d 条，失败 %d 条", passCount, failCount))
}

func (a *App) StopPlay() error {
	a.cmdMu.Lock()
	cancel := playCancel
	serial := strings.TrimSpace(a.activePlayDevice)
	a.cmdMu.Unlock()

	if cancel != nil {
		a.emitPlayOutput("正在停止，等待资源清理...")
		cancel()
		player.StopAllPlayback()
		adb.StopAllAdbProcessesForDevice(serial)
	}
	return nil
}

func (a *App) StopGenerate() error {
	a.cmdMu.Lock()
	cancel := generateCancel
	a.cmdMu.Unlock()

	if cancel != nil {
		cancel()
	}
	return nil
}
