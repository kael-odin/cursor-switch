package config

import "testing"

// TestNormalizeModelAdapterPreservesImageModelID 验证 ImageModelID 经 normalize 透传（TrimSpace）。
//
// imageModelID 是 adapter 用于生图的模型（如 gpt-image-2），与 ModelID（chat 模型）独立。
// 同一 adapter 既能 chat（ModelID）又能生图（imageModelID）。normalize 必须原样保留，不能清空。
func TestNormalizeModelAdapterPreservesImageModelID(t *testing.T) {
	enabled := true
	input := []ModelAdapterConfig{
		{
			DisplayName:    "GPT55Sol",
			Type:           "openai",
			ImageModelID:   "  gpt-image-2  ",
			BaseURL:        "https://api.openai.com",
			APIKey:         "sk-test-key",
			TooltipData:    "tip",
			ModelID:        "gpt-5.6-sol",
			OpenAIEndpoint: "/v1/responses",
			Enabled:        &enabled,
		},
	}
	got, err := NormalizeModelAdapterConfigs(input)
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 adapter, got %d", len(got))
	}
	if want := "gpt-image-2"; got[0].ImageModelID != want {
		t.Fatalf("ImageModelID = %q, want %q (should be trimmed, not cleared)", got[0].ImageModelID, want)
	}
	if got[0].ModelID != "gpt-5.6-sol" {
		t.Fatalf("ModelID = %q, want gpt-5.6-sol (image model must not corrupt chat model)", got[0].ModelID)
	}
}

// TestNormalizeModelAdapterEmptyImageModelID 验证留空 imageModelID 不会报错——
// 此时 resolveImageChannel 会回退到 ModelID。
func TestNormalizeModelAdapterEmptyImageModelID(t *testing.T) {
	enabled := true
	input := []ModelAdapterConfig{
		{
			DisplayName:    "PlainChat",
			Type:           "openai",
			ImageModelID:   "",
			BaseURL:        "https://api.openai.com",
			APIKey:         "sk-test-key",
			TooltipData:    "tip",
			ModelID:        "gpt-4.1",
			OpenAIEndpoint: "/v1/responses",
			Enabled:        &enabled,
		},
	}
	got, err := NormalizeModelAdapterConfigs(input)
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs error: %v", err)
	}
	if got[0].ImageModelID != "" {
		t.Fatalf("ImageModelID = %q, want empty", got[0].ImageModelID)
	}
}
