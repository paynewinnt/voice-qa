package perfetto

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"voice-qa/internal/adb"
)

const remoteTraceDir = "/sdcard"
const minAnalyzableTraceBytes int64 = 10 * 1024

type Options struct {
	Enabled            bool `json:"enabled"`
	MaxDurationSeconds int  `json:"maxDurationSeconds"`
	BufferSizeMB       int  `json:"bufferSizeMB"`
}

type LaunchResult struct {
	Success          bool             `json:"success"`
	Message          string           `json:"message"`
	Serial           string           `json:"serial"`
	Component        string           `json:"component"`
	PackageName      string           `json:"packageName"`
	ThisTimeMS       int              `json:"thisTimeMs"`
	TotalTimeMS      int              `json:"totalTimeMs"`
	WaitTimeMS       int              `json:"waitTimeMs"`
	ManualDurationMS int64            `json:"manualDurationMs"`
	RawOutput        string           `json:"rawOutput"`
	TraceFile        string           `json:"traceFile"`
	LogcatFile       string           `json:"logcatFile"`
	ReportFile       string           `json:"reportFile"`
	TimelineFile     string           `json:"timelineFile"`
	TraceSizeBytes   int64            `json:"traceSizeBytes"`
	TraceAnalyzable  bool             `json:"traceAnalyzable"`
	TraceWarning     string           `json:"traceWarning"`
	ResultDir        string           `json:"resultDir"`
	Timestamp        string           `json:"timestamp"`
	StoppedAt        string           `json:"stoppedAt"`
	StopReason       string           `json:"stopReason"`
	Capability       CapabilityResult `json:"capability"`
	LogcatAnalysis   LogcatAnalysis   `json:"logcatAnalysis"`
}

type CapabilityResult struct {
	PerfettoCommandFound        bool   `json:"perfettoCommandFound"`
	PerfettoPath                string `json:"perfettoPath"`
	PerfettoHelpOK              bool   `json:"perfettoHelpOk"`
	PerfettoHelpSummary         string `json:"perfettoHelpSummary"`
	SupportsBackgroundWait      bool   `json:"supportsBackgroundWait"`
	SupportsTextConfig          bool   `json:"supportsTextConfig"`
	SupportsDetach              bool   `json:"supportsDetach"`
	AtraceListOK                bool   `json:"atraceListOk"`
	AtraceError                 string `json:"atraceError"`
	TracefsPath                 string `json:"tracefsPath"`
	TracefsReadable             bool   `json:"tracefsReadable"`
	TracefsHasEntries           bool   `json:"tracefsHasEntries"`
	FtraceLikelyUsable          bool   `json:"ftraceLikelyUsable"`
	AnalyzableKernelTraceLikely bool   `json:"analyzableKernelTraceLikely"`
	Warning                     string `json:"warning"`
}

type Session struct {
	Serial          string
	ConfigPath      string
	RemotePath      string
	LocalPath       string
	ResultDir       string
	PID             string
	cmd             *exec.Cmd
	StartedAt       time.Time
	Capability      CapabilityResult
	TraceSizeBytes  int64
	TraceAnalyzable bool
	TraceWarning    string
}

func NormalizeOptions(opts Options) Options {
	if opts.MaxDurationSeconds <= 0 {
		opts.MaxDurationSeconds = 30
	}
	if opts.BufferSizeMB <= 0 {
		opts.BufferSizeMB = 64
	}
	return opts
}

func PackageNameFromComponent(component string) string {
	if idx := strings.Index(component, "/"); idx > 0 {
		return component[:idx]
	}
	return component
}

func CreateResultDir(baseDir string) (string, error) {
	if baseDir == "" {
		baseDir = "output"
	}
	dir := filepath.Join(baseDir, "perf", "launch-"+time.Now().Format("20060102-150405")+"-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

func CheckSupport(serial string) error {
	capability := DetectCapability(serial)
	if !capability.PerfettoCommandFound {
		return fmt.Errorf("设备未找到 perfetto 命令")
	}
	if !capability.PerfettoHelpOK {
		return fmt.Errorf("设备 perfetto 命令存在但无法执行 --help: %s", capability.PerfettoHelpSummary)
	}
	return nil
}

func DetectCapability(serial string) CapabilityResult {
	result := CapabilityResult{}

	cmd := adb.Command("-s", serial, "shell", "command", "-v", "perfetto")
	adb.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	result.PerfettoPath = strings.TrimSpace(string(output))
	result.PerfettoCommandFound = err == nil && result.PerfettoPath != ""

	help := runOutput(serial, "perfetto", "--help")
	result.PerfettoHelpOK = strings.TrimSpace(help.output) != "" && (help.err == nil || strings.Contains(help.output, "Usage: perfetto"))
	result.PerfettoHelpSummary = summarizeOutput(help.output)
	result.SupportsBackgroundWait = strings.Contains(help.output, "--background-wait")
	result.SupportsTextConfig = strings.Contains(help.output, "--txt")
	result.SupportsDetach = strings.Contains(help.output, "--detach") || strings.Contains(help.output, "--background")

	atrace := runOutput(serial, "atrace", "--list_categories")
	result.AtraceListOK = atrace.err == nil && !containsAny(atrace.output, "Did not find trace folder", "No trace folder found")
	if !result.AtraceListOK {
		result.AtraceError = summarizeOutput(atrace.output)
		if result.AtraceError == "" && atrace.err != nil {
			result.AtraceError = atrace.err.Error()
		}
	}

	result.TracefsPath, result.TracefsReadable, result.TracefsHasEntries = detectTracefs(serial)
	result.FtraceLikelyUsable = result.AtraceListOK || (result.TracefsReadable && result.TracefsHasEntries)
	result.AnalyzableKernelTraceLikely = result.PerfettoCommandFound && result.PerfettoHelpOK && result.FtraceLikelyUsable
	if !result.AnalyzableKernelTraceLikely {
		result.Warning = "Perfetto 命令存在，但 ftrace/atrace 不可用，生成的 trace 可能没有 kernel slice，无法用于标准 Perfetto 性能分析"
	}
	return result
}

func Start(serial, packageName, resultDir string, opts Options) (*Session, error) {
	opts = NormalizeOptions(opts)
	capability := DetectCapability(serial)
	if !capability.PerfettoCommandFound {
		return nil, fmt.Errorf("设备未找到 perfetto 命令")
	}
	if !capability.PerfettoHelpOK {
		return nil, fmt.Errorf("设备 perfetto 命令存在但无法执行 --help: %s", capability.PerfettoHelpSummary)
	}
	if err := SaveCapability(resultDir, capability); err != nil {
		return nil, err
	}
	startDaemons(serial)

	traceName := "voiceqa-" + time.Now().Format("20060102-150405") + "-" + strconv.FormatInt(time.Now().UnixNano(), 10) + ".perfetto-trace"
	remotePath := remoteTraceDir + "/" + traceName
	localPath := filepath.Join(resultDir, "trace.perfetto-trace")
	configPath := remoteTraceDir + "/" + strings.TrimSuffix(traceName, ".perfetto-trace") + ".bin"

	runHidden("-s", serial, "shell", "rm", "-f", remotePath)
	runHidden("-s", serial, "shell", "rm", "-f", configPath)

	args := []string{
		"-s", serial, "shell", "perfetto",
		"--background-wait",
		"-o", remotePath,
		"-t", fmt.Sprintf("%ds", opts.MaxDurationSeconds),
		"-b", fmt.Sprintf("%dmb", opts.BufferSizeMB),
	}
	if strings.TrimSpace(packageName) != "" {
		args = append(args, "-a", packageName)
	}
	args = append(args, "sched", "freq", "idle", "am", "wm", "view", "gfx", "input")

	cmd := adb.Command(args...)
	adb.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "unrecognized option") {
			return startWithBinaryConfig(serial, packageName, resultDir, remotePath, localPath, configPath, opts)
		}
		return nil, fmt.Errorf("启动 Perfetto 失败: %s %w", strings.TrimSpace(string(output)), err)
	}

	return &Session{
		Serial:     serial,
		ConfigPath: configPath,
		RemotePath: remotePath,
		LocalPath:  localPath,
		ResultDir:  resultDir,
		PID:        parsePID(string(output)),
		StartedAt:  time.Now(),
		Capability: capability,
	}, nil
}

func (s *Session) StopAndPull() (string, error) {
	if s == nil {
		return "", nil
	}

	if strings.TrimSpace(s.PID) != "" {
		runHidden("-s", s.Serial, "shell", "kill", "-INT", s.PID)
	} else {
		runHidden("-s", s.Serial, "shell", "pkill", "-INT", "perfetto")
	}
	if s.cmd != nil {
		done := make(chan struct{})
		go func() {
			_ = s.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if s.cmd.Process != nil {
				_ = s.cmd.Process.Kill()
			}
		}
	}

	time.Sleep(1200 * time.Millisecond)

	if err := pullFile(s.Serial, s.RemotePath, s.LocalPath); err != nil {
		return "", err
	}
	s.TraceSizeBytes, s.TraceAnalyzable, s.TraceWarning = ValidateTraceFile(s.LocalPath)
	runHidden("-s", s.Serial, "shell", "rm", "-f", s.RemotePath)
	if s.ConfigPath != "" {
		runHidden("-s", s.Serial, "shell", "rm", "-f", s.ConfigPath)
	}
	return s.LocalPath, nil
}

func pullFile(serial, remotePath, localPath string) error {
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return err
	}

	pullCmd := adb.Command("-s", serial, "pull", remotePath, localPath)
	adb.HideWindow(pullCmd)
	if output, err := pullCmd.CombinedOutput(); err == nil {
		return nil
	} else {
		catCmd := adb.Command("-s", serial, "shell", "cat", remotePath)
		adb.HideWindow(catCmd)
		data, catErr := catCmd.Output()
		if catErr != nil {
			return fmt.Errorf("拉取 Perfetto trace 失败: %s；cat 兜底失败: %w", strings.TrimSpace(string(output)), catErr)
		}
		return os.WriteFile(localPath, data, 0644)
	}
}

func startDaemons(serial string) {
	runHidden("-s", serial, "shell", "setprop", "ctl.start", "traced")
	runHidden("-s", serial, "shell", "setprop", "ctl.start", "traced_probes")
	time.Sleep(250 * time.Millisecond)
}

func startWithBinaryConfig(serial, packageName, resultDir, remotePath, localPath, configPath string, opts Options) (*Session, error) {
	config := buildTraceConfig(opts, packageName)
	configFile := filepath.Join(resultDir, "trace_config.bin")
	if err := os.WriteFile(configFile, config, 0644); err != nil {
		return nil, fmt.Errorf("写入 Perfetto 配置失败: %w", err)
	}

	push := adb.Command("-s", serial, "push", configFile, configPath)
	adb.HideWindow(push)
	if output, err := push.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("推送 Perfetto 配置失败: %s %w", strings.TrimSpace(string(output)), err)
	}

	cmd := adb.Command("-s", serial, "shell", "perfetto", "-c", configPath, "-o", remotePath)
	adb.HideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动旧版 Perfetto 失败: %w", err)
	}

	return &Session{
		Serial:     serial,
		ConfigPath: configPath,
		RemotePath: remotePath,
		LocalPath:  localPath,
		ResultDir:  resultDir,
		cmd:        cmd,
		StartedAt:  time.Now(),
		Capability: DetectCapability(serial),
	}, nil
}

func runHidden(args ...string) {
	cmd := adb.Command(args...)
	adb.HideWindow(cmd)
	_ = cmd.Run()
}

func buildTraceConfig(opts Options, packageName string) []byte {
	bufferSizeKB := uint64(opts.BufferSizeMB * 1024)
	if bufferSizeKB == 0 {
		bufferSizeKB = 64 * 1024
	}
	durationMS := uint64(opts.MaxDurationSeconds * 1000)
	if durationMS == 0 {
		durationMS = 30 * 1000
	}

	buffer := protoVarintField(1, bufferSizeKB)
	ftrace := []byte{}
	for _, event := range []string{"sched_switch", "sched_wakeup"} {
		ftrace = append(ftrace, protoStringField(1, event)...)
	}
	for _, category := range []string{"am", "wm", "view", "gfx", "input"} {
		ftrace = append(ftrace, protoStringField(2, category)...)
	}
	if strings.TrimSpace(packageName) != "" {
		ftrace = append(ftrace, protoStringField(3, packageName)...)
	}

	dataSourceConfig := []byte{}
	dataSourceConfig = append(dataSourceConfig, protoStringField(1, "linux.ftrace")...)
	dataSourceConfig = append(dataSourceConfig, protoVarintField(2, 0)...)
	dataSourceConfig = append(dataSourceConfig, protoMessageField(100, ftrace)...)

	dataSource := protoMessageField(1, dataSourceConfig)
	trace := []byte{}
	trace = append(trace, protoMessageField(1, buffer)...)
	trace = append(trace, protoMessageField(2, dataSource)...)
	trace = append(trace, protoVarintField(3, durationMS)...)
	return trace
}

func protoVarintField(field int, value uint64) []byte {
	return append(protoVarint(uint64(field<<3)), protoVarint(value)...)
}

func protoStringField(field int, value string) []byte {
	data := []byte(value)
	out := append(protoVarint(uint64(field<<3|2)), protoVarint(uint64(len(data)))...)
	return append(out, data...)
}

func protoMessageField(field int, data []byte) []byte {
	out := append(protoVarint(uint64(field<<3|2)), protoVarint(uint64(len(data)))...)
	return append(out, data...)
}

func protoVarint(value uint64) []byte {
	var out []byte
	for value >= 0x80 {
		out = append(out, byte(value)|0x80)
		value >>= 7
	}
	return append(out, byte(value))
}

func ParseAmStartOutput(output string) (thisTime, totalTime, waitTime int) {
	thisTime = parseNamedTime(output, "ThisTime")
	totalTime = parseNamedTime(output, "TotalTime")
	waitTime = parseNamedTime(output, "WaitTime")
	return thisTime, totalTime, waitTime
}

func SaveAmStartOutput(resultDir, output string) error {
	return os.WriteFile(filepath.Join(resultDir, "am_start.txt"), []byte(output), 0644)
}

func SaveMetadata(resultDir string, result LaunchResult) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(resultDir, "metadata.json"), data, 0644)
}

func SaveCapability(resultDir string, capability CapabilityResult) error {
	data, err := json.MarshalIndent(capability, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(resultDir, "perfetto_capability.json"), data, 0644)
}

func ValidateTraceFile(path string) (sizeBytes int64, analyzable bool, warning string) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false, fmt.Sprintf("trace 文件不存在或无法读取: %v", err)
	}
	sizeBytes = info.Size()
	if sizeBytes < minAnalyzableTraceBytes {
		return sizeBytes, false, fmt.Sprintf("trace 文件只有 %d 字节，小于 %d 字节阈值，通常表示设备 ftrace/atrace 不可用或 trace 无有效数据", sizeBytes, minAnalyzableTraceBytes)
	}
	return sizeBytes, true, ""
}

type commandOutput struct {
	output string
	err    error
}

func runOutput(serial string, args ...string) commandOutput {
	cmdArgs := append([]string{"-s", serial, "shell"}, args...)
	cmd := adb.Command(cmdArgs...)
	adb.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	return commandOutput{output: string(output), err: err}
}

func detectTracefs(serial string) (path string, readable bool, hasEntries bool) {
	for _, candidate := range []string{"/sys/kernel/tracing", "/sys/kernel/debug/tracing", "/d/tracing"} {
		check := runOutput(serial, "sh", "-c", fmt.Sprintf("[ -d %s ] && echo exists && ls -A %s 2>/dev/null | head -n 1", candidate, candidate))
		lines := nonEmptyLines(check.output)
		if len(lines) == 0 || lines[0] != "exists" {
			continue
		}
		return candidate, true, len(lines) > 1
	}
	return "", false, false
}

func summarizeOutput(output string) string {
	lines := nonEmptyLines(output)
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > 4 {
		lines = lines[:4]
	}
	return strings.Join(lines, " | ")
}

func nonEmptyLines(output string) []string {
	rawLines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func containsAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func parseNamedTime(output, name string) int {
	re := regexp.MustCompile(`(?m)` + regexp.QuoteMeta(name) + `:\s*(-?\d+)`)
	match := re.FindStringSubmatch(output)
	if len(match) != 2 {
		return 0
	}
	value, _ := strconv.Atoi(match[1])
	return value
}

func parsePID(output string) string {
	re := regexp.MustCompile(`(?m)\bpid\s*:?\s*(\d+)\b|\bPID\s*:?\s*(\d+)\b|^\s*(\d+)\s*$`)
	matches := re.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return ""
	}
	last := matches[len(matches)-1]
	for i := 1; i < len(last); i++ {
		if last[i] != "" {
			return last[i]
		}
	}
	return ""
}
