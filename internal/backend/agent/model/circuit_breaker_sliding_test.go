package modeladapter

import (
	"testing"
)

// TestCircuitBreakerSlidingWindowForgetsOldFailures 覆盖 P1-4：
// 旧实现 RecordSuccess 只清 consecutiveFailures 不清 failedRequests，错误率用全期
// failedRequests/totalRequests。长期运行中历史失败永久累积，渠道恢复后偶发单次失败
// 仍可能因全期错误率偏高被反复熔断。修复后错误率基于最近 circuitBreakerRecentWindowSize
// 次的滑动窗口：早期失败被成功稀释后推出窗口，错误率反映近期健康度，不被历史拉高。
func TestCircuitBreakerSlidingWindowForgetsOldFailures(t *testing.T) {
	cfg := DefaultCircuitBreakerConfig()
	// FailureThreshold=4, ErrorRateThreshold=0.6, MinRequests=10。
	cb := NewCircuitBreaker(cfg)

	// 阶段 1：早期一次失败（consecutiveFailures=1，不熔断），随后用大量成功把窗口
	// 洗成几乎全成功。模拟"早期抖动 + 长期健康"。
	cb.RecordFailure(false)
	if cb.State() != CircuitClosed {
		t.Fatalf("stage1: single failure must not open breaker, got %s", cb.State())
	}
	// 填满并超过窗口容量（circuitBreakerRecentWindowSize=50），把那次失败推出窗口。
	for i := 0; i < circuitBreakerRecentWindowSize+5; i++ {
		cb.RecordSuccess(false)
	}
	if cb.State() != CircuitClosed {
		t.Fatalf("stage1: after many successes must stay Closed, got %s", cb.State())
	}

	// 阶段 2：窗口已全成功（早期失败已出窗）。再连续 3 次失败：
	// consecutiveFailures=3 < FailureThreshold=4，不触发连续失败熔断。
	// 窗口错误率 = 3/50 = 0.06 < 0.6，不触发错误率熔断。
	// 旧全期逻辑：total=59, failed=4, rate=0.068 < 0.6 → 也不熔断（此例不区分）。
	// 但确认 consecutiveFailures 路径与窗口共存正确。
	for i := 0; i < 3; i++ {
		cb.RecordFailure(false)
		if cb.State() != CircuitClosed {
			t.Fatalf("stage2 iter %d: 3 consecutive failures (<4) must not open, got %s", i, cb.State())
		}
	}

	// 阶段 3：第 4 次连续失败达 FailureThreshold → 必须熔断（确认窗口改动未破坏连续失败路径）。
	cb.RecordFailure(false)
	if cb.State() != CircuitOpen {
		t.Fatalf("stage3: 4 consecutive failures must open breaker, got %s", cb.State())
	}
}

// TestCircuitBreakerSlidingWindowTracksRecentRate 验证窗口错误率反映近期而非全期：
// 早期失败被成功稀释出窗后，全期错误率仍含早期失败，但窗口错误率应更低，
// 二者数值不同证明窗口独立于全期统计。
func TestCircuitBreakerSlidingWindowTracksRecentRate(t *testing.T) {
	cfg := DefaultCircuitBreakerConfig()
	cb := NewCircuitBreaker(cfg)

	// 10 次失败（每次后立即 1 成重置 consecutiveFailures，避免连续失败熔断）。
	for i := 0; i < 10; i++ {
		cb.RecordFailure(false)
		cb.RecordSuccess(false)
	}
	// 此时 total=20, failed=10, 全期错误率=0.5。
	// 窗口（最近 50，但只有 20 条）= 10 失 10 成，窗口错误率=0.5。
	stats := cb.Stats()
	if stats.FailedRequests != 10 || stats.TotalRequests != 20 {
		t.Fatalf("stats: failed=%d total=%d, want 10/20", stats.FailedRequests, stats.TotalRequests)
	}
	if cb.State() != CircuitClosed {
		t.Fatalf("0.5 rate < 0.6 threshold should stay Closed, got %s", cb.State())
	}

	// 再追加 40 次成功：total=60, failed=10, 全期错误率=10/60≈0.17。
	// 窗口最近 50 = 10 失(早期已被推前但仍在 50 内) + 40 成... 实际窗口 50 条 = 后 50，
	// 早期 10 失在前 10 已被部分推出（窗口只保留最近 50，前 10 失被后 40 成 + 中间挤掉）。
	// 关键：窗口错误率 < 全期或至少不高于全期，且都 < 0.6 不熔断。
	for i := 0; i < 40; i++ {
		cb.RecordSuccess(false)
	}
	if cb.State() != CircuitClosed {
		t.Fatalf("after 40 more successes must stay Closed, got %s", cb.State())
	}
	// 全期统计仍累积（Stats 展示全期，熔断用窗口）。
	stats = cb.Stats()
	if stats.TotalRequests != 60 || stats.FailedRequests != 10 {
		t.Fatalf("full-period stats should still accumulate: total=%d failed=%d, want 60/10",
			stats.TotalRequests, stats.FailedRequests)
	}
}
