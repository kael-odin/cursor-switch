// healthz_exempt_test.go 锁定 LoopbackAuth 对健康检查端点的放行（审计 M10）。
// 此前 middleware 的 loopbackAuthExemptPath 与 host.go 的 healthPath 各定义一份"/healthz"，
// 靠注释保持一致——若一方改了，LoopbackAuth 会拦截 HealthCheck 探测导致误判后端宕机。
// 现统一到 server.HealthzPath 单一常量源。本测试断言：
//  1. 无 relay proof 的请求打到 HealthzPath → 放行（返回 200 ok，不要求 proof）；
//  2. 无 relay proof 的请求打到其他路径 → 拒绝（ErrRelayUnauthorized）。
package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoopbackAuthExemptsHealthzPath(t *testing.T) {
	mw := LoopbackAuth()
	handler := mw(func(ctx *Context) error {
		// 放行后落到此 handler，写 200 表示"健康检查端点未被拦截"。
		ctx.Writer.WriteHeader(http.StatusOK)
		_, _ = ctx.Writer.Write([]byte("ok"))
		return nil
	})

	// 1) HealthzPath 路径不带 relay proof 必须放行。
	req := httptest.NewRequest(http.MethodGet, HealthzPath, nil)
	rec := httptest.NewRecorder()
	ctx := &Context{Request: req, Writer: rec}
	if err := handler(ctx); err != nil {
		t.Fatalf("HealthzPath 不应被 LoopbackAuth 拦截，got err=%v", err)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("HealthzPath 应放行到 handler 返回 200 ok，got code=%d body=%q", rec.Code, rec.Body.String())
	}

	// 2) 非 HealthzPath 路径不带 relay proof 必须拒绝（ErrRelayUnauthorized）。
	req2 := httptest.NewRequest(http.MethodGet, "/aiserver.v1.AiService/RunSSE", nil)
	rec2 := httptest.NewRecorder()
	ctx2 := &Context{Request: req2, Writer: rec2}
	err := handler(ctx2)
	if err == nil {
		t.Fatal("非 HealthzPath 路径不带 relay proof 应被 LoopbackAuth 拒绝，got nil err")
	}
	if !errors.Is(err, ErrRelayUnauthorized) {
		t.Fatalf("非 HealthzPath 路径应返回 ErrRelayUnauthorized，got err=%v", err)
	}
}
