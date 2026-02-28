package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// TemplateSegment 模板片段
type TemplateSegment struct {
	Type    string  `json:"type"`              // 类型: "voice" 或 "silence"
	Text    string  `json:"text,omitempty"`    // 语音文本 (type=voice 时有效，$MAIN 表示主文本)
	Seconds float64 `json:"seconds,omitempty"` // 静音秒数 (type=silence 时有效)
}

// Config 配置结构
type Config struct {
	// 文件路径配置
	TextFile  string `json:"text_file"`  // 文本文件路径
	OutputDir string `json:"output_dir"` // 输出目录
	ModelFile string `json:"model_file"` // 模型文件路径（可选，留空自动查找）

	// TTS 引擎配置
	VoiceID string `json:"voice_id"` // 声音 ID，格式：engine:voice，如 edge:zh-CN-XiaoxiaoNeural

	// 语音模板配置 (新版本使用 Template，旧版本字段保留兼容)
	Template []TemplateSegment `json:"template,omitempty"` // 语音模板序列

	// 旧版配置字段 (保留向后兼容)
	PrefixText  string `json:"prefix_text,omitempty"`  // 开头语音文本
	SuffixText1 string `json:"suffix_text1,omitempty"` // 结尾语音文本1
	SuffixText2 string `json:"suffix_text2,omitempty"` // 结尾语音文本2

	// 旧版静音时间配置（秒）
	SilenceStart       float64 `json:"silence_start,omitempty"`        // 开始前静音
	SilenceAfterPrefix float64 `json:"silence_after_prefix,omitempty"` // 前缀后静音
	SilenceAfterMain   float64 `json:"silence_after_main,omitempty"`   // 主文本后静音
	SilenceAfterSuffix float64 `json:"silence_after_suffix,omitempty"` // 后缀1后静音
	SilenceEnd         float64 `json:"silence_end,omitempty"`          // 结束后静音

	// 播放模式配置
	ScreenshotBeforeEnd float64 `json:"screenshot_before_end"` // 结束前多少秒截图

	// 视频录制配置
	EnableVideoRecording   bool    `json:"enable_video_recording"`    // 是否启用视频录制
	RecordingStartDelay    float64 `json:"recording_start_delay"`     // 音频开始后多少秒开始录制
	RecordingEndBeforeEnd  float64 `json:"recording_end_before_end"`  // 音频结束前多少秒停止录制

	// 文件名配置
	FileNameMaxLength int `json:"filename_max_length"` // 文件名最大字符数
}

// DefaultConfig 默认配置
func DefaultConfig() *Config {
	return &Config{
		TextFile:  "text.txt",
		OutputDir: "output",
		ModelFile: "",
		VoiceID:   "edge:zh-CN-YunjianNeural", // 默认使用 Edge TTS 播音男声

		// 新版模板配置
		Template: []TemplateSegment{
			{Type: "silence", Seconds: 1},
			{Type: "voice", Text: "小翼管家"},
			{Type: "silence", Seconds: 2},
			{Type: "voice", Text: "$MAIN"},
			{Type: "silence", Seconds: 18},
			{Type: "voice", Text: "小翼管家"},
			{Type: "silence", Seconds: 2},
			{Type: "voice", Text: "返回首页"},
			{Type: "silence", Seconds: 5},
		},

		ScreenshotBeforeEnd: 12.0,

		// 视频录制默认配置
		EnableVideoRecording:  false, // 默认关闭视频录制
		RecordingStartDelay:   5.0,   // 音频开始后5秒开始录制
		RecordingEndBeforeEnd: 5.0,   // 音频结束前5秒停止录制

		FileNameMaxLength: 30,
	}
}

// GetTemplate 获取模板（兼容旧配置）
func (c *Config) GetTemplate() []TemplateSegment {
	// 如果有新版模板，直接返回
	if len(c.Template) > 0 {
		return c.Template
	}

	// 兼容旧版配置，转换为模板格式
	var template []TemplateSegment

	if c.SilenceStart > 0 {
		template = append(template, TemplateSegment{Type: "silence", Seconds: c.SilenceStart})
	}

	if c.PrefixText != "" {
		template = append(template, TemplateSegment{Type: "voice", Text: c.PrefixText})
		if c.SilenceAfterPrefix > 0 {
			template = append(template, TemplateSegment{Type: "silence", Seconds: c.SilenceAfterPrefix})
		}
	}

	template = append(template, TemplateSegment{Type: "voice", Text: "$MAIN"})

	if c.SilenceAfterMain > 0 {
		template = append(template, TemplateSegment{Type: "silence", Seconds: c.SilenceAfterMain})
	}

	if c.SuffixText1 != "" {
		template = append(template, TemplateSegment{Type: "voice", Text: c.SuffixText1})
		if c.SilenceAfterSuffix > 0 {
			template = append(template, TemplateSegment{Type: "silence", Seconds: c.SilenceAfterSuffix})
		}
	}

	if c.SuffixText2 != "" {
		template = append(template, TemplateSegment{Type: "voice", Text: c.SuffixText2})
	}

	if c.SilenceEnd > 0 {
		template = append(template, TemplateSegment{Type: "silence", Seconds: c.SilenceEnd})
	}

	return template
}

// Load 从文件加载配置
func Load(configPath string) (*Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	return cfg, nil
}

// Save 保存配置到文件
func (c *Config) Save(configPath string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("写入配置文件失败: %w", err)
	}

	return nil
}

// FindConfigFile 查找配置文件
// 优先查找程序同目录下的 config.json
func FindConfigFile() string {
	// 1. 程序所在目录
	if exePath, err := os.Executable(); err == nil {
		configPath := filepath.Join(filepath.Dir(exePath), "config.json")
		if _, err := os.Stat(configPath); err == nil {
			return configPath
		}
	}

	// 2. 当前工作目录
	if cwd, err := os.Getwd(); err == nil {
		configPath := filepath.Join(cwd, "config.json")
		if _, err := os.Stat(configPath); err == nil {
			return configPath
		}
	}

	return ""
}

// LoadOrCreate 加载配置，如果不存在则创建默认配置
func LoadOrCreate(configPath string) (*Config, error) {
	// 如果没有指定路径，尝试查找
	if configPath == "" {
		configPath = FindConfigFile()
	}

	// 如果找到了配置文件，加载它
	if configPath != "" {
		return Load(configPath)
	}

	// 否则返回默认配置
	return DefaultConfig(), nil
}
