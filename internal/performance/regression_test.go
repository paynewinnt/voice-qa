package performance

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"voice-qa/internal/adb"
)

func TestSparseCPUAndMemoryMustNotPass(t *testing.T) {
	for _, metric := range []string{"cpu", "pss"} {
		t.Run(metric, func(t *testing.T) {
			device := passingDeviceResult()
			if metric == "cpu" {
				device.Samples.TopCPUPercent.Count = 1
			} else {
				device.Samples.PSSKB.Count = 1
			}
			got := assessDevice(device, DefaultThresholds())
			if got.Passed || !strings.Contains(strings.Join(got.Issues, "\n"), "有效采样不足：1/4") {
				t.Fatalf("incomplete sampling accepted: %+v", got)
			}
		})
	}
	device := passingDeviceResult()
	device.Samples.TopCPUPercent = MetricStats{Count: 4}
	if !assessDevice(device, DefaultThresholds()).Passed {
		t.Fatal("valid zero CPU rejected")
	}
	device.Rounds, device.ScenariosPerRound = 4, 12
	if assessDevice(device, DefaultThresholds()).Passed {
		t.Fatal("incomplete planned scenarios passed")
	}
}

func TestGfxInfoRequiresFramesAndPercentile(t *testing.T) {
	for _, raw := range []string{
		"Total frames rendered: 0\nJanky frames: 0 (0.00%)\n90th percentile: 0ms",
		"Total frames rendered: 100\nJanky frames: 0 (0.00%)",
		"Total frames rendered: 100\n90th percentile: 16ms",
	} {
		if got := parseGfxInfo(raw); got.Available {
			t.Fatalf("invalid gfxinfo accepted: %+v", got)
		}
	}
	if got := parseGfxInfo("Total frames rendered: 100\nJanky frames: 0 (0.00%)\n90th percentile: 16ms"); !got.Available {
		t.Fatal("zero jank must remain valid")
	}
}

func TestStartupSummarySeparatesLaunchGroups(t *testing.T) {
	got := summarizeStartup([]StartupSample{
		{Group: "a", Label: "device a", TotalTimeMS: 1000, ManualDurationMS: 10000},
		{Group: "b", Label: "device b", TotalTimeMS: 9000, ManualDurationMS: 90000},
	})
	if len(got.Groups) != 2 || got.TotalTimeMS.Count != 0 || got.ManualTimeMS.Count != 0 {
		t.Fatalf("mixed average: %+v", got)
	}
	if got.Groups[0].ManualTimeMS.Mean != 10000 || got.Groups[1].ManualTimeMS.Mean != 90000 {
		t.Fatalf("wrong group stats: %+v", got.Groups)
	}
}

func TestFocusRequiresExactPackage(t *testing.T) {
	for _, focus := range []string{"mCurrentFocus=null", "mCurrentFocus=Window{a u0 com.example.other/.Main}", "mCurrentFocus=Window{a u0 org.com.example/.Main}"} {
		if focusBelongsToPackage(focus, "com.example") {
			t.Fatalf("wrong foreground accepted: %q", focus)
		}
	}
	if !focusBelongsToPackage("mCurrentFocus=Window{a u0 com.example/.Main}", "com.example") {
		t.Fatal("exact foreground rejected")
	}
}

func TestRunnerStopsTappingWhenAppLeavesForeground(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake adb uses POSIX shell")
	}
	dir := t.TempDir()
	fakeADB := filepath.Join(dir, "adb")
	script := `#!/bin/sh
shift 2
case "$*" in
  get-state) printf 'device\n';;
  'shell pidof '*) printf '42\n';;
  'shell dumpsys window'*)
    if [ -f "$FAKE_ADB_TAPS" ]; then
      printf 'mCurrentFocus=Window{a u0 com.launcher/.Home}\nmFocusedApp=ActivityRecord{b u0 com.example/.Main}\n'
    else
      printf 'mCurrentFocus=Window{a u0 com.example/.Main}\n'
    fi;;
  'shell input tap '*) printf 'tap\n' >> "$FAKE_ADB_TAPS";;
  'shell dumpsys meminfo '*) printf 'TOTAL: 10240\n';;
  'shell top '*) printf 'PID USER %%CPU %%MEM ARGS\n42 user 10 1 com.example\n';;
  'logcat -b all -v threadtime') exec /bin/sleep 60;;
esac
`
	if err := os.WriteFile(fakeADB, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	taps := filepath.Join(dir, "taps.txt")
	t.Setenv("FAKE_ADB_TAPS", taps)
	if adb.Command().Path != fakeADB {
		t.Fatal("test must not execute a real adb binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := Run(ctx, Options{Package: "com.example", Devices: []string{"fake-device"}, Rounds: 2, SettleMilliseconds: 200,
		Scenarios: []Scenario{{Name: "first", X: 10, Y: 10}, {Name: "must not tap", X: 20, Y: 20}}, OutputRoot: t.TempDir()}, nil)
	if result.Success || len(result.Errors) == 0 || !strings.Contains(result.Errors[0].Message, "停止后续点击") {
		t.Fatalf("foreground change not reported: %+v", result)
	}
	commands, err := os.ReadFile(taps)
	if err != nil {
		t.Fatal(err)
	}
	if string(commands) != "tap\n" {
		t.Fatalf("clicked after foreground loss: %q", commands)
	}
	if len(result.Results) != 1 || result.Results[0].Samples.SampleCount != 1 {
		t.Fatal("last sample not preserved")
	}
	if _, err := os.Stat(result.ReportFile); err != nil {
		t.Fatalf("partial report missing: %v", err)
	}
}
