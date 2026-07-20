package forwarder

import (
	"encoding/json"
	"strings"
	"testing"

	"cursor/gen/agentv1"
)

// TestAssistantTextEntryPayload 验证 assistant_text entry 的 payload 序列化与字段 trim。
// 这是 forwarder 写 history 的核心单元，trim 行为错了会让前端展示出现前后空格。
func TestAssistantTextEntryPayload(t *testing.T) {
	entry := newAssistantTextEntryWithProviderMetadata(
		1, "  req-1  ", "hello", "  reasoning  ", "  sig  ", "provider", "item-1", "done", json.RawMessage(`{"x":1}`),
	)
	if entry.TurnSeq != 1 {
		t.Fatalf("TurnSeq = %d", entry.TurnSeq)
	}
	if entry.RequestID != "req-1" {
		t.Fatalf("RequestID not trimmed: %q", entry.RequestID)
	}
	if entry.Role != "assistant" || entry.Kind != "assistant_text" {
		t.Fatalf("role/kind = %s/%s", entry.Role, entry.Kind)
	}
	var payload assistantTextPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Text != "hello" {
		t.Fatalf("Text = %q", payload.Text)
	}
	if payload.ReasoningContent != "  reasoning  " {
		// reasoning content 不被 trim（保留原始格式），仅签名类字段 trim
		t.Fatalf("ReasoningContent should preserve formatting, got %q", payload.ReasoningContent)
	}
	if payload.ReasoningSignature != "sig" {
		t.Fatalf("ReasoningSignature not trimmed: %q", payload.ReasoningSignature)
	}
	if payload.ReasoningSignatureSource != "provider" {
		t.Fatalf("ReasoningSignatureSource = %q", payload.ReasoningSignatureSource)
	}
}

// TestToolCallEntryPayload 验证 tool_call entry 的 payload 与 ToolCallID 字段同步。
func TestToolCallEntryPayload(t *testing.T) {
	entry := newToolCallEntry(2, "req-2", "call-1", "Read", "rsn", "sig", json.RawMessage(`{"path":"a"}`))
	if entry.ToolCallID != "call-1" {
		t.Fatalf("ToolCallID = %q", entry.ToolCallID)
	}
	if entry.Kind != "tool_call" {
		t.Fatalf("Kind = %s", entry.Kind)
	}
	var payload toolCallEntryPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.ToolName != "Read" {
		t.Fatalf("ToolName = %q", payload.ToolName)
	}
	if string(payload.ToolCall) != `{"path":"a"}` {
		t.Fatalf("ToolCall not preserved: %q", string(payload.ToolCall))
	}
}

// TestToolResultEntryPayload 验证 tool_result entry 的字段 trim。
func TestToolResultEntryPayload(t *testing.T) {
	entry := newToolResultEntry(3, "req-3", "call-1", "Read", "  args  ", "  result  ", "rsn", json.RawMessage(`{}`))
	var payload toolResultEntryPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Arguments != "args" {
		t.Fatalf("Arguments not trimmed: %q", payload.Arguments)
	}
	if payload.ResultText != "result" {
		t.Fatalf("ResultText not trimmed: %q", payload.ResultText)
	}
	if entry.Role != "tool" {
		t.Fatalf("Role = %s", entry.Role)
	}
}

// TestMetadataEntry 验证 metadata entry 的 type 与 value 结构。
func TestMetadataEntry(t *testing.T) {
	entry := newMetadataEntry(4, "req-4", "run_request", map[string]any{"model_id": "gpt-5"})
	if entry.Role != "system" || entry.Kind != "metadata" {
		t.Fatalf("role/kind = %s/%s", entry.Role, entry.Kind)
	}
	var payload metadataPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Type != "run_request" {
		t.Fatalf("Type = %q", payload.Type)
	}
	if payload.Value["model_id"] != "gpt-5" {
		t.Fatalf("Value = %v", payload.Value)
	}
}

// TestModeAlias 验证 mode→alias 映射，未知 mode 返回错误。
func TestModeAlias(t *testing.T) {
	tests := []struct {
		mode    agentv1.AgentMode
		want    string
		wantErr bool
	}{
		{agentv1.AgentMode_AGENT_MODE_AGENT, "agent", false},
		{agentv1.AgentMode_AGENT_MODE_ASK, "ask", false},
		{agentv1.AgentMode_AGENT_MODE_PLAN, "plan", false},
		{agentv1.AgentMode_AGENT_MODE_DEBUG, "debug", false},
		{agentv1.AgentMode_AGENT_MODE_MULTITASK, "multitask", false},
		{agentv1.AgentMode_AGENT_MODE_UNSPECIFIED, "", true},
	}
	for _, tc := range tests {
		got, err := modeAlias(tc.mode)
		if tc.wantErr {
			if err == nil {
				t.Errorf("mode %v: expected error, got %q", tc.mode, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("mode %v: unexpected err: %v", tc.mode, err)
			continue
		}
		if got != tc.want {
			t.Errorf("mode %v: got %q want %q", tc.mode, got, tc.want)
		}
	}
}

// TestNewModeMetadataEntry 验证 explicit + source 是否进入 payload。
func TestNewModeMetadataEntry(t *testing.T) {
	entry, err := newModeMetadataEntry(5, "req-5", agentv1.AgentMode_AGENT_MODE_AGENT, true, ModeSourceUserMessage)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	var payload metadataPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Value["mode"] != "agent" {
		t.Fatalf("mode alias = %v", payload.Value["mode"])
	}
	if payload.Value["explicit"] != true {
		t.Fatalf("explicit missing")
	}
	if payload.Value["source"] != string(ModeSourceUserMessage) {
		t.Fatalf("source = %v", payload.Value["source"])
	}
}

// TestBuildRunEntries_Minimal 验证无 UserMessage/RequestContext 时仍产出 mode + run_request 两条。
func TestBuildRunEntries_Minimal(t *testing.T) {
	intent := InboundIntent{
		RequestID:       "req-6",
		ModelID:         "gpt-5",
		ModelName:       "GPT-5",
		Mode:            agentv1.AgentMode_AGENT_MODE_AGENT,
		HasExplicitMode: false,
		ModeSource:      ModeSourceUserMessage,
	}
	entries, err := buildRunEntries(intent, agentv1.AgentMode_AGENT_MODE_AGENT, 1)
	if err != nil {
		t.Fatalf("buildRunEntries: %v", err)
	}
	// 至少包含 mode entry 和 run_request metadata entry。
	var sawMode, sawRunRequest bool
	for _, e := range entries {
		var p metadataPayload
		if e.Kind != "metadata" {
			continue
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("unmarshal metadata: %v", err)
		}
		if p.Type == "mode" {
			sawMode = true
		}
		if p.Type == "run_request" {
			sawRunRequest = true
			if p.Value["model_id"] != "gpt-5" {
				t.Fatalf("run_request model_id = %v", p.Value["model_id"])
			}
		}
	}
	if !sawMode {
		t.Fatalf("mode entry missing, got %+v", entries)
	}
	if !sawRunRequest {
		t.Fatalf("run_request entry missing, got %+v", entries)
	}
}

// TestBuildRunEntries_ExplicitModeAddsPromptContext 验证 HasExplicitMode 时追加 mode_change prompt context。
func TestBuildRunEntries_ExplicitModeAddsPromptContext(t *testing.T) {
	intent := InboundIntent{
		RequestID:       "req-7",
		Mode:            agentv1.AgentMode_AGENT_MODE_AGENT,
		HasExplicitMode: true,
		ModeSource:      ModeSourceUserMessage,
	}
	entries, err := buildRunEntries(intent, agentv1.AgentMode_AGENT_MODE_AGENT, 1)
	if err != nil {
		t.Fatalf("buildRunEntries: %v", err)
	}
	sawPromptContext := false
	for _, e := range entries {
		if strings.Contains(e.Kind, "prompt_context") || e.Kind == "prompt_context" {
			sawPromptContext = true
		}
	}
	if !sawPromptContext {
		t.Fatalf("explicit mode should add prompt_context entry, got kinds: %+v", entryKinds(entries))
	}
}

func entryKinds(entries []HistoryEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Kind)
	}
	return out
}
