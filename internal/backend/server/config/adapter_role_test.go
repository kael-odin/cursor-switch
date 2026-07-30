package config

import "testing"

// TestNormalizeModelAdapterRoleDefault 验证 Role 空/非法时回退 chat（现状不破）。
func TestNormalizeModelAdapterRoleDefault(t *testing.T) {
	enabled := true
	// role=image/both 的 case 必须配 ImageModelID（否则 normalize 会报 imageModelID 必填）。
	cases := []struct {
		role          string
		want          string
		imageModelID  string
	}{
		{"", "chat", ""},
		{"unknown", "chat", ""},
		{"CHAT", "chat", ""},
		{"  image  ", "image", "gpt-image-2"},
		{"both", "both", "gpt-image-2"},
	}
	for i, c := range cases {
		input := []ModelAdapterConfig{
			{
				DisplayName: "A", Type: "openai", Role: c.role, ImageModelID: c.imageModelID,
				BaseURL: "https://api.openai.com", APIKey: "sk", TooltipData: "tip",
				ModelID: "gpt-x", OpenAIEndpoint: "/v1/responses", Enabled: &enabled,
			},
		}
		got, err := NormalizeModelAdapterConfigs(input)
		if err != nil {
			t.Fatalf("case %d role=%q normalize error: %v", i, c.role, err)
		}
		if got[0].Role != c.want {
			t.Fatalf("case %d role=%q got %q want %q", i, c.role, got[0].Role, c.want)
		}
	}
}

// TestNormalizeModelAdapterRoleImageModelIDFallback 验证 Role==image 且 ModelID 空时，
// ModelID 兜底成 ImageModelID——让纯 image adapter 绕过 ModelID 必填，独立服务生图。
func TestNormalizeModelAdapterRoleImageModelIDFallback(t *testing.T) {
	enabled := true
	input := []ModelAdapterConfig{
		{
			DisplayName: "ImageOnly", Type: "openai", Role: "image", ModelID: "",
			ImageModelID: "gpt-image-2", BaseURL: "https://api.openai.com",
			APIKey: "sk", TooltipData: "tip", OpenAIEndpoint: "/v1/responses", Enabled: &enabled,
		},
	}
	got, err := NormalizeModelAdapterConfigs(input)
	if err != nil {
		t.Fatalf("role=image with empty ModelID should fall back, got error: %v", err)
	}
	if got[0].ModelID != "gpt-image-2" {
		t.Fatalf("ModelID = %q, want gpt-image-2 (ImageModelID fallback)", got[0].ModelID)
	}
	if got[0].ImageModelID != "gpt-image-2" {
		t.Fatalf("ImageModelID = %q, want gpt-image-2", got[0].ImageModelID)
	}
	if got[0].Role != "image" {
		t.Fatalf("Role = %q, want image", got[0].Role)
	}
}

// TestNormalizeModelAdapterRoleImageRequiresImageModelID 验证 Role==image 且 ImageModelID 空 → 报错。
func TestNormalizeModelAdapterRoleImageRequiresImageModelID(t *testing.T) {
	enabled := true
	input := []ModelAdapterConfig{
		{
			DisplayName: "BadImage", Type: "openai", Role: "image", ModelID: "", ImageModelID: "",
			BaseURL: "https://api.openai.com", APIKey: "sk", TooltipData: "tip",
			OpenAIEndpoint: "/v1/responses", Enabled: &enabled,
		},
	}
	if _, err := NormalizeModelAdapterConfigs(input); err == nil {
		t.Fatal("role=image with empty imageModelID should error")
	}
}

// TestResolveImageAdapterChannelsPicksRoleImage 验证 SelectChannelForImage 的纯函数核心：
// 筛 Role==image/both 且 enabled，按 Priority 升序取首个。
func TestResolveImageAdapterChannelsPicksRoleImage(t *testing.T) {
	enabled := true
	disabled := false
	adapters := []ModelAdapterConfig{
		// chat adapter（Role 默认 chat）——应被忽略
		{DisplayName: "Chat", Type: "openai", Role: "chat", ModelID: "gpt-x", BaseURL: "https://a", APIKey: "sk", TooltipData: "t", OpenAIEndpoint: "/v1/responses", Enabled: &enabled, Priority: 0},
		// image adapter，priority 高（数字大）——应排在 both 之后
		{DisplayName: "Img2", Type: "openai", Role: "image", ModelID: "gpt-image-2", ImageModelID: "gpt-image-2", BaseURL: "https://b", APIKey: "sk", TooltipData: "t", OpenAIEndpoint: "/v1/responses", Enabled: &enabled, Priority: 10},
		// both adapter，priority 低（数字小）——应被首选
		{DisplayName: "Both1", Type: "openai", Role: "both", ModelID: "gpt-5", ImageModelID: "gpt-image-2", BaseURL: "https://c", APIKey: "sk", TooltipData: "t", OpenAIEndpoint: "/v1/responses", Enabled: &enabled, Priority: 1},
		// 被禁用的 image adapter——应被忽略
		{DisplayName: "Disabled", Type: "openai", Role: "image", ModelID: "gpt-image-2", ImageModelID: "gpt-image-2", BaseURL: "https://d", APIKey: "sk", TooltipData: "t", OpenAIEndpoint: "/v1/responses", Enabled: &disabled, Priority: 0},
	}
	got, err := resolveImageAdapterChannels(adapters)
	if err != nil {
		t.Fatalf("resolveImageAdapterChannels error: %v", err)
	}
	if got == nil {
		t.Fatal("expected a channel, got nil")
	}
	if got.Name != "Both1" {
		t.Fatalf("Name = %q, want Both1 (lowest priority enabled image/both)", got.Name)
	}
	if got.Role != "both" {
		t.Fatalf("Role = %q, want both", got.Role)
	}
}

// TestResolveImageAdapterChannelsEmpty 验证无 image/both adapter 时返回 ErrChannelNotAvailable。
func TestResolveImageAdapterChannelsEmpty(t *testing.T) {
	enabled := true
	adapters := []ModelAdapterConfig{
		{DisplayName: "Chat", Type: "openai", Role: "chat", ModelID: "gpt-x", BaseURL: "https://a", APIKey: "sk", TooltipData: "t", OpenAIEndpoint: "/v1/responses", Enabled: &enabled},
	}
	if _, err := resolveImageAdapterChannels(adapters); err == nil {
		t.Fatal("expected error when no image/both adapter configured")
	}
}
