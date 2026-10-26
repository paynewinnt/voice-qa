package performance

import (
	"encoding/binary"
	"testing"
)

func TestParseAndroidMetrics(t *testing.T) {
	t.Run("meminfo", func(t *testing.T) {
		raw := `
 App Summary
                       Pss(KB)                        Rss(KB)
                        ------                         ------
           Java Heap:    18520                          24500
         Native Heap:    22110                          28100
                Code:    10300                          28000
       Private Other:     5500
              System:    18000
               TOTAL:    74430                         126000
 Objects
               WebViews:        2
`
		got := parseMemorySnapshot(raw)
		if !got.Available || got.PSSKB != 74430 || got.JavaHeapKB != 18520 || got.NativeHeapKB != 22110 || got.WebViews != 2 {
			t.Fatalf("unexpected meminfo parse: %+v", got)
		}
	})

	t.Run("top", func(t *testing.T) {
		raw := `Tasks: 300 total
  PID USER         PR  NI VIRT  RES  SHR S %CPU %MEM     TIME+ ARGS
 9407 u0_a326      10 -10 3.8G 180M 100M S 37.5  4.2   0:12.00 com.zjdx.neuralnexus
`
		cpu, memory, line, ok := parseTop(raw, 9407, "com.zjdx.neuralnexus")
		if !ok || cpu != 37.5 || memory != 4.2 || line == "" {
			t.Fatalf("unexpected top parse: cpu=%v memory=%v ok=%v line=%q", cpu, memory, ok, line)
		}
	})

	t.Run("proc ticks", func(t *testing.T) {
		raw := "9407 (com.zjdx app) S 1 2 3 4 5 6 7 8 9 10 40 5 0 0 0"
		ticks, ok := parseProcTicks(raw)
		if !ok || ticks != 45 {
			t.Fatalf("unexpected proc ticks: %d, %v", ticks, ok)
		}
	})

	t.Run("gfxinfo", func(t *testing.T) {
		raw := `
Total frames rendered: 120
Janky frames: 18 (15.00%)
50th percentile: 8ms
90th percentile: 22ms
95th percentile: 30ms
99th percentile: 55ms
Number Missed Vsync: 4
Number High input latency: 2
Number Slow UI thread: 3
Number Slow bitmap uploads: 1
Number Slow issue draw commands: 2
Number Frame deadline missed: 5
`
		got := parseGfxInfo(raw)
		if !got.Available || got.TotalFrames != 120 || got.JankyFrames != 18 || got.JankyPercent != 15 || got.P90MS != 22 || got.FrameDeadlineMissed != 5 {
			t.Fatalf("unexpected gfxinfo parse: %+v", got)
		}
	})
}

func TestAnalyzeLogcatDeduplicatesAndFindsTargetFailures(t *testing.T) {
	raw := `08-26 12:00:00.000 E AndroidRuntime: FATAL EXCEPTION: main
08-26 12:00:00.001 E AndroidRuntime: Process: com.zjdx.neuralnexus, PID: 9407
08-26 12:00:01.000 I am_crash: [9407,com.zjdx.neuralnexus]
08-26 12:00:02.000 E ActivityManager: ANR in com.zjdx.neuralnexus
08-26 12:00:03.000 I AudioManager: requestAudioFocus package=com.zjdx.neuralnexus
08-26 12:00:03.000 I AudioManager: requestAudioFocus package=com.zjdx.neuralnexus
08-26 12:00:04.000 I AudioManager: requestAudioFocus package=com.zjdx.neuralnexus
`
	got := analyzeLogcat(raw, "com.zjdx.neuralnexus", 2)
	if got.FatalExceptionCount != 1 || got.AMCrashCount != 1 || got.ANRCount != 1 {
		t.Fatalf("failure counts are wrong: %+v", got)
	}
	if got.AudioFocusRequestCount != 2 || got.AudioFocusRequestsPerSecond != 1 {
		t.Fatalf("audio focus dedupe/rate is wrong: %+v", got)
	}
}

func TestPNGNormalizationAndEvidence(t *testing.T) {
	png := make([]byte, 33)
	copy(png, []byte("\x89PNG\r\n\x1a\n"))
	binary.BigEndian.PutUint32(png[8:12], 13)
	copy(png[12:16], []byte("IHDR"))
	binary.BigEndian.PutUint32(png[16:20], 1920)
	binary.BigEndian.PutUint32(png[20:24], 1080)
	prefixed := append([]byte("adb daemon message\r\n"), png...)
	normalized := normalizePNG(prefixed)
	evidence := pngEvidence(normalized)
	if !evidence.ValidPNG || evidence.Width != 1920 || evidence.Height != 1080 {
		t.Fatalf("unexpected png evidence: %+v", evidence)
	}
}

func TestSummarizeStartupPrefersManualTime(t *testing.T) {
	got := summarizeStartup([]StartupSample{
		{TotalTimeMS: 1600, ManualDurationMS: 14900},
		{TotalTimeMS: 1700, ManualDurationMS: 15100},
	})
	if got.PrimaryMetric != "manual" || got.ManualTimeMS.Mean != 15000 || got.TotalTimeMS.Mean != 1650 {
		t.Fatalf("unexpected startup summary: %+v", got)
	}
}
