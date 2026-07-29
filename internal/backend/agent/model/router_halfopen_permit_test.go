package modeladapter

import (
	"context"
	"testing"

	legacyruntime "cursor/internal/runtime"
)

// TestRouterHalfOpenPermitReleasedOnUnsupportedProvider 覆盖 N-01：
// 渠道熔断后处于 HalfOpen 放行探测（UsedHalfOpenPermit=true），但该渠道
// Provider 类型不支持（adapterFor 返回 aerr）。修复前 adapterFor 失败路径
// 直接 continue 不调 RecordFailure，halfOpenInFlight 卡在 1 永不释放，
// 渠道永久卡死 HalfOpen 只能人工 Reset。修复后应释放名额并回退 Open。
func TestRouterHalfOpenPermitReleasedOnUnsupportedProvider(t *testing.T) {
	// TimeoutSeconds=0：breaker 进 Open 后立即可转 HalfOpen 探测，无需等待。
	cfg := DefaultCircuitBreakerConfig()
	cfg.TimeoutSeconds = 0
	breakers := NewCircuitBreakerRegistry(cfg)

	ch := makeChannel("primary", "totally-unsupported-provider")
	resolver := &fakeResolver{channels: []*legacyruntime.ResolvedChannel{ch}}
	// openai/anthropic 适配器都正常，但渠道 provider 既不是 openai 也不是
	// anthropic，adapterFor 会返回 "unsupported provider" aerr。
	router := makeRouterWithFakes(resolver, breakers, &fakeAdapter{err: nil}, &fakeAdapter{err: nil})

	cb := breakers.Get(ch.ID)
	// 连续失败达 FailureThreshold(4) → Open。
	for i := 0; i < int(cfg.FailureThreshold); i++ {
		cb.RecordFailure(false)
	}
	if cb.State() != CircuitOpen {
		t.Fatalf("precondition: want Open, got %s", cb.State())
	}
	// 首次 Stream：AllowRequest 会 Open→HalfOpen 并放行探测（UsedHalfOpenPermit=true），
	// 随后 adapterFor 因 provider 不支持失败。修复前此后 halfOpenInFlight=1 卡死。
	req := StreamRequest{ModelID: "gpt-5"}
	if err := router.Stream(context.Background(), req, func(ModelEvent) error { return nil }); err == nil {
		t.Fatalf("expected unsupported-provider error, got nil")
	}

	// 关键断言：名额已释放。AllowRequest 应能再次放行探测（而非因
	// halfOpenInFlight>=1 被拒）。修复前会卡在 HalfOpen 且拒绝后续探测。
	// （此时因 RecordFailure 已使 HalfOpen→Open，AllowRequest 又会 Open→HalfOpen
	// 放行新探测——这正说明名额已释放、渠道不再卡死。）
	permit := cb.AllowRequest()
	if !permit.Allowed {
		t.Fatalf("channel stuck in HalfOpen: permit not released after unsupported-provider failure. "+
			"state=%s allowed=%v", cb.State(), permit.Allowed)
	}
	if !permit.UsedHalfOpenPermit {
		t.Fatalf("expected a fresh half-open probe permit (proves release), got usedHalfOpenPermit=false. state=%s", cb.State())
	}
}

// 兜底：正常支持的 provider + HalfOpen 探测成功路径不受影响。
func TestRouterHalfOpenRecoversOnSupportedProvider(t *testing.T) {
	cfg := DefaultCircuitBreakerConfig()
	cfg.TimeoutSeconds = 0
	breakers := NewCircuitBreakerRegistry(cfg)

	ch := makeChannel("primary", "openai")
	resolver := &fakeResolver{channels: []*legacyruntime.ResolvedChannel{ch}}
	ok := &fakeAdapter{err: nil}
	router := makeRouterWithFakes(resolver, breakers, ok, &fakeAdapter{err: nil})

	cb := breakers.Get(ch.ID)
	for i := 0; i < int(cfg.FailureThreshold); i++ {
		cb.RecordFailure(false)
	}
	if cb.State() != CircuitOpen {
		t.Fatalf("precondition: want Open, got %s", cb.State())
	}
	req := StreamRequest{ModelID: "gpt-5"}
	if err := router.Stream(context.Background(), req, func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("expected success on supported provider, got %v", err)
	}
	// 探测成功应计入 consecutiveSuccesses（未达 SuccessThreshold=2 仍是 HalfOpen）。
	if cb.State() != CircuitHalfOpen {
		t.Fatalf("want HalfOpen after one probe success (need 2 to close), got %s", cb.State())
	}
}
