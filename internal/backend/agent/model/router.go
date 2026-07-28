// router.go 按模型标识选择 OpenAI 或 Anthropic 兼容适配器。
package modeladapter

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"

	legacyruntime "cursor/internal/runtime"
)

// Router 是 MVP 阶段的模型适配路由器。
type Router struct {
	// openai 负责 OpenAI 兼容流式请求。
	openai ModelAdapter
	// anthropic 负责 Anthropic 兼容流式请求。
	anthropic ModelAdapter
	// resolver 负责从本地配置中解析实际模型通道。
	resolver ChannelResolver
	// breakers 按渠道 ID 跟踪熔断状态，驱动 B2 failover 候选选择与失败计数。
	// 为 nil 时退化为"无熔断"行为——每个候选都按 Priority 顺序直接尝试。
	breakers *CircuitBreakerRegistry
}

type ChannelResolver interface {
	// SelectChannelForModel 返回主候选（候选链 [0]）。保留给只读单字段调用点。
	SelectChannelForModel(context.Context, string) (*legacyruntime.ResolvedChannel, error)
	// SelectChannelsForModel 返回 modelID 的全部 enabled 候选（B2 failover 候选链），
	// 已按 adapter.Priority 升序稳定排序。空切片表示无可用候选。
	SelectChannelsForModel(context.Context, string) ([]*legacyruntime.ResolvedChannel, error)
	ProviderStreamIdleTimeout(context.Context) time.Duration
}

// NewRouter 创建模型适配路由器。breakers 可为 nil（禁用熔断，仅 failover）。
func NewRouter(resolver ChannelResolver, breakers *CircuitBreakerRegistry) *Router {
	return &Router{
		openai:    NewOpenAIAdapter(),
		anthropic: NewAnthropicAdapter(),
		resolver:  resolver,
		breakers:  breakers,
	}
}

// Stream 根据模型标识选择具体 provider 并转发请求。
//
// B2 failover：解析出候选链（按 Priority 升序），逐个尝试。
//   - 熔断中（IsAvailable==false）的候选排到末尾兜底，避免候选全空直接报错。
//   - 候选失败后：仅当错误可重试（连接层/5xx/429/idle-timeout）且**尚未向 sink 发出任何事件**
//     时才切换下一个候选；已首字节或 4xx 等不可重试错误直接透传。
//   - sink wrapper 记录 sinkStarted——任何 ModelEvent 触达 sink 后立即置 true，
//     保证 failover 不会对同一请求双发事件。
func (router *Router) Stream(ctx context.Context, req StreamRequest, sink func(ModelEvent) error) error {
	if router == nil || router.resolver == nil {
		return fmt.Errorf("model adapter resolver is unavailable")
	}
	channels, err := router.resolver.SelectChannelsForModel(ctx, req.ModelID)
	if err != nil {
		return err
	}
	if len(channels) == 0 {
		return fmt.Errorf("no available channel for model %q", req.ModelID)
	}

	idleTimeout := router.resolver.ProviderStreamIdleTimeout(ctx)
	candidates := router.orderCandidates(channels)

	var lastErr error
	for index, channel := range candidates {
		cb := router.breakerFor(channel.ID)
		permit := cb.AllowRequest()
		if !permit.Allowed {
			// 熔断中：跳过本候选，保留兜底（末尾候选熔断时仍会被跳过，最终走 lastErr）。
			if lastErr == nil {
				lastErr = fmt.Errorf("channel %q circuit open", channel.ID)
			}
			continue
		}

		resolved := router.applyChannelToRequest(req, channel, idleTimeout)

		// sink wrapper：任何事件触达 sink 即标记，作为 failover 闸门；
		// 同时捕获 sink 写错误，区分"provider 出错"与"下游写失败"——后者不计 provider 故障。
		sinkStarted := false
		var sinkErr error
		wrappedSink := func(ev ModelEvent) error {
			sinkStarted = true
			if e := sink(ev); e != nil {
				sinkErr = e
				return e
			}
			return nil
		}

		adapter, aerr := router.adapterFor(resolved.Provider)
		if aerr != nil {
			// provider 类型不支持：构造错误，不可重试（换候选也是同类型问题）。
			// 这是配置/路由层错误，不是 provider 故障，不计熔断。
			lastErr = aerr
			if sinkStarted {
				return lastErr
			}
			continue
		}

		streamErr := adapter.Stream(ctx, resolved, wrappedSink)

		if streamErr == nil && sinkErr == nil {
			cb.RecordSuccess(permit.UsedHalfOpenPermit)
			return nil
		}
		// 失败原因归类后决定是否计入 provider 熔断：
		//   - 客户端取消/全局超时（context.Canceled/DeadlineExceeded）：不是 provider 的错，不计，不 failover。
		//   - sink 写失败（下游断连）：不是 provider 的错，不计；已 sinkStarted 本来就不 failover。
		//   - 其余 provider 真错误：计 failure，按 isRetryableChannelError 决定 failover。
		effErr := streamErr
		if effErr == nil {
			effErr = sinkErr
		}
		if !isClientSideCancellation(ctx, effErr) && sinkErr == nil {
			cb.RecordFailure(permit.UsedHalfOpenPermit)
		}
		lastErr = effErr

		// 已首字节 → 绝不 failover（双发比报错更糟）。
		if sinkStarted {
			return lastErr
		}
		// 客户端取消 → 不 failover（客户端已走，换候选无意义）。
		if isClientSideCancellation(ctx, effErr) {
			return lastErr
		}
		// 不可重试错误（4xx 除 429）→ 换候选也会同样失败，透传。
		if !isRetryableChannelError(effErr) {
			return lastErr
		}
		// 可重试 + 未吐事件 → 换下一个候选。
		if index < len(candidates)-1 {
			log.Printf("router failover: model=%q from=%s to=%s reason=%v",
				strings.TrimSpace(req.ModelID), strings.TrimSpace(channel.ID),
				strings.TrimSpace(candidates[index+1].ID), effErr)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no usable channel for model %q", req.ModelID)
	}
	return lastErr
}

// orderCandidates 把熔断中（IsAvailable==false）的候选排到末尾，保留兜底。
// 已按 Priority 排序的候选顺序在"可用组"与"熔断组"内部各自保持稳定。
func (router *Router) orderCandidates(channels []*legacyruntime.ResolvedChannel) []*legacyruntime.ResolvedChannel {
	if router == nil || router.breakers == nil || len(channels) <= 1 {
		return channels
	}
	available := make([]*legacyruntime.ResolvedChannel, 0, len(channels))
	blocked := make([]*legacyruntime.ResolvedChannel, 0, len(channels))
	for _, ch := range channels {
		cb := router.breakers.Get(ch.ID)
		if cb.IsAvailable() {
			available = append(available, ch)
		} else {
			blocked = append(blocked, ch)
		}
	}
	return append(available, blocked...)
}

// breakerFor 返回指定渠道的熔断器；router 无 registry 时返回一个永远放行的 no-op breaker。
func (router *Router) breakerFor(channelID string) channelBreaker {
	if router == nil || router.breakers == nil {
		return noopCircuitBreakerInstance
	}
	return router.breakers.Get(channelID)
}

// channelBreaker 是 Router 消费熔断器所需的最小接口，让 *CircuitBreaker 与 no-op 占位实现统一。
type channelBreaker interface {
	IsAvailable() bool
	AllowRequest() AllowResult
	RecordSuccess(usedHalfOpenPermit bool)
	RecordFailure(usedHalfOpenPermit bool)
}

func (router *Router) adapterFor(provider string) (ModelAdapter, error) {
	switch strings.TrimSpace(provider) {
	case "anthropic":
		return router.anthropic, nil
	case "openai":
		return router.openai, nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", provider)
	}
}

// applyChannelToRequest 把单个 ResolvedChannel 的字段填充进 StreamRequest，
// 并完成 thinking effort / max_tokens / RequestKnobs 的归一化。每次 failover 对每个候选调一次。
func (router *Router) applyChannelToRequest(req StreamRequest, channel *legacyruntime.ResolvedChannel, idleTimeout time.Duration) StreamRequest {
	resolved := req
	resolved.Provider = strings.TrimSpace(channel.Provider)
	resolved.BaseURL = strings.TrimSpace(channel.BaseURL)
	resolved.APIKey = strings.TrimSpace(channel.APIKey)
	resolved.ProviderModelID = strings.TrimSpace(channel.Model)
	resolved.ResolvedChannelID = strings.TrimSpace(channel.ID)
	resolved.ResolvedChannelName = strings.TrimSpace(channel.Name)
	resolved.ResolvedContextWindowTokens = channel.ContextWindowTokens
	resolved.ReasoningEffort = openAIReasoningEffortFromRuntime(channel.ReasoningEffort)
	resolved.OpenAIEndpoint = strings.TrimSpace(channel.OpenAIEndpoint)
	resolved.OpenAIExtraParamsEnabled = channel.OpenAIExtraParamsEnabled
	resolved.OpenAIExtraParamsJSON = strings.TrimSpace(channel.OpenAIExtraParamsJSON)
	resolved.CustomHeadersEnabled = channel.CustomHeadersEnabled
	resolved.CustomHeadersJSON = strings.TrimSpace(channel.CustomHeadersJSON)
	resolved.AnthropicExtraParamsEnabled = channel.AnthropicExtraParamsEnabled
	resolved.AnthropicExtraParamsJSON = strings.TrimSpace(channel.AnthropicExtraParamsJSON)
	resolved.AnthropicMaxTokens = channel.AnthropicMaxTokens
	resolved.AnthropicThinkingEffort = strings.TrimSpace(channel.AnthropicThinkingEffort)
	resolved.ThinkingBudgetTokens = channel.ThinkingBudgetTokens
	resolved.ProviderStreamIdleTimeout = idleTimeout
	runtimeThinkingEffort := normalizeRuntimeThinkingEffort(req.ThinkingEffort)
	if runtimeThinkingEffort != "" {
		resolved.ThinkingEffort = runtimeThinkingEffort
		if runtimeThinkingEffort == "disabled" {
			resolved.ReasoningEffort = ""
			resolved.AnthropicThinkingEffort = ""
		} else {
			resolved.ReasoningEffort = openAIReasoningEffortFromRuntime(runtimeThinkingEffort)
			resolved.AnthropicThinkingEffort = runtimeThinkingEffort
		}
	} else {
		resolved.ThinkingEffort = ""
	}
	if resolved.MaxTokens <= 0 && channel.MaxTokens > 0 {
		resolved.MaxTokens = channel.MaxTokens
	}
	if req.MaxTokens > 0 && (resolved.AnthropicMaxTokens <= 0 || req.MaxTokens < resolved.AnthropicMaxTokens) {
		resolved.AnthropicMaxTokens = req.MaxTokens
	}
	if resolved.AnthropicMaxTokens <= 0 && resolved.MaxTokens > 0 {
		resolved.AnthropicMaxTokens = resolved.MaxTokens
	}
	if resolved.ProviderModelID == "" {
		resolved.ProviderModelID = strings.TrimSpace(req.ModelID)
	}
	resolved.Messages = sanitizeProviderMessages(req.Messages)
	if resolved.RequestKnobs != nil {
		resolved.RequestKnobs["max_tokens"] = resolved.MaxTokens
		if runtimeThinkingEffort != "" {
			resolved.RequestKnobs["runtime_thinking_effort"] = runtimeThinkingEffort
		} else {
			delete(resolved.RequestKnobs, "runtime_thinking_effort")
		}
		if resolved.Provider == "openai" {
			if strings.TrimSpace(resolved.ReasoningEffort) != "" {
				resolved.RequestKnobs["reasoning_effort"] = strings.TrimSpace(resolved.ReasoningEffort)
			} else {
				delete(resolved.RequestKnobs, "reasoning_effort")
			}
			resolved.RequestKnobs["openai_endpoint"] = resolved.OpenAIEndpoint
			resolved.RequestKnobs["openai_extra_params_enabled"] = resolved.OpenAIExtraParamsEnabled
			resolved.RequestKnobs["custom_headers_enabled"] = resolved.CustomHeadersEnabled
		} else if resolved.Provider == "anthropic" {
			delete(resolved.RequestKnobs, "reasoning_effort")
			resolved.RequestKnobs["custom_headers_enabled"] = resolved.CustomHeadersEnabled
			resolved.RequestKnobs["anthropic_extra_params_enabled"] = resolved.AnthropicExtraParamsEnabled
			anthropicMaxTokens := maxAnthropicTokens(resolved)
			resolved.RequestKnobs["max_tokens"] = anthropicMaxTokens
			resolved.RequestKnobs["anthropic_max_tokens"] = anthropicMaxTokens
			if strings.TrimSpace(resolved.AnthropicThinkingEffort) != "" {
				resolved.RequestKnobs["anthropic_thinking_effort"] = anthropicThinkingEffort(resolved)
			} else {
				delete(resolved.RequestKnobs, "anthropic_thinking_effort")
			}
		}
	}
	return resolved
}

// isClientSideCancellation 判定错误是否源于客户端取消或全局超时，而非 provider 故障。
//
// 触发条件：
//   - 请求 ctx 已取消/超时（客户端断连、用户停止、上层 deadline）。
//   - 错误链包含 context.Canceled 或 context.DeadlineExceeded（net/http 会把它包装进 *url.Error）。
//
// 这类错误不计入 provider 熔断（健康 provider 不该因客户端取消被 Open），
// 也不 failover（客户端已走，换候选无意义）。
func isClientSideCancellation(ctx context.Context, err error) bool {
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// isRetryableChannelError 判定一个 channel 错误是否值得 failover 到下一个候选。
//
// 可重试（换 provider 可能成功）：
//   - 连接层错误：*url.Error（DNS/TLS/conn-refused/连接超时），由 net/http 直接返回。
//   - HTTP 5xx：buildHTTPStatusError 的 "status=5NN" 字符串形态。
//   - HTTP 429：限流，换 provider 可绕过。
//   - 流 idle 超时：providerStreamIdleTimeoutError 的 "provider stream idle timeout after ..." 字符串。
//   - 流未合法终止（F-20）：streamTerminatorMissingError 的 "provider stream truncated: ..." 形态。
//     provider 提前 EOF、缺终止事件、零事件——换 provider 可能正常完成流。
//   - body 读取失败：buildHTTPStatusError 的 "body_read_error=" 形态（响应体中途断流）。
//
// 不可重试（换 provider 也会同样失败）：
//   - HTTP 4xx 除 429：请求本身有问题（鉴权/参数/模型不存在）。
//   - 其它未知错误：保守不重试，透传给客户端可见真实原因。
//
// 错误形态由 buildHTTPStatusError（http_error.go）与 providerStreamIdleTimeoutError（stream_idle.go）
// 产生，均为 fmt.Errorf 字符串，无结构化类型，故按子串/前缀判定。
func isRetryableChannelError(err error) bool {
	if err == nil {
		return false
	}
	// 连接层错误：net/http 把 Do 的网络错误包装成 *url.Error。
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	text := err.Error()
	// 流 idle 超时（两种字符串形态都以 "provider stream idle timeout after " 开头）。
	if strings.HasPrefix(text, "provider stream idle timeout after ") {
		return true
	}
	// F-20：流未以合法终止事件结束（缺 [DONE]/response.completed/message_stop 或零事件）。
	if strings.HasPrefix(text, "provider stream truncated: ") {
		return true
	}
	// F-21：流资源预算超限（总字节/总事件数），换 provider 可能不超限。
	if strings.HasPrefix(text, "provider stream budget exceeded: ") {
		return true
	}
	// HTTP 状态码错误：解析 "status=NNN"。
	status, ok := extractHTTPStatus(text)
	if !ok {
		return false
	}
	if status == 429 || status >= 500 {
		return true
	}
	// 4xx（除 429）不可重试。body_read_error= 形态也带 status=，归到这里：5xx 才重试，
	// 4xx 的 body_read_error 仍属请求侧问题。
	return false
}

// extractHTTPStatus 从 "prefix status=NNN ..." 形态的错误串里提取 NNN。
func extractHTTPStatus(text string) (int, bool) {
	idx := strings.Index(text, "status=")
	if idx < 0 {
		return 0, false
	}
	rest := text[idx+len("status="):]
	// 读连续数字。
	end := 0
	for end < len(rest) {
		ch := rest[end]
		if ch < '0' || ch > '9' {
			break
		}
		end++
	}
	if end == 0 {
		return 0, false
	}
	status, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0, false
	}
	return status, true
}

// noopCircuitBreaker 是无 registry 时的占位熔断器，永远放行、不计计数。
type noopCircuitBreaker struct{}

var noopCircuitBreakerInstance = &noopCircuitBreaker{}

func (n *noopCircuitBreaker) IsAvailable() bool         { return true }
func (n *noopCircuitBreaker) AllowRequest() AllowResult { return AllowResult{Allowed: true} }
func (n *noopCircuitBreaker) RecordSuccess(bool)        {}
func (n *noopCircuitBreaker) RecordFailure(bool)        {}


// sanitizeProviderMessages removes replay-only placeholders and trims trailing
// assistant prefill so providers that require a user/tool terminal message do
// not reject the request.
func sanitizeProviderMessages(input []Message) []Message {
	if len(input) == 0 {
		return nil
	}

	filtered := make([]Message, 0, len(input))
	for _, message := range input {
		if isAssistantPlaceholderMessage(message) {
			continue
		}
		filtered = append(filtered, message)
	}
	filtered = mergeAdjacentAssistantToolCallMessages(filtered)
	filtered = trimDanglingAssistantToolCalls(filtered)
	for len(filtered) > 0 && isAssistantPrefillMessage(filtered[len(filtered)-1]) {
		filtered = filtered[:len(filtered)-1]
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func isAssistantPlaceholderMessage(message Message) bool {
	if strings.TrimSpace(message.Role) != "assistant" {
		return false
	}
	if len(message.ToolCalls) > 0 || len(message.ContentParts) > 0 {
		return false
	}
	if strings.TrimSpace(message.ToolCallID) != "" || strings.TrimSpace(message.Name) != "" {
		return false
	}
	if strings.TrimSpace(message.ReasoningContent) != "" {
		return false
	}
	if strings.TrimSpace(message.ReasoningSignature) != "" {
		return false
	}
	switch strings.TrimSpace(message.Content) {
	case "":
		return true
	default:
		return false
	}
}

func isAssistantPrefillMessage(message Message) bool {
	if strings.TrimSpace(message.Role) != "assistant" {
		return false
	}
	if len(message.ToolCalls) > 0 {
		return false
	}
	if strings.TrimSpace(message.ToolCallID) != "" || strings.TrimSpace(message.Name) != "" {
		return false
	}
	return strings.TrimSpace(message.Content) != "" || strings.TrimSpace(message.ReasoningContent) != ""
}

func mergeAdjacentAssistantToolCallMessages(input []Message) []Message {
	if len(input) == 0 {
		return nil
	}
	merged := make([]Message, 0, len(input))
	for _, raw := range input {
		message := cloneProviderMessage(raw)
		if mergeProviderAssistantToolCalls(&merged, message) {
			continue
		}
		merged = append(merged, message)
	}
	return merged
}

func cloneProviderMessage(message Message) Message {
	cloned := message
	if len(message.ContentParts) > 0 {
		cloned.ContentParts = append([]ContentPart(nil), message.ContentParts...)
	}
	if len(message.ToolCalls) > 0 {
		cloned.ToolCalls = append([]ToolCallDescriptor(nil), message.ToolCalls...)
	}
	if len(message.OpenAIResponsesReasoningSummary) > 0 {
		cloned.OpenAIResponsesReasoningSummary = append([]byte(nil), message.OpenAIResponsesReasoningSummary...)
	}
	return cloned
}

func mergeProviderAssistantToolCalls(messages *[]Message, message Message) bool {
	if len(*messages) == 0 {
		return false
	}
	last := &(*messages)[len(*messages)-1]
	if !canMergeProviderAssistantToolCalls(*last, message) {
		return false
	}
	startIndex := len(last.ToolCalls)
	for index, toolCall := range message.ToolCalls {
		item := toolCall
		item.Index = startIndex + index
		last.ToolCalls = append(last.ToolCalls, item)
	}
	last.ReasoningContent = mergeProviderReasoning(last.ReasoningContent, message.ReasoningContent)
	mergeProviderReasoningMetadata(last, message)
	return true
}

func canMergeProviderAssistantToolCalls(last Message, current Message) bool {
	if strings.TrimSpace(last.Role) != "assistant" || strings.TrimSpace(current.Role) != "assistant" {
		return false
	}
	if len(last.ToolCalls) == 0 || len(current.ToolCalls) == 0 {
		return false
	}
	if strings.TrimSpace(last.ToolCallID) != "" || strings.TrimSpace(last.Name) != "" {
		return false
	}
	if strings.TrimSpace(current.ToolCallID) != "" || strings.TrimSpace(current.Name) != "" {
		return false
	}
	if strings.TrimSpace(current.Content) != "" || len(current.ContentParts) > 0 {
		return false
	}
	return true
}

func mergeProviderReasoning(left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	switch {
	case left == "":
		return right
	case right == "", right == left:
		return left
	default:
		return left + "\n\n" + right
	}
}

func mergeProviderReasoningSignature(left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	switch {
	case left == "":
		return right
	case right == "", right == left:
		return left
	default:
		return ""
	}
}

func mergeProviderReasoningSignatureSource(left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	switch {
	case left == "":
		return right
	case right == "", right == left:
		return left
	default:
		return ""
	}
}

func mergeProviderReasoningMetadata(last *Message, current Message) {
	if last == nil {
		return
	}
	leftSignature := strings.TrimSpace(last.ReasoningSignature)
	rightSignature := strings.TrimSpace(current.ReasoningSignature)
	mergedSignature := mergeProviderReasoningSignature(leftSignature, rightSignature)
	last.ReasoningSignature = mergedSignature
	if mergedSignature == "" {
		last.ReasoningSignatureSource = ""
		last.OpenAIResponsesReasoningID = ""
		last.OpenAIResponsesReasoningStatus = ""
		last.OpenAIResponsesReasoningSummary = nil
		return
	}
	if leftSignature == "" && rightSignature != "" {
		last.ReasoningSignatureSource = strings.TrimSpace(current.ReasoningSignatureSource)
		last.OpenAIResponsesReasoningID = current.OpenAIResponsesReasoningID
		last.OpenAIResponsesReasoningStatus = current.OpenAIResponsesReasoningStatus
		last.OpenAIResponsesReasoningSummary = append([]byte(nil), current.OpenAIResponsesReasoningSummary...)
		return
	}
	if leftSignature == rightSignature {
		last.ReasoningSignatureSource = mergeProviderReasoningSignatureSource(last.ReasoningSignatureSource, current.ReasoningSignatureSource)
		if strings.TrimSpace(last.OpenAIResponsesReasoningID) == "" {
			last.OpenAIResponsesReasoningID = current.OpenAIResponsesReasoningID
		}
		if strings.TrimSpace(last.OpenAIResponsesReasoningStatus) == "" {
			last.OpenAIResponsesReasoningStatus = current.OpenAIResponsesReasoningStatus
		}
		if len(last.OpenAIResponsesReasoningSummary) == 0 {
			last.OpenAIResponsesReasoningSummary = append([]byte(nil), current.OpenAIResponsesReasoningSummary...)
		}
	}
}

func trimDanglingAssistantToolCalls(input []Message) []Message {
	if len(input) == 0 {
		return nil
	}
	trimmed := make([]Message, 0, len(input))
	for index := 0; index < len(input); index++ {
		message := cloneProviderMessage(input[index])
		if strings.TrimSpace(message.Role) != "assistant" || len(message.ToolCalls) == 0 {
			trimmed = append(trimmed, message)
			continue
		}

		end := index + 1
		responded := make(map[string]struct{}, len(message.ToolCalls))
		for end < len(input) && strings.TrimSpace(input[end].Role) == "tool" {
			toolCallID := strings.TrimSpace(input[end].ToolCallID)
			if toolCallID != "" {
				responded[toolCallID] = struct{}{}
			}
			end++
		}

		nextToolCalls := make([]ToolCallDescriptor, 0, len(message.ToolCalls))
		allowedToolCallIDs := make(map[string]struct{}, len(message.ToolCalls))
		for _, toolCall := range message.ToolCalls {
			toolCallID := strings.TrimSpace(toolCall.ID)
			if _, ok := responded[toolCallID]; !ok {
				continue
			}
			item := toolCall
			item.Index = len(nextToolCalls)
			nextToolCalls = append(nextToolCalls, item)
			allowedToolCallIDs[toolCallID] = struct{}{}
		}

		if len(nextToolCalls) > 0 {
			message.ToolCalls = nextToolCalls
			trimmed = append(trimmed, message)
			for toolIndex := index + 1; toolIndex < end; toolIndex++ {
				toolMessage := cloneProviderMessage(input[toolIndex])
				if _, ok := allowedToolCallIDs[strings.TrimSpace(toolMessage.ToolCallID)]; !ok {
					continue
				}
				trimmed = append(trimmed, toolMessage)
			}
		} else if strings.TrimSpace(message.Content) != "" || len(message.ContentParts) > 0 || strings.TrimSpace(message.ReasoningContent) != "" {
			message.ToolCalls = nil
			trimmed = append(trimmed, message)
		}

		index = end - 1
	}
	return trimmed
}

func normalizeRuntimeThinkingEffort(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "disabled", "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(raw))
	case "disable", "off", "none", "false", "no", "0":
		return "disabled"
	case "very_high", "very-high", "veryhigh", "x-high", "extra_high", "extra-high", "extrahigh":
		return "xhigh"
	case "maximum":
		return "max"
	default:
		return ""
	}
}

func openAIReasoningEffortFromRuntime(runtimeThinkingEffort string) string {
	switch normalizeRuntimeThinkingEffort(runtimeThinkingEffort) {
	case "low", "medium", "high", "xhigh", "max":
		return normalizeRuntimeThinkingEffort(runtimeThinkingEffort)
	default:
		return ""
	}
}
