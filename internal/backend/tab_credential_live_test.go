package backend

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cursor/internal/backend/server"
	"cursor/internal/backend/server/upstream"
	"cursor/internal/logger"
)

// tabRecordingHTTPClient 是 upstream.HTTPClient 的可观测替身：记录每次 Do 调用与出站请求，
// 直接返回 200 不做真实网络。作为凭证策略的判别信号——CredentialNone 复用注入 client（会被调用），
// CredentialOriginalCursor 强制换 NoRedirect client（注入 client 不被调用）。
type tabRecordingHTTPClient struct {
	called  int
	lastReq *http.Request
}

func (c *tabRecordingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.called++
	c.lastReq = req.Clone(req.Context())
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

// TestTabServerCredentialPolicyLive (F-4) 验证 Tab 路由的凭证策略按请求时解析，而非路由构造期烘焙。
//
// 背景：tabServerUpstreamProcedure 在 rebuild 时构造一次路由，但运行中 config 变更
// （SaveUserConfigPatch/SaveConfig 仅在 httpServer==nil 时 rebuild）不重建路由。修复前
// resolveTabCredentialPolicy 在构造期调用一次并烘焙进 CompatRouteConfig.Credential——运行中
// 改变 TabServerBaseURL/TabUseCursorCredentials 后路由仍按旧策略转发：
//   - 先「留空 + 开关开」带本机 Cursor 凭证启动、后填自建 tab server → 请求重定向到第三方 server
//     却仍带构造期的 OriginalCursor（凭证外泄风险，虽 restore 对非 cursor host fail-closed 兜底）；
//   - 先填自建 server（构造期 None）、运行中清空切回官方 + 开关开 → 官方补全静默失效（零消耗、不可用）。
//
// 修复后 action 每请求重解析 resolveTabCredentialPolicy，与 TabServerBaseURL 的请求时读取同生命周期。
// 本测试用注入 client 的调用与否作为可观测信号：阶段 1（填地址→None）注入 client 必须被调用且出站
// 头剥离凭证；阶段 2 运行中清空地址切官方（→OriginalCursor），注入 client 不得再被调用。
func TestTabServerCredentialPolicyLive(t *testing.T) {
	host := newTestHost(t)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(func() {
		stopCancel()
		_ = host.Stop(stopCtx)
		// newTestHost 把 HOME/USERPROFILE 重定向到 t.TempDir()，logger 首次写日志会打开该 tmp 下
		// 的 app.log 并长期持有句柄，导致 TempDir RemoveAll 报 "being used by another process"。
		logger.CloseLogFile()
	})
	fake := &tabRecordingHTTPClient{}
	deps := upstream.Dependencies{HTTPClient: fake}
	app := server.New(tabServerUpstreamProcedure("/tab/cred-test", "tab_cred_test", false, server.HTTP(), deps, host.configs))
	ts := httptest.NewServer(app)
	defer ts.Close()

	post := func() *http.Response {
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/tab/cred-test", strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer local-cred")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do request: %v", err)
		}
		resp.Body.Close()
		return resp
	}

	// 阶段 1：构造期配置 = 填了自建 server + 开关开 → 凭证策略 CredentialNone。
	// 注入 client 必须被调用（None 复用注入 client），出站请求被重定向到自建 host 且 Authorization 被剥离。
	setRouting(t, host, "local", "https://tab.example.com", true)
	if resp := post(); resp.StatusCode != http.StatusOK {
		t.Fatalf("phase1 status = %d, want 200 (injected client responded)", resp.StatusCode)
	}
	if fake.called != 1 {
		t.Fatalf("phase1 injected client called = %d, want 1 (CredentialNone must use injected client)", fake.called)
	}
	if fake.lastReq == nil || fake.lastReq.URL == nil || fake.lastReq.URL.Host != "tab.example.com" {
		t.Fatalf("phase1 outbound host = %v, want tab.example.com (live baseURL redirect)", fake.lastReq.URL)
	}
	if auth := fake.lastReq.Header.Get("Authorization"); auth != "" {
		t.Fatalf("phase1 outbound Authorization = %q, want empty (no local credential to third-party server)", auth)
	}

	// 阶段 2：运行中清空 TabServerBaseURL 切回官方 + 开关开，不触发 rebuild（模拟运行中 SaveUserConfigPatch）。
	// 修复后 action 每请求重解析为 CredentialOriginalCursor → 强制 NoRedirect client → 注入 client 不再被调用，
	// 且目标非 cursor host 在校验处 fail-closed（502）。修复前构造期烘焙的 CredentialNone 仍生效 →
	// 注入 client 被二次调用（called==2、响应 200）→ 下述断言失败，即证明凭证策略未跟踪实时配置。
	setRouting(t, host, "local", "", true)
	resp := post()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("phase2 got 200 from injected client — stale CredentialNone still forwarding after config change (F-4)")
	}
	if fake.called != 1 {
		t.Fatalf("after runtime config change, injected client called = %d, want still 1 — "+
			"credential policy did not re-resolve from live config (F-4)", fake.called)
	}
}
