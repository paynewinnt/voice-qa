package logassert

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"
)

// AssertLogContent checks whether a play-test log contains the expected user query.
func AssertLogContent(logFile, expectedQuery string) (bool, string) {
	data, err := os.ReadFile(logFile)
	if err != nil {
		return false, fmt.Sprintf("无法读取日志文件: %v", err)
	}

	content := string(data)
	expected := normalizeForMatch(expectedQuery)
	if expected == "" {
		return false, "断言文本为空"
	}

	if ok, lineNo, source := findStructuredMatch(content, expected); ok {
		return true, fmt.Sprintf("找到用户问题匹配: %s（第 %d 行，来源: %s）", strings.TrimSpace(expectedQuery), lineNo, source)
	}

	if strings.Contains(normalizeForMatch(content), expected) {
		return true, fmt.Sprintf("找到文本匹配: %s（未识别到结构化字段）", strings.TrimSpace(expectedQuery))
	}

	switch {
	case containsAny(content, `"nlpResult"`, "nlpResult"):
		return false, fmt.Sprintf("日志包含 nlpResult，但未找到用户问题 \"%s\"；已同时检查 query/text/content/asr 字段", strings.TrimSpace(expectedQuery))
	case containsAny(content, "input_data.audio_result", "input_data.completed", "AudioResultData", "HiainputCompletedEntity"):
		return false, fmt.Sprintf("日志包含语音识别/输入事件，但未匹配到用户问题 \"%s\"", strings.TrimSpace(expectedQuery))
	default:
		return false, fmt.Sprintf("日志中未找到用户问题 \"%s\" 相关内容", strings.TrimSpace(expectedQuery))
	}
}

func findStructuredMatch(content, expected string) (bool, int, string) {
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		normalizedLine := normalizeForMatch(line)
		if !strings.Contains(normalizedLine, expected) {
			continue
		}
		if source := assertionSource(line); source != "" {
			return true, lineNo, source
		}
	}
	return false, 0, ""
}

func assertionSource(line string) string {
	switch {
	case containsAny(line, `"query"`, `query=`, `query:`):
		return "query"
	case containsAny(line, `"text"`, `text=`, "AudioResultData", "input_data.audio_result"):
		return "text/asr"
	case containsAny(line, `"content"`, `content=`, `content='`, "input_data.completed", "HiainputCompletedEntity"):
		return "content"
	case containsAny(line, "show asr:", "startDisplay"):
		return "asr/display"
	case containsAny(line, "intent_event", "skill_end", "send", "type=\"user\"", `"type":"user"`):
		return "intent/send"
	default:
		return ""
	}
}

func normalizeForMatch(value string) string {
	value = decodeUnicodeEscapes(value)
	value = strings.ToLower(value)
	var b strings.Builder
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) || isIgnorable(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func decodeUnicodeEscapes(value string) string {
	if !strings.Contains(value, `\u`) && !strings.Contains(value, `\\u`) {
		return value
	}

	for strings.Contains(value, `\\u`) {
		value = strings.ReplaceAll(value, `\\u`, `\u`)
	}

	runes := []rune(value)
	var b strings.Builder
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' && i+5 < len(runes) && runes[i+1] == 'u' {
			hexValue := string(runes[i+2 : i+6])
			code, err := strconv.ParseInt(hexValue, 16, 32)
			if err == nil {
				b.WriteRune(rune(code))
				i += 5
				continue
			}
		}
		b.WriteRune(runes[i])
	}
	return b.String()
}

func isIgnorable(r rune) bool {
	switch r {
	case '\ufeff', '\u200b', '\u200c', '\u200d':
		return true
	default:
		return false
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
