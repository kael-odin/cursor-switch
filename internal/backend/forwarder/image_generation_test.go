package forwarder

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGenerateImageSuccess 验证文生图：调 OpenAI 兼容 images API，成功返回 base64。
//
// GenerateImage 此前是空壳 stub，现改为真实调 {baseURL}/v1/images/generations。
// 这里用 httptest mock 上游，确认请求体含 model/prompt/response_format=b64_json，
// 且正确解析 {data:[{b64_json}]} 响应。
func TestGenerateImageSuccess(t *testing.T) {
	expectedB64 := base64.StdEncoding.EncodeToString([]byte("fake-png-bytes"))
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/images/generations") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("auth header = %q, want Bearer sk-test", r.Header.Get("Authorization"))
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"b64_json": expectedB64}},
		})
	}))
	defer srv.Close()

	got, err := generateImage(context.Background(), srv.URL, "sk-test", "gpt-image-2", "a cat")
	if err != nil {
		t.Fatalf("generateImage error: %v", err)
	}
	if got != expectedB64 {
		t.Fatalf("got %q, want %q", got, expectedB64)
	}
	if gotBody["model"] != "gpt-image-2" {
		t.Errorf("request model = %v, want gpt-image-2", gotBody["model"])
	}
	if gotBody["prompt"] != "a cat" {
		t.Errorf("request prompt = %v, want a cat", gotBody["prompt"])
	}
	if gotBody["response_format"] != "b64_json" {
		t.Errorf("response_format = %v, want b64_json", gotBody["response_format"])
	}
}

// TestGenerateImageAPIError 验证上游报错时返回友好错误（提取 error.message）。
func TestGenerateImageAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "model gpt-image-2 not found"},
		})
	}))
	defer srv.Close()

	_, err := generateImage(context.Background(), srv.URL, "sk-test", "gpt-image-2", "a cat")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "model gpt-image-2 not found") {
		t.Fatalf("error %q should contain upstream message", err.Error())
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error %q should contain status code 400", err.Error())
	}
}

// TestGenerateImageNoData 验证 200 但 data 为空时报错（而非返回空字符串假成功）。
func TestGenerateImageNoData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}})
	}))
	defer srv.Close()

	_, err := generateImage(context.Background(), srv.URL, "sk-test", "gpt-image-2", "a cat")
	if err == nil || !strings.Contains(err.Error(), "no image data") {
		t.Fatalf("expected no-image-data error, got %v", err)
	}
}

// TestParseImageAPIError 验证错误体解析：标准 OpenAI 格式提 message，否则回退截断原文。
func TestParseImageAPIError(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"standard_error", `{"error":{"message":"rate limited","type":"rate_limit"}}`, "rate limited"},
		{"no_message_fallback", `{"some":"other"}`, `{"some":"other"}`},
		{"invalid_json_fallback", `not json at all`, "not json at all"},
		{"truncated", strings.Repeat("x", 300), strings.Repeat("x", 200)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseImageAPIError([]byte(tt.raw))
			if got != tt.want {
				t.Fatalf("parseImageAPIError() = %q, want %q", got, tt.want)
			}
		})
	}
}
