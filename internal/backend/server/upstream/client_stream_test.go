// client_stream_test.go 锁定流式推理面切 upstream 的超时修复：
// run_sse/bidi_append 切 upstream（CredentialOriginalCursor + Stream）必须用无端到端超时的
// 流式 NoRedirect 客户端，否则 http.Client.Timeout 到点会强制 cancel 截断长 SSE/Connect 流。
//
// 背景：http.Client.Timeout 是端到端超时（含读取响应体）。控制面短响应用 30s 没问题；
// 但流式推理响应体可持续数分钟，30s 到点会 cancel 在途流。修复：Stream 路径改用
// NewHTTPClientNoRedirectStream（Timeout=0 + transport 级 ResponseHeaderTimeout）。
package upstream

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cursor/internal/backend/server"
)

// assertNoRedirectClient 校验给定 client 不跟随 3xx（防凭证外泄），复用 client_redirect_test 的 302 场景。
func assertNoRedirectClient(t *testing.T, client HTTPClient) {
	t.Helper()
	status, targetHits := runClientAgainst302(t, client)
	if status != http.StatusFound || targetHits != 0 {
		t.Fatalf("streaming client must be NoRedirect (302, 0 target hits), got status=%d targetHits=%d",
			status, targetHits)
	}
}

// extractTimeout 从 HTTPClient 取底层 *http.Client 的 Timeout；非 *http.Client 类型则 fatal。
func extractTimeout(t *testing.T, client HTTPClient) time.Duration {
	t.Helper()
	concrete, ok := client.(*http.Client)
	if !ok {
		t.Fatalf("client is %T, want *http.Client to inspect Timeout", client)
	}
	return concrete.Timeout
}

// TestStreamCredentialOriginalCursorUsesNoEndToEndTimeout 是核心回归：
// CredentialOriginalCursor + Stream 必须返回 Timeout==0 的客户端（无端到端超时），
// 否则长推理流会在 30s 到点被截断。修复前该路径走 NewHTTPClientNoRedirect(30s)。
func TestStreamCredentialOriginalCursorUsesNoEndToEndTimeout(t *testing.T) {
	target := mustURL(t, "https://api2.cursor.sh/agent.v1.AgentService/RunSSE")
	reqCtx := &RequestContext{
		TargetURL:  target,
		Method:     http.MethodPost,
		Headers:    http.Header{},
		Request:    httptest.NewRequest(http.MethodPost, target.String(), nil),
		Deps:       &Dependencies{HTTPClient: injectFollowingClient()},
		Credentials: credsFor("https://api2.cursor.sh"),
	}

	_, client, err := buildUpstreamRequest(reqCtx, []byte{}, ForwardOptions{
		Credential: CredentialOriginalCursor,
		Stream:     true,
	})
	if err != nil {
		t.Fatalf("buildUpstreamRequest: %v", err)
	}

	if timeout := extractTimeout(t, client); timeout != 0 {
		t.Fatalf("streaming CredentialOriginalCursor client must have Timeout==0 (no end-to-end), got %v — "+
			"long SSE/Connect streams will be canceled at the timeout boundary", timeout)
	}

	// 仍须 NoRedirect：流式场景同样不能让 3xx 把凭证带到未校验目标。
	assertNoRedirectClient(t, client)
}

// TestNonStreamCredentialOriginalCursorKeeps30sTimeout 防过度修复回归：
// 非流式控制面接口（marketplace/账号等）CredentialOriginalCursor 仍须带 30s 端到端超时，
// 避免官方控制面转发因无超时而永久挂起。不能因为加了 Stream 分支就把所有 OriginalCursor 都改成无超时。
func TestNonStreamCredentialOriginalCursorKeeps30sTimeout(t *testing.T) {
	target := mustURL(t, "https://api2.cursor.sh/aiserver.v1.DashboardService/InstallUserPlugin")
	reqCtx := &RequestContext{
		TargetURL:  target,
		Method:     http.MethodPost,
		Headers:    http.Header{},
		Request:    httptest.NewRequest(http.MethodPost, target.String(), nil),
		Deps:       &Dependencies{HTTPClient: injectFollowingClient()},
		Credentials: credsFor("https://api2.cursor.sh"),
	}

	_, client, err := buildUpstreamRequest(reqCtx, []byte{}, ForwardOptions{
		Credential: CredentialOriginalCursor,
		Stream:     false,
	})
	if err != nil {
		t.Fatalf("buildUpstreamRequest: %v", err)
	}

	if timeout := extractTimeout(t, client); timeout != upstreamRedirectClientTimeout {
		t.Fatalf("non-streaming CredentialOriginalCursor must keep %v end-to-end timeout, got %v",
			upstreamRedirectClientTimeout, timeout)
	}
	assertNoRedirectClient(t, client)
}

// TestStreamCredentialNoneUsesInjectedClient 防 Stream 标志被误用于无凭证路径：
// CredentialNone + Stream 不应触发流式专用客户端（那条路径只对 OriginalCursor 有意义），
// 仍应复用注入的普通 client，保持 provider 链等正常重定向行为。
func TestStreamCredentialNoneUsesInjectedClient(t *testing.T) {
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

	_, client, err := buildUpstreamRequest(reqCtx, []byte{}, ForwardOptions{
		Credential: CredentialNone,
		Stream:     true,
	})
	if err != nil {
		t.Fatalf("buildUpstreamRequest: %v", err)
	}
	if client != injected {
		t.Fatal("CredentialNone+Stream should reuse injected client (Stream only matters for OriginalCursor)")
	}
	// 复用注入的普通 client：会跟随重定向（200, 1 target hit），证明没被错误地切到 NoRedirect。
	status, targetHits := runClientAgainst302(t, client)
	if status != http.StatusOK || targetHits != 1 {
		t.Fatalf("expected injected following client (200, 1 target hit), got status=%d targetHits=%d",
			status, targetHits)
	}
}
