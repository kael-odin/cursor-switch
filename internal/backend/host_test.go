package backend

import (
	"context"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cursor/internal/appdata"
	serverconfig "cursor/internal/backend/server/config"
)

// TestResolveTabUpstreamURL 是 H1 的回归测试：tab server 地址可配置，
// 空地址时不重写 URL（透传官方 api2.cursor.sh），非空时改写 scheme/host 保留 path/query。
func TestResolveTabUpstreamURL(t *testing.T) {
	req, _ := url.Parse("https://api2.cursor.sh/aiserver.v1.AiService/StreamCpp?foo=bar")

	// 空地址（默认）：返回 nil，调用方不重写 UpstreamURL，透传官方。
	if got := resolveTabUpstreamURL(req, ""); got != nil {
		t.Errorf("empty baseURL: got %v, want nil (passthrough to official)", got)
	}
	if got := resolveTabUpstreamURL(req, "   "); got != nil {
		t.Errorf("whitespace baseURL: got %v, want nil", got)
	}

	// 非空地址：改写 scheme+host，保留 path+query。
	got := resolveTabUpstreamURL(req, "https://tab.example.com:8443")
	if got == nil {
		t.Fatalf("non-empty baseURL: got nil, want rewritten URL")
	}
	if got.Scheme != "https" {
		t.Errorf("scheme = %q, want https", got.Scheme)
	}
	if got.Host != "tab.example.com:8443" {
		t.Errorf("host = %q, want tab.example.com:8443", got.Host)
	}
	if got.Path != "/aiserver.v1.AiService/StreamCpp" {
		t.Errorf("path = %q, want preserved", got.Path)
	}
	if got.RawQuery != "foo=bar" {
		t.Errorf("query = %q, want preserved", got.RawQuery)
	}

	// 非法地址：返回 nil 透传兜底（不 panic、不报错）。
	if got := resolveTabUpstreamURL(req, "://bad-url"); got != nil {
		t.Errorf("invalid baseURL: got %v, want nil (fallback)", got)
	}
}

// TestRoutingTabServerBaseURLNormalized 验证 NormalizeConfig 对 TabServerBaseURL 做 trim。
func TestRoutingTabServerBaseURLNormalized(t *testing.T) {
	in := serverconfig.Config{
		Routing: serverconfig.RoutingConfig{
			Mode:           "local",
			TabServerBaseURL: "  https://tab.example.com  ",
		},
	}
	out, err := serverconfig.NormalizeConfig(in)
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if out.Routing.TabServerBaseURL != "https://tab.example.com" {
		t.Errorf("normalized TabServerBaseURL = %q, want trimmed", out.Routing.TabServerBaseURL)
	}

	// 默认（空）= 禁用重定向。
	def := serverconfig.DefaultConfig()
	if def.Routing.TabServerBaseURL != "" {
		t.Errorf("DefaultConfig TabServerBaseURL = %q, want empty (disabled by default)", def.Routing.TabServerBaseURL)
	}
}

// newTestHost 构造一个指向临时配置目录的 Host，用于 F-35 并发测试。
func newTestHost(t *testing.T) *Host {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp) // appdata.RootDir 从 HOME 推导
	if err := appdata.EnsureAssistantHome(); err != nil {
		t.Fatalf("EnsureAssistantHome: %v", err)
	}
	store := serverconfig.NewStore(filepath.Join(appdata.ConfigFilePath()), filepath.Join(appdata.LogsRootPath()))
	host, err := NewHost(store)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	return host
}

// TestSaveConfigVsStartNoRace (F-35) 验证 SaveConfig 读 httpServer 在 runMu 内，
// 与并发 Start 不产生 data race。用 -race 跑时若 SaveConfig 未持锁读 httpServer 会触发告警。
func TestSaveConfigVsStartNoRace(t *testing.T) {
	host := newTestHost(t)
	ctx := context.Background()
	cfg := host.configs.Current()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = host.Start()
		}()
		go func() {
			defer wg.Done()
			_, _ = host.SaveConfig(ctx, cfg)
		}()
	}
	wg.Wait()
	stopCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = host.Stop(stopCtx)
}
