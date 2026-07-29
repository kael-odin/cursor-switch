// model_context_window.go 提供 fetch-models 的上下文窗口自动回填。
//
// 背景：provider 的 /v1/models 端点几乎都不返回 context window，用户得手填。
// cc-switch 的 ModelsDevPickerDialog 思路是从 models.dev 反查。这里用内置静态表
// （数据源 models.dev，缓存于 2026-07）做候选匹配回填，免去运行时联网依赖；
// 表里没有的模型回退到 0（前端再让用户手填）。
//
// 匹配复用 pricing 的候选算法（去命名空间/版本/日期/努力后缀/前缀回退），
// 使 "claude-opus-4-8-20260101" 也能命中 "claude-opus-4-8" 的窗口。
package client

import (
	"strings"

	serverconfig "cursor/internal/backend/server/config"
)

// contextWindowByModelID 是内置模型上下文窗口表（token 数）。数据源 models.dev（2026-07 缓存）。
// 只收录窗口 ≠ 200K 默认值的常见模型，避免表过大；未命中的模型由 resolver 的 200K 默认值兜底。
var contextWindowByModelID = map[string]int64{
	// Anthropic Claude —— 1M 上下文（Opus/Sonnet 4.6+、Fable/Mythos 5）
	"claude-fable-5":     1_000_000,
	"claude-mythos-5":    1_000_000,
	"claude-opus-5":      1_000_000,
	"claude-opus-4-8":    1_000_000,
	"claude-opus-4-7":    1_000_000,
	"claude-opus-4-6":    1_000_000,
	"claude-sonnet-5":    1_000_000,
	"claude-sonnet-4-6":  1_000_000,
	"claude-opus-4-5":    200_000,
	"claude-sonnet-4-5":  200_000,
	"claude-haiku-4-5":   200_000,
	"claude-opus-4-1":    200_000,
	"claude-opus-4":      200_000,
	"claude-sonnet-4":    200_000,
	"claude-3-5-sonnet":  200_000,
	"claude-3-5-haiku":   200_000,
	// OpenAI GPT-5.x —— 400K 上下文
	"gpt-5.6-sol":   400_000,
	"gpt-5.6-terra": 400_000,
	"gpt-5.6-luna":  400_000,
	"gpt-5.5":       400_000,
	"gpt-5.4":       400_000,
	"gpt-5.4-mini":  400_000,
	"gpt-5.4-nano":  400_000,
	"gpt-5.2":       400_000,
	"gpt-5.2-codex": 400_000,
	"gpt-5.3-codex": 400_000,
	"gpt-5.1":       400_000,
	"gpt-5.1-codex": 400_000,
	"gpt-5":         400_000,
	"gpt-5-codex":   400_000,
	"gpt-5-mini":    400_000,
	"gpt-5-nano":    400_000,
	"gpt-4.1":       1_000_000,
	"gpt-4.1-mini":  1_000_000,
	"gpt-4.1-nano":  1_000_000,
	"o3":            200_000,
	"o3-pro":        200_000,
	"o3-mini":       200_000,
	"o4-mini":       200_000,
	"o1":            200_000,
	"o1-mini":       128_000,
	// Google Gemini
	"gemini-3.5-flash":      1_000_000,
	"gemini-3.1-pro-preview": 2_000_000,
	"gemini-3.1-flash-lite": 1_000_000,
	"gemini-3-pro-preview":  2_000_000,
	"gemini-3-flash-preview": 1_000_000,
	"gemini-2.5-pro":        2_000_000,
	"gemini-2.5-flash":      1_000_000,
	"gemini-2.5-flash-lite": 1_000_000,
	"gemini-2.0-flash":      1_000_000,
	// xAI Grok
	"grok-4.5": 256_000,
	"grok-4.3": 256_000,
	"grok-4":   256_000,
	"grok-3":   1_000_000,
	"grok-3-mini": 1_000_000,
	// DeepSeek
	"deepseek-v3.2":      128_000,
	"deepseek-v3.1":      128_000,
	"deepseek-v3":        128_000,
	"deepseek-chat":      128_000,
	"deepseek-reasoner":  128_000,
	"deepseek-v4-flash":  128_000,
	"deepseek-v4-pro":    128_000,
	// Moonshot Kimi
	"kimi-k2-thinking": 256_000,
	"kimi-k2":          256_000,
	"kimi-k2-turbo":    256_000,
	"kimi-k2.5":        256_000,
	"kimi-k2.6":        256_000,
	"kimi-k2.7-code":   256_000,
	"kimi-k3":          256_000,
	// ByteDance Doubao
	"doubao-seed-2-1-pro":   256_000,
	"doubao-seed-2-1-turbo": 256_000,
	"doubao-seed-code":      256_000,
	"doubao-seed-2-0-pro":   256_000,
	"doubao-seed-2-0-code":  256_000,
	"doubao-seed-2-0-lite":  256_000,
	"doubao-seed-2-0-mini":  256_000,
	// Qwen
	"qwen3.7-max":        256_000,
	"qwen3.7-plus":       256_000,
	"qwen3-max":          256_000,
	"qwen3-235b-a22b":    256_000,
	"qwen3-coder-plus":   1_000_000,
	"qwen3-coder-480b":   1_000_000,
	"qwen3-coder-flash":  1_000_000,
	"qwq-plus":           256_000,
	"qwq-32b":            131_072,
	"qwen3-32b":          131_072,
	// GLM
	"glm-4.7": 128_000,
	"glm-4.6": 128_000,
	"glm-5":   128_000,
	"glm-5.1": 128_000,
	"glm-5.2": 128_000,
	// MiniMax
	"minimax-m2.1": 1_000_000,
	"minimax-m2":   1_000_000,
	"minimax-m2.5": 1_000_000,
	"minimax-m3":   1_000_000,
	// Xiaomi MiMo —— 实测确认（2026-07-29 直连 api.xiaomimimo.com 问模型自报）：
	// v2.5 系列上下文窗口均为 1,000,000。此前内置表写 128_000 是错的（小米 /v1/models
	// 不返回 context window，models.dev 未收录小米，错误值无法被在线回退纠正，导致
	// Cursor UI 显示 25K/128K 的分母错误）。v2-pro/v2-flash 已下线（"Unsupported model"），
	// 但保留并改 1M 与 v2.5 系列同口径，老配置渠道命中也合理。
	"mimo-v2-flash":             1_000_000,
	"mimo-v2-pro":               1_000_000,
	"mimo-v2.5":                 1_000_000,
	"mimo-v2.5-pro":             1_000_000,
	"mimo-v2.5-pro-ultraspeed":  1_000_000,
	// Step / Hunyuan / Mistral / Cohere
	"step-3.7-flash":   256_000,
	"step-3.5-flash":   256_000,
	"hunyuan-hy3":      256_000,
	"mistral-medium-3.5": 128_000,
	"mistral-small-4":    128_000,
	"mistral-large-3":    128_000,
	"codestral":          256_000,
	"command-a":          256_000,
	"command-r-plus":     128_000,
	"command-r":          128_000,
}

// lookupContextWindowTokens 用候选匹配查模型上下文窗口。未命中返回 0（由调用方兜底）。
func lookupContextWindowTokens(modelID string) int64 {
	if strings.TrimSpace(modelID) == "" {
		return 0
	}
	candidates := serverconfig.MatchPricingCandidates(modelID)
	for _, c := range candidates {
		if v, ok := contextWindowByModelID[c]; ok {
			return v
		}
	}
	return 0
}
