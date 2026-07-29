package modeladapter

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestShouldRectifyThinkingSignature 覆盖 cc-switch 对齐的 7 个签名错误场景与若干不应触发的反例。
func TestShouldRectifyThinkingSignature(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("connection refused"), false},
		{"plain 400 no signature", errors.New("anthropic adapter status=400 body={\"error\":\"bad request\"}"), false},
		// 场景1：thinking block 中的签名无效。
		{"scene1", errors.New(`anthropic adapter status=400 body={"type":"invalid_request_error","message":"Invalid 'signature' in 'thinking' block"}`), true},
		// 场景1b：Gemini / 第三方 "Thought signature is not valid"。
		{"scene1b", errors.New("anthropic adapter status=400 body=Unable to submit request because Thought signature is not valid"), true},
		// 场景2：assistant 消息必须以 thinking block 开头。
		{"scene2", errors.New("anthropic adapter status=400 body=messages: assistant message must start with a thinking block"), true},
		// 场景3：expected thinking or redacted_thinking, found tool_use。
		{"scene3", errors.New("anthropic adapter status=400 body=Expected `thinking` or `redacted_thinking`, but found `tool_use`"), true},
		// 场景3 反例：缺 tool_use 不应触发（避免过宽）。
		{"scene3-no-tool-use", errors.New("expected thinking or redacted_thinking, found text"), false},
		// 场景4：signature 字段必需但缺失。
		{"scene4", errors.New("anthropic adapter status=400 body=signature: Field required"), true},
		// 场景5：signature 字段不被接受（第三方渠道）。
		{"scene5", errors.New("anthropic adapter status=422 body=messages.0.content.0.signature: Extra inputs are not permitted"), true},
		// 场景6：thinking/redacted_thinking 块被修改。
		{"scene6", errors.New("anthropic adapter status=400 body=thinking or redacted_thinking blocks cannot be modified"), true},
		// 场景7：非法请求兜底。
		{"scene7-cn", errors.New("anthropic adapter status=400 body=非法请求"), true},
		{"scene7-illegal", errors.New("anthropic adapter status=400 body=illegal request: bad signature"), true},
		{"scene7-invalid", errors.New("anthropic adapter status=400 body=invalid request body"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldRectifyThinkingSignature(c.err); got != c.want {
				t.Errorf("shouldRectifyThinkingSignature(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// TestRectifyMessagesForThinkingSignature 校验整流只清 assistant 推理/签名字段，不动 user/tool，
// 且仅当确实有改动时返回 (out, true)。
func TestRectifyMessagesForThinkingSignature(t *testing.T) {
	summary := []byte(`[{"type":"summary_text","text":"s"}]`)
	input := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "answer", ReasoningContent: "thinking...", ReasoningSignature: "sig-1", ReasoningSignatureSource: ReasoningSignatureSourceAnthropic},
		{Role: "assistant", Content: "tool answer", OpenAIResponsesReasoningID: "r-id", OpenAIResponsesReasoningStatus: "completed", OpenAIResponsesReasoningSummary: summary},
		{Role: "tool", Content: "result", ToolCallID: "call-1"},
		{Role: "assistant", Content: "plain answer"}, // 无推理字段，应原样保留
	}

	out, changed := rectifyMessagesForThinkingSignature(input)
	if !changed {
		t.Fatalf("expected changed=true when assistant carries reasoning fields")
	}
	if len(out) != len(input) {
		t.Fatalf("expected %d messages, got %d", len(input), len(out))
	}

	// user / tool / 无推理 assistant 原样。
	if out[0].Content != "hi" {
		t.Errorf("user message altered: %q", out[0].Content)
	}
	if out[3].Content != "result" || out[3].ToolCallID != "call-1" {
		t.Errorf("tool message altered: %+v", out[3])
	}
	if out[4].Content != "plain answer" {
		t.Errorf("plain assistant altered: %q", out[4].Content)
	}

	// assistant[1] 推理/签名清空，正文保留。
	if out[1].ReasoningContent != "" || out[1].ReasoningSignature != "" || out[1].ReasoningSignatureSource != "" {
		t.Errorf("assistant[1] reasoning/signature not cleared: %+v", out[1])
	}
	if out[1].Content != "answer" {
		t.Errorf("assistant[1] content should be preserved, got %q", out[1].Content)
	}

	// assistant[2] OpenAI Responses 推理字段清空。
	if out[2].OpenAIResponsesReasoningID != "" || out[2].OpenAIResponsesReasoningStatus != "" || len(out[2].OpenAIResponsesReasoningSummary) != 0 {
		t.Errorf("assistant[2] openai responses reasoning not cleared: %+v", out[2])
	}

	// 入参未被修改（深拷贝语义）。
	if input[1].ReasoningSignature != "sig-1" {
		t.Errorf("rectify mutated input slice (reasoning signature): %q", input[1].ReasoningSignature)
	}

	// 无任何推理字段时返回 (nil, false)。
	plain := []Message{{Role: "user", Content: "x"}, {Role: "assistant", Content: "y"}}
	if out2, changed2 := rectifyMessagesForThinkingSignature(plain); changed2 || out2 != nil {
		t.Errorf("expected (nil,false) for plain history, got (%v,%v)", out2, changed2)
	}
}

// scriptedDoStream 是可编程的流替身：按调用序返回预设结果，并记录每次入参的 messages 摘要。
type scriptedDoStream struct {
	steps []scriptedStep
	calls []string // 每次调用的 messages 摘要（role[:reasoning] 标记）
	idx   int
}

type scriptedStep struct {
	err error
	// sinkBeforeErr 若非 nil，返回前先发一个事件（模拟首字节已发）。
	sinkBeforeErr *ModelEvent
}

func (s *scriptedDoStream) do(req StreamRequest, sink func(ModelEvent) error) error {
	tag := describeMessages(req.Messages)
	s.calls = append(s.calls, tag)
	if s.idx >= len(s.steps) {
		s.idx++
		return nil
	}
	step := s.steps[s.idx]
	s.idx++
	if step.sinkBeforeErr != nil {
		_ = sink(*step.sinkBeforeErr)
	}
	return step.err
}

func describeMessages(msgs []Message) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteByte('|')
		}
		b.WriteString(m.Role)
		if strings.TrimSpace(m.ReasoningSignature) != "" || strings.TrimSpace(m.ReasoningContent) != "" {
			b.WriteString("+reasoning")
		}
	}
	return b.String()
}

func makeRectifiableRequest() StreamRequest {
	return StreamRequest{
		// 按约定预初始化 RequestKnobs（router.applyChannelToRequest 每次都写 knob），
		// 使 setThinkingRectified 的写入在 streamWithThinkingRectifier 的值拷贝与调用方间共享可见。
		RequestKnobs: map[string]any{},
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "answer", ReasoningContent: "t", ReasoningSignature: "sig", ReasoningSignatureSource: ReasoningSignatureSourceAnthropic},
		},
	}
}

// TestStreamWithThinkingRectifier_RetriesAfterSignatureError 首试命中签名错误 → 整流后重试成功。
func TestStreamWithThinkingRectifier_RetriesAfterSignatureError(t *testing.T) {
	script := &scriptedDoStream{
		steps: []scriptedStep{
			{err: errors.New(`anthropic adapter status=400 body=Invalid 'signature' in 'thinking' block`)},
			{err: nil},
		},
	}
	req := makeRectifiableRequest()

	err := streamWithThinkingRectifier(context.Background(), req, func(ModelEvent) error { return nil }, script.do)
	if err != nil {
		t.Fatalf("expected nil after rectified retry, got %v", err)
	}
	if len(script.calls) != 2 {
		t.Fatalf("expected doStream called twice, got %d (%v)", len(script.calls), script.calls)
	}
	// 第二次调用的 messages 应已剥离 reasoning。
	if strings.Contains(script.calls[1], "+reasoning") {
		t.Errorf("rectified retry still carries reasoning: %s", script.calls[1])
	}
	if !isThinkingRectified(req) {
		t.Errorf("expected thinking_rectified knob set after retry")
	}
}

// TestStreamWithThinkingRectifier_NoRetryWhenSignatureAbsent 非签名错误不整流。
func TestStreamWithThinkingRectifier_NoRetryWhenSignatureAbsent(t *testing.T) {
	script := &scriptedDoStream{
		steps: []scriptedStep{{err: errors.New("anthropic adapter status=401 body=unauthorized")}},
	}
	req := makeRectifiableRequest()

	err := streamWithThinkingRectifier(context.Background(), req, func(ModelEvent) error { return nil }, script.do)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 error propagated, got %v", err)
	}
	if len(script.calls) != 1 {
		t.Errorf("expected single attempt for non-signature error, got %d", len(script.calls))
	}
	if isThinkingRectified(req) {
		t.Errorf("rectified knob should not be set for non-signature error")
	}
}

// TestStreamWithThinkingRectifier_NoRetryAfterSinkStarted 首字节已发不重试（避免双发）。
func TestStreamWithThinkingRectifier_NoRetryAfterSinkStarted(t *testing.T) {
	script := &scriptedDoStream{
		steps: []scriptedStep{{
			sinkBeforeErr: &ModelEvent{Kind: ModelEventKindTextDelta, Text: "x"},
			err:           errors.New(`anthropic adapter status=400 body=Invalid 'signature' in 'thinking' block`),
		}},
	}
	req := makeRectifiableRequest()

	err := streamWithThinkingRectifier(context.Background(), req, func(ModelEvent) error { return nil }, script.do)
	if err == nil {
		t.Fatalf("expected error propagated after sink started")
	}
	if len(script.calls) != 1 {
		t.Errorf("expected no retry after sink started, got %d calls", len(script.calls))
	}
}

// TestStreamWithThinkingRectifier_NoRetryWhenAlreadyRectified 整流闸门：已整流过不再重试。
func TestStreamWithThinkingRectifier_NoRetryWhenAlreadyRectified(t *testing.T) {
	script := &scriptedDoStream{
		steps: []scriptedStep{
			{err: errors.New(`anthropic adapter status=400 body=Invalid 'signature' in 'thinking' block`)},
		},
	}
	req := makeRectifiableRequest()
	setThinkingRectified(&req) // 模拟本请求已被整流过

	err := streamWithThinkingRectifier(context.Background(), req, func(ModelEvent) error { return nil }, script.do)
	if err == nil {
		t.Fatalf("expected error propagated without second rectify")
	}
	if len(script.calls) != 1 {
		t.Errorf("expected single attempt when already rectified, got %d", len(script.calls))
	}
}

// TestStreamWithThinkingRectifier_NoRetryWhenNothingToRectify 历史无推理字段时不整流（changed=false 旁路）。
func TestStreamWithThinkingRectifier_NoRetryWhenNothingToRectify(t *testing.T) {
	script := &scriptedDoStream{
		steps: []scriptedStep{{err: errors.New(`anthropic adapter status=400 body=Invalid 'signature' in 'thinking' block`)}},
	}
	req := StreamRequest{
		Messages: []Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "answer"}},
	}

	err := streamWithThinkingRectifier(context.Background(), req, func(ModelEvent) error { return nil }, script.do)
	if err == nil {
		t.Fatalf("expected error propagated when nothing to rectify")
	}
	if len(script.calls) != 1 {
		t.Errorf("expected single attempt when nothing to rectify, got %d", len(script.calls))
	}
}

// TestStreamWithThinkingRectifier_SecondFailurePropagated 整流后仍失败，透传第二次错误。
func TestStreamWithThinkingRectifier_SecondFailurePropagated(t *testing.T) {
	script := &scriptedDoStream{
		steps: []scriptedStep{
			{err: errors.New(`anthropic adapter status=400 body=Invalid 'signature' in 'thinking' block`)},
			{err: errors.New(`anthropic adapter status=500 body=internal`)},
		},
	}
	req := makeRectifiableRequest()

	err := streamWithThinkingRectifier(context.Background(), req, func(ModelEvent) error { return nil }, script.do)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected second-attempt 500 error propagated, got %v", err)
	}
	if len(script.calls) != 2 {
		t.Errorf("expected two attempts, got %d", len(script.calls))
	}
	// 不会再有第三次（rectifiedOnce 闸门）。
	if script.idx != 2 {
		t.Errorf("expected exactly 2 doStream invocations, got %d", script.idx)
	}
}

// TestStreamWithThinkingRectifier_CancelledContextNoRetry 客户端已取消不重试。
func TestStreamWithThinkingRectifier_CancelledContextNoRetry(t *testing.T) {
	script := &scriptedDoStream{
		steps: []scriptedStep{{err: errors.New(`anthropic adapter status=400 body=Invalid 'signature' in 'thinking' block`)}},
	}
	req := makeRectifiableRequest()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := streamWithThinkingRectifier(ctx, req, func(ModelEvent) error { return nil }, script.do)
	if err == nil {
		t.Fatalf("expected error propagated on cancelled ctx")
	}
	if len(script.calls) != 1 {
		t.Errorf("expected no retry on cancelled ctx, got %d", len(script.calls))
	}
}
