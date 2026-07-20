package cursor

import (
	"testing"
)

// TestNormalizeJSONC 验证 Cursor settings.jsonc 的注释剥离、尾逗号清理、BOM 处理。
// 这是 P0-7 settings.json 解析失败改备份的核心逻辑：JSONC 解析错了会导致整个代理配置失效。
func TestNormalizeJSONC(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want map[string]any
	}{
		{
			name: "line comment",
			in:   `{"a": 1 // comment` + "\n" + `,"b": 2}`,
			want: map[string]any{"a": float64(1), "b": float64(2)},
		},
		{
			name: "block comment",
			in:   `{"a": /* block */ 1,"b":2}`,
			want: map[string]any{"a": float64(1), "b": float64(2)},
		},
		{
			name: "trailing comma",
			in:   `{"a": 1, "b": 2,}`,
			want: map[string]any{"a": float64(1), "b": float64(2)},
		},
		{
			name: "comment inside string preserved",
			in:   `{"url": "http://x // not a comment"}`,
			want: map[string]any{"url": "http://x // not a comment"},
		},
		{
			name: "BOM stripped",
			in:   "\xEF\xBB\xBF" + `{"a":1}`,
			want: map[string]any{"a": float64(1)},
		},
		{
			name: "empty input",
			in:   "   ",
			want: map[string]any{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeCursorSettingsJSONC([]byte(tc.in))
			if err != nil {
				t.Fatalf("decodeCursorSettingsJSONC err: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("key %q: got %v want %v", k, got[k], v)
				}
			}
		})
	}
}

// TestDecodeCursorSettingsJSONC_InvalidJSON 验证非法 JSON 返回错误而非 panic。
func TestDecodeCursorSettingsJSONC_InvalidJSON(t *testing.T) {
	_, err := decodeCursorSettingsJSONC([]byte(`{"a": }`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// TestProxyURLFromListenAddr 验证监听地址→代理 URL 的归一化。
func TestProxyURLFromListenAddr(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"", "http://127.0.0.1:8080"},
		{"  :8189  ", "http://127.0.0.1:8189"},
		{":8189", "http://127.0.0.1:8189"},
		{"0.0.0.0:8189", "http://127.0.0.1:8189"},
		{"127.0.0.1:8189", "http://127.0.0.1:8189"},
		{"localhost:8189", "http://localhost:8189"},
		{"[::]:8189", "http://127.0.0.1:8189"},
	}
	for _, tc := range tests {
		if got := ProxyURLFromListenAddr(tc.addr); got != tc.want {
			t.Errorf("ProxyURLFromListenAddr(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

// TestSamePath 验证路径比较的 clean 与空值守卫。
func TestSamePath(t *testing.T) {
	if samePath("", "/x") {
		t.Fatal("empty left should be false")
	}
	if !samePath("/a/b/../c", "/a/c") {
		t.Fatal("Clean should normalize ../")
	}
	if samePath("/a/c", "/a/d") {
		t.Fatal("different paths should be false")
	}
}

// TestDisplayOSName 验证 goos→展示名映射。
func TestDisplayOSName(t *testing.T) {
	tests := map[string]string{
		"darwin":  "macOS",
		"DARWIN":  "macOS",
		"windows": "Windows",
		"linux":   "Linux",
		"freebsd": "freebsd", // 未知原样返回
	}
	for in, want := range tests {
		if got := displayOSName(in); got != want {
			t.Errorf("displayOSName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCursorStateDJB2Hash 验证 DJB2 变体哈希的确定性。
func TestCursorStateDJB2Hash(t *testing.T) {
	// 确定性：同输入同输出。
	if cursorStateDJB2Hash("abc") != cursorStateDJB2Hash("abc") {
		t.Fatal("hash not deterministic")
	}
	// 不同输入不同输出。
	if cursorStateDJB2Hash("abc") == cursorStateDJB2Hash("abd") {
		t.Fatal("collision for distinct inputs")
	}
	// 手算验证：hash=0; 'a'=97 → 0*31+97=97; 'b'=98 → 97*31+98=3105; 'c'=99 → 3105*31+99=96354
	if got := cursorStateDJB2Hash("abc"); got != "96354" {
		t.Errorf("hash(abc) = %q, want 96354", got)
	}
}

// TestDisableCursorStatsigGate 验证 gate 被强制置 false，且保留既有字段。
func TestDisableCursorStatsigGate(t *testing.T) {
	gates := map[string]any{}
	disableCursorStatsigGate(gates, "some_gate")
	gate, ok := gates["some_gate"].(map[string]any)
	if !ok {
		t.Fatalf("gate not created: %+v", gates)
	}
	if gate["value"] != false {
		t.Fatalf("value = %v, want false", gate["value"])
	}
	if gate["name"] != "some_gate" {
		t.Fatalf("name = %v", gate["name"])
	}

	// 已存在的 gate 只置 value，不重建。
	existing := map[string]any{"some_gate": map[string]any{"name": "orig", "custom": "keep"}}
	disableCursorStatsigGate(existing, "some_gate")
	ex := existing["some_gate"].(map[string]any)
	if ex["custom"] != "keep" {
		t.Fatalf("existing field lost: %+v", ex)
	}
	if ex["value"] != false {
		t.Fatalf("value not set to false: %+v", ex)
	}
}

// TestBuildCursorAuthStateValues 验证 auth state 值结构与 trim。
func TestBuildCursorAuthStateValues(t *testing.T) {
	v := buildCursorAuthStateValues("  user@example.com  ", "  tok-123  ")
	if v["cursorAuth/cachedEmail"] != "user@example.com" {
		t.Errorf("email not trimmed: %q", v["cursorAuth/cachedEmail"])
	}
	if v["cursorAuth/accessToken"] != "tok-123" {
		t.Errorf("token not trimmed: %q", v["cursorAuth/accessToken"])
	}
	if v["cursorAuth/refreshToken"] != "tok-123" {
		t.Errorf("refreshToken should mirror token: %q", v["cursorAuth/refreshToken"])
	}
	if _, ok := v["cursorAuth/cachedSignUpType"]; !ok {
		t.Errorf("signUpType missing: %+v", v)
	}
}
