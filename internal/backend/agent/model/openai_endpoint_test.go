package modeladapter

import "testing"

// F-23：Custom Endpoint 字符串拼接，Query/Fragment 破坏 URL。
// 此前 base + endpoint 字符串拼接会让含 query（Azure 风格 ?api-version=...）
// 或 fragment 的 baseURL 把 endpoint 落进 query 值里或丢路径。
// 修复用 net/url / 手动拆分只拼到 path，query/fragment 原样保留。

func TestOpenAIEndpointURL_QueryPreserved(t *testing.T) {
	// endpoint /v1/chat/completions 追加到 path /openai 之后，query 保留。
	got := OpenAIEndpointURL("https://host/openai?api-version=2024", "/v1/chat/completions")
	want := "https://host/openai/v1/chat/completions?api-version=2024"
	if got != want {
		t.Errorf("F-23 query preserved: got %q want %q", got, want)
	}
}

func TestOpenAIEndpointURL_FragmentPreserved(t *testing.T) {
	got := OpenAIEndpointURL("https://host/base#section", "/v1/responses")
	want := "https://host/base/v1/responses#section"
	if got != want {
		t.Errorf("F-23 fragment preserved: got %q want %q", got, want)
	}
}

func TestOpenAIEndpointURL_NoQueryNoFragment(t *testing.T) {
	// 回归保护：普通无 query/fragment URL 行为不变。
	got := OpenAIEndpointURL("https://host/v1", "/chat/completions")
	want := "https://host/v1/chat/completions"
	if got != want {
		t.Errorf("F-23 plain URL: got %q want %q", got, want)
	}
}

func TestOpenAIEndpointURL_AlreadyHasEndpointWithQuery(t *testing.T) {
	// baseURL 已含 /chat/completions 且带 query → 短路原样返回（query 保留，不重复拼路径）。
	got := OpenAIEndpointURL("https://host/v1/chat/completions?api-version=x", "/v1/chat/completions")
	want := "https://host/v1/chat/completions?api-version=x"
	if got != want {
		t.Errorf("F-23 already-has-endpoint + query: got %q want %q", got, want)
	}
}

func TestOpenAIEndpointURL_CustomModeWithQuery(t *testing.T) {
	// /custom 模式 + baseURL 已含 /chat/completions + query → 直接返回 base。
	got := OpenAIEndpointURL("https://host/v1/chat/completions?api-version=x", "/custom")
	want := "https://host/v1/chat/completions?api-version=x"
	if got != want {
		t.Errorf("F-23 custom mode + query: got %q want %q", got, want)
	}
}

func TestOpenAIEndpointURL_VersionStripWithQuery(t *testing.T) {
	// 版本段剥离 + query 保留：base .../v4 + endpoint /v1/chat/completions → .../v4/chat/completions
	got := OpenAIEndpointURL("https://host/v4?api-version=x", "/v1/chat/completions")
	want := "https://host/v4/chat/completions?api-version=x"
	if got != want {
		t.Errorf("F-23 version strip + query: got %q want %q", got, want)
	}
}

func TestOpenAIEndpointURL_QueryAndFragmentPreserved(t *testing.T) {
	got := OpenAIEndpointURL("https://host/openai?api-version=2024#frag", "/v1/chat/completions")
	want := "https://host/openai/v1/chat/completions?api-version=2024#frag"
	if got != want {
		t.Errorf("F-23 query+fragment: got %q want %q", got, want)
	}
}

func TestOpenAIEndpointURL_DefaultEndpointWithQuery(t *testing.T) {
	// endpoint 空 → 默认 /v1/responses，仍只拼到 path。
	got := OpenAIEndpointURL("https://host?api-version=x", "")
	want := "https://host/v1/responses?api-version=x"
	if got != want {
		t.Errorf("F-23 default endpoint + query: got %q want %q", got, want)
	}
}

func TestAnthropicEndpointURL_QueryPreserved(t *testing.T) {
	got := anthropicEndpointURL("https://host?api-version=x")
	want := "https://host/v1/messages?api-version=x"
	if got != want {
		t.Errorf("F-23 anthropic query: got %q want %q", got, want)
	}
}

func TestAnthropicEndpointURL_AlreadyHasMessagesWithQuery(t *testing.T) {
	// baseURL 已含 /v1/messages + query → 短路原样返回。
	got := anthropicEndpointURL("https://host/v1/messages?api-version=x")
	want := "https://host/v1/messages?api-version=x"
	if got != want {
		t.Errorf("F-23 anthropic already-has-endpoint + query: got %q want %q", got, want)
	}
}

func TestAnthropicEndpointURL_NoQueryNoFragment(t *testing.T) {
	// 回归保护：普通 Anthropic URL 行为不变。
	got := anthropicEndpointURL("https://host")
	want := "https://host/v1/messages"
	if got != want {
		t.Errorf("F-23 anthropic plain URL: got %q want %q", got, want)
	}
}
