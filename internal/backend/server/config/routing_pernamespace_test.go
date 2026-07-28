// routing_pernamespace_test.go 验证审计第二部分「优先级 2」per-namespace 路由覆盖：
// Routing.PerNamespace 按 route name 覆盖全局 Mode，未列出回退全局，native 请求恒 local。
package config

import (
	"context"
	"testing"
)

// TestNormalizePerNamespaceCleansAndDropsAuto 验证归一化：合法值保留、非法/空丢弃、
// 全空时返回 nil（向后兼容旧配置无此字段，序列化不多空 map）。
func TestNormalizePerNamespaceCleansAndDropsAuto(t *testing.T) {
	out := normalizePerNamespace(map[string]string{
		"run_sse":           "upstream", // 合法
		"bidi_append":       "local",    // 合法
		"  repository_status  ": " upstream ", // 空白被 trim
		"empty":             "",          // 空值丢弃（语义=跟随全局）
		"auto_route":        "auto",      // "auto" 非合法枚举丢弃（跟随全局）
		"bogus":             "passthrough", // 非法值丢弃
		"":                  "upstream",  // 空 key 丢弃
	})
	if out == nil {
		t.Fatal("expected non-nil map with 3 valid entries")
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(out), out)
	}
	if out["run_sse"] != "upstream" {
		t.Errorf("run_sse = %q, want upstream", out["run_sse"])
	}
	if out["bidi_append"] != "local" {
		t.Errorf("bidi_append = %q, want local", out["bidi_append"])
	}
	// key 被 trim
	if _, ok := out["repository_status"]; !ok {
		t.Errorf("repository_status (trimmed) missing: %+v", out)
	}
}

// TestNormalizePerNamespaceNilForEmpty 验证空/nil 输入返回 nil（向后兼容）。
func TestNormalizePerNamespaceNilForEmpty(t *testing.T) {
	if got := normalizePerNamespace(nil); got != nil {
		t.Errorf("nil input = %+v, want nil", got)
	}
	if got := normalizePerNamespace(map[string]string{}); got != nil {
		t.Errorf("empty map = %+v, want nil", got)
	}
	if got := normalizePerNamespace(map[string]string{"x": "bogus"}); got != nil {
		t.Errorf("all-invalid = %+v, want nil", got)
	}
}

// TestRouteModeForOverrideBeatsGlobal 是 per-namespace 路由核心：
// 全局 Mode=local，但 run_sse 覆盖为 upstream → RouteModeFor(run_sse)=upstream，
// 未覆盖的 bidi_append 仍跟随全局 local。
func TestRouteModeForOverrideBeatsGlobal(t *testing.T) {
	store := NewStore(t.TempDir()+"/config.yaml", "")
	ctx := context.Background()
	if _, err := store.Save(ctx, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	// 全局 local + run_sse 单条覆盖 upstream。
	if _, err := manager.Update(ctx, func(cfg *Config) error {
		cfg.Routing.Mode = "local"
		cfg.Routing.PerNamespace = map[string]string{"run_sse": "upstream"}
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// hasUpstreamURL=true（MITM 转发请求）。
	if got := manager.RouteModeFor(true, "run_sse"); got != "upstream" {
		t.Errorf("RouteModeFor(run_sse) = %q, want upstream (override)", got)
	}
	// 未覆盖路由跟随全局 local。
	if got := manager.RouteModeFor(true, "bidi_append"); got != "local" {
		t.Errorf("RouteModeFor(bidi_append) = %q, want local (global fallback)", got)
	}
	// 空路由名也回退全局。
	if got := manager.RouteModeFor(true, ""); got != "local" {
		t.Errorf("RouteModeFor('') = %q, want local", got)
	}
}

// TestRouteModeForNativeRequestAlwaysLocal 验证 native 请求（无 UpstreamURL）恒 local，
// 即使该路由有 upstream 覆盖——本地直连没有上游可切。
func TestRouteModeForNativeRequestAlwaysLocal(t *testing.T) {
	store := NewStore(t.TempDir()+"/config.yaml", "")
	ctx := context.Background()
	if _, err := store.Save(ctx, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Update(ctx, func(cfg *Config) error {
		cfg.Routing.Mode = "upstream" // 全局都 upstream
		cfg.Routing.PerNamespace = map[string]string{"run_sse": "upstream"}
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// hasUpstreamURL=false → native，恒 local。
	if got := manager.RouteModeFor(false, "run_sse"); got != "local" {
		t.Errorf("native RouteModeFor(run_sse) = %q, want local", got)
	}
}

// TestRouteModeForGlobalUpstreamUnaffectedByOverride 验证全局 upstream 时，
// 单条覆盖为 local 的路由仍走 local（覆盖双向生效，不止 local→upstream）。
func TestRouteModeForGlobalUpstreamUnaffectedByOverride(t *testing.T) {
	store := NewStore(t.TempDir()+"/config.yaml", "")
	ctx := context.Background()
	if _, err := store.Save(ctx, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Update(ctx, func(cfg *Config) error {
		cfg.Routing.Mode = "upstream"
		// 全局直连，但 codebase 索引强制 byok 本地（用户不想把代码传 cursor 云）。
		cfg.Routing.PerNamespace = map[string]string{"repository_status": "local"}
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if got := manager.RouteModeFor(true, "repository_status"); got != "local" {
		t.Errorf("RouteModeFor(repository_status) = %q, want local (override beats global upstream)", got)
	}
	if got := manager.RouteModeFor(true, "run_sse"); got != "upstream" {
		t.Errorf("RouteModeFor(run_sse) = %q, want upstream (global)", got)
	}
}

// TestNormalizeConfigPersistsPerNamespace 验证 NormalizeConfig 透传并清洗 PerNamespace，
// 保证 Save/Load 往返不丢覆盖表。
func TestNormalizeConfigPersistsPerNamespace(t *testing.T) {
	normalized, err := NormalizeConfig(Config{
		Routing: RoutingConfig{
			Mode:         "local",
			PerNamespace: map[string]string{"run_sse": "upstream", "junk": "bogus"},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if normalized.Routing.PerNamespace == nil {
		t.Fatal("PerNamespace nil after normalize")
	}
	if got := normalized.Routing.PerNamespace["run_sse"]; got != "upstream" {
		t.Errorf("run_sse = %q, want upstream", got)
	}
	if _, ok := normalized.Routing.PerNamespace["junk"]; ok {
		t.Error("junk (illegal value) should be dropped by normalize")
	}
}

// TestNormalizeConfigEmptyPerNamespaceStaysNil 验证无覆盖时 PerNamespace 保持 nil，
// 不产生空 map（向后兼容：旧配置序列化形态不变）。
func TestNormalizeConfigEmptyPerNamespaceStaysNil(t *testing.T) {
	normalized, err := NormalizeConfig(Config{
		Routing: RoutingConfig{Mode: "local"},
	})
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if normalized.Routing.PerNamespace != nil {
		t.Errorf("PerNamespace = %+v, want nil for empty input", normalized.Routing.PerNamespace)
	}
}
