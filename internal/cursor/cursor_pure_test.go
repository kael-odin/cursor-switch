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

// 注：cursorStateDJB2Hash / disableCursorStatsigGate / buildCursorAuthStateValues
// 三组函数随 InjectCursorUserInfo 死代码在 P3-1 一并移除（无调用者），
// 对应的 TestCursorStateDJB2Hash / TestDisableCursorStatsigGate /
// TestBuildCursorAuthStateValues 同步删除。
