// router_diagnostic_test.go 验证审计 B8 的路由诊断日志：每个副作用操作打一条
// router.diagnostic 行，带 event= + request_id/conversationID/modelID 关联字段。
// 覆盖关键事件：candidates_resolved / channel_succeeded / channel_failed_failover /
// channel_failed_non_retryable / channel_failed_after_sink_started / channel_skipped_circuit_open。
package modeladapter

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"

	legacyruntime "cursor/internal/runtime"
)

// logCapture 把标准库 log 输出重定向到 buffer，返回恢复函数。
// log 是进程级全局，测试间用 mu 串行化避免输出交错。
var logCaptureMu sync.Mutex

func captureLog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	logCaptureMu.Lock()
	buf := &bytes.Buffer{}
	prev := log.Writer()
	log.SetOutput(buf)
	return buf, func() {
		log.SetOutput(prev)
		logCaptureMu.Unlock()
	}
}

// containsLine 在 buf 里找前缀为 "router.diagnostic event=<event>" 的行。
func containsLine(buf *bytes.Buffer, event string) bool {
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "router.diagnostic") && strings.Contains(line, "event="+event+" ") {
			return true
		}
	}
	return false
}

func TestRouterDiagnosticLogsFailoverSuccess(t *testing.T) {
	// 主候选 503 → failover 到备候选成功。
	primary := &fakeAdapter{err: fmt.Errorf("openai adapter status=503 body=down")}
	backup := &fakeAdapter{err: nil}
	resolver := &fakeResolver{
		channels: []*legacyruntime.ResolvedChannel{
			makeChannel("primary", "anthropic"),
			makeChannel("backup", "openai"),
		},
	}
	router := makeRouterWithFakes(resolver, NewCircuitBreakerRegistry(DefaultCircuitBreakerConfig()), openaiBackupAdapter(backup), anthropicPrimaryAdapter(primary))

	req := StreamRequest{ModelID: "gpt-5", RequestID: "req-1", ConversationID: "conv-1"}
	buf, restore := captureLog(t)
	defer restore()
	err := router.Stream(context.Background(), req, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("expected nil error after failover, got %v", err)
	}

	if !containsLine(buf, "candidates_resolved") {
		t.Errorf("缺少 candidates_resolved 诊断行\n%s", buf.String())
	}
	if !containsLine(buf, "channel_failed_failover") {
		t.Errorf("缺少 channel_failed_failover 诊断行\n%s", buf.String())
	}
	if !containsLine(buf, "channel_succeeded") {
		t.Errorf("缺少 channel_succeeded 诊断行\n%s", buf.String())
	}
	// 关联字段应带上 requestID 与 conversationID，便于跨日志追踪。
	if !strings.Contains(buf.String(), "request_id=req-1") || !strings.Contains(buf.String(), "conversation_id=conv-1") {
		t.Errorf("诊断行应带 request_id=req-1 与 conversation_id=conv-1\n%s", buf.String())
	}
}

func TestRouterDiagnosticLogsNonRetryableFailure(t *testing.T) {
	// 审计 N-02/F-07：400（请求体本身有问题，换渠道也失败）仍不可重试 → 直接透传，
	// 不 failover。401/403/404 已放宽为可重试（见 TestRouterFailoverOn401/404）。
	primary := &fakeAdapter{err: fmt.Errorf("openai adapter status=400 body=bad request")}
	backup := &fakeAdapter{err: nil}
	resolver := &fakeResolver{
		channels: []*legacyruntime.ResolvedChannel{
			makeChannel("primary", "openai"),
			makeChannel("backup", "anthropic"),
		},
	}
	router := makeRouterWithFakes(resolver, NewCircuitBreakerRegistry(DefaultCircuitBreakerConfig()), openaiBackupAdapter(primary), anthropicPrimaryAdapter(backup))

	req := StreamRequest{ModelID: "gpt-5", RequestID: "req-2"}
	buf, restore := captureLog(t)
	defer restore()
	err := router.Stream(context.Background(), req, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatalf("expected 400 error to propagate, got nil")
	}
	if !containsLine(buf, "channel_failed_non_retryable") {
		t.Errorf("缺少 channel_failed_non_retryable 诊断行\n%s", buf.String())
	}
	// 不可重试不应 failover，故不应出现 channel_failed_failover。
	if containsLine(buf, "channel_failed_failover") {
		t.Errorf("400 不可重试不应 failover，却出现 channel_failed_failover\n%s", buf.String())
	}
}

func TestRouterDiagnosticLogsAfterSinkStarted(t *testing.T) {
	// 主候选先发事件再 503 → 已首字节，不 failover，走 channel_failed_after_sink_started。
	event := ModelEvent{Kind: ModelEventKindTextDelta, Text: "partial"}
	primary := &fakeAdapter{sinkBeforeErr: &event, err: fmt.Errorf("openai adapter status=503 body=mid")}
	backup := &fakeAdapter{err: nil}
	resolver := &fakeResolver{
		channels: []*legacyruntime.ResolvedChannel{
			makeChannel("primary", "openai"),
			makeChannel("backup", "anthropic"),
		},
	}
	router := makeRouterWithFakes(resolver, NewCircuitBreakerRegistry(DefaultCircuitBreakerConfig()), openaiBackupAdapter(primary), anthropicPrimaryAdapter(backup))

	req := StreamRequest{ModelID: "gpt-5", RequestID: "req-3"}
	buf, restore := captureLog(t)
	defer restore()
	_ = router.Stream(context.Background(), req, func(ModelEvent) error { return nil })
	if !containsLine(buf, "channel_failed_after_sink_started") {
		t.Errorf("缺少 channel_failed_after_sink_started 诊断行\n%s", buf.String())
	}
}

func TestRouterDiagnosticLogsCircuitOpenSkip(t *testing.T) {
	// 单候选，熔断器 Open → 候选被跳过（permit.Allowed==false），最终返回 circuit open 错误。
	// 应出现 channel_skipped_circuit_open 诊断行。
	primary := &fakeAdapter{err: nil}
	channels := []*legacyruntime.ResolvedChannel{
		makeChannel("primary", "openai"),
	}
	resolver := &fakeResolver{channels: channels}
	reg := NewCircuitBreakerRegistry(DefaultCircuitBreakerConfig())
	cb := reg.Get("primary")
	threshold := int(DefaultCircuitBreakerConfig().FailureThreshold)
	for i := 0; i < threshold+1; i++ {
		cb.RecordFailure(false)
	}
	if cb.IsAvailable() {
		t.Fatalf("主候选熔断器应已 Open")
	}
	router := makeRouterWithFakes(resolver, reg, openaiBackupAdapter(primary), anthropicPrimaryAdapter(&fakeAdapter{}))

	req := StreamRequest{ModelID: "gpt-5", RequestID: "req-4"}
	buf, restore := captureLog(t)
	defer restore()
	err := router.Stream(context.Background(), req, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatalf("单候选熔断中应返回错误，got nil")
	}
	if !containsLine(buf, "channel_skipped_circuit_open") {
		t.Errorf("缺少 channel_skipped_circuit_open 诊断行\n%s", buf.String())
	}
}
