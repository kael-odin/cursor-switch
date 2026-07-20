// openai_messages.go 实现 OpenAI provider 消息归一化与 thinking disable。从 openai.go 拆出。
package modeladapter

import (
	"strconv"
	"strings"

	"cursor/internal/modelchannel"
)


func normalizeOpenAIProviderMessages(messages []Message, thinkingEnabled bool) ([]map[string]any, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	items := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		content, err := openAIContentValue(message)
		if err != nil {
			return nil, err
		}
		item := map[string]any{
			"role":    strings.TrimSpace(message.Role),
			"content": content,
		}
		// 开启 thinking 时，tool_calls 对应的 assistant message 也要显式携带空 reasoning_content。
		if shouldIncludeOpenAIReasoningContent(message, thinkingEnabled) {
			item["reasoning_content"] = message.ReasoningContent
		}
		if len(message.ToolCalls) > 0 {
			item["tool_calls"] = normalizeToolCallDescriptors(message.ToolCalls)
		}
		if strings.TrimSpace(message.ToolCallID) != "" {
			item["tool_call_id"] = providerToolCallID(message.ToolCallID)
		}
		if strings.TrimSpace(message.Name) != "" {
			item["name"] = strings.TrimSpace(message.Name)
		}
		items = append(items, item)
	}
	return items, nil
}

func shouldSendOpenAIMaxOutputTokens(modelID string) bool {
	return !strings.Contains(strings.ToLower(strings.TrimSpace(modelID)), "gpt")
}

func shouldIncludeOpenAIReasoningContent(message Message, thinkingEnabled bool) bool {
	if strings.TrimSpace(message.ReasoningContent) != "" {
		return true
	}
	if !thinkingEnabled {
		return false
	}
	if strings.TrimSpace(message.Role) != "assistant" {
		return false
	}
	return len(message.ToolCalls) > 0
}

func applyOpenAIThinkingDisable(body map[string]any, req StreamRequest, baseURL string, modelID string, endpoint string) {
	if len(body) == 0 || normalizeRuntimeThinkingEffort(req.ThinkingEffort) != "disabled" {
		return
	}
	switch openAIThinkingDisableKind(baseURL, modelID, endpoint) {
	case "thinking_type":
		body["thinking"] = map[string]any{"type": "disabled"}
		delete(body, "reasoning_effort")
		setRequestKnob(req, "thinking_disabled_provider_param", "thinking.type")
	case "enable_thinking":
		body["enable_thinking"] = false
		delete(body, "reasoning_effort")
		setRequestKnob(req, "thinking_disabled_provider_param", "enable_thinking")
	case "reasoning_none":
		if modelchannel.OpenAIEndpointShape(endpoint) == "responses" {
			body["reasoning"] = map[string]any{"effort": "none"}
		} else {
			body["reasoning_effort"] = "none"
		}
		setRequestKnob(req, "thinking_disabled_provider_param", "reasoning.effort")
	}
}

func openAIThinkingDisableKind(baseURL string, modelID string, endpoint string) string {
	base := strings.ToLower(strings.TrimSpace(baseURL))
	model := strings.ToLower(strings.TrimSpace(modelID))
	switch {
	case strings.Contains(base, "dashscope") ||
		strings.Contains(base, "qwen") ||
		strings.Contains(base, "aliyun") ||
		strings.Contains(model, "qwen"):
		return "enable_thinking"
	case strings.Contains(base, "deepseek") ||
		strings.Contains(base, "bigmodel") ||
		strings.Contains(base, "z.ai") ||
		strings.Contains(base, "zhipu") ||
		strings.Contains(base, "xiaomimimo") ||
		strings.Contains(base, "mimo") ||
		strings.Contains(model, "deepseek") ||
		strings.Contains(model, "glm") ||
		strings.Contains(model, "zai") ||
		strings.Contains(model, "zhipu") ||
		strings.Contains(model, "mimo"):
		return "thinking_type"
	case openAIModelSupportsReasoningNone(model):
		return "reasoning_none"
	default:
		return ""
	}
}

func openAIModelSupportsReasoningNone(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(model, "gpt-6") {
		return true
	}
	if strings.Contains(model, "gpt-5.1") {
		return true
	}
	if !strings.HasPrefix(model, "gpt-5.") {
		return false
	}
	minorText := strings.TrimPrefix(model, "gpt-5.")
	minorEnd := 0
	for minorEnd < len(minorText) && minorText[minorEnd] >= '0' && minorText[minorEnd] <= '9' {
		minorEnd++
	}
	if minorEnd == 0 {
		return false
	}
	minor, err := strconv.Atoi(minorText[:minorEnd])
	return err == nil && minor >= 1
}

func setRequestKnob(req StreamRequest, key string, value any) {
	if req.RequestKnobs == nil {
		return
	}
	req.RequestKnobs[key] = value
}

