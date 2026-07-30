// tab_credential_test.go 锁定 Tab 补全「留空带凭证走官方」开关（TabUseCursorCredentials）
// 的凭证选择逻辑：仅留空 + 开关开才带真实 Cursor 凭证；填了自建 tab server 时强制不带本机凭证。
package backend

import (
	"context"
	"testing"

	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/backend/server/upstream"
)

// setRouting 通过 manager.Update 设置 Routing 字段，返回更新后的 manager（host.configs 同源）。
func setRouting(t *testing.T, host *Host, mode, tabServerBaseURL string, useCursorCreds bool) {
	t.Helper()
	if _, err := host.configs.Update(context.Background(), func(cfg *serverconfig.Config) error {
		cfg.Routing.Mode = mode
		cfg.Routing.TabServerBaseURL = tabServerBaseURL
		cfg.Routing.TabUseCursorCredentials = useCursorCreds
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// TestResolveTabCredentialEmptyAndFlagOff 是默认行为：留空 + 开关关 → CredentialNone。
// 即留空转发到官方 api2.cursor.sh 时不带凭证（官方 401，补全不可用，零消耗额度）——维持历史现状。
func TestResolveTabCredentialEmptyAndFlagOff(t *testing.T) {
	host := newTestHost(t)
	setRouting(t, host, "local", "", false)
	if got := resolveTabCredentialPolicy(host.configs); got != upstream.CredentialNone {
		t.Fatalf("empty baseURL + flag off: credential = %v, want CredentialNone (no creds leaked, status quo)", got)
	}
}

// TestResolveTabCredentialEmptyAndFlagOn 是用户开启开关的核心场景：留空 + 开关开 →
// CredentialOriginalCursor。留空时目标是官方 cursor.sh，严格校验能过，补全带真实凭证可用（消耗账号额度）。
func TestResolveTabCredentialEmptyAndFlagOn(t *testing.T) {
	host := newTestHost(t)
	setRouting(t, host, "local", "", true)
	if got := resolveTabCredentialPolicy(host.configs); got != upstream.CredentialOriginalCursor {
		t.Fatalf("empty baseURL + flag on: credential = %v, want CredentialOriginalCursor (carry cursor creds to official)", got)
	}
}

// TestResolveTabCredentialFilledServerIgnoresFlag 是安全铁律：填了自建 tab server 时，
// 无论开关开/关都强制 CredentialNone——绝不让本机 Cursor token 送到第三方 server。
// server 端自带账号回源，不需要也不应携带本机凭证。
func TestResolveTabCredentialFilledServerIgnoresFlag(t *testing.T) {
	host := newTestHost(t)
	// 开关开但填了地址：仍 CredentialNone（开关对自建 server 路径无效）。
	setRouting(t, host, "local", "https://tab.example.com", true)
	if got := resolveTabCredentialPolicy(host.configs); got != upstream.CredentialNone {
		t.Fatalf("filled tab server + flag on: credential = %v, want CredentialNone (never send local token to 3rd-party server)", got)
	}
	// 开关关 + 填地址：同样 CredentialNone。
	setRouting(t, host, "local", "https://tab.example.com", false)
	if got := resolveTabCredentialPolicy(host.configs); got != upstream.CredentialNone {
		t.Fatalf("filled tab server + flag off: credential = %v, want CredentialNone", got)
	}
}

// TestResolveTabCredentialNilConfigs 防御：configs 为 nil 时（理论上不会发生）安全回退到 CredentialNone。
func TestResolveTabCredentialNilConfigs(t *testing.T) {
	if got := resolveTabCredentialPolicy(nil); got != upstream.CredentialNone {
		t.Fatalf("nil configs: credential = %v, want CredentialNone (fail-safe)", got)
	}
}

// TestTabUseCursorCredentialsNormalizeRoundTrip 验证新字段经 NormalizeConfig 与 merge 透传：
// 用户保存的开关值不被归一化清掉，merge 时整体替换 dst。
func TestTabUseCursorCredentialsNormalizeRoundTrip(t *testing.T) {
	in := serverconfig.Config{
		Routing: serverconfig.RoutingConfig{
			Mode:                    "local",
			TabUseCursorCredentials: true,
		},
	}
	out, err := serverconfig.NormalizeConfig(in)
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if !out.Routing.TabUseCursorCredentials {
		t.Fatal("NormalizeConfig dropped TabUseCursorCredentials=true")
	}
	// 默认 false。
	def := serverconfig.DefaultConfig()
	if def.Routing.TabUseCursorCredentials {
		t.Fatal("DefaultConfig TabUseCursorCredentials should be false")
	}
}
