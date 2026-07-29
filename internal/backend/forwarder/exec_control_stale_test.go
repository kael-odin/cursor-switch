package forwarder

import (
	"testing"
	"time"

	agentv1 "cursor/gen/agentv1"
)

// TestShouldIgnoreMissingExecControlDistinguishesStaleVsNeverExisted 是审计 L7 的回归：
// 原 shouldIgnoreMissingExecControl 对 Heartbeat/StreamClose 一律返回 true（静默吞），
// 不区分"已处理（recentlyCompletedExecExists）"与"从未存在（可能协议错误）"。
// 修复后：
//   - Heartbeat/StreamClose 且对应 exec 最近刚完成 → 忽略（已处理，合理）；
//   - Heartbeat/StreamClose 但从未存在 → 仍忽略（绝不杀流，迟到控制消息不应经 failStream
//     误杀整个流），但应可被诊断（生产代码记 WARN）；
//   - 非 Heartbeat/StreamClose（如 Throw）→ 走原 recentlyCompletedExecExists 判定。
//
// 关键约束："never existed" 必须返回 true（不杀流）——若返回 error 会经 actor.go:288-289
// failStream 把整个流标失败，重连客户端迟到的 Heartbeat 会误杀对话，比静默吞更糟。
func TestShouldIgnoreMissingExecControlDistinguishesStaleVsNeverExisted(t *testing.T) {
	const requestID = "req-123"
	completedStream := &ActiveStream{
		RecentCompletedExecs: map[uint32]time.Time{
			42: time.Now().Add(-1 * time.Second), // 刚完成，在 retention 窗口内
		},
	}
	emptyStream := &ActiveStream{
		RecentCompletedExecs: map[uint32]time.Time{}, // 从未存在该 exec
	}

	tests := []struct {
		name    string
		message *agentv1.ExecClientControlMessage
		stream  *ActiveStream
		want    bool // true = 忽略（不杀流）
	}{
		{
			name:    "Heartbeat 已完成的 exec → 忽略（已处理，合理）",
			message: &agentv1.ExecClientControlMessage{Message: &agentv1.ExecClientControlMessage_Heartbeat{Heartbeat: &agentv1.ExecClientHeartbeat{Id: 42}}},
			stream:  completedStream,
			want:    true,
		},
		{
			name:    "StreamClose 已完成的 exec → 忽略（已处理，合理）",
			message: &agentv1.ExecClientControlMessage{Message: &agentv1.ExecClientControlMessage_StreamClose{StreamClose: &agentv1.ExecClientStreamClose{Id: 42}}},
			stream:  completedStream,
			want:    true,
		},
		{
			name:    "Heartbeat 从未存在的 exec → 仍忽略（不杀流，仅记 WARN）",
			message: &agentv1.ExecClientControlMessage{Message: &agentv1.ExecClientControlMessage_Heartbeat{Heartbeat: &agentv1.ExecClientHeartbeat{Id: 99}}},
			stream:  emptyStream,
			want:    true, // 关键：绝不杀流
		},
		{
			name:    "StreamClose 从未存在的 exec → 仍忽略（不杀流，仅记 WARN）",
			message: &agentv1.ExecClientControlMessage{Message: &agentv1.ExecClientControlMessage_StreamClose{StreamClose: &agentv1.ExecClientStreamClose{Id: 99}}},
			stream:  emptyStream,
			want:    true, // 关键：绝不杀流
		},
		{
			name:    "Throw 已完成的 exec → 忽略（走 recentlyCompletedExecExists）",
			message: &agentv1.ExecClientControlMessage{Message: &agentv1.ExecClientControlMessage_Throw{Throw: &agentv1.ExecClientThrow{Id: 42}}},
			stream:  completedStream,
			want:    true,
		},
		{
			name:    "Throw 从未存在的 exec → 不忽略（Throw 非传输噪声，应 surface）",
			message: &agentv1.ExecClientControlMessage{Message: &agentv1.ExecClientControlMessage_Throw{Throw: &agentv1.ExecClientThrow{Id: 99}}},
			stream:  emptyStream,
			want:    false,
		},
		{
			name:    "nil message → 不忽略",
			message: nil,
			stream:  completedStream,
			want:    false,
		},
		{
			name:    "nil stream → 非传输噪声消息不忽略",
			message: &agentv1.ExecClientControlMessage{Message: &agentv1.ExecClientControlMessage_Throw{Throw: &agentv1.ExecClientThrow{Id: 1}}},
			stream:  nil,
			want:    false,
		},
		{
			name:    "nil stream + Heartbeat → 仍忽略（isStaleTransportExecControl 不依赖 stream）",
			message: &agentv1.ExecClientControlMessage{Message: &agentv1.ExecClientControlMessage_Heartbeat{Heartbeat: &agentv1.ExecClientHeartbeat{Id: 1}}},
			stream:  nil,
			want:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldIgnoreMissingExecControl(tt.message, tt.stream, requestID)
			if got != tt.want {
				t.Fatalf("shouldIgnoreMissingExecControl() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestShouldIgnoreStaleExecControlStreamNotActive 验证 stream 已不 active 场景
// （exec_intent.go:100）：Heartbeat/StreamClose 静默吞合理，无 stream 可查 recent completed。
func TestShouldIgnoreStaleExecControlStreamNotActive(t *testing.T) {
	tests := []struct {
		name    string
		message *agentv1.ExecClientControlMessage
		want    bool
	}{
		{"Heartbeat 静默吞", &agentv1.ExecClientControlMessage{Message: &agentv1.ExecClientControlMessage_Heartbeat{Heartbeat: &agentv1.ExecClientHeartbeat{Id: 1}}}, true},
		{"StreamClose 静默吞", &agentv1.ExecClientControlMessage{Message: &agentv1.ExecClientControlMessage_StreamClose{StreamClose: &agentv1.ExecClientStreamClose{Id: 1}}}, true},
		{"Throw 不吞", &agentv1.ExecClientControlMessage{Message: &agentv1.ExecClientControlMessage_Throw{Throw: &agentv1.ExecClientThrow{Id: 1}}}, false},
		{"nil 不吞", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldIgnoreStaleExecControl(tt.message); got != tt.want {
				t.Fatalf("shouldIgnoreStaleExecControl() = %v, want %v", got, tt.want)
			}
		})
	}
}
