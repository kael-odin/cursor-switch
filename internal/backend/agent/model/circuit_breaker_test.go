package modeladapter

import (
	"testing"
	"time"
)

func testConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		FailureThreshold:   3,
		SuccessThreshold:   2,
		TimeoutSeconds:     1,
		ErrorRateThreshold: 0.6,
		MinRequests:        5,
	}
}

func TestCircuitBreakerClosedAllowsAll(t *testing.T) {
	cb := NewCircuitBreaker(testConfig())
	for i := 0; i < 5; i++ {
		r := cb.AllowRequest()
		if !r.Allowed {
			t.Fatalf("request %d should be allowed in Closed", i)
		}
		if r.UsedHalfOpenPermit {
			t.Fatalf("Closed should not use half-open permit")
		}
	}
	if cb.State() != CircuitClosed {
		t.Errorf("state=%s want closed", cb.State())
	}
}

func TestCircuitBreakerOpensOnConsecutiveFailures(t *testing.T) {
	cb := NewCircuitBreaker(testConfig())
	// 连续失败 3 次达阈值 → Open。
	for i := 0; i < 3; i++ {
		cb.RecordFailure(false)
	}
	if cb.State() != CircuitOpen {
		t.Fatalf("after 3 failures state=%s want open", cb.State())
	}
	// Open 状态应拒绝。
	if r := cb.AllowRequest(); r.Allowed {
		t.Errorf("Open should reject, got allowed=%v", r.Allowed)
	}
}

func TestCircuitBreakerOpensOnErrorRate(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 100 // 抬高，确保只由 error_rate 触发
	cfg.MinRequests = 5
	cfg.ErrorRateThreshold = 0.5
	cb := NewCircuitBreaker(cfg)
	// 5 请求里 3 失败 2 成功，错误率 60% >= 50%，且 total>=MinRequests → Open。
	cb.RecordFailure(false)
	cb.RecordSuccess(false)
	cb.RecordFailure(false)
	cb.RecordSuccess(false)
	cb.RecordFailure(false) // 第 5 次，触发错误率检查
	if cb.State() != CircuitOpen {
		t.Fatalf("error rate 60%% should open, state=%s", cb.State())
	}
}

func TestCircuitBreakerErrorRateBelowMinRequestsStaysClosed(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 100
	cfg.MinRequests = 10
	cb := NewCircuitBreaker(cfg)
	// 4 次全失败，但 total < MinRequests=10，错误率不触发。
	for i := 0; i < 4; i++ {
		cb.RecordFailure(false)
	}
	if cb.State() != CircuitClosed {
		t.Fatalf("below MinRequests should stay Closed, state=%s", cb.State())
	}
}

func TestCircuitBreakerOpenToHalfOpenAfterTimeout(t *testing.T) {
	cb := NewCircuitBreaker(testConfig())
	for i := 0; i < 3; i++ {
		cb.RecordFailure(false)
	}
	if cb.State() != CircuitOpen {
		t.Fatalf("want open, got %s", cb.State())
	}
	// 等待超时（TimeoutSeconds=1）。
	time.Sleep(1100 * time.Millisecond)
	// AllowRequest 应触发 Open→HalfOpen 并放行 1 个探测。
	r := cb.AllowRequest()
	if !r.Allowed || !r.UsedHalfOpenPermit {
		t.Fatalf("after timeout should allow half-open probe, got %+v", r)
	}
	if cb.State() != CircuitHalfOpen {
		t.Errorf("state=%s want half_open", cb.State())
	}
	// 第二个请求应被限流拒绝（半开只放 1 个）。
	r2 := cb.AllowRequest()
	if r2.Allowed {
		t.Errorf("half-open should limit to 1 probe, second got allowed=%v", r2.Allowed)
	}
}

func TestCircuitBreakerHalfOpenProbeSuccessCloses(t *testing.T) {
	cb := NewCircuitBreaker(testConfig())
	for i := 0; i < 3; i++ {
		cb.RecordFailure(false)
	}
	time.Sleep(1100 * time.Millisecond)
	r := cb.AllowRequest()
	if !r.UsedHalfOpenPermit {
		t.Fatalf("expected half-open permit")
	}
	// SuccessThreshold=2，需 2 次连续成功探测。第一次成功后仍是 HalfOpen。
	cb.RecordSuccess(true)
	if cb.State() != CircuitHalfOpen {
		t.Fatalf("after 1 success state=%s want half_open", cb.State())
	}
	// 第二个探测名额。
	r2 := cb.AllowRequest()
	if !r2.Allowed {
		t.Fatalf("second probe should be allowed")
	}
	cb.RecordSuccess(true)
	if cb.State() != CircuitClosed {
		t.Errorf("after 2 successes state=%s want closed", cb.State())
	}
}

func TestCircuitBreakerHalfOpenProbeFailureReopens(t *testing.T) {
	cb := NewCircuitBreaker(testConfig())
	for i := 0; i < 3; i++ {
		cb.RecordFailure(false)
	}
	time.Sleep(1100 * time.Millisecond)
	r := cb.AllowRequest()
	if !r.UsedHalfOpenPermit {
		t.Fatalf("expected half-open permit")
	}
	// 探测失败 → 立即回 Open。
	cb.RecordFailure(true)
	if cb.State() != CircuitOpen {
		t.Errorf("probe failure should reopen, state=%s", cb.State())
	}
}

func TestCircuitBreakerReset(t *testing.T) {
	cb := NewCircuitBreaker(testConfig())
	for i := 0; i < 3; i++ {
		cb.RecordFailure(false)
	}
	if cb.State() != CircuitOpen {
		t.Fatalf("want open")
	}
	cb.Reset()
	if cb.State() != CircuitClosed {
		t.Errorf("after Reset state=%s want closed", cb.State())
	}
}

func TestCircuitBreakerSuccessResetsConsecutiveFailures(t *testing.T) {
	cfg := testConfig()
	cfg.MinRequests = 1000 // 抬高，禁用错误率触发，专注测连续失败计数
	cb := NewCircuitBreaker(cfg)
	cb.RecordFailure(false)
	cb.RecordFailure(false)
	cb.RecordSuccess(false) // 清零连续失败
	cb.RecordFailure(false)
	cb.RecordFailure(false)
	// 只累计 2 次连续失败，未达阈值 3。
	if cb.State() != CircuitClosed {
		t.Errorf("success should reset failure counter, state=%s", cb.State())
	}
}

func TestCircuitBreakerRegistryGetReturnsSameInstance(t *testing.T) {
	reg := NewCircuitBreakerRegistry(testConfig())
	a := reg.Get("adapter-A")
	b := reg.Get("adapter-A")
	if a != b {
		t.Errorf("same adapterID should return same breaker")
	}
	c := reg.Get("adapter-B")
	if a == c {
		t.Errorf("different adapterID should return different breaker")
	}
	// 大小写归一化。
	d := reg.Get("ADAPTER-A")
	if d != a {
		t.Errorf("adapterID should be case-insensitive")
	}
}

func TestCircuitBreakerRegistryStats(t *testing.T) {
	reg := NewCircuitBreakerRegistry(testConfig())
	reg.Get("a").RecordFailure(false)
	reg.Get("b").RecordSuccess(false)
	stats := reg.Stats()
	if len(stats) != 2 {
		t.Fatalf("len(stats)=%d want 2", len(stats))
	}
	if stats["a"].FailedRequests != 1 {
		t.Errorf("a.FailedRequests=%d want 1", stats["a"].FailedRequests)
	}
	if stats["b"].TotalRequests != 1 {
		t.Errorf("b.TotalRequests=%d want 1", stats["b"].TotalRequests)
	}
}

func TestCircuitBreakerRegistryUpdateConfig(t *testing.T) {
	reg := NewCircuitBreakerRegistry(testConfig())
	cb := reg.Get("x")
	newCfg := testConfig()
	newCfg.FailureThreshold = 99
	newCfg.MinRequests = 1000 // 禁用错误率触发，专注测 FailureThreshold 热更新
	reg.UpdateConfig(newCfg)
	// 旧实例应看到新配置：99 次失败也不该开。
	for i := 0; i < 10; i++ {
		cb.RecordFailure(false)
	}
	if cb.State() != CircuitClosed {
		t.Errorf("after UpdateConfig FailureThreshold=99, 10 failures should stay Closed, state=%s", cb.State())
	}
}

// TestCircuitBreakerLastFailureReason 覆盖 N-39：RecordFailure 前经 NoteFailureReason
// 记录真实原因，熔断打开后可读回；成功后清除。
func TestCircuitBreakerLastFailureReason(t *testing.T) {
	cb := NewCircuitBreaker(testConfig())
	if got := cb.LastFailureReason(); got != "" {
		t.Fatalf("fresh breaker LastFailureReason=%q want empty", got)
	}
	cb.NoteFailureReason("  provider stream idle timeout after 30s  ")
	cb.RecordFailure(false)
	if got := cb.LastFailureReason(); got != "provider stream idle timeout after 30s" {
		t.Errorf("LastFailureReason=%q want trimmed idle-timeout reason", got)
	}
	// 空原因不覆盖既有。
	cb.NoteFailureReason("   ")
	if got := cb.LastFailureReason(); got != "provider stream idle timeout after 30s" {
		t.Errorf("empty NoteFailureReason overwrote reason: %q", got)
	}
	// 成功清除。
	cb.RecordSuccess(false)
	if got := cb.LastFailureReason(); got != "" {
		t.Errorf("after RecordSuccess LastFailureReason=%q want empty", got)
	}
}

// TestCircuitBreakerIsAvailableIsSideEffectFree 覆盖 N-40：Open 且已过恢复超时点时，
// IsAvailable 返回 true 但**不得**把状态从 Open 转成 HalfOpen，也不得消耗探测名额。
// 只有 AllowRequest 才做转换与限流。
func TestCircuitBreakerIsAvailableIsSideEffectFree(t *testing.T) {
	cfg := testConfig()
	cfg.TimeoutSeconds = 0 // 打开即视为已过恢复点，shouldRecover 恒真，便于确定性测试
	cb := NewCircuitBreaker(cfg)
	for i := 0; i < int(cfg.FailureThreshold); i++ {
		cb.RecordFailure(false)
	}
	if cb.State() != CircuitOpen {
		t.Fatalf("breaker should be Open after threshold failures, got %s", cb.State())
	}
	// 多次 IsAvailable：应恒 true（已过超时点）且状态保持 Open（无副作用转换）。
	for i := 0; i < 5; i++ {
		if !cb.IsAvailable() {
			t.Fatalf("IsAvailable call %d = false, want true (past recovery timeout)", i)
		}
		if cb.State() != CircuitOpen {
			t.Fatalf("IsAvailable call %d mutated state to %s, want Open (must be side-effect-free)", i, cb.State())
		}
	}
	// 第一次 AllowRequest 才做 Open→HalfOpen 转换并发放唯一探测名额。
	first := cb.AllowRequest()
	if !first.Allowed || !first.UsedHalfOpenPermit {
		t.Fatalf("first AllowRequest after timeout = %+v, want Allowed+UsedHalfOpenPermit", first)
	}
	if cb.State() != CircuitHalfOpen {
		t.Fatalf("after AllowRequest state=%s want HalfOpen", cb.State())
	}
	// 第二次 AllowRequest：探测名额已被占，拒绝（单探测限流未被 IsAvailable 破坏）。
	second := cb.AllowRequest()
	if second.Allowed {
		t.Errorf("second AllowRequest = %+v, want denied (half-open single-probe limit)", second)
	}
}
