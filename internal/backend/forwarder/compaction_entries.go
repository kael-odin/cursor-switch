// compaction_entries.go 实现 compaction 落盘的 entry 构造器与 current-turn summarizer。从 compaction.go 拆出。
package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"

	"cursor/gen/agentv1"
	"google.golang.org/protobuf/encoding/protojson"
)

func newCompactionSummaryEntry(plan *PendingCompaction, summaryText string) HistoryEntry {
	payload, _ := json.Marshal(compactionSummaryEntryPayload{
		Summary:                   strings.TrimSpace(summaryText),
		Trigger:                   strings.TrimSpace(plan.Trigger),
		CurrentTurnSeq:            plan.CurrentTurnSeq,
		CurrentRequestID:          strings.TrimSpace(plan.CurrentRequestID),
		CompactTurnCount:          plan.CompactTurnCount,
		MessagesToCompact:         plan.MessagesToCompact,
		PreserveCurrentTurnInputs: plan.PreserveCurrentTurnInputs,
	})
	return HistoryEntry{
		TurnSeq:   plan.CurrentTurnSeq,
		RequestID: strings.TrimSpace(plan.CurrentRequestID),
		Role:      "system",
		Kind:      "compacted_summary",
		Payload:   payload,
	}
}

func newCompactedRuntimeStateEntry(conversation *ConversationFile, plan *PendingCompaction) (HistoryEntry, bool, error) {
	state, err := projectConversationStructuredState(conversation)
	if err != nil {
		return HistoryEntry{}, false, err
	}
	payload := runtimeStateEntryPayload{
		PlanText: state.PlanText,
		Plans:    clonePlanRegistryEntries(state.Plans),
		Todos:    cloneTodoItems(state.Todos),
	}
	if strings.TrimSpace(payload.PlanText) == "" && len(payload.Plans) == 0 && len(payload.Todos) == 0 {
		return HistoryEntry{}, false, nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return HistoryEntry{}, false, fmt.Errorf("encode compacted runtime state: %w", err)
	}
	return HistoryEntry{
		TurnSeq:   plan.CurrentTurnSeq,
		RequestID: strings.TrimSpace(plan.CurrentRequestID),
		Role:      "system",
		Kind:      "runtime_state",
		Payload:   encoded,
	}, true, nil
}

func newCompactionRequestEntry(plan *PendingCompaction) HistoryEntry {
	payload, _ := json.Marshal(map[string]any{
		"trigger":               strings.TrimSpace(plan.Trigger),
		"context_tokens":        plan.ContextTokens,
		"context_window_size":   plan.ContextWindowSize,
		"reserve_tokens":        plan.ReserveTokens,
		"messages_to_compact":   plan.MessagesToCompact,
		"compact_turn_count":    plan.CompactTurnCount,
		"request_source":        strings.TrimSpace(plan.RequestSource),
		"summary_model_call_id": strings.TrimSpace(plan.SummaryModelCallID),
	})
	return HistoryEntry{
		TurnSeq:   plan.CurrentTurnSeq,
		RequestID: strings.TrimSpace(plan.CurrentRequestID),
		Role:      "system",
		Kind:      "compaction_request",
		Payload:   payload,
	}
}

func newCompactionFailedEntry(plan *PendingCompaction, cause error) HistoryEntry {
	payload := map[string]any{
		"error": "compaction failed",
	}
	entry := HistoryEntry{
		Role: "system",
		Kind: "compaction_failed",
	}
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		payload["error"] = strings.TrimSpace(cause.Error())
	}
	if plan != nil {
		payload["trigger"] = strings.TrimSpace(plan.Trigger)
		payload["request_source"] = strings.TrimSpace(plan.RequestSource)
		payload["summary_model_call_id"] = strings.TrimSpace(plan.SummaryModelCallID)
		entry.TurnSeq = plan.CurrentTurnSeq
		entry.RequestID = strings.TrimSpace(plan.CurrentRequestID)
	}
	entry.Payload, _ = json.Marshal(payload)
	return entry
}

func buildFallbackCompactionSummary(plan *PendingCompaction) string {
	sections := []string{
		"Conversation summary",
		"Earlier context was compacted into this summary. Preserve the facts, decisions, tool results, and user intent below when continuing the conversation.",
	}
	if plan == nil {
		return strings.Join(sections, "\n\n")
	}
	if strings.TrimSpace(plan.ExistingSummary) != "" {
		sections = append(sections, "Previous summary:\n"+truncateCompactionText(plan.ExistingSummary, compactionSummaryMaxChars/4))
	}
	lines := make([]string, 0, len(plan.CompactedTurns))
	for index, item := range plan.CompactedTurns {
		parts := make([]string, 0, len(item.Steps)+1)
		if strings.TrimSpace(item.UserText) != "" {
			parts = append(parts, "user="+truncateCompactionText(item.UserText, 400))
		}
		for _, step := range item.Steps {
			if strings.TrimSpace(step) == "" {
				continue
			}
			parts = append(parts, truncateCompactionText(step, 400))
		}
		if len(parts) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, strings.Join(parts, " | ")))
	}
	if len(lines) > 0 {
		sections = append(sections, "Compacted turns:\n"+truncateCompactionText(strings.Join(lines, "\n"), compactionSummaryMaxChars/2))
	}
	if strings.TrimSpace(plan.HookMessage) != "" {
		sections = append(sections, "Compaction note:\n"+truncateCompactionText(plan.HookMessage, 800))
	}
	if strings.TrimSpace(plan.ManualInstruction) != "" {
		sections = append(sections, "Manual compact instruction:\n"+truncateCompactionText(plan.ManualInstruction, 800))
	}
	return strings.TrimSpace(truncateCompactionText(strings.Join(sections, "\n\n"), compactionSummaryMaxChars))
}

func currentTurnUserText(entry HistoryEntry) string {
	userMessage := &agentv1.UserMessage{}
	if err := protojson.Unmarshal(entry.Payload, userMessage); err != nil {
		return ""
	}
	return truncateCompactionText(userMessage.GetText(), compactionTurnSnippetMaxChars/3)
}

func summarizeCurrentTurnAssistantEntry(entry HistoryEntry) string {
	var payload assistantTextPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return ""
	}
	if text := truncateCompactionText(payload.Text, compactionTurnSnippetMaxChars/3); text != "" {
		return "assistant=" + text
	}
	if text := truncateCompactionText(payload.ReasoningContent, compactionTurnSnippetMaxChars/4); text != "" {
		return "thinking=" + text
	}
	return ""
}

func summarizeCurrentTurnToolCallEntry(entry HistoryEntry) string {
	var payload toolCallEntryPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return ""
	}
	toolName := strings.TrimSpace(payload.ToolName)
	if toolName == "" {
		toolName = "tool_call"
	}
	return toolName + "=called"
}

func summarizeCurrentTurnToolResultEntry(entry HistoryEntry) string {
	var payload toolResultEntryPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return ""
	}
	if len(payload.ToolCall) > 0 {
		toolCall := &agentv1.ToolCall{}
		if err := protojson.Unmarshal(payload.ToolCall, toolCall); err == nil {
			if toolName, detail := summarizeCompactedToolCall(toolCall); toolName != "" {
				return toolName + "=" + detail
			}
		}
	}
	toolName := strings.TrimSpace(payload.ToolName)
	if toolName == "" {
		toolName = "tool_result"
	}
	if result := truncateCompactionText(payload.ResultText, compactionTurnSnippetMaxChars/3); result != "" {
		return toolName + "=" + result
	}
	return toolName + "=completed"
}
