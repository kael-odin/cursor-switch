package modeladapter

import "testing"

// TestEffectiveProvider 验证 usage 落盘 provider 标签的回退逻辑：
// 有 ProviderLabel（品牌）时优先用品牌，否则回退协议 Provider（openai/anthropic）。
//
// 背景：openai.go/anthropic.go 的 TurnFinished 事件改用 effectiveProvider(req)，
// 让用户在模型配置填的 providerLabel 覆盖硬编码的 "openai"/"anthropic"，
// 使接 deepseek（走 openai 协议）的请求在使用统计 by-provider 表里归到 deepseek。
func TestEffectiveProvider(t *testing.T) {
	tests := []struct {
		name string
		req  StreamRequest
		want string
	}{
		{
			name: "label_set_returns_label",
			req:  StreamRequest{Provider: "openai", ProviderLabel: "deepseek"},
			want: "deepseek",
		},
		{
			name: "label_empty_falls_back_to_provider",
			req:  StreamRequest{Provider: "openai", ProviderLabel: ""},
			want: "openai",
		},
		{
			name: "label_whitespace_only_falls_back",
			req:  StreamRequest{Provider: "anthropic", ProviderLabel: "   "},
			want: "anthropic",
		},
		{
			name: "label_trimmed",
			req:  StreamRequest{Provider: "openai", ProviderLabel: "  qwen  "},
			want: "qwen",
		},
		{
			name: "both_empty_returns_empty",
			req:  StreamRequest{Provider: "", ProviderLabel: ""},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveProvider(tt.req)
			if got != tt.want {
				t.Fatalf("effectiveProvider(%+v) = %q, want %q", tt.req, got, tt.want)
			}
		})
	}
}
