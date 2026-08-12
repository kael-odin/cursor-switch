package interaction

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// TestFreeSearchChainOrder 验证免 key 链的实际执行顺序：链首在前，其余按固定兜底顺序补位；
// 未知/空链首归一为 duckduckgo。三个可用链首恰好复现 handoff 的两条默认链：
// baidu 链首 = 中文优先（百度→DDG→必应），bing 链首 = 混搜（必应→DDG→百度）。
func TestFreeSearchChainOrder(t *testing.T) {
	cases := []struct {
		head string
		want []string
	}{
		{"duckduckgo", []string{"duckduckgo", "bing", "baidu"}},
		{"bing", []string{"bing", "duckduckgo", "baidu"}},
		{"baidu", []string{"baidu", "duckduckgo", "bing"}},
		{"", []string{"duckduckgo", "bing", "baidu"}},
		{"unknown", []string{"duckduckgo", "bing", "baidu"}},
	}
	for _, c := range cases {
		got := freeWebSearchChain(c.head)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("freeWebSearchChain(%q) = %v, want %v", c.head, got, c.want)
		}
	}
}

// TestDispatchWebSearchFreeChainFallback 验证跨引擎链式回退（免 key 层核心行为）：
// duckduckgo 链首，DDG Instant Answer JSON 403 → DDG HTML 403 → bing HTML 200 胜出。
// 注入脚本化 transport 精确编排三次请求，断言返回的是 bing 结果且恰好 3 次请求——
// 确定性证明「首个返回非空结果的引擎胜出」，而非单引擎静默失败。
func TestDispatchWebSearchFreeChainFallback(t *testing.T) {
	transport := &scriptedSearchTransport{steps: []scriptedSearchStep{
		{status: 403, body: "ddg json blocked"},
		{status: 403, body: "ddg html blocked"},
		{status: 200, body: bingHTMLFixture},
	}}
	bridge := NewBridge(func() WebToolsConfig {
		return WebToolsConfig{WebSearchProvider: "duckduckgo"}
	})
	bridge.httpClient = &http.Client{Transport: transport}

	refs, payload, err := bridge.dispatchWebSearch("golang")
	if err != nil {
		t.Fatalf("dispatchWebSearch: %v", err)
	}
	if transport.callCount() != 3 {
		t.Errorf("requests = %d, want exactly 3 (DDG JSON, DDG HTML, bing HTML)", transport.callCount())
	}
	if len(refs) != 2 || refs[0].GetUrl() != "https://go.dev/" {
		t.Fatalf("expected bing fallback results, got %+v", refs)
	}
	if !strings.Contains(payload, "The Go Programming Language") {
		t.Errorf("payload not from bing engine: %q", payload)
	}
}

// TestDispatchWebSearchFreeChainAllFail 验证全链失败返回聚合错误（含各引擎原因），
// 绝不静默返回空。duckduckgo 引擎两次请求（JSON+HTML）后 bing、baidu 依次失败。
func TestDispatchWebSearchFreeChainAllFail(t *testing.T) {
	transport := &scriptedSearchTransport{} // 所有请求 500
	bridge := NewBridge(func() WebToolsConfig {
		return WebToolsConfig{WebSearchProvider: "duckduckgo"}
	})
	bridge.httpClient = &http.Client{Transport: transport}

	_, _, err := bridge.dispatchWebSearch("golang")
	if err == nil {
		t.Fatal("expected aggregate error, got nil")
	}
	if !strings.Contains(err.Error(), "failed on all free engines") {
		t.Fatalf("error not aggregate: %v", err)
	}
	for _, engine := range []string{"duckduckgo", "bing", "baidu"} {
		if !strings.Contains(err.Error(), engine) {
			t.Errorf("aggregate error missing engine %q: %v", engine, err)
		}
	}
	if transport.callCount() != 4 {
		t.Errorf("requests = %d, want 4 (DDG JSON+HTML, bing, baidu)", transport.callCount())
	}
}

// TestDispatchWebSearchBingNoKeyUsesFreeHTML 验证 bing 双模：无 key 时不再报缺 key，
// 而是走免 key HTML 抓取作为链首（恰 1 次请求即胜出，不触发后续引擎）。
func TestDispatchWebSearchBingNoKeyUsesFreeHTML(t *testing.T) {
	transport := &scriptedSearchTransport{steps: []scriptedSearchStep{
		{status: 200, body: bingHTMLFixture},
	}}
	bridge := NewBridge(func() WebToolsConfig {
		return WebToolsConfig{WebSearchProvider: "bing"} // 无 key
	})
	bridge.httpClient = &http.Client{Transport: transport}

	refs, _, err := bridge.dispatchWebSearch("golang")
	if err != nil {
		t.Fatalf("dispatchWebSearch: %v", err)
	}
	if errors.Is(err, errWebSearchAPIKeyMissing) {
		t.Fatal("bing without key should not report missing key")
	}
	if transport.callCount() != 1 {
		t.Errorf("requests = %d, want 1 (bing HTML as chain head)", transport.callCount())
	}
	if len(refs) != 2 {
		t.Fatalf("expected bing HTML results, got %d", len(refs))
	}
}

// TestDispatchWebSearchBingWithKeyStillLoud 验证 bing 双模的有 key 分支仍走官方 API，
// 且失败响亮报错（不回退免费层）：注入 403 → 错误非空且非链聚合错误。
func TestDispatchWebSearchBingWithKeyStillLoud(t *testing.T) {
	transport := &scriptedSearchTransport{steps: []scriptedSearchStep{
		{status: 403, body: "bing api blocked"},
	}}
	bridge := NewBridge(func() WebToolsConfig {
		return WebToolsConfig{WebSearchProvider: "bing", WebSearchAPIKey: "key-123"}
	})
	bridge.httpClient = &http.Client{Transport: transport}

	_, _, err := bridge.dispatchWebSearch("golang")
	if err == nil {
		t.Fatal("expected loud error from bing API, got nil")
	}
	if strings.Contains(err.Error(), "failed on all free engines") {
		t.Fatalf("bing-with-key must not fall back to free chain: %v", err)
	}
	if transport.callCount() != 1 {
		t.Errorf("requests = %d, want 1 (single bing API call)", transport.callCount())
	}
}

// scriptedSearchStep 是一次请求的预置响应。
type scriptedSearchStep struct {
	status int
	body   string
}

// scriptedSearchTransport 是测试用 RoundTripper：按请求序号依次返回预置响应，
// 越界请求返回 500，并统计总请求数。用于精确编排免 key 链的多次请求。
type scriptedSearchTransport struct {
	mu    sync.Mutex
	calls int
	steps []scriptedSearchStep
}

func (transport *scriptedSearchTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	idx := transport.calls
	transport.calls++
	step := scriptedSearchStep{status: 500, body: "unexpected request"}
	if idx < len(transport.steps) {
		step = transport.steps[idx]
	}
	transport.mu.Unlock()
	return &http.Response{
		StatusCode: step.status,
		Status:     fmt.Sprintf("%d", step.status),
		Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       io.NopCloser(strings.NewReader(step.body)),
		Request:    request,
	}, nil
}

func (transport *scriptedSearchTransport) callCount() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.calls
}
