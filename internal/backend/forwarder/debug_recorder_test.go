package forwarder

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type stubDebugLogConfig struct{ enabled bool }

func (stub stubDebugLogConfig) IsObservabilityLogEnabled(context.Context) bool { return stub.enabled }

// TestSanitizeDebugEventRedactsSensitiveFields 验证 P0-3 递归脱敏：
// 任意嵌套深度的敏感字段（authorization/api_key/token/password/secret/cookie…）
// 值被替换为 ***，普通字段原样保留，token 用量计数（input_tokens 等）不误伤。
func TestSanitizeDebugEventRedactsSensitiveFields(t *testing.T) {
	input := map[string]any{
		"request_id":   "req-1",
		"message_count": 2,
		"headers": map[string]any{
			"authorization": "Bearer sk-real-secret-123",
			"x-api-key":     "key-abc",
			"content-type":  "application/json",
		},
		"session": map[string]any{
			"account": map[string]any{
				"client_secret": "super-secret",
				"password":      "p@ssw0rd",
			},
			"credentials": "super-secret-2",
		},
		"tools": []any{
			map[string]any{"name": "bash", "command": "echo hi"},
		},
		"summary": map[string]any{
			"input_tokens":  100,
			"output_tokens": 200,
			"cache_read_tokens": 50,
		},
		"plain": "keep me",
	}
	got := sanitizeDebugEvent(input)

	headers := got.(map[string]any)["headers"].(map[string]any)
	if headers["authorization"] != "***" {
		t.Errorf("authorization not redacted: %#v", headers["authorization"])
	}
	if headers["x-api-key"] != "***" {
		t.Errorf("x-api-key not redacted: %#v", headers["x-api-key"])
	}
	if headers["content-type"] != "application/json" {
		t.Errorf("content-type should be preserved: %#v", headers["content-type"])
	}
	account := got.(map[string]any)["session"].(map[string]any)["account"].(map[string]any)
	if account["client_secret"] != "***" || account["password"] != "***" {
		t.Errorf("nested secrets not redacted: %#v", account)
	}
	// "credentials" 本身是敏感字段名，整个子树应被替换为 ***。
	if got.(map[string]any)["session"].(map[string]any)["credentials"] != "***" {
		t.Errorf("credentials subtree should be redacted wholesale")
	}
	summary := got.(map[string]any)["summary"].(map[string]any)
	if summary["input_tokens"] != 100 || summary["output_tokens"] != 200 || summary["cache_read_tokens"] != 50 {
		t.Errorf("token usage counts must NOT be redacted: %#v", summary)
	}
	if got.(map[string]any)["plain"] != "keep me" {
		t.Errorf("plain field should be preserved")
	}
	// Bearer 值形态脱敏
	if redactSensitiveStringValue("Bearer abc.def") != "***" {
		t.Error("Bearer value should be redacted")
	}
	if redactSensitiveStringValue("just-a-plain-sentence") != "just-a-plain-sentence" {
		t.Error("plain string should be preserved")
	}
}

// TestAppendJSONLRedactsBeforeWrite 验证 appendJSONL 落盘内容已被脱敏（端到端）。
func TestAppendJSONLRedactsBeforeWrite(t *testing.T) {
	dir := t.TempDir()
	recorder := newDebugRecorder(dir, NewStreamBroker(), stubDebugLogConfig{enabled: true})
	recorder.appendJSONL(context.Background(), "req-1", "conv-1", "provider.jsonl", map[string]any{
		"event": "llm_request",
		"payload": map[string]any{
			"url":   "https://example.com/v1/chat",
			"body":  map[string]any{"api_key": "sk-leak-me"},
		},
	})
	raw, err := os.ReadFile(filepath.Join(dir, "conv-1", "debug", "provider.jsonl"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(raw), "sk-leak-me") {
		t.Fatalf("sensitive value leaked to disk: %s", raw)
	}
	if !strings.Contains(string(raw), "\"api_key\":\"***\"") {
		t.Fatalf("redaction marker missing: %s", raw)
	}
}

// TestAppendJSONLRotates 验证 P0-6 轮转：文件超过 debugLogFileCap 时滚动到 .1/.2/.3，
// 保留 3 份备份，新数据写入全新文件。
func TestAppendJSONLRotates(t *testing.T) {
	dir := t.TempDir()
	recorder := newDebugRecorder(dir, NewStreamBroker(), stubDebugLogConfig{enabled: true})
	orig := debugLogFileCap
	debugLogFileCap = 64
	t.Cleanup(func() { debugLogFileCap = orig })

	big := strings.Repeat("x", 200) // 单行超过上限，两次追加必然触发轮转
	recorder.appendJSONL(context.Background(), "req-1", "conv-1", "runsse.jsonl", map[string]any{"event": "first", "blob": big})
	recorder.appendJSONL(context.Background(), "req-1", "conv-1", "runsse.jsonl", map[string]any{"event": "second", "blob": big})
	recorder.appendJSONL(context.Background(), "req-1", "conv-1", "runsse.jsonl", map[string]any{"event": "third", "blob": big})
	recorder.appendJSONL(context.Background(), "req-1", "conv-1", "runsse.jsonl", map[string]any{"event": "fourth", "blob": big})

	base := filepath.Join(dir, "conv-1", "debug", "runsse.jsonl")
	current, err := os.ReadFile(base)
	if err != nil {
		t.Fatalf("read current log: %v", err)
	}
	back1, err := os.ReadFile(base + ".1")
	if err != nil {
		t.Fatalf("read backup .1: %v", err)
	}
	back2, err := os.ReadFile(base + ".2")
	if err != nil {
		t.Fatalf("read backup .2: %v", err)
	}
	back3, err := os.ReadFile(base + ".3")
	if err != nil {
		t.Fatalf("read backup .3: %v", err)
	}
	if !strings.Contains(string(current), "fourth") {
		t.Errorf("newest event should live in current file: %s", current)
	}
	if !strings.Contains(string(back1), "third") {
		t.Errorf("backup .1 should hold the third event: %s", back1)
	}
	if !strings.Contains(string(back2), "second") {
		t.Errorf("backup .2 should hold the second event: %s", back2)
	}
	if !strings.Contains(string(back3), "first") {
		t.Errorf("backup .3 should hold the first event: %s", back3)
	}
	// 轮转后当前文件应是全新文件：只含最新一条事件（单行可超过上限，用行数而非字节验证）。
	if strings.Count(string(current), "\n") != 1 {
		t.Errorf("current file should be freshly rotated with a single line, got %d lines", strings.Count(string(current), "\n"))
	}
	// 再追加一次应把 .3 挤掉、.2→.3，共 4 份（当前 + 3 备份）不增。
	recorder.appendJSONL(context.Background(), "req-1", "conv-1", "runsse.jsonl", map[string]any{"event": "fifth", "blob": big})
	entries, _ := filepath.Glob(base + ".*")
	if len(entries) != maxDebugBackupCount {
		t.Errorf("expected %d backups, got %d: %v", maxDebugBackupCount, len(entries), entries)
	}
}

// TestTruncateDebugDataHex 验证 bidi 原始请求体 hex 超限截断。
func TestTruncateDebugDataHex(t *testing.T) {
	long := strings.Repeat("ab", maxDebugDataHexLength)
	got := truncateDebugDataHex(long)
	if len(got) != maxDebugDataHexLength {
		t.Errorf("expected truncation to %d, got %d", maxDebugDataHexLength, len(got))
	}
	short := "ff00"
	if truncateDebugDataHex(short) != short {
		t.Error("short hex should be untouched")
	}
}

// TestSanitizeDebugEventEndToEndJSON 验证脱敏结果可被 JSON 序列化且形状稳定。
func TestSanitizeDebugEventEndToEndJSON(t *testing.T) {
	event := map[string]any{
		"layer":     "provider",
		"token":     "secret-token",
		"nested":    []any{map[string]any{"secret": "v"}, map[string]any{"ok": 1}},
	}
	_, err := json.Marshal(sanitizeDebugEvent(event))
	if err != nil {
		t.Fatalf("marshal sanitized event: %v", err)
	}
}
