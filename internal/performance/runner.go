package performance

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"voice-qa/internal/adb"
)

const (
	defaultCommandTimeout = 60 * time.Second
	cleanupTimeout        = 3 * time.Minute
)

var forbiddenCommands = []string{
	"am force-stop",
	"pm clear",
	"am start",
	"monkey",
}

type progressHandler func(Progress)

// Run executes a foreground-only performance test. It never starts, stops, or clears the app.
func Run(ctx context.Context, opts Options, onProgress func(Progress)) Result {
	result := Result{}
	normalized, err := normalizeOptions(opts)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	opts = normalized
	progress := progressHandler(onProgress)

	if err := os.MkdirAll(opts.OutputRoot, 0o755); err != nil {
		result.Message = fmt.Sprintf("创建综合性能测试目录失败: %v", err)
		return result
	}
	resultDir := filepath.Join(opts.OutputRoot, "performance-"+time.Now().Format("20060102-150405"))
	if _, err := os.Stat(resultDir); err == nil {
		resultDir += "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	if err := os.Mkdir(resultDir, 0o755); err != nil {
		result.Message = fmt.Sprintf("创建本次结果目录失败: %v", err)
		return result
	}

	protocol := Protocol{
		SchemaVersion:           1,
		CreatedAt:               nowISO(),
		Package:                 opts.Package,
		Devices:                 append([]string(nil), opts.Devices...),
		Rounds:                  opts.Rounds,
		Scenarios:               append([]Scenario(nil), opts.Scenarios...),
		SettleMilliseconds:      opts.SettleMilliseconds,
		StartupCommandsExecuted: false,
		ForbiddenCommands:       append([]string(nil), forbiddenCommands...),
		Thresholds:              opts.Thresholds,
	}
	result.ResultDir = resultDir
	result.Protocol = protocol
	result.Startup = summarizeStartup(opts.StartupSamples)
	if err := writeJSON(filepath.Join(resultDir, "protocol.json"), protocol); err != nil {
		result.Message = fmt.Sprintf("写入测试协议失败: %v", err)
		return result
	}

	totalSamplesPerDevice := opts.Rounds * len(opts.Scenarios)
	for deviceIndex, serial := range opts.Devices {
		if err := ctx.Err(); err != nil {
			result.Stopped = true
			result.Errors = append(result.Errors, RunError{Serial: serial, Type: "Canceled", Message: "用户停止测试"})
			break
		}
		progress.emit(Progress{
			Stage:          "preflight",
			Message:        fmt.Sprintf("%s：检查应用进程和前台状态", serial),
			Device:         serial,
			DeviceIndex:    deviceIndex + 1,
			DeviceCount:    len(opts.Devices),
			SamplesTotal:   totalSamplesPerDevice,
			OverallPercent: overallPercent(deviceIndex, len(opts.Devices), 0, totalSamplesPerDevice),
		})

		deviceResult, runErr := runDevice(ctx, opts, serial, deviceIndex, progress, resultDir)
		if deviceResult.Serial != "" {
			result.Results = append(result.Results, deviceResult)
		}
		if runErr != nil {
			errorType := "RunError"
			if errors.Is(runErr, context.Canceled) {
				errorType = "Canceled"
				result.Stopped = true
			}
			result.Errors = append(result.Errors, RunError{Serial: serial, Type: errorType, Message: runErr.Error()})
			if result.Stopped {
				break
			}
		}
	}

	result.OverallPassed = len(result.Errors) == 0 && evaluateOverall(result.Results, opts.Thresholds)
	result.Success = len(result.Errors) == 0 && len(result.Results) == len(opts.Devices)
	if result.Stopped {
		result.Message = "综合性能测试已停止，已保存当前采集结果"
	} else if result.Success {
		if result.OverallPassed {
			result.Message = "综合性能测试完成，当前建议阈值下判定通过"
		} else {
			result.Message = "综合性能测试完成，存在未通过或需关注的指标"
		}
	} else {
		result.Message = fmt.Sprintf("综合性能测试完成，%d/%d 台设备成功", len(result.Results), len(opts.Devices))
	}

	reportPath := filepath.Join(resultDir, "performance_report.md")
	if err := os.WriteFile(reportPath, []byte(buildMarkdownReport(result, opts)), 0o644); err != nil {
		result.Errors = append(result.Errors, RunError{Type: "ReportError", Message: err.Error()})
		result.Success = false
		result.Message += fmt.Sprintf("；写入 Markdown 报告失败: %v", err)
	} else {
		result.ReportFile = reportPath
	}
	combinedPath := filepath.Join(resultDir, "combined-results.json")
	if err := writeJSON(combinedPath, result); err != nil {
		result.Errors = append(result.Errors, RunError{Type: "ArtifactError", Message: fmt.Sprintf("写入 combined-results.json 失败: %v", err)})
		result.Success = false
		result.Message += fmt.Sprintf("；写入结构化汇总失败: %v", err)
	}

	progress.emit(Progress{
		Stage:          "done",
		Message:        result.Message,
		DeviceIndex:    len(result.Results),
		DeviceCount:    len(opts.Devices),
		SamplesTotal:   totalSamplesPerDevice,
		OverallPercent: 100,
	})
	return result
}

func normalizeOptions(opts Options) (Options, error) {
	opts.Package = strings.TrimSpace(opts.Package)
	if !packageNamePattern.MatchString(opts.Package) {
		return opts, fmt.Errorf("请输入有效的应用包名，例如 com.example.app")
	}
	if len(opts.Devices) == 0 {
		return opts, fmt.Errorf("请至少选择一台设备")
	}
	if len(opts.Devices) > 8 {
		return opts, fmt.Errorf("单次最多测试 8 台设备")
	}
	seenDevices := make(map[string]bool)
	devices := make([]string, 0, len(opts.Devices))
	for _, serial := range opts.Devices {
		serial = strings.TrimSpace(serial)
		if serial == "" || seenDevices[serial] {
			continue
		}
		seenDevices[serial] = true
		devices = append(devices, serial)
	}
	if len(devices) == 0 {
		return opts, fmt.Errorf("请至少选择一台有效设备")
	}
	opts.Devices = devices
	if opts.Rounds < 1 || opts.Rounds > 20 {
		return opts, fmt.Errorf("测试轮数必须在 1-20 之间")
	}
	if opts.SettleMilliseconds < 200 || opts.SettleMilliseconds > 30000 {
		return opts, fmt.Errorf("场景等待时间必须在 200-30000 ms 之间")
	}
	if len(opts.Scenarios) == 0 || len(opts.Scenarios) > 50 {
		return opts, fmt.Errorf("交互场景数量必须在 1-50 之间")
	}
	for index := range opts.Scenarios {
		scenario := &opts.Scenarios[index]
		scenario.Name = strings.TrimSpace(scenario.Name)
		if scenario.Name == "" {
			return opts, fmt.Errorf("第 %d 个场景缺少名称", index+1)
		}
		passive := scenario.X == -1 && scenario.Y == -1
		if !passive && (scenario.X < 0 || scenario.Y < 0 || scenario.X > 10000 || scenario.Y > 10000) {
			return opts, fmt.Errorf("场景 %q 坐标无效；仅采样当前页面请使用 -1,-1", scenario.Name)
		}
	}
	if strings.TrimSpace(opts.OutputRoot) == "" {
		return opts, fmt.Errorf("综合性能测试输出目录为空")
	}
	defaults := DefaultThresholds()
	if opts.Thresholds.CPUAveragePercent <= 0 {
		opts.Thresholds.CPUAveragePercent = defaults.CPUAveragePercent
	}
	if opts.Thresholds.CPUPeakPercent <= 0 {
		opts.Thresholds.CPUPeakPercent = defaults.CPUPeakPercent
	}
	if opts.Thresholds.PSSAverageMB <= 0 {
		opts.Thresholds.PSSAverageMB = defaults.PSSAverageMB
	}
	if opts.Thresholds.JankyFramesPercent <= 0 {
		opts.Thresholds.JankyFramesPercent = defaults.JankyFramesPercent
	}
	if opts.Thresholds.FrameP90MS <= 0 {
		opts.Thresholds.FrameP90MS = defaults.FrameP90MS
	}
	return opts, nil
}

func runDevice(ctx context.Context, opts Options, serial string, deviceIndex int, progress progressHandler, resultRoot string) (DeviceResult, error) {
	slug := deviceSlug(serial)
	resultDir := filepath.Join(resultRoot, "device-"+slug)
	if err := os.Mkdir(resultDir, 0o755); err != nil {
		return DeviceResult{}, fmt.Errorf("创建设备结果目录失败: %w", err)
	}

	initialPID, initialFocus, err := assertForeground(ctx, serial, opts.Package)
	if err != nil {
		return DeviceResult{}, err
	}
	startedAt := nowISO()
	metadata := collectMetadata(ctx, serial, opts.Package, resultDir)
	_ = os.WriteFile(filepath.Join(resultDir, "focus-before.txt"), []byte(initialFocus), 0o644)
	screenBefore := collectScreenshot(ctx, serial, filepath.Join(resultDir, "screen-before.png"))
	uiBefore := collectUI(ctx, serial, opts.Package, "before", filepath.Join(resultDir, "ui-before.xml"))
	memBeforeRaw, _ := commandToFile(ctx, serial, filepath.Join(resultDir, "meminfo-before.txt"), "shell", "dumpsys", "meminfo", opts.Package)
	memoryBefore := parseMemorySnapshot(memBeforeRaw)
	networkBefore := readNetwork(ctx, serial, metadata.Package.UserID)

	clearOutput, clearErr := runADBText(ctx, serial, defaultCommandTimeout, "logcat", "-b", "all", "-c")
	if clearErr != nil {
		clearOutput, _ = runADBText(ctx, serial, defaultCommandTimeout, "logcat", "-c")
	}
	_ = os.WriteFile(filepath.Join(resultDir, "logcat-clear.txt"), []byte(clearOutput), 0o644)
	gfxReset, _ := runADBText(ctx, serial, defaultCommandTimeout, "shell", "dumpsys", "gfxinfo", opts.Package, "reset")
	_ = os.WriteFile(filepath.Join(resultDir, "gfxinfo-reset.txt"), []byte(gfxReset), 0o644)

	logPath := filepath.Join(resultDir, "logcat-live.txt")
	stream, streamErr := startLogcatStream(ctx, serial, logPath)
	if streamErr != nil {
		return DeviceResult{}, fmt.Errorf("启动连续 logcat 失败: %w", streamErr)
	}
	testStart := time.Now()
	samples := make([]Sample, 0, opts.Rounds*len(opts.Scenarios))
	var runErr error

	for round := 1; round <= opts.Rounds && runErr == nil; round++ {
		for scenarioIndex, scenario := range opts.Scenarios {
			if err := ctx.Err(); err != nil {
				runErr = err
				break
			}
			sampleNumber := len(samples) + 1
			pid, pidErr := getPID(ctx, serial, opts.Package)
			if pidErr != nil {
				runErr = fmt.Errorf("第 %d 个样本读取 PID 失败: %w", sampleNumber, pidErr)
				break
			}
			if pid != initialPID {
				runErr = fmt.Errorf("第 %d 个样本检测到应用进程变化: 初始 PID %d，当前 PID %d", sampleNumber, initialPID, pid)
				break
			}
			ticksBefore, ticksBeforeOK := readProcTicks(ctx, serial, pid)
			intervalStart := time.Now()
			focusBefore, focusErr := getFocus(ctx, serial)
			if focusErr != nil || !focusBelongsToPackage(focusBefore, opts.Package) {
				runErr = fmt.Errorf("场景 %q 执行前目标应用已离开前台或无法确认前台，已停止点击: %s %v", scenario.Name, focusBefore, focusErr)
				break
			}

			if scenario.X >= 0 && scenario.Y >= 0 {
				if _, err := runADBText(ctx, serial, defaultCommandTimeout, "shell", "input", "tap", strconv.Itoa(scenario.X), strconv.Itoa(scenario.Y)); err != nil {
					runErr = fmt.Errorf("场景 %q 点击失败: %w", scenario.Name, err)
					break
				}
			}
			if err := waitContext(ctx, time.Duration(opts.SettleMilliseconds)*time.Millisecond); err != nil {
				runErr = err
				break
			}
			ticksAfter, ticksAfterOK := readProcTicks(ctx, serial, pid)
			intervalSeconds := time.Since(intervalStart).Seconds()
			intervalCPU := 0.0
			intervalAvailable := ticksBeforeOK && ticksAfterOK && intervalSeconds > 0 && ticksAfter >= ticksBefore
			if intervalAvailable {
				// Android kernels normally expose 100 process clock ticks per second.
				intervalCPU = roundTo(float64(ticksAfter-ticksBefore)/intervalSeconds, 3)
			}

			topRaw, _ := runADBText(ctx, serial, defaultCommandTimeout, "shell", "top", "-b", "-n", "1", "-m", "80")
			topCPU, topMemory, topLine, topOK := parseTop(topRaw, pid, opts.Package)
			memRaw, _ := runADBText(ctx, serial, defaultCommandTimeout, "shell", "dumpsys", "meminfo", opts.Package)
			memory := parseMemorySnapshot(memRaw)
			statusRaw, _ := runADBText(ctx, serial, defaultCommandTimeout, "shell", "cat", fmt.Sprintf("/proc/%d/status", pid))
			status := parseProcessStatus(statusRaw)
			focus, _ := getFocus(ctx, serial)
			currentPID, currentPIDErr := getPID(ctx, serial, opts.Package)
			alive := currentPIDErr == nil && currentPID == initialPID

			sample := Sample{
				Number:             sampleNumber,
				Round:              round,
				ScenarioIndex:      scenarioIndex + 1,
				ScenarioName:       scenario.Name,
				X:                  scenario.X,
				Y:                  scenario.Y,
				Timestamp:          nowISO(),
				ElapsedSeconds:     roundTo(time.Since(testStart).Seconds(), 3),
				PID:                pid,
				TopCPUPercent:      topCPU,
				TopCPUAvailable:    topOK,
				IntervalCPUPercent: intervalCPU,
				IntervalAvailable:  intervalAvailable,
				TopMemoryPercent:   topMemory,
				PSSKB:              memory.PSSKB,
				JavaHeapKB:         memory.JavaHeapKB,
				NativeHeapKB:       memory.NativeHeapKB,
				WebViews:           memory.WebViews,
				Threads:            status.Threads,
				VMRSSKB:            status.VMRSSKB,
				ProcessAlive:       alive,
				Foreground:         focusBelongsToPackage(focus, opts.Package),
				TopLine:            topLine,
			}
			samples = append(samples, sample)
			if err := writeSamplesCSV(filepath.Join(resultDir, "samples.csv"), samples); err != nil {
				runErr = fmt.Errorf("写入第 %d 个采样结果失败: %w", sampleNumber, err)
				break
			}

			progress.emit(Progress{
				Stage:          "sampling",
				Message:        fmt.Sprintf("%s：%d/%d %s，CPU %s，PSS %s", serial, sampleNumber, opts.Rounds*len(opts.Scenarios), scenario.Name, formatOptional(topCPU, topOK, "%"), formatOptional(float64(memory.PSSKB)/1024, memory.Available, " MB")),
				Device:         serial,
				DeviceIndex:    deviceIndex + 1,
				DeviceCount:    len(opts.Devices),
				Sample:         sampleNumber,
				SamplesTotal:   opts.Rounds * len(opts.Scenarios),
				OverallPercent: overallPercent(deviceIndex, len(opts.Devices), sampleNumber, opts.Rounds*len(opts.Scenarios)),
			})
			if !sample.ProcessAlive || !sample.Foreground {
				runErr = fmt.Errorf("场景 %q 后应用进程或前台状态变化，已停止后续点击并保存当前样本", scenario.Name)
				break
			}
		}
	}
	if err := writeSamplesCSV(filepath.Join(resultDir, "samples.csv"), samples); err != nil && runErr == nil {
		runErr = fmt.Errorf("写入采样结果失败: %w", err)
	}

	elapsedSeconds := time.Since(testStart).Seconds()
	_ = stream.stop()
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancelCleanup()

	progress.emit(Progress{
		Stage:          "finalizing",
		Message:        fmt.Sprintf("%s：整理帧、日志、网络和前后证据", serial),
		Device:         serial,
		DeviceIndex:    deviceIndex + 1,
		DeviceCount:    len(opts.Devices),
		Sample:         len(samples),
		SamplesTotal:   opts.Rounds * len(opts.Scenarios),
		OverallPercent: overallPercent(deviceIndex, len(opts.Devices), len(samples), opts.Rounds*len(opts.Scenarios)),
	})

	memFinalRaw, _ := commandToFile(cleanupCtx, serial, filepath.Join(resultDir, "meminfo-final.txt"), "shell", "dumpsys", "meminfo", opts.Package)
	memoryFinal := parseMemorySnapshot(memFinalRaw)
	gfxRaw, _ := commandToFile(cleanupCtx, serial, filepath.Join(resultDir, "gfxinfo-final.txt"), "shell", "dumpsys", "gfxinfo", opts.Package)
	gfxInfo := parseGfxInfo(gfxRaw)
	logFinal, _ := commandToFile(cleanupCtx, serial, filepath.Join(resultDir, "logcat-final-buffer.txt"), "logcat", "-b", "all", "-d", "-v", "threadtime")
	logLive, _ := os.ReadFile(logPath)
	logAnalysis := analyzeLogcat(string(logLive)+"\n"+logFinal, opts.Package, elapsedSeconds)
	networkAfter := readNetwork(cleanupCtx, serial, metadata.Package.UserID)
	networkDelta := buildNetworkDelta(networkBefore, networkAfter)
	finalPID, finalPIDErr := getPID(cleanupCtx, serial, opts.Package)
	if finalPIDErr != nil {
		finalPID = 0
	}
	finalFocus, _ := getFocus(cleanupCtx, serial)
	_ = os.WriteFile(filepath.Join(resultDir, "focus-after.txt"), []byte(finalFocus), 0o644)
	screenAfter := collectScreenshot(cleanupCtx, serial, filepath.Join(resultDir, "screen-after.png"))
	uiAfter := collectUI(cleanupCtx, serial, opts.Package, "after", filepath.Join(resultDir, "ui-after.xml"))

	deviceResult := DeviceResult{
		Serial:            serial,
		Slug:              slug,
		StartedAt:         startedAt,
		FinishedAt:        nowISO(),
		ElapsedSeconds:    roundTo(elapsedSeconds, 3),
		Rounds:            opts.Rounds,
		ScenariosPerRound: len(opts.Scenarios),
		InitialPID:        initialPID,
		FinalPID:          finalPID,
		Metadata:          metadata,
		ScreenBefore:      screenBefore,
		ScreenAfter:       screenAfter,
		UIBefore:          uiBefore,
		UIAfter:           uiAfter,
		MemoryBefore:      memoryBefore,
		MemoryFinal:       memoryFinal,
		Samples:           summarizeSamples(samples, opts.Scenarios),
		GfxInfo:           gfxInfo,
		Logcat:            logAnalysis,
		NetworkBefore:     networkBefore,
		NetworkAfter:      networkAfter,
		NetworkDelta:      networkDelta,
		ResultDir:         resultDir,
	}
	if err := writeJSON(filepath.Join(resultDir, "summary.json"), deviceResult); err != nil && runErr == nil {
		runErr = fmt.Errorf("写入设备汇总失败: %w", err)
	}
	if runErr == nil && finalPID != initialPID {
		runErr = fmt.Errorf("测试前后 PID 发生变化: %d -> %d", initialPID, finalPID)
	}
	return deviceResult, runErr
}

func assertForeground(ctx context.Context, serial, packageName string) (int, string, error) {
	state, err := runADBText(ctx, serial, 30*time.Second, "get-state")
	if err != nil || strings.TrimSpace(state) != "device" {
		return 0, "", fmt.Errorf("设备 %s 不可用: %s %v", serial, strings.TrimSpace(state), err)
	}
	pid, err := getPID(ctx, serial, packageName)
	if err != nil {
		return 0, "", fmt.Errorf("%s 未运行；综合性能测试不会自动启动应用", packageName)
	}
	focus, err := getFocus(ctx, serial)
	if err != nil {
		return 0, "", fmt.Errorf("读取前台页面失败: %w", err)
	}
	if !focusBelongsToPackage(focus, packageName) {
		return 0, focus, fmt.Errorf("%s 不在设备 %s 前台；请先把应用打开到可操作页面", packageName, serial)
	}
	return pid, focus, nil
}

func collectMetadata(ctx context.Context, serial, packageName, resultDir string) DeviceMetadata {
	getprop, _ := commandToFile(ctx, serial, filepath.Join(resultDir, "getprop.txt"), "shell", "getprop")
	packageRaw, _ := commandToFile(ctx, serial, filepath.Join(resultDir, "package.txt"), "shell", "dumpsys", "package", packageName)
	wmSize, _ := commandToFile(ctx, serial, filepath.Join(resultDir, "wm-size.txt"), "shell", "wm", "size")
	wmDensity, _ := commandToFile(ctx, serial, filepath.Join(resultDir, "wm-density.txt"), "shell", "wm", "density")
	_, _ = commandToFile(ctx, serial, filepath.Join(resultDir, "proc-meminfo.txt"), "shell", "cat", "/proc/meminfo")
	_, _ = commandToFile(ctx, serial, filepath.Join(resultDir, "battery.txt"), "shell", "dumpsys", "battery")
	_, _ = commandToFile(ctx, serial, filepath.Join(resultDir, "thermalservice.txt"), "shell", "dumpsys", "thermalservice")
	apkPathRaw, _ := commandToFile(ctx, serial, filepath.Join(resultDir, "apk-path.txt"), "shell", "pm", "path", packageName)
	apkPath := firstMatch(apkPathRaw, `(?m)^package:(\S+)`)
	apkSHA := ""
	if apkPath != "" {
		shaRaw, _ := commandToFile(ctx, serial, filepath.Join(resultDir, "apk-sha256.txt"), "shell", "sha256sum", apkPath)
		apkSHA = strings.ToLower(firstMatch(shaRaw, `(?i)\b([0-9a-f]{64})\b`))
	}
	props := parseGetProp(getprop)
	return DeviceMetadata{
		Serial:           serial,
		Manufacturer:     props["ro.product.manufacturer"],
		Model:            props["ro.product.model"],
		Android:          props["ro.build.version.release"],
		SDK:              props["ro.build.version.sdk"],
		BuildFingerprint: props["ro.build.fingerprint"],
		PhysicalSize:     compactLines(wmSize),
		Density:          compactLines(wmDensity),
		Package:          parsePackageMetadata(packageRaw),
		APKPath:          apkPath,
		APKSHA256:        apkSHA,
	}
}

func getPID(ctx context.Context, serial, packageName string) (int, error) {
	output, err := runADBText(ctx, serial, 30*time.Second, "shell", "pidof", packageName)
	if err != nil {
		return 0, err
	}
	match := regexp.MustCompile(`\b(\d+)\b`).FindStringSubmatch(output)
	if len(match) < 2 {
		return 0, fmt.Errorf("未读取到应用 PID")
	}
	return strconv.Atoi(match[1])
}

func getFocus(ctx context.Context, serial string) (string, error) {
	output, err := runADBText(ctx, serial, defaultCommandTimeout, "shell", "dumpsys", "window", "windows")
	if err != nil || strings.TrimSpace(output) == "" {
		output, err = runADBText(ctx, serial, defaultCommandTimeout, "shell", "dumpsys", "window")
	}
	if err != nil {
		return output, err
	}
	for _, marker := range []string{"mCurrentFocus=", "topResumedActivity=", "mFocusedApp="} {
		for _, line := range strings.Split(output, "\n") {
			if strings.Contains(line, marker) {
				return strings.TrimSpace(line), nil
			}
		}
	}
	return "", fmt.Errorf("未读取到明确的前台窗口")
}

var focusedComponentPattern = regexp.MustCompile(`(?:^|[\s{])([A-Za-z_][A-Za-z0-9_.]*)/[A-Za-z0-9_.$]+`)

func focusBelongsToPackage(focus, packageName string) bool {
	match := focusedComponentPattern.FindStringSubmatch(focus)
	return len(match) == 2 && match[1] == packageName
}

func readProcTicks(ctx context.Context, serial string, pid int) (int64, bool) {
	output, err := runADBText(ctx, serial, 30*time.Second, "shell", "cat", fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	return parseProcTicks(output)
}

func readNetwork(ctx context.Context, serial string, uid int) NetworkSnapshot {
	if uid <= 0 {
		return NetworkSnapshot{Reason: "应用 UID 不可用"}
	}
	output, err := runADBText(ctx, serial, defaultCommandTimeout, "shell", "cat", "/proc/net/xt_qtaguid/stats")
	if err != nil {
		return NetworkSnapshot{UID: uid, Reason: strings.TrimSpace(output)}
	}
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "idx iface") {
		return NetworkSnapshot{UID: uid, Reason: "设备未提供 xt_qtaguid 统计"}
	}
	header := strings.Fields(lines[0])
	uidIndex := indexOf(header, "uid_tag_int")
	rxIndex := indexOf(header, "rx_bytes")
	txIndex := indexOf(header, "tx_bytes")
	if uidIndex < 0 || rxIndex < 0 || txIndex < 0 {
		return NetworkSnapshot{UID: uid, Reason: "xt_qtaguid 字段格式不受支持"}
	}
	result := NetworkSnapshot{Available: true, UID: uid}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if uidIndex >= len(fields) || rxIndex >= len(fields) || txIndex >= len(fields) || parseInt(fields[uidIndex]) != uid {
			continue
		}
		result.RXBytes += parseInt64(fields[rxIndex])
		result.TXBytes += parseInt64(fields[txIndex])
		result.Rows++
	}
	return result
}

func buildNetworkDelta(before, after NetworkSnapshot) NetworkDelta {
	if !before.Available || !after.Available {
		reason := before.Reason
		if reason == "" {
			reason = after.Reason
		}
		return NetworkDelta{Reason: reason}
	}
	rx := after.RXBytes - before.RXBytes
	tx := after.TXBytes - before.TXBytes
	return NetworkDelta{Available: true, RXBytes: rx, TXBytes: tx, TotalBytes: rx + tx}
}

func collectScreenshot(ctx context.Context, serial, path string) ScreenEvidence {
	data, _ := runADBBytes(ctx, serial, defaultCommandTimeout, "exec-out", "screencap", "-p")
	data = normalizePNG(data)
	_ = os.WriteFile(path, data, 0o644)
	return pngEvidence(data)
}

func collectUI(ctx context.Context, serial, packageName, suffix, path string) UIEvidence {
	remotePath := "/sdcard/voice-qa-performance-" + suffix + ".xml"
	_, _ = runADBText(ctx, serial, defaultCommandTimeout, "shell", "uiautomator", "dump", remotePath)
	data, _ := runADBBytes(ctx, serial, defaultCommandTimeout, "exec-out", "cat", remotePath)
	_, _ = runADBText(ctx, serial, 30*time.Second, "shell", "rm", "-f", remotePath)
	_ = os.WriteFile(path, data, 0o644)
	return UIEvidence{Bytes: int64(len(data)), ContainsPackage: strings.Contains(string(data), packageName)}
}

func commandToFile(ctx context.Context, serial, path string, args ...string) (string, error) {
	output, err := runADBText(ctx, serial, defaultCommandTimeout, args...)
	if writeErr := os.WriteFile(path, []byte(output), 0o644); writeErr != nil {
		return output, fmt.Errorf("写入 %s 失败: %w", filepath.Base(path), writeErr)
	}
	return output, err
}

func runADBText(ctx context.Context, serial string, timeout time.Duration, args ...string) (string, error) {
	data, err := runADB(ctx, serial, timeout, args...)
	return string(data), err
}

func runADBBytes(ctx context.Context, serial string, timeout time.Duration, args ...string) ([]byte, error) {
	return runADB(ctx, serial, timeout, args...)
}

func runADB(ctx context.Context, serial string, timeout time.Duration, args ...string) ([]byte, error) {
	joined := strings.ToLower(strings.Join(args, " "))
	for _, fragment := range forbiddenCommands {
		if strings.Contains(joined, fragment) {
			return nil, fmt.Errorf("综合性能测试禁止执行启动状态变更命令: %s", fragment)
		}
	}
	commandArgs := append([]string{"-s", serial}, args...)
	var lastOutput []byte
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		commandCtx, cancel := context.WithTimeout(ctx, timeout)
		cmd := adb.CommandContext(commandCtx, commandArgs...)
		adb.HideWindow(cmd)
		output, err := cmd.CombinedOutput()
		cancel()
		lastOutput, lastErr = output, err
		if err == nil && !hasTransportError(string(output)) {
			return output, nil
		}
		if ctx.Err() != nil {
			return output, ctx.Err()
		}
		if attempt == 0 && strings.Contains(serial, ":") && hasTransportError(string(output)) {
			reconnectCtx, reconnectCancel := context.WithTimeout(ctx, 20*time.Second)
			reconnectCmd := adb.CommandContext(reconnectCtx, "connect", serial)
			adb.HideWindow(reconnectCmd)
			_, _ = reconnectCmd.CombinedOutput()
			reconnectCancel()
			_ = waitContext(ctx, time.Second)
		}
	}
	message := strings.TrimSpace(string(lastOutput))
	if message == "" {
		message = fmt.Sprint(lastErr)
	}
	return lastOutput, fmt.Errorf("adb %s 失败: %s", strings.Join(commandArgs, " "), message)
}

func hasTransportError(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "device offline") ||
		strings.Contains(lower, "device not found") ||
		strings.Contains(lower, "error: closed") ||
		strings.Contains(lower, "no devices/emulators found")
}

type logcatStream struct {
	cancel context.CancelFunc
	done   chan error
	file   *os.File
}

func startLogcatStream(ctx context.Context, serial, path string) (*logcatStream, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	cmd := adb.CommandContext(streamCtx, "-s", serial, "logcat", "-b", "all", "-v", "threadtime")
	adb.HideWindow(cmd)
	cmd.Stdout = file
	cmd.Stderr = file
	if err := cmd.Start(); err != nil {
		cancel()
		_ = file.Close()
		return nil, err
	}
	stream := &logcatStream{cancel: cancel, done: make(chan error, 1), file: file}
	go func() {
		stream.done <- cmd.Wait()
	}()
	return stream, nil
}

func (s *logcatStream) stop() error {
	s.cancel()
	var err error
	select {
	case err = <-s.done:
	case <-time.After(5 * time.Second):
		err = fmt.Errorf("停止 logcat 超时")
	}
	_ = s.file.Sync()
	closeErr := s.file.Close()
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "killed") && !strings.Contains(strings.ToLower(err.Error()), "canceled") {
		return err
	}
	return closeErr
}

func writeSamplesCSV(path string, samples []Sample) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	_, _ = file.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(file)
	defer writer.Flush()
	header := []string{
		"sample", "round", "scenarioIndex", "scenarioName", "x", "y", "timestamp", "elapsedSeconds", "pid",
		"topCpuPercent", "intervalCpuPercent", "topMemoryPercent", "pssKb", "javaHeapKb", "nativeHeapKb", "webViews",
		"threads", "vmrssKb", "processAlive", "foreground", "topLine",
	}
	if err := writer.Write(header); err != nil {
		return err
	}
	for _, sample := range samples {
		row := []string{
			strconv.Itoa(sample.Number), strconv.Itoa(sample.Round), strconv.Itoa(sample.ScenarioIndex), sample.ScenarioName,
			strconv.Itoa(sample.X), strconv.Itoa(sample.Y), sample.Timestamp, formatFloat(sample.ElapsedSeconds), strconv.Itoa(sample.PID),
			optionalFloat(sample.TopCPUPercent, sample.TopCPUAvailable), optionalFloat(sample.IntervalCPUPercent, sample.IntervalAvailable),
			optionalFloat(sample.TopMemoryPercent, sample.TopCPUAvailable), strconv.FormatInt(sample.PSSKB, 10),
			strconv.FormatInt(sample.JavaHeapKB, 10), strconv.FormatInt(sample.NativeHeapKB, 10), strconv.FormatInt(sample.WebViews, 10),
			strconv.FormatInt(sample.Threads, 10), strconv.FormatInt(sample.VMRSSKB, 10), strconv.FormatBool(sample.ProcessAlive),
			strconv.FormatBool(sample.Foreground), sample.TopLine,
		}
		if err := writer.Write(row); err != nil {
			return err
		}
	}
	return writer.Error()
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func deviceSlug(serial string) string {
	var builder strings.Builder
	for _, char := range serial {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '-' || char == '_' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('-')
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		result = "device"
	}
	if len(result) > 64 {
		result = result[:64]
	}
	return result
}

func nowISO() string {
	return time.Now().Format(time.RFC3339)
}

func compactLines(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func overallPercent(deviceIndex, deviceCount, sample, samplesTotal int) float64 {
	if deviceCount <= 0 || samplesTotal <= 0 {
		return 0
	}
	completed := float64(deviceIndex*samplesTotal + sample)
	return roundTo(mathMin(100, completed/float64(deviceCount*samplesTotal)*100), 1)
}

func mathMin(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func (handler progressHandler) emit(progress Progress) {
	if handler != nil {
		handler(progress)
	}
}

func formatOptional(value float64, available bool, suffix string) string {
	if !available {
		return "N/A"
	}
	return fmt.Sprintf("%.1f%s", value, suffix)
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 3, 64)
}

func optionalFloat(value float64, available bool) string {
	if !available {
		return ""
	}
	return formatFloat(value)
}
