// history_entries.go 实现 forwarder 写入 history 所用的 entry 构造器：
// run/mode/assistant_text/tool_call/tool_result/metadata 等。从 service.go 拆出以便独立阅读。
package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"

	"cursor/gen/agentv1"
	"cursor/internal/backend/agent/model"

	"google.golang.org/protobuf/encoding/protojson"
)

func buildRunEntries(intent InboundIntent, effectiveMode agentv1.AgentMode, turnSeq int64) ([]HistoryEntry, error) {
	entries := make([]HistoryEntry, 0, 4)
	if intent.RequestContext != nil {
		normalized := normalizeRequestContextForStorageMode(intent.RequestContext, turnSeq == 1)
		if normalized != nil {
			payload, err := protojson.Marshal(normalized)
			if err != nil {
				return nil, err
			}
			entries = append(entries, HistoryEntry{
				TurnSeq:   turnSeq,
				RequestID: intent.RequestID,
				Role:      "user",
				Kind:      "request_context",
				Payload:   payload,
			})
		}
	}
	if intent.UserMessage != nil {
		payload, err := protojson.Marshal(normalizeUserMessageForStorage(intent.UserMessage))
		if err != nil {
			return nil, err
		}
		entries = append(entries, HistoryEntry{
			TurnSeq:   turnSeq,
			RequestID: intent.RequestID,
			Role:      "user",
			Kind:      "user_message",
			Payload:   payload,
		})
	}
	modeEntry, err := newModeMetadataEntry(turnSeq, intent.RequestID, effectiveMode, intent.HasExplicitMode, intent.ModeSource)
	if err != nil {
		return nil, err
	}
	entries = append(entries,
		modeEntry,
		newMetadataEntry(turnSeq, intent.RequestID, "run_request", buildRunRequestMetadata(intent)),
	)
	if intent.HasExplicitMode {
		entries = append(entries, newModeChangePromptContextEntry(turnSeq, intent.RequestID, effectiveMode))
	}
	return entries, nil
}

func buildRunRequestMetadata(intent InboundIntent) map[string]any {
	return map[string]any{
		"model_id":   intent.ModelID,
		"model_name": intent.ModelName,
		"prewarm":    intent.Prewarm,
	}
}

func newModeMetadataEntry(turnSeq int64, requestID string, mode agentv1.AgentMode, explicit bool, source ModeSource) (HistoryEntry, error) {
	modeAliasValue, err := modeAlias(mode)
	if err != nil {
		return HistoryEntry{}, err
	}
	payload := map[string]any{
		"mode": modeAliasValue,
	}
	if explicit {
		payload["explicit"] = true
	}
	if strings.TrimSpace(string(source)) != "" {
		payload["source"] = strings.TrimSpace(string(source))
	}
	return newMetadataEntry(turnSeq, requestID, "mode", payload), nil
}

func newModeChangePromptContextEntry(turnSeq int64, requestID string, mode agentv1.AgentMode) HistoryEntry {
	modeAliasValue, err := modeAlias(mode)
	if err != nil {
		modeAliasValue = "agent"
	}
	return newPromptContextEntry(turnSeq, requestID, newPromptContextMessage(
		"mode_change",
		modeladapter.Message{
			Role:    "user",
			Content: wrapSystemReminder(fmt.Sprintf("At this point, the active mode changed to %s; follow later mode reminders if present.", modeAliasValue)),
		},
		true,
	))
}

// newAssistantTextEntry 构造 assistant 文本 entry。
func newAssistantTextEntry(turnSeq int64, requestID string, text string, reasoningContent string, reasoningSignature string) HistoryEntry {
	return newAssistantTextEntryWithProviderMetadata(turnSeq, requestID, text, reasoningContent, reasoningSignature, "", "", "", nil)
}

func newAssistantTextEntryWithProviderMetadata(turnSeq int64, requestID string, text string, reasoningContent string, reasoningSignature string, reasoningSignatureSource string, reasoningItemID string, reasoningStatus string, reasoningSummary json.RawMessage) HistoryEntry {
	payload, _ := json.Marshal(assistantTextPayload{
		Text:                     text,
		ReasoningContent:         reasoningContent,
		ReasoningSignature:       strings.TrimSpace(reasoningSignature),
		ReasoningSignatureSource: strings.TrimSpace(reasoningSignatureSource),
		ReasoningItemID:          strings.TrimSpace(reasoningItemID),
		ReasoningStatus:          strings.TrimSpace(reasoningStatus),
		ReasoningSummary:         append(json.RawMessage(nil), reasoningSummary...),
	})
	return HistoryEntry{
		TurnSeq:   turnSeq,
		RequestID: strings.TrimSpace(requestID),
		Role:      "assistant",
		Kind:      "assistant_text",
		Payload:   payload,
	}
}

// newToolCallEntry 构造 tool_call entry。
func newToolCallEntry(turnSeq int64, requestID string, toolCallID string, toolName string, reasoningContent string, reasoningSignature string, toolCall json.RawMessage) HistoryEntry {
	return newToolCallEntryWithProviderMetadata(turnSeq, requestID, toolCallID, toolName, reasoningContent, reasoningSignature, "", "", "", nil, "", "", "", toolCall)
}

func newToolCallEntryWithProviderMetadata(turnSeq int64, requestID string, toolCallID string, toolName string, reasoningContent string, reasoningSignature string, reasoningSignatureSource string, reasoningItemID string, reasoningStatus string, reasoningSummary json.RawMessage, providerItemID string, providerCallID string, providerStatus string, toolCall json.RawMessage) HistoryEntry {
	payload, _ := json.Marshal(toolCallEntryPayload{
		ToolCallID:               strings.TrimSpace(toolCallID),
		ToolName:                 strings.TrimSpace(toolName),
		ReasoningContent:         reasoningContent,
		ReasoningSignature:       strings.TrimSpace(reasoningSignature),
		ReasoningSignatureSource: strings.TrimSpace(reasoningSignatureSource),
		ReasoningItemID:          strings.TrimSpace(reasoningItemID),
		ReasoningStatus:          strings.TrimSpace(reasoningStatus),
		ReasoningSummary:         append(json.RawMessage(nil), reasoningSummary...),
		ProviderItemID:           strings.TrimSpace(providerItemID),
		ProviderCallID:           strings.TrimSpace(providerCallID),
		ProviderStatus:           strings.TrimSpace(providerStatus),
		ToolCall:                 append(json.RawMessage(nil), toolCall...),
	})
	return HistoryEntry{
		TurnSeq:    turnSeq,
		RequestID:  strings.TrimSpace(requestID),
		Role:       "assistant",
		Kind:       "tool_call",
		ToolCallID: strings.TrimSpace(toolCallID),
		Payload:    payload,
	}
}

// newToolResultEntry 构造 tool_result entry。
func newToolResultEntry(turnSeq int64, requestID string, toolCallID string, toolName string, arguments string, resultText string, reasoningContent string, toolCall json.RawMessage) HistoryEntry {
	payload, _ := json.Marshal(toolResultEntryPayload{
		ToolCallID:       strings.TrimSpace(toolCallID),
		ToolName:         strings.TrimSpace(toolName),
		Arguments:        strings.TrimSpace(arguments),
		ResultText:       strings.TrimSpace(resultText),
		ReasoningContent: strings.TrimSpace(reasoningContent),
		ToolCall:         append(json.RawMessage(nil), toolCall...),
	})
	return HistoryEntry{
		TurnSeq:    turnSeq,
		RequestID:  strings.TrimSpace(requestID),
		Role:       "tool",
		Kind:       "tool_result",
		ToolCallID: strings.TrimSpace(toolCallID),
		Payload:    payload,
	}
}

// newMetadataEntry 构造 metadata entry。
func newMetadataEntry(turnSeq int64, requestID string, eventType string, values map[string]any) HistoryEntry {
	payload, _ := json.Marshal(metadataPayload{
		Type:  strings.TrimSpace(eventType),
		Value: values,
	})
	return HistoryEntry{
		TurnSeq:   turnSeq,
		RequestID: strings.TrimSpace(requestID),
		Role:      "system",
		Kind:      "metadata",
		Payload:   payload,
	}
}
