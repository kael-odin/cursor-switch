package config

import "testing"

// TestNormalizeModelAdapterPreservesProviderLabel 验证 ProviderLabel 经 normalize 透传。
//
// providerLabel 是模型配置里新增的品牌标签（如 deepseek/qwen/glm），与 type（协议）独立。
// 用户填了 deepseek 走 openai 协议时，type=openai、providerLabel=deepseek，
// 使用统计 by-provider 表按 label 归类。normalize 必须原样保留（TrimSpace），不能清空。
func TestNormalizeModelAdapterPreservesProviderLabel(t *testing.T) {
	enabled := true
	input := []ModelAdapterConfig{
		{
			DisplayName:     "DeepSeek",
			Type:            "openai",
			ProviderLabel:   "  deepseek  ",
			BaseURL:         "https://api.deepseek.com",
			APIKey:          "sk-test-key",
			TooltipData:     "tip",
			ModelID:         "deepseek-chat",
			ReasoningEffort: "medium",
			OpenAIEndpoint:  "/v1/chat/completions",
			Enabled:         &enabled,
		},
	}
	got, err := NormalizeModelAdapterConfigs(input)
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 adapter, got %d", len(got))
	}
	if want := "deepseek"; got[0].ProviderLabel != want {
		t.Fatalf("ProviderLabel = %q, want %q (should be trimmed, not cleared)", got[0].ProviderLabel, want)
	}
	if got[0].Type != "openai" {
		t.Fatalf("Type = %q, want openai (label must not corrupt protocol type)", got[0].Type)
	}
}

// TestNormalizeModelAdapterEmptyProviderLabel 验证留空 label 不会报错也不会变成非空。
func TestNormalizeModelAdapterEmptyProviderLabel(t *testing.T) {
	enabled := true
	input := []ModelAdapterConfig{
		{
			DisplayName:     "PlainOpenAI",
			Type:            "openai",
			ProviderLabel:   "",
			BaseURL:         "https://api.openai.com",
			APIKey:          "sk-test-key",
			TooltipData:     "tip",
			ModelID:         "gpt-4.1",
			ReasoningEffort: "medium",
			OpenAIEndpoint:  "/v1/chat/completions",
			Enabled:         &enabled,
		},
	}
	got, err := NormalizeModelAdapterConfigs(input)
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs error: %v", err)
	}
	if got[0].ProviderLabel != "" {
		t.Fatalf("ProviderLabel = %q, want empty", got[0].ProviderLabel)
	}
}
