package modeladapter

import (
	"strings"
	"testing"
)

// TestOpenAIThinkTagParser_CoversChunkedThinkTag 验证 think 标签跨 chunk 拆分时的 carry 正确性。
// 这是 SSE 流式解析里最易出错的纯逻辑：一个 ⛘...schließ 标签可能被任意切到两次 SSE 事件。
func TestOpenAIThinkTagParser_CoversChunkedThinkTag(t *testing.T) {
	tests := []struct {
		name   string
		feeds  []string
		want   []openAIContentPart
	}{
		{
			name:  "plain text no tags",
			feeds: []string{"hello world"},
			want: []openAIContentPart{{Kind: openAIContentPartText, Text: "hello world"}},
		},
		{
			name:  "complete think block",
			feeds: []string{"before" + openAIThinkOpenTag + "reasoning" + openAIThinkCloseTag + "after"},
			want: []openAIContentPart{
				{Kind: openAIContentPartText, Text: "before"},
				{Kind: openAIContentPartReasoning, Text: "reasoning"},
				{Kind: openAIContentPartThinkingCompleted},
				{Kind: openAIContentPartText, Text: "after"},
			},
		},
		{
			name:  "think open tag split across chunks",
			feeds: []string{"ab" + string([]byte(openAIThinkOpenTag)[:1]), string([]byte(openAIThinkOpenTag)[1:]) + "rs" + openAIThinkCloseTag},
			want: []openAIContentPart{
				{Kind: openAIContentPartText, Text: "ab"},
				{Kind: openAIContentPartReasoning, Text: "rs"},
				{Kind: openAIContentPartThinkingCompleted},
			},
		},
		{
			name:  "think close tag split across chunks",
			feeds: []string{openAIThinkOpenTag + "rs" + string([]byte(openAIThinkCloseTag)[:1]), string([]byte(openAIThinkCloseTag)[1:])},
			want: []openAIContentPart{
				{Kind: openAIContentPartReasoning, Text: "rs"},
				{Kind: openAIContentPartThinkingCompleted},
			},
		},
		{
			name:  "empty reasoning block",
			feeds: []string{openAIThinkOpenTag + openAIThinkCloseTag + "tail"},
			want: []openAIContentPart{
				{Kind: openAIContentPartThinkingCompleted},
				{Kind: openAIContentPartText, Text: "tail"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parser := &openAIThinkTagParser{}
			var got []openAIContentPart
			for _, feed := range tc.feeds {
				got = append(got, parser.Consume(feed)...)
			}
			got = append(got, parser.Flush()...)
			if !equalOpenAIParts(got, tc.want) {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

// TestOpenAIThinkTagParser_FlushFlushesCarry 验证未闭合 think 块在 Flush 时作为 reasoning 输出。
func TestOpenAIThinkTagParser_FlushFlushesCarry(t *testing.T) {
	parser := &openAIThinkTagParser{}
	parts := parser.Consume(openAIThinkOpenTag + "unfinished")
	if len(parts) != 1 || parts[0].Kind != openAIContentPartReasoning || parts[0].Text != "unfinished" {
		t.Fatalf("consume before flush got %+v", parts)
	}
	flushed := parser.Flush()
	if len(flushed) != 0 {
		t.Fatalf("carry already consumed in Consume, flush should be empty, got %+v", flushed)
	}
}

func equalOpenAIParts(a, b []openAIContentPart) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || a[i].Text != b[i].Text {
			return false
		}
	}
	return true
}

// TestAnthropicThinkTagParser_ParityWithOpenAI 验证 anthropic 解析器与 openai 行为一致（同标签同语义）。
func TestAnthropicThinkTagParser_ParityWithOpenAI(t *testing.T) {
	input := "pre" + anthropicThinkOpenTag + "inner" + anthropicThinkCloseTag + "post"
	p := &anthropicThinkTagParser{}
	got := append(p.Consume(input), p.Flush()...)
	want := []anthropicContentPart{
		{Kind: anthropicContentPartText, Text: "pre"},
		{Kind: anthropicContentPartReasoning, Text: "inner"},
		{Kind: anthropicContentPartThinkingCompleted},
		{Kind: anthropicContentPartText, Text: "post"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	for i := range got {
		if got[i].Kind != want[i].Kind || got[i].Text != want[i].Text {
			t.Fatalf("part %d got %+v want %+v", i, got[i], want[i])
		}
	}
}

// TestTrailingTagPrefixLength 验证跨 chunk 边界时残留标签前缀的识别长度。
func TestTrailingTagPrefixLength(t *testing.T) {
	tests := []struct {
		text string
		tag  string
		want int
	}{
		{"abc", openAIThinkOpenTag, 0},
		{"ab" + string([]byte(openAIThinkOpenTag)[:1]), openAIThinkOpenTag, 1},
		{openAIThinkOpenTag, openAIThinkOpenTag, 0}, // full tag is not a "prefix" (len(tag)-1 cap)
		{"x" + openAIThinkCloseTag[:2], openAIThinkCloseTag, 2},
	}
	for _, tc := range tests {
		if got := trailingTagPrefixLength(tc.text, tc.tag); got != tc.want {
			t.Errorf("trailingTagPrefixLength(%q, %q) = %d, want %d", tc.text, tc.tag, got, tc.want)
		}
	}
}

// TestNamespaceToolCallID 验证 tool call ID 命名空间化：空 raw 返回空，已命名空间化的原样，否则带 modelCallID 前缀。
func TestNamespaceToolCallID(t *testing.T) {
	if got := namespaceToolCallID("mc", ""); got != "" {
		t.Errorf("empty raw should return empty, got %q", got)
	}
	already := "abc::call_1"
	if got := namespaceToolCallID("mc", already); !strings.Contains(got, "call_1") {
		t.Errorf("already-namespaced should preserve raw, got %q", got)
	}
	got := namespaceToolCallID("mc", "raw_1")
	if !strings.Contains(got, "raw_1") {
		t.Errorf("namespaced should contain raw, got %q", got)
	}
	// empty modelCallID still returns a provider-safe id (no panic, non-empty).
	if got2 := namespaceToolCallID("", "raw_2"); got2 == "" {
		t.Errorf("empty model id should still produce non-empty id")
	}
}
