package forwarder

import (
	"testing"

	"cursor/gen/agentv1"
)

// TestStreamBrokerBacklogHardCap (F-28) 验证 Publish 在 backlog 超过上限时丢弃最旧非终态事件，
// 保留最近事件；终态事件（End=true）永不丢弃。
func TestStreamBrokerBacklogHardCap(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("req-cap", "conv", 1, "gpt-5", "GPT-5", agentv1.AgentMode_AGENT_MODE_AGENT, "x")
	if err != nil || stream == nil {
		t.Fatalf("OpenStream: err=%v stream=%v", err, stream)
	}
	// 用小阈值覆盖上限：上限 3，发 5 个非终态事件 → 应保留最近 3 个。
	prev := backlogCap
	backlogCap = 3
	defer func() { backlogCap = prev }()

	for i := 0; i < 5; i++ {
		if err := broker.Publish("req-cap", StreamEvent{Message: buildTextDeltaMessage("e" + string(rune('0'+i)))}); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}
	stream.mu.Lock()
	got := len(stream.Backlog)
	stream.mu.Unlock()
	if got != 3 {
		t.Fatalf("expected backlog capped at 3, got %d", got)
	}
}

// TestStreamBrokerBacklogCapPreservesEndEvent 验证终态事件（End=true）即便在淘汰窗口中也不被丢。
func TestStreamBrokerBacklogCapPreservesEndEvent(t *testing.T) {
	broker := NewStreamBroker()
	stream, _ := broker.OpenStream("req-end", "conv", 1, "gpt-5", "GPT-5", agentv1.AgentMode_AGENT_MODE_AGENT, "x")
	prev := backlogCap
	backlogCap = 3
	defer func() { backlogCap = prev }()

	// 先放 1 个 End 事件到最旧位置，再发 4 个普通事件 → 超过 3。
	if err := broker.Publish("req-end", StreamEvent{Message: buildTextDeltaMessage("end"), End: true}); err != nil {
		t.Fatalf("Publish end: %v", err)
	}
	for i := 0; i < 4; i++ {
		if err := broker.Publish("req-end", StreamEvent{Message: buildTextDeltaMessage("e" + string(rune('0'+i)))}); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	var hasEnd bool
	for _, ev := range stream.Backlog {
		if ev.End {
			hasEnd = true
		}
	}
	if !hasEnd {
		t.Fatalf("end event was evicted; backlog cap must preserve End events; backlog len=%d", len(stream.Backlog))
	}
}
