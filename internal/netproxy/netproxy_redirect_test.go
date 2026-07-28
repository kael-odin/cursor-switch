// netproxy_redirect_test.go 验证 F-22：NewHTTPClientNoRedirect 不跟随 3xx，
// 保护 x-api-key / 自定义认证头不泄漏到重定向目标。
package netproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestNewHTTPClientNoRedirectDoesNotFollow3xx 验证 NoRedirect 客户端收到 302 后
// 原样返回 302，不发起第二跳请求——首跳携带的认证头绝不会到达重定向目标。
func TestNewHTTPClientNoRedirectDoesNotFollow3xx(t *testing.T) {
	var redirectTargetHits int32
	// 第二跳目标：若客户端跟随了重定向，此 handler 会被命中（凭证泄漏证据）。
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&redirectTargetHits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "redirected")
	}))
	defer target.Close()

	// 首跳 server：返回 302 指向 target，并记录收到的认证头。
	var firstHit int32
	var gotAPIKey string
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&firstHit, 1)
		gotAPIKey = r.Header.Get("x-api-key")
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer first.Close()

	client := NewHTTPClientNoRedirect(5 * time.Second)
	req, err := http.NewRequest(http.MethodGet, first.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-api-key", "secret-key-should-not-leak")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 returned as-is, got %d", resp.StatusCode)
	}
	if atomic.LoadInt32(&firstHit) != 1 {
		t.Errorf("expected exactly 1 request to first server, got %d", firstHit)
	}
	if atomic.LoadInt32(&redirectTargetHits) != 0 {
		t.Errorf("F-22 FAIL: redirect target was hit %d times — credentials leaked to redirect destination",
			redirectTargetHits)
	}
	if gotAPIKey != "secret-key-should-not-leak" {
		t.Errorf("first server should have received x-api-key, got %q", gotAPIKey)
	}
}

// TestNewHTTPClientFollowsRedirectForComparison 反向验证：普通 NewHTTPClient
// 会跟随重定向（证明 NoRedirect 的必要性——默认行为确实会带凭证到第二跳）。
// 注意：Go 标准库跨 host 会剥离 Authorization/Cookie，但 x-api-key 不被保护。
func TestNewHTTPClientFollowsRedirectForComparison(t *testing.T) {
	var targetHits int32
	var targetGotAPIKey string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&targetHits, 1)
		targetGotAPIKey = r.Header.Get("x-api-key")
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer first.Close()

	client := NewHTTPClient(5 * time.Second)
	req, _ := http.NewRequest(http.MethodGet, first.URL, nil)
	req.Header.Set("x-api-key", "secret-leaks-here")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	if atomic.LoadInt32(&targetHits) != 1 {
		t.Fatalf("default client should follow redirect, target hits=%d", targetHits)
	}
	// 这正是 F-22 描述的漏洞：x-api-key 被带到重定向目标。
	if !strings.Contains(targetGotAPIKey, "secret-leaks-here") {
		t.Fatalf("expected x-api-key to leak to redirect target (proving F-22), got %q", targetGotAPIKey)
	}
}
