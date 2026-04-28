package perfetto

import (
	"strings"
	"testing"
)

func TestParseLogcatTimeline(t *testing.T) {
	text := strings.Join([]string{
		"04-28 10:00:00.000 I ActivityManager: START u0 {cmp=com.example/.MainActivity} from uid 2000",
		"04-28 10:00:00.120 I ActivityManager: Start proc 1234:com.example/u0a123 for activity",
		"04-28 10:00:00.200 I com.example: first app log",
		"04-28 10:00:00.500 I ActivityTaskManager: Displayed com.example/.MainActivity: +500ms",
		"04-28 10:00:00.600 I MiniSDK: registerActiveApp com.example",
		"04-28 10:00:00.700 W Glide: GlideException: Received null model",
		"04-28 10:00:00.800 I art: Explicit concurrent copying GC freed 1024",
		"04-28 10:00:01.000 W ActivityTaskManager: Activity pause timeout for ActivityRecord",
		"04-28 10:00:02.000 W ActivityTaskManager: Launch timeout has expired",
	}, "\n")

	analysis := ParseLogcatTimeline(strings.NewReader(text), "com.example", "com.example/.MainActivity", 2026)
	if analysis.StartTime == "" {
		t.Fatal("expected start time")
	}
	if analysis.ProcessStartOffsetMS != 120 {
		t.Fatalf("ProcessStartOffsetMS = %d, want 120", analysis.ProcessStartOffsetMS)
	}
	if analysis.FirstAppLogOffsetMS != 200 {
		t.Fatalf("FirstAppLogOffsetMS = %d, want 200", analysis.FirstAppLogOffsetMS)
	}
	if analysis.DisplayedOffsetMS != 500 {
		t.Fatalf("DisplayedOffsetMS = %d, want 500", analysis.DisplayedOffsetMS)
	}
	if analysis.ActivityPauseTimeoutOffsetMS != 1000 {
		t.Fatalf("ActivityPauseTimeoutOffsetMS = %d, want 1000", analysis.ActivityPauseTimeoutOffsetMS)
	}
	if analysis.LaunchTimeoutOffsetMS != 2000 {
		t.Fatalf("LaunchTimeoutOffsetMS = %d, want 2000", analysis.LaunchTimeoutOffsetMS)
	}
	if analysis.GCCount != 1 || analysis.ActivityPauseTimeoutCount != 1 || analysis.LaunchTimeoutCount != 1 {
		t.Fatalf("unexpected issue counts: %+v", analysis)
	}
	if analysis.MiniSDKRegisterCount != 1 || analysis.GlideNullModelCount != 1 {
		t.Fatalf("unexpected SDK/Glide counts: %+v", analysis)
	}
	if len(analysis.Milestones) == 0 {
		t.Fatal("expected milestones")
	}
}
