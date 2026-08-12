package forwarder

import (
	agentv1 "cursor/gen/agentv1"
	"context"
	"testing"
	"time"
)

// TestProviderDoneKeepsCancelForInFlightImages 验证 #1 取消传播修复的核心不变式：
// pass 结束时若存在在途生图（PendingImages 非空），handleProviderDoneEvent 必须
// **保留** ProviderCancel/ProviderContext（不置 nil、不调用），否则生图 goroutine
// 快照的 ctx 在用户取消时收不到信号——取消无法传播到在途生图 HTTP，白耗上游额度。
//
// 修复前（2.0.9）：handleProviderDoneEvent 无条件 ProviderCancel=nil，用户 ~93s
// 生图等待期点取消 → broker.Cancel 发现 ProviderCancel==nil → 跳过 → 生图空跑到
// 15min 看门狗。
func TestProviderDoneKeepsCancelForInFlightImages(t *testing.T) {
	svc := &Service{}
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("req-cancel-img", "conv-1", 1, "gpt-5", "agent", agentv1.AgentMode_AGENT_MODE_AGENT, "")
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stream.mu.Lock()
	stream.ProviderActive = true
	stream.ProviderCancel = cancel
	stream.ProviderContext = ctx
	stream.PendingImages["img-1"] = pendingImage{ImageID: "img-1"}
	stream.mu.Unlock()

	// pass 结束（interrupted 短路路径：不触碰 service 其他方法）。
	if err := svc.handleProviderDoneEvent(stream, &streamProviderEvent{Err: errProviderLoopInterrupted}); err != nil {
		t.Fatalf("handleProviderDoneEvent: %v", err)
	}

	stream.mu.Lock()
	preservedCancel := stream.ProviderCancel
	preservedCtx := stream.ProviderContext
	active := stream.ProviderActive
	stream.mu.Unlock()
	if preservedCancel == nil {
		t.Fatal("ProviderCancel was dropped while an image was in flight — user cancel cannot propagate")
	}
	if preservedCtx == nil {
		t.Fatal("ProviderContext was dropped while an image was in flight")
	}
	if active {
		t.Fatal("ProviderActive should be false after provider done")
	}
	if ctx.Err() != nil {
		t.Fatal("provider ctx should still be alive after done — image goroutine may be waiting")
	}

	// 用户取消：broker.Cancel → ProviderCancel()。保留的 cancel 必须让快照 ctx 立即取消。
	stream.mu.Lock()
	pc := stream.ProviderCancel
	stream.mu.Unlock()
	if pc == nil {
		t.Fatal("ProviderCancel nil before user cancel")
	}
	pc()
	select {
	case <-ctx.Done():
		if ctx.Err() != context.Canceled {
			t.Fatalf("ctx err = %v, want context.Canceled", ctx.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("preserved ProviderCancel did not cancel the in-flight image ctx")
	}
}

// TestProviderDoneReclaimsCancelWithoutImages 验证无在途生图时 pass 结束照旧回收
// ProviderCancel/Context（回归保护：不能因为保留逻辑破坏原有清理语义）。
func TestProviderDoneReclaimsCancelWithoutImages(t *testing.T) {
	svc := &Service{}
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("req-no-img", "conv-1", 1, "gpt-5", "agent", agentv1.AgentMode_AGENT_MODE_AGENT, "")
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}

	_, cancel := context.WithCancel(context.Background())
	stream.mu.Lock()
	stream.ProviderActive = true
	stream.ProviderCancel = cancel
	stream.ProviderContext = context.Background()
	stream.mu.Unlock()

	if err := svc.handleProviderDoneEvent(stream, &streamProviderEvent{Err: errProviderLoopInterrupted}); err != nil {
		t.Fatalf("handleProviderDoneEvent: %v", err)
	}

	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.ProviderCancel != nil {
		t.Fatal("ProviderCancel should be reclaimed when no image is in flight")
	}
	if stream.ProviderContext != nil {
		t.Fatal("ProviderContext should be reclaimed when no image is in flight")
	}
}

// TestRemoveIfIdleReclaimsStaleCancel 验证 broker.RemoveIfIdle 在终结流上兜底回收
// 残留 ProviderCancel/Context：handleProviderDoneEvent 因在途生图保留了取消信号，
// 但生图 goroutine 可能从不回投（异常/直接终结）——终结清理必须调用 cancel 释放
// 生图快照 ctx，避免泄漏。
func TestRemoveIfIdleReclaimsStaleCancel(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("req-stale", "conv-1", 1, "gpt-5", "agent", agentv1.AgentMode_AGENT_MODE_AGENT, "")
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stream.mu.Lock()
	stream.ProviderCancel = cancel
	stream.ProviderContext = ctx
	stream.PendingImages["img-1"] = pendingImage{ImageID: "img-1"}
	stream.Status = StreamStatusCanceled
	stream.mu.Unlock()

	if !broker.RemoveIfIdle("req-stale") {
		t.Fatal("RemoveIfIdle should remove the canceled idle stream")
	}
	if ctx.Err() == nil {
		t.Fatal("RemoveIfIdle should invoke stale ProviderCancel to release the image ctx")
	}
}
