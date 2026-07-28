// client_redirect_test.go 验证 F-09：CredentialOriginalCursor 策略下，
// 即使 Deps.HTTPClient 注入了会跟随重定向的普通 client，buildUpstreamRequest
// 也必须返回 NoRedirect client，杜绝凭证随 3xx 外泄。
package upstream

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"cursor/internal/backend/server"
	"cursor/internal/netproxy"
)

// injectFollowingClient 模拟 Host.rebuildLocked 注入的 netproxy.NewHTTPClient
// （会跟随重定向），用于证明 F-09：注入存在时 NoRedirect 仍生效。
func injectFollowingClient() HTTPClient {
	return netproxy.NewHTTPClient(0)
}

// runClientAgainst302 用给定 client 打 first（返回 302 指 target），
// 返回 (finalStatus, targetHits)。
// NoRedirect client：finalStatus=302、targetHits=0。
// 普通跟随 client：finalStatus=200、targetHits=1。
func runClientAgainst302(t *testing.T, client HTTPClient) (int, int32) {
	t.Helper()
	var targetHits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&targetHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer first.Close()

	req, err := http.NewRequest(http.MethodGet, first.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("x-cursor-checksum", "should-not-reach-target")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode, atomic.LoadInt32(&targetHits)
}

// TestF09CredentialOriginalCursorForcesNoRedirectDespiteInjectedClient 是 F-09 核心回归：
// Deps 注入了跟随重定向的普通 client，但 CredentialOriginalCursor 必须返回 NoRedirect client。
// 修复前 buildUpstreamRequest 取 reqCtx.Deps.HTTPClient（跟随重定向），302 会被跟随，
// x-cursor-checksum 等非标准头带到第二跳；修复后策略优先于注入，无条件 NoRedirect。
func TestF09CredentialOriginalCursorForcesNoRedirectDespiteInjectedClient(t *testing.T) {
	target := mustURL(t, "https://api2.cursor.sh/aiserver.v1.DashboardService/InstallUserPlugin")
	reqCtx := &RequestContext{
		TargetURL:  target,
		Method:     http.MethodPost,
		Headers:    http.Header{},
		Request:    httptest.NewRequest(http.MethodPost, target.String(), nil),
		Deps:       &Dependencies{HTTPClient: injectFollowingClient()},
		Credentials: credsFor("https://api2.cursor.sh"),
	}

	_, client, err := buildUpstreamRequest(reqCtx, []byte{}, ForwardOptions{Credential: CredentialOriginalCursor})
	if err != nil {
		t.Fatalf("buildUpstreamRequest: %v", err)
	}

	status, targetHits := runClientAgainst302(t, client)
	if status != http.StatusFound {
		t.Fatalf("F-09 FAIL: expected 302 returned as-is (NoRedirect), got %d — injected client leaked through",
			status)
	}
	if targetHits != 0 {
		t.Fatalf("F-09 FAIL: redirect target hit %d times — x-cursor-checksum leaked to unverified destination",
			targetHits)
	}
}

// TestF09CredentialOriginalCursorNoRedirectEvenWithoutInjectedClient 验证未注入 client 时
// CredentialOriginalCursor 同样走 NoRedirect（修复前的 nil 分支仍生效，无回归）。
func TestF09CredentialOriginalCursorNoRedirectEvenWithoutInjectedClient(t *testing.T) {
	target := mustURL(t, "https://api2.cursor.sh/aiserver.v1.DashboardService/InstallUserPlugin")
	reqCtx := &RequestContext{
		TargetURL:  target,
		Method:     http.MethodPost,
		Headers:    http.Header{},
		Request:    httptest.NewRequest(http.MethodPost, target.String(), nil),
		Deps:       &Dependencies{}, // HTTPClient 未注入
		Credentials: credsFor("https://api2.cursor.sh"),
	}

	_, client, err := buildUpstreamRequest(reqCtx, []byte{}, ForwardOptions{Credential: CredentialOriginalCursor})
	if err != nil {
		t.Fatalf("buildUpstreamRequest: %v", err)
	}

	status, targetHits := runClientAgainst302(t, client)
	if status != http.StatusFound || targetHits != 0 {
		t.Fatalf("expected NoRedirect (302, 0 target hits), got status=%d targetHits=%d", status, targetHits)
	}
}

// TestF09CredentialNoneUsesInjectedClient 验证非 OriginalCursor 策略仍用注入 client
// （回归保护：不能把所有路径都改成 NoRedirect，否则 provider 链等正常重定向会断）。
func TestF09CredentialNoneUsesInjectedClient(t *testing.T) {
	target := mustURL(t, "https://api2.cursor.sh/x")
	injected := injectFollowingClient()
	reqCtx := &RequestContext{
		TargetURL:  target,
		Method:    http.MethodGet,
		Headers:   http.Header{},
		Request:   httptest.NewRequest(http.MethodGet, target.String(), nil),
		Deps:      &Dependencies{HTTPClient: injected},
		Credentials: server.CapturedCredentials{},
	}

	_, client, err := buildUpstreamRequest(reqCtx, []byte{}, ForwardOptions{Credential: CredentialNone})
	if err != nil {
		t.Fatalf("buildUpstreamRequest: %v", err)
	}

	// CredentialNone 应复用注入的跟随重定向 client（普通转发链允许 3xx）。
	if client != injected {
		t.Fatal("CredentialNone should reuse injected client, not replace it")
	}
	status, targetHits := runClientAgainst302(t, client)
	if status != http.StatusOK || targetHits != 1 {
		t.Fatalf("expected injected following client (200, 1 target hit), got status=%d targetHits=%d",
			status, targetHits)
	}
}

// TestF09CredentialNoneFallbackCreatesFollowingClient 验证无注入时 CredentialNone
// 生成跟随重定向 client（兜底分支保持原语义，不是 NoRedirect）。
func TestF09CredentialNoneFallbackCreatesFollowingClient(t *testing.T) {
	target := mustURL(t, "https://api2.cursor.sh/x")
	reqCtx := &RequestContext{
		TargetURL:  target,
		Method:    http.MethodGet,
		Headers:   http.Header{},
		Request:   httptest.NewRequest(http.MethodGet, target.String(), nil),
		Deps:      &Dependencies{}, // 无注入
		Credentials: server.CapturedCredentials{},
	}

	_, client, err := buildUpstreamRequest(reqCtx, []byte{}, ForwardOptions{Credential: CredentialNone})
	if err != nil {
		t.Fatalf("buildUpstreamRequest: %v", err)
	}

	status, targetHits := runClientAgainst302(t, client)
	if status != http.StatusOK || targetHits != 1 {
		t.Fatalf("expected fallback following client (200, 1 target hit), got status=%d targetHits=%d",
			status, targetHits)
	}
}
