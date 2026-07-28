package forwarder

import (
	"testing"

	"cursor/gen/agentv1"
)

// TestArtifactRecorderSessionsClearedOnStreamRemoval (F-36) 验证当 StreamBroker 删除一条
// 终态活动流时，artifactRecorder 会清掉该 request 下缓存的全部 session——堵住"每次 provider
// request 永久保留请求/摘要 payload"的内存泄漏。覆盖成功/失败/取消三种终态与多 modelCallID
// （B2 failover 链）场景，并确认不影响其他 request 的 session。
func TestArtifactRecorderSessionsClearedOnStreamRemoval(t *testing.T) {
	cases := []struct {
		name   string
		status StreamStatus
	}{
		{"completed", StreamStatusCompleted},
		{"failed", StreamStatusFailed},
		{"canceled", StreamStatusCanceled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			broker := NewStreamBroker()
			recorder := newArtifactRecorder(nil, broker, nil)
			if broker.OnStreamRemoved == nil {
				t.Fatalf("broker.OnStreamRemoved not wired by newArtifactRecorder")
			}

			requestID := "req-" + c.name
			stream, err := broker.OpenStream(requestID, "conv-test", 1, "gpt-5", "GPT-5", agentv1.AgentMode_AGENT_MODE_AGENT, "hi")
			if err != nil || stream == nil {
				t.Fatalf("OpenStream: err=%v stream=%v", err, stream)
			}
			// 模拟 provider request 记录（B2 failover 可能产生多个 model_call_id）。
			if _, err := recorder.RecordLLMRequest(requestID, "run-1", "call-a", map[string]any{"provider": "openai"}); err != nil {
				t.Fatalf("RecordLLMRequest call-a: %v", err)
			}
			if _, err := recorder.RecordLLMRequest(requestID, "run-1", "call-b", map[string]any{"provider": "openai"}); err != nil {
				t.Fatalf("RecordLLMRequest call-b: %v", err)
			}
			if _, err := recorder.RecordLLMSummary(requestID, "run-1", "call-a", map[string]any{"provider": "openai"}); err != nil {
				t.Fatalf("RecordLLMSummary call-a: %v", err)
			}
			if got := sessionCountForRequest(recorder, requestID); got != 2 {
				t.Fatalf("expected 2 sessions before cleanup, got %d", got)
			}

			// 走终态：设置 status 后 RemoveIfIdle 触发 OnStreamRemoved。
			stream.mu.Lock()
			stream.Status = c.status
			stream.mu.Unlock()
			if !broker.RemoveIfIdle(requestID) {
				t.Fatalf("RemoveIfIdle should remove terminal stream")
			}
			if got := sessionCountForRequest(recorder, requestID); got != 0 {
				t.Fatalf("expected 0 sessions after cleanup, got %d (sessions not reclaimed)", got)
			}
		})
	}
}

// TestArtifactRecorderCleanupPreservesOtherRequests 验证清理一个 request 不影响其他 request。
func TestArtifactRecorderCleanupPreservesOtherRequests(t *testing.T) {
	broker := NewStreamBroker()
	recorder := newArtifactRecorder(nil, broker, nil)

	streamA, _ := broker.OpenStream("req-A", "conv-a", 1, "gpt-5", "GPT-5", agentv1.AgentMode_AGENT_MODE_AGENT, "a")
	_, _ = broker.OpenStream("req-B", "conv-b", 1, "gpt-5", "GPT-5", agentv1.AgentMode_AGENT_MODE_AGENT, "b")
	if _, err := recorder.RecordLLMRequest("req-A", "run", "call", map[string]any{"provider": "openai"}); err != nil {
		t.Fatalf("RecordLLMRequest A: %v", err)
	}
	if _, err := recorder.RecordLLMRequest("req-B", "run", "call", map[string]any{"provider": "anthropic"}); err != nil {
		t.Fatalf("RecordLLMRequest B: %v", err)
	}

	streamA.mu.Lock()
	streamA.Status = StreamStatusCompleted
	streamA.mu.Unlock()
	broker.RemoveIfIdle("req-A")

	if got := sessionCountForRequest(recorder, "req-A"); got != 0 {
		t.Fatalf("req-A sessions should be cleared, got %d", got)
	}
	if got := sessionCountForRequest(recorder, "req-B"); got != 1 {
		t.Fatalf("req-B sessions should be preserved, got %d", got)
	}
}

// TestArtifactRecorderCleanupNotTriggeredForActiveStream 验证非终态流不会被回收。
func TestArtifactRecorderCleanupNotTriggeredForActiveStream(t *testing.T) {
	broker := NewStreamBroker()
	recorder := newArtifactRecorder(nil, broker, nil)

	stream, _ := broker.OpenStream("req-active", "conv", 1, "gpt-5", "GPT-5", agentv1.AgentMode_AGENT_MODE_AGENT, "x")
	if _, err := recorder.RecordLLMRequest("req-active", "run", "call", map[string]any{"provider": "openai"}); err != nil {
		t.Fatalf("RecordLLMRequest: %v", err)
	}
	// 仍处于 Created 态且有 conversation——RemoveIfIdle 不应删除。
	if broker.RemoveIfIdle("req-active") {
		t.Fatalf("RemoveIfIdle should not remove active stream")
	}
	if got := sessionCountForRequest(recorder, "req-active"); got != 1 {
		t.Fatalf("active stream session should be retained, got %d", got)
	}
	// 流终态化后才清理。
	stream.mu.Lock()
	stream.Status = StreamStatusCompleted
	stream.mu.Unlock()
	broker.RemoveIfIdle("req-active")
	if got := sessionCountForRequest(recorder, "req-active"); got != 0 {
		t.Fatalf("terminal stream session should be cleared, got %d", got)
	}
}

func sessionCountForRequest(recorder *artifactRecorder, requestID string) int {
	if recorder == nil {
		return 0
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	prefix := requestID + "::"
	count := 0
	for key := range recorder.sessions {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			count++
		}
	}
	return count
}
