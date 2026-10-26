package performance

// Scenario describes one foreground interaction sampled during a performance run.
type Scenario struct {
	Name string `json:"name"`
	X    int    `json:"x"`
	Y    int    `json:"y"`
}

type Thresholds struct {
	CPUAveragePercent  float64 `json:"cpuAveragePercent"`
	CPUPeakPercent     float64 `json:"cpuPeakPercent"`
	PSSAverageMB       float64 `json:"pssAverageMb"`
	JankyFramesPercent float64 `json:"jankyFramesPercent"`
	FrameP90MS         float64 `json:"frameP90Ms"`
}

func DefaultThresholds() Thresholds {
	return Thresholds{
		CPUAveragePercent:  50,
		CPUPeakPercent:     120,
		PSSAverageMB:       120,
		JankyFramesPercent: 20,
		FrameP90MS:         24,
	}
}

type StartupSample struct {
	Group            string `json:"group,omitempty"`
	Label            string `json:"label,omitempty"`
	TotalTimeMS      int64  `json:"totalTimeMs"`
	ManualDurationMS int64  `json:"manualDurationMs"`
}

type Options struct {
	Package            string          `json:"package"`
	Devices            []string        `json:"devices"`
	Scenarios          []Scenario      `json:"scenarios"`
	Rounds             int             `json:"rounds"`
	SettleMilliseconds int             `json:"settleMilliseconds"`
	Thresholds         Thresholds      `json:"thresholds"`
	OutputRoot         string          `json:"-"`
	StartupSamples     []StartupSample `json:"-"`
}

type Progress struct {
	Stage          string  `json:"stage"`
	Message        string  `json:"message"`
	Device         string  `json:"device"`
	DeviceIndex    int     `json:"deviceIndex"`
	DeviceCount    int     `json:"deviceCount"`
	Sample         int     `json:"sample"`
	SamplesTotal   int     `json:"samplesTotal"`
	OverallPercent float64 `json:"overallPercent"`
}

type MetricStats struct {
	Count int     `json:"count"`
	Mean  float64 `json:"mean"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	P90   float64 `json:"p90"`
}

type PackageMetadata struct {
	VersionName      string `json:"versionName"`
	VersionCode      string `json:"versionCode"`
	MinSDK           string `json:"minSdk"`
	TargetSDK        string `json:"targetSdk"`
	UserID           int    `json:"userId"`
	FirstInstallTime string `json:"firstInstallTime"`
	LastUpdateTime   string `json:"lastUpdateTime"`
}

type DeviceMetadata struct {
	Serial           string          `json:"serial"`
	Manufacturer     string          `json:"manufacturer"`
	Model            string          `json:"model"`
	Android          string          `json:"android"`
	SDK              string          `json:"sdk"`
	BuildFingerprint string          `json:"buildFingerprint"`
	PhysicalSize     string          `json:"physicalSize"`
	Density          string          `json:"density"`
	Package          PackageMetadata `json:"package"`
	APKPath          string          `json:"apkPath"`
	APKSHA256        string          `json:"apkSha256"`
}

type MemorySnapshot struct {
	Available      bool  `json:"available"`
	PSSKB          int64 `json:"pssKb"`
	JavaHeapKB     int64 `json:"javaHeapKb"`
	NativeHeapKB   int64 `json:"nativeHeapKb"`
	CodeKB         int64 `json:"codeKb"`
	PrivateOtherKB int64 `json:"privateOtherKb"`
	SystemKB       int64 `json:"systemKb"`
	WebViews       int64 `json:"webViews"`
}

type ProcessStatus struct {
	Threads int64 `json:"threads"`
	VMRSSKB int64 `json:"vmrssKb"`
}

type Sample struct {
	Number             int     `json:"sample"`
	Round              int     `json:"round"`
	ScenarioIndex      int     `json:"scenarioIndex"`
	ScenarioName       string  `json:"scenarioName"`
	X                  int     `json:"x"`
	Y                  int     `json:"y"`
	Timestamp          string  `json:"timestamp"`
	ElapsedSeconds     float64 `json:"elapsedSeconds"`
	PID                int     `json:"pid"`
	TopCPUPercent      float64 `json:"topCpuPercent"`
	TopCPUAvailable    bool    `json:"topCpuAvailable"`
	IntervalCPUPercent float64 `json:"intervalCpuPercent"`
	IntervalAvailable  bool    `json:"intervalCpuAvailable"`
	TopMemoryPercent   float64 `json:"topMemoryPercent"`
	PSSKB              int64   `json:"pssKb"`
	JavaHeapKB         int64   `json:"javaHeapKb"`
	NativeHeapKB       int64   `json:"nativeHeapKb"`
	WebViews           int64   `json:"webViews"`
	Threads            int64   `json:"threads"`
	VMRSSKB            int64   `json:"vmrssKb"`
	ProcessAlive       bool    `json:"processAlive"`
	Foreground         bool    `json:"foreground"`
	TopLine            string  `json:"topLine"`
}

type ScenarioSummary struct {
	ScenarioIndex      int         `json:"scenarioIndex"`
	ScenarioName       string      `json:"scenarioName"`
	SampleCount        int         `json:"sampleCount"`
	TopCPUPercent      MetricStats `json:"topCpuPercent"`
	IntervalCPUPercent MetricStats `json:"intervalCpuPercent"`
	PSSKB              MetricStats `json:"pssKb"`
}

type SampleSummary struct {
	SampleCount         int               `json:"sampleCount"`
	TopCPUPercent       MetricStats       `json:"topCpuPercent"`
	IntervalCPUPercent  MetricStats       `json:"intervalCpuPercent"`
	PSSKB               MetricStats       `json:"pssKb"`
	Threads             MetricStats       `json:"threads"`
	VMRSSKB             MetricStats       `json:"vmrssKb"`
	ProcessAliveSamples int               `json:"processAliveSamples"`
	ForegroundSamples   int               `json:"foregroundSamples"`
	PerScenario         []ScenarioSummary `json:"perScenario"`
}

type GfxMetrics struct {
	Available           bool    `json:"available"`
	TotalFrames         int64   `json:"totalFrames"`
	JankyFrames         int64   `json:"jankyFrames"`
	JankyPercent        float64 `json:"jankyPercent"`
	P50MS               float64 `json:"p50Ms"`
	P90MS               float64 `json:"p90Ms"`
	P95MS               float64 `json:"p95Ms"`
	P99MS               float64 `json:"p99Ms"`
	MissedVsync         int64   `json:"missedVsync"`
	HighInputLatency    int64   `json:"highInputLatency"`
	SlowUIThread        int64   `json:"slowUiThread"`
	SlowBitmapUploads   int64   `json:"slowBitmapUploads"`
	SlowDrawCommands    int64   `json:"slowDrawCommands"`
	FrameDeadlineMissed int64   `json:"frameDeadlineMissed"`
}

type NetworkSnapshot struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	UID       int    `json:"uid"`
	Rows      int    `json:"rows"`
	RXBytes   int64  `json:"rxBytes"`
	TXBytes   int64  `json:"txBytes"`
}

type NetworkDelta struct {
	Available  bool   `json:"available"`
	Reason     string `json:"reason"`
	RXBytes    int64  `json:"rxBytes"`
	TXBytes    int64  `json:"txBytes"`
	TotalBytes int64  `json:"totalBytes"`
}

type LogAnalysis struct {
	UniqueLineCount             int     `json:"uniqueLineCount"`
	FatalExceptionCount         int     `json:"fatalExceptionCount"`
	AMCrashCount                int     `json:"amCrashCount"`
	ANRCount                    int     `json:"anrCount"`
	AudioFocusRequestCount      int     `json:"audioFocusRequestCount"`
	AudioFocusRequestsPerSecond float64 `json:"audioFocusRequestsPerSecond"`
	AudioFocusFirstLine         string  `json:"audioFocusFirstLine"`
	AudioFocusLastLine          string  `json:"audioFocusLastLine"`
}

type ScreenEvidence struct {
	Bytes    int64 `json:"bytes"`
	ValidPNG bool  `json:"validPng"`
	Width    int   `json:"width"`
	Height   int   `json:"height"`
}

type UIEvidence struct {
	Bytes           int64 `json:"bytes"`
	ContainsPackage bool  `json:"containsPackage"`
}

type DeviceResult struct {
	Serial            string          `json:"serial"`
	Slug              string          `json:"slug"`
	StartedAt         string          `json:"startedAt"`
	FinishedAt        string          `json:"finishedAt"`
	ElapsedSeconds    float64         `json:"elapsedSeconds"`
	Rounds            int             `json:"rounds"`
	ScenariosPerRound int             `json:"scenariosPerRound"`
	InitialPID        int             `json:"initialPid"`
	FinalPID          int             `json:"finalPid"`
	Metadata          DeviceMetadata  `json:"metadata"`
	ScreenBefore      ScreenEvidence  `json:"screenBefore"`
	ScreenAfter       ScreenEvidence  `json:"screenAfter"`
	UIBefore          UIEvidence      `json:"uiBefore"`
	UIAfter           UIEvidence      `json:"uiAfter"`
	MemoryBefore      MemorySnapshot  `json:"memoryBefore"`
	MemoryFinal       MemorySnapshot  `json:"memoryFinal"`
	Samples           SampleSummary   `json:"samples"`
	GfxInfo           GfxMetrics      `json:"gfxinfo"`
	Logcat            LogAnalysis     `json:"logcat"`
	NetworkBefore     NetworkSnapshot `json:"networkBefore"`
	NetworkAfter      NetworkSnapshot `json:"networkAfter"`
	NetworkDelta      NetworkDelta    `json:"networkDelta"`
	ResultDir         string          `json:"resultDir"`
}

type StartupSummary struct {
	Groups        []StartupGroup `json:"groups,omitempty"`
	SampleCount   int            `json:"sampleCount"`
	TotalTimeMS   MetricStats    `json:"totalTimeMs"`
	ManualTimeMS  MetricStats    `json:"manualTimeMs"`
	PrimaryMetric string         `json:"primaryMetric"`
}

type StartupGroup struct {
	Label         string      `json:"label"`
	SampleCount   int         `json:"sampleCount"`
	TotalTimeMS   MetricStats `json:"totalTimeMs"`
	ManualTimeMS  MetricStats `json:"manualTimeMs"`
	PrimaryMetric string      `json:"primaryMetric"`
}

type RunError struct {
	Serial  string `json:"serial"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

type Protocol struct {
	SchemaVersion           int        `json:"schemaVersion"`
	CreatedAt               string     `json:"createdAt"`
	Package                 string     `json:"package"`
	Devices                 []string   `json:"devices"`
	Rounds                  int        `json:"rounds"`
	Scenarios               []Scenario `json:"scenarios"`
	SettleMilliseconds      int        `json:"settleMilliseconds"`
	StartupCommandsExecuted bool       `json:"startupCommandsExecuted"`
	ForbiddenCommands       []string   `json:"forbiddenCommands"`
	Thresholds              Thresholds `json:"thresholds"`
}

type Result struct {
	Success       bool           `json:"success"`
	Stopped       bool           `json:"stopped"`
	OverallPassed bool           `json:"overallPassed"`
	Message       string         `json:"message"`
	ResultDir     string         `json:"resultDir"`
	ReportFile    string         `json:"reportFile"`
	Protocol      Protocol       `json:"protocol"`
	Startup       StartupSummary `json:"startup"`
	Results       []DeviceResult `json:"results"`
	Errors        []RunError     `json:"errors"`
}
