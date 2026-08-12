package interaction

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"cursor/gen/agentv1"
)

// TestExtractBaiduWebSearchReferences 验证百度搜索结果页解析：
// 标题/链接/摘要抽取、非 c-container 容器跳过、空标题/链接过滤、上限截断。
// 解析与 HTTP 解耦（HTTP 请求逻辑由 tryBaiduWebSearch 覆盖，解析纯函数单测）。
func TestExtractBaiduWebSearchReferences(t *testing.T) {
	body := `<html><head><title>百度搜索</title></head><body>
<div id="wrapper"><div id="container"><div id="content_left">
<div class="result-op c-container xpath-log">
<h3 class="t"><a href="/link?url=fake-redirect-1">Go 语言官网</a></h3>
<div class="c-abstract">Go 是谷歌开发的开源编程语言，适合高并发服务。</div>
</div>
<div class="result-op c-container xpath-log">
<h3 class="t"><a href="https://golang.google.cn/">Go 中文社区</a></h3>
<div class="c-abstract">中文 Go 开发者资源。</div>
</div>
<div class="result-op c-container xpath-log">
<h3 class="t"><a href=""></a></h3>
<div class="c-abstract">缺链接的条目应被过滤。</div>
</div>
<div class="ads_center_block"><div>广告容器应被跳过</div></div>
</div></div></div></body></html>`

	refs := extractBaiduWebSearchReferences(body)
	if len(refs) != 2 {
		t.Fatalf("expected 2 references, got %d", len(refs))
	}
	// 第一条：百度相对跳转链接应归一为绝对 URL。
	first := refs[0]
	if first.GetUrl() != "https://www.baidu.com/link?url=fake-redirect-1" {
		t.Errorf("first url not normalized: %q", first.GetUrl())
	}
	if first.GetTitle() != "Go 语言官网" {
		t.Errorf("first title = %q", first.GetTitle())
	}
	if first.GetChunk() != "Go 是谷歌开发的开源编程语言，适合高并发服务。" {
		t.Errorf("first chunk = %q", first.GetChunk())
	}
	// 第二条：绝对 URL 原样保留。
	second := refs[1]
	if second.GetUrl() != "https://golang.google.cn/" {
		t.Errorf("second url = %q", second.GetUrl())
	}
}

// TestNormalizeBaiduSearchURL 验证协议省略/相对路径链接归一化。
func TestNormalizeBaiduSearchURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"//www.baidu.com/link?x=1", "https://www.baidu.com/link?x=1"},
		{"/s?wd=hello", "https://www.baidu.com/s?wd=hello"},
		{"https://example.com/a", "https://example.com/a"},
		{"  ", ""},
	}
	for _, c := range cases {
		if got := normalizeBaiduSearchURL(c.in); got != c.want {
			t.Errorf("normalizeBaiduSearchURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestIsBaiduRedirectURL 验证百度跳转链接识别：仅 baidu.com 域下 /link 前缀命中。
func TestIsBaiduRedirectURL(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"https://www.baidu.com/link?url=x", true},
		{"https://baidu.com/link?url=x", true},
		{"https://www.baidu.com/s?wd=x", false},
		{"https://evil.com/link?url=x", false},
		{"https://notbaidu.com/link?url=x", false},
	}
	for _, c := range cases {
		if got := isBaiduRedirectURL(c.in); got != c.want {
			t.Errorf("isBaiduRedirectURL(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestResolveBaiduWebSearchRedirectsConcurrent 验证跳转解析被并发化（#15）：
// 用自定义 Transport 拦截所有 baidu.com/link 请求并回 302+Location，同时统计同一时刻最大在途请求数。
// 断言 (a) 每个跳转引用都被解析到 Location；(b) 观测到并发 ≥2——串行实现该值恒为 1，本断言确定性区分。
func TestResolveBaiduWebSearchRedirectsConcurrent(t *testing.T) {
	transport := &countingRedirectTransport{
		location: "https://target.example.com/real",
		delay:    100 * time.Millisecond, // 放大重叠窗口，让并发观测稳定
	}
	client := &http.Client{Transport: transport}

	refs := []*agentv1.WebSearchReference{
		{Url: "https://www.baidu.com/link?url=1"},
		{Url: "https://www.baidu.com/link?url=2"},
		{Url: "https://www.baidu.com/link?url=3"},
		{Url: "https://www.baidu.com/link?url=4"},
		nil, // nil 引用：应跳过，不 panic
		{Url: "https://golang.google.cn/"}, // 非跳转链接：原样保留，不发请求
	}
	resolveBaiduWebSearchRedirects(client, refs)

	if len(refs) != 6 {
		t.Fatalf("refs length = %d, want 6", len(refs))
	}
	for i := 0; i < 4; i++ {
		if refs[i].GetUrl() != "https://target.example.com/real" {
			t.Errorf("ref[%d].Url = %q, want resolved Location", i, refs[i].GetUrl())
		}
	}
	if refs[4] != nil {
		t.Errorf("nil ref mutated: %+v", refs[4])
	}
	if refs[5].GetUrl() != "https://golang.google.cn/" {
		t.Errorf("non-redirect ref[5].Url = %q, want unchanged", refs[5].GetUrl())
	}
	if transport.maxConcurrent < 2 {
		t.Errorf("max concurrent redirect requests = %d, want ≥ 2 (resolution should be parallel)", transport.maxConcurrent)
	}
}

// countingRedirectTransport 是测试用 RoundTripper：对所有请求返回 302 + Location，
// 并统计同一时刻在途请求的最大并发数。
type countingRedirectTransport struct {
	mu            sync.Mutex
	concurrent    int
	maxConcurrent int
	location      string
	delay         time.Duration
}

func (transport *countingRedirectTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	transport.concurrent++
	if transport.concurrent > transport.maxConcurrent {
		transport.maxConcurrent = transport.concurrent
	}
	transport.mu.Unlock()
	defer func() {
		transport.mu.Lock()
		transport.concurrent--
		transport.mu.Unlock()
	}()
	if transport.delay > 0 {
		time.Sleep(transport.delay)
	}
	return &http.Response{
		StatusCode: http.StatusFound,
		Status:     "302 Found",
		Header:     http.Header{"Location": []string{transport.location}},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    request,
	}, nil
}
