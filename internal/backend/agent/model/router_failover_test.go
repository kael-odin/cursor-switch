package modeladapter

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	legacyruntime "cursor/internal/runtime"
)

// fakeAdapter 是可编程的 ModelAdapter 测试替身：按脚本返回错误/向 sink 发事件。
type fakeAdapter struct {
	// sinkBeforeErr 若非 nil，则在返回 err 前先调一次 sink（模拟"已首字节"）。
	sinkBeforeErr *ModelEvent
	// err 返回值。nil 表示成功。
	err error
	// calls 记录被调用的 req.ResolvedChannelID，便于断言 failover 顺序。
	calls []string
	mu    sync.Mutex
}

func (f *fakeAdapter) Stream(_ context.Context, req StreamRequest, sink func(ModelEvent) error) error {
	f.mu.Lock()
	f.calls = append(f.calls, req.ResolvedChannelID)
	f.mu.Unlock()
	if f.sinkBeforeErr != nil {
		_ = sink(*f.sinkBeforeErr)
	}
	return f.err
}

func (f *fakeAdapter) callOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

// fakeResolver 是 ChannelResolver 测试替身。
type fakeResolver struct {
	channels     []*legacyruntime.ResolvedChannel
	idleTimeout  time.Duration
	singleCallID string // SelectChannelForModel 返回 [0] 的语义
}

func (r *fakeResolver) SelectChannelForModel(_ context.Context, _ string) (*legacyruntime.ResolvedChannel, error) {
	if len(r.channels) == 0 {
		return nil, legacyruntime.ErrChannelNotAvailable
	}
	return r.channels[0], nil
}

func (r *fakeResolver) SelectChannelsForModel(_ context.Context, _ string) ([]*legacyruntime.ResolvedChannel, error) {
	return r.channels, nil
}

func (r *fakeResolver) ProviderStreamIdleTimeout(_ context.Context) time.Duration {
	return r.idleTimeout
}

// makeChannel 构造一个最小 ResolvedChannel 测试值。
func makeChannel(id, provider string) *legacyruntime.ResolvedChannel {
	return &legacyruntime.ResolvedChannel{
		ID:       id,
		Name:     id,
		Provider: provider,
		BaseURL:  "https://example.test",
		APIKey:   "sk-test",
		Model:    "gpt-5",
	}
}

// makeRouterWithFakes 构造一个 Router，其 openai/anthropic 适配器分别用给定 fake。
// 通过函数而非 newTestRouter 来直接覆盖 anthropic 字段。
func makeRouterWithFakes(resolver ChannelResolver, breakers *CircuitBreakerRegistry, openai, anthropic ModelAdapter) *Router {
	r := &Router{
		openai:    openai,
		anthropic: anthropic,
		resolver:  resolver,
		breakers:  breakers,
	}
	return r
}

func TestRouterFailoverOn5xx(t *testing.T) {
	// 主候选 openai 返回 503，备候选 openai 成功 → 应 failover 到备候选。
	primary := &fakeAdapter{err: fmt.Errorf("openai adapter status=503 body=server down")}
	backup := &fakeAdapter{err: nil}
	// 两个 openai 适配器：主失败、备成功。但 Router 只有一个 openai 字段。
	// 所以这里主备用不同 provider 验证：主 anthropic 503 → 备 openai 成功。
	resolver := &fakeResolver{
		channels: []*legacyruntime.ResolvedChannel{
			makeChannel("primary", "anthropic"),
			makeChannel("backup", "openai"),
		},
	}
	router := makeRouterWithFakes(resolver, NewCircuitBreakerRegistry(DefaultCircuitBreakerConfig()), openaiBackupAdapter(backup), anthropicPrimaryAdapter(primary))

	req := StreamRequest{ModelID: "gpt-5"}
	err := router.Stream(context.Background(), req, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("expected nil error after failover, got %v", err)
	}
	if got := primary.callOrder(); len(got) != 1 || got[0] != "primary" {
		t.Errorf("primary call order = %v, want [primary]", got)
	}
	if got := backup.callOrder(); len(got) != 1 || got[0] != "backup" {
		t.Errorf("backup call order = %v, want [backup]", got)
	}
}

// openaiBackupAdapter 把 fakeAdapter 包成 ModelAdapter（命名仅为可读性）。
func openaiBackupAdapter(f *fakeAdapter) ModelAdapter  { return f }
func anthropicPrimaryAdapter(f *fakeAdapter) ModelAdapter { return f }

func TestRouterNoFailoverOn4xx(t *testing.T) {
	// 主候选 401（不可重试）→ 直接透传，不尝试备候选。
	primary := &fakeAdapter{err: fmt.Errorf("openai adapter status=401 body=unauthorized")}
	backup := &fakeAdapter{err: nil}
	resolver := &fakeResolver{
		channels: []*legacyruntime.ResolvedChannel{
			makeChannel("primary", "openai"),
			makeChannel("backup", "openai"),
		},
	}
	// 两候选都是 openai，但 Router 只有一个 openai 适配器字段——所以主备共享同一 fake。
	// 这会破坏断言。改用 provider 区分：主 openai 401，备 anthropic 成功。
	primary = &fakeAdapter{err: fmt.Errorf("openai adapter status=401 body=unauthorized")}
	resolver = &fakeResolver{
		channels: []*legacyruntime.ResolvedChannel{
			makeChannel("primary", "openai"),
			makeChannel("backup", "anthropic"),
		},
	}
	router := makeRouterWithFakes(resolver, NewCircuitBreakerRegistry(DefaultCircuitBreakerConfig()), openaiBackupAdapter(primary), anthropicPrimaryAdapter(backup))

	req := StreamRequest{ModelID: "gpt-5"}
	err := router.Stream(context.Background(), req, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatalf("expected 401 error to propagate, got nil")
	}
	if got := backup.callOrder(); len(got) != 0 {
		t.Errorf("backup should NOT be tried on 4xx, got calls=%v", got)
	}
}

func TestRouterNoFailoverAfterSinkStarted(t *testing.T) {
	// 主候选先发一个事件再返回 503 → 已首字节，不 failover。
	event := ModelEvent{Kind: ModelEventKindTextDelta, Text: "partial"}
	primary := &fakeAdapter{sinkBeforeErr: &event, err: fmt.Errorf("openai adapter status=503 body=mid-stream")}
	backup := &fakeAdapter{err: nil}
	resolver := &fakeResolver{
		channels: []*legacyruntime.ResolvedChannel{
			makeChannel("primary", "openai"),
			makeChannel("backup", "anthropic"),
		},
	}
	router := makeRouterWithFakes(resolver, NewCircuitBreakerRegistry(DefaultCircuitBreakerConfig()), openaiBackupAdapter(primary), anthropicPrimaryAdapter(backup))

	var sinkEvents int
	req := StreamRequest{ModelID: "gpt-5"}
	err := router.Stream(context.Background(), req, func(ModelEvent) error {
		sinkEvents++
		return nil
	})
	if err == nil {
		t.Fatalf("expected 503 to propagate after sink, got nil")
	}
	if sinkEvents != 1 {
		t.Errorf("expected exactly 1 sink event (no double-emit), got %d", sinkEvents)
	}
	if got := backup.callOrder(); len(got) != 0 {
		t.Errorf("backup should NOT be tried after sink started, got calls=%v", got)
	}
}

func TestRouterFailoverOnConnectionError(t *testing.T) {
	// 连接层 *url.Error 可重试 → failover 到备候选。
	connErr := &url.Error{Op: "Post", URL: "https://example.test", Err: errors.New("dial tcp: connection refused")}
	primary := &fakeAdapter{err: connErr}
	backup := &fakeAdapter{err: nil}
	resolver := &fakeResolver{
		channels: []*legacyruntime.ResolvedChannel{
			makeChannel("primary", "openai"),
			makeChannel("backup", "anthropic"),
		},
	}
	router := makeRouterWithFakes(resolver, NewCircuitBreakerRegistry(DefaultCircuitBreakerConfig()), openaiBackupAdapter(primary), anthropicPrimaryAdapter(backup))

	req := StreamRequest{ModelID: "gpt-5"}
	err := router.Stream(context.Background(), req, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("expected nil after failover, got %v", err)
	}
	if got := backup.callOrder(); len(got) != 1 {
		t.Errorf("backup should be tried once, got %v", got)
	}
}

func TestRouterFailoverOnIdleTimeout(t *testing.T) {
	primary := &fakeAdapter{err: fmt.Errorf("provider stream idle timeout after 240s without effective content")}
	backup := &fakeAdapter{err: nil}
	resolver := &fakeResolver{
		channels: []*legacyruntime.ResolvedChannel{
			makeChannel("primary", "openai"),
			makeChannel("backup", "anthropic"),
		},
	}
	router := makeRouterWithFakes(resolver, NewCircuitBreakerRegistry(DefaultCircuitBreakerConfig()), openaiBackupAdapter(primary), anthropicPrimaryAdapter(backup))

	req := StreamRequest{ModelID: "gpt-5"}
	err := router.Stream(context.Background(), req, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("expected nil after idle-timeout failover, got %v", err)
	}
	if got := backup.callOrder(); len(got) != 1 {
		t.Errorf("backup should be tried once after idle timeout, got %v", got)
	}
}

func TestRouterAllCandidatesFailReturnsLastError(t *testing.T) {
	primary := &fakeAdapter{err: fmt.Errorf("openai adapter status=503 body=down")}
	backup := &fakeAdapter{err: fmt.Errorf("anthropic adapter status=503 body=down")}
	resolver := &fakeResolver{
		channels: []*legacyruntime.ResolvedChannel{
			makeChannel("primary", "openai"),
			makeChannel("backup", "anthropic"),
		},
	}
	router := makeRouterWithFakes(resolver, NewCircuitBreakerRegistry(DefaultCircuitBreakerConfig()), openaiBackupAdapter(primary), anthropicPrimaryAdapter(backup))

	req := StreamRequest{ModelID: "gpt-5"}
	err := router.Stream(context.Background(), req, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatalf("expected error when all candidates fail, got nil")
	}
	// 应返回最后一个候选的错误（anthropic 503）。
	if got := backup.callOrder(); len(got) != 1 {
		t.Errorf("backup should be tried, got %v", got)
	}
}

func TestRouterCircuitOpenSkipsToBackup(t *testing.T) {
	// 主候选熔断打开 → 直接走备候选，不调主适配器。
	breakers := NewCircuitBreakerRegistry(DefaultCircuitBreakerConfig())
	primaryCB := breakers.Get("primary")
	// 强制把主候选熔断器打开。
	for i := 0; i < int(DefaultCircuitBreakerConfig().FailureThreshold)+1; i++ {
		primaryCB.RecordFailure(false)
	}
	if primaryCB.IsAvailable() {
		t.Fatalf("primary breaker should be open after threshold failures")
	}

	primary := &fakeAdapter{err: nil} // 不应被调用
	backup := &fakeAdapter{err: nil}
	resolver := &fakeResolver{
		channels: []*legacyruntime.ResolvedChannel{
			makeChannel("primary", "openai"),
			makeChannel("backup", "anthropic"),
		},
	}
	router := makeRouterWithFakes(resolver, breakers, openaiBackupAdapter(primary), anthropicPrimaryAdapter(backup))

	req := StreamRequest{ModelID: "gpt-5"}
	err := router.Stream(context.Background(), req, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("expected nil (backup succeeds), got %v", err)
	}
	if got := primary.callOrder(); len(got) != 0 {
		t.Errorf("primary adapter should NOT be called when circuit open, got %v", got)
	}
	if got := backup.callOrder(); len(got) != 1 {
		t.Errorf("backup should be called once, got %v", got)
	}
}

func TestRouterNoRegistryStillFailovers(t *testing.T) {
	// breakers==nil（退化模式）仍应按 Priority failover，只是不计数。
	primary := &fakeAdapter{err: fmt.Errorf("openai adapter status=503 body=down")}
	backup := &fakeAdapter{err: nil}
	resolver := &fakeResolver{
		channels: []*legacyruntime.ResolvedChannel{
			makeChannel("primary", "openai"),
			makeChannel("backup", "anthropic"),
		},
	}
	router := makeRouterWithFakes(resolver, nil, openaiBackupAdapter(primary), anthropicPrimaryAdapter(backup))

	req := StreamRequest{ModelID: "gpt-5"}
	err := router.Stream(context.Background(), req, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("expected nil failover without registry, got %v", err)
	}
	if got := backup.callOrder(); len(got) != 1 {
		t.Errorf("backup should be tried once, got %v", got)
	}
}

func TestRouterRecordsFailureOnCandidateError(t *testing.T) {
	// 主候选 503 失败 → 熔断器应记录一次失败。
	breakers := NewCircuitBreakerRegistry(DefaultCircuitBreakerConfig())
	primary := &fakeAdapter{err: fmt.Errorf("openai adapter status=503 body=down")}
	backup := &fakeAdapter{err: nil}
	resolver := &fakeResolver{
		channels: []*legacyruntime.ResolvedChannel{
			makeChannel("primary", "openai"),
			makeChannel("backup", "anthropic"),
		},
	}
	router := makeRouterWithFakes(resolver, breakers, openaiBackupAdapter(primary), anthropicPrimaryAdapter(backup))

	req := StreamRequest{ModelID: "gpt-5"}
	_ = router.Stream(context.Background(), req, func(ModelEvent) error { return nil })

	stats := breakers.Get("primary").Stats()
	if stats.FailedRequests != 1 {
		t.Errorf("primary breaker FailedRequests = %d, want 1", stats.FailedRequests)
	}
	// 备候选成功 → 记录一次成功。
	backupStats := breakers.Get("backup").Stats()
	if backupStats.TotalRequests != 1 || backupStats.FailedRequests != 0 {
		t.Errorf("backup breaker stats = %+v, want 1 success", backupStats)
	}
}

// ---- 纯函数测试 ----

func TestIsRetryableChannelError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"503", fmt.Errorf("openai adapter status=503 body=down"), true},
		{"500", fmt.Errorf("anthropic adapter status=500 body=err"), true},
		{"429", fmt.Errorf("openai adapter status=429 body=rate limited"), true},
		{"401", fmt.Errorf("openai adapter status=401 body=unauthorized"), false},
		{"404", fmt.Errorf("anthropic adapter status=404 body=not found"), false},
		{"400", fmt.Errorf("openai adapter status=400 body=bad request"), false},
		{"idle_timeout_seconds", fmt.Errorf("provider stream idle timeout after 240s without effective content"), true},
		{"idle_timeout_duration", fmt.Errorf("provider stream idle timeout after 4m0s without effective content"), true},
		{"body_read_error_5xx", fmt.Errorf("openai adapter status=502 body_read_error=EOF"), true},
		{"body_read_error_4xx", fmt.Errorf("openai adapter status=400 body_read_error=EOF"), false},
		{"url_error", &url.Error{Op: "Post", URL: "https://x", Err: errors.New("dial: refused")}, true},
		{"url_error_wrapped", fmt.Errorf("wrap: %w", &url.Error{Op: "Post", URL: "https://x", Err: errors.New("refused")}), true},
		{"generic_unknown", errors.New("some random error"), false},
		{"status_no_digits", fmt.Errorf("openai adapter status= body=empty"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isRetryableChannelError(c.err); got != c.want {
				t.Errorf("isRetryableChannelError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestExtractHTTPStatus(t *testing.T) {
	cases := []struct {
		text    string
		status  int
		ok      bool
	}{
		{"openai adapter status=503 body=down", 503, true},
		{"status=429", 429, true},
		{"anthropic adapter status=401 body=unauthorized", 401, true},
		{"no status here", 0, false},
		{"status=abc", 0, false},
		{"status=", 0, false},
		{"prefix status=500 more text", 500, true},
	}
	for _, c := range cases {
		got, ok := extractHTTPStatus(c.text)
		if got != c.status || ok != c.ok {
			t.Errorf("extractHTTPStatus(%q) = %d,%v want %d,%v", c.text, got, ok, c.status, c.ok)
		}
	}
}

func TestOrderCandidatesBlockedToTail(t *testing.T) {
	breakers := NewCircuitBreakerRegistry(DefaultCircuitBreakerConfig())
	// 把 "primary" 熔断打开。
	primaryCB := breakers.Get("primary")
	for i := 0; i < int(DefaultCircuitBreakerConfig().FailureThreshold)+1; i++ {
		primaryCB.RecordFailure(false)
	}
	channels := []*legacyruntime.ResolvedChannel{
		makeChannel("primary", "openai"),
		makeChannel("secondary", "anthropic"),
		makeChannel("tertiary", "openai"),
	}
	router := &Router{breakers: breakers}
	ordered := router.orderCandidates(channels)
	if len(ordered) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(ordered))
	}
	// primary 应排到末尾。
	if ordered[2].ID != "primary" {
		t.Errorf("blocked candidate should be at tail, got order=%v", idsOf(ordered))
	}
	// secondary/tertiary 保持在前的相对顺序。
	if ordered[0].ID != "secondary" || ordered[1].ID != "tertiary" {
		t.Errorf("available candidates should keep order, got %v", idsOf(ordered))
	}
}

func idsOf(chs []*legacyruntime.ResolvedChannel) []string {
	out := make([]string, len(chs))
	for i, c := range chs {
		out[i] = c.ID
	}
	return out
}
