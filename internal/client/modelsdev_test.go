package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseModelsDevBody(t *testing.T) {
	body := []byte(`{
		"anthropic/claude-opus-5": {"limit": {"context": 1000000, "output": 128000}},
		"openai/gpt-5": {"limit": {"context": 400000}},
		"empty/none": {"limit": {"context": 0}}
	}`)
	m := parseModelsDevBody(body)
	if len(m) != 2 {
		t.Fatalf("expect 2 entries with context>0, got %d", len(m))
	}
	if m["anthropic/claude-opus-5"].Limit.Context != 1000000 {
		t.Errorf("claude-opus-5 context = %d, want 1000000", m["anthropic/claude-opus-5"].Limit.Context)
	}
	if m["openai/gpt-5"].Limit.Context != 400000 {
		t.Errorf("gpt-5 context = %d, want 400000", m["openai/gpt-5"].Limit.Context)
	}
}

func TestStripLabPrefix(t *testing.T) {
	cases := map[string]string{
		"anthropic/claude-opus-5": "claude-opus-5",
		"OPENAI/GPT-5":            "gpt-5",
		"claude-opus-5":           "claude-opus-5",
		"":                        "",
	}
	for in, want := range cases {
		if got := stripLabPrefix(in); got != want {
			t.Errorf("stripLabPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLookupContextWindowOnlineFromDiskCache(t *testing.T) {
	dir := t.TempDir()
	// 预写一份落盘缓存，模拟已拉取过的场景（避免打网络）。
	type limitT struct {
		Context int64 `json:"context"`
		Output  int64 `json:"output"`
	}
	doc := modelsDevCacheDoc{
		FetchedAt: time.Now(),
		Models: map[string]modelsDevEntry{
			"anthropic/claude-opus-5": {Limit: limitT{Context: 1000000, Output: 128000}},
			"openai/gpt-5.6-luna":     {Limit: limitT{Context: 400000, Output: 128000}},
		},
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, modelsDevCacheFile), body, 0o644); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	// 重置内存缓存，强制走落盘。
	modelsDevMu.Lock()
	modelsDevMemCache = nil
	modelsDevMemAt = time.Time{}
	modelsDevMu.Unlock()

	// 命中带日期后缀的变体（候选匹配去日期后命中 claude-opus-5）。
	if got := lookupContextWindowOnline("claude-opus-5-20260101", dir); got != 1000000 {
		t.Errorf("lookup claude-opus-5-20260101 = %d, want 1000000", got)
	}
	// 命中 lab 前缀变体（去命名空间 anthropic. 后裸 id gpt-5.6-luna 命中 openai/gpt-5.6-luna）。
	if got := lookupContextWindowOnline("anthropic.gpt-5.6-luna", dir); got != 400000 {
		t.Errorf("lookup anthropic.gpt-5.6-luna = %d, want 400000", got)
	}
	// 未命中。
	if got := lookupContextWindowOnline("totally-unknown", dir); got != 0 {
		t.Errorf("lookup unknown = %d, want 0", got)
	}
}

func TestEnrichWithContextWindowFallback(t *testing.T) {
	dir := t.TempDir()
	type limitT struct {
		Context int64 `json:"context"`
		Output  int64 `json:"output"`
	}
	doc := modelsDevCacheDoc{
		FetchedAt: time.Now(),
		Models: map[string]modelsDevEntry{
			"deepseek/deepseek-v4": {Limit: limitT{Context: 256000}},
		},
	}
	body, _ := json.Marshal(doc)
	_ = os.WriteFile(filepath.Join(dir, modelsDevCacheFile), body, 0o644)
	modelsDevMu.Lock()
	modelsDevMemCache = nil
	modelsDevMemAt = time.Time{}
	modelsDevMu.Unlock()

	models := []FetchedModel{
		{ID: "claude-opus-4-8"},        // 静态表命中（1M）
		{ID: "deepseek-v4"},            // 静态表 miss → 在线命中（256K）
		{ID: "totally-unknown-model"},  // 都 miss → 0
	}
	enrichWithContextWindow(models, dir)
	if models[0].ContextWindowTokens != 1000000 {
		t.Errorf("claude-opus-4-8 = %d, want 1000000 (static)", models[0].ContextWindowTokens)
	}
	if models[1].ContextWindowTokens != 256000 {
		t.Errorf("deepseek-v4 = %d, want 256000 (online)", models[1].ContextWindowTokens)
	}
	if models[2].ContextWindowTokens != 0 {
		t.Errorf("unknown = %d, want 0", models[2].ContextWindowTokens)
	}
}
