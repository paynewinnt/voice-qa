package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"voice-qa/internal/audio"
	"voice-qa/internal/config"
	"voice-qa/internal/perfetto"
)

func TestManualMetricsExcludeAutomaticAndFailedRecords(t *testing.T) {
	runs := []perfetto.LaunchResult{
		{Success: true, StopReason: "manual", ManualDurationMS: 15000},
		{Success: true, StopReason: "batch", ManualDurationMS: 20000},
		{Success: true, StopReason: "timeout", ManualDurationMS: 30000},
		{Success: false, StopReason: "manual", ManualDurationMS: 40000},
		{Success: true, ManualDurationMS: 50000},
	}
	got := collectManualDurations(runs)
	if len(got) != 1 || got[0] != 15000 {
		t.Fatalf("manual durations = %v", got)
	}
	report := buildPerfAggregateReport(PerfBatchResult{Runs: runs[1:3]}, "report.txt", perfReportMetrics{includeTotal: true, includeManual: true})
	if strings.Contains(report, "以手动(ms)作为本次启动耗时的主要结论") || !strings.Contains(report, "手动(ms): 无有效数据") {
		t.Fatalf("automatic captures used as manual measurements:\n%s", report)
	}
}

func TestStopLaunchTraceSavesAuthoritativeDuration(t *testing.T) {
	for _, reason := range []string{"manual", "timeout"} {
		t.Run(reason, func(t *testing.T) {
			a := NewApp()
			release, err := a.beginStartupTask("device", "启动计时")
			if err != nil {
				t.Fatal(err)
			}
			a.perfStartedAt = time.Now().Add(-2 * time.Second)
			a.perfRelease = release
			a.perfResult = &perfetto.LaunchResult{
				Success: true, Serial: "device", ResultDir: t.TempDir(),
				Timestamp: a.perfStartedAt.Format("2006-01-02 15:04:05.000"),
			}
			stopped := a.stopLaunchTrace(reason)
			if !stopped.Success || stopped.Run == nil || stopped.Run.CaptureDurationMS < 2000 {
				t.Fatalf("invalid stop result: %+v", stopped)
			}
			if reason == "manual" && stopped.Run.ManualDurationMS != stopped.Run.CaptureDurationMS {
				t.Fatal("manual duration differs from backend capture clock")
			}
			if reason == "timeout" {
				if stopped.Run.ManualDurationMS != 0 {
					t.Fatal("timeout counted as manual")
				}
				concurrentManual := a.stopLaunchTrace("manual")
				if concurrentManual.Run == nil || concurrentManual.Reason != "timeout" {
					t.Fatal("manual stop racing with timeout lost the saved record")
				}
			}
			loadedRuns, err := loadPerfRecordMetadata(stopped.ResultDir, []string{stopped.ResultDir})
			if err != nil {
				t.Fatal(err)
			}
			loaded := loadedRuns[0]
			if loaded.ManualDurationMS != stopped.Run.ManualDurationMS || loaded.StopReason != reason {
				t.Fatalf("disk and UI result differ: %+v / %+v", loaded, stopped.Run)
			}
			if a.perfBusy {
				t.Fatal("startup lease not released")
			}
		})
	}
}

func TestStaleTimerDoesNotStopNewSession(t *testing.T) {
	a := NewApp()
	current := &perfetto.LaunchResult{Serial: "device"}
	a.perfResult = current
	stop := a.stopLaunchTraceFor(&perfetto.LaunchResult{}, "timeout")
	if stop.Run != nil || a.perfResult != current {
		t.Fatal("stale timer stopped new session")
	}
}

func TestDeviceTaskAcquisitionIsAtomicAndReleaseIdempotent(t *testing.T) {
	a := NewApp()
	release, err := a.acquireDeviceTasks([]string{"b"}, "综合性能测试")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := a.acquireDeviceTasks([]string{"a", "b"}, "启动测试"); err == nil {
		t.Fatal("busy device accepted")
	}
	releaseA, err := a.acquireDeviceTasks([]string{"a"}, "启动测试")
	if err != nil {
		t.Fatalf("partial acquisition leaked: %v", err)
	}
	releaseA()
	newReleaseA, err := a.acquireDeviceTasks([]string{"a"}, "新任务")
	if err != nil {
		t.Fatal(err)
	}
	defer newReleaseA()
	releaseA()
	if _, err := a.acquireDeviceTasks([]string{"a"}, "其他任务"); err == nil {
		t.Fatal("old release removed new owner")
	}
}

func TestConcurrentDeviceTaskAcquisitionHasOneOwner(t *testing.T) {
	a := NewApp()
	var wg sync.WaitGroup
	releases := make(chan func(), 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if release, err := a.acquireDeviceTasks([]string{"device"}, "test"); err == nil {
				releases <- release
			}
		}()
	}
	wg.Wait()
	close(releases)
	if len(releases) != 1 {
		t.Errorf("owners = %d, want 1", len(releases))
	}
	for release := range releases {
		release()
	}
}

func TestBusyPerformanceDeviceRejectsStateChangingOperations(t *testing.T) {
	a := NewApp()
	release, err := a.acquireDeviceTasks([]string{"device"}, "综合性能测试")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	verify := a.VerifyPackageLaunch("device", PerfLaunchTarget{Component: "com.example/.MainActivity"}, PackageLaunchVerificationOptions{ForceStop: true})
	if verify.Success || !strings.Contains(verify.Message, "综合性能测试") {
		t.Fatalf("verify not guarded: %+v", verify)
	}
	for _, result := range []AdbResult{
		a.PerfPrepColdStart("device", "com.example"),
		a.PerfForceStopAll("device"),
		a.PerfGoHome("device"),
		a.PerfRunControlCommands("device", "com.example", PerfControlCommands{ForceStop: true}),
	} {
		if result.Success || !strings.Contains(result.Message, "综合性能测试") {
			t.Fatalf("unguarded control: %+v", result)
		}
	}
}

func TestAggregateSeparatesDevicesAndLaunchConditions(t *testing.T) {
	base := perfetto.LaunchResult{Serial: "one", PackageName: "com.example", Component: "com.example/.MainActivity", Success: true, TotalTimeMS: 1000}
	otherDevice := base
	otherDevice.Serial = "two"
	otherDevice.TotalTimeMS = 9000
	otherIntent := base
	otherIntent.LaunchDataURI = "app://other"
	otherMode := base
	otherMode.ClearDataBefore = true
	runs := []perfetto.LaunchResult{base, otherDevice, otherIntent, otherMode}
	if got := len(groupPerfRecords(runs)); got != 4 {
		t.Fatalf("groups = %d", got)
	}
	report := buildPerfAggregateReport(PerfBatchResult{Runs: runs[:2]}, "report.txt", perfReportMetrics{includeTotal: true})
	for _, want := range []string{"分为 2 组", "TotalTime 平均值: 1000.00 ms", "TotalTime 平均值: 9000.00 ms"} {
		if !strings.Contains(report, want) {
			t.Fatalf("missing %q in report:\n%s", want, report)
		}
	}
	if strings.Contains(report, "5000.00 ms") {
		t.Fatal("mixed-device average")
	}
	canonical := base
	canonical.Component = "com.example/com.example.MainActivity"
	if perfRecordGroupKey(base) != perfRecordGroupKey(canonical) {
		t.Fatal("equivalent components split into groups")
	}
}

type cancelTestEngine struct {
	calls   int
	blocked chan struct{}
	blockAt int
}

func (e *cancelTestEngine) Synthesize(string, string) error {
	return errors.New("uncancellable path used")
}
func (e *cancelTestEngine) SynthesizeContext(ctx context.Context, text, path string) error {
	e.calls++
	if e.calls == e.blockAt {
		close(e.blocked)
		<-ctx.Done()
		return ctx.Err()
	}
	header := &audio.WAVHeader{ChunkID: [4]byte{'R', 'I', 'F', 'F'}, Format: [4]byte{'W', 'A', 'V', 'E'}, Subchunk1ID: [4]byte{'f', 'm', 't', ' '}, Subchunk1Size: 16, AudioFormat: 1, NumChannels: 1, SampleRate: 24000, ByteRate: 48000, BlockAlign: 2, BitsPerSample: 16, Subchunk2ID: [4]byte{'d', 'a', 't', 'a'}}
	return audio.WriteWAV(path, header, []byte{0, 0, 0, 0})
}

func TestGenerateCancellationKeepsCompletedManifest(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.OutputDir = t.TempDir()
	cfg.TextFile = filepath.Join(t.TempDir(), "text.txt")
	cfg.Template = []config.TemplateSegment{{Type: "voice", Text: "$MAIN"}}
	if err := os.WriteFile(cfg.TextFile, []byte("first complete text\nsecond blocked text\nthird skipped text"), 0600); err != nil {
		t.Fatal(err)
	}
	engine := &cancelTestEngine{blocked: make(chan struct{}), blockAt: 2}
	a := &App{cfg: cfg, ttsEngine: engine}
	done := make(chan struct{})
	go func() { defer close(done); a.runGenerateInBackground() }()
	defer func() { _ = a.StopGenerate(); <-done }()
	select {
	case <-engine.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("second synthesis did not start")
	}
	manifest, err := readPlayManifest(cfg.OutputDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 1 || manifest.Entries[0].Text != "first complete text" {
		t.Fatalf("completed mapping not saved: %+v", manifest)
	}
	if err := a.StopGenerate(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("synthesis did not cancel")
	}
	if engine.calls != 2 {
		t.Fatalf("synthesized after stop: %d", engine.calls)
	}
	if _, err := os.Stat(filepath.Join(cfg.OutputDir, manifest.Entries[0].WavFile)); err != nil {
		t.Fatal(err)
	}
	manifestAfter, err := readPlayManifest(cfg.OutputDir)
	if err != nil || len(manifestAfter.Entries) != 1 {
		t.Fatalf("manifest lost after cancellation: %v %+v", err, manifestAfter)
	}
}
