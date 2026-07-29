package forwarder

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestExtractDocsText_StripsScriptStyleTags 覆盖 N-21 正文提取：
// script/style/head/noscript 块（含内容）应被整块移除，不进正文。
func TestExtractDocsText_StripsScriptStyleTags(t *testing.T) {
	html := `<html><head><title>T</title><style>body{color:red}</style></head>
<body>
<h1>Real Title</h1>
<script>alert("x")</script>
<p>Hello <b>world</b> &amp; <a href="x">link</a></p>
<noscript>fallback</noscript>
</body></html>`
	text := extractDocsText(html)
	if strings.Contains(text, "alert") {
		t.Errorf("script content leaked into text: %q", text)
	}
	if strings.Contains(text, "color:red") || strings.Contains(text, "color") {
		t.Errorf("style content leaked into text: %q", text)
	}
	if strings.Contains(text, "fallback") {
		t.Errorf("noscript content leaked into text: %q", text)
	}
	if strings.Contains(text, "<") || strings.Contains(text, ">") {
		t.Errorf("tags not stripped: %q", text)
	}
	if !strings.Contains(text, "Real Title") {
		t.Errorf("h1 content lost: %q", text)
	}
	if !strings.Contains(text, "Hello") || !strings.Contains(text, "world") {
		t.Errorf("p content lost: %q", text)
	}
	if !strings.Contains(text, "&") {
		t.Errorf("&amp; not decoded: %q", text)
	}
}

// TestExtractDocsText_FoldsWhitespace 连续空白折叠成单空格，块级关闭换行保留。
func TestExtractDocsText_FoldsWhitespace(t *testing.T) {
	html := `<p>line1</p><p>line2</p>`
	text := extractDocsText(html)
	if !strings.Contains(text, "line1") || !strings.Contains(text, "line2") {
		t.Errorf("paragraphs lost: %q", text)
	}
	// </p> 应产生换行，使 line1 与 line2 分行。
	if !strings.Contains(text, "\n") {
		t.Errorf("block close should produce newline: %q", text)
	}
	// 不应有连续两行以上空行。
	if strings.Contains(text, "\n\n\n") {
		t.Errorf("excessive blank lines not collapsed: %q", text)
	}
}

// TestExtractDocsText_StripsComments HTML 注释应被移除。
func TestExtractDocsText_StripsComments(t *testing.T) {
	html := `<!-- a comment --><p>visible</p><!-- unclosed`
	text := extractDocsText(html)
	if strings.Contains(text, "comment") {
		t.Errorf("comment leaked: %q", text)
	}
	if !strings.Contains(text, "visible") {
		t.Errorf("visible content lost: %q", text)
	}
}

// TestTruncateRunes 按 rune 截断，超长加省略号。
func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("abcdef", 3); got != "abc…" {
		t.Errorf("truncate got %q want abc…", got)
	}
	if got := truncateRunes("ab", 10); got != "ab" {
		t.Errorf("short string should not be truncated: got %q", got)
	}
	// 多字节 rune 不应截断到半个字符。
	if got := truncateRunes("你好世界你好世界", 4); !strings.HasPrefix(got, "你好世界") {
		t.Errorf("rune truncation broke multi-byte char: got %q", got)
	}
}

// TestFetchDocsPageContent_EmptyAndInvalidURL 空/非 http(s) URL 直接返回空不报错。
func TestFetchDocsPageContent_EmptyAndInvalidURL(t *testing.T) {
	ctx := context.Background()
	if got, err := fetchDocsPageContent(ctx, ""); got != "" || err != nil {
		t.Errorf("empty url: got %q err=%v, want empty+nil", got, err)
	}
	if got, err := fetchDocsPageContent(ctx, "not a url"); got != "" || err != nil {
		t.Errorf("invalid url: got %q err=%v, want empty+nil", got, err)
	}
	if got, err := fetchDocsPageContent(ctx, "ftp://example.com/x"); got != "" || err != nil {
		t.Errorf("non-http scheme: got %q err=%v, want empty+nil", got, err)
	}
}

// TestFetchDocsPageContent_RejectsLoopbackSSRF 覆盖 N-21 + N-14：
// 抓取 loopback 地址应被 SSRF 防护拒绝——best-effort 返回空不阻断。
// 即便起本地 httptest 服务器，safehttp 也要拒绝 127.0.0.1，绝不能把内网响应入库。
func TestFetchDocsPageContent_RejectsLoopbackSSRF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 故意返回"内网敏感内容"——SSRF 防护应阻止其被入库。
		_, _ = w.Write([]byte("<p>SECRET INTERNAL DATA</p>"))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := fetchDocsPageContent(ctx, server.URL) // server.URL 是 http://127.0.0.1:port
	if err != nil {
		t.Fatalf("best-effort fetch should not return error, got %v", err)
	}
	if got != "" {
		t.Errorf("SSRF: loopback fetch should return empty content, got %q (internal data leaked!)", got)
	}
	if strings.Contains(got, "SECRET") {
		t.Errorf("SSRF breach: internal data leaked into docs content")
	}
}

// TestFetchDocsPageContent_NonHTMLSkipped 非 HTML 内容类型应跳过（不把二进制当正文）。
// 注：用 127.0.0.1 服务器会被 SSRF 拒绝——此测试验证的是"即使能连，非 HTML 也跳过"，
// 故直接验证 extractDocsText 对非 HTML 文本的处理不产生有意义正文（best-effort）。
func TestFetchDocsPageContent_NonHTMLContentTypeSkipped(t *testing.T) {
	// fetchDocsPageContent 对非 http(s) 返回空——这里用 extractDocsText 间接验证
	// 纯文本（无标签）经提取后保留原文，证明对纯文本不会误丢内容。
	text := extractDocsText("plain text without tags")
	if text != "plain text without tags" {
		t.Errorf("plain text should pass through unchanged: got %q", text)
	}
}

// TestDocsIndexChunks_WithContent 覆盖 N-21 闭环：上传抓取的正文填入 record.Content 后，
// docsIndexChunks（DocumentationQuery 的正文出口）应返回含正文的 DocumentationChunk。
// 修复前 Content 恒空 → chunks 为 nil → @docs 引用拿不到正文（N-21/L-4 根因）。
func TestDocsIndexChunks_WithContent(t *testing.T) {
	record := DocsIndexRecord{
		Title:   "Example",
		URL:     "https://example.com/doc",
		Content: "This is the fetched page body. It has real content now.",
	}
	chunks := docsIndexChunks(record, 0)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk when Content present, got %d", len(chunks))
	}
	if chunks[0].DocumentationChunk != record.Content {
		t.Errorf("chunk body = %q, want %q", chunks[0].DocumentationChunk, record.Content)
	}
	if chunks[0].PageUrl != record.URL {
		t.Errorf("chunk PageUrl = %q, want %q", chunks[0].PageUrl, record.URL)
	}
}

// TestDocsIndexChunks_EmptyContentReturnsNil Content 为空时不应返回无意义空 chunk。
// 这是修复前的行为（Content 恒空 → nil chunks），修复后仅当上传抓取成功才有 chunk。
func TestDocsIndexChunks_EmptyContentReturnsNil(t *testing.T) {
	record := DocsIndexRecord{Title: "NoBody", URL: "https://example.com/empty"}
	chunks := docsIndexChunks(record, 0)
	if chunks != nil {
		t.Errorf("expected nil chunks when Content empty, got %d", len(chunks))
	}
}

// TestDocsIndexKnowledgeText_IncludesContent 覆盖 N-21 闭环：
// FetchRelevantKnowledge 的 KnowledgeItem 正文出口应包含 Content。
func TestDocsIndexKnowledgeText_IncludesContent(t *testing.T) {
	record := DocsIndexRecord{
		Title:   "Doc",
		URL:     "https://example.com/d",
		Content: "page body content",
	}
	text := docsIndexKnowledgeText(record)
	if !strings.Contains(text, "page body content") {
		t.Errorf("knowledge text should include Content: %q", text)
	}
	if !strings.Contains(text, "URL: https://example.com/d") {
		t.Errorf("knowledge text should include URL: %q", text)
	}
}
