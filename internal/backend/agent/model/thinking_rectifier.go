// thinking_rectifier.go 实现 Thinking Signature 整流器（审计第二部分 A3 / 路线图 #12）。
//
// 借鉴 cc-switch src-tauri/src/proxy/thinking_rectifier.rs：上游返回 thinking signature
// 校验失败时，自动剥离请求里的 thinking / redacted_thinking block 与 signature 字段后重试，
// 而不是直接把签名错误透传给用户。这种"错误触发 → 自动整流 → 重试"的自愈模式对走 Anthropic
// 兼容路由的第三方中转尤其有用——它们的 signature/reasoning 实现参差，常常回签无效签名。
//
// 在 cursor-switch 里，provider 请求体由适配器从 StreamRequest.Messages 构造。thinking block
// 与 signature 在统一消息模型上的落点是 Message.ReasoningContent / ReasoningSignature /
// ReasoningSignatureSource（OpenAI Responses 另有 OpenAIResponsesReasoning* 字段）。因此整流
// 操作等价于：把会话历史里 assistant 消息携带的推理内容与签名一并清空后重试，让 provider 视
// 作"无 thinking 历史的新请求"，绕开签名校验失败。
//
// 作用域：仅清 assistant 消息（thinking / signature 是 assistant 侧产物），user/tool 消息不动。
// 触发后只重试一次（rectifiedOnce 闸门），避免与一个真正坏掉的 provider 死循环。
package modeladapter

import (
	"context"
	"strings"
)

// thinkingRectifiedKnob 是 RequestKnobs 里标记"本次请求已整流过推理历史"的键。
// 整流重试只允许一次：第一次失败后整流消息、置本键为 true，第二次若再失败就不再整流，
// 直接把错误透传给上层（Router 是否 failover 由 isRetryableChannelError 决定）。
const thinkingRectifiedKnob = "thinking_rectified"

// streamWithThinkingRectifier 是 A3 的接线层：把"实际发流"包成"发一次 → 若命中 thinking
// signature 类错误且尚未首字节、尚未整流过 → 整流消息历史后重试一次"。
//
// 为什么放在 adapter 内、而非 Router：
//   - thinking signature 错误通常是 HTTP 400，isRetryableChannelError 视为不可重试，Router
//     不会 failover，直接把签名错误透传给用户。根因是会话历史里 assistant 消息携带的无效签名，
//     换候选治标不治本——同一份历史发到下一个候选大概率还是 400。必须整流消息本身。
//   - 整流重试必须发生在 sink 首字节之前（否则双发）。签名校验失败是 provider 在流开始前就
//     拒绝请求，正好处于 sinkStarted=false 阶段，故本地再用 sinkStarted 闸门兜底：一旦首字节
//     已发，即使错误可整流也绝不重试。
//
// doStream 是真正的流方法（AnthropicAdapter.streamOnce / OpenAIAdapter.streamOnce）。
// 它接收一份（可能已被整流过的）req 与一个 sink，返回流结果。本函数保证：
//   - 至多调用 doStream 两次（首试 + 整流后重试）。
//   - 整流仅当 shouldRectifyThinkingSignature(err) && !sinkStarted && !已整流过 时触发。
//   - 整流后仍失败，透传第二次的错误（让上层决定 failover）。
func streamWithThinkingRectifier(
	ctx context.Context,
	req StreamRequest,
	sink func(ModelEvent) error,
	doStream func(StreamRequest, func(ModelEvent) error) error,
) error {
	// 本地 sink 闸门：与 Router 的 sinkStarted 同理——任何事件触达 sink 即认为流已开始，
	// 此后即便 provider 报错也绝不重试（重试会向下游再发一遍事件，双发比报错更糟）。
	sinkStarted := false
	localSink := func(ev ModelEvent) error {
		sinkStarted = true
		return sink(ev)
	}

	firstErr := doStream(req, localSink)
	if firstErr == nil {
		return nil
	}
	// 首字节已发 → 不整流重试（即便错误形态可整流）。
	if sinkStarted {
		return firstErr
	}
	// 客户端已取消 → 重试无意义。
	if ctx == nil || ctx.Err() != nil {
		return firstErr
	}
	// 已整流过 → 仍失败，透传给上层。
	if isThinkingRectified(req) {
		return firstErr
	}
	if !shouldRectifyThinkingSignature(firstErr) {
		return firstErr
	}

	rectified, changed := rectifyMessagesForThinkingSignature(req.Messages)
	if !changed {
		// 历史里没有可剥离的推理字段——整流无意义，直接透传首试错误。
		return firstErr
	}

	// 用整流后的历史重试。标记已整流，保证即便二次失败也不会再整流（rectifiedOnce 闸门）。
	// 注意：req 是值拷贝，但 RequestKnobs 是 map（引用语义）。只要调用方按约定预初始化
	// RequestKnobs（Router 的 applyChannelToRequest 每次都写 knob，故必然非 nil），
	// setThinkingRectified 写入会被调用方共享可见。这里再保底初始化一次，确保即便调用方
	// 传 nil 也能在内部两次 doStream 间正确生效闸门。
	req.Messages = rectified
	setThinkingRectified(&req)
	return doStream(req, localSink)
}

// isThinkingRectified 判断本次请求是否已经整流过推理历史。
func isThinkingRectified(req StreamRequest) bool {
	if len(req.RequestKnobs) == 0 {
		return false
	}
	value, ok := req.RequestKnobs[thinkingRectifiedKnob]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(typed, "true")
	default:
		return false
	}
}

// setThinkingRectified 标记本次请求已整流过推理历史（rectifiedOnce 闸门）。
func setThinkingRectified(req *StreamRequest) {
	if req.RequestKnobs == nil {
		req.RequestKnobs = map[string]any{}
	}
	req.RequestKnobs[thinkingRectifiedKnob] = true
}

// shouldRectifyThinkingSignature 判定一个 channel 错误是否属于 thinking signature 类问题，
// 值得剥掉推理历史后重试。
//
// 错误形态来自 buildHTTPStatusError（http_error.go）：形如
// "anthropic adapter status=400 body=<provider 返回的 JSON 或文本>"。provider 的原始错误信息
// 会原样出现在 body 里（嵌套 JSON 也会被展开成字符串），故按子串匹配 cc-switch 的 7 个场景。
// 大小写不敏感。
func shouldRectifyThinkingSignature(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())

	// 场景1：thinking block 中的签名无效
	//   "Invalid 'signature' in 'thinking' block"
	if containsAll(lower, "invalid", "signature", "thinking", "block") {
		return true
	}

	// 场景1b：Gemini / 第三方渠道 "Thought signature is not valid"
	if strings.Contains(lower, "thought signature") &&
		(strings.Contains(lower, "not valid") || strings.Contains(lower, "invalid")) {
		return true
	}

	// 场景2：assistant 消息必须以 thinking block 开头
	if strings.Contains(lower, "must start with a thinking block") {
		return true
	}

	// 场景3：expected thinking or redacted_thinking, found tool_use
	//   与 CCH 对齐：要求明确包含 tool_use，避免过宽匹配。
	if strings.Contains(lower, "expected") &&
		(strings.Contains(lower, "thinking") || strings.Contains(lower, "redacted_thinking")) &&
		strings.Contains(lower, "found") &&
		strings.Contains(lower, "tool_use") {
		return true
	}

	// 场景4：signature 字段必需但缺失
	if strings.Contains(lower, "signature") && strings.Contains(lower, "field required") {
		return true
	}

	// 场景5：signature 字段不被接受（第三方渠道）
	if strings.Contains(lower, "signature") && strings.Contains(lower, "extra inputs are not permitted") {
		return true
	}

	// 场景6：thinking/redacted_thinking 块被修改
	if (strings.Contains(lower, "thinking") || strings.Contains(lower, "redacted_thinking")) &&
		strings.Contains(lower, "cannot be modified") {
		return true
	}

	// 场景7：非法请求（与 CCH 对齐，按 invalid request 统一兜底）
	if strings.Contains(lower, "非法请求") ||
		strings.Contains(lower, "illegal request") ||
		strings.Contains(lower, "invalid request") {
		return true
	}

	return false
}

// containsAll 判断 s 是否同时包含全部 parts（已假设 lower 化输入）。
func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if p == "" {
			continue
		}
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

// rectifyMessagesForThinkingSignature 返回一份剥离了推理内容与签名的历史消息。
//
// 仅清 assistant 消息的 ReasoningContent / ReasoningSignature / ReasoningSignatureSource
// 与 OpenAI Responses 推理字段（OpenAIResponsesReasoningID/Status/Summary）。user/tool 消息
// 不动——thinking 与 signature 是 assistant 侧产物，清 user 侧没有意义还会丢用户输入。
//
// 返回新切片，不修改入参；第二个返回值表示是否有消息被改动（用于决定是否值得重试）。
// 若没有任何 assistant 消息携带推理字段，返回 (nil, false)——此时整流无意义，调用方应直接 failover。
func rectifyMessagesForThinkingSignature(msgs []Message) ([]Message, bool) {
	modified := false
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if strings.TrimSpace(m.Role) == "assistant" &&
			(hasReasoningFields(m)) {
			m.ReasoningContent = ""
			m.ReasoningSignature = ""
			m.ReasoningSignatureSource = ""
			m.OpenAIResponsesReasoningID = ""
			m.OpenAIResponsesReasoningStatus = ""
			m.OpenAIResponsesReasoningSummary = nil
			modified = true
		}
		out = append(out, m)
	}
	if !modified {
		return nil, false
	}
	return out, true
}

// hasReasoningFields 判断 assistant 消息是否携带任何推理/签名字段（需整流）。
func hasReasoningFields(m Message) bool {
	return strings.TrimSpace(m.ReasoningContent) != "" ||
		strings.TrimSpace(m.ReasoningSignature) != "" ||
		strings.TrimSpace(m.ReasoningSignatureSource) != "" ||
		strings.TrimSpace(m.OpenAIResponsesReasoningID) != "" ||
		strings.TrimSpace(m.OpenAIResponsesReasoningStatus) != "" ||
		len(m.OpenAIResponsesReasoningSummary) != 0
}
