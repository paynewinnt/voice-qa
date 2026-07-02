package tts

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	defaultEdgeVoice = "zh-CN-YunjianNeural"
	edgeEndpoint     = "wss://speech.platform.bing.com/consumer/speech/synthesize/readaloud/edge/v1"
	edgeToken        = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"
	edgeFormat       = "riff-24khz-16bit-mono-pcm"
)

// Engine is the common text-to-speech interface used by CLI and GUI flows.
type Engine interface {
	Synthesize(text, outputPath string) error
}

// VoiceInfo describes one selectable voice in the GUI.
type VoiceInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Gender string `json:"gender"`
	Engine string `json:"engine"`
}

// GetAvailableVoices returns the built-in voice choices.
func GetAvailableVoices() []VoiceInfo {
	return []VoiceInfo{
		{ID: "edge:zh-CN-YunjianNeural", Name: "云健 (播音男声)", Gender: "男", Engine: "Edge TTS"},
		{ID: "edge:zh-CN-XiaoxiaoNeural", Name: "晓晓 (标准女声)", Gender: "女", Engine: "Edge TTS"},
		{ID: "edge:zh-CN-XiaoyiNeural", Name: "晓伊 (甜美女声)", Gender: "女", Engine: "Edge TTS"},
		{ID: "edge:zh-CN-YunxiNeural", Name: "云希 (标准男声)", Gender: "男", Engine: "Edge TTS"},
		{ID: "edge:zh-CN-YunxiaNeural", Name: "云夏 (少年男声)", Gender: "男", Engine: "Edge TTS"},
		{ID: "edge:zh-CN-YunyangNeural", Name: "云扬 (新闻男声)", Gender: "男", Engine: "Edge TTS"},
		{ID: "edge:zh-CN-liaoning-XiaobeiNeural", Name: "晓北 (东北女声)", Gender: "女", Engine: "Edge TTS"},
		{ID: "edge:zh-CN-shaanxi-XiaoniNeural", Name: "晓妮 (陕西女声)", Gender: "女", Engine: "Edge TTS"},
		{ID: "piper:zh_CN-huayan-medium", Name: "Piper 中文女声", Gender: "女", Engine: "Piper"},
	}
}

// CreateEngine creates an engine from a voice ID such as edge:zh-CN-YunjianNeural.
func CreateEngine(voiceID string) Engine {
	voiceID = strings.TrimSpace(voiceID)
	if voiceID == "" {
		voiceID = "edge:" + defaultEdgeVoice
	}

	engineName, voiceName, ok := strings.Cut(voiceID, ":")
	if !ok {
		return NewEdge(voiceID)
	}

	switch strings.ToLower(strings.TrimSpace(engineName)) {
	case "piper":
		modelName := strings.TrimSpace(voiceName)
		if modelName == "" {
			modelName = "zh_CN-huayan-medium"
		}
		if !strings.HasSuffix(modelName, ".onnx") {
			modelName += ".onnx"
		}
		return NewPiper(FindModelPath(modelName))
	case "edge":
		fallthrough
	default:
		if strings.TrimSpace(voiceName) == "" {
			voiceName = defaultEdgeVoice
		}
		return NewEdge(voiceName)
	}
}

// FindModelPath searches common release and development locations for a Piper model.
func FindModelPath(modelName string) string {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		modelName = "zh_CN-huayan-medium.onnx"
	}
	if filepath.IsAbs(modelName) {
		return modelName
	}

	var candidates []string
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		candidates = append(candidates,
			filepath.Join(exeDir, "models", modelName),
			filepath.Join(filepath.Dir(exeDir), "models", modelName),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, "models", modelName),
			filepath.Join(cwd, "..", "models", modelName),
		)
	}
	candidates = append(candidates, filepath.Join("models", modelName), modelName)

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return filepath.Join("models", modelName)
}

// Edge synthesizes speech with Microsoft Edge Read Aloud service.
type Edge struct {
	Voice string
}

func NewEdge(voice string) *Edge {
	voice = strings.TrimSpace(voice)
	if voice == "" {
		voice = defaultEdgeVoice
	}
	return &Edge{Voice: voice}
}

func (e *Edge) Synthesize(text, outputPath string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("文本不能为空")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil && filepath.Dir(outputPath) != "." {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := dialEdge(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	requestID := newConnectionID()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(edgeSpeechConfigMessage(requestID))); err != nil {
		return fmt.Errorf("发送 Edge TTS 配置失败: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(edgeSSMLMessage(requestID, e.Voice, text))); err != nil {
		return fmt.Errorf("发送 Edge TTS 文本失败: %w", err)
	}

	var audio bytes.Buffer
	for {
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetReadDeadline(deadline)
		}
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("读取 Edge TTS 响应失败: %w", err)
		}

		switch messageType {
		case websocket.BinaryMessage:
			payload := edgeAudioPayload(data)
			if len(payload) > 0 {
				audio.Write(payload)
			}
		case websocket.TextMessage:
			if strings.Contains(string(data), "Path:turn.end") {
				if audio.Len() == 0 {
					return fmt.Errorf("Edge TTS 未返回音频数据")
				}
				return os.WriteFile(outputPath, audio.Bytes(), 0644)
			}
		}
	}
}

func dialEdge(ctx context.Context) (*websocket.Conn, error) {
	values := url.Values{}
	values.Set("TrustedClientToken", edgeToken)
	values.Set("ConnectionId", newConnectionID())

	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		Proxy:            http.ProxyFromEnvironment,
	}
	conn, _, err := dialer.DialContext(ctx, edgeEndpoint+"?"+values.Encode(), http.Header{
		"Origin":     []string{"chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold"},
		"User-Agent": []string{"Mozilla/5.0"},
	})
	if err != nil {
		return nil, fmt.Errorf("连接 Edge TTS 失败: %w", err)
	}
	return conn, nil
}

func edgeSpeechConfigMessage(requestID string) string {
	body := fmt.Sprintf(`{"context":{"synthesis":{"audio":{"metadataoptions":{"sentenceBoundaryEnabled":"false","wordBoundaryEnabled":"false"},"outputFormat":%q}}}}`, edgeFormat)
	return edgeMessage("speech.config", requestID, "application/json; charset=utf-8", body)
}

func edgeSSMLMessage(requestID, voice, text string) string {
	body := fmt.Sprintf(`<speak version="1.0" xmlns="http://www.w3.org/2001/10/synthesis" xml:lang="zh-CN"><voice name="%s">%s</voice></speak>`,
		xmlEscape(voice),
		xmlEscape(text),
	)
	return edgeMessage("ssml", requestID, "application/ssml+xml", body)
}

func edgeMessage(path, requestID, contentType, body string) string {
	return fmt.Sprintf("X-RequestId:%s\r\nX-Timestamp:%s\r\nContent-Type:%s\r\nPath:%s\r\n\r\n%s",
		requestID,
		edgeTimestamp(),
		contentType,
		path,
		body,
	)
}

func edgeTimestamp() string {
	return time.Now().UTC().Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)")
}

func edgeAudioPayload(data []byte) []byte {
	if len(data) < 2 {
		return nil
	}
	headerLen := int(data[0])<<8 | int(data[1])
	start := 2 + headerLen
	if start < 0 || start > len(data) {
		return nil
	}
	return data[start:]
}

func xmlEscape(value string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

func newConnectionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Piper synthesizes speech by invoking the bundled piper binary.
type Piper struct {
	ModelPath string
	ExePath   string
}

func NewPiper(modelPath string) *Piper {
	return &Piper{ModelPath: modelPath, ExePath: findPiperExecutable()}
}

func (p *Piper) Synthesize(text, outputPath string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("文本不能为空")
	}
	if p.ModelPath == "" {
		return fmt.Errorf("Piper 模型路径为空")
	}
	if _, err := os.Stat(p.ModelPath); err != nil {
		return fmt.Errorf("Piper 模型不存在: %s", p.ModelPath)
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil && filepath.Dir(outputPath) != "." {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.ExePath, "--model", p.ModelPath, "--output_file", outputPath)
	cmd.Stdin = strings.NewReader(text)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("Piper 语音合成超时")
	}
	if err != nil {
		return fmt.Errorf("Piper 语音合成失败: %s %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func findPiperExecutable() string {
	name := "piper"
	if goruntime.GOOS == "windows" {
		name = "piper.exe"
	}

	var candidates []string
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		candidates = append(candidates,
			filepath.Join(exeDir, "ttsengine", name),
			filepath.Join(exeDir, "piper", name),
			filepath.Join(filepath.Dir(exeDir), "ttsengine", name),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, "ttsengine", name),
			filepath.Join(cwd, "bin", "piper", name),
			filepath.Join(cwd, "bin", "piper-windows", "piper", name),
			filepath.Join(cwd, "..", "bin", "piper", name),
			filepath.Join(cwd, "..", "bin", "piper-windows", "piper", name),
		)
	}
	candidates = append(candidates, name)

	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return name
}
