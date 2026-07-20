package forwarder

import (
	"testing"

	"cursor/gen/agentv1"

	"google.golang.org/protobuf/proto"
)

// newAgentClientMessageWithRunRequest 构造一个带 run_request 的 AgentClientMessage，
// 用于 decodeInboundIntent 测试夹具。ConversationId 是 *string，用 proto.String。
func newAgentClientMessageWithRunRequest(t *testing.T, conversationID string, modelID string) *agentv1.AgentClientMessage {
	t.Helper()
	req := &agentv1.AgentRunRequest{
		ConversationId: proto.String(conversationID),
		Action: &agentv1.ConversationAction{
			Action: &agentv1.ConversationAction_UserMessageAction{
				UserMessageAction: &agentv1.UserMessageAction{},
			},
		},
	}
	if modelID != "" {
		req.RequestedModel = &agentv1.RequestedModel{ModelId: modelID}
	}
	return &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{RunRequest: req},
	}
}

// TestDecodeInboundIntent_RunRequest 验证 run_request 路径产出 run intent，
// ModelID 空时回退 default，RequestID 被 trim。最小夹具：&Service{}（debug/broker nil-safe）。
func TestDecodeInboundIntent_RunRequest(t *testing.T) {
	service := &Service{}
	msg := newAgentClientMessageWithRunRequest(t, "conv-1", "gpt-5")
	intent, err := service.decodeInboundIntent("  req-1  ", msg, "run_request")
	if err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if intent.Kind != "run" {
		t.Fatalf("Kind = %q, want run", intent.Kind)
	}
	if !intent.StartsRun {
		t.Fatal("StartsRun should be true")
	}
	if intent.RequestID != "req-1" {
		t.Fatalf("RequestID not trimmed: %q", intent.RequestID)
	}
	if intent.ConversationID != "conv-1" {
		t.Fatalf("ConversationID = %q", intent.ConversationID)
	}
	if intent.ModelID != "gpt-5" {
		t.Fatalf("ModelID = %q", intent.ModelID)
	}
}

// TestDecodeInboundIntent_RunRequestEmptyModelFallsBack 验证空 modelID 回退 default。
func TestDecodeInboundIntent_RunRequestEmptyModelFallsBack(t *testing.T) {
	service := &Service{}
	msg := newAgentClientMessageWithRunRequest(t, "conv-2", "")
	intent, err := service.decodeInboundIntent("req-2", msg, "run_request")
	if err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if intent.ModelID != "default" {
		t.Fatalf("ModelID = %q, want default", intent.ModelID)
	}
}

// TestDecodeInboundIntent_RunRequestMissingPayload 验证 run_request 缺 payload 报错。
func TestDecodeInboundIntent_RunRequestMissingPayload(t *testing.T) {
	service := &Service{}
	msg := &agentv1.AgentClientMessage{}
	_, err := service.decodeInboundIntent("req-3", msg, "run_request")
	if err == nil {
		t.Fatal("expected error for missing run_request payload")
	}
}

// TestDecodeInboundIntent_RunRequestMissingConversationID 验证缺 conversation_id 报错。
func TestDecodeInboundIntent_RunRequestMissingConversationID(t *testing.T) {
	service := &Service{}
	msg := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{ConversationId: proto.String("")},
		},
	}
	_, err := service.decodeInboundIntent("req-4", msg, "run_request")
	if err == nil {
		t.Fatal("expected error for missing conversation_id")
	}
}

// TestDecodeInboundIntent_ExecResultKind 验证 exec_client_message 路径。
func TestDecodeInboundIntent_ExecResultKind(t *testing.T) {
	service := &Service{}
	msg := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ExecClientMessage{
			ExecClientMessage: &agentv1.ExecClientMessage{},
		},
	}
	intent, err := service.decodeInboundIntent("req-5", msg, "exec_client_message")
	if err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if intent.Kind != "exec_result" {
		t.Fatalf("Kind = %q, want exec_result", intent.Kind)
	}
	if intent.ExecClientMessage == nil {
		t.Fatal("ExecClientMessage should be set")
	}
}

// TestDecodeInboundIntent_ExecControlKind 验证 exec_client_control_message 路径。
func TestDecodeInboundIntent_ExecControlKind(t *testing.T) {
	service := &Service{}
	msg := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ExecClientControlMessage{
			ExecClientControlMessage: &agentv1.ExecClientControlMessage{},
		},
	}
	intent, err := service.decodeInboundIntent("req-6", msg, "exec_client_control_message")
	if err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if intent.Kind != "exec_control" {
		t.Fatalf("Kind = %q, want exec_control", intent.Kind)
	}
}

// TestDecodeInboundIntent_Heartbeat 验证 client_heartbeat 路径产出 metadata。
func TestDecodeInboundIntent_Heartbeat(t *testing.T) {
	service := &Service{}
	msg := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ClientHeartbeat{},
	}
	intent, err := service.decodeInboundIntent("req-7", msg, "client_heartbeat")
	if err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if intent.Kind != "metadata" {
		t.Fatalf("Kind = %q, want metadata", intent.Kind)
	}
}

// TestDecodeInboundIntent_InteractionResponse 验证 interaction_response 路径。
func TestDecodeInboundIntent_InteractionResponse(t *testing.T) {
	service := &Service{}
	msg := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_InteractionResponse{
			InteractionResponse: &agentv1.InteractionResponse{},
		},
	}
	intent, err := service.decodeInboundIntent("req-8", msg, "interaction_response")
	if err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if intent.Kind != "interaction_result" {
		t.Fatalf("Kind = %q, want interaction_result", intent.Kind)
	}
}

// TestDecodeInboundIntent_KVClientMessage 验证 kv_client_message 路径。
func TestDecodeInboundIntent_KVClientMessage(t *testing.T) {
	service := &Service{}
	msg := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_KvClientMessage{
			KvClientMessage: &agentv1.KvClientMessage{},
		},
	}
	intent, err := service.decodeInboundIntent("req-9", msg, "kv_client_message")
	if err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if intent.Kind != "kv_result" {
		t.Fatalf("Kind = %q, want kv_result", intent.Kind)
	}
}

// TestDecodeInboundIntent_UnsupportedKind 验证未知 kind 报错。
func TestDecodeInboundIntent_UnsupportedKind(t *testing.T) {
	service := &Service{}
	_, err := service.decodeInboundIntent("req-10", &agentv1.AgentClientMessage{}, "bogus_kind")
	if err == nil {
		t.Fatal("expected error for unsupported kind")
	}
}
