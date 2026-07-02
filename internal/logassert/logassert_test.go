package logassert

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAssertLogContentMatchesASRTextAndCompletedContent(t *testing.T) {
	log := `[2026-07-02 14:32:53.168] D HiAiSdk : {"eventType":"input_data.audio_result","data":{"data":{"results":[{"text":"今天杭州天气怎么样"}]}}}
[2026-07-02 14:32:53.343] D HiAiSdk : {"eventType":"input_data.completed","data":{"content":"今天杭州天气怎么样","type":"audio"}}`

	ok, info := assertTempLog(t, log, "今天杭州天气怎么样")
	if !ok {
		t.Fatalf("expected assertion to pass, got: %s", info)
	}
}

func TestAssertLogContentMatchesSkillSendContentWithoutNLPResult(t *testing.T) {
	log := `[2026-07-02 14:46:05.452] D YYZS-HiAiListener: 自定义类型: {"type":"skill_end","attr":{"data":{"send":{"type":"user","content":"绿萝黄叶是什么原因怎么养护"}}}}`

	ok, info := assertTempLog(t, log, "绿萝黄叶是什么原因怎么养护")
	if !ok {
		t.Fatalf("expected assertion to pass, got: %s", info)
	}
}

func TestAssertLogContentNormalizesEscapedUnicodeAndPunctuation(t *testing.T) {
	log := `[2026-07-02 14:32:54.024] D HiAiSdk : {"eventType":"input_data.completed","data":{"content":"\u4eca\u5929\u676d\u5dde\u5929\u6c14\u600e\u4e48\u6837"}}`

	ok, info := assertTempLog(t, log, "今天杭州天气怎么样？")
	if !ok {
		t.Fatalf("expected assertion to pass, got: %s", info)
	}
}

func assertTempLog(t *testing.T, content, expected string) (bool, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "play.log")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return AssertLogContent(path, expected)
}
