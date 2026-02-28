package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"voice-qa/internal/adb"
	"voice-qa/internal/audio"
	"voice-qa/internal/config"
	"voice-qa/internal/player"
	"voice-qa/internal/tts"
)

const defaultModel = "zh_CN-huayan-medium.onnx"

// 全局配置
var cfg *config.Config

func main() {
	// 命令行参数
	configFile := flag.String("config", "", "配置文件路径 (默认自动查找 config.json)")
	text := flag.String("text", "", "要转换的文本")
	file := flag.String("file", "", "输入文本文件 (按行批量生成)")
	output := flag.String("output", "", "输出文件路径 (批量模式为输出目录)")
	model := flag.String("model", "", "模型文件路径 (默认自动查找)")
	simple := flag.Bool("simple", false, "简单模式，不添加前后缀和静音")
	playMode := flag.Bool("play", false, "播放模式：播放已生成的语音，同时记录logcat和截图")
	playDir := flag.String("playdir", "", "播放模式的音频目录 (默认使用 output 目录)")
	initConfig := flag.Bool("init", false, "生成默认配置文件 config.json")
	flag.Parse()

	// 生成默认配置文件
	if *initConfig {
		generateDefaultConfig()
		return
	}

	// 加载配置
	var err error
	cfg, err = config.LoadOrCreate(*configFile)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 命令行参数覆盖配置文件
	textFile := *file
	if textFile == "" {
		textFile = cfg.TextFile
	}

	outputDir := *output
	if outputDir == "" {
		outputDir = cfg.OutputDir
	}

	modelPath := *model
	if modelPath == "" {
		modelPath = cfg.ModelFile
	}

	// 播放模式
	if *playMode {
		wavDir := *playDir
		if wavDir == "" {
			wavDir = outputDir // 默认使用输出目录
		}
		runPlayMode(textFile, wavDir)
		return
	}

	if *text == "" && textFile == "" {
		printUsage()
		os.Exit(1)
	}

	// 创建 TTS 引擎
	var engine tts.Engine
	if cfg.VoiceID != "" {
		// 根据配置的声音 ID 创建引擎
		engine = tts.CreateEngine(cfg.VoiceID)
	} else {
		// 兼容旧配置：使用 Piper
		if modelPath == "" {
			modelPath = tts.FindModelPath(defaultModel)
		}
		engine = tts.NewPiper(modelPath)
	}

	// 批量模式
	if textFile != "" && *text == "" {
		processBatch(engine, textFile, outputDir, *simple)
		return
	}

	// 单条模式
	if *simple {
		err := engine.Synthesize(*text, outputDir)
		if err != nil {
			log.Fatalf("语音合成失败: %v", err)
		}
	} else {
		err := generateFullAudio(engine, *text, outputDir)
		if err != nil {
			log.Fatalf("语音合成失败: %v", err)
		}
	}
	fmt.Printf("语音已生成: %s\n", outputDir)
}

func printUsage() {
	fmt.Println("TTS 文本转语音工具")
	fmt.Println("")
	fmt.Println("用法:")
	fmt.Println("  tts -init                         生成默认配置文件")
	fmt.Println("  tts                               使用 config.json 配置生成语音")
	fmt.Println("  tts -play                         播放模式 (使用默认输出目录)")
	fmt.Println("  tts -play -playdir ./scene1       播放模式 (指定目录)")
	fmt.Println("  tts -text \"你好\" -output out.wav  单条生成")
	fmt.Println("  tts -file text.txt -output dir    批量生成")
	fmt.Println("")
	fmt.Println("选项:")
	fmt.Println("  -config   指定配置文件路径")
	fmt.Println("  -init     生成默认配置文件 config.json")
	fmt.Println("  -text     要转换的文本")
	fmt.Println("  -file     输入文本文件路径")
	fmt.Println("  -output   输出路径")
	fmt.Println("  -model    模型文件路径")
	fmt.Println("  -simple   简单模式，不添加前后缀")
	fmt.Println("  -play     播放模式")
	fmt.Println("  -playdir  播放模式的音频目录 (默认使用 output)")
}

func generateDefaultConfig() {
	configPath := "config.json"

	// 检查文件是否存在
	if _, err := os.Stat(configPath); err == nil {
		fmt.Printf("配置文件已存在: %s\n", configPath)
		fmt.Print("是否覆盖? (y/N): ")
		var answer string
		fmt.Scanln(&answer)
		if strings.ToLower(answer) != "y" {
			fmt.Println("已取消")
			return
		}
	}

	cfg := config.DefaultConfig()
	if err := cfg.Save(configPath); err != nil {
		log.Fatalf("生成配置文件失败: %v", err)
	}

	fmt.Printf("已生成配置文件: %s\n", configPath)
	fmt.Println("")
	fmt.Println("配置说明:")
	fmt.Println("  text_file              - 输入文本文件路径")
	fmt.Println("  output_dir             - 输出目录")
	fmt.Println("  model_file             - 模型文件路径 (留空自动查找)")
	fmt.Println("  prefix_text            - 开头语音文本")
	fmt.Println("  suffix_text1           - 结尾语音文本1")
	fmt.Println("  suffix_text2           - 结尾语音文本2")
	fmt.Println("  silence_start          - 开始前静音秒数")
	fmt.Println("  silence_after_prefix   - 前缀后静音秒数")
	fmt.Println("  silence_after_main     - 主文本后静音秒数")
	fmt.Println("  silence_after_suffix   - 后缀1后静音秒数")
	fmt.Println("  silence_end            - 结束后静音秒数")
	fmt.Println("  screenshot_before_end  - 结束前多少秒截图")
	fmt.Println("  filename_max_length    - 文件名最大字符数")
}

// TestResult 测试结果
type TestResult struct {
	Text       string
	LogFile    string
	PngFile    string
	WavFile    string
	Passed     bool
	AssertInfo string
}

// runPlayMode 运行播放模式
func runPlayMode(textFile, wavDir string) {
	if textFile == "" {
		log.Fatal("播放模式需要指定文本文件 (-file 或 config.json 中的 text_file)")
	}

	// 检查 adb 设备
	fmt.Println("检查 adb 设备连接...")
	if err := adb.CheckDevice(); err != nil {
		log.Fatalf("adb 设备检查失败: %v", err)
	}
	fmt.Println("adb 设备已连接")

	// 读取文本文件
	f, err := os.Open(textFile)
	if err != nil {
		log.Fatalf("无法打开文件: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNum := 0

	fmt.Println("---")
	fmt.Println("开始播放模式")
	fmt.Printf("流程: 播放语音 -> logcat记录 -> 倒数%.0f秒截图 -> 停止记录 -> 断言验证\n", cfg.ScreenshotBeforeEnd)
	if cfg.EnableVideoRecording {
		fmt.Printf("视频录制: 启用 (开始后%.0f秒启动, 结束前%.0f秒停止)\n", cfg.RecordingStartDelay, cfg.RecordingEndBeforeEnd)
	} else {
		fmt.Println("视频录制: 禁用")
	}
	fmt.Println("---")

	var results []TestResult
	passCount := 0
	failCount := 0
	reportFile := filepath.Join(wavDir, "test_report.txt")
	isFirstLine := true

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// 处理 UTF-8 BOM (Windows 记事本可能添加)
		if isFirstLine {
			line = strings.TrimPrefix(line, "\xef\xbb\xbf")
			isFirstLine = false
		}
		line = strings.TrimSpace(line)

		if line == "" {
			continue
		}

		// 构建文件名
		safeName := sanitizeFileName(truncate(line, cfg.FileNameMaxLength))
		baseName := fmt.Sprintf("%04d%s", lineNum, safeName)

		wavFile := filepath.Join(wavDir, baseName+".wav")
		logFile := filepath.Join(wavDir, baseName+".log")
		pngFile := filepath.Join(wavDir, baseName+".png")

		result := TestResult{
			Text:    line,
			LogFile: logFile,
			PngFile: pngFile,
			WavFile: wavFile,
		}

		// 检查 wav 文件是否存在
		if _, err := os.Stat(wavFile); os.IsNotExist(err) {
			fmt.Printf("[%d] %s ... 跳过 (wav文件不存在)\n", lineNum, truncate(line, 30))
			continue
		}

		fmt.Printf("[%d] %s\n", lineNum, truncate(line, 30))

		// 执行播放流程
		if err := playWithLogAndScreenshot(wavFile, logFile, pngFile); err != nil {
			fmt.Printf("    错误: %v\n", err)
			result.Passed = false
			result.AssertInfo = fmt.Sprintf("播放错误: %v", err)
			results = append(results, result)
			failCount++
			// 每完成一条就增量保存测试报告
			saveTestReport(reportFile, results, passCount, failCount)
			continue
		}

		// 断言验证
		passed, assertInfo := assertLogContent(logFile, line)
		result.Passed = passed
		result.AssertInfo = assertInfo

		if passed {
			fmt.Printf("    断言: ✓ 通过\n")
			passCount++
		} else {
			fmt.Printf("    断言: ✗ 失败 - %s\n", assertInfo)
			failCount++
		}

		mp4File := strings.TrimSuffix(wavFile, ".wav") + ".mp4"
		fmt.Printf("    完成: %s, %s, %s, %s\n",
			filepath.Base(logFile),
			filepath.Base(pngFile),
			filepath.Base(mp4File),
			filepath.Base(wavFile))

		results = append(results, result)

		// 每完成一条就增量保存测试报告
		saveTestReport(reportFile, results, passCount, failCount)
	}

	fmt.Println("---")
	fmt.Println("播放模式结束")
	fmt.Println("")
	fmt.Println("================== 测试结果汇总 ==================")
	fmt.Printf("总计: %d, 通过: %d, 失败: %d\n", passCount+failCount, passCount, failCount)
	fmt.Println("")

	for i, r := range results {
		status := "✓ PASS"
		if !r.Passed {
			status = "✗ FAIL"
		}
		fmt.Printf("[%d] %s %s\n", i+1, status, truncate(r.Text, 30))
		if !r.Passed {
			fmt.Printf("    原因: %s\n", r.AssertInfo)
		}
	}
	fmt.Println("==================================================")

	// 最终保存测试报告（标记为已完成）
	saveTestReport(reportFile, results, passCount, failCount, true)
	fmt.Printf("\n测试报告已保存: %s\n", reportFile)
}

// assertLogContent 断言日志内容
func assertLogContent(logFile, expectedQuery string) (bool, string) {
	data, err := os.ReadFile(logFile)
	if err != nil {
		return false, fmt.Sprintf("无法读取日志文件: %v", err)
	}

	content := string(data)

	// 查找 nlpResult 中的 query
	// 格式: "nlpResult":{"intent":{"name":"openApp","query":"我要看电视"}
	expectedPattern := fmt.Sprintf(`"query":"%s"`, expectedQuery)

	if strings.Contains(content, expectedPattern) {
		return true, fmt.Sprintf("找到匹配: %s", expectedPattern)
	}

	// 尝试查找部分匹配
	if strings.Contains(content, `"nlpResult"`) {
		return false, fmt.Sprintf("日志包含 nlpResult 但未找到 query=\"%s\"", expectedQuery)
	}

	return false, "日志中未找到 nlpResult 相关内容"
}

// saveTestReport 保存测试报告
func saveTestReport(reportFile string, results []TestResult, passCount, failCount int, args ...bool) {
	isComplete := false
	if len(args) > 0 {
		isComplete = args[0]
	}

	var sb strings.Builder

	sb.WriteString("================== 测试执行报告 ==================\n")
	sb.WriteString(fmt.Sprintf("执行时间: %s\n", time.Now().Format("2006-01-02 15:04:05")))
	if isComplete {
		sb.WriteString("执行状态: 已完成\n")
	} else {
		sb.WriteString("执行状态: 执行中/已中断\n")
	}
	sb.WriteString(fmt.Sprintf("总计: %d, 通过: %d, 失败: %d\n", passCount+failCount, passCount, failCount))
	sb.WriteString("\n")
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

	os.WriteFile(reportFile, []byte(sb.String()), 0644)
}

// playWithLogAndScreenshot 播放语音，同时记录logcat、录制视频和截图
func playWithLogAndScreenshot(wavFile, logFile, pngFile string) error {
	// 获取音频时长
	duration, err := player.GetWAVDuration(wavFile)
	if err != nil {
		return fmt.Errorf("获取音频时长失败: %w", err)
	}

	screenshotBeforeEnd := cfg.ScreenshotBeforeEnd
	fmt.Printf("    时长: %.1f秒, 截图时间: %.1f秒\n", duration, duration-screenshotBeforeEnd)

	// 视频录制配置
	enableVideoRecording := cfg.EnableVideoRecording
	recordingStartDelay := cfg.RecordingStartDelay
	recordingEndBeforeEnd := cfg.RecordingEndBeforeEnd

	// 视频文件路径
	mp4File := strings.TrimSuffix(wavFile, ".wav") + ".mp4"

	// 1. 启动 logcat 记录
	fmt.Print("    启动logcat...")
	logRecorder, err := adb.StartLogcat(logFile)
	if err != nil {
		return fmt.Errorf("启动logcat失败: %w", err)
	}
	fmt.Println("OK")

	// 等待 logcat 准备就绪
	time.Sleep(500 * time.Millisecond)

	// 2. 计算截图时间
	screenshotTime := duration - screenshotBeforeEnd
	if screenshotTime < 0 {
		screenshotTime = duration / 2 // 如果音频太短，在中间截图
	}

	// 3. 视频录制相关变量
	videoRecorderDone := make(chan bool, 1)
	stopRecording := make(chan bool, 1) // 用于通知录制 goroutine 提前停止

	// 4. 设置截图定时器
	screenshotDone := make(chan bool, 1)
	go func() {
		time.Sleep(time.Duration(screenshotTime * float64(time.Second)))
		fmt.Print("    截图...")
		if err := adb.Screenshot(pngFile); err != nil {
			fmt.Printf("失败: %v\n", err)
		} else {
			fmt.Println("OK")
		}
		screenshotDone <- true
	}()

	// 5. 设置视频录制定时器（如果启用）
	if enableVideoRecording {
		// 计算录制时长
		recordingDuration := duration - recordingStartDelay - recordingEndBeforeEnd
		if recordingDuration > 0 {
			fmt.Printf("    录制配置: 开始后%.1f秒启动, 结束前%.1f秒停止, 录制时长约%.1f秒\n",
				recordingStartDelay, recordingEndBeforeEnd, recordingDuration)

			go func() {
				// 等待开始延迟，同时监听停止信号
				select {
				case <-time.After(time.Duration(recordingStartDelay * float64(time.Second))):
					// 正常等待完成
				case <-stopRecording:
					// 收到停止信号，直接退出
					videoRecorderDone <- true
					return
				}

				// 启动录制
				fmt.Print("    启动录制...")
				recorder, recErr := adb.StartVideoRecording(mp4File, int(recordingDuration)+5)
				if recErr != nil {
					fmt.Printf("失败: %v\n", recErr)
					videoRecorderDone <- true
					return
				}
				fmt.Println("OK")

				// 等待录制时长后停止，同时监听停止信号
				select {
				case <-time.After(time.Duration(recordingDuration * float64(time.Second))):
					// 正常等待完成
				case <-stopRecording:
					// 收到停止信号，提前停止
				}

				// 停止录制
				fmt.Print("    停止录制...")
				if err := recorder.Stop(); err != nil {
					fmt.Printf("失败: %v\n", err)
				} else {
					fmt.Println("OK")
				}
				videoRecorderDone <- true
			}()
		} else {
			fmt.Println("    音频太短，跳过视频录制")
			videoRecorderDone <- true
		}
	} else {
		fmt.Println("    视频录制已禁用")
		videoRecorderDone <- true
	}

	// 6. 播放音频
	fmt.Print("    播放中...")
	if err := player.Play(wavFile); err != nil {
		// 通知录制 goroutine 停止
		select {
		case stopRecording <- true:
		default:
		}
		// 等待录制 goroutine 完成
		<-videoRecorderDone
		logRecorder.Stop()
		return fmt.Errorf("播放失败: %w", err)
	}
	fmt.Println("完成")

	// 7. 等待截图完成
	<-screenshotDone

	// 8. 等待视频录制完成（如果启用）
	<-videoRecorderDone

	// 9. 等待日志写入完成（确保抓取完整）
	fmt.Print("    等待日志...")
	time.Sleep(2 * time.Second)
	fmt.Println("OK")

	// 10. 停止 logcat 记录
	fmt.Print("    停止logcat...")
	logRecorder.Stop()
	fmt.Println("OK")

	return nil
}

func processBatch(engine tts.Engine, inputFile, outputDir string, simple bool) {
	// 读取文件
	f, err := os.Open(inputFile)
	if err != nil {
		log.Fatalf("无法打开文件: %v", err)
	}
	defer f.Close()

	// 创建输出目录
	if outputDir == "" || outputDir == "output.wav" {
		outputDir = "output"
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		log.Fatalf("创建输出目录失败: %v", err)
	}

	scanner := bufio.NewScanner(f)
	lineNum := 0
	successCount := 0
	failCount := 0
	isFirstLine := true

	fmt.Printf("开始批量处理: %s\n", inputFile)
	if !simple {
		fmt.Printf("模式: 完整模式 (%s)\n", buildModeDescription())
	}
	fmt.Println("---")

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// 处理 UTF-8 BOM (Windows 记事本可能添加)
		if isFirstLine {
			line = strings.TrimPrefix(line, "\xef\xbb\xbf")
			isFirstLine = false
		}
		line = strings.TrimSpace(line)

		// 跳过空行
		if line == "" {
			continue
		}

		// 文件名: 序号 + 文本内容(最多N字)
		safeName := sanitizeFileName(truncate(line, cfg.FileNameMaxLength))
		outputFile := filepath.Join(outputDir, fmt.Sprintf("%04d%s.wav", lineNum, safeName))

		fmt.Printf("[%d] %s ... ", lineNum, truncate(line, 30))

		var genErr error
		if simple {
			genErr = engine.Synthesize(line, outputFile)
		} else {
			genErr = generateFullAudio(engine, line, outputFile)
		}

		if genErr != nil {
			fmt.Printf("失败: %v\n", genErr)
			failCount++
			continue
		}

		fmt.Printf("-> %s\n", outputFile)
		successCount++
	}

	if err := scanner.Err(); err != nil {
		log.Fatalf("读取文件出错: %v", err)
	}

	fmt.Println("---")
	fmt.Printf("完成! 成功: %d, 失败: %d, 输出目录: %s\n", successCount, failCount, outputDir)
}

// generateFullAudio 根据模板生成完整音频
func generateFullAudio(engine tts.Engine, text, outputPath string) error {
	// 创建临时目录
	tmpDir, err := os.MkdirTemp("", "tts-")
	if err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// 获取模板
	template := cfg.GetTemplate()

	// 用于存储生成的语音文件
	voiceFiles := make(map[string]string)
	voiceIndex := 0

	// 第一遍：生成所有语音文件
	for _, seg := range template {
		if seg.Type == "voice" {
			voiceText := seg.Text
			if voiceText == "$MAIN" {
				voiceText = text
				fmt.Print("(主体)")
			} else {
				fmt.Printf("(%s)", truncate(voiceText, 6))
			}

			voiceFile := filepath.Join(tmpDir, fmt.Sprintf("voice_%d.wav", voiceIndex))
			if err := engine.Synthesize(voiceText, voiceFile); err != nil {
				return fmt.Errorf("生成语音失败 [%s]: %w", truncate(voiceText, 10), err)
			}
			voiceFiles[seg.Text] = voiceFile
			voiceIndex++
		}
	}

	// 第二遍：构建音频片段序列
	fmt.Print("(合并)")
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

	if err := audio.ConcatWAVFiles(segments, outputPath); err != nil {
		return fmt.Errorf("合并音频失败: %w", err)
	}

	return nil
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
	// 常见中英文标点符号
	punctuations := `，。！？、；：""''（）【】《》·…—～,.!?;:"'()[]<>/-\|*@#$%^&+=` + "`~"
	return strings.ContainsRune(punctuations, r)
}

// buildModeDescription 构建模式描述字符串
func buildModeDescription() string {
	var parts []string

	template := cfg.GetTemplate()
	for _, seg := range template {
		switch seg.Type {
		case "silence":
			if seg.Seconds > 0 {
				parts = append(parts, fmt.Sprintf("静音%.0f秒", seg.Seconds))
			}
		case "voice":
			if seg.Text == "$MAIN" {
				parts = append(parts, "文本")
			} else {
				parts = append(parts, seg.Text)
			}
		}
	}

	return strings.Join(parts, " + ")
}
