package performance

import (
	"strings"
	"testing"
)

func TestBuildMarkdownReportIncludesCoreSectionsAndEvidence(t *testing.T) {
	thresholds := DefaultThresholds()
	device := passingDeviceResult()
	result := Result{
		Success:       true,
		OverallPassed: true,
		Message:       "完成",
		ResultDir:     "/tmp/performance-1",
		ReportFile:    "/tmp/performance-1/performance_report.md",
		Protocol: Protocol{
			CreatedAt:          "2026-09-01T12:00:00+08:00",
			Package:            "com.zjdx.neuralnexus",
			Devices:            []string{"192.168.1.111:5555"},
			Rounds:             4,
			Scenarios:          []Scenario{{Name: "AI聊天", X: 116, Y: 122}},
			SettleMilliseconds: 1200,
			Thresholds:         thresholds,
		},
		Startup: summarizeStartup([]StartupSample{
			{TotalTimeMS: 1625, ManualDurationMS: 14860},
			{TotalTimeMS: 1640, ManualDurationMS: 15135},
		}),
		Results: []DeviceResult{device},
	}

	report := buildMarkdownReport(result, Options{Thresholds: thresholds})
	for _, expected := range []string{
		"# 应用综合性能测试报告",
		"## 1. 总体结论",
		"## 5. 启动性能",
		"手动（ms）表示用户看到页面完全加载",
		"## 6. CPU 表现",
		"## 8. 渲染与流畅度",
		"## 12. 证据与产物",
		"device-192.168.1.111-5555/samples.csv",
		"## 13. 限制与复测建议",
	} {
		if !strings.Contains(report, expected) {
			t.Fatalf("report does not contain %q\n%s", expected, report)
		}
	}
	if !evaluateOverall(result.Results, thresholds) {
		t.Fatal("passing fixture should pass overall evaluation")
	}
}

func TestEvaluateOverallRejectsMissingOrExceededMetrics(t *testing.T) {
	thresholds := DefaultThresholds()
	if evaluateOverall(nil, thresholds) {
		t.Fatal("empty results must not pass")
	}

	device := passingDeviceResult()
	device.Samples.TopCPUPercent.Mean = thresholds.CPUAveragePercent + 1
	if evaluateOverall([]DeviceResult{device}, thresholds) {
		t.Fatal("CPU threshold breach must not pass")
	}

	device = passingDeviceResult()
	device.GfxInfo.Available = false
	if evaluateOverall([]DeviceResult{device}, thresholds) {
		t.Fatal("missing gfx metrics must require review")
	}
}

func passingDeviceResult() DeviceResult {
	return DeviceResult{
		Serial:         "192.168.1.111:5555",
		Slug:           "192.168.1.111-5555",
		InitialPID:     9407,
		FinalPID:       9407,
		ElapsedSeconds: 60,
		Metadata: DeviceMetadata{
			Manufacturer: "Example",
			Model:        "TV",
			Android:      "13",
			SDK:          "33",
			PhysicalSize: "Physical size: 1920x1080",
			Density:      "Physical density: 320",
			APKSHA256:    strings.Repeat("a", 64),
			Package: PackageMetadata{
				VersionName: "2.4.32",
				VersionCode: "2432",
				UserID:      10326,
			},
		},
		MemoryBefore: MemorySnapshot{Available: true, PSSKB: 80 * 1024},
		MemoryFinal:  MemorySnapshot{Available: true, PSSKB: 85 * 1024, JavaHeapKB: 20 * 1024, NativeHeapKB: 25 * 1024},
		Samples: SampleSummary{
			SampleCount:         4,
			TopCPUPercent:       MetricStats{Count: 4, Mean: 20, Min: 10, Max: 40, P90: 35},
			IntervalCPUPercent:  MetricStats{Count: 4, Mean: 18, Min: 8, Max: 36, P90: 32},
			PSSKB:               MetricStats{Count: 4, Mean: 82 * 1024, Min: 80 * 1024, Max: 85 * 1024, P90: 84 * 1024},
			Threads:             MetricStats{Count: 4, Mean: 80, Min: 78, Max: 82, P90: 82},
			ProcessAliveSamples: 4,
			ForegroundSamples:   4,
			PerScenario: []ScenarioSummary{{
				ScenarioIndex: 1,
				ScenarioName:  "AI聊天",
				SampleCount:   4,
				TopCPUPercent: MetricStats{Count: 4, Mean: 20, Max: 40},
				PSSKB:         MetricStats{Count: 4, Mean: 82 * 1024},
			}},
		},
		GfxInfo:      GfxMetrics{Available: true, TotalFrames: 100, JankyFrames: 5, JankyPercent: 5, P50MS: 8, P90MS: 16, P95MS: 20, P99MS: 30},
		Logcat:       LogAnalysis{},
		NetworkDelta: NetworkDelta{Available: true, RXBytes: 1024, TXBytes: 2048, TotalBytes: 3072},
		ResultDir:    "/tmp/performance-1/device-192.168.1.111-5555",
	}
}
