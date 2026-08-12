package interaction

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// bingHTMLFixture 是必应搜索结果页的 HTML 片段，覆盖三条解析路径：
// .b_caption p（摘要）、.b_snippet（摘要）、空 href（应跳过）。含非 b_algo 节点验证不误收。
const bingHTMLFixture = `<!DOCTYPE html><html><body>
<ol id="b_results">
  <li class="b_algo">
    <h2><a href="https://go.dev/">The Go Programming Language</a></h2>
    <div class="b_caption"><p>Go is an open source programming language that makes it easy to build simple, reliable, and efficient software.</p></div>
  </li>
  <li class="b_algo">
    <h2><a href="https://golang.org/doc/">Go Documentation</a></h2>
    <div class="b_snippet">Official documentation for the Go programming language.</div>
  </li>
  <li class="b_algo">
    <h2><a href="">Empty URL result</a></h2>
    <p>This entry should be skipped.</p>
  </li>
  <li class="b_pag">
    <h2><a href="https://example.com/pagination">Pagination</a></h2>
  </li>
</ol>
</body></html>`

// TestParseBingHTMLReferences 验证必应 HTML 解析：li.b_algo → h2 a（标题/URL）、
// .b_caption p / .b_snippet（摘要），空 href 跳过，非 b_algo 节点不误收。纯函数单测。
func TestParseBingHTMLReferences(t *testing.T) {
	refs := parseBingHTMLReferences(bingHTMLFixture)
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(refs))
	}
	first := refs[0]
	if first.GetTitle() != "The Go Programming Language" {
		t.Errorf("first title = %q", first.GetTitle())
	}
	if first.GetUrl() != "https://go.dev/" {
		t.Errorf("first url = %q", first.GetUrl())
	}
	if first.GetChunk() != "Go is an open source programming language that makes it easy to build simple, reliable, and efficient software." {
		t.Errorf("first chunk = %q", first.GetChunk())
	}
	second := refs[1]
	if second.GetTitle() != "Go Documentation" || second.GetUrl() != "https://golang.org/doc/" {
		t.Errorf("second mismatch: %+v", second)
	}
	if second.GetChunk() != "Official documentation for the Go programming language." {
		t.Errorf("second chunk (b_snippet) = %q", second.GetChunk())
	}
}

// TestParseBingHTMLReferencesNoResults 验证无 b_algo 的页面解析结果为空（触发链接管继续）。
func TestParseBingHTMLReferencesNoResults(t *testing.T) {
	if refs := parseBingHTMLReferences("<html><body><p>no results</p></body></html>"); len(refs) != 0 {
		t.Fatalf("expected 0 refs for empty page, got %d", len(refs))
	}
}

// TestNormalizeBingSearchURL 验证协议省略/相对链接归一化（对齐 amadeus），
// 防必应返回非绝对 URL 时产出畸形引用。
func TestNormalizeBingSearchURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"//www.bing.com/ck/a?x=1", "https://www.bing.com/ck/a?x=1"},
		{"/search?q=hello", "https://www.bing.com/search?q=hello"},
		{"https://example.com/a", "https://example.com/a"},
		{"  ", ""},
	}
	for _, c := range cases {
		if got := normalizeBingSearchURL(c.in); got != c.want {
			t.Errorf("normalizeBingSearchURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestExecuteBingHTMLSearch 验证免 key 必应 HTML 搜索：注入返回 200+HTML 的 transport，
// 断言解析成功且结果 ≤ 5（与 limitWebSearchReferences 口径一致）。
func TestExecuteBingHTMLSearch(t *testing.T) {
	client := &http.Client{Transport: &staticSearchTransport{status: 200, body: bingHTMLFixture}}
	refs, payload, err := executeBingHTMLSearch(client, "golang")
	if err != nil {
		t.Fatalf("executeBingHTMLSearch: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %d, want 2", len(refs))
	}
	if !strings.Contains(payload, "The Go Programming Language") {
		t.Errorf("payload missing result title: %q", payload)
	}
}

// TestExecuteBingHTMLSearchErrors 验证 HTTP 错误 / 无结果页面返回错误（不静默空），
// 由免 key 链接管继续尝试下一引擎。
func TestExecuteBingHTMLSearchErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"http error", 403, ""},
		{"no results", 200, "<html><body>empty</body></html>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: &staticSearchTransport{status: tc.status, body: tc.body}}
			if _, _, err := executeBingHTMLSearch(client, "golang"); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// staticSearchTransport 是测试用 RoundTripper：所有请求返回固定 status + body。
type staticSearchTransport struct {
	status int
	body   string
}

func (transport *staticSearchTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: transport.status,
		Status:     http.StatusText(transport.status),
		Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       io.NopCloser(strings.NewReader(transport.body)),
		Request:    request,
	}, nil
}
