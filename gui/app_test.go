package main

import (
	"bytes"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"voice-qa/internal/config"
	"voice-qa/internal/perfetto"
)

func TestAdbTargetArgDetection(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{name: "serial", args: []string{"-s", "device-1", "shell", "id"}, want: true},
		{name: "transport id", args: []string{"-t", "3", "shell", "id"}, want: true},
		{name: "usb shortcut", args: []string{"-d", "shell", "id"}, want: true},
		{name: "emulator shortcut", args: []string{"-e", "shell", "id"}, want: true},
		{name: "no target", args: []string{"shell", "id"}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasAdbTargetArg(tc.args); got != tc.want {
				t.Fatalf("hasAdbTargetArg(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestShouldAutoTargetAdbCommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{name: "shell is device scoped", args: []string{"shell", "pm", "list", "packages", "-3"}, want: true},
		{name: "install is device scoped", args: []string{"install", "app.apk"}, want: true},
		{name: "devices is global", args: []string{"devices"}, want: false},
		{name: "connect is global", args: []string{"connect", "127.0.0.1:5555"}, want: false},
		{name: "explicit serial still classifies command", args: []string{"-s", "device-1", "shell", "id"}, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAutoTargetAdbCommand(tc.args); got != tc.want {
				t.Fatalf("shouldAutoTargetAdbCommand(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestResolveAdbDeviceFromList(t *testing.T) {
	cases := []struct {
		name    string
		devices []string
		want    string
		wantErr bool
	}{
		{name: "no devices", wantErr: true},
		{name: "unique device", devices: []string{"device-1"}, want: "device-1"},
		{name: "multiple devices", devices: []string{"device-1", "device-2"}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveAdbDeviceFromList(tc.devices)
			if (err != nil) != tc.wantErr {
				t.Fatalf("resolveAdbDeviceFromList(%v) error = %v, wantErr %v", tc.devices, err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("resolveAdbDeviceFromList(%v) = %q, want %q", tc.devices, got, tc.want)
			}
		})
	}
}

func TestPrepareConfigForSave(t *testing.T) {
	if _, err := prepareConfigForSave(nil); err == nil {
		t.Fatal("prepareConfigForSave(nil) error = nil, want validation error")
	}

	cfg := config.DefaultConfig()
	cfg.TextFile = "  "
	cfg.OutputDir = ""

	got, err := prepareConfigForSave(cfg)
	if err != nil {
		t.Fatalf("prepareConfigForSave() error = %v", err)
	}
	if got.TextFile != "text.txt" || got.OutputDir != "output" {
		t.Fatalf("paths = %q, %q, want defaults", got.TextFile, got.OutputDir)
	}
	if cfg.TextFile != "  " || cfg.OutputDir != "" {
		t.Fatal("prepareConfigForSave mutated the input config")
	}

	invalidCases := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{name: "nil filename length", mutate: func(cfg *config.Config) { cfg.FileNameMaxLength = 0 }},
		{name: "negative screenshot", mutate: func(cfg *config.Config) { cfg.ScreenshotBeforeEnd = -1 }},
		{name: "nan recording delay", mutate: func(cfg *config.Config) { cfg.RecordingStartDelay = math.NaN() }},
		{name: "duplicate main", mutate: func(cfg *config.Config) {
			cfg.Template = append(cfg.Template, config.TemplateSegment{Type: "voice", Text: "$MAIN"})
		}},
		{name: "invalid segment type", mutate: func(cfg *config.Config) {
			cfg.Template[0].Type = "unknown"
		}},
		{name: "negative silence", mutate: func(cfg *config.Config) {
			cfg.Template[0] = config.TemplateSegment{Type: "silence", Seconds: -0.5}
		}},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			invalid := config.DefaultConfig()
			tc.mutate(invalid)
			if _, err := prepareConfigForSave(invalid); err == nil {
				t.Fatal("prepareConfigForSave() error = nil, want validation error")
			}
		})
	}
}

func TestGeneratePerfRecordsReportIncludesManualRecords(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "output")
	app := NewApp()
	app.cfg = config.DefaultConfig()
	app.cfg.OutputDir = outputDir

	perfRoot := filepath.Join(outputDir, "perf")
	resultDirs := make([]string, 0, 5)
	startedAt := time.Date(2026, time.August, 26, 10, 0, 0, 0, time.Local)
	for i := 0; i < 5; i++ {
		resultDir := filepath.Join(perfRoot, "launch-manual-"+strconv.Itoa(i+1))
		if err := os.MkdirAll(resultDir, 0755); err != nil {
			t.Fatalf("os.MkdirAll() error = %v", err)
		}
		duration := int64(1000 + i*100)
		run := perfetto.LaunchResult{
			Success:          true,
			Message:          "启动成功",
			Serial:           "device-1",
			Component:        "com.example/.MainActivity",
			PackageName:      "com.example",
			ManualDurationMS: duration,
			ResultDir:        resultDir,
			Timestamp:        startedAt.Format("2006-01-02 15:04:05.000"),
			StoppedAt:        startedAt.Add(time.Duration(duration) * time.Millisecond).Format("2006-01-02 15:04:05.000"),
			StopReason:       "manual",
		}
		if err := perfetto.SaveMetadata(resultDir, run); err != nil {
			t.Fatalf("perfetto.SaveMetadata() error = %v", err)
		}
		resultDirs = append(resultDirs, resultDir)
	}

	result := app.GeneratePerfRecordsReport(resultDirs, false, true)
	if !result.Success {
		t.Fatalf("GeneratePerfRecordsReport() = %+v, want success", result)
	}
	if filepath.Dir(result.ResultDir) != perfRoot || !strings.HasPrefix(filepath.Base(result.ResultDir), "aggregate-") {
		t.Fatalf("result directory = %q, want aggregate directory under %q", result.ResultDir, perfRoot)
	}
	if _, err := os.Stat(filepath.Join(result.ResultDir, "summary.json")); err != nil {
		t.Fatalf("summary.json was not generated: %v", err)
	}
	report, err := os.ReadFile(result.ReportFile)
	if err != nil {
		t.Fatalf("os.ReadFile(report) error = %v", err)
	}
	reportText := string(report)
	for _, want := range []string{
		"启动性能测试聚合报告",
		"测试轮数: 5",
		"通过“执行启动计时”开始测试",
		"以手动(ms)作为本次启动耗时的主要结论",
		"手动(ms)表示用户看到页面完全加载好并可以进行下一步操作所需的全部时间",
		"手动(ms)平均值: 1200.00 ms",
		"手动(ms)中位数: 1200 ms",
		"手动(ms) P90: 1360 ms",
		"手动(ms)最小/最大: 1000 / 1400 ms",
		"手动(ms)标准差: 141.42 ms",
		"手动(ms)波动系数: 11.8%",
	} {
		if !strings.Contains(reportText, want) {
			t.Fatalf("report does not contain %q:\n%s", want, reportText)
		}
	}
	if noMetrics := app.GeneratePerfRecordsReport(resultDirs, false, false); noMetrics.Success || !strings.Contains(noMetrics.Message, "至少选择一个报告统计项") {
		t.Fatalf("GeneratePerfRecordsReport() without metrics = %+v, want validation error", noMetrics)
	}
}

func TestBuildPerfAggregateReportPrioritizesSelectedManualMetric(t *testing.T) {
	runs := []perfetto.LaunchResult{
		{Success: true, TotalTimeMS: 1575, WaitTimeMS: 1595, ManualDurationMS: 14625},
		{Success: true, TotalTimeMS: 1640, WaitTimeMS: 1656, ManualDurationMS: 14819},
		{Success: true, TotalTimeMS: 1662, WaitTimeMS: 1664, ManualDurationMS: 15135},
	}
	batch := PerfBatchResult{Success: true, Successes: len(runs), Runs: runs, ResultDir: t.TempDir()}

	report := buildPerfAggregateReport(batch, filepath.Join(batch.ResultDir, "aggregate_report.txt"), perfReportMetrics{
		includeTotal:  true,
		includeManual: true,
	})
	for _, want := range []string{
		"以手动(ms)作为本次启动耗时的主要结论",
		"TotalTime/WaitTime 作为 Activity 启动到首帧阶段的辅助指标",
		"手动(ms)平均值: 14859.67 ms",
		"手动(ms)中位数: 14819 ms",
		"手动(ms) P90: 15072 ms",
		"手动(ms)最小/最大: 14625 / 15135 ms",
		"手动(ms)标准差: 210.18 ms",
		"手动(ms)波动系数: 1.4%",
		"TotalTime 平均值: 1625.67 ms",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report does not contain %q:\n%s", want, report)
		}
	}
	manualIndex := strings.Index(report, "手动(ms)平均值")
	totalIndex := strings.Index(report, "TotalTime 平均值")
	if manualIndex < 0 || totalIndex < 0 || manualIndex > totalIndex {
		t.Fatalf("manual summary must appear before TotalTime summary:\n%s", report)
	}

	totalOnlyReport := buildPerfAggregateReport(batch, filepath.Join(batch.ResultDir, "total_only_report.txt"), perfReportMetrics{
		includeTotal: true,
	})
	if !strings.Contains(totalOnlyReport, "使用 TotalTime 作为本次启动耗时的主要结论") {
		t.Fatalf("TotalTime-only report does not use TotalTime as its primary conclusion:\n%s", totalOnlyReport)
	}
	if strings.Contains(totalOnlyReport, "手动(ms)平均值") {
		t.Fatalf("TotalTime-only report unexpectedly contains manual summary:\n%s", totalOnlyReport)
	}
}

func TestGeneratePerfRecordsReportRejectsDirectoryOutsidePerfRoot(t *testing.T) {
	baseDir := t.TempDir()
	outputDir := filepath.Join(baseDir, "output")
	outsideDir := filepath.Join(baseDir, "outside")
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	if err := perfetto.SaveMetadata(outsideDir, perfetto.LaunchResult{Success: true, ManualDurationMS: 1000}); err != nil {
		t.Fatalf("perfetto.SaveMetadata() error = %v", err)
	}

	app := NewApp()
	app.cfg = config.DefaultConfig()
	app.cfg.OutputDir = outputDir
	result := app.GeneratePerfRecordsReport([]string{outsideDir}, true, true)
	if result.Success || !strings.Contains(result.Message, "不在启动性能输出目录内") {
		t.Fatalf("GeneratePerfRecordsReport() = %+v, want outside-root validation error", result)
	}
}

func TestTruncateInvalidLimitUsesDefault(t *testing.T) {
	got := truncate(strings.Repeat("字", defaultFileNameMaxLength+5), -1)
	if len([]rune(got)) != defaultFileNameMaxLength {
		t.Fatalf("truncate() length = %d, want %d", len([]rune(got)), defaultFileNameMaxLength)
	}
}

func TestDefaultUpdateURLUsesPublicCloudServer(t *testing.T) {
	parsed, err := url.Parse(defaultUpdateURL)
	if err != nil {
		t.Fatalf("url.Parse(defaultUpdateURL) error = %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "124.223.218.142" {
		t.Fatalf("defaultUpdateURL = %q, want public cloud HTTPS URL", defaultUpdateURL)
	}
	if parsed.Path != "/voice-qa/latest.json" {
		t.Fatalf("defaultUpdateURL path = %q, want cloud update manifest", parsed.Path)
	}

	sources := NewApp().GetUpdateSources()
	if len(sources) != 1 {
		t.Fatalf("GetUpdateSources() count = %d, want 1", len(sources))
	}
	if sources[0].URL != defaultUpdateURL || sources[0].Label != "公网云服务器" {
		t.Fatalf("GetUpdateSources() = %+v, want public cloud source", sources)
	}
}

func TestNewUpdateDownloadClientHasNoWholeDownloadTimeout(t *testing.T) {
	client := newUpdateDownloadClient()
	defer client.CloseIdleConnections()
	if client.Timeout != 0 {
		t.Fatalf("client.Timeout = %v, want no whole-download timeout", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client.Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout != updateHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout = %v, want %v", transport.ResponseHeaderTimeout, updateHeaderTimeout)
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 = true, want HTTP/1.1-compatible update downloads")
	}
}

func TestCopyUpdateBodyAllowsActiveDownloadPastIdleWindow(t *testing.T) {
	reader, writer := io.Pipe()
	go func() {
		defer writer.Close()
		for i := 0; i < 15; i++ {
			time.Sleep(25 * time.Millisecond)
			if _, err := writer.Write([]byte("x")); err != nil {
				return
			}
		}
	}()

	var output bytes.Buffer
	size, err := copyUpdateBody(&output, reader, 250*time.Millisecond)
	if err != nil {
		t.Fatalf("copyUpdateBody() error = %v", err)
	}
	if size != 15 || output.String() != strings.Repeat("x", 15) {
		t.Fatalf("copyUpdateBody() size/output = %d/%q, want 15 bytes", size, output.String())
	}
}

func TestCopyUpdateBodyReportsProgress(t *testing.T) {
	reader := io.NopCloser(strings.NewReader(strings.Repeat("x", 128*1024)))
	var reported []int64
	size, err := copyUpdateBodyWithProgress(io.Discard, reader, time.Second, func(downloaded int64) {
		reported = append(reported, downloaded)
	})
	if err != nil {
		t.Fatalf("copyUpdateBodyWithProgress() error = %v", err)
	}
	if len(reported) == 0 {
		t.Fatal("copyUpdateBodyWithProgress() did not report progress")
	}
	if reported[len(reported)-1] != size {
		t.Fatalf("last reported progress = %d, want copied size %d", reported[len(reported)-1], size)
	}
	for i := 1; i < len(reported); i++ {
		if reported[i] <= reported[i-1] {
			t.Fatalf("reported progress is not increasing: %v", reported)
		}
	}
}

func TestCopyUpdateBodyStopsAfterIdleTimeout(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()

	started := time.Now()
	_, err := copyUpdateBody(io.Discard, reader, 50*time.Millisecond)
	if !errors.Is(err, errUpdateDownloadIdleTimeout) {
		t.Fatalf("copyUpdateBody() error = %v, want idle timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("copyUpdateBody() elapsed = %v, want prompt idle cancellation", elapsed)
	}
}

func TestFetchUpdateInfoAndResolveRelativePackageURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.json":
			http.Redirect(w, r, "/assets/latest.json", http.StatusFound)
		case "/assets/latest.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"2026.0714.1800","notes":"test","url":"voice-qa.zip","sha256":"abc","history":[{"version":"2026.0714.1800","date":"2026-07-14","notes":["新增更新历史","  "]},{"version":"","date":"2026-07-13","notes":["忽略无版本记录"]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	manifestURL := server.URL + "/latest.json"
	info, err := fetchUpdateInfo(manifestURL)
	if err != nil {
		t.Fatalf("fetchUpdateInfo() error = %v", err)
	}
	if info.Version != "2026.0714.1800" || info.URL != "voice-qa.zip" {
		t.Fatalf("fetchUpdateInfo() = %+v", info)
	}
	if len(info.History) != 1 || info.History[0].Version != "2026.0714.1800" || len(info.History[0].Notes) != 1 {
		t.Fatalf("fetchUpdateInfo() history = %+v", info.History)
	}

	got, err := resolveUpdateURL(server.URL+"/assets/latest.json", info.URL)
	if err != nil {
		t.Fatalf("resolveUpdateURL() error = %v", err)
	}
	want := server.URL + "/assets/voice-qa.zip"
	if got != want {
		t.Fatalf("resolveUpdateURL() = %q, want %q", got, want)
	}
}

func TestFetchUpdateInfoRejectsOversizedManifest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxUpdateManifestBytes+1)))
	}))
	defer server.Close()

	_, err := fetchUpdateInfo(server.URL)
	if err == nil || !strings.Contains(err.Error(), "版本清单过大") {
		t.Fatalf("fetchUpdateInfo() error = %v, want oversized manifest error", err)
	}
}

func TestResolveUpdateURLRejectsUnsupportedProtocol(t *testing.T) {
	for _, rawURL := range []string{"file:///tmp/update.zip", "ftp://example.com/update.zip", "javascript:alert(1)"} {
		if _, err := resolveUpdateURL("https://example.com/latest.json", rawURL); err == nil {
			t.Fatalf("resolveUpdateURL(%q) error = nil, want unsupported protocol error", rawURL)
		}
	}
}

func TestParseHeartbeatPopupLog(t *testing.T) {
	log := `07-03 16:54:41.343 10961 14880 I com.zjdx.neuralnexus: heartbeat json: {"devId":"864396b5a6bd4ae394d9e0be98629c47","type":1}
07-03 16:54:41.364 10961 14881 I com.zjdx.neuralnexus: ║ Request URL: https://kdzs.zjip.com/ehome/device/heartbeat
07-03 16:54:42.778 10961 10961 I com.zjdx.neuralnexus: newHeartBeat return data :{"data":{"type":"PONG","taskId":null},"errorCode":0,"errorMsg":"success"}
07-03 17:28:42.144 10961 15284 D pretty-Logger: │ ScheduledPopupService 参数信息 [悬浮弹窗]: 悬浮弹窗 已触发弹窗: 测试20260702交付任务2
07-03 17:28:42.157 10961 15284 D pretty-Logger: │ TAG 参数信息 [触发弹窗参数]: 悬浮弹窗 准备弹窗{"content":"测试20260702交付任务1测试20260702交付任务2","planId":74,"title":"测试20260702交付任务2"}
07-03 17:28:42.182 10961 15284 D pretty-Logger: │ ScheduledPopupService 参数信息 [悬浮弹窗]: 悬浮弹窗 scheduledPopupService checkScheduledPopups 触发定时弹窗: 测试20260702交付任务2 at Thu Jul 02 16:23:35 GMT+08:00 2026
07-03 17:28:42.191 10961 10961 D pretty-Logger: │ ADFloatWindowService 参数信息 [悬浮弹窗popplan createFloatWindow]: 创建悬浮弹窗===
07-03 17:28:42.204 10961 15284 D pretty-Logger: │ ScheduledPopupService 参数信息 [悬浮弹窗]: 悬浮弹窗 已安排下次弹窗检查: 2026-07-03 17:58:42
07-03 17:28:42.764 10961 10961 D pretty-Logger: │ ADFloatWindowService 参数信息 [悬浮弹窗popplan获取悬浮弹窗]: 悬浮弹窗已获取焦点
`

	heartbeats, popups, nextCheck := parseHeartbeatPopupLog(log)
	if len(heartbeats) != 2 {
		t.Fatalf("heartbeats = %d, want 2", len(heartbeats))
	}
	if heartbeats[0].Direction != "request" || heartbeats[0].DevID != "864396b5a6bd4ae394d9e0be98629c47" {
		t.Fatalf("unexpected heartbeat request: %+v", heartbeats[0])
	}
	if heartbeats[1].Direction != "response" || heartbeats[1].ResultType != "PONG" {
		t.Fatalf("unexpected heartbeat response: %+v", heartbeats[1])
	}
	if len(popups) != 1 {
		t.Fatalf("popups = %d, want 1", len(popups))
	}
	popup := popups[0]
	if popup.FocusTime != "07-03 17:28:42.764" || popup.CreateTime != "07-03 17:28:42.191" {
		t.Fatalf("unexpected popup times: %+v", popup)
	}
	if popup.Title != "测试20260702交付任务2" || popup.PlanID != "74" {
		t.Fatalf("unexpected popup payload: %+v", popup)
	}
	if nextCheck != "2026-07-03 17:58:42" {
		t.Fatalf("nextCheck = %q, want %q", nextCheck, "2026-07-03 17:58:42")
	}
}
