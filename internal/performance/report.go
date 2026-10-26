package performance

import (
	"fmt"
	"path/filepath"
	"strings"
)

type deviceAssessment struct {
	Passed bool
	Issues []string
}

func evaluateOverall(results []DeviceResult, thresholds Thresholds) bool {
	if len(results) == 0 {
		return false
	}
	for _, result := range results {
		if !assessDevice(result, thresholds).Passed {
			return false
		}
	}
	return true
}

func assessDevice(result DeviceResult, thresholds Thresholds) deviceAssessment {
	assessment := deviceAssessment{Passed: true}
	addIssue := func(format string, args ...any) {
		assessment.Passed = false
		assessment.Issues = append(assessment.Issues, fmt.Sprintf(format, args...))
	}

	if result.Samples.SampleCount == 0 {
		addIssue("未采集到交互样本")
	}
	if expected := result.Rounds * result.ScenariosPerRound; expected > 0 && result.Samples.SampleCount != expected {
		addIssue("测试场景未完整采集：%d/%d", result.Samples.SampleCount, expected)
	}
	if result.Samples.TopCPUPercent.Count == 0 {
		addIssue("未读取到 top CPU 数据")
	} else if result.Samples.TopCPUPercent.Count != result.Samples.SampleCount {
		addIssue("CPU 有效采样不足：%d/%d", result.Samples.TopCPUPercent.Count, result.Samples.SampleCount)
	} else {
		if result.Samples.TopCPUPercent.Mean > thresholds.CPUAveragePercent {
			addIssue("CPU 平均值 %.1f%% 超过建议阈值 %.1f%%", result.Samples.TopCPUPercent.Mean, thresholds.CPUAveragePercent)
		}
		if result.Samples.TopCPUPercent.Max > thresholds.CPUPeakPercent {
			addIssue("CPU 峰值 %.1f%% 超过建议阈值 %.1f%%", result.Samples.TopCPUPercent.Max, thresholds.CPUPeakPercent)
		}
	}
	if result.Samples.PSSKB.Count == 0 {
		addIssue("未读取到 PSS 内存数据")
	} else if result.Samples.PSSKB.Count != result.Samples.SampleCount {
		addIssue("PSS 有效采样不足：%d/%d", result.Samples.PSSKB.Count, result.Samples.SampleCount)
	} else if result.Samples.PSSKB.Mean/1024 > thresholds.PSSAverageMB {
		addIssue("PSS 平均值 %.1f MB 超过建议阈值 %.1f MB", result.Samples.PSSKB.Mean/1024, thresholds.PSSAverageMB)
	}
	if !result.GfxInfo.Available {
		addIssue("未读取到有效的 gfxinfo 帧数据")
	} else {
		if result.GfxInfo.JankyPercent > thresholds.JankyFramesPercent {
			addIssue("Janky frames %.1f%% 超过建议阈值 %.1f%%", result.GfxInfo.JankyPercent, thresholds.JankyFramesPercent)
		}
		if result.GfxInfo.P90MS > thresholds.FrameP90MS {
			addIssue("帧耗时 P90 %.1f ms 超过建议阈值 %.1f ms", result.GfxInfo.P90MS, thresholds.FrameP90MS)
		}
	}
	if result.Logcat.FatalExceptionCount+result.Logcat.AMCrashCount > 0 {
		addIssue("发现崩溃线索 %d 条", result.Logcat.FatalExceptionCount+result.Logcat.AMCrashCount)
	}
	if result.Logcat.ANRCount > 0 {
		addIssue("发现 ANR 线索 %d 条", result.Logcat.ANRCount)
	}
	if result.Logcat.AudioFocusRequestsPerSecond > 1 {
		addIssue("AudioFocus 请求频率 %.2f 次/秒，存在连续重复请求风险", result.Logcat.AudioFocusRequestsPerSecond)
	}
	if result.InitialPID <= 0 || result.FinalPID != result.InitialPID {
		addIssue("测试前后 PID 不一致（%d -> %d）", result.InitialPID, result.FinalPID)
	}
	if result.Samples.ProcessAliveSamples != result.Samples.SampleCount {
		addIssue("仅 %d/%d 个样本确认进程存活", result.Samples.ProcessAliveSamples, result.Samples.SampleCount)
	}
	if result.Samples.ForegroundSamples != result.Samples.SampleCount {
		addIssue("仅 %d/%d 个样本确认应用位于前台", result.Samples.ForegroundSamples, result.Samples.SampleCount)
	}
	return assessment
}

func buildMarkdownReport(result Result, opts Options) string {
	var report strings.Builder
	report.WriteString("# 应用综合性能测试报告\n\n")
	fmt.Fprintf(&report, "- **应用包名**：`%s`\n", markdownInline(result.Protocol.Package))
	fmt.Fprintf(&report, "- **生成时间**：%s\n", markdownInline(result.Protocol.CreatedAt))
	fmt.Fprintf(&report, "- **测试设备数**：%d\n", len(result.Results))
	fmt.Fprintf(&report, "- **测试模型**：前台交互采样，%d 个场景 x %d 轮/设备\n", len(result.Protocol.Scenarios), result.Protocol.Rounds)
	report.WriteString("- **启动状态约束**：测试过程中未执行 `am force-stop`、`pm clear`、`am start` 或 `monkey`，应用须由用户提前打开并保持前台。\n\n")

	report.WriteString("## 1. 总体结论\n\n")
	if result.OverallPassed && len(result.Errors) == 0 {
		report.WriteString("**结论：通过。** 本次采集范围内未发现超过当前建议阈值的关键性能、稳定性或前台连续性问题。\n\n")
	} else {
		report.WriteString("**结论：未通过或需要复核。** 发现超过建议阈值、关键数据缺失或测试连续性异常，不能仅凭本次结果判定性能达标。\n\n")
	}

	issues := collectIssues(result, opts.Thresholds)
	if len(issues) == 0 {
		report.WriteString("本次没有发现需单独列出的异常项。\n\n")
	} else {
		for index, issue := range issues {
			fmt.Fprintf(&report, "%d. %s\n", index+1, issue)
		}
		report.WriteString("\n")
	}

	report.WriteString("### 1.1 核心数据汇总\n\n")
	report.WriteString("| 设备 | 样本 | CPU 平均 / 峰值 | PSS 平均 | Janky frames | 帧耗时 P90 | 崩溃 / ANR | AudioFocus | 判定 |\n")
	report.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---|\n")
	for _, device := range result.Results {
		assessment := assessDevice(device, opts.Thresholds)
		fmt.Fprintf(&report, "| %s | %d | %s / %s | %s | %s | %s | %d / %d | %s | %s |\n",
			markdownTable(deviceDisplayName(device)), device.Samples.SampleCount,
			formatMetric(device.Samples.TopCPUPercent.Mean, device.Samples.TopCPUPercent.Count > 0, "%"),
			formatMetric(device.Samples.TopCPUPercent.Max, device.Samples.TopCPUPercent.Count > 0, "%"),
			formatMetric(device.Samples.PSSKB.Mean/1024, device.Samples.PSSKB.Count > 0, " MB"),
			formatMetric(device.GfxInfo.JankyPercent, device.GfxInfo.Available, "%"),
			formatMetric(device.GfxInfo.P90MS, device.GfxInfo.Available, " ms"),
			device.Logcat.FatalExceptionCount+device.Logcat.AMCrashCount, device.Logcat.ANRCount,
			formatRate(device.Logcat.AudioFocusRequestCount, device.Logcat.AudioFocusRequestsPerSecond),
			passLabel(assessment.Passed))
	}
	if len(result.Results) == 0 {
		report.WriteString("| 无有效设备结果 | 0 | N/A | N/A | N/A | N/A | N/A | N/A | 需复核 |\n")
	}
	report.WriteString("\n")

	report.WriteString("### 1.2 指标口径\n\n")
	report.WriteString("| 指标 | 含义 |\n|---|---|\n")
	report.WriteString("| CPU 平均 / 峰值 | 每个业务场景稳定等待后，通过 `top` 读取目标进程 CPU；Android 多核环境下进程 CPU 可能超过 100%。 |\n")
	report.WriteString("| 区间 CPU | 交互等待窗口内 `/proc/<pid>/stat` 的进程时钟增量，用于交叉验证瞬时 `top` 数据。 |\n")
	report.WriteString("| PSS | `dumpsys meminfo` 的目标进程 Proportional Set Size，适合比较应用实际内存占用。 |\n")
	report.WriteString("| Janky frames | 受控 `gfxinfo reset` 窗口内被系统判定为卡顿的帧占比。 |\n")
	report.WriteString("| 帧耗时 P90 | 90% 帧耗时不超过该值，值越低通常越流畅。 |\n")
	report.WriteString("| AudioFocus | 日志中目标包的音频焦点请求次数与频率；没有统一官方阈值，连续高频请求按风险项处理。 |\n")
	report.WriteString("| 网络增量 | `/proc/net/xt_qtaguid/stats` 中应用 UID 的收发字节差；不支持该接口时显示 N/A。 |\n\n")

	report.WriteString("## 2. 测试范围与数据边界\n\n")
	report.WriteString("- 每台设备顺序执行相同场景，设备之间不并发，减少主机 ADB 调度对结果的相互干扰。\n")
	report.WriteString("- 每个场景可点击一个坐标；坐标为 `-1,-1` 时仅采样当前页面，不产生点击。\n")
	fmt.Fprintf(&report, "- 每次交互后等待 %d ms，再采集 CPU、PSS、线程、RSS、前台状态与 PID。\n", result.Protocol.SettleMilliseconds)
	report.WriteString("- 测试前后保存截图、UI XML、应用/设备元数据和内存快照；测试窗口持续记录 logcat，并单独重置和收集 gfxinfo。\n")
	report.WriteString("- 本报告不包含电量功耗、GPU 频率、Perfetto 系统级 trace 或实验室温控结论；这些指标需要额外测试环境。\n\n")

	report.WriteString("## 3. 测试环境\n\n")
	report.WriteString("| 设备 | Android / SDK | 应用版本 | UID | 分辨率 / 密度 | APK SHA-256 |\n")
	report.WriteString("|---|---|---|---:|---|---|\n")
	for _, device := range result.Results {
		version := strings.TrimSpace(device.Metadata.Package.VersionName + " (" + device.Metadata.Package.VersionCode + ")")
		fmt.Fprintf(&report, "| %s | %s / %s | %s | %d | %s / %s | %s |\n",
			markdownTable(deviceDisplayName(device)), markdownTable(valueOrNA(device.Metadata.Android)), markdownTable(valueOrNA(device.Metadata.SDK)),
			markdownTable(valueOrNA(version)), device.Metadata.Package.UserID,
			markdownTable(valueOrNA(device.Metadata.PhysicalSize)), markdownTable(valueOrNA(device.Metadata.Density)),
			markdownTable(shortHash(device.Metadata.APKSHA256)))
	}
	report.WriteString("\n")
	if sameAPK(result.Results) {
		report.WriteString("所测设备的 APK SHA-256 一致（或仅有一台设备提供有效哈希）。\n\n")
	} else {
		report.WriteString("**注意：所测设备的 APK SHA-256 不一致，跨设备对比可能混入版本差异。**\n\n")
	}

	report.WriteString("## 4. 测试方法\n\n")
	fmt.Fprintf(&report, "1. 用户先在每台设备上打开 `%s` 并停留在可操作前台页面。\n", markdownInline(result.Protocol.Package))
	report.WriteString("2. 工具核对设备状态、PID 和前台窗口，记录测试前证据并清空测试窗口日志、重置 gfxinfo。\n")
	fmt.Fprintf(&report, "3. 按场景顺序执行 %d 轮，共计划 %d 个样本/设备；每个样本保留场景名、坐标、时间和原始 `top` 行。\n", result.Protocol.Rounds, result.Protocol.Rounds*len(result.Protocol.Scenarios))
	report.WriteString("4. 测试结束后再次核对 PID/前台状态，收集最终 meminfo、gfxinfo、logcat、网络增量、截图和 UI XML。\n")
	report.WriteString("5. 依据当前建议阈值生成判定，同时保留 JSON、CSV 与原始证据，便于复核。\n\n")

	writeStartupSection(&report, result.Startup)
	writeCPUSection(&report, result.Results, opts.Thresholds)
	writeMemorySection(&report, result.Results, opts.Thresholds)
	writeRenderingSection(&report, result.Results, opts.Thresholds)
	writeStabilitySection(&report, result.Results)
	writeNetworkSection(&report, result.Results)
	writeRecommendationsSection(&report, issues)
	writeEvidenceSection(&report, result)

	report.WriteString("## 13. 限制与复测建议\n\n")
	report.WriteString("- 坐标交互依赖页面布局稳定；分辨率或 UI 改版后应重新校准场景坐标。\n")
	report.WriteString("- `top`、`gfxinfo` 和网络统计能力因 Android 厂商版本不同而异；N/A 表示缺少数据，不表示指标为零。\n")
	report.WriteString("- 建议在设备温度、网络、后台负载和应用数据状态一致的条件下至少重复 3 次，并比较多次报告趋势。\n")
	report.WriteString("- 若要定位具体卡顿调用栈，应针对问题场景追加 Perfetto 或 Android Studio Profiler 定点采集。\n")
	return report.String()
}

func collectIssues(result Result, thresholds Thresholds) []string {
	issues := make([]string, 0)
	for _, runErr := range result.Errors {
		prefix := "运行"
		if runErr.Serial != "" {
			prefix = deviceDisplaySerial(runErr.Serial)
		}
		issues = append(issues, fmt.Sprintf("%s：%s", prefix, runErr.Message))
	}
	for _, device := range result.Results {
		for _, issue := range assessDevice(device, thresholds).Issues {
			issues = append(issues, fmt.Sprintf("%s：%s", deviceDisplayName(device), issue))
		}
	}
	return issues
}

func writeStartupSection(report *strings.Builder, startup StartupSummary) {
	report.WriteString("## 5. 启动性能（可选关联数据）\n\n")
	if len(startup.Groups) > 0 {
		report.WriteString("关联记录按设备及启动参数分别统计，不计算跨组平均值。\n\n")
		for index, group := range startup.Groups {
			fmt.Fprintf(report, "### 分组 %d：%s\n\n", index+1, markdownInline(group.Label))
			writeStartupMetrics(report, StartupSummary{SampleCount: group.SampleCount, TotalTimeMS: group.TotalTimeMS, ManualTimeMS: group.ManualTimeMS, PrimaryMetric: group.PrimaryMetric})
		}
		return
	}
	writeStartupMetrics(report, startup)
}

func writeStartupMetrics(report *strings.Builder, startup StartupSummary) {
	if startup.TotalTimeMS.Count == 0 && startup.ManualTimeMS.Count == 0 {
		report.WriteString("本次没有关联启动测试记录。综合性能测试不会自动重启应用；需要启动结论时，请先在本页完成启动测试并勾选关联记录。\n\n")
		return
	}
	report.WriteString("| 指标 | 样本 | 平均值 | 最小 / 最大 | P90 | 结论权重 |\n")
	report.WriteString("|---|---:|---:|---:|---:|---|\n")
	if startup.ManualTimeMS.Count > 0 {
		fmt.Fprintf(report, "| 手动（ms） | %d | %.2f ms | %.0f / %.0f ms | %.0f ms | 主要结论 |\n",
			startup.ManualTimeMS.Count, startup.ManualTimeMS.Mean, startup.ManualTimeMS.Min, startup.ManualTimeMS.Max, startup.ManualTimeMS.P90)
	}
	if startup.TotalTimeMS.Count > 0 {
		weight := "主要结论"
		if startup.ManualTimeMS.Count > 0 {
			weight = "辅助参考"
		}
		fmt.Fprintf(report, "| TotalTime | %d | %.2f ms | %.0f / %.0f ms | %.0f ms | %s |\n",
			startup.TotalTimeMS.Count, startup.TotalTimeMS.Mean, startup.TotalTimeMS.Min, startup.TotalTimeMS.Max, startup.TotalTimeMS.P90, weight)
	}
	report.WriteString("\n`TotalTime` 主要衡量 Activity 启动到首帧相关耗时，不等于页面完全加载完成；手动（ms）表示用户看到页面完全加载并可以进行下一步操作所需的全部时间。两者同时存在时，以手动（ms）作为本次启动耗时的主要结论。\n\n")
}

func writeCPUSection(report *strings.Builder, results []DeviceResult, thresholds Thresholds) {
	report.WriteString("## 6. CPU 表现\n\n")
	fmt.Fprintf(report, "建议阈值：平均值 <= %.1f%%，峰值 <= %.1f%%。\n\n", thresholds.CPUAveragePercent, thresholds.CPUPeakPercent)
	for _, device := range results {
		fmt.Fprintf(report, "### %s\n\n", markdownInline(deviceDisplayName(device)))
		fmt.Fprintf(report, "- `top` CPU：平均 %s，峰值 %s，P90 %s。\n",
			formatMetric(device.Samples.TopCPUPercent.Mean, device.Samples.TopCPUPercent.Count > 0, "%"),
			formatMetric(device.Samples.TopCPUPercent.Max, device.Samples.TopCPUPercent.Count > 0, "%"),
			formatMetric(device.Samples.TopCPUPercent.P90, device.Samples.TopCPUPercent.Count > 0, "%"))
		fmt.Fprintf(report, "- 区间 CPU：平均 %s，峰值 %s。\n\n",
			formatMetric(device.Samples.IntervalCPUPercent.Mean, device.Samples.IntervalCPUPercent.Count > 0, "%"),
			formatMetric(device.Samples.IntervalCPUPercent.Max, device.Samples.IntervalCPUPercent.Count > 0, "%"))
		report.WriteString("| 场景 | 样本 | CPU 平均 | CPU 峰值 | 区间 CPU 平均 | PSS 平均 |\n")
		report.WriteString("|---|---:|---:|---:|---:|---:|\n")
		for _, scenario := range device.Samples.PerScenario {
			fmt.Fprintf(report, "| %s | %d | %s | %s | %s | %s |\n",
				markdownTable(scenario.ScenarioName), scenario.SampleCount,
				formatMetric(scenario.TopCPUPercent.Mean, scenario.TopCPUPercent.Count > 0, "%"),
				formatMetric(scenario.TopCPUPercent.Max, scenario.TopCPUPercent.Count > 0, "%"),
				formatMetric(scenario.IntervalCPUPercent.Mean, scenario.IntervalCPUPercent.Count > 0, "%"),
				formatMetric(scenario.PSSKB.Mean/1024, scenario.PSSKB.Count > 0, " MB"))
		}
		report.WriteString("\n")
	}
}

func writeMemorySection(report *strings.Builder, results []DeviceResult, thresholds Thresholds) {
	report.WriteString("## 7. 内存表现\n\n")
	fmt.Fprintf(report, "建议阈值：交互样本 PSS 平均值 <= %.1f MB。\n\n", thresholds.PSSAverageMB)
	report.WriteString("| 设备 | PSS 平均 / P90 / 峰值 | 测试前 PSS | 测试后 PSS | Java / Native / Code / Private Other（测试后） | 线程平均 / 峰值 |\n")
	report.WriteString("|---|---:|---:|---:|---:|---:|\n")
	for _, device := range results {
		componentsAvailable := device.MemoryFinal.Available
		components := "N/A"
		if componentsAvailable {
			components = fmt.Sprintf("%.1f / %.1f / %.1f / %.1f MB",
				float64(device.MemoryFinal.JavaHeapKB)/1024, float64(device.MemoryFinal.NativeHeapKB)/1024,
				float64(device.MemoryFinal.CodeKB)/1024, float64(device.MemoryFinal.PrivateOtherKB)/1024)
		}
		fmt.Fprintf(report, "| %s | %s / %s / %s | %s | %s | %s | %s / %s |\n",
			markdownTable(deviceDisplayName(device)),
			formatMetric(device.Samples.PSSKB.Mean/1024, device.Samples.PSSKB.Count > 0, " MB"),
			formatMetric(device.Samples.PSSKB.P90/1024, device.Samples.PSSKB.Count > 0, " MB"),
			formatMetric(device.Samples.PSSKB.Max/1024, device.Samples.PSSKB.Count > 0, " MB"),
			formatMetric(float64(device.MemoryBefore.PSSKB)/1024, device.MemoryBefore.Available, " MB"),
			formatMetric(float64(device.MemoryFinal.PSSKB)/1024, device.MemoryFinal.Available, " MB"), components,
			formatMetric(device.Samples.Threads.Mean, device.Samples.Threads.Count > 0, ""),
			formatMetric(device.Samples.Threads.Max, device.Samples.Threads.Count > 0, ""))
	}
	report.WriteString("\n")
}

func writeRenderingSection(report *strings.Builder, results []DeviceResult, thresholds Thresholds) {
	report.WriteString("## 8. 渲染与流畅度\n\n")
	fmt.Fprintf(report, "建议阈值：Janky frames <= %.1f%%，帧耗时 P90 <= %.1f ms。\n\n", thresholds.JankyFramesPercent, thresholds.FrameP90MS)
	report.WriteString("| 设备 | 总帧数 | 卡顿帧 | Janky | P50 / P90 / P95 / P99 | Missed Vsync | High input latency | Deadline missed |\n")
	report.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, device := range results {
		if !device.GfxInfo.Available {
			fmt.Fprintf(report, "| %s | N/A | N/A | N/A | N/A | N/A | N/A | N/A |\n", markdownTable(deviceDisplayName(device)))
			continue
		}
		fmt.Fprintf(report, "| %s | %d | %d | %.1f%% | %.0f / %.0f / %.0f / %.0f ms | %d | %d | %d |\n",
			markdownTable(deviceDisplayName(device)), device.GfxInfo.TotalFrames, device.GfxInfo.JankyFrames, device.GfxInfo.JankyPercent,
			device.GfxInfo.P50MS, device.GfxInfo.P90MS, device.GfxInfo.P95MS, device.GfxInfo.P99MS,
			device.GfxInfo.MissedVsync, device.GfxInfo.HighInputLatency, device.GfxInfo.FrameDeadlineMissed)
	}
	report.WriteString("\n")
}

func writeStabilitySection(report *strings.Builder, results []DeviceResult) {
	report.WriteString("## 9. 稳定性与 AudioFocus\n\n")
	report.WriteString("| 设备 | PID 前 / 后 | 进程存活样本 | 前台样本 | FATAL / am_crash | ANR | AudioFocus 次数 / 频率 |\n")
	report.WriteString("|---|---:|---:|---:|---:|---:|---:|\n")
	for _, device := range results {
		fmt.Fprintf(report, "| %s | %d / %d | %d/%d | %d/%d | %d / %d | %d | %s |\n",
			markdownTable(deviceDisplayName(device)), device.InitialPID, device.FinalPID,
			device.Samples.ProcessAliveSamples, device.Samples.SampleCount,
			device.Samples.ForegroundSamples, device.Samples.SampleCount,
			device.Logcat.FatalExceptionCount, device.Logcat.AMCrashCount, device.Logcat.ANRCount,
			formatRate(device.Logcat.AudioFocusRequestCount, device.Logcat.AudioFocusRequestsPerSecond))
	}
	report.WriteString("\nAudioFocus 没有统一官方通过阈值；本工具将超过 1 次/秒的持续请求列为风险，建议结合首尾日志行和业务行为复核是否存在重复申请。\n\n")
}

func writeNetworkSection(report *strings.Builder, results []DeviceResult) {
	report.WriteString("## 10. 网络与系统观察\n\n")
	report.WriteString("| 设备 | 测试时长 | 接收 | 发送 | 总流量 | WebView（测试后） |\n")
	report.WriteString("|---|---:|---:|---:|---:|---:|\n")
	for _, device := range results {
		rx, tx, total := "N/A", "N/A", "N/A"
		if device.NetworkDelta.Available {
			rx, tx, total = formatBytes(device.NetworkDelta.RXBytes), formatBytes(device.NetworkDelta.TXBytes), formatBytes(device.NetworkDelta.TotalBytes)
		}
		fmt.Fprintf(report, "| %s | %.1f s | %s | %s | %s | %d |\n", markdownTable(deviceDisplayName(device)), device.ElapsedSeconds, rx, tx, total, device.MemoryFinal.WebViews)
	}
	report.WriteString("\n")
}

func writeRecommendationsSection(report *strings.Builder, issues []string) {
	report.WriteString("## 11. 问题与建议\n\n")
	if len(issues) == 0 {
		report.WriteString("1. 当前建议阈值下没有明确异常；建议用相同场景和设备状态定期复测，观察版本趋势。\n")
		report.WriteString("2. 发布前仍应补充长稳、弱网、冷热启动和低端设备覆盖，本次交互采样不能替代这些专项测试。\n\n")
		return
	}
	for index, issue := range issues {
		fmt.Fprintf(report, "%d. **现象**：%s\n", index+1, issue)
	}
	report.WriteString("\n优先处理超过阈值且能稳定复现的场景；对 CPU/帧问题追加 Perfetto，对内存问题追加 heap/profile，对 AudioFocus 问题检查申请与释放调用链。修复后使用相同坐标、轮数、等待时间和设备状态复测。\n\n")
}

func writeEvidenceSection(report *strings.Builder, result Result) {
	report.WriteString("## 12. 证据与产物\n\n")
	report.WriteString("根目录包含 `protocol.json`、`combined-results.json` 和本报告；每台设备目录包含 `samples.csv`、`summary.json` 及原始证据。\n\n")
	for _, device := range result.Results {
		base := relativePath(result.ResultDir, device.ResultDir)
		fmt.Fprintf(report, "### %s\n\n", markdownInline(deviceDisplayName(device)))
		fmt.Fprintf(report, "- [采样明细](%s)\n", markdownLink(filepath.Join(base, "samples.csv")))
		fmt.Fprintf(report, "- [结构化汇总](%s)\n", markdownLink(filepath.Join(base, "summary.json")))
		fmt.Fprintf(report, "- [测试前截图](%s) / [测试后截图](%s)\n", markdownLink(filepath.Join(base, "screen-before.png")), markdownLink(filepath.Join(base, "screen-after.png")))
		fmt.Fprintf(report, "- [连续日志](%s) / [最终日志缓冲](%s)\n", markdownLink(filepath.Join(base, "logcat-live.txt")), markdownLink(filepath.Join(base, "logcat-final-buffer.txt")))
		fmt.Fprintf(report, "- [最终 gfxinfo](%s) / [测试前 meminfo](%s) / [测试后 meminfo](%s)\n\n",
			markdownLink(filepath.Join(base, "gfxinfo-final.txt")), markdownLink(filepath.Join(base, "meminfo-before.txt")), markdownLink(filepath.Join(base, "meminfo-final.txt")))
	}
}

func deviceDisplayName(result DeviceResult) string {
	model := strings.TrimSpace(strings.Join([]string{result.Metadata.Manufacturer, result.Metadata.Model}, " "))
	if model == "" {
		return deviceDisplaySerial(result.Serial)
	}
	return model + "（" + result.Serial + "）"
}

func deviceDisplaySerial(serial string) string {
	if strings.TrimSpace(serial) == "" {
		return "未知设备"
	}
	return "设备 " + serial
}

func formatMetric(value float64, available bool, suffix string) string {
	if !available {
		return "N/A"
	}
	return fmt.Sprintf("%.1f%s", value, suffix)
}

func formatRate(count int, rate float64) string {
	return fmt.Sprintf("%d / %.2f 次/秒", count, rate)
}

func passLabel(passed bool) string {
	if passed {
		return "通过"
	}
	return "未通过/复核"
}

func valueOrNA(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "()" {
		return "N/A"
	}
	return value
}

func shortHash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "N/A"
	}
	if len(value) <= 16 {
		return value
	}
	return value[:12] + "..." + value[len(value)-4:]
}

func sameAPK(results []DeviceResult) bool {
	hashes := make(map[string]bool)
	for _, result := range results {
		if hash := strings.TrimSpace(strings.ToLower(result.Metadata.APKSHA256)); hash != "" {
			hashes[hash] = true
		}
	}
	return len(hashes) <= 1
}

func formatBytes(value int64) string {
	negative := value < 0
	if negative {
		value = -value
	}
	var formatted string
	switch {
	case value >= 1024*1024:
		formatted = fmt.Sprintf("%.2f MiB", float64(value)/(1024*1024))
	case value >= 1024:
		formatted = fmt.Sprintf("%.1f KiB", float64(value)/1024)
	default:
		formatted = fmt.Sprintf("%d B", value)
	}
	if negative {
		return "-" + formatted
	}
	return formatted
}

func relativePath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.Base(path)
	}
	return filepath.ToSlash(relative)
}

func markdownLink(path string) string {
	return strings.ReplaceAll(filepath.ToSlash(path), " ", "%20")
}

func markdownInline(value string) string {
	value = strings.ReplaceAll(value, "`", "'")
	return strings.ReplaceAll(strings.TrimSpace(value), "\n", " ")
}

func markdownTable(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "\n", " ")
}
