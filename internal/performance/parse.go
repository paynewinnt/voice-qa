package performance

import (
	"bytes"
	"encoding/binary"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	packageNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+$`)
)

func metricStats(values []float64) MetricStats {
	if len(values) == 0 {
		return MetricStats{}
	}
	values = append([]float64(nil), values...)
	sort.Float64s(values)
	var sum float64
	for _, value := range values {
		sum += value
	}
	return MetricStats{
		Count: len(values),
		Mean:  roundTo(sum/float64(len(values)), 3),
		Min:   roundTo(values[0], 3),
		Max:   roundTo(values[len(values)-1], 3),
		P90:   roundTo(percentile(values, 0.9), 3),
	}
}

func percentile(sortedValues []float64, p float64) float64 {
	if len(sortedValues) == 0 {
		return 0
	}
	if len(sortedValues) == 1 {
		return sortedValues[0]
	}
	rank := float64(len(sortedValues)-1) * p
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return sortedValues[lo]
	}
	return sortedValues[lo] + (sortedValues[hi]-sortedValues[lo])*(rank-float64(lo))
}

func roundTo(value float64, digits int) float64 {
	factor := math.Pow10(digits)
	return math.Round(value*factor) / factor
}

func parsePackageMetadata(text string) PackageMetadata {
	return PackageMetadata{
		VersionName:      firstMatch(text, `(?m)\bversionName=([^\s]+)`),
		VersionCode:      firstMatch(text, `(?m)\bversionCode=(\d+)`),
		MinSDK:           firstMatch(text, `(?m)\bminSdk=(\d+)`),
		TargetSDK:        firstMatch(text, `(?m)\btargetSdk=(\d+)`),
		UserID:           parseInt(firstMatch(text, `(?m)\buserId=(\d+)`)),
		FirstInstallTime: strings.TrimSpace(firstMatch(text, `(?m)\bfirstInstallTime=([^\r\n]+)`)),
		LastUpdateTime:   strings.TrimSpace(firstMatch(text, `(?m)\blastUpdateTime=([^\r\n]+)`)),
	}
}

func parseGetProp(text string) map[string]string {
	properties := make(map[string]string)
	linePattern := regexp.MustCompile(`^\[([^]]+)\]: \[([^]]*)\]$`)
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		match := linePattern.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) == 3 {
			properties[match[1]] = match[2]
		}
	}
	return properties
}

func parseMemorySnapshot(text string) MemorySnapshot {
	pss := firstInt64(text,
		`(?m)^\s*TOTAL PSS:\s*(\d+)`,
		`(?m)^\s*TOTAL:\s*(\d+)`,
		`(?m)^\s*TOTAL\s+(\d+)`,
	)
	return MemorySnapshot{
		Available:      pss > 0,
		PSSKB:          pss,
		JavaHeapKB:     firstInt64(text, `(?m)^\s*Java Heap:\s*(\d+)`, `(?m)^\s*Java Heap\s+(\d+)`),
		NativeHeapKB:   firstInt64(text, `(?m)^\s*Native Heap:\s*(\d+)`, `(?m)^\s*Native Heap\s+(\d+)`),
		CodeKB:         firstInt64(text, `(?m)^\s*Code:\s*(\d+)`, `(?m)^\s*Code\s+(\d+)`),
		PrivateOtherKB: firstInt64(text, `(?m)^\s*Private Other:\s*(\d+)`, `(?m)^\s*Private Other\s+(\d+)`),
		SystemKB:       firstInt64(text, `(?m)^\s*System:\s*(\d+)`, `(?m)^\s*System\s+(\d+)`),
		WebViews:       firstInt64(text, `(?m)^\s*WebViews:\s*(\d+)`),
	}
}

func parseProcessStatus(text string) ProcessStatus {
	return ProcessStatus{
		Threads: firstInt64(text, `(?m)^Threads:\s*(\d+)`),
		VMRSSKB: firstInt64(text, `(?m)^VmRSS:\s*(\d+)`),
	}
}

func parseTop(text string, pid int, packageName string) (cpu, memory float64, raw string, ok bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	pidIndex, cpuIndex, memoryIndex := 0, -1, -1
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if indexOf(fields, "PID") >= 0 {
			pidIndex = indexOf(fields, "PID")
			cpuIndex = firstIndexOf(fields, "%CPU", "CPU%")
			memoryIndex = firstIndexOf(fields, "%MEM", "MEM%")
			continue
		}
		if pidIndex >= len(fields) || fields[pidIndex] != strconv.Itoa(pid) || !strings.Contains(line, packageName) {
			continue
		}
		if cpuIndex >= 0 && cpuIndex < len(fields) {
			cpu, ok = parsePercent(fields[cpuIndex])
		}
		if memoryIndex >= 0 && memoryIndex < len(fields) {
			memory, _ = parsePercent(fields[memoryIndex])
		}
		if ok {
			return cpu, memory, strings.TrimSpace(line), true
		}
		statePattern := regexp.MustCompile(`\b[SRDTZ]\s+([\d.]+)%?\s+([\d.]+)%?\s+`)
		if match := statePattern.FindStringSubmatch(line); len(match) == 3 {
			cpu, errCPU := strconv.ParseFloat(match[1], 64)
			memory, errMemory := strconv.ParseFloat(match[2], 64)
			if errCPU == nil && errMemory == nil {
				return cpu, memory, strings.TrimSpace(line), true
			}
		}
	}
	return 0, 0, "", false
}

func parseProcTicks(text string) (int64, bool) {
	closeIndex := strings.LastIndex(text, ")")
	if closeIndex < 0 || closeIndex+2 >= len(text) {
		return 0, false
	}
	fields := strings.Fields(text[closeIndex+2:])
	if len(fields) <= 12 {
		return 0, false
	}
	userTicks, errUser := strconv.ParseInt(fields[11], 10, 64)
	systemTicks, errSystem := strconv.ParseInt(fields[12], 10, 64)
	if errUser != nil || errSystem != nil {
		return 0, false
	}
	return userTicks + systemTicks, true
}

func parseGfxInfo(text string) GfxMetrics {
	metrics := GfxMetrics{
		TotalFrames:         lastInt64(text, `(?im)Total frames rendered:\s*(\d+)`),
		P50MS:               float64(lastInt64(text, `(?im)50th percentile:\s*(\d+)ms`)),
		P90MS:               float64(lastInt64(text, `(?im)90th percentile:\s*(\d+)ms`)),
		P95MS:               float64(lastInt64(text, `(?im)95th percentile:\s*(\d+)ms`)),
		P99MS:               float64(lastInt64(text, `(?im)99th percentile:\s*(\d+)ms`)),
		MissedVsync:         lastInt64(text, `(?im)Number Missed Vsync:\s*(\d+)`),
		HighInputLatency:    lastInt64(text, `(?im)Number High input latency:\s*(\d+)`),
		SlowUIThread:        lastInt64(text, `(?im)Number Slow UI thread:\s*(\d+)`),
		SlowBitmapUploads:   lastInt64(text, `(?im)Number Slow bitmap uploads:\s*(\d+)`),
		SlowDrawCommands:    lastInt64(text, `(?im)Number Slow issue draw commands:\s*(\d+)`),
		FrameDeadlineMissed: lastInt64(text, `(?im)Number Frame deadline missed:\s*(\d+)`),
	}
	jankyPattern := regexp.MustCompile(`(?im)Janky frames:\s*(\d+)\s*\(([\d.]+)%\)`)
	matches := jankyPattern.FindAllStringSubmatch(text, -1)
	if len(matches) > 0 {
		last := matches[len(matches)-1]
		metrics.JankyFrames, _ = strconv.ParseInt(last[1], 10, 64)
		metrics.JankyPercent, _ = strconv.ParseFloat(last[2], 64)
	}
	metrics.Available = metrics.TotalFrames > 0 && len(matches) > 0 &&
		regexp.MustCompile(`(?im)90th percentile:\s*\d+ms`).MatchString(text)
	return metrics
}

func analyzeLogcat(text, packageName string, elapsedSeconds float64) LogAnalysis {
	seen := make(map[string]struct{})
	lines := make([]string, 0)
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, exists := seen[line]; exists {
			continue
		}
		seen[line] = struct{}{}
		lines = append(lines, line)
	}
	combined := strings.Join(lines, "\n")
	audioLines := make([]string, 0)
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "requestaudiofocus") && strings.Contains(line, packageName) {
			audioLines = append(audioLines, line)
		}
	}
	quotedPackage := regexp.QuoteMeta(packageName)
	result := LogAnalysis{
		UniqueLineCount:        len(lines),
		AMCrashCount:           len(regexp.MustCompile(`(?im)am_crash[^\r\n]*`+quotedPackage).FindAllString(combined, -1)),
		ANRCount:               len(regexp.MustCompile(`(?im)(?:am_anr[^\r\n]*`+quotedPackage+`|ANR in\s+`+quotedPackage+`|Application Not Responding[^\r\n]*`+quotedPackage+`)`).FindAllString(combined, -1)),
		AudioFocusRequestCount: len(audioLines),
	}
	result.FatalExceptionCount = countFatalExceptions(combined, packageName)
	if elapsedSeconds > 0 {
		result.AudioFocusRequestsPerSecond = roundTo(float64(len(audioLines))/elapsedSeconds, 3)
	}
	if len(audioLines) > 0 {
		result.AudioFocusFirstLine = audioLines[0]
		result.AudioFocusLastLine = audioLines[len(audioLines)-1]
	}
	return result
}

func countFatalExceptions(text, packageName string) int {
	const marker = "FATAL EXCEPTION"
	count := 0
	remaining := text
	for {
		start := strings.Index(remaining, marker)
		if start < 0 {
			return count
		}
		block := remaining[start+len(marker):]
		if next := strings.Index(block, marker); next >= 0 {
			block = block[:next]
		}
		if len(block) > 3000 {
			block = block[:3000]
		}
		if strings.Contains(block, "Process: "+packageName) {
			count++
		}
		remaining = remaining[start+len(marker):]
	}
}

func normalizePNG(data []byte) []byte {
	signature := []byte("\x89PNG\r\n\x1a\n")
	start := bytes.Index(data, signature)
	if start < 0 {
		return data
	}
	position := start + len(signature)
	for position+12 <= len(data) {
		chunkLength := int(binary.BigEndian.Uint32(data[position : position+4]))
		chunkType := data[position+4 : position+8]
		position += 12 + chunkLength
		if position > len(data) {
			return data[start:]
		}
		if bytes.Equal(chunkType, []byte("IEND")) {
			return data[start:position]
		}
	}
	return data[start:]
}

func pngEvidence(data []byte) ScreenEvidence {
	result := ScreenEvidence{Bytes: int64(len(data)), ValidPNG: bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n"))}
	if result.ValidPNG && len(data) >= 24 {
		result.Width = int(binary.BigEndian.Uint32(data[16:20]))
		result.Height = int(binary.BigEndian.Uint32(data[20:24]))
	}
	return result
}

func summarizeSamples(samples []Sample, scenarios []Scenario) SampleSummary {
	result := SampleSummary{SampleCount: len(samples)}
	topCPU := make([]float64, 0, len(samples))
	intervalCPU := make([]float64, 0, len(samples))
	pss := make([]float64, 0, len(samples))
	threads := make([]float64, 0, len(samples))
	vmrss := make([]float64, 0, len(samples))
	for _, sample := range samples {
		if sample.TopCPUAvailable {
			topCPU = append(topCPU, sample.TopCPUPercent)
		}
		if sample.IntervalAvailable {
			intervalCPU = append(intervalCPU, sample.IntervalCPUPercent)
		}
		if sample.PSSKB > 0 {
			pss = append(pss, float64(sample.PSSKB))
		}
		if sample.Threads > 0 {
			threads = append(threads, float64(sample.Threads))
		}
		if sample.VMRSSKB > 0 {
			vmrss = append(vmrss, float64(sample.VMRSSKB))
		}
		if sample.ProcessAlive {
			result.ProcessAliveSamples++
		}
		if sample.Foreground {
			result.ForegroundSamples++
		}
	}
	result.TopCPUPercent = metricStats(topCPU)
	result.IntervalCPUPercent = metricStats(intervalCPU)
	result.PSSKB = metricStats(pss)
	result.Threads = metricStats(threads)
	result.VMRSSKB = metricStats(vmrss)

	for index, scenario := range scenarios {
		selected := make([]Sample, 0)
		for _, sample := range samples {
			if sample.ScenarioIndex == index+1 {
				selected = append(selected, sample)
			}
		}
		cardTop := make([]float64, 0, len(selected))
		cardInterval := make([]float64, 0, len(selected))
		cardPSS := make([]float64, 0, len(selected))
		for _, sample := range selected {
			if sample.TopCPUAvailable {
				cardTop = append(cardTop, sample.TopCPUPercent)
			}
			if sample.IntervalAvailable {
				cardInterval = append(cardInterval, sample.IntervalCPUPercent)
			}
			if sample.PSSKB > 0 {
				cardPSS = append(cardPSS, float64(sample.PSSKB))
			}
		}
		result.PerScenario = append(result.PerScenario, ScenarioSummary{
			ScenarioIndex:      index + 1,
			ScenarioName:       scenario.Name,
			SampleCount:        len(selected),
			TopCPUPercent:      metricStats(cardTop),
			IntervalCPUPercent: metricStats(cardInterval),
			PSSKB:              metricStats(cardPSS),
		})
	}
	return result
}

func summarizeStartup(samples []StartupSample) StartupSummary {
	byGroup := make(map[string][]StartupSample)
	var keys []string
	for _, sample := range samples {
		if _, exists := byGroup[sample.Group]; !exists {
			keys = append(keys, sample.Group)
		}
		byGroup[sample.Group] = append(byGroup[sample.Group], sample)
	}
	if len(keys) > 1 {
		result := StartupSummary{SampleCount: len(samples)}
		for _, key := range keys {
			summary := summarizeStartup(byGroup[key])
			result.Groups = append(result.Groups, StartupGroup{
				Label: byGroup[key][0].Label, SampleCount: summary.SampleCount,
				TotalTimeMS: summary.TotalTimeMS, ManualTimeMS: summary.ManualTimeMS, PrimaryMetric: summary.PrimaryMetric,
			})
		}
		return result
	}
	totalValues := make([]float64, 0, len(samples))
	manualValues := make([]float64, 0, len(samples))
	for _, sample := range samples {
		if sample.TotalTimeMS > 0 {
			totalValues = append(totalValues, float64(sample.TotalTimeMS))
		}
		if sample.ManualDurationMS > 0 {
			manualValues = append(manualValues, float64(sample.ManualDurationMS))
		}
	}
	primary := ""
	if len(manualValues) > 0 {
		primary = "manual"
	} else if len(totalValues) > 0 {
		primary = "totalTime"
	}
	return StartupSummary{
		SampleCount:   len(samples),
		TotalTimeMS:   metricStats(totalValues),
		ManualTimeMS:  metricStats(manualValues),
		PrimaryMetric: primary,
	}
}

func firstMatch(text, pattern string) string {
	match := regexp.MustCompile(pattern).FindStringSubmatch(text)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func firstInt64(text string, patterns ...string) int64 {
	for _, pattern := range patterns {
		if value := parseInt64(firstMatch(text, pattern)); value != 0 {
			return value
		}
	}
	return 0
}

func lastInt64(text, pattern string) int64 {
	matches := regexp.MustCompile(pattern).FindAllStringSubmatch(text, -1)
	if len(matches) == 0 || len(matches[len(matches)-1]) < 2 {
		return 0
	}
	return parseInt64(matches[len(matches)-1][1])
}

func parseInt(value string) int {
	parsed, _ := strconv.Atoi(strings.TrimSpace(value))
	return parsed
}

func parseInt64(value string) int64 {
	parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return parsed
}

func parsePercent(value string) (float64, bool) {
	parsed, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(value), "%"), 64)
	return parsed, err == nil
}

func indexOf(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}

func firstIndexOf(values []string, targets ...string) int {
	for _, target := range targets {
		if index := indexOf(values, target); index >= 0 {
			return index
		}
	}
	return -1
}
