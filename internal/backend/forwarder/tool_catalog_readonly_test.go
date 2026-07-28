package forwarder

import (
	"testing"

	"cursor/gen/agentv1"
)

// TestIsToolAllowedInModeReadonlySubagent 是 F-31 的核心回归：
// readonly=true 的 Task，客户端把子代理 Mode 映射为 AGENT_MODE_PLAN。
// 此前 subagentTypeName 非空一律走完整 agentModeToolNames，readonly 子代理仍可拿到
// Write/Delete/Shell 等副作用工具。修复后 mode==PLAN 的子代理必须使用只读能力集。
func TestIsToolAllowedInModeReadonlySubagent(t *testing.T) {
	const subagent = "explore"
	readonlyMode := agentv1.AgentMode_AGENT_MODE_PLAN

	// 副作用工具在 readonly 子代理下必须被拒。
	disallowed := []string{"Write", "PatchEdit", "Delete", "Shell", "WriteShellStdin", "ForceBackgroundShell", "Task", "GenerateImage", "AwaitShell"}
	for _, tool := range disallowed {
		if isToolAllowedInMode(readonlyMode, subagent, tool) {
			t.Errorf("F-31 FAIL: readonly subagent allowed side-effect tool %q (mode=PLAN, subagent=%q)", tool, subagent)
		}
	}

	// 只读工具应放行。
	allowed := []string{"Read", "Grep", "Glob", "Ls", "ReadLints", "WebSearch", "WebFetch", "TodoWrite", "SwitchMode", "CallMcpTool", "FetchMcpResource"}
	for _, tool := range allowed {
		if !isToolAllowedInMode(readonlyMode, subagent, tool) {
			t.Errorf("F-31 FAIL: readonly subagent rejected read-only tool %q (mode=PLAN, subagent=%q)", tool, subagent)
		}
	}
}

// TestIsToolAllowedInModeReadwriteSubagentKeepsFullAgentSet 验证非 readonly 子代理
// （mode != PLAN，如 AGENT）仍保留完整 agent 工具集——修复不影响正常子代理能力。
func TestIsToolAllowedInModeReadwriteSubagentKeepsFullAgentSet(t *testing.T) {
	const subagent = "explore"
	rwMode := agentv1.AgentMode_AGENT_MODE_AGENT
	// 写工具在读写子代理下应放行。
	for _, tool := range []string{"Write", "Delete", "Shell", "PatchEdit"} {
		if !isToolAllowedInMode(rwMode, subagent, tool) {
			t.Errorf("F-31 regression: read-write subagent (mode=AGENT) rejected tool %q", tool)
		}
	}
	// AskQuestion 在子代理下始终被禁（既有行为）。
	if isToolAllowedInMode(rwMode, subagent, "AskQuestion") {
		t.Errorf("AskQuestion should be disallowed in child conversation (existing behavior)")
	}
}

// TestIsToolAllowedInModePlanTopLevelUnchanged 验证顶层 Plan 模式（非子代理）
// 行为不变——仍用 planModeToolNames。
func TestIsToolAllowedInModePlanTopLevelUnchanged(t *testing.T) {
	// subagentTypeName 空 → 顶层模式，走 supportedToolNamesForMode。
	if !isToolAllowedInMode(agentv1.AgentMode_AGENT_MODE_PLAN, "", "Read") {
		t.Error("top-level Plan should allow Read")
	}
	if isToolAllowedInMode(agentv1.AgentMode_AGENT_MODE_PLAN, "", "Write") {
		t.Error("top-level Plan should disallow Write (planModeToolNames)")
	}
}

// TestReadonlySubagentToolNamesExcludesAllSideEffects 是能力集自身的完整性校验：
// 确保没有副作用工具漏进只读集合。
func TestReadonlySubagentToolNamesExcludesAllSideEffects(t *testing.T) {
	sideEffect := []string{"Write", "PatchEdit", "Delete", "Shell", "AwaitShell", "WriteShellStdin", "ForceBackgroundShell", "Task", "GenerateImage"}
	for _, tool := range sideEffect {
		if _, ok := readonlySubagentToolNames[tool]; ok {
			t.Errorf("F-31 FAIL: readonlySubagentToolNames must not contain side-effect tool %q", tool)
		}
	}
}
