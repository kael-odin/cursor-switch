// policy_pernamespace_test.go 验证 PolicyMiddleware 按 ctx.RouteName 应用 per-namespace
// 路由覆盖（审计第二部分「优先级 2」）：覆盖表里的路由走 override，未覆盖的走全局 Mode。
package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	serverconfig "cursor/internal/backend/server/config"
)

// newManagerWithPerNamespace 构一个全局 mode + per-namespace 覆盖的 manager。
func newManagerWithPerNamespace(t *testing.T, globalMode string, overrides map[string]string) *serverconfig.Manager {
	t.Helper()
	store := serverconfig.NewStore(t.TempDir()+"/config.yaml", "")
	ctx := context.Background()
	if _, err := store.Save(ctx, serverconfig.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	mgr, err := serverconfig.NewManager(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Update(ctx, func(cfg *serverconfig.Config) error {
		cfg.Routing.Mode = globalMode
		cfg.Routing.PerNamespace = overrides
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	return mgr
}

func newPolicyCtx(routeName string, hasUpstream bool) *Context {
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	ctx := &Context{
		Request:   req,
		RouteName: routeName,
		Mode:      ModeLocal,
	}
	if hasUpstream {
		// PolicyMiddleware 只看 ctx.UpstreamURL != nil，不解析 header；直接构造即可。
		req.Header.Set(HeaderServerUpstreamURL, "https://api2.cursor.sh")
		u, err := url.Parse("https://api2.cursor.sh")
		if err != nil {
			panic(err)
		}
		ctx.UpstreamURL = u
	}
	return ctx
}

// TestPolicyMiddlewareAppliesPerNamespaceOverride 是核心：
// 全局 local + run_sse 覆盖 upstream → ctx.Mode 按 RouteName 分别得到 upstream / local。
func TestPolicyMiddlewareAppliesPerNamespaceOverride(t *testing.T) {
	mgr := newManagerWithPerNamespace(t, "local", map[string]string{"run_sse": "upstream"})
	mw := PolicyMiddleware(mgr)

	// run_sse 路由 → 覆盖为 upstream。
	ctxRunSSE := newPolicyCtx("run_sse", true)
	mw(func(c *Context) error { return nil })(ctxRunSSE)
	if ctxRunSSE.Mode != ModeUpstream {
		t.Errorf("run_sse Mode = %s, want upstream (override)", ctxRunSSE.Mode)
	}

	// 未覆盖的 bidi_append → 跟随全局 local。
	ctxBidi := newPolicyCtx("bidi_append", true)
	mw(func(c *Context) error { return nil })(ctxBidi)
	if ctxBidi.Mode != ModeLocal {
		t.Errorf("bidi_append Mode = %s, want local (global fallback)", ctxBidi.Mode)
	}
}

// TestPolicyMiddlewareNativeRequestAlwaysLocal 验证 native 请求（无 UpstreamURL）
// 恒 local，即使路由有 upstream 覆盖——本地直连没有上游可切。
func TestPolicyMiddlewareNativeRequestAlwaysLocal(t *testing.T) {
	mgr := newManagerWithPerNamespace(t, "upstream", map[string]string{"run_sse": "upstream"})
	mw := PolicyMiddleware(mgr)

	ctx := newPolicyCtx("run_sse", false) // 无 UpstreamURL = native
	mw(func(c *Context) error { return nil })(ctx)
	if ctx.Mode != ModeLocal {
		t.Errorf("native run_sse Mode = %s, want local", ctx.Mode)
	}
}

// TestPolicyMiddlewareOverrideLocalAgainstGlobalUpstream 验证覆盖双向：
// 全局 upstream 时单条覆盖 local 仍生效（用户想全局直连但某面强制 byok）。
func TestPolicyMiddlewareOverrideLocalAgainstGlobalUpstream(t *testing.T) {
	mgr := newManagerWithPerNamespace(t, "upstream", map[string]string{"repository_status": "local"})
	mw := PolicyMiddleware(mgr)

	ctxRepo := newPolicyCtx("repository_status", true)
	mw(func(c *Context) error { return nil })(ctxRepo)
	if ctxRepo.Mode != ModeLocal {
		t.Errorf("repository_status Mode = %s, want local (override beats global upstream)", ctxRepo.Mode)
	}

	ctxSSE := newPolicyCtx("run_sse", true)
	mw(func(c *Context) error { return nil })(ctxSSE)
	if ctxSSE.Mode != ModeUpstream {
		t.Errorf("run_sse Mode = %s, want upstream (global)", ctxSSE.Mode)
	}
}

// TestPolicyMiddlewareEmptyRouteNameFallsBackGlobal 验证空 RouteName（未命名路由）
// 不命中覆盖表，回退全局。
func TestPolicyMiddlewareEmptyRouteNameFallsBackGlobal(t *testing.T) {
	mgr := newManagerWithPerNamespace(t, "local", map[string]string{"run_sse": "upstream"})
	mw := PolicyMiddleware(mgr)

	ctx := newPolicyCtx("", true)
	mw(func(c *Context) error { return nil })(ctx)
	if ctx.Mode != ModeLocal {
		t.Errorf("empty RouteName Mode = %s, want local (global fallback)", ctx.Mode)
	}
}
