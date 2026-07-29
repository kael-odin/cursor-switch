package modeladapter

import (
	"context"
	"errors"
	"testing"

	legacyruntime "cursor/internal/runtime"
)

// TestRouterHalfOpenPermitReleasedOnSinkFailure 覆盖 P0-3：
// 渠道熔断后处于 HalfOpen 放行探测（UsedHalfOpenPermit=true），provider 正常完成流
// （streamErr==nil），但下游 sink 写入失败（sinkErr!=nil）。修复前此路径既不调
// RecordSuccess 也不调 RecordFailure，halfOpenInFlight 卡在 1 永不释放，渠道永久
// 卡死 HalfOpen 只能人工 Reset。修复后应释放名额（且不污染失败统计，否则会扭曲
// 错误率见 P1-4）。
func TestRouterHalfOpenPermitReleasedOnSinkFailure(t *testing.T) {
	cfg := DefaultCircuitBreakerConfig()
	cfg.TimeoutSeconds = 0
	breakers := NewCircuitBreakerRegistry(cfg)

	ch := makeChannel("primary", "openai")
	resolver := &fakeResolver{channels: []*legacyruntime.ResolvedChannel{ch}}
	// sinkBeforeErr 非 nil：fakeAdapter 会调一次 sink（触发 sinkStarted）。
	// err=nil：Stream 成功返回。两者组合出 streamErr==nil 且 sinkStarted=true。
	sinkEvent := ModelEvent{}
	ok := &fakeAdapter{err: nil, sinkBeforeErr: &sinkEvent}
	router := makeRouterWithFakes(resolver, breakers, ok, &fakeAdapter{err: nil})

	cb := breakers.Get(ch.ID)
	for i := 0; i < int(cfg.FailureThreshold); i++ {
		cb.RecordFailure(false)
	}
	if cb.State() != CircuitOpen {
		t.Fatalf("precondition: want Open, got %s", cb.State())
	}

	// sink 返回错误：模拟下游断连。streamErr==nil && sinkErr!=nil 路径。
	sinkErr := errors.New("downstream write failed (client disconnected)")
	req := StreamRequest{ModelID: "gpt-5"}
	if err := router.Stream(context.Background(), req, func(ModelEvent) error { return sinkErr }); err == nil {
		t.Fatalf("expected sink error to propagate, got nil")
	}

	// 关键断言：名额已释放，渠道不再卡死 HalfOpen。
	permit := cb.AllowRequest()
	if !permit.Allowed {
		t.Fatalf("channel stuck in HalfOpen: permit not released after sink failure. "+
			"state=%s allowed=%v", cb.State(), permit.Allowed)
	}
	if !permit.UsedHalfOpenPermit {
		t.Fatalf("expected a fresh half-open probe permit (proves release), got usedHalfOpenPermit=false. state=%s", cb.State())
	}

	// 不应把 sink 失败计入 provider 失败统计：failedRequests 不应因 sink 失败而增长。
	stats := cb.Stats()
	if stats.FailedRequests != uint32(cfg.FailureThreshold) {
		t.Fatalf("sink failure must NOT pollute failedRequests: got %d want %d (P1-4 guard)",
			stats.FailedRequests, cfg.FailureThreshold)
	}
}
