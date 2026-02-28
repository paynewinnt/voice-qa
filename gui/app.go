package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
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
	ctx         context.Context
	cfg         *config.Config
	ttsEngine   tts.Engine   // TTS 引擎接口
	generateCmd *exec.Cmd    // 当前生成进程
	playCmd     *exec.Cmd    // 当前播放进程
	playDir     string       // 播放目录（默认为 output）
	cmdMu       sync.Mutex   // 保护 generateCmd 和 playCmd 的互斥锁
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

// initTTSEngine 初始化 TTS 引擎
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

	a.cfg = cfg

	// 重新初始化 TTS 引擎（如果声音配置改变了）
	a.initTTSEngine()

	// 保存到文件
	configPath := config.FindConfigFile()
	if configPath == "" {
		if exePath, err := os.Executable(); err == nil {
			configPath = filepath.Join(filepath.Dir(exePath), "config.json")
		} else {
			configPath = "config.json"
		}
	}

	return cfg.Save(configPath)
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
	if !filepath.IsAbs(baseDir) {
		if exePath, err := os.Executable(); err == nil {
			baseDir = filepath.Join(filepath.Dir(exePath), baseDir)
		}
	}

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
		if exePath, err := os.Executable(); err == nil {
			absPath := filepath.Join(filepath.Dir(exePath), textFile)
			if _, err := os.Stat(absPath); err == nil {
				textFile = absPath
			}
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
	if !filepath.IsAbs(textFile) {
		if exePath, err := os.Executable(); err == nil {
			textFile = filepath.Join(filepath.Dir(exePath), textFile)
		}
	}

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
	outputDir := a.cfg.OutputDir
	if outputDir == "" {
		outputDir = "output"
	}

	// 获取绝对路径
	if !filepath.IsAbs(outputDir) {
		if exePath, err := os.Executable(); err == nil {
			outputDir = filepath.Join(filepath.Dir(exePath), outputDir)
		}
	}

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

	return GenerateResult{Success: true, Message: "生成成功", File: outputFile}
}

// GenerateBatch 批量生成语音
func (a *App) GenerateBatch(texts []string, simple bool) []GenerateResult {
	var results []GenerateResult

	outputDir := a.cfg.OutputDir
	if outputDir == "" {
		outputDir = "output"
	}

	// 获取绝对路径
	if !filepath.IsAbs(outputDir) {
		if exePath, err := os.Executable(); err == nil {
			outputDir = filepath.Join(filepath.Dir(exePath), outputDir)
		}
	}

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
			results = append(results, GenerateResult{Success: true, Message: "生成成功", File: outputFile})
		}
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
	if !filepath.IsAbs(path) {
		if exePath, err := os.Executable(); err == nil {
			path = filepath.Join(filepath.Dir(exePath), path)
		}
	}

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
	if !filepath.IsAbs(textFile) {
		if exePath, err := os.Executable(); err == nil {
			textFile = filepath.Join(filepath.Dir(exePath), textFile)
		}
	}

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
	outputDir := a.cfg.OutputDir
	if outputDir == "" {
		outputDir = "output"
	}

	// 获取绝对路径
	if !filepath.IsAbs(outputDir) {
		if exePath, err := os.Executable(); err == nil {
			outputDir = filepath.Join(filepath.Dir(exePath), outputDir)
		}
	}

	reportFile := filepath.Join(outputDir, "test_report.txt")

	// 检查文件是否存在
	if _, err := os.Stat(reportFile); os.IsNotExist(err) {
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
	cmd := exec.Command("adb", "disconnect", serial)
	hideConsoleWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return AdbResult{Success: false, Message: fmt.Sprintf("断开失败: %s %v", string(output), err)}
	}
	return AdbResult{Success: true, Message: fmt.Sprintf("已断开 %s", serial)}
}

// DisconnectAllDevices 断开所有 ADB 设备
func (a *App) DisconnectAllDevices() AdbResult {
	cmd := exec.Command("adb", "disconnect")
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
	cmd := exec.Command("adb", "-s", serial, "install", "-r", apkPath)
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

// RunGenerate 运行批量生成语音（在后台执行，进度推送到前端）
func (a *App) RunGenerate() {
	go a.runGenerateInBackground()
}

// runGenerateInBackground 后台执行生成
func (a *App) runGenerateInBackground() {
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
	hideConsoleWindow(cmd)  // 隐藏控制台窗口
	a.cmdMu.Lock()
	a.generateCmd = cmd     // 保存进程引用
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

// RunPlay 运行播放模式（在后台执行，进度推送到前端）
func (a *App) RunPlay() {
	go a.runPlayInBackground()
}

// runPlayInBackground 后台执行播放
func (a *App) runPlayInBackground() {
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
	a.playCmd = cmd        // 保存进程引用
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

// StopPlay 停止播放
func (a *App) StopPlay() error {
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
	cmd := exec.Command("adb", "devices")
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
	cmd := exec.Command("adb", "-s", serial, "shell", "pm", "list", "packages")
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
	cmd1 := exec.Command("adb", "-s", serial, "shell", "am", "force-stop", packageName)
	hideConsoleWindow(cmd1)
	if out, err := cmd1.CombinedOutput(); err != nil {
		return AdbResult{Success: false, Message: fmt.Sprintf("force-stop 失败: %s %v", string(out), err)}
	}

	// pm clear
	cmd2 := exec.Command("adb", "-s", serial, "shell", "pm", "clear", packageName)
	hideConsoleWindow(cmd2)
	if out, err := cmd2.CombinedOutput(); err != nil {
		return AdbResult{Success: false, Message: fmt.Sprintf("pm clear 失败: %s %v", string(out), err)}
	}

	return AdbResult{Success: true, Message: "force-stop 和 pm clear 完成"}
}

// PerfForceStopAll 停止所有第三方应用（包括前台正在运行的）
func (a *App) PerfForceStopAll(serial string) AdbResult {
	// 获取所有第三方包并逐个 force-stop
	listCmd := exec.Command("adb", "-s", serial, "shell", "pm", "list", "packages", "-3")
	hideConsoleWindow(listCmd)
	listOutput, err := listCmd.Output()
	if err != nil {
		// 回退方案
		cmd2 := exec.Command("adb", "-s", serial, "shell", "am", "kill-all")
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
		stopCmd := exec.Command("adb", "-s", serial, "shell", "am", "force-stop", pkg)
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
		cmd := exec.Command("adb", "-s", serial, "shell", "input", "keyevent", "KEYCODE_BACK")
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

	cmd := exec.Command("adb", "-s", serial, "shell", "am", "start", "-W", "-n", component)
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

// StopGenerate 停止生成
func (a *App) StopGenerate() error {
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
