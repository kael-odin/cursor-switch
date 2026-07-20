// openai_responses.go 实现 OpenAI /v1/responses 端点的消息与工具归一化。从 openai.go 拆出。
package modeladapter

import (
	"encoding/json"
	"fmt"
	"strings"
)

func normalizeOpenAIResponsesInput(messages []Message) (string, []map[string]any, error) {
	if len(messages) == 0 {
		return "", nil, nil
	}
	instructionParts := make([]string, 0, 2)
	items := make([]map[string]any, 0, len(messages))
	responsesCallIDs := make(map[string]string)
	activeAssistantReasoningKey := ""
	for _, message := range messages {
		role := strings.TrimSpace(message.Role)
		if role == "system" {
			if text := openAIResponsesMessageText(message); strings.TrimSpace(text) != "" {
				instructionParts = append(instructionParts, strings.TrimSpace(text))
			}
			activeAssistantReasoningKey = ""
			continue
		}
		if role == "tool" && strings.TrimSpace(message.ToolCallID) != "" {
			callID := openAIResponsesToolMessageCallID(message, responsesCallIDs)
			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  openAIResponsesMessageText(message),
			})
			activeAssistantReasoningKey = ""
			continue
		}
		if role != "assistant" {
			activeAssistantReasoningKey = ""
		}
		if shouldIncludeOpenAIResponsesReasoningItem(message) {
			reasoningKey := openAIResponsesReasoningReplayKey(message)
			if reasoningKey != activeAssistantReasoningKey {
				items = append(items, openAIResponsesReasoningItem(message))
				activeAssistantReasoningKey = reasoningKey
			}
		}
		if strings.TrimSpace(message.Content) != "" || len(message.ContentParts) > 0 {
			content, err := openAIResponsesMessageContent(message, role == "assistant")
			if err != nil {
				return "", nil, err
			}
			if len(content) > 0 {
				items = append(items, map[string]any{
					"role":    openAIResponsesMessageRole(role),
					"content": content,
				})
			}
		}
		if role == "assistant" && len(message.ToolCalls) > 0 {
			for _, toolCall := range message.ToolCalls {
				name := strings.TrimSpace(toolCall.Function.Name)
				if name == "" {
					continue
				}
				callID := openAIResponsesToolCallCallID(toolCall)
				if strings.TrimSpace(callID) == "" {
					callID = openAIResponsesProviderCallID(name)
				}
				if internalID := strings.TrimSpace(toolCall.ID); internalID != "" && strings.TrimSpace(callID) != "" {
					responsesCallIDs[internalID] = strings.TrimSpace(callID)
				}
				toolItem := map[string]any{
					"type":      "function_call",
					"call_id":   callID,
					"name":      name,
					"arguments": toolCall.Function.Arguments,
				}
				if itemID := strings.TrimSpace(toolCall.OpenAIResponsesID); itemID != "" {
					toolItem["id"] = itemID
				}
				if status := strings.TrimSpace(toolCall.OpenAIResponsesStatus); status != "" {
					toolItem["status"] = status
				} else {
					toolItem["status"] = "completed"
				}
				items = append(items, toolItem)
			}
		}
	}
	return strings.Join(instructionParts, "\n\n"), items, nil
}

func openAIResponsesReasoningReplayKey(message Message) string {
	return strings.Join([]string{
		strings.TrimSpace(message.ReasoningSignature),
		strings.TrimSpace(message.OpenAIResponsesReasoningID),
		strings.TrimSpace(message.OpenAIResponsesReasoningStatus),
		string(message.OpenAIResponsesReasoningSummary),
	}, "\x00")
}

func openAIResponsesReasoningItem(message Message) map[string]any {
	reasoningItem := map[string]any{
		"type":              "reasoning",
		"encrypted_content": strings.TrimSpace(message.ReasoningSignature),
	}
	if reasoningID := strings.TrimSpace(message.OpenAIResponsesReasoningID); reasoningID != "" {
		reasoningItem["id"] = reasoningID
	}
	if reasoningStatus := strings.TrimSpace(message.OpenAIResponsesReasoningStatus); reasoningStatus != "" {
		reasoningItem["status"] = reasoningStatus
	}
	if len(message.OpenAIResponsesReasoningSummary) > 0 {
		reasoningItem["summary"] = json.RawMessage(append([]byte(nil), message.OpenAIResponsesReasoningSummary...))
	} else {
		reasoningItem["summary"] = []any{}
	}
	return reasoningItem
}

func shouldIncludeOpenAIResponsesReasoningItem(message Message) bool {
	if strings.TrimSpace(message.Role) != "assistant" || strings.TrimSpace(message.ReasoningSignature) == "" {
		return false
	}
	return strings.TrimSpace(message.ReasoningSignatureSource) == ReasoningSignatureSourceOpenAIResponses
}

func openAIResponsesToolMessageCallID(message Message, responsesCallIDs map[string]string) string {
	internalID := strings.TrimSpace(message.ToolCallID)
	if internalID == "" {
		return ""
	}
	if callID := strings.TrimSpace(responsesCallIDs[internalID]); callID != "" {
		return callID
	}
	return openAIResponsesProviderCallID(internalID)
}

func openAIResponsesToolCallCallID(toolCall ToolCallDescriptor) string {
	if callID := strings.TrimSpace(toolCall.OpenAIResponsesCallID); callID != "" {
		return callID
	}
	return openAIResponsesProviderCallID(toolCall.ID)
}

func openAIResponsesProviderCallID(toolCallID string) string {
	trimmed := strings.TrimSpace(toolCallID)
	if trimmed == "" {
		return ""
	}
	if _, raw, ok := splitLegacyToolCallID(trimmed); ok {
		return raw
	}
	if strings.HasPrefix(trimmed, "tc_") {
		parts := strings.SplitN(trimmed, "_", 3)
		if len(parts) == 3 && strings.TrimSpace(parts[2]) != "" {
			return strings.TrimSpace(parts[2])
		}
	}
	return providerToolCallID(trimmed)
}

func openAIResponsesMessageRole(role string) string {
	switch strings.TrimSpace(role) {
	case "assistant":
		return "assistant"
	default:
		return "user"
	}
}

func openAIResponsesMessageText(message Message) string {
	if strings.TrimSpace(message.Content) != "" {
		return message.Content
	}
	if len(message.ContentParts) > 0 {
		return collapseTextContentParts(message.ContentParts)
	}
	return ""
}

func openAIResponsesMessageContent(message Message, assistant bool) ([]map[string]any, error) {
	textType := "input_text"
	if assistant {
		textType = "output_text"
	}
	if !hasImageContentParts(message.ContentParts) {
		text := openAIResponsesMessageText(message)
		if text == "" {
			return nil, nil
		}
		return []map[string]any{{
			"type": textType,
			"text": text,
		}}, nil
	}
	parts := make([]map[string]any, 0, len(message.ContentParts)+1)
	if len(message.ContentParts) == 0 && strings.TrimSpace(message.Content) != "" {
		parts = append(parts, map[string]any{
			"type": textType,
			"text": message.Content,
		})
	}
	for _, part := range message.ContentParts {
		switch normalizeContentPartType(part.Type) {
		case contentPartTypeText:
			if part.Text == "" {
				continue
			}
			parts = append(parts, map[string]any{
				"type": textType,
				"text": part.Text,
			})
		case contentPartTypeImage:
			dataURL, err := imageContentDataURL(part.Image)
			if err != nil {
				return nil, err
			}
			parts = append(parts, map[string]any{
				"type":      "input_image",
				"image_url": dataURL,
			})
		default:
			return nil, fmt.Errorf("unsupported openai responses content part type: %s", strings.TrimSpace(part.Type))
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return parts, nil
}

func normalizeOpenAIResponsesTools(items []json.RawMessage) ([]map[string]any, error) {
	if len(items) == 0 {
		return nil, nil
	}
	tools := make([]map[string]any, 0, len(items))
	for _, item := range items {
		var raw map[string]any
		if err := json.Unmarshal(item, &raw); err != nil {
			return nil, fmt.Errorf("decode openai responses tool descriptor failed: %w", err)
		}
		source := raw
		if functionShape, ok := raw["function"].(map[string]any); ok {
			source = functionShape
		}
		name := strings.TrimSpace(asStringMapValue(source, "name"))
		if name == "" {
			return nil, fmt.Errorf("openai responses tool descriptor name is required")
		}
		tool := map[string]any{
			"type": "function",
			"name": name,
		}
		if description := strings.TrimSpace(asStringMapValue(source, "description")); description != "" {
			tool["description"] = description
		}
		if parameters, ok := source["parameters"]; ok && parameters != nil {
			tool["parameters"] = parameters
		} else {
			tool["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		if strict, ok := source["strict"]; ok {
			tool["strict"] = strict
		} else if strict, ok := raw["strict"]; ok {
			tool["strict"] = strict
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

func asStringMapValue(source map[string]any, key string) string {
	if len(source) == 0 {
		return ""
	}
	switch value := source[key].(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	default:
		return ""
	}
}

func openAIStreamErrorDetails(errorType string, code string, requestID string) string {
	parts := make([]string, 0, 3)
	if value := strings.TrimSpace(errorType); value != "" {
		parts = append(parts, "type="+value)
	}
	if value := strings.TrimSpace(code); value != "" {
		parts = append(parts, "code="+value)
	}
	if value := strings.TrimSpace(requestID); value != "" {
		parts = append(parts, "request_id="+value)
	}
	if len(parts) == 0 {
		return "provider_error"
	}
	return strings.Join(parts, " ")
}
