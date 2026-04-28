package perfetto

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"voice-qa/internal/adb"
)

type LogcatSession struct {
	Serial  string
	LogFile string
	ErrFile string
	cmd     *exec.Cmd
	outFile *os.File
	errFile *os.File
}

type TimelineEvent struct {
	Name     string `json:"name"`
	Time     string `json:"time"`
	OffsetMS int64  `json:"offsetMs"`
	Line     string `json:"line"`
}

type LogcatAnalysis struct {
	PackageName                  string          `json:"packageName"`
	Component                    string          `json:"component"`
	StartTime                    string          `json:"startTime"`
	ProcessStartOffsetMS         int64           `json:"processStartOffsetMs"`
	FirstAppLogOffsetMS          int64           `json:"firstAppLogOffsetMs"`
	DisplayedOffsetMS            int64           `json:"displayedOffsetMs"`
	ActivityPauseTimeoutOffsetMS int64           `json:"activityPauseTimeoutOffsetMs"`
	LaunchTimeoutOffsetMS        int64           `json:"launchTimeoutOffsetMs"`
	GCCount                      int             `json:"gcCount"`
	ActivityPauseTimeoutCount    int             `json:"activityPauseTimeoutCount"`
	LaunchTimeoutCount           int             `json:"launchTimeoutCount"`
	MiniSDKRegisterCount         int             `json:"miniSdkRegisterCount"`
	GlideNullModelCount          int             `json:"glideNullModelCount"`
	Milestones                   []TimelineEvent `json:"milestones"`
}

func StartLogcat(serial, resultDir string) (*LogcatSession, error) {
	if err := os.MkdirAll(resultDir, 0755); err != nil {
		return nil, err
	}
	clearCmd := adb.Command("-s", serial, "logcat", "-c")
	adb.HideWindow(clearCmd)
	if output, err := clearCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("清理 logcat 失败: %s %w", strings.TrimSpace(string(output)), err)
	}

	logFile := filepath.Join(resultDir, "logcat.txt")
	errFile := filepath.Join(resultDir, "logcat.err.txt")
	out, err := os.Create(logFile)
	if err != nil {
		return nil, fmt.Errorf("创建 logcat 文件失败: %w", err)
	}
	errOut, err := os.Create(errFile)
	if err != nil {
		_ = out.Close()
		return nil, fmt.Errorf("创建 logcat 错误文件失败: %w", err)
	}

	cmd := adb.Command("-s", serial, "logcat", "-v", "time")
	adb.HideWindow(cmd)
	cmd.Stdout = out
	cmd.Stderr = errOut
	if err := cmd.Start(); err != nil {
		_ = out.Close()
		_ = errOut.Close()
		return nil, fmt.Errorf("启动 logcat 失败: %w", err)
	}

	return &LogcatSession{
		Serial:  serial,
		LogFile: logFile,
		ErrFile: errFile,
		cmd:     cmd,
		outFile: out,
		errFile: errOut,
	}, nil
}

func (s *LogcatSession) Stop() error {
	if s == nil {
		return nil
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		done := make(chan error, 1)
		go func() {
			done <- s.cmd.Wait()
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
	if s.outFile != nil {
		_ = s.outFile.Sync()
		_ = s.outFile.Close()
	}
	if s.errFile != nil {
		_ = s.errFile.Sync()
		_ = s.errFile.Close()
	}
	return nil
}

func AnalyzeLogcatFile(path, packageName, component string) (LogcatAnalysis, error) {
	file, err := os.Open(path)
	if err != nil {
		return LogcatAnalysis{}, err
	}
	defer file.Close()
	return ParseLogcatTimeline(file, packageName, component, time.Now().Year()), nil
}

func ParseLogcatText(text, packageName, component string) LogcatAnalysis {
	return ParseLogcatTimeline(strings.NewReader(text), packageName, component, time.Now().Year())
}

func ParseLogcatTimeline(r io.Reader, packageName, component string, year int) LogcatAnalysis {
	analysis := LogcatAnalysis{
		PackageName: packageName,
		Component:   component,
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	var startTime time.Time
	for scanner.Scan() {
		line := scanner.Text()
		ts, ok := parseLogcatTime(line, year)
		if !ok {
			continue
		}
		if isStartLine(line, packageName, component) && startTime.IsZero() {
			startTime = ts
			analysis.StartTime = formatLogTime(ts)
			addMilestone(&analysis, "ActivityManager START", ts, startTime, line)
			continue
		}
		if startTime.IsZero() {
			continue
		}

		offset := ts.Sub(startTime).Milliseconds()
		if offset < -1000 {
			continue
		}
		lower := strings.ToLower(line)

		if analysis.ProcessStartOffsetMS == 0 && strings.Contains(line, packageName) && containsAny(line, "Start proc", "ProcessRecord") {
			analysis.ProcessStartOffsetMS = offset
			addMilestone(&analysis, "Process started", ts, startTime, line)
		}
		if analysis.FirstAppLogOffsetMS == 0 && strings.Contains(line, packageName) && !strings.Contains(line, "ActivityManager") && !strings.Contains(line, "ActivityTaskManager") {
			analysis.FirstAppLogOffsetMS = offset
			addMilestone(&analysis, "First app log", ts, startTime, line)
		}
		if analysis.DisplayedOffsetMS == 0 && strings.Contains(line, "Displayed") && (strings.Contains(line, component) || strings.Contains(line, packageName)) {
			analysis.DisplayedOffsetMS = offset
			addMilestone(&analysis, "Displayed", ts, startTime, line)
		}
		if containsAny(line, "Activity pause timeout") {
			analysis.ActivityPauseTimeoutCount++
			if analysis.ActivityPauseTimeoutOffsetMS == 0 {
				analysis.ActivityPauseTimeoutOffsetMS = offset
				addMilestone(&analysis, "Activity pause timeout", ts, startTime, line)
			}
		}
		if containsAny(line, "Launch timeout has expired") {
			analysis.LaunchTimeoutCount++
			if analysis.LaunchTimeoutOffsetMS == 0 {
				analysis.LaunchTimeoutOffsetMS = offset
				addMilestone(&analysis, "Launch timeout", ts, startTime, line)
			}
		}
		if strings.Contains(line, "registerActiveApp") {
			analysis.MiniSDKRegisterCount++
			if analysis.MiniSDKRegisterCount == 1 {
				addMilestone(&analysis, "MiniSDK registerActiveApp", ts, startTime, line)
			}
		}
		if strings.Contains(line, "GlideException: Received null model") {
			analysis.GlideNullModelCount++
			if analysis.GlideNullModelCount == 1 {
				addMilestone(&analysis, "Glide null model", ts, startTime, line)
			}
		}
		if strings.Contains(lower, "gc") && containsAny(line, "Explicit concurrent copying GC", "Background concurrent copying GC", "Clamp target GC heap") {
			analysis.GCCount++
			if analysis.GCCount == 1 {
				addMilestone(&analysis, "GC", ts, startTime, line)
			}
		}
	}
	return analysis
}

func SaveTimeline(resultDir string, analysis LogcatAnalysis) (string, error) {
	path := filepath.Join(resultDir, "timeline.json")
	data, err := json.MarshalIndent(analysis, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0644)
}

func SaveReport(resultDir string, analysis LogcatAnalysis) (string, error) {
	path := filepath.Join(resultDir, "report.txt")
	var b strings.Builder
	fmt.Fprintf(&b, "启动性能报告\n")
	fmt.Fprintf(&b, "Package: %s\n", analysis.PackageName)
	fmt.Fprintf(&b, "Component: %s\n", analysis.Component)
	fmt.Fprintf(&b, "Start: %s\n\n", analysis.StartTime)
	fmt.Fprintf(&b, "关键时间线:\n")
	for _, event := range analysis.Milestones {
		fmt.Fprintf(&b, "- %+d ms %s: %s\n", event.OffsetMS, event.Name, event.Line)
	}
	fmt.Fprintf(&b, "\n统计:\n")
	fmt.Fprintf(&b, "- Displayed: %d ms\n", analysis.DisplayedOffsetMS)
	fmt.Fprintf(&b, "- Process started: %d ms\n", analysis.ProcessStartOffsetMS)
	fmt.Fprintf(&b, "- First app log: %d ms\n", analysis.FirstAppLogOffsetMS)
	fmt.Fprintf(&b, "- GC count: %d\n", analysis.GCCount)
	fmt.Fprintf(&b, "- Activity pause timeout: %d\n", analysis.ActivityPauseTimeoutCount)
	fmt.Fprintf(&b, "- Launch timeout: %d\n", analysis.LaunchTimeoutCount)
	fmt.Fprintf(&b, "- MiniSDK registerActiveApp: %d\n", analysis.MiniSDKRegisterCount)
	fmt.Fprintf(&b, "- Glide null model: %d\n", analysis.GlideNullModelCount)
	return path, os.WriteFile(path, []byte(b.String()), 0644)
}

var logcatTimeRE = regexp.MustCompile(`^(\d{2})-(\d{2})\s+(\d{2}):(\d{2}):(\d{2})\.(\d{3})`)

func parseLogcatTime(line string, year int) (time.Time, bool) {
	match := logcatTimeRE.FindStringSubmatch(line)
	if len(match) != 7 {
		return time.Time{}, false
	}
	value := fmt.Sprintf("%04d-%s-%s %s:%s:%s.%s", year, match[1], match[2], match[3], match[4], match[5], match[6])
	t, err := time.ParseInLocation("2006-01-02 15:04:05.000", value, time.Local)
	return t, err == nil
}

func isStartLine(line, packageName, component string) bool {
	return (strings.Contains(line, "ActivityManager") || strings.Contains(line, "ActivityTaskManager")) &&
		strings.Contains(line, "START") &&
		(strings.Contains(line, component) || strings.Contains(line, packageName))
}

func addMilestone(analysis *LogcatAnalysis, name string, t, start time.Time, line string) {
	analysis.Milestones = append(analysis.Milestones, TimelineEvent{
		Name:     name,
		Time:     formatLogTime(t),
		OffsetMS: t.Sub(start).Milliseconds(),
		Line:     line,
	})
}

func formatLogTime(t time.Time) string {
	return t.Format("2006-01-02 15:04:05.000")
}
