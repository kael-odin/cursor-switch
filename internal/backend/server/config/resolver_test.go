package config

import (
	"testing"
)

// adapterForTest 构造一个最小可用的 ModelAdapterConfig（通过 Normalize 校验）。
func adapterForTest(displayName, modelID string, priority int, enabled *bool) ModelAdapterConfig {
	next := ModelAdapterConfig{
		DisplayName:     displayName,
		Type:            "openai",
		BaseURL:         "https://api.example.com",
		APIKey:          "sk-test-" + displayName,
		TooltipData:     displayName,
		ModelID:         modelID,
		ReasoningEffort: "medium",
		OpenAIEndpoint:  "/v1/chat/completions",
		Priority:        priority,
		Enabled:         enabled,
	}
	return next
}

func boolPtr(v bool) *bool { return &v }

func normalizeAdaptersForTest(t *testing.T, adapters []ModelAdapterConfig) []ModelAdapterConfig {
	t.Helper()
	out, err := NormalizeModelAdapterConfigs(adapters)
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs: %v", err)
	}
	return out
}

func TestResolveModelAdapterChannelsEmpty(t *testing.T) {
	adapters := normalizeAdaptersForTest(t, []ModelAdapterConfig{
		adapterForTest("a1", "gpt-5", 0, nil),
	})
	got, err := resolveModelAdapterChannels(adapters, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %d channels", len(got))
	}
}

func TestResolveModelAdapterChannelsReturnsAllCandidates(t *testing.T) {
	adapters := normalizeAdaptersForTest(t, []ModelAdapterConfig{
		adapterForTest("a1", "gpt-5", 0, nil),
		adapterForTest("a2", "gpt-5", 0, nil),
		adapterForTest("a3", "claude", 0, nil),
	})
	got, err := resolveModelAdapterChannels(adapters, "gpt-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates for gpt-5, got %d", len(got))
	}
	if got[0].Name != "a1" || got[1].Name != "a2" {
		t.Errorf("candidate names = %q,%q; want a1,a2", got[0].Name, got[1].Name)
	}
}

func TestResolveModelAdapterChannelsSortByPriority(t *testing.T) {
	adapters := normalizeAdaptersForTest(t, []ModelAdapterConfig{
		adapterForTest("primary", "gpt-5", 10, nil),
		adapterForTest("backup", "gpt-5", 1, nil),
		adapterForTest("middle", "gpt-5", 5, nil),
	})
	got, err := resolveModelAdapterChannels(adapters, "gpt-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(got))
	}
	// Priority 升序：backup(1) → middle(5) → primary(10)
	want := []string{"backup", "middle", "primary"}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("channel[%d].Name = %q, want %q", i, got[i].Name, w)
		}
	}
}

func TestResolveModelAdapterChannelsPriorityTieKeepsConfigOrder(t *testing.T) {
	// 同 priority 时保持配置顺序（稳定排序）。
	adapters := normalizeAdaptersForTest(t, []ModelAdapterConfig{
		adapterForTest("first", "gpt-5", 0, nil),
		adapterForTest("second", "gpt-5", 0, nil),
		adapterForTest("third", "gpt-5", 0, nil),
	})
	got, err := resolveModelAdapterChannels(adapters, "gpt-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(got))
	}
	want := []string{"first", "second", "third"}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("channel[%d].Name = %q, want %q (stable order)", i, got[i].Name, w)
		}
	}
}

func TestResolveModelAdapterChannelsFiltersDisabled(t *testing.T) {
	adapters := normalizeAdaptersForTest(t, []ModelAdapterConfig{
		adapterForTest("enabled1", "gpt-5", 0, nil),
		adapterForTest("disabled", "gpt-5", 0, boolPtr(false)),
		adapterForTest("enabled2", "gpt-5", 0, boolPtr(true)),
	})
	got, err := resolveModelAdapterChannels(adapters, "gpt-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 enabled candidates, got %d", len(got))
	}
	for _, ch := range got {
		if ch.Name == "disabled" {
			t.Errorf("disabled adapter should not be in candidate list")
		}
	}
}

func TestResolveModelAdapterChannelsAllDisabledReturnsEmpty(t *testing.T) {
	adapters := normalizeAdaptersForTest(t, []ModelAdapterConfig{
		adapterForTest("a1", "gpt-5", 0, boolPtr(false)),
		adapterForTest("a2", "gpt-5", 0, boolPtr(false)),
	})
	got, err := resolveModelAdapterChannels(adapters, "gpt-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("all-disabled should return empty, got %d", len(got))
	}
}

func TestResolveModelAdapterChannelsMetaAlias(t *testing.T) {
	// auto/default/fast meta alias 取 adapters[0] 的 ID。
	adapters := normalizeAdaptersForTest(t, []ModelAdapterConfig{
		adapterForTest("first", "gpt-5", 0, nil),
		adapterForTest("second", "gpt-5", 0, nil),
	})
	got, err := resolveModelAdapterChannels(adapters, "auto")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// target 变成 "first"，只匹配第 0 个的 ID。
	if len(got) != 1 {
		t.Fatalf("meta alias should resolve to first adapter's ID only, got %d", len(got))
	}
	if got[0].Name != "first" {
		t.Errorf("meta alias = %q, want %q", got[0].Name, "first")
	}
}

func TestResolveModelAdapterChannelsBuildsResolvedFields(t *testing.T) {
	// 验证 buildResolvedChannel 字段映射（特别是 ContextWindowTokens/MaxTokens 覆盖）。
	src := ModelAdapterConfig{
		DisplayName:         "bigctx",
		Type:                "anthropic",
		BaseURL:             "https://api.anthropic.example.com",
		APIKey:              "sk-anthropic-key",
		TooltipData:         "bigctx",
		ModelID:             "claude-opus-5",
		ReasoningEffort:     "",
		OpenAIEndpoint:      "",
		ContextWindowTokens: 500_000,
		MaxCompletionTokens: 64_000,
		AnthropicMaxTokens:  100_000,
		ThinkingBudgetTokens: 32_000,
		AnthropicThinkingEffort: "high",
		Priority:            0,
	}
	// anthropic 不需要 ReasoningEffort/OpenAIEndpoint。
	adapters := normalizeAdaptersForTest(t, []ModelAdapterConfig{src})
	got, err := resolveModelAdapterChannels(adapters, "claude-opus-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	ch := got[0]
	if ch.ContextWindowTokens != 500_000 {
		t.Errorf("ContextWindowTokens = %d, want 500000", ch.ContextWindowTokens)
	}
	if ch.MaxTokens != 64_000 {
		t.Errorf("MaxTokens = %d, want 64000", ch.MaxTokens)
	}
	if ch.AnthropicMaxTokens != 100_000 {
		t.Errorf("AnthropicMaxTokens = %d, want 100000", ch.AnthropicMaxTokens)
	}
	if ch.ThinkingBudgetTokens != 32_000 {
		t.Errorf("ThinkingBudgetTokens = %d, want 32000", ch.ThinkingBudgetTokens)
	}
	if ch.AnthropicThinkingEffort != "high" {
		t.Errorf("AnthropicThinkingEffort = %q, want %q", ch.AnthropicThinkingEffort, "high")
	}
	if ch.Provider != "anthropic" {
		t.Errorf("Provider = %q, want anthropic", ch.Provider)
	}
	if ch.Model != "claude-opus-5" {
		t.Errorf("Model = %q, want claude-opus-5", ch.Model)
	}
	// ID 应该由 modelchannel.BuildChannelID 生成。
	if ch.ID == "" {
		t.Errorf("ID should be populated by BuildChannelID")
	}
	if ch.GroupName != "local" {
		t.Errorf("GroupName = %q, want local", ch.GroupName)
	}
}
