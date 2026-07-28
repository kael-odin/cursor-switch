package modeladapter

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestApplyCustomHeaders_BlocksSensitiveHeaders(t *testing.T) {
	tests := []struct {
		name       string
		headers    string
		wantSet    map[string]string // headers that SHOULD be applied
		wantBlocked []string          // headers that must NOT appear
	}{
		{
			name: "authorization blocked",
			headers: `{"Authorization":"Bearer evil","X-Custom":"v"}`,
			wantSet: map[string]string{"X-Custom": "v"},
			wantBlocked: []string{"Authorization"},
		},
		{
			name: "x-api-key case-insensitive blocked",
			headers: `{"X-API-KEY":"evil","Accept":"application/json"}`,
			wantSet: map[string]string{"Accept": "application/json"},
			wantBlocked: []string{"X-Api-Key"},
		},
		{
			name: "host and cookie blocked",
			headers: `{"Host":"evil.com","Cookie":"session=1","X-Trace":"t"}`,
			wantSet: map[string]string{"X-Trace": "t"},
			wantBlocked: []string{"Host", "Cookie"},
		},
		{
			name: "normal headers all applied",
			headers: `{"X-Trace":"t","X-Req-Id":"r"}`,
			wantSet: map[string]string{"X-Trace": "t", "X-Req-Id": "r"},
			wantBlocked: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://localhost", nil)
			if err := ApplyCustomHeaders(req, true, tc.headers); err != nil {
				t.Fatalf("ApplyCustomHeaders error: %v", err)
			}
			for k, v := range tc.wantSet {
				if got := req.Header.Get(k); got != v {
					t.Errorf("header %q = %q, want %q", k, got, v)
				}
			}
			for _, b := range tc.wantBlocked {
				if req.Header.Get(b) != "" {
					t.Errorf("blocked header %q should not be set, got %q", b, req.Header.Get(b))
				}
			}
		})
	}
}

func TestApplyCustomHeaders_DisabledNoop(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://localhost", nil)
	if err := ApplyCustomHeaders(req, false, `{"X-Custom":"v"}`); err != nil {
		t.Fatalf("error: %v", err)
	}
	if req.Header.Get("X-Custom") != "" {
		t.Fatalf("disabled ApplyCustomHeaders should not set headers")
	}
}

// --- F-19: extra params 不得覆盖协议关键字段 ---

func TestApplyExtraParams_BlocksProtocolFields(t *testing.T) {
	// 全部 blocked 字段各一个攻击值，大小写混合证 lower-case 比对。
	params := `{
		"stream": false,
		"MODEL": "evil-model",
		"messages": [{"role":"user","content":"evil"}],
		"input": [{"role":"user","content":"evil"}],
		"tools": [{"type":"function","function":{"name":"evil"}}],
		"tool_choice": "none",
		"system": "evil-instructions",
		"instructions": "evil-instructions",
		"metadata": {"user_id":"evil"},
		"temperature": 0.7
	}`
	body := map[string]any{"model": "real-model"}
	if err := ApplyOpenAIExtraParams(body, true, params); err != nil {
		t.Fatalf("ApplyOpenAIExtraParams error: %v", err)
	}
	// blocked 字段一律不得出现。
	for _, blocked := range []string{
		"stream", "MODEL", "messages", "input", "tools",
		"tool_choice", "system", "instructions", "metadata",
	} {
		if _, ok := body[blocked]; ok {
			t.Errorf("F-19: blocked extra param %q should not be applied", blocked)
		}
	}
	// 合法调参字段应进 body。
	if got, ok := body["temperature"]; !ok || got != 0.7 {
		t.Errorf("F-19: temperature should be applied, got %v (present=%v)", got, ok)
	}
	// 原有 model 字段未被覆盖。
	if body["model"] != "real-model" {
		t.Errorf("F-19: existing model field should not be overwritten, got %v", body["model"])
	}
}

func TestApplyExtraParams_DisabledNoop(t *testing.T) {
	body := map[string]any{"model": "real-model"}
	if err := ApplyOpenAIExtraParams(body, false, `{"stream":false,"temperature":0.7}`); err != nil {
		t.Fatalf("error: %v", err)
	}
	if _, ok := body["stream"]; ok {
		t.Error("disabled extra params should not apply stream")
	}
	if _, ok := body["temperature"]; ok {
		t.Error("disabled extra params should not apply temperature")
	}
	if body["model"] != "real-model" {
		t.Errorf("disabled extra params should not mutate existing fields, model=%v", body["model"])
	}
}

func TestApplyExtraParams_AllowsTuningFields(t *testing.T) {
	params := `{
		"temperature": 0.5,
		"top_p": 0.9,
		"max_tokens": 1024,
		"reasoning_effort": "high",
		"response_format": {"type":"json_object"},
		"stop": ["\n"],
		"seed": 42
	}`
	body := map[string]any{}
	if err := ApplyOpenAIExtraParams(body, true, params); err != nil {
		t.Fatalf("error: %v", err)
	}
	for _, allowed := range []string{
		"temperature", "top_p", "max_tokens",
		"reasoning_effort", "response_format", "stop", "seed",
	} {
		if _, ok := body[allowed]; !ok {
			t.Errorf("F-19: tuning field %q should be allowed (denylist over-blocks)", allowed)
		}
	}
}

func TestApplyExtraParams_InvalidJSONReturnsError(t *testing.T) {
	body := map[string]any{}
	if err := ApplyOpenAIExtraParams(body, true, "{bad"); err == nil {
		t.Fatal("invalid JSON should return error")
	}
}

func TestApplyExtraParams_EmptyBodyReturnsError(t *testing.T) {
	if err := ApplyOpenAIExtraParams(nil, true, `{"temperature":0.7}`); err == nil {
		t.Fatal("nil body should return error")
	}
}

func TestApplyExtraParams_AnthropicWrapperSameDenylist(t *testing.T) {
	// Anthropic wrapper 与 OpenAI 共用 applyExtraParams，denylist 同效。
	body := map[string]any{"model": "real-model"}
	if err := ApplyAnthropicExtraParams(body, true, `{"stream":false,"system":"evil","max_tokens":100}`); err != nil {
		t.Fatalf("error: %v", err)
	}
	if _, ok := body["stream"]; ok {
		t.Error("F-19: anthropic stream should be blocked")
	}
	if _, ok := body["system"]; ok {
		t.Error("F-19: anthropic system should be blocked")
	}
	if _, ok := body["max_tokens"]; !ok {
		t.Error("F-19: anthropic max_tokens should be allowed")
	}
}

// keep strings import used by future table-driven cases if needed.
var _ = strings.TrimSpace
