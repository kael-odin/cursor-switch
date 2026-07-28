// tool_catalog.go 负责从静态 prompt 资产中装载并筛选 canonical tool catalog。
package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"

	"cursor/gen/agentv1"
	promptassets "cursor/prompt"
)

type DefaultToolCatalog struct {
}

// NewToolCatalog 创建默认工具目录实现。
func NewToolCatalog() *DefaultToolCatalog {
	return &DefaultToolCatalog{}
}

// Load 按 mode 读取工具资产，并过滤出当前阶段真正允许暴露的工具。
func (catalog *DefaultToolCatalog) Load(mode agentv1.AgentMode, subagentTypeName string) ([]json.RawMessage, []string, error) {
	assetMode, err := toolAssetModeForConversation(mode, subagentTypeName)
	if err != nil {
		return nil, nil, err
	}
	rawTools, err := promptassets.ReadTools(assetMode)
	if err != nil {
		return nil, nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawTools, &items); err != nil {
		return nil, nil, fmt.Errorf("decode tools asset failed: %w", err)
	}
	filtered := make([]json.RawMessage, 0, len(items))
	names := make([]string, 0, len(items))
	for _, item := range items {
		name, err := extractToolName(item)
		if err != nil {
			return nil, nil, err
		}
		if !isToolAllowedInMode(mode, subagentTypeName, name) {
			continue
		}
		filtered = append(filtered, item)
		names = append(names, name)
	}
	return filtered, names, nil
}

var agentModeToolNames = map[string]struct{}{
	"AskQuestion":          {},
	"CallMcpTool":          {},
	"Delete":               {},
	"FetchMcpResource":     {},
	"GenerateImage":        {},
	"Glob":                 {},
	"Grep":                 {},
	"Ls":                   {},
	"PatchEdit":            {},
	"Read":                 {},
	"ReadLints":            {},
	"Shell":                {},
	"AwaitShell":           {},
	"WriteShellStdin":      {},
	"ForceBackgroundShell": {},
	"SwitchMode":           {},
	"Task":                 {},
	"TodoWrite":            {},
	"WebFetch":             {},
	"WebSearch":            {},
	"Write":                {},
}

var multitaskModeToolNames = map[string]struct{}{
	"AskQuestion":          {},
	"CallMcpTool":          {},
	"Delete":               {},
	"FetchMcpResource":     {},
	"GenerateImage":        {},
	"Glob":                 {},
	"Grep":                 {},
	"Ls":                   {},
	"PatchEdit":            {},
	"Read":                 {},
	"ReadLints":            {},
	"Shell":                {},
	"AwaitShell":           {},
	"WriteShellStdin":      {},
	"ForceBackgroundShell": {},
	"SwitchMode":           {},
	"Task":                 {},
	"TodoWrite":            {},
	"WebFetch":             {},
	"WebSearch":            {},
	"Write":                {},
}

var debugModeToolNames = map[string]struct{}{
	"AskQuestion":          {},
	"CallMcpTool":          {},
	"Delete":               {},
	"FetchMcpResource":     {},
	"Glob":                 {},
	"Grep":                 {},
	"Ls":                   {},
	"PatchEdit":            {},
	"Read":                 {},
	"ReadLints":            {},
	"Shell":                {},
	"AwaitShell":           {},
	"WriteShellStdin":      {},
	"ForceBackgroundShell": {},
	"Task":                 {},
	"TodoWrite":            {},
	"WebFetch":             {},
	"WebSearch":            {},
	"Write":                {},
}

var askModeToolNames = map[string]struct{}{
	"AskQuestion":          {},
	"CallMcpTool":          {},
	"Delete":               {},
	"FetchMcpResource":     {},
	"Glob":                 {},
	"Grep":                 {},
	"Ls":                   {},
	"PatchEdit":            {},
	"Read":                 {},
	"ReadLints":            {},
	"Shell":                {},
	"AwaitShell":           {},
	"WriteShellStdin":      {},
	"ForceBackgroundShell": {},
	"Task":                 {},
	"TodoWrite":            {},
	"WebFetch":             {},
	"WebSearch":            {},
	"Write":                {},
}

var planModeToolNames = map[string]struct{}{
	"AskQuestion":          {},
	"CallMcpTool":          {},
	"CreatePlan":           {},
	"FetchMcpResource":     {},
	"Glob":                 {},
	"Grep":                 {},
	"Ls":                   {},
	"Read":                 {},
	"ReadLints":            {},
	"Shell":                {},
	"AwaitShell":           {},
	"WriteShellStdin":      {},
	"ForceBackgroundShell": {},
	"Task":                 {},
	"TodoWrite":            {},
	"WebFetch":             {},
	"WebSearch":            {},
}

var childConversationDisallowedAgentToolNames = map[string]struct{}{
	"AskQuestion": {},
}

// readonlySubagentToolNames 是 F-31 引入的"只读子代理"能力集。
//
// 此前 subagentTypeName 非空的子代理一律使用完整 agentModeToolNames，仅排除
// AskQuestion——即使 readonly=true 的 Task（客户端把子代理 Mode 映射为
// AGENT_MODE_PLAN）也能拿到 Write/PatchEdit/Delete/Shell 等副作用工具，只靠
// prompt 约束。本集合剔除全部副作用入口（写/删/打补丁/Shell/写 stdin/Task 生成
// 子代理/GenerateImage），只留读取与无副作用交互（Read/Grep/Glob/Ls/ReadLints/
// WebSearch/WebFetch/FetchMcpResource 读/CallMcpTool/TodoWrite/SwitchMode）。
//
// 注意：CallMcpTool 与 FetchMcpResource 理论上可触发副作用，但子代理场景下它们
// 主要用于读取工具结果；若需更严格可后续剔除。当前与 planModeToolNames 对齐。
var readonlySubagentToolNames = map[string]struct{}{
	"AskQuestion":      {},
	"CallMcpTool":      {},
	"FetchMcpResource": {},
	"Glob":             {},
	"Grep":             {},
	"Ls":               {},
	"Read":             {},
	"ReadLints":        {},
	"SwitchMode":       {},
	"TodoWrite":        {},
	"WebFetch":         {},
	"WebSearch":        {},
}

func supportedToolNamesForMode(mode agentv1.AgentMode) map[string]struct{} {
	switch normalizeMode(mode) {
	case agentv1.AgentMode_AGENT_MODE_AGENT:
		return agentModeToolNames
	case agentv1.AgentMode_AGENT_MODE_ASK:
		return askModeToolNames
	case agentv1.AgentMode_AGENT_MODE_PLAN:
		return planModeToolNames
	case agentv1.AgentMode_AGENT_MODE_DEBUG:
		return debugModeToolNames
	case agentv1.AgentMode_AGENT_MODE_MULTITASK:
		return multitaskModeToolNames
	default:
		return nil
	}
}

func isToolAllowedInMode(mode agentv1.AgentMode, subagentTypeName string, toolName string) bool {
	trimmedToolName := strings.TrimSpace(toolName)
	if trimmedToolName == "" {
		return false
	}
	if isChildConversationSubagentTypeName(subagentTypeName) {
		// F-31：readonly=true 的 Task，客户端把子代理 Mode 映射为 AGENT_MODE_PLAN。
		// 此前一律走完整 agentModeToolNames，忽略 readonly——子代理仍可拿 Write/Delete/
		// Shell 等副作用工具，只靠 prompt 约束。这里对 mode==PLAN 的子代理强制使用
		// 只读能力集，形成后端 capability（catalog 生成、tool dispatch、child conversation
		// 三个边界都经过本函数）。
		if normalizeMode(mode) == agentv1.AgentMode_AGENT_MODE_PLAN {
			_, ok := readonlySubagentToolNames[trimmedToolName]
			return ok
		}
		if _, disallowed := childConversationDisallowedAgentToolNames[trimmedToolName]; disallowed {
			return false
		}
		_, ok := agentModeToolNames[trimmedToolName]
		return ok
	}
	supported := supportedToolNamesForMode(mode)
	if supported == nil {
		return false
	}
	_, ok := supported[trimmedToolName]
	return ok
}

func isChildConversationSubagentTypeName(subagentTypeName string) bool {
	return strings.TrimSpace(subagentTypeName) != ""
}

func selectToolsByOrderedNames(items []json.RawMessage, orderedNames []string) ([]json.RawMessage, []string, error) {
	byName := make(map[string]json.RawMessage, len(items))
	for _, item := range items {
		name, err := extractToolName(item)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := byName[name]; !exists {
			byName[name] = item
		}
	}
	filtered := make([]json.RawMessage, 0, len(orderedNames))
	names := make([]string, 0, len(orderedNames))
	for _, name := range orderedNames {
		item, ok := byName[name]
		if !ok {
			return nil, nil, fmt.Errorf("tool descriptor %q not found in prompt asset", name)
		}
		filtered = append(filtered, item)
		names = append(names, name)
	}
	return filtered, names, nil
}

func toolAssetModeForConversation(mode agentv1.AgentMode, subagentTypeName string) (promptassets.Mode, error) {
	if isChildConversationSubagentTypeName(subagentTypeName) {
		return promptassets.ModeAgent, nil
	}
	return mapPromptMode(mode)
}

func promptAssetModeForConversation(mode agentv1.AgentMode, subagentTypeName string) (promptassets.Mode, error) {
	if isChildConversationSubagentTypeName(subagentTypeName) {
		return promptassets.ModeSubagent, nil
	}
	return mapPromptMode(mode)
}

// mapPromptMode 把协议 mode 映射为静态 prompt 资产对应的目录名。
func mapPromptMode(mode agentv1.AgentMode) (promptassets.Mode, error) {
	switch normalizeMode(mode) {
	case agentv1.AgentMode_AGENT_MODE_AGENT:
		return promptassets.ModeAgent, nil
	case agentv1.AgentMode_AGENT_MODE_ASK:
		return promptassets.ModeAsk, nil
	case agentv1.AgentMode_AGENT_MODE_PLAN:
		return promptassets.ModePlan, nil
	case agentv1.AgentMode_AGENT_MODE_DEBUG:
		return promptassets.ModeDebug, nil
	case agentv1.AgentMode_AGENT_MODE_MULTITASK:
		return promptassets.ModeMultitask, nil
	default:
		return "", fmt.Errorf("unsupported prompt asset mode: %s", mode.String())
	}
}

// extractToolName 从原始 tool descriptor JSON 中提取函数名。
func extractToolName(raw json.RawMessage) (string, error) {
	var wrapper struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return "", fmt.Errorf("decode tool descriptor failed: %w", err)
	}
	name := strings.TrimSpace(wrapper.Function.Name)
	if name == "" {
		return "", fmt.Errorf("tool descriptor name is required")
	}
	return name, nil
}

// sanitizePromptAsset 去掉资产文件中的说明性标题，只保留真正的 prompt 文本。
func sanitizePromptAsset(text string, modelName string) string {
	lines := strings.Split(text, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "# 通用系统提示词", "# 模式静态补充", "---":
			continue
		default:
			filtered = append(filtered, line)
		}
	}
	return promptassets.RenderPromptTemplate(strings.TrimSpace(strings.Join(filtered, "\n")), modelName)
}
