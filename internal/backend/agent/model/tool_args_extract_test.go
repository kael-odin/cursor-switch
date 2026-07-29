package modeladapter

import (
	"strings"
	"testing"
)

// TestExtractJSONStringFieldPrefix_BasicCases 覆盖 N-07 的核心解析函数行为基线：
// key 定位、value 提取、转义解码、未闭合、未找到、跨边界（searchFrom）。
// 这些是 N-07 增量优化前就应成立的契约——优化不得改变语义。
func TestExtractJSONStringFieldPrefix_BasicCases(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		field    string
		wantVal  string
		wantFound bool
		wantComplete bool
	}{
		{"simple", `{"path":"/a/b"}`, "path", "/a/b", true, true},
		{"file_path alt", `{"file_path":"x.txt"}`, "file_path", "x.txt", true, true},
		{"field absent", `{"other":1}`, "path", "", false, false},
		{"unclosed string", `{"path":"/a`, "path", "/a", true, false},
		{"escape quote", `{"path":"a\"b"}`, "path", `a"b`, true, true},
		{"escape backslash", `{"path":"a\\b"}`, "path", `a\b`, true, true},
		{"escape newline", `{"path":"a\nb"}`, "path", "a\nb", true, true},
		{"unicode escape", `{"path":"é"}`, "path", "é", true, true},
		{"whitespace around colon", `{"path" : "/x"}`, "path", "/x", true, true},
		{"key in value substring not matched first", `{"path":"/x","content":"\"path\""}`, "path", "/x", true, true},
		{"no colon after key", `{"path" "/x"}`, "path", "", false, false},
		{"no quote after colon", `{"path": 123}`, "path", "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			val, found, complete := extractJSONStringFieldPrefix(c.input, c.field)
			if found != c.wantFound || complete != c.wantComplete {
				t.Errorf("extract(%q,%q) = (%q,%v,%v), want found=%v complete=%v", c.input, c.field, val, found, complete, c.wantFound, c.wantComplete)
			}
			if found && val != c.wantVal {
				t.Errorf("extract(%q,%q) value = %q, want %q", c.input, c.field, val, c.wantVal)
			}
		})
	}
}

// TestExtractJSONStringFieldPrefixFrom_SearchFrom 覆盖 N-07 增量优化：
// searchFrom 让 key 搜索从指定偏移开始——accumulator 维护此偏移避免每 delta 全扫。
// 跨边界场景：keyToken 跨 delta 边界（前缀在上个 delta 末尾），searchFrom 需回退
// keyToken 长度-1 以命中跨边界 key。
func TestExtractJSONStringFieldPrefixFrom_SearchFrom(t *testing.T) {
	// key 在偏移 1（{"path":...}），searchFrom=0 命中。
	val, found, complete := extractJSONStringFieldPrefixFrom(`{"path":"/x"}`, "path", 0)
	if !found || !complete || val != "/x" {
		t.Errorf("searchFrom=0: got (%q,%v,%v), want (/x,true,true)", val, found, complete)
	}
	// searchFrom 越过 key 起点 → 不应命中（调用方应保证 searchFrom 在 key 之前或回退）。
	_, found, _ = extractJSONStringFieldPrefixFrom(`{"path":"/x"}`, "path", 3)
	if found {
		t.Errorf("searchFrom=3 should not find key at offset 1")
	}
}

// TestExtractToolStreamContentPrefix 覆盖 streamContent 字段提取：
// PatchEdit 取 new_string，Write 取 contents/content/stream_content/streamContent 之一。
func TestExtractToolStreamContentPrefix(t *testing.T) {
	cases := []struct {
		toolName string
		input    string
		want     string
		wantFound bool
	}{
		{"PatchEdit", `{"new_string":"abc"}`, "abc", true},
		{"Write", `{"contents":"c1"}`, "c1", true},
		{"Write", `{"content":"c2"}`, "c2", true},
		{"Write", `{"stream_content":"c3"}`, "c3", true},
		{"Write", `{"streamContent":"c4"}`, "c4", true},
		{"Write", `{"other":1}`, "", false},
		{"PatchEdit", `{"old_string":"x","new_string":"y"}`, "y", true},
	}
	for _, c := range cases {
		t.Run(c.toolName+"/"+c.input, func(t *testing.T) {
			val, found := extractToolStreamContentPrefix(c.input, c.toolName)
			if found != c.wantFound {
				t.Errorf("extractToolStreamContentPrefix(%q,%q) found=%v, want %v", c.input, c.toolName, found, c.wantFound)
			}
			if found && val != c.want {
				t.Errorf("extractToolStreamContentPrefix(%q,%q) = %q, want %q", c.input, c.toolName, val, c.want)
			}
		})
	}
}

// TestExtractToolStreamContentPrefixFrom_SearchFrom 覆盖 N-07 streamContent key 搜索增量偏移：
// searchFrom 越过 key 起点 → 不命中；searchFrom=0 → 命中。
func TestExtractToolStreamContentPrefixFrom_SearchFrom(t *testing.T) {
	val, found := extractToolStreamContentPrefixFrom(`{"new_string":"abc"}`, "PatchEdit", 0)
	if !found || val != "abc" {
		t.Errorf("searchFrom=0: got (%q,%v), want (abc,true)", val, found)
	}
	_, found = extractToolStreamContentPrefixFrom(`{"new_string":"abc"}`, "PatchEdit", 5)
	if found {
		t.Errorf("searchFrom=5 should not find key at offset 1")
	}
}

// TestSuffixAfterCommonPrefix 覆盖增量 delta 计算：
// 返回 current 相对 previous 的公共前缀之后的后缀。
func TestSuffixAfterCommonPrefix(t *testing.T) {
	if got := suffixAfterCommonPrefix("", "abc"); got != "abc" {
		t.Errorf("empty previous: got %q want abc", got)
	}
	if got := suffixAfterCommonPrefix("abc", "abcdef"); got != "def" {
		t.Errorf("growth: got %q want def", got)
	}
	if got := suffixAfterCommonPrefix("abc", "abc"); got != "" {
		t.Errorf("equal: got %q want empty", got)
	}
	// current 比 previous 短（不应发生于流式追加，但不应 panic）。
	if got := suffixAfterCommonPrefix("abcdef", "abc"); got != "" {
		t.Errorf("shrink: got %q want empty", got)
	}
}

// TestExtractJSONStringFieldPrefix_LargeAccumulatedBuffer 覆盖 N-07 性能场景：
// 累积大 buffer（模拟 streamContent 增长）时提取仍正确。这是回归基线——
// 增量优化后此用例必须仍通过。
func TestExtractJSONStringFieldPrefix_LargeAccumulatedBuffer(t *testing.T) {
	largeValue := strings.Repeat("a", 5000)
	input := `{"new_string":"` + largeValue + `"}`
	val, found, complete := extractJSONStringFieldPrefix(input, "new_string")
	if !found || !complete {
		t.Fatalf("large buffer: found=%v complete=%v", found, complete)
	}
	if val != largeValue {
		t.Errorf("large buffer value len = %d, want %d", len(val), len(largeValue))
	}
}
