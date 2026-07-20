// exec_recovery.go 实现 exec 流关闭后的恢复与快照辅助。从 service.go 拆出。
package forwarder

import (
	"strings"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func shouldRecoverNonStreamingExecOnStreamClose(message *agentv1.ExecClientControlMessage, pending runtimecore.PendingExec) bool {
	if message == nil || isStreamingPendingExecKind(pending.ExecKind) {
		return false
	}
	switch message.GetMessage().(type) {
	case *agentv1.ExecClientControlMessage_StreamClose:
		return true
	default:
		return false
	}
}

func shouldObserveShellStreamClose(message *agentv1.ExecClientControlMessage, pending runtimecore.PendingExec) bool {
	if message == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return false
	}
	switch message.GetMessage().(type) {
	case *agentv1.ExecClientControlMessage_StreamClose:
		return true
	default:
		return false
	}
}

func isStreamingPendingExecKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "shell":
		return true
	default:
		return false
	}
}

func markExecTransportClosed(stream *ActiveStream, pending runtimecore.PendingExec) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	current, ok := stream.PendingExecs[pending.ExecID]
	if ok {
		now := time.Now().UTC()
		current.StreamState = "transport_closed"
		current.LastShellActivityAt = now
		stream.PendingExecs[pending.ExecID] = current
		stream.UpdatedAt = now
	}
	stream.mu.Unlock()
}

func snapshotPendingExec(stream *ActiveStream, execID string) (runtimecore.PendingExec, bool) {
	if stream == nil || strings.TrimSpace(execID) == "" {
		return runtimecore.PendingExec{}, false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	item, ok := stream.PendingExecs[strings.TrimSpace(execID)]
	return item, ok
}
