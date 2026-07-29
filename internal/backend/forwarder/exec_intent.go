// exec_intent.go 实现 exec 结果/控制 intent 的处理与流关闭恢复。从 service.go 拆出。
package forwarder

import (
	"fmt"
	"log"
	"strings"
	"time"

	runtimecore "cursor/internal/backend/agent/core"
)

// handleExecResult 处理客户端返回的执行桥结果，并在终态时把 tool_result 写回 history。
func (service *Service) handleExecResult(intent InboundIntent) error {
	stream, ok := service.broker.Get(intent.RequestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", intent.RequestID)
	}
	if intent.ExecClientMessage == nil {
		return fmt.Errorf("exec client message is required")
	}
	pending, found := selectPendingExec(intent.ExecClientMessage.GetExecId(), intent.ExecClientMessage.GetId(), stream)
	if !found {
		if service.observeMissingBackgroundShellExecClientMessage(stream, intent.ExecClientMessage) {
			return nil
		}
		if service.observeMissingShellExecClientMessage(stream, intent.ExecClientMessage) {
			return nil
		}
		if shouldIgnoreMissingExecResult(intent.ExecClientMessage, stream) {
			return nil
		}
		return fmt.Errorf("pending exec not found")
	}
	service.observeBackgroundShellExecClientMessage(stream, pending, intent.ExecClientMessage)
	service.observeShellExecClientMessage(stream, pending, intent.ExecClientMessage)
	pending = service.applyExecProgress(stream, pending, intent.ExecClientMessage)
	if isHiddenPatchEditExecKind(pending.ExecKind) {
		return service.handleHiddenPatchEditExecResult(stream, pending, intent.ExecClientMessage)
	}
	if isHiddenWriteExecKind(pending.ExecKind) {
		return service.handleHiddenWriteExecResult(stream, pending, intent.ExecClientMessage)
	}
	result, err := service.execBridge.ApplyExecClientMessage(intent.ExecClientMessage, pending)
	if err != nil {
		return err
	}
	if result.ShellOutputDelta != nil {
		if err := service.broker.Publish(intent.RequestID, StreamEvent{
			Message: buildShellOutputDeltaMessage(result.ShellOutputDelta),
		}); err != nil {
			return err
		}
	}
	if !result.IsTerminal {
		return nil
	}
	markExecCompleted(stream, pending)
	backgroundShellToolCallID := ""
	if strings.TrimSpace(pending.ExecKind) == "shell" && shellToolCallIsBackgrounded(result.ToolCall) {
		backgroundShellToolCallID = firstNonEmpty(strings.TrimSpace(result.ToolCallID), strings.TrimSpace(pending.ToolCallID))
	}
	if strings.TrimSpace(pending.ExecKind) == "execute_hook_pre_compact" {
		return service.handlePreCompactTerminal(stream, pending.ProviderPass, strings.TrimSpace(result.ToolResultPayload))
	}
	if result.ToolCall != nil {
		if err := service.appendToolResult(stream, result.ToolCallID, deriveToolNameFromPendingExec(pending), pending.ArgsJSON, result.ToolResultPayload, pending.ReasoningContent, result.ToolCall); err != nil {
			return err
		}
	} else if strings.TrimSpace(result.ToolResultPayload) != "" {
		if err := service.appendToolResult(stream, pending.ToolCallID, deriveToolNameFromPendingExec(pending), pending.ArgsJSON, result.ToolResultPayload, pending.ReasoningContent, nil); err != nil {
			return err
		}
	}
	if backgroundShellToolCallID != "" {
		if recordedToolCallID, recorded := recordBackgroundShellActionMemory(stream, backgroundShellToolCallID, time.Now().UTC()); recorded {
			if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
				newBackgroundShellActionMetadataEntry(stream.TurnSeq, stream.RequestID, recordedToolCallID, backgroundShellActionSourceLocalBackgrounded),
			}); err != nil {
				return err
			}
		}
	}
	if err := service.publishToolCallCompleted(intent.RequestID, result.ToolCallID, pending.ModelCallID, result.ToolCall); err != nil {
		return err
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, intent.RequestID, pending.ModelCallID); err != nil {
		return err
	}
	if err := service.publishCheckpoint(intent.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}

// handleExecControl 处理执行桥控制面结果，例如 stream_close 或 throw。
func (service *Service) handleExecControl(intent InboundIntent) error {
	stream, ok := service.broker.Get(intent.RequestID)
	if !ok || stream == nil {
		if shouldIgnoreStaleExecControl(intent.ExecClientControlMessage) {
			return nil
		}
		return fmt.Errorf("request is not active: %s", intent.RequestID)
	}
	if intent.ExecClientControlMessage == nil {
		return fmt.Errorf("exec client control message is required")
	}
	pending, found := selectPendingExecByControl(intent.ExecClientControlMessage, stream)
	if !found {
		if shouldIgnoreMissingExecControl(intent.ExecClientControlMessage, stream, intent.RequestID) {
			return nil
		}
		return fmt.Errorf("pending exec not found for control message")
	}
	pending = service.applyExecControlProgress(stream, pending, intent.ExecClientControlMessage)
	if isHiddenPatchEditExecKind(pending.ExecKind) {
		return service.handleHiddenPatchEditExecControl(stream, pending, intent.ExecClientControlMessage)
	}
	if isHiddenWriteExecKind(pending.ExecKind) {
		return service.handleHiddenWriteExecControl(stream, pending, intent.ExecClientControlMessage)
	}
	result, err := service.execBridge.ApplyExecClientControl(intent.ExecClientControlMessage, pending)
	if err != nil {
		return err
	}
	if !result.IsTerminal {
		if shouldRecoverNonStreamingExecOnStreamClose(intent.ExecClientControlMessage, pending) {
			markExecTransportClosed(stream, pending)
			service.scheduleNonStreamingExecRecovery(intent.RequestID, pending)
			return nil
		}
		if shouldObserveShellStreamClose(intent.ExecClientControlMessage, pending) {
			service.observeShellStreamClose(stream, pending)
		}
		return nil
	}
	markExecCompleted(stream, pending)
	if strings.TrimSpace(pending.ExecKind) == "execute_hook_pre_compact" {
		return service.handlePreCompactTerminal(stream, pending.ProviderPass, "")
	}
	if strings.TrimSpace(result.ToolResultPayload) != "" {
		if err := service.appendToolResult(stream, pending.ToolCallID, deriveToolNameFromPendingExec(pending), pending.ArgsJSON, result.ToolResultPayload, pending.ReasoningContent, nil); err != nil {
			return err
		}
		_, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
			newMetadataEntry(stream.TurnSeq, stream.RequestID, "tool_control", map[string]any{
				"tool_call_id": result.ToolCallID,
				"payload":      result.ToolResultPayload,
			}),
		})
		if err != nil {
			return err
		}
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, intent.RequestID, pending.ModelCallID); err != nil {
		return err
	}
	if err := service.publishToolCallCompleted(intent.RequestID, result.ToolCallID, pending.ModelCallID, nil); err != nil {
		return err
	}
	if err := service.publishCheckpoint(intent.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}


func (service *Service) scheduleNonStreamingExecRecovery(requestID string, pending runtimecore.PendingExec) {
	if service == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(pending.ExecID) == "" {
		return
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return
	}
	service.scheduleStreamTimer(
		stream,
		providerTimerKey(streamTimerNonStreamingRecovery, pending.ExecID),
		nonStreamingExecCloseGrace,
		streamTimerNonStreamingRecovery,
		pending.ExecID,
		pending.MessageID,
		"",
	)
}

func (service *Service) recoverNonStreamingExecAfterStreamClose(stream *ActiveStream, pending runtimecore.PendingExec) error {
	if stream == nil {
		return nil
	}
	markExecCompleted(stream, pending)
	toolName := strings.TrimSpace(deriveToolNameFromPendingExec(pending))
	resultPayload := fmt.Sprintf("%s transport closed before terminal result arrived", firstNonEmpty(toolName, pending.ExecKind, "tool"))
	log.Printf("forwarder synthetic exec recovery request_id=%s tool_call_id=%s message_id=%d exec_id=%s exec_kind=%s", strings.TrimSpace(stream.RequestID), strings.TrimSpace(pending.ToolCallID), pending.MessageID, strings.TrimSpace(pending.ExecID), strings.TrimSpace(pending.ExecKind))
	if toolName != "" {
		if err := service.appendToolResult(stream, pending.ToolCallID, toolName, pending.ArgsJSON, resultPayload, pending.ReasoningContent, nil); err != nil {
			return err
		}
	}
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "tool_transport_closed", map[string]any{
			"tool_call_id": pending.ToolCallID,
			"message_id":   pending.MessageID,
			"exec_id":      pending.ExecID,
			"exec_kind":    pending.ExecKind,
			"payload":      resultPayload,
		}),
	}); err != nil {
		return err
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, stream.RequestID, pending.ModelCallID); err != nil {
		return err
	}
	if err := service.publishToolCallCompleted(stream.RequestID, pending.ToolCallID, pending.ModelCallID, nil); err != nil {
		return err
	}
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}

func (service *Service) observeShellStreamClose(stream *ActiveStream, pending runtimecore.PendingExec) {
	if service == nil || stream == nil {
		return
	}
	current, ok := snapshotPendingExec(stream, pending.ExecID)
	if !ok {
		return
	}
	recentState := strings.TrimSpace(current.StreamState)
	if recentState == "transport_closed" || recentState == "exited" || recentState == "backgrounded" || recentState == "rejected" || recentState == "permission_denied" {
		return
	}
	log.Printf(
		"forwarder shell stream closed without terminal event request_id=%s tool_call_id=%s message_id=%d exec_id=%s stream_state=%s chunk_count=%d",
		strings.TrimSpace(stream.RequestID),
		strings.TrimSpace(current.ToolCallID),
		current.MessageID,
		strings.TrimSpace(current.ExecID),
		recentState,
		current.ChunkCount,
	)
	markExecTransportClosed(stream, current)
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "shell_stream_transport_closed", map[string]any{
			"tool_call_id":        current.ToolCallID,
			"message_id":          current.MessageID,
			"exec_id":             current.ExecID,
			"exec_kind":           current.ExecKind,
			"recent_stream_state": recentState,
			"chunk_count":         current.ChunkCount,
			"first_chunk_at":      current.FirstChunkAt,
			"reasoning_present":   strings.TrimSpace(current.ReasoningContent) != "",
			"stdout_buffer_bytes": len(current.StdoutBuffer),
			"stderr_buffer_bytes": len(current.StderrBuffer),
		}),
	}); err != nil {
		log.Printf("forwarder shell stream close metadata failed request_id=%s tool_call_id=%s err=%v", strings.TrimSpace(stream.RequestID), strings.TrimSpace(current.ToolCallID), err)
	}
	service.scheduleShellTransportCloseRecovery(stream.RequestID, current)
}
