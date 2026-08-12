package forwarder

import (
	"context"
	"testing"

	agentv1 "cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
)

// capturingCompactionProvider 记录 ProviderRequest 并返回 errProviderLoopInterrupted，
// 使 generateCompactionSummary 走短路路径（跳过 usage 记录），只验证字段取值。
type capturingCompactionProvider struct {
	req ProviderRequest
}

func (p *capturingCompactionProvider) StartStream(ctx context.Context, req ProviderRequest, _ func(modeladapter.ModelEvent) error) error {
	p.req = req
	return errProviderLoopInterrupted
}

// TestCompactionSummaryUsesSnapshotFields 验证 P0-4：压缩 goroutine 经
// generateCompactionSummary 发给 provider 的 RequestID/ConversationID/ModelID
// 必须来自 startPendingCompactionSummary 持锁采集的快照，而不是无锁直读的 stream
// 当前字段（后者与 actor 线程并发写存在数据竞争，可能读到半更新值打到错误模型）。
func TestCompactionSummaryUsesSnapshotFields(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("req-live", "conv-live", 3, "model-live", "Model Live", agentv1.AgentMode_AGENT_MODE_AGENT, "")
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	provider := &capturingCompactionProvider{}
	svc := newServiceWithDependencies(NewConversationFileStore(t.TempDir()), nil, nil, provider, broker)

	plan := &PendingCompaction{
		Trigger:           "auto",
		RequestSource:     "test",
		MessagesToCompact: 1,
		CompactTurnCount:  1,
	}
	// 快照与 stream 当前字段刻意不同：若实现回退直读 stream 字段即断言失败。
	snapshot := compactionStreamSnapshot{
		RequestID:      "req-snap",
		ConversationID: "conv-snap",
		ModelID:        "model-snap",
	}
	modelCallID := "mc-1"
	if _, err := svc.generateCompactionSummary(context.Background(), stream, plan, modelCallID, snapshot); err == nil {
		t.Fatal("expected errProviderLoopInterrupted to short-circuit")
	}
	if provider.req.RequestID != "req-snap" {
		t.Errorf("RequestID = %q, want snapshot %q", provider.req.RequestID, "req-snap")
	}
	if provider.req.ConversationID != "conv-snap" {
		t.Errorf("ConversationID = %q, want snapshot %q", provider.req.ConversationID, "conv-snap")
	}
	if provider.req.ModelID != "model-snap" {
		t.Errorf("ModelID = %q, want snapshot %q", provider.req.ModelID, "model-snap")
	}
}
