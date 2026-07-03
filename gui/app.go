package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"voice-qa/internal/adb"
	"voice-qa/internal/audio"
	"voice-qa/internal/config"
	"voice-qa/internal/logassert"
	"voice-qa/internal/perfetto"
	"voice-qa/internal/player"
	"voice-qa/internal/tts"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const defaultModel = "zh_CN-huayan-medium.onnx"

var (
	appVersion       = "dev"
	defaultUpdateURL = "http://172.16.15.15/latest.json"
)

// App struct
type App struct {
	ctx                   context.Context
	cfg                   *config.Config
	ttsEngine             tts.Engine // TTS 引擎接口
	generateCmd           *exec.Cmd  // 当前生成进程
	playCmd               *exec.Cmd  // 当前播放进程
	playDir               string     // 播放目录（默认为 output）
	playDevice            string     // 播放模式目标设备序列号
	activePlayDevice      string
	perfSession           *perfetto.Session
	perfLogcat            *perfetto.LogcatSession
	perfResult            *perfetto.LaunchResult
	perfTimer             *time.Timer
	manualRecorder        *adb.VideoRecorder
	manualRecordPath      string
	manualRecordSerial    string
	manualRecordStartedAt time.Time
	cmdMu                 sync.Mutex // 保护 generateCmd 和 playCmd 的互斥锁
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
	perfSession := a.perfSession
	perfLogcat := a.perfLogcat
	manualRecorder := a.manualRecorder
	if a.perfTimer != nil {
		a.perfTimer.Stop()
		a.perfTimer = nil
	}
	a.perfSession = nil
	a.perfLogcat = nil
	a.manualRecorder = nil
	a.manualRecordPath = ""
	a.manualRecordSerial = ""
	a.manualRecordStartedAt = time.Time{}
	a.cmdMu.Unlock()

	if genCancel != nil {
		genCancel()
	}
	if playCancel != nil {
		playCancel()
	}
	if perfSession != nil {
		_, _ = perfSession.StopAndPull()
	}
	if perfLogcat != nil {
		_ = perfLogcat.Stop()
	}
	if manualRecorder != nil {
		_ = manualRecorder.Stop()
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

type UpdateInfo struct {
	Version string `json:"version"`
	Notes   string `json:"notes"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}

type UpdateCheckResult struct {
	Success        bool   `json:"success"`
	Message        string `json:"message"`
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	HasUpdate      bool   `json:"hasUpdate"`
	Notes          string `json:"notes"`
	URL            string `json:"url"`
	SHA256         string `json:"sha256"`
	UpdateJSONURL  string `json:"updateJsonUrl"`
}

type UpdateDownloadResult struct {
	Success  bool   `json:"success"`
	Message  string `json:"message"`
	FilePath string `json:"filePath"`
	Dir      string `json:"dir"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

func (a *App) GetAppVersion() string {
	return appVersion
}

func (a *App) GetDefaultUpdateURL() string {
	return defaultUpdateURL
}

func (a *App) CheckUpdate(updateJSONURL string) UpdateCheckResult {
	updateJSONURL = strings.TrimSpace(updateJSONURL)
	if updateJSONURL == "" {
		updateJSONURL = defaultUpdateURL
	}

	info, err := fetchUpdateInfo(updateJSONURL)
	if err != nil {
		return UpdateCheckResult{Success: false, Message: err.Error(), CurrentVersion: appVersion, UpdateJSONURL: updateJSONURL}
	}
	downloadURL, err := resolveUpdateURL(updateJSONURL, info.URL)
	if err != nil {
		return UpdateCheckResult{Success: false, Message: err.Error(), CurrentVersion: appVersion, UpdateJSONURL: updateJSONURL}
	}

	hasUpdate := compareVersions(info.Version, appVersion) > 0
	message := "当前已是最新版本"
	if hasUpdate {
		message = fmt.Sprintf("发现新版本 %s", info.Version)
	}
	return UpdateCheckResult{
		Success:        true,
		Message:        message,
		CurrentVersion: appVersion,
		LatestVersion:  info.Version,
		HasUpdate:      hasUpdate,
		Notes:          info.Notes,
		URL:            downloadURL,
		SHA256:         strings.ToLower(strings.TrimSpace(info.SHA256)),
		UpdateJSONURL:  updateJSONURL,
	}
}

func (a *App) DownloadUpdate(updateURL, expectedSHA256 string) UpdateDownloadResult {
	updateURL = strings.TrimSpace(updateURL)
	if updateURL == "" {
		return UpdateDownloadResult{Success: false, Message: "更新包 URL 为空"}
	}
	parsed, err := url.Parse(updateURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return UpdateDownloadResult{Success: false, Message: "更新包 URL 无效"}
	}

	fileName := filepath.Base(parsed.Path)
	if fileName == "." || fileName == "/" || strings.TrimSpace(fileName) == "" {
		fileName = "voice-qa-update.zip"
	}
	updateDir := a.updateDownloadDir()
	if err := os.MkdirAll(updateDir, 0755); err != nil {
		fallbackDir := filepath.Join(a.resolvedOutputDir(), "updates")
		if fallbackErr := os.MkdirAll(fallbackDir, 0755); fallbackErr != nil {
			return UpdateDownloadResult{Success: false, Message: fmt.Sprintf("创建更新目录失败: %v", err)}
		}
		updateDir = fallbackDir
	}
	targetPath := filepath.Join(updateDir, fileName)

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(updateURL)
	if err != nil {
		return UpdateDownloadResult{Success: false, Message: fmt.Sprintf("下载失败: %v", err), Dir: updateDir}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return UpdateDownloadResult{Success: false, Message: fmt.Sprintf("下载失败: HTTP %d", resp.StatusCode), Dir: updateDir}
	}

	tmpPath := targetPath + ".download"
	file, err := os.Create(tmpPath)
	if err != nil {
		return UpdateDownloadResult{Success: false, Message: fmt.Sprintf("创建文件失败: %v", err), Dir: updateDir}
	}
	hasher := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(file, hasher), resp.Body)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return UpdateDownloadResult{Success: false, Message: fmt.Sprintf("写入文件失败: %v", copyErr), Dir: updateDir}
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return UpdateDownloadResult{Success: false, Message: fmt.Sprintf("关闭文件失败: %v", closeErr), Dir: updateDir}
	}

	actualSHA256 := fmt.Sprintf("%x", hasher.Sum(nil))
	expectedSHA256 = strings.ToLower(strings.TrimSpace(expectedSHA256))
	if expectedSHA256 != "" && expectedSHA256 != actualSHA256 {
		_ = os.Remove(tmpPath)
		return UpdateDownloadResult{Success: false, Message: fmt.Sprintf("SHA256 校验失败: 期望 %s，实际 %s", expectedSHA256, actualSHA256), Dir: updateDir, Size: size, SHA256: actualSHA256}
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return UpdateDownloadResult{Success: false, Message: fmt.Sprintf("保存更新包失败: %v", err), Dir: updateDir, Size: size, SHA256: actualSHA256}
	}

	return UpdateDownloadResult{Success: true, Message: "更新包下载完成", FilePath: targetPath, Dir: updateDir, Size: size, SHA256: actualSHA256}
}

func (a *App) updateDownloadDir() string {
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		parentDir := filepath.Dir(exeDir)
		if parentDir != "" && parentDir != "." && parentDir != exeDir {
			return parentDir
		}
	}
	return filepath.Join(a.resolvedOutputDir(), "updates")
}

func fetchUpdateInfo(updateJSONURL string) (UpdateInfo, error) {
	parsed, err := url.Parse(updateJSONURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return UpdateInfo{}, fmt.Errorf("版本清单 URL 无效")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(updateJSONURL)
	if err != nil {
		return UpdateInfo{}, fmt.Errorf("检查更新失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return UpdateInfo{}, fmt.Errorf("检查更新失败: HTTP %d", resp.StatusCode)
	}
	var info UpdateInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return UpdateInfo{}, fmt.Errorf("解析版本清单失败: %w", err)
	}
	info.Version = strings.TrimSpace(info.Version)
	info.URL = strings.TrimSpace(info.URL)
	if info.Version == "" {
		return UpdateInfo{}, fmt.Errorf("版本清单缺少 version")
	}
	if info.URL == "" {
		return UpdateInfo{}, fmt.Errorf("版本清单缺少 url")
	}
	return info, nil
}

func resolveUpdateURL(baseURL, updatePath string) (string, error) {
	parsedUpdate, err := url.Parse(strings.TrimSpace(updatePath))
	if err != nil {
		return "", fmt.Errorf("更新包 URL 无效: %w", err)
	}
	if parsedUpdate.IsAbs() {
		return parsedUpdate.String(), nil
	}
	parsedBase, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("版本清单 URL 无效: %w", err)
	}
	return parsedBase.ResolveReference(parsedUpdate).String(), nil
}

func compareVersions(a, b string) int {
	ap := versionParts(a)
	bp := versionParts(b)
	maxLen := len(ap)
	if len(bp) > maxLen {
		maxLen = len(bp)
	}
	for i := 0; i < maxLen; i++ {
		var av, bv int
		if i < len(ap) {
			av = ap[i]
		}
		if i < len(bp) {
			bv = bp[i]
		}
		if av > bv {
			return 1
		}
		if av < bv {
			return -1
		}
	}
	return strings.Compare(strings.TrimSpace(a), strings.TrimSpace(b))
}

func versionParts(version string) []int {
	fields := strings.FieldsFunc(version, func(r rune) bool {
		return r < '0' || r > '9'
	})
	parts := make([]int, 0, len(fields))
	for _, field := range fields {
		if field == "" {
			continue
		}
		var value int
		fmt.Sscanf(field, "%d", &value)
		parts = append(parts, value)
	}
	return parts
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

// LogcatQueryOptions logcat 查询选项。
type LogcatQueryOptions struct {
	PackageName string   `json:"packageName"`
	Keyword     string   `json:"keyword"`
	Lines       int      `json:"lines"`
	Buffers     []string `json:"buffers"`
	MinLevel    string   `json:"minLevel"`
	UsePid      bool     `json:"usePid"`
	SaveToFile  bool     `json:"saveToFile"`
}

// LogcatQueryResult logcat 查询结果。
type LogcatQueryResult struct {
	Success      bool   `json:"success"`
	Message      string `json:"message"`
	Command      string `json:"command"`
	Output       string `json:"output"`
	FilePath     string `json:"filePath"`
	Dir          string `json:"dir"`
	Serial       string `json:"serial"`
	PackageName  string `json:"packageName"`
	Pid          string `json:"pid"`
	UsedPid      bool   `json:"usedPid"`
	TotalLines   int    `json:"totalLines"`
	MatchedLines int    `json:"matchedLines"`
}

// HeartbeatPopupAnalysisOptions 心跳和弹窗日志分析选项。
type HeartbeatPopupAnalysisOptions struct {
	PackageName string   `json:"packageName"`
	Lines       int      `json:"lines"`
	Buffers     []string `json:"buffers"`
	UsePid      bool     `json:"usePid"`
	SaveToFile  bool     `json:"saveToFile"`
}

// HeartbeatPopupAnalysisResult 心跳和弹窗日志分析汇总。
type HeartbeatPopupAnalysisResult struct {
	Success         bool                                 `json:"success"`
	Message         string                               `json:"message"`
	PackageName     string                               `json:"packageName"`
	Devices         []DeviceHeartbeatPopupAnalysisResult `json:"devices"`
	TotalHeartbeats int                                  `json:"totalHeartbeats"`
	TotalPopups     int                                  `json:"totalPopups"`
	FilePath        string                               `json:"filePath"`
	Dir             string                               `json:"dir"`
}

// DeviceHeartbeatPopupAnalysisResult 单台设备心跳和弹窗日志分析结果。
type DeviceHeartbeatPopupAnalysisResult struct {
	Success     bool             `json:"success"`
	Message     string           `json:"message"`
	Serial      string           `json:"serial"`
	PackageName string           `json:"packageName"`
	Pid         string           `json:"pid"`
	UsedPid     bool             `json:"usedPid"`
	Command     string           `json:"command"`
	TotalLines  int              `json:"totalLines"`
	Heartbeats  []HeartbeatEvent `json:"heartbeats"`
	Popups      []PopupEvent     `json:"popups"`
	NextCheckAt string           `json:"nextCheckAt"`
}

// HeartbeatEvent 心跳日志事件。
type HeartbeatEvent struct {
	Time       string `json:"time"`
	Direction  string `json:"direction"`
	ResultType string `json:"resultType"`
	TaskID     string `json:"taskId"`
	DevID      string `json:"devId"`
	URL        string `json:"url"`
	Raw        string `json:"raw"`
}

// PopupEvent 弹窗日志事件。
type PopupEvent struct {
	FocusTime   string `json:"focusTime"`
	PrepareTime string `json:"prepareTime"`
	CreateTime  string `json:"createTime"`
	TriggerTime string `json:"triggerTime"`
	ScheduledAt string `json:"scheduledAt"`
	Title       string `json:"title"`
	PlanID      string `json:"planId"`
	Content     string `json:"content"`
	Raw         string `json:"raw"`
}

// ManualRecordingResult 手动录屏操作结果
type ManualRecordingResult struct {
	Success    bool   `json:"success"`
	Message    string `json:"message"`
	Path       string `json:"path"`
	Serial     string `json:"serial"`
	DurationMs int64  `json:"durationMs"`
	SaveMs     int64  `json:"saveMs"`
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

	if serial != "" && !hasAdbTargetArg(args) {
		args = append([]string{"-s", serial}, args...)
		return runAdbCommandArgs(args)
	}

	if serial == "" && !hasAdbTargetArg(args) && shouldAutoTargetAdbCommand(args) {
		devices, err := a.GetConnectedDevices()
		if err == nil {
			switch len(devices) {
			case 1:
				args = append([]string{"-s", devices[0]}, args...)
				return runAdbCommandArgs(args)
			default:
				if len(devices) > 1 {
					return runAdbCommandForDevices(devices, args)
				}
			}
		}
	}

	return runAdbCommandArgs(args)
}

func runAdbCommandArgs(args []string) AdbCommandResult {
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

func runAdbCommandForDevices(devices []string, args []string) AdbCommandResult {
	var output strings.Builder
	successCount := 0
	for i, device := range devices {
		deviceArgs := append([]string{"-s", device}, args...)
		result := runAdbCommandArgs(deviceArgs)
		if i > 0 {
			output.WriteString("\n\n")
		}
		fmt.Fprintf(&output, "===== %s =====\n", device)
		fmt.Fprintf(&output, "$ %s\n", result.Command)
		if result.Output != "" {
			output.WriteString(result.Output)
		} else {
			output.WriteString(result.Message)
		}
		if result.Success {
			successCount++
		} else if result.Message != "" && !strings.Contains(result.Output, result.Message) {
			fmt.Fprintf(&output, "\n%s", result.Message)
		}
	}

	success := successCount == len(devices)
	message := fmt.Sprintf("已对 %d 台设备执行命令", len(devices))
	if !success {
		message = fmt.Sprintf("部分设备执行失败：%d/%d 成功", successCount, len(devices))
	}

	return AdbCommandResult{
		Success: success,
		Message: message,
		Command: "adb " + strings.Join(args, " ") + "  # auto all devices",
		Output:  strings.TrimSpace(output.String()),
	}
}

func shouldAutoTargetAdbCommand(args []string) bool {
	command := primaryAdbCommand(args)
	if command == "" {
		return false
	}
	switch command {
	case "devices", "connect", "disconnect", "start-server", "kill-server", "version", "help", "keygen", "server-status", "reconnect", "track-devices", "mdns":
		return false
	default:
		return true
	}
}

func primaryAdbCommand(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" {
			continue
		}
		switch arg {
		case "-s", "-t", "-H", "-P", "-L":
			i++
			continue
		case "-d", "-e", "-a":
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return strings.ToLower(arg)
	}
	return ""
}

// QueryLogcat 查询设备 logcat，可按包名 PID、包名文本和关键字过滤。
func (a *App) QueryLogcat(serial string, opts LogcatQueryOptions) LogcatQueryResult {
	resolvedSerial, err := a.resolveAdbDevice(serial)
	if err != nil {
		return LogcatQueryResult{Success: false, Message: err.Error()}
	}

	opts.PackageName = strings.TrimSpace(opts.PackageName)
	opts.Keyword = strings.TrimSpace(opts.Keyword)
	opts.Lines = normalizeLogcatLines(opts.Lines)
	opts.Buffers = normalizeLogcatBuffers(opts.Buffers)
	opts.MinLevel = normalizeLogcatLevel(opts.MinLevel)

	pid := ""
	if opts.UsePid && opts.PackageName != "" {
		pid = a.findPackagePid(resolvedSerial, opts.PackageName)
	}

	raw, displayCmd, usedPid, err := a.runLogcatDump(resolvedSerial, opts, pid)
	if err != nil && pid != "" {
		raw, displayCmd, usedPid, err = a.runLogcatDump(resolvedSerial, opts, "")
	}
	if err != nil {
		return LogcatQueryResult{
			Success:     false,
			Message:     err.Error(),
			Command:     displayCmd,
			Serial:      resolvedSerial,
			PackageName: opts.PackageName,
			Pid:         pid,
		}
	}

	totalLines := countNonEmptyLines(raw)
	requirePackageText := opts.PackageName != "" && !usedPid
	output, matchedLines := filterLogcatOutput(raw, opts.PackageName, opts.Keyword, requirePackageText)
	if strings.TrimSpace(output) == "" {
		output = "(无匹配日志)"
		matchedLines = 0
	}

	result := LogcatQueryResult{
		Success:      true,
		Message:      "日志查询完成",
		Command:      displayCmd,
		Output:       output,
		Serial:       resolvedSerial,
		PackageName:  opts.PackageName,
		Pid:          pid,
		UsedPid:      usedPid,
		TotalLines:   totalLines,
		MatchedLines: matchedLines,
	}
	if usedPid {
		result.Message = fmt.Sprintf("日志查询完成，已按 PID %s 过滤", pid)
	} else if opts.PackageName != "" {
		result.Message = "日志查询完成，未获取到运行中 PID，已按包名文本过滤"
	}

	if opts.SaveToFile {
		filePath, dir, err := a.saveLogcatQueryResult(result)
		if err != nil {
			result.Success = false
			result.Message = fmt.Sprintf("日志查询完成，但保存失败: %v", err)
			return result
		}
		result.FilePath = filePath
		result.Dir = dir
		result.Message += "，已保存到文件"
	}

	return result
}

// ClearDeviceLogcat 清空设备 logcat 缓冲区。
func (a *App) ClearDeviceLogcat(serial string, buffers []string) AdbCommandResult {
	resolvedSerial, err := a.resolveAdbDevice(serial)
	if err != nil {
		return AdbCommandResult{Success: false, Message: err.Error()}
	}

	normalizedBuffers := normalizeLogcatBuffers(buffers)
	args := []string{"-s", resolvedSerial, "logcat"}
	for _, buffer := range normalizedBuffers {
		args = append(args, "-b", buffer)
	}
	args = append(args, "-c")
	displayCmd := "adb " + strings.Join(args, " ")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := adb.CommandContext(ctx, args...)
	hideConsoleWindow(cmd)
	output, err := cmd.CombinedOutput()
	outStr := strings.TrimSpace(string(output))
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return AdbCommandResult{Success: false, Message: "清空日志超时", Command: displayCmd, Output: outStr}
		}
		if len(normalizedBuffers) == 1 && normalizedBuffers[0] == "all" {
			fallbackArgs := []string{"-s", resolvedSerial, "logcat", "-c"}
			fallbackCmd := adb.Command(fallbackArgs...)
			hideConsoleWindow(fallbackCmd)
			fallbackOutput, fallbackErr := fallbackCmd.CombinedOutput()
			fallbackOut := strings.TrimSpace(string(fallbackOutput))
			if fallbackErr == nil {
				return AdbCommandResult{Success: true, Message: "日志已清空", Command: "adb " + strings.Join(fallbackArgs, " "), Output: emptyAsDash(fallbackOut)}
			}
			return AdbCommandResult{Success: false, Message: fmt.Sprintf("清空日志失败: %s %v；fallback: %s %v", outStr, err, fallbackOut, fallbackErr), Command: displayCmd, Output: outStr}
		}
		return AdbCommandResult{Success: false, Message: fmt.Sprintf("清空日志失败: %s %v", outStr, err), Command: displayCmd, Output: outStr}
	}

	return AdbCommandResult{Success: true, Message: "日志已清空", Command: displayCmd, Output: emptyAsDash(outStr)}
}

func (a *App) GetLogcatOutputDir() string {
	return filepath.Join(a.resolvedOutputDir(), "logcat")
}

// AnalyzeHeartbeatPopupLogs 分析 com.zjdx.neuralnexus 的心跳和悬浮弹窗日志。
func (a *App) AnalyzeHeartbeatPopupLogs(serial string, opts HeartbeatPopupAnalysisOptions) HeartbeatPopupAnalysisResult {
	opts.PackageName = strings.TrimSpace(opts.PackageName)
	if opts.PackageName == "" {
		opts.PackageName = "com.zjdx.neuralnexus"
	}
	opts.Lines = normalizeAnalysisLogcatLines(opts.Lines)
	opts.Buffers = normalizeLogcatBuffers(opts.Buffers)

	devices, err := a.resolveAnalysisDevices(serial)
	if err != nil {
		return HeartbeatPopupAnalysisResult{Success: false, Message: err.Error(), PackageName: opts.PackageName}
	}

	result := HeartbeatPopupAnalysisResult{
		Success:     true,
		PackageName: opts.PackageName,
	}

	for _, device := range devices {
		deviceResult := a.analyzeHeartbeatPopupForDevice(device, opts)
		result.Devices = append(result.Devices, deviceResult)
		result.TotalHeartbeats += len(deviceResult.Heartbeats)
		result.TotalPopups += len(deviceResult.Popups)
		if !deviceResult.Success {
			result.Success = false
		}
	}

	result.Message = fmt.Sprintf("分析完成：%d 台设备，心跳 %d 条，弹窗 %d 次", len(result.Devices), result.TotalHeartbeats, result.TotalPopups)
	if !result.Success {
		result.Message = "部分设备分析失败，" + result.Message
	}

	if opts.SaveToFile {
		filePath, dir, err := a.saveHeartbeatPopupAnalysis(result)
		if err != nil {
			result.Success = false
			result.Message += fmt.Sprintf("；保存失败: %v", err)
			return result
		}
		result.FilePath = filePath
		result.Dir = dir
		result.Message += "，已保存到文件"
	}

	return result
}

func (a *App) resolveAnalysisDevices(serial string) ([]string, error) {
	serial = strings.TrimSpace(serial)
	if serial == "__all__" {
		devices, err := a.GetConnectedDevices()
		if err != nil {
			return nil, err
		}
		if len(devices) == 0 {
			return nil, fmt.Errorf("未检测到可用设备")
		}
		return devices, nil
	}

	resolved, err := a.resolveAdbDevice(serial)
	if err != nil {
		return nil, err
	}
	return []string{resolved}, nil
}

func (a *App) analyzeHeartbeatPopupForDevice(serial string, opts HeartbeatPopupAnalysisOptions) DeviceHeartbeatPopupAnalysisResult {
	logOpts := LogcatQueryOptions{
		PackageName: opts.PackageName,
		Lines:       opts.Lines,
		Buffers:     opts.Buffers,
		UsePid:      opts.UsePid,
	}

	pid := ""
	if opts.UsePid && opts.PackageName != "" {
		pid = a.findPackagePid(serial, opts.PackageName)
	}

	raw, displayCmd, usedPid, err := a.runLogcatDump(serial, logOpts, pid)
	if err != nil && pid != "" {
		raw, displayCmd, usedPid, err = a.runLogcatDump(serial, logOpts, "")
	}
	if err != nil {
		return DeviceHeartbeatPopupAnalysisResult{
			Success:     false,
			Message:     err.Error(),
			Serial:      serial,
			PackageName: opts.PackageName,
			Pid:         pid,
			Command:     displayCmd,
		}
	}

	heartbeats, popups, nextCheckAt := parseHeartbeatPopupLog(raw)
	message := fmt.Sprintf("心跳 %d 条，弹窗 %d 次", len(heartbeats), len(popups))
	if usedPid && pid != "" {
		message += fmt.Sprintf("，按 PID %s 查询", pid)
	} else {
		message += "，按包名/关键词日志分析"
	}

	return DeviceHeartbeatPopupAnalysisResult{
		Success:     true,
		Message:     message,
		Serial:      serial,
		PackageName: opts.PackageName,
		Pid:         pid,
		UsedPid:     usedPid,
		Command:     displayCmd,
		TotalLines:  countNonEmptyLines(raw),
		Heartbeats:  heartbeats,
		Popups:      popups,
		NextCheckAt: nextCheckAt,
	}
}

func (a *App) runLogcatDump(serial string, opts LogcatQueryOptions, pid string) (string, string, bool, error) {
	args := []string{"-s", serial, "logcat", "-d", "-v", "time"}
	for _, buffer := range opts.Buffers {
		args = append(args, "-b", buffer)
	}
	args = append(args, "-t", strconv.Itoa(opts.Lines))
	usedPid := strings.TrimSpace(pid) != ""
	if usedPid {
		args = append(args, "--pid", pid)
	}
	if opts.MinLevel != "" {
		args = append(args, "*:"+opts.MinLevel)
	}
	displayCmd := "adb " + strings.Join(args, " ")

	timeout := 15 * time.Second
	if opts.Lines > 5000 {
		timeout = 30 * time.Second
	}
	if opts.Lines > 50000 {
		timeout = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := adb.CommandContext(ctx, args...)
	hideConsoleWindow(cmd)
	output, err := cmd.CombinedOutput()
	outStr := strings.TrimRight(string(output), "\r\n")
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return outStr, displayCmd, usedPid, fmt.Errorf("查询日志超时")
		}
		return outStr, displayCmd, usedPid, fmt.Errorf("查询日志失败: %s %v", strings.TrimSpace(outStr), err)
	}
	return outStr, displayCmd, usedPid, nil
}

func (a *App) findPackagePid(serial, packageName string) string {
	packageName = strings.TrimSpace(packageName)
	if packageName == "" {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := adb.CommandContext(ctx, "-s", serial, "shell", "pidof", packageName)
	hideConsoleWindow(cmd)
	output, err := cmd.Output()
	if err != nil || ctx.Err() != nil {
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(string(output)))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func (a *App) saveLogcatQueryResult(result LogcatQueryResult) (string, string, error) {
	logDir := a.GetLogcatOutputDir()
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return "", logDir, err
	}

	nameParts := []string{"logcat", time.Now().Format("20060102-150405")}
	if result.PackageName != "" {
		if safePkg := sanitizeFileName(result.PackageName); safePkg != "" {
			nameParts = append(nameParts, safePkg)
		}
	}
	filePath := filepath.Join(logDir, strings.Join(nameParts, "-")+".txt")
	var content strings.Builder
	if result.Command != "" {
		fmt.Fprintf(&content, "$ %s\n", result.Command)
	}
	fmt.Fprintf(&content, "device: %s\n", emptyAsDash(result.Serial))
	if result.PackageName != "" {
		fmt.Fprintf(&content, "package: %s\n", result.PackageName)
	}
	if result.Pid != "" {
		fmt.Fprintf(&content, "pid: %s\n", result.Pid)
	}
	fmt.Fprintf(&content, "matched_lines: %d / %d\n\n", result.MatchedLines, result.TotalLines)
	content.WriteString(result.Output)
	if !strings.HasSuffix(result.Output, "\n") {
		content.WriteString("\n")
	}
	if err := os.WriteFile(filePath, []byte(content.String()), 0644); err != nil {
		return "", logDir, err
	}
	return filePath, logDir, nil
}

func (a *App) saveHeartbeatPopupAnalysis(result HeartbeatPopupAnalysisResult) (string, string, error) {
	logDir := a.GetLogcatOutputDir()
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return "", logDir, err
	}

	filePath := filepath.Join(logDir, "heartbeat-popup-"+time.Now().Format("20060102-150405")+".txt")
	var content strings.Builder
	fmt.Fprintf(&content, "package: %s\n", result.PackageName)
	fmt.Fprintf(&content, "devices: %d\n", len(result.Devices))
	fmt.Fprintf(&content, "heartbeats: %d\n", result.TotalHeartbeats)
	fmt.Fprintf(&content, "popups: %d\n\n", result.TotalPopups)

	for _, device := range result.Devices {
		fmt.Fprintf(&content, "===== %s =====\n", device.Serial)
		fmt.Fprintf(&content, "status: %s\n", boolStatus(device.Success))
		fmt.Fprintf(&content, "message: %s\n", device.Message)
		if device.Command != "" {
			fmt.Fprintf(&content, "command: %s\n", device.Command)
		}
		if device.Pid != "" {
			fmt.Fprintf(&content, "pid: %s\n", device.Pid)
		}
		if device.NextCheckAt != "" {
			fmt.Fprintf(&content, "next_popup_check: %s\n", device.NextCheckAt)
		}

		content.WriteString("\nheartbeats:\n")
		if len(device.Heartbeats) == 0 {
			content.WriteString("  (none)\n")
		}
		for _, hb := range device.Heartbeats {
			fmt.Fprintf(&content, "  %s\t%s\t%s\ttaskId=%s\tdevId=%s\turl=%s\n", hb.Time, hb.Direction, emptyAsDash(hb.ResultType), emptyAsDash(hb.TaskID), emptyAsDash(hb.DevID), emptyAsDash(hb.URL))
		}

		content.WriteString("\npopups:\n")
		if len(device.Popups) == 0 {
			content.WriteString("  (none)\n")
		}
		for _, popup := range device.Popups {
			fmt.Fprintf(&content, "  focus=%s\tcreate=%s\tprepare=%s\ttrigger=%s\tplanId=%s\ttitle=%s\n", popup.FocusTime, emptyAsDash(popup.CreateTime), emptyAsDash(popup.PrepareTime), emptyAsDash(popup.TriggerTime), emptyAsDash(popup.PlanID), emptyAsDash(popup.Title))
		}
		content.WriteString("\n")
	}

	if err := os.WriteFile(filePath, []byte(content.String()), 0644); err != nil {
		return "", logDir, err
	}
	return filePath, logDir, nil
}

func parseHeartbeatPopupLog(output string) ([]HeartbeatEvent, []PopupEvent, string) {
	var heartbeats []HeartbeatEvent
	var popups []PopupEvent

	var lastHeartbeatURL string
	var lastPrepare PopupEvent
	var lastCreateTime string
	var lastTriggerTime string
	var lastTriggerTitle string
	var lastScheduledAt string
	var nextCheckAt string

	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		ts := logcatLineTime(line)
		if ts == "" {
			continue
		}

		if strings.Contains(line, "device/heartbeat") && strings.Contains(line, "Request URL:") {
			lastHeartbeatURL = strings.TrimSpace(line[strings.LastIndex(line, "Request URL:")+len("Request URL:"):])
			continue
		}

		if idx := strings.Index(line, "heartbeat json:"); idx >= 0 {
			raw := strings.TrimSpace(line[idx+len("heartbeat json:"):])
			heartbeats = append(heartbeats, HeartbeatEvent{
				Time:      ts,
				Direction: "request",
				DevID:     jsonStringValue(raw, "devId"),
				URL:       lastHeartbeatURL,
				Raw:       raw,
			})
			continue
		}

		if idx := strings.Index(line, "newHeartBeat return data"); idx >= 0 {
			raw := strings.TrimSpace(line[idx:])
			if colon := strings.Index(raw, ":"); colon >= 0 {
				raw = strings.TrimSpace(raw[colon+1:])
			}
			heartbeats = append(heartbeats, HeartbeatEvent{
				Time:       ts,
				Direction:  "response",
				ResultType: heartbeatResultType(raw),
				TaskID:     heartbeatTaskID(raw),
				URL:        lastHeartbeatURL,
				Raw:        raw,
			})
			continue
		}

		if idx := strings.Index(line, "已安排下次弹窗检查:"); idx >= 0 {
			nextCheckAt = strings.TrimSpace(line[idx+len("已安排下次弹窗检查:"):])
			continue
		}

		if idx := strings.Index(line, "已触发弹窗:"); idx >= 0 {
			lastTriggerTime = ts
			lastTriggerTitle = strings.TrimSpace(line[idx+len("已触发弹窗:"):])
			continue
		}

		if idx := strings.Index(line, "触发定时弹窗:"); idx >= 0 {
			lastTriggerTime = ts
			triggerText := strings.TrimSpace(line[idx+len("触发定时弹窗:"):])
			if atIdx := strings.Index(triggerText, " at "); atIdx >= 0 {
				lastTriggerTitle = strings.TrimSpace(triggerText[:atIdx])
				lastScheduledAt = strings.TrimSpace(triggerText[atIdx+len(" at "):])
			} else {
				lastTriggerTitle = triggerText
			}
			continue
		}

		if strings.Contains(line, "准备弹窗") {
			rawJSON := extractJSONObject(line)
			if rawJSON != "" {
				lastPrepare = PopupEvent{
					PrepareTime: ts,
					TriggerTime: lastTriggerTime,
					ScheduledAt: lastScheduledAt,
					Title:       jsonStringValue(rawJSON, "title"),
					PlanID:      jsonNumberOrStringValue(rawJSON, "planId"),
					Content:     jsonStringValue(rawJSON, "content"),
					Raw:         rawJSON,
				}
				if lastPrepare.Title == "" {
					lastPrepare.Title = lastTriggerTitle
				}
			}
			continue
		}

		if strings.Contains(line, "创建悬浮弹窗") {
			lastCreateTime = ts
			continue
		}

		if strings.Contains(line, "悬浮弹窗已获取焦点") {
			popup := lastPrepare
			popup.FocusTime = ts
			popup.CreateTime = lastCreateTime
			if popup.TriggerTime == "" {
				popup.TriggerTime = lastTriggerTime
			}
			if popup.ScheduledAt == "" {
				popup.ScheduledAt = lastScheduledAt
			}
			if popup.Title == "" {
				popup.Title = lastTriggerTitle
			}
			popups = append(popups, popup)
			lastPrepare = PopupEvent{}
			lastCreateTime = ""
			continue
		}
	}

	return heartbeats, popups, nextCheckAt
}

func logcatLineTime(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return ""
	}
	if len(fields[0]) != 5 || fields[0][2] != '-' || !strings.Contains(fields[1], ":") {
		return ""
	}
	return fields[0] + " " + fields[1]
}

func extractJSONObject(line string) string {
	start := strings.Index(line, "{")
	end := strings.LastIndex(line, "}")
	if start < 0 || end <= start {
		return ""
	}
	return line[start : end+1]
}

func jsonStringValue(raw, key string) string {
	value, ok := jsonMapValue(raw, key)
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

func jsonNumberOrStringValue(raw, key string) string {
	value, ok := jsonMapValue(raw, key)
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	default:
		return fmt.Sprint(v)
	}
}

func jsonMapValue(raw, key string) (any, bool) {
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return nil, false
	}
	value, ok := data[key]
	return value, ok
}

func heartbeatResultType(raw string) string {
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return ""
	}
	payload, _ := data["data"].(map[string]any)
	if payload == nil {
		return ""
	}
	if value, ok := payload["type"].(string); ok {
		return value
	}
	return ""
}

func heartbeatTaskID(raw string) string {
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return ""
	}
	payload, _ := data["data"].(map[string]any)
	if payload == nil {
		return ""
	}
	if value, ok := payload["taskId"].(string); ok {
		return value
	}
	return ""
}

func normalizeAnalysisLogcatLines(lines int) int {
	if lines <= 0 {
		return 10000
	}
	if lines > 100000 {
		return 100000
	}
	return lines
}

func boolStatus(ok bool) string {
	if ok {
		return "success"
	}
	return "failed"
}

func normalizeLogcatLines(lines int) int {
	if lines <= 0 {
		return 300
	}
	if lines > 5000 {
		return 5000
	}
	return lines
}

func normalizeLogcatBuffers(buffers []string) []string {
	valid := map[string]bool{
		"main":   true,
		"system": true,
		"crash":  true,
		"events": true,
		"radio":  true,
		"all":    true,
	}
	var normalized []string
	for _, buffer := range buffers {
		buffer = strings.ToLower(strings.TrimSpace(buffer))
		if !valid[buffer] {
			continue
		}
		if buffer == "all" {
			return []string{"all"}
		}
		if !containsString(normalized, buffer) {
			normalized = append(normalized, buffer)
		}
	}
	if len(normalized) == 0 {
		return []string{"all"}
	}
	return normalized
}

func normalizeLogcatLevel(level string) string {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "V", "D", "I", "W", "E", "F", "S":
		return strings.ToUpper(strings.TrimSpace(level))
	default:
		return ""
	}
}

func filterLogcatOutput(output, packageName, keyword string, requirePackage bool) (string, int) {
	packageName = strings.ToLower(strings.TrimSpace(packageName))
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	if strings.TrimSpace(output) == "" {
		return "", 0
	}
	if !requirePackage && keyword == "" {
		return strings.TrimRight(output, "\r\n"), countNonEmptyLines(output)
	}

	var b strings.Builder
	matched := 0
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		lowerLine := strings.ToLower(line)
		if requirePackage && packageName != "" && !strings.Contains(lowerLine, packageName) {
			continue
		}
		if keyword != "" && !strings.Contains(lowerLine, keyword) {
			continue
		}
		if matched > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		matched++
	}
	return b.String(), matched
}

func countNonEmptyLines(output string) int {
	count := 0
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			count++
		}
	}
	return count
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func hasAdbTargetArg(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d", "-e":
			return true
		case "-s", "-t":
			if i+1 < len(args) && strings.TrimSpace(args[i+1]) != "" {
				return true
			}
			i++
		}
	}
	return false
}

// StartManualScreenRecording 开始手动录屏，文件保存到 output/recordings。
func (a *App) StartManualScreenRecording(serial string) ManualRecordingResult {
	a.cmdMu.Lock()
	if a.manualRecorder != nil {
		path := a.manualRecordPath
		activeSerial := a.manualRecordSerial
		a.cmdMu.Unlock()
		return ManualRecordingResult{Success: false, Message: "录屏已在进行中，请先停止当前录屏", Path: path, Serial: activeSerial}
	}
	a.cmdMu.Unlock()

	resolvedSerial, err := a.resolveAdbDevice(serial)
	if err != nil {
		return ManualRecordingResult{Success: false, Message: err.Error()}
	}

	recordDir := filepath.Join(a.resolvedOutputDir(), "recordings")
	if err := os.MkdirAll(recordDir, 0755); err != nil {
		return ManualRecordingResult{Success: false, Message: fmt.Sprintf("创建录屏目录失败: %v", err), Serial: resolvedSerial}
	}

	fileName := fmt.Sprintf("screenrecord-%s.mp4", time.Now().Format("20060102-150405"))
	outputPath := filepath.Join(recordDir, fileName)
	recorder, err := adb.StartVideoRecordingForDevice(resolvedSerial, outputPath, 180)
	if err != nil {
		return ManualRecordingResult{Success: false, Message: err.Error(), Path: outputPath, Serial: resolvedSerial}
	}

	a.cmdMu.Lock()
	if a.manualRecorder != nil {
		path := a.manualRecordPath
		activeSerial := a.manualRecordSerial
		a.cmdMu.Unlock()
		_ = recorder.Stop()
		return ManualRecordingResult{Success: false, Message: "录屏已在进行中，请先停止当前录屏", Path: path, Serial: activeSerial}
	}
	a.manualRecorder = recorder
	a.manualRecordPath = outputPath
	a.manualRecordSerial = resolvedSerial
	a.manualRecordStartedAt = time.Now()
	a.cmdMu.Unlock()

	return ManualRecordingResult{
		Success: true,
		Message: fmt.Sprintf("已开始录屏：%s（最长 180 秒）", resolvedSerial),
		Path:    outputPath,
		Serial:  resolvedSerial,
	}
}

// StopManualScreenRecording 停止手动录屏并拉取到本地。
func (a *App) StopManualScreenRecording() ManualRecordingResult {
	a.cmdMu.Lock()
	recorder := a.manualRecorder
	outputPath := a.manualRecordPath
	serial := a.manualRecordSerial
	startedAt := a.manualRecordStartedAt
	if recorder == nil {
		a.cmdMu.Unlock()
		return ManualRecordingResult{Success: false, Message: "当前没有正在进行的录屏"}
	}
	a.manualRecorder = nil
	a.manualRecordPath = ""
	a.manualRecordSerial = ""
	a.manualRecordStartedAt = time.Time{}
	a.cmdMu.Unlock()

	stopRequestedAt := time.Now()
	if err := recorder.Stop(); err != nil {
		return ManualRecordingResult{Success: false, Message: fmt.Sprintf("停止录屏失败: %v", err), Path: outputPath, Serial: serial}
	}
	savedAt := time.Now()
	durationMs := stopRequestedAt.Sub(startedAt).Milliseconds()
	if startedAt.IsZero() {
		durationMs = 0
	}
	saveMs := savedAt.Sub(stopRequestedAt).Milliseconds()

	return ManualRecordingResult{
		Success:    true,
		Message:    fmt.Sprintf("录屏已保存: %s（录制约 %.1f 秒，保存耗时 %.1f 秒）", outputPath, float64(durationMs)/1000, float64(saveMs)/1000),
		Path:       outputPath,
		Serial:     serial,
		DurationMs: durationMs,
		SaveMs:     saveMs,
	}
}

func (a *App) resolveAdbDevice(serial string) (string, error) {
	serial = strings.TrimSpace(serial)
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
		return devices[0], nil
	default:
		return "", fmt.Errorf("检测到多台设备，请先在目标设备中选择一台")
	}
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

// ResolveLaunchComponent returns the launcher Activity component for a package.
func (a *App) ResolveLaunchComponent(serial, packageName string) AdbResult {
	serial = strings.TrimSpace(serial)
	packageName = strings.TrimSpace(packageName)
	if serial == "" {
		return AdbResult{Success: false, Message: "请先选择设备"}
	}
	if packageName == "" {
		return AdbResult{Success: false, Message: "请先选择应用包名"}
	}

	cmd := adb.Command("-s", serial, "shell", "cmd", "package", "resolve-activity", "--brief", packageName)
	hideConsoleWindow(cmd)
	output, err := cmd.CombinedOutput()
	raw := strings.TrimSpace(string(output))
	if err != nil {
		return AdbResult{Success: false, Message: fmt.Sprintf("解析启动 Activity 失败: %s %v", raw, err)}
	}

	lines := strings.Split(raw, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(strings.TrimSuffix(lines[i], "\r"))
		if line == "" || strings.HasPrefix(line, "priority=") || strings.Contains(strings.ToLower(line), "no activity") {
			continue
		}
		if validatePerfComponent(line) == nil {
			return AdbResult{Success: true, Message: line}
		}
	}
	return AdbResult{Success: false, Message: "未找到该包的 launcher Activity，请手动填写完整 Component"}
}

// GetPackageComponents lists likely Activity components for a package.
func (a *App) GetPackageComponents(serial, packageName string) PackageComponentsResult {
	serial = strings.TrimSpace(serial)
	packageName = strings.TrimSpace(packageName)
	if serial == "" {
		return PackageComponentsResult{Success: false, Message: "请先选择设备"}
	}
	if packageName == "" {
		return PackageComponentsResult{Success: false, Message: "请先选择应用包名"}
	}

	launcherResult := a.ResolveLaunchComponent(serial, packageName)
	launcher := ""
	if launcherResult.Success && validatePerfComponent(launcherResult.Message) == nil {
		launcher = launcherResult.Message
	}

	var dumpOutput string
	var dumpErr error
	for _, args := range [][]string{
		{"-s", serial, "shell", "cmd", "package", "dump", packageName},
		{"-s", serial, "shell", "dumpsys", "package", packageName},
	} {
		cmd := adb.Command(args...)
		hideConsoleWindow(cmd)
		output, err := cmd.CombinedOutput()
		dumpOutput = string(output)
		dumpErr = err
		if err == nil && strings.TrimSpace(dumpOutput) != "" {
			break
		}
	}

	components := parsePackageComponents(dumpOutput, packageName)
	components = prependComponent(components, launcher)
	if len(components) == 0 {
		if dumpErr != nil {
			return PackageComponentsResult{Success: false, Message: fmt.Sprintf("读取 Component 失败: %v", dumpErr)}
		}
		return PackageComponentsResult{Success: false, Message: "未解析到 Activity Component，请手动填写"}
	}

	return PackageComponentsResult{
		Success:    true,
		Message:    fmt.Sprintf("已解析到 %d 个可能的 Component", len(components)),
		Components: components,
		Launcher:   launcher,
	}
}

func validatePerfComponent(component string) error {
	component = strings.TrimSpace(component)
	slash := strings.Index(component, "/")
	if component == "" || slash <= 0 {
		return fmt.Errorf("请输入完整的 Component 名称（包名/Activity名）")
	}
	if slash == len(component)-1 {
		return fmt.Errorf("请输入完整的 Component 名称（包名/Activity名），当前缺少 Activity")
	}
	return nil
}

func parsePackageComponents(output, packageName string) []string {
	packageName = strings.TrimSpace(packageName)
	if strings.TrimSpace(output) == "" || packageName == "" {
		return nil
	}

	seen := map[string]bool{}
	add := func(component string) {
		component = strings.Trim(component, " \t\r\n,;)")
		if validatePerfComponent(component) != nil {
			return
		}
		if !strings.HasPrefix(component, packageName+"/") {
			return
		}
		seen[component] = true
	}

	componentRe := regexp.MustCompile(regexp.QuoteMeta(packageName) + `/[A-Za-z0-9_.$]+`)
	activityClassRe := regexp.MustCompile(regexp.QuoteMeta(packageName) + `\.[A-Za-z0-9_.$]+`)
	for _, match := range componentRe.FindAllString(output, -1) {
		className := match[strings.LastIndex(match, "/")+1:]
		if strings.HasSuffix(className, "Activity") {
			add(match)
		}
	}

	inActivityResolver := false
	inDeclaredActivities := false
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(line)
		switch {
		case strings.Contains(line, "Activity Resolver Table"):
			inActivityResolver = true
			inDeclaredActivities = false
			continue
		case strings.Contains(line, "Receiver Resolver Table") ||
			strings.Contains(line, "Service Resolver Table") ||
			strings.Contains(line, "Provider Resolver Table") ||
			strings.HasPrefix(trimmed, "Permissions:"):
			inActivityResolver = false
		case trimmed == "Activities:":
			inDeclaredActivities = true
			inActivityResolver = false
			continue
		case strings.HasPrefix(trimmed, "Receivers:") ||
			strings.HasPrefix(trimmed, "Services:") ||
			strings.HasPrefix(trimmed, "Providers:") ||
			strings.HasPrefix(trimmed, "Instrumentation:"):
			inDeclaredActivities = false
		}

		activityContext := inActivityResolver ||
			inDeclaredActivities ||
			strings.Contains(line, "Activity{") ||
			strings.Contains(line, "realActivity=") ||
			strings.Contains(line, "cmp="+packageName+"/")
		if !activityContext {
			continue
		}

		for _, match := range componentRe.FindAllString(line, -1) {
			add(match)
		}
		if strings.Contains(lower, "activ") {
			for _, match := range activityClassRe.FindAllString(line, -1) {
				add(packageName + "/" + match)
			}
		}
	}

	components := make([]string, 0, len(seen))
	for component := range seen {
		components = append(components, component)
	}
	sort.Strings(components)
	return components
}

func prependComponent(components []string, component string) []string {
	component = strings.TrimSpace(component)
	if component == "" {
		return components
	}
	result := []string{component}
	for _, item := range components {
		if item != component {
			result = append(result, item)
		}
	}
	return result
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

// PerfControlCommands 应用控制命令选项。
type PerfControlCommands struct {
	ForceStop bool `json:"forceStop"`
	ClearData bool `json:"clearData"`
	Back      bool `json:"back"`
}

// PerfRunControlCommands 按选择顺序执行应用控制命令。
func (a *App) PerfRunControlCommands(serial, packageName string, opts PerfControlCommands) AdbResult {
	serial = strings.TrimSpace(serial)
	packageName = strings.TrimSpace(packageName)
	if serial == "" {
		return AdbResult{Success: false, Message: "请先选择设备"}
	}
	if (opts.ForceStop || opts.ClearData) && packageName == "" {
		return AdbResult{Success: false, Message: "请选择应用包名"}
	}
	if !opts.ForceStop && !opts.ClearData && !opts.Back {
		return AdbResult{Success: false, Message: "请至少选择一个命令"}
	}

	var messages []string
	if opts.ForceStop {
		cmd := adb.Command("-s", serial, "shell", "am", "force-stop", packageName)
		hideConsoleWindow(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			return AdbResult{Success: false, Message: fmt.Sprintf("force-stop 失败: %s %v", string(out), err)}
		}
		messages = append(messages, fmt.Sprintf("force-stop %s 完成", packageName))
	}
	if opts.ClearData {
		cmd := adb.Command("-s", serial, "shell", "pm", "clear", packageName)
		hideConsoleWindow(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			return AdbResult{Success: false, Message: fmt.Sprintf("pm clear 失败: %s %v", string(out), err)}
		}
		messages = append(messages, fmt.Sprintf("pm clear %s 完成", packageName))
	}
	if opts.Back {
		for i := 0; i < 2; i++ {
			cmd := adb.Command("-s", serial, "shell", "input", "keyevent", "KEYCODE_BACK")
			hideConsoleWindow(cmd)
			if out, err := cmd.CombinedOutput(); err != nil {
				return AdbResult{Success: false, Message: fmt.Sprintf("back 第%d次失败: %s %v", i+1, string(out), err)}
			}
			if i == 0 {
				time.Sleep(300 * time.Millisecond)
			}
		}
		messages = append(messages, "back 完成")
	}

	return AdbResult{Success: true, Message: strings.Join(messages, "；")}
}

// PerfLaunchResult 性能启动结果
type PerfLaunchResult struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	Output      string `json:"output"`
	Timestamp   string `json:"timestamp"` // 启动命令发出时的精确时间
	ThisTimeMS  int    `json:"thisTimeMs"`
	TotalTimeMS int    `json:"totalTimeMs"`
	WaitTimeMS  int    `json:"waitTimeMs"`
	ResultDir   string `json:"resultDir"`
}

type PackageComponentsResult struct {
	Success    bool     `json:"success"`
	Message    string   `json:"message"`
	Components []string `json:"components"`
	Launcher   string   `json:"launcher"`
}

// PerfettoOptions 启动时间测试的 Perfetto 采集配置。
type PerfettoOptions struct {
	Enabled            bool `json:"enabled"`
	MaxDurationSeconds int  `json:"maxDurationSeconds"`
	BufferSizeMB       int  `json:"bufferSizeMB"`
}

type PerfBatchOptions struct {
	Count                    int             `json:"count"`
	IntervalSeconds          int             `json:"intervalSeconds"`
	PostLaunchCaptureSeconds int             `json:"postLaunchCaptureSeconds"`
	ClearData                bool            `json:"clearData"`
	ForceStop                bool            `json:"forceStop"`
	Perfetto                 PerfettoOptions `json:"perfetto"`
}

type PerfBatchResult struct {
	Success     bool                    `json:"success"`
	Message     string                  `json:"message"`
	ResultDir   string                  `json:"resultDir"`
	Runs        []perfetto.LaunchResult `json:"runs"`
	Count       int                     `json:"count"`
	Successes   int                     `json:"successes"`
	MeanTotalMS float64                 `json:"meanTotalMs"`
	StdTotalMS  float64                 `json:"stdTotalMs"`
	MinTotalMS  int                     `json:"minTotalMs"`
	MaxTotalMS  int                     `json:"maxTotalMs"`
}

type PerfAggregateReportResult struct {
	Success    bool   `json:"success"`
	Message    string `json:"message"`
	ReportFile string `json:"reportFile"`
	ResultDir  string `json:"resultDir"`
}

// PerfettoStartResult 启动计时结果。
type PerfettoStartResult struct {
	Success      bool                      `json:"success"`
	Message      string                    `json:"message"`
	Output       string                    `json:"output"`
	Timestamp    string                    `json:"timestamp"`
	ThisTimeMS   int                       `json:"thisTimeMs"`
	TotalTimeMS  int                       `json:"totalTimeMs"`
	WaitTimeMS   int                       `json:"waitTimeMs"`
	ResultDir    string                    `json:"resultDir"`
	TraceEnabled bool                      `json:"traceEnabled"`
	Capability   perfetto.CapabilityResult `json:"capability"`
}

// PerfettoStopResult 停止计时结果。
type PerfettoStopResult struct {
	Success         bool                      `json:"success"`
	Message         string                    `json:"message"`
	TraceFile       string                    `json:"traceFile"`
	LogcatFile      string                    `json:"logcatFile"`
	ReportFile      string                    `json:"reportFile"`
	TimelineFile    string                    `json:"timelineFile"`
	TraceSizeBytes  int64                     `json:"traceSizeBytes"`
	TraceAnalyzable bool                      `json:"traceAnalyzable"`
	TraceWarning    string                    `json:"traceWarning"`
	ResultDir       string                    `json:"resultDir"`
	Reason          string                    `json:"reason"`
	Capability      perfetto.CapabilityResult `json:"capability"`
}

// PerfLaunchActivity 启动 Activity 并返回 am start -W 的输出
func (a *App) PerfLaunchActivity(serial, component string) PerfLaunchResult {
	component = strings.TrimSpace(component)
	if err := validatePerfComponent(component); err != nil {
		return PerfLaunchResult{Success: false, Message: err.Error()}
	}

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
	thisTime, totalTime, waitTime := perfetto.ParseAmStartOutput(string(output))

	return PerfLaunchResult{
		Success:     true,
		Message:     "启动成功",
		Output:      string(output),
		Timestamp:   timestamp,
		ThisTimeMS:  thisTime,
		TotalTimeMS: totalTime,
		WaitTimeMS:  waitTime,
	}
}

func (a *App) PerfRunLaunchBatch(serial, component string, opts PerfBatchOptions) PerfBatchResult {
	serial = strings.TrimSpace(serial)
	component = strings.TrimSpace(component)
	if serial == "" {
		return PerfBatchResult{Success: false, Message: "请先选择设备"}
	}
	if err := validatePerfComponent(component); err != nil {
		return PerfBatchResult{Success: false, Message: err.Error()}
	}

	if opts.Count <= 0 {
		opts.Count = 5
	}
	if opts.Count > 50 {
		opts.Count = 50
	}
	if opts.IntervalSeconds < 0 {
		opts.IntervalSeconds = 0
	}
	if opts.PostLaunchCaptureSeconds <= 0 {
		opts.PostLaunchCaptureSeconds = 5
	}
	if opts.PostLaunchCaptureSeconds > 60 {
		opts.PostLaunchCaptureSeconds = 60
	}

	a.cmdMu.Lock()
	if a.perfResult != nil || a.perfSession != nil || a.perfLogcat != nil {
		a.cmdMu.Unlock()
		return PerfBatchResult{Success: false, Message: "当前已有启动时间测试在运行"}
	}
	a.cmdMu.Unlock()

	batchDir := filepath.Join(a.resolvedOutputDir(), "perf", "batch-"+time.Now().Format("20060102-150405")+"-"+fmt.Sprint(time.Now().UnixNano()))
	if err := os.MkdirAll(batchDir, 0755); err != nil {
		return PerfBatchResult{Success: false, Message: fmt.Sprintf("创建批量结果目录失败: %v", err)}
	}

	packageName := perfetto.PackageNameFromComponent(component)
	result := PerfBatchResult{
		Success:   true,
		Message:   "批量启动测试完成",
		ResultDir: batchDir,
		Count:     opts.Count,
		Runs:      make([]perfetto.LaunchResult, 0, opts.Count),
	}

	for i := 1; i <= opts.Count; i++ {
		runDir := filepath.Join(batchDir, fmt.Sprintf("run-%02d", i))
		run := a.runLaunchBatchOnce(serial, component, packageName, runDir, opts, i)
		result.Runs = append(result.Runs, run)
		if run.Success {
			result.Successes++
		}
		if i < opts.Count && opts.IntervalSeconds > 0 {
			time.Sleep(time.Duration(opts.IntervalSeconds) * time.Second)
		}
	}

	fillBatchStats(&result)
	result.Success = result.Successes == len(result.Runs)
	if !result.Success {
		result.Message = fmt.Sprintf("批量启动测试完成，%d/%d 成功", result.Successes, len(result.Runs))
	}
	_ = saveJSON(filepath.Join(batchDir, "summary.json"), result)
	return result
}

func (a *App) runLaunchBatchOnce(serial, component, packageName, resultDir string, opts PerfBatchOptions, index int) perfetto.LaunchResult {
	_ = os.MkdirAll(resultDir, 0755)
	startTime := time.Now()
	result := perfetto.LaunchResult{
		Success:     false,
		Serial:      serial,
		Component:   component,
		PackageName: packageName,
		ResultDir:   resultDir,
		Timestamp:   startTime.Format("2006-01-02 15:04:05.000"),
		Message:     fmt.Sprintf("第 %d 轮启动测试", index),
	}

	var prep strings.Builder
	if opts.ForceStop || opts.ClearData {
		cmd := adb.Command("-s", serial, "shell", "am", "force-stop", packageName)
		hideConsoleWindow(cmd)
		out, err := cmd.CombinedOutput()
		fmt.Fprintf(&prep, "$ am force-stop %s\n%s\n", packageName, string(out))
		if err != nil {
			fmt.Fprintf(&prep, "force-stop error: %v\n", err)
		}
	}
	if opts.ClearData {
		cmd := adb.Command("-s", serial, "shell", "pm", "clear", packageName)
		hideConsoleWindow(cmd)
		out, err := cmd.CombinedOutput()
		fmt.Fprintf(&prep, "$ pm clear %s\n%s\n", packageName, string(out))
		if err != nil {
			fmt.Fprintf(&prep, "pm clear error: %v\n", err)
		}
	}
	if prep.Len() > 0 {
		_ = os.WriteFile(filepath.Join(resultDir, "prep.txt"), []byte(prep.String()), 0644)
	}

	logcatSession, logcatErr := perfetto.StartLogcat(serial, resultDir)
	if logcatErr == nil {
		result.LogcatFile = logcatSession.LogFile
	} else {
		result.Message = fmt.Sprintf("启动 logcat 失败: %v", logcatErr)
	}

	var session *perfetto.Session
	if opts.Perfetto.Enabled {
		perfOpts := perfetto.NormalizeOptions(perfetto.Options{
			Enabled:            true,
			MaxDurationSeconds: opts.PostLaunchCaptureSeconds + 5,
			BufferSizeMB:       opts.Perfetto.BufferSizeMB,
		})
		var err error
		session, err = perfetto.Start(serial, packageName, resultDir, perfOpts)
		if err != nil {
			result.Capability = perfetto.DetectCapability(serial)
			result.TraceWarning = err.Error()
		} else {
			result.Capability = session.Capability
		}
	}

	cmd := adb.Command("-s", serial, "shell", "am", "start", "-W", "-n", component)
	hideConsoleWindow(cmd)
	output, launchErr := cmd.CombinedOutput()
	rawOutput := string(output)
	result.RawOutput = rawOutput
	result.ThisTimeMS, result.TotalTimeMS, result.WaitTimeMS = perfetto.ParseAmStartOutput(rawOutput)
	_ = perfetto.SaveAmStartOutput(resultDir, rawOutput)

	if opts.PostLaunchCaptureSeconds > 0 {
		time.Sleep(time.Duration(opts.PostLaunchCaptureSeconds) * time.Second)
	}
	if logcatSession != nil {
		_ = logcatSession.Stop()
		result.LogcatFile = logcatSession.LogFile
		a.saveLogcatAnalysis(&result)
	}
	if session != nil {
		traceFile, err := session.StopAndPull()
		result.TraceFile = traceFile
		result.TraceSizeBytes = session.TraceSizeBytes
		result.TraceAnalyzable = session.TraceAnalyzable
		result.TraceWarning = session.TraceWarning
		result.Capability = session.Capability
		if err != nil {
			result.TraceWarning = fmt.Sprintf("停止 Perfetto 失败: %v", err)
		}
	}

	if launchErr != nil {
		result.Success = false
		result.Message = fmt.Sprintf("启动失败: %v", launchErr)
	} else {
		result.Success = true
		result.Message = "启动成功"
	}
	result.StoppedAt = time.Now().Format("2006-01-02 15:04:05.000")
	result.StopReason = "batch"
	result.ManualDurationMS = time.Since(startTime).Milliseconds()
	_ = perfetto.SaveMetadata(resultDir, result)
	return result
}

// PerfStartLaunchTrace 开始启动计时：可选启动 Perfetto，并执行 am start -W。
func (a *App) PerfStartLaunchTrace(serial, component string, opts PerfettoOptions) PerfettoStartResult {
	serial = strings.TrimSpace(serial)
	component = strings.TrimSpace(component)
	if serial == "" {
		return PerfettoStartResult{Success: false, Message: "请先选择设备"}
	}
	if err := validatePerfComponent(component); err != nil {
		return PerfettoStartResult{Success: false, Message: err.Error()}
	}

	a.cmdMu.Lock()
	if a.perfResult != nil || a.perfSession != nil {
		a.cmdMu.Unlock()
		return PerfettoStartResult{Success: false, Message: "当前已有启动时间测试在运行"}
	}
	a.cmdMu.Unlock()

	resultDir, err := perfetto.CreateResultDir(a.resolvedOutputDir())
	if err != nil {
		return PerfettoStartResult{Success: false, Message: fmt.Sprintf("创建结果目录失败: %v", err)}
	}

	packageName := perfetto.PackageNameFromComponent(component)
	startTime := time.Now()
	result := &perfetto.LaunchResult{
		Success:     false,
		Serial:      serial,
		Component:   component,
		PackageName: packageName,
		ResultDir:   resultDir,
		Timestamp:   startTime.Format("2006-01-02 15:04:05.000"),
	}

	perfOpts := perfetto.NormalizeOptions(perfetto.Options{
		Enabled:            opts.Enabled,
		MaxDurationSeconds: opts.MaxDurationSeconds,
		BufferSizeMB:       opts.BufferSizeMB,
	})

	logcatSession, err := perfetto.StartLogcat(serial, resultDir)
	if err != nil {
		result.Message = err.Error()
		_ = perfetto.SaveMetadata(resultDir, *result)
		return PerfettoStartResult{Success: false, Message: err.Error(), ResultDir: resultDir}
	}
	result.LogcatFile = logcatSession.LogFile

	var session *perfetto.Session
	if perfOpts.Enabled {
		session, err = perfetto.Start(serial, packageName, resultDir, perfOpts)
		if err != nil {
			_ = logcatSession.Stop()
			result.LogcatFile = logcatSession.LogFile
			result.Capability = perfetto.DetectCapability(serial)
			result.Message = err.Error()
			_ = perfetto.SaveMetadata(resultDir, *result)
			return PerfettoStartResult{Success: false, Message: err.Error(), ResultDir: resultDir, TraceEnabled: true, Capability: result.Capability}
		}
		result.Capability = session.Capability
	}

	a.cmdMu.Lock()
	a.perfSession = session
	a.perfLogcat = logcatSession
	a.perfResult = result
	if session != nil || logcatSession != nil {
		a.perfTimer = time.AfterFunc(time.Duration(perfOpts.MaxDurationSeconds)*time.Second, func() {
			stop := a.stopLaunchTrace("timeout")
			runtime.EventsEmit(a.ctx, "perfetto-timeout", map[string]interface{}{
				"success":         stop.Success,
				"message":         stop.Message,
				"traceFile":       stop.TraceFile,
				"logcatFile":      stop.LogcatFile,
				"reportFile":      stop.ReportFile,
				"timelineFile":    stop.TimelineFile,
				"traceSizeBytes":  stop.TraceSizeBytes,
				"traceAnalyzable": stop.TraceAnalyzable,
				"traceWarning":    stop.TraceWarning,
				"resultDir":       stop.ResultDir,
				"capability":      stop.Capability,
			})
		})
	}
	a.cmdMu.Unlock()

	cmd := adb.Command("-s", serial, "shell", "am", "start", "-W", "-n", component)
	hideConsoleWindow(cmd)
	output, launchErr := cmd.CombinedOutput()
	rawOutput := string(output)
	thisTime, totalTime, waitTime := perfetto.ParseAmStartOutput(rawOutput)

	result.RawOutput = rawOutput
	result.ThisTimeMS = thisTime
	result.TotalTimeMS = totalTime
	result.WaitTimeMS = waitTime
	_ = perfetto.SaveAmStartOutput(resultDir, rawOutput)

	if launchErr != nil {
		result.Success = false
		result.Message = fmt.Sprintf("启动失败: %v", launchErr)
		_ = logcatSession.Stop()
		a.saveLogcatAnalysis(result)
		if session != nil {
			traceFile, stopErr := session.StopAndPull()
			result.TraceFile = traceFile
			result.TraceSizeBytes = session.TraceSizeBytes
			result.TraceAnalyzable = session.TraceAnalyzable
			result.TraceWarning = session.TraceWarning
			if stopErr != nil {
				result.Message = fmt.Sprintf("%s；停止 Perfetto 失败: %v", result.Message, stopErr)
			}
		}
		_ = perfetto.SaveMetadata(resultDir, *result)
		a.cmdMu.Lock()
		if a.perfSession == session {
			a.perfSession = nil
		}
		if a.perfResult == result {
			a.perfResult = nil
		}
		if a.perfLogcat == logcatSession {
			a.perfLogcat = nil
		}
		if a.perfTimer != nil {
			a.perfTimer.Stop()
			a.perfTimer = nil
		}
		a.cmdMu.Unlock()
		return PerfettoStartResult{
			Success:      false,
			Message:      result.Message,
			Output:       rawOutput,
			Timestamp:    result.Timestamp,
			ThisTimeMS:   thisTime,
			TotalTimeMS:  totalTime,
			WaitTimeMS:   waitTime,
			ResultDir:    resultDir,
			TraceEnabled: session != nil,
			Capability:   result.Capability,
		}
	}

	result.Success = true
	result.Message = "启动成功"
	_ = perfetto.SaveMetadata(resultDir, *result)
	return PerfettoStartResult{
		Success:      true,
		Message:      "启动成功",
		Output:       rawOutput,
		Timestamp:    result.Timestamp,
		ThisTimeMS:   thisTime,
		TotalTimeMS:  totalTime,
		WaitTimeMS:   waitTime,
		ResultDir:    resultDir,
		TraceEnabled: session != nil,
		Capability:   result.Capability,
	}
}

// PerfStopLaunchTrace 停止当前启动计时的 Perfetto 会话并拉取 trace。
func (a *App) PerfStopLaunchTrace() PerfettoStopResult {
	return a.stopLaunchTrace("manual")
}

func (a *App) stopLaunchTrace(reason string) PerfettoStopResult {
	a.cmdMu.Lock()
	session := a.perfSession
	logcatSession := a.perfLogcat
	result := a.perfResult
	timer := a.perfTimer
	a.perfSession = nil
	a.perfLogcat = nil
	a.perfResult = nil
	a.perfTimer = nil
	a.cmdMu.Unlock()

	if timer != nil {
		timer.Stop()
	}
	if result == nil {
		return PerfettoStopResult{Success: false, Message: "当前没有运行中的启动时间测试", Reason: reason}
	}

	now := time.Now()
	result.StoppedAt = now.Format("2006-01-02 15:04:05.000")
	result.StopReason = reason
	if startedAt, err := time.ParseInLocation("2006-01-02 15:04:05.000", result.Timestamp, time.Local); err == nil {
		result.ManualDurationMS = now.Sub(startedAt).Milliseconds()
	}
	if logcatSession != nil {
		_ = logcatSession.Stop()
		result.LogcatFile = logcatSession.LogFile
		a.saveLogcatAnalysis(result)
	}
	if session != nil {
		result.ManualDurationMS = now.Sub(session.StartedAt).Milliseconds()
		traceFile, err := session.StopAndPull()
		result.TraceFile = traceFile
		result.TraceSizeBytes = session.TraceSizeBytes
		result.TraceAnalyzable = session.TraceAnalyzable
		result.TraceWarning = session.TraceWarning
		result.Capability = session.Capability
		if err != nil {
			result.Success = false
			result.Message = fmt.Sprintf("停止 Perfetto 失败: %v", err)
			_ = perfetto.SaveMetadata(result.ResultDir, *result)
			return PerfettoStopResult{
				Success:         false,
				Message:         result.Message,
				TraceFile:       traceFile,
				LogcatFile:      result.LogcatFile,
				ReportFile:      result.ReportFile,
				TimelineFile:    result.TimelineFile,
				TraceSizeBytes:  result.TraceSizeBytes,
				TraceAnalyzable: result.TraceAnalyzable,
				TraceWarning:    result.TraceWarning,
				ResultDir:       result.ResultDir,
				Reason:          reason,
				Capability:      result.Capability,
			}
		}
		if result.TraceAnalyzable {
			result.Message = "Perfetto trace 已保存"
		} else {
			result.Message = "Perfetto trace 已保存，但当前文件不可分析"
		}
	} else {
		result.Message = "启动时间测试已结束"
	}

	_ = perfetto.SaveMetadata(result.ResultDir, *result)
	return PerfettoStopResult{
		Success:         true,
		Message:         result.Message,
		TraceFile:       result.TraceFile,
		LogcatFile:      result.LogcatFile,
		ReportFile:      result.ReportFile,
		TimelineFile:    result.TimelineFile,
		TraceSizeBytes:  result.TraceSizeBytes,
		TraceAnalyzable: result.TraceAnalyzable,
		TraceWarning:    result.TraceWarning,
		ResultDir:       result.ResultDir,
		Reason:          reason,
		Capability:      result.Capability,
	}
}

func (a *App) saveLogcatAnalysis(result *perfetto.LaunchResult) {
	if result == nil || strings.TrimSpace(result.LogcatFile) == "" {
		return
	}
	analysis, err := perfetto.AnalyzeLogcatFile(result.LogcatFile, result.PackageName, result.Component)
	if err != nil {
		return
	}
	result.LogcatAnalysis = analysis
	if timelineFile, err := perfetto.SaveTimeline(result.ResultDir, analysis); err == nil {
		result.TimelineFile = timelineFile
	}
	if reportFile, err := perfetto.SaveReport(result.ResultDir, analysis); err == nil {
		result.ReportFile = reportFile
	}
}

func fillBatchStats(result *PerfBatchResult) {
	var values []int
	for _, run := range result.Runs {
		if run.TotalTimeMS > 0 {
			values = append(values, run.TotalTimeMS)
		}
	}
	if len(values) == 0 {
		return
	}
	result.MinTotalMS = values[0]
	result.MaxTotalMS = values[0]
	var sum int
	for _, value := range values {
		sum += value
		if value < result.MinTotalMS {
			result.MinTotalMS = value
		}
		if value > result.MaxTotalMS {
			result.MaxTotalMS = value
		}
	}
	result.MeanTotalMS = float64(sum) / float64(len(values))
	var variance float64
	for _, value := range values {
		diff := float64(value) - result.MeanTotalMS
		variance += diff * diff
	}
	result.StdTotalMS = variance / float64(len(values))
	if result.StdTotalMS > 0 {
		result.StdTotalMS = sqrt(result.StdTotalMS)
	}
}

func (a *App) GeneratePerfBatchReport(resultDir string) PerfAggregateReportResult {
	resultDir = strings.TrimSpace(resultDir)
	if resultDir == "" {
		return PerfAggregateReportResult{Success: false, Message: "请先完成一次批量启动测试，或打开已有批量结果目录"}
	}

	summaryPath := filepath.Join(resultDir, "summary.json")
	data, err := os.ReadFile(summaryPath)
	if err != nil {
		return PerfAggregateReportResult{Success: false, Message: fmt.Sprintf("读取批量 summary.json 失败: %v", err), ResultDir: resultDir}
	}

	var batch PerfBatchResult
	if err := json.Unmarshal(data, &batch); err != nil {
		return PerfAggregateReportResult{Success: false, Message: fmt.Sprintf("解析批量 summary.json 失败: %v", err), ResultDir: resultDir}
	}
	if len(batch.Runs) == 0 {
		return PerfAggregateReportResult{Success: false, Message: "summary.json 中没有测试轮次数据", ResultDir: resultDir}
	}
	if strings.TrimSpace(batch.ResultDir) == "" {
		batch.ResultDir = resultDir
	}
	fillBatchStats(&batch)

	reportPath := filepath.Join(resultDir, "aggregate_report.txt")
	report := buildPerfAggregateReport(batch, reportPath)
	if err := os.WriteFile(reportPath, []byte(report), 0644); err != nil {
		return PerfAggregateReportResult{Success: false, Message: fmt.Sprintf("写入聚合报告失败: %v", err), ResultDir: resultDir}
	}

	return PerfAggregateReportResult{
		Success:    true,
		Message:    "聚合性能测试报告已生成",
		ReportFile: reportPath,
		ResultDir:  resultDir,
	}
}

func buildPerfAggregateReport(batch PerfBatchResult, reportPath string) string {
	totalRuns := len(batch.Runs)
	successRuns := batch.Successes
	if successRuns == 0 {
		for _, run := range batch.Runs {
			if run.Success {
				successRuns++
			}
		}
	}
	failedRuns := totalRuns - successRuns
	successRate := 0.0
	if totalRuns > 0 {
		successRate = float64(successRuns) * 100 / float64(totalRuns)
	}

	totalValues := collectPositiveTotals(batch.Runs)
	waitValues := collectPositiveWaits(batch.Runs)
	manualValues := collectManualDurations(batch.Runs)
	sort.Ints(totalValues)
	sort.Ints(waitValues)
	sort.Slice(manualValues, func(i, j int) bool { return manualValues[i] < manualValues[j] })

	meanTotal := meanInts(totalValues)
	stdTotal := stddevInts(totalValues, meanTotal)
	cv := 0.0
	if meanTotal > 0 {
		cv = stdTotal * 100 / meanTotal
	}

	firstRun := batch.Runs[0]
	var b strings.Builder
	fmt.Fprintf(&b, "启动性能批量测试聚合报告\n")
	fmt.Fprintf(&b, "============================================================\n\n")
	fmt.Fprintf(&b, "报告文件: %s\n", reportPath)
	fmt.Fprintf(&b, "生成时间: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "结果目录: %s\n\n", batch.ResultDir)

	fmt.Fprintf(&b, "一、测试结论\n")
	fmt.Fprintf(&b, "------------------------------------------------------------\n")
	if successRuns == totalRuns {
		fmt.Fprintf(&b, "结论: 本次 %d 次启动全部成功，可以使用 TotalTime 作为本次冷启动耗时的主要结论。\n", totalRuns)
		fmt.Fprintf(&b, "平均启动耗时约 %.0f ms，最快 %d ms，最慢 %d ms。\n", meanTotal, minInt(totalValues), maxInt(totalValues))
		if len(totalValues) >= 2 {
			fmt.Fprintf(&b, "稳定性: 标准方差 %.0f ms，波动系数 %.1f%%。%s\n", stdTotal, cv, stabilityText(cv))
		}
	} else if successRuns > 0 {
		fmt.Fprintf(&b, "结论: 本次 %d 次测试中 %d 次成功、%d 次失败，成功率 %.1f%%。启动链路存在不稳定或参数/环境问题，建议先处理失败轮次再比较性能。\n", totalRuns, successRuns, failedRuns, successRate)
		fmt.Fprintf(&b, "成功轮次的平均启动耗时约 %.0f ms，最快 %d ms，最慢 %d ms。\n", meanTotal, minInt(totalValues), maxInt(totalValues))
	} else {
		fmt.Fprintf(&b, "结论: 本次 %d 次启动全部失败，无法得出有效启动耗时结论。需要先解决启动失败原因，再重新跑批量测试。\n", totalRuns)
	}
	if failedRuns > 0 {
		fmt.Fprintf(&b, "主要失败信息: %s\n", topFailureMessage(batch.Runs))
	}
	fmt.Fprintf(&b, "\n")

	fmt.Fprintf(&b, "二、本次测试对象和链路\n")
	fmt.Fprintf(&b, "------------------------------------------------------------\n")
	fmt.Fprintf(&b, "设备: %s\n", emptyAsDash(firstRun.Serial))
	fmt.Fprintf(&b, "应用包名: %s\n", emptyAsDash(firstRun.PackageName))
	fmt.Fprintf(&b, "启动 Component: %s\n", emptyAsDash(firstRun.Component))
	fmt.Fprintf(&b, "测试轮数: %d\n", totalRuns)
	fmt.Fprintf(&b, "测试方式: 每一轮先按配置执行 force-stop / pm clear，然后采集 logcat，再执行 am start -W -n <Component> 启动目标 Activity。\n")
	fmt.Fprintf(&b, "数据来源: Android am start -W 输出、logcat 时间线、可选 Perfetto trace。\n")
	fmt.Fprintf(&b, "注意: TotalTime/WaitTime 主要衡量 Activity 启动到首帧相关耗时，不等于用户看到页面完全加载好所需的全部时间。\n\n")

	fmt.Fprintf(&b, "三、核心数据汇总\n")
	fmt.Fprintf(&b, "------------------------------------------------------------\n")
	fmt.Fprintf(&b, "成功次数: %d/%d (%.1f%%)\n", successRuns, totalRuns, successRate)
	if len(totalValues) > 0 {
		fmt.Fprintf(&b, "TotalTime 平均值: %.2f ms\n", meanTotal)
		fmt.Fprintf(&b, "TotalTime 中位数: %.0f ms\n", percentileInt(totalValues, 50))
		fmt.Fprintf(&b, "TotalTime P90: %.0f ms\n", percentileInt(totalValues, 90))
		fmt.Fprintf(&b, "TotalTime 最小/最大: %d / %d ms\n", minInt(totalValues), maxInt(totalValues))
		fmt.Fprintf(&b, "TotalTime 标准方差: %.2f ms\n", stdTotal)
		fmt.Fprintf(&b, "TotalTime 波动系数: %.1f%%\n", cv)
	} else {
		fmt.Fprintf(&b, "TotalTime: 无有效数据，通常表示启动命令失败或 am start 未返回计时结果。\n")
	}
	if len(waitValues) > 0 {
		fmt.Fprintf(&b, "WaitTime 平均值: %.2f ms，P90: %.0f ms\n", meanInts(waitValues), percentileInt(waitValues, 90))
	}
	if len(manualValues) > 0 {
		fmt.Fprintf(&b, "人工计时/采集窗口平均耗时: %.2f ms\n", meanInt64s(manualValues))
	}
	fmt.Fprintf(&b, "\n")

	fmt.Fprintf(&b, "四、逐轮明细\n")
	fmt.Fprintf(&b, "------------------------------------------------------------\n")
	fmt.Fprintf(&b, "轮次 | 结果 | TotalTime | WaitTime | ThisTime | 手动耗时 | 说明\n")
	for i, run := range batch.Runs {
		status := "失败"
		if run.Success {
			status = "成功"
		}
		fmt.Fprintf(&b, "%02d | %s | %s | %s | %s | %d ms | %s\n",
			i+1,
			status,
			msOrDash(run.TotalTimeMS),
			msOrDash(run.WaitTimeMS),
			msOrDash(run.ThisTimeMS),
			run.ManualDurationMS,
			compactRunMessage(run),
		)
	}
	fmt.Fprintf(&b, "\n")

	fmt.Fprintf(&b, "五、每轮产物位置\n")
	fmt.Fprintf(&b, "------------------------------------------------------------\n")
	for i, run := range batch.Runs {
		fmt.Fprintf(&b, "第 %02d 轮:\n", i+1)
		fmt.Fprintf(&b, "  目录: %s\n", emptyAsDash(run.ResultDir))
		fmt.Fprintf(&b, "  am start 输出: %s\n", filepath.Join(run.ResultDir, "am_start.txt"))
		fmt.Fprintf(&b, "  logcat: %s\n", emptyAsDash(run.LogcatFile))
		fmt.Fprintf(&b, "  单轮报告: %s\n", emptyAsDash(run.ReportFile))
		if run.TraceFile != "" {
			fmt.Fprintf(&b, "  Perfetto trace: %s\n", run.TraceFile)
		}
	}
	fmt.Fprintf(&b, "\n")

	fmt.Fprintf(&b, "六、异常和风险提示\n")
	fmt.Fprintf(&b, "------------------------------------------------------------\n")
	if failedRuns == 0 {
		fmt.Fprintf(&b, "未发现启动失败轮次。\n")
	} else {
		for i, run := range batch.Runs {
			if run.Success {
				continue
			}
			fmt.Fprintf(&b, "第 %02d 轮失败: %s\n", i+1, compactRunMessage(run))
			if reason := inferFailureReason(run); reason != "" {
				fmt.Fprintf(&b, "  建议: %s\n", reason)
			}
		}
	}
	if hasTraceWarnings(batch.Runs) {
		fmt.Fprintf(&b, "Perfetto/Trace 提示:\n")
		for i, run := range batch.Runs {
			if strings.TrimSpace(run.TraceWarning) != "" {
				fmt.Fprintf(&b, "  第 %02d 轮: %s\n", i+1, strings.TrimSpace(run.TraceWarning))
			}
		}
	}
	fmt.Fprintf(&b, "\n")

	fmt.Fprintf(&b, "七、名词解释\n")
	fmt.Fprintf(&b, "------------------------------------------------------------\n")
	fmt.Fprintf(&b, "TotalTime: Android am start -W 返回的总启动耗时，通常用来衡量 Activity 冷启动到首帧相关阶段的耗时。\n")
	fmt.Fprintf(&b, "WaitTime: am start 命令等待启动完成的时间，一般接近 TotalTime，但受系统调度影响可能不同。\n")
	fmt.Fprintf(&b, "ThisTime: 当前 Activity 自身启动耗时；如果启动过程中跳转了多个 Activity，它可能小于 TotalTime。\n")
	fmt.Fprintf(&b, "手动耗时: 工具从本轮开始到采集结束的墙钟时间，包含启动后继续采集 logcat/trace 的时间，不等同于启动性能。\n")
	fmt.Fprintf(&b, "P90: 90%% 分位值。可理解为 10 次里大约 9 次不会超过这个耗时，比平均值更能反映偶发慢启动。\n")
	fmt.Fprintf(&b, "标准方差: 数据波动大小。越小表示多次启动越稳定。\n")
	fmt.Fprintf(&b, "波动系数: 标准方差 / 平均值。用于判断稳定性，通常低于 10%% 较稳定，10%%-20%% 有波动，高于 20%% 需要关注。\n")
	fmt.Fprintf(&b, "logcat: Android 系统日志，可用于还原启动过程中的关键事件和错误。\n")
	fmt.Fprintf(&b, "Perfetto trace: Android 性能追踪文件，可用于更深入分析线程、CPU、调度等问题；未启用 Perfetto 时不会生成。\n")
	return b.String()
}

func collectPositiveTotals(runs []perfetto.LaunchResult) []int {
	values := make([]int, 0, len(runs))
	for _, run := range runs {
		if run.TotalTimeMS > 0 {
			values = append(values, run.TotalTimeMS)
		}
	}
	return values
}

func collectPositiveWaits(runs []perfetto.LaunchResult) []int {
	values := make([]int, 0, len(runs))
	for _, run := range runs {
		if run.WaitTimeMS > 0 {
			values = append(values, run.WaitTimeMS)
		}
	}
	return values
}

func collectManualDurations(runs []perfetto.LaunchResult) []int64 {
	values := make([]int64, 0, len(runs))
	for _, run := range runs {
		if run.ManualDurationMS > 0 {
			values = append(values, run.ManualDurationMS)
		}
	}
	return values
}

func meanInts(values []int) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0
	for _, value := range values {
		sum += value
	}
	return float64(sum) / float64(len(values))
}

func meanInt64s(values []int64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum int64
	for _, value := range values {
		sum += value
	}
	return float64(sum) / float64(len(values))
}

func stddevInts(values []int, mean float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var variance float64
	for _, value := range values {
		diff := float64(value) - mean
		variance += diff * diff
	}
	return sqrt(variance / float64(len(values)))
}

func percentileInt(sortedValues []int, percentile int) float64 {
	if len(sortedValues) == 0 {
		return 0
	}
	if len(sortedValues) == 1 {
		return float64(sortedValues[0])
	}
	if percentile <= 0 {
		return float64(sortedValues[0])
	}
	if percentile >= 100 {
		return float64(sortedValues[len(sortedValues)-1])
	}
	rank := float64(percentile) / 100 * float64(len(sortedValues)-1)
	lower := int(rank)
	upper := lower + 1
	if upper >= len(sortedValues) {
		return float64(sortedValues[lower])
	}
	weight := rank - float64(lower)
	return float64(sortedValues[lower])*(1-weight) + float64(sortedValues[upper])*weight
}

func minInt(values []int) int {
	if len(values) == 0 {
		return 0
	}
	return values[0]
}

func maxInt(values []int) int {
	if len(values) == 0 {
		return 0
	}
	return values[len(values)-1]
}

func stabilityText(cv float64) string {
	switch {
	case cv <= 0:
		return "当前样本波动不可判断。"
	case cv < 10:
		return "整体比较稳定。"
	case cv < 20:
		return "存在一定波动，建议结合逐轮日志查看是否有偶发干扰。"
	default:
		return "波动较大，建议排查设备负载、网络、缓存、广告或初始化任务等影响。"
	}
}

func topFailureMessage(runs []perfetto.LaunchResult) string {
	counts := map[string]int{}
	for _, run := range runs {
		if run.Success {
			continue
		}
		message := compactRunMessage(run)
		if message == "" {
			message = "未知失败"
		}
		counts[message]++
	}
	topMessage := ""
	topCount := 0
	for message, count := range counts {
		if count > topCount || (count == topCount && message < topMessage) {
			topMessage = message
			topCount = count
		}
	}
	if topMessage == "" {
		return "-"
	}
	return fmt.Sprintf("%s（出现 %d 次）", topMessage, topCount)
}

func compactRunMessage(run perfetto.LaunchResult) string {
	raw := strings.TrimSpace(run.RawOutput)
	if strings.Contains(raw, "Bad component name") {
		return firstMatchingLine(raw, "Bad component name")
	}
	if strings.Contains(raw, "Error type 3") || strings.Contains(raw, "Activity class") {
		return firstNonEmptyLine(raw)
	}
	if strings.TrimSpace(run.Message) != "" {
		return strings.TrimSpace(run.Message)
	}
	return firstNonEmptyLine(raw)
}

func inferFailureReason(run perfetto.LaunchResult) string {
	raw := strings.TrimSpace(run.RawOutput)
	switch {
	case strings.Contains(raw, "Bad component name"):
		return "Component 不完整或格式错误。请填写类似 com.example/.MainActivity 或 com.example/com.example.MainActivity 的完整值。"
	case strings.Contains(raw, "Error type 3") || strings.Contains(raw, "Activity class"):
		return "目标 Activity 不存在或未导出。请用 Component 下拉重新选择可启动 Activity。"
	case strings.Contains(strings.ToLower(raw), "permission"):
		return "可能存在权限限制。请确认目标 Activity 是否允许 adb shell 启动。"
	case strings.Contains(strings.ToLower(run.Message), "logcat"):
		return "logcat 采集启动失败。请确认设备连接稳定，adb 可正常执行 logcat。"
	default:
		return "请打开该轮 am_start.txt 和 logcat.txt 查看更详细错误。"
	}
}

func hasTraceWarnings(runs []perfetto.LaunchResult) bool {
	for _, run := range runs {
		if strings.TrimSpace(run.TraceWarning) != "" {
			return true
		}
	}
	return false
}

func msOrDash(value int) string {
	if value <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d ms", value)
}

func emptyAsDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func firstMatchingLine(text, pattern string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, pattern) {
			return line
		}
	}
	return firstNonEmptyLine(text)
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func sqrt(value float64) float64 {
	if value <= 0 {
		return 0
	}
	x := value
	for i := 0; i < 20; i++ {
		x = 0.5 * (x + value/x)
	}
	return x
}

func saveJSON(path string, value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
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
	return logassert.AssertLogContent(logFile, expectedQuery)
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
		if err := trimLog(); err != nil {
			if strings.Contains(err.Error(), "日志中未找到起始标记") {
				a.emitPlayOutput(fmt.Sprintf("日志起始标记未采集到，已保留完整日志: %v", err))
				return nil
			}
			return err
		}
		return nil
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
