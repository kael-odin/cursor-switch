package config

import (
	"regexp"
	"strings"
)

// InputTokenSemantics 描述一家 provider 的 input token 计费口径。
// 不同 provider 对「input token 是否包含缓存部分」计费方式不同，cc-switch 把它分成三类：
//   - FRESH：input 按原样计费（缓存部分单独按 cache_read/cache_write 价计）。
//   - TOTAL：input 已包含 cache_read + cache_creation，需从 input 里减掉这两部分再按 input 价计费，
//     否则会把缓存部分既按 input 价又按 cache 价重复计费。
//   - legacy（默认）：input 已包含 cache_read，需从 input 里减掉 cache_read 再按 input 价计费。
//
// 参考 cc-switch usage_stats.rs 的 input_token_semantics。
type InputTokenSemantics string

const (
	InputSemanticsLegacy InputTokenSemantics = ""       // legacy：input 含 cache_read，减 cache_read
	InputSemanticsFresh  InputTokenSemantics = "FRESH" // FRESH：input 原样计费
	InputSemanticsTotal  InputTokenSemantics = "TOTAL" // TOTAL：input 含 cache_read+cache_creation，减两者
)

// ModelPricing 是单个模型的定价记录。价格单位：USD / 1,000,000 tokens。
// 照搬 cc-switch 的 model_pricing 表结构，用字符串存储以保留十进制精度（避免 float 误差）。
type ModelPricing struct {
	ModelID               string              `json:"modelId" yaml:"modelId"`
	DisplayName           string              `json:"displayName" yaml:"displayName"`
	InputPerMillion       string              `json:"inputPerMillion" yaml:"inputPerMillion"`
	OutputPerMillion      string              `json:"outputPerMillion" yaml:"outputPerMillion"`
	CacheReadPerMillion   string              `json:"cacheReadPerMillion,omitempty" yaml:"cacheReadPerMillion,omitempty"`
	CacheWritePerMillion  string              `json:"cacheWritePerMillion,omitempty" yaml:"cacheWritePerMillion,omitempty"`
	// InputTokenSemantics 控制 input token 成本回算口径（见 InputTokenSemantics 注释）。
	// 空字符串等价 legacy（向后兼容旧配置）。
	InputTokenSemantics InputTokenSemantics `json:"inputTokenSemantics,omitempty" yaml:"inputTokenSemantics,omitempty"`
}

// PricingConfig 承载全局倍率与模型定价表。
type PricingConfig struct {
	// DefaultCostMultiplier 全局默认成本倍率（字符串形式，如 "1" / "1.5"）。
	// 应用到所有模型的总成本。per-adapter 的 CostMultiplier 可覆盖此值。
	DefaultCostMultiplier string         `json:"defaultCostMultiplier" yaml:"defaultCostMultiplier"`
	Models                []ModelPricing `json:"models" yaml:"models"`
}

// pricingModelSeed 是内置标准价目表（USD/1M tokens）。照搬 cc-switch seed_model_pricing。
// 首次启动或表中缺失时按 modelId 增量补齐（INSERT OR IGNORE 语义），用户编辑过的同 id 不覆盖。
//
// InputTokenSemantics 标注规则（cc-switch 实测口径）：
//   - Anthropic Claude：legacy（input 已含 cache_read，需减 cache_read）
//   - OpenAI gpt-5.x / o-系列：TOTAL（input 含 cache_read+cache_creation，需减两者）
//   - 其余 provider 默认 legacy，有确切信息时单独标注。
var pricingModelSeed = []ModelPricing{
	// Anthropic Claude —— legacy 口径（input 含 cache_read）
	{ModelID: "claude-fable-5", DisplayName: "Claude Fable 5", InputPerMillion: "10", OutputPerMillion: "50", CacheReadPerMillion: "1.00", CacheWritePerMillion: "12.50"},
	{ModelID: "claude-mythos-5", DisplayName: "Claude Mythos 5", InputPerMillion: "10", OutputPerMillion: "50", CacheReadPerMillion: "1.00", CacheWritePerMillion: "12.50"},
	{ModelID: "claude-opus-5", DisplayName: "Claude Opus 5", InputPerMillion: "5", OutputPerMillion: "25", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25"},
	{ModelID: "claude-opus-4-8", DisplayName: "Claude Opus 4.8", InputPerMillion: "5", OutputPerMillion: "25", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25"},
	{ModelID: "claude-sonnet-5", DisplayName: "Claude Sonnet 5", InputPerMillion: "3", OutputPerMillion: "15", CacheReadPerMillion: "0.30", CacheWritePerMillion: "3.75"},
	{ModelID: "claude-opus-4-7", DisplayName: "Claude Opus 4.7", InputPerMillion: "5", OutputPerMillion: "25", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25"},
	{ModelID: "claude-opus-4-6", DisplayName: "Claude Opus 4.6", InputPerMillion: "5", OutputPerMillion: "25", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25"},
	{ModelID: "claude-opus-4-6-20260206", DisplayName: "Claude Opus 4.6", InputPerMillion: "5", OutputPerMillion: "25", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25"},
	{ModelID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6", InputPerMillion: "3", OutputPerMillion: "15", CacheReadPerMillion: "0.30", CacheWritePerMillion: "3.75"},
	{ModelID: "claude-sonnet-4-6-20260217", DisplayName: "Claude Sonnet 4.6", InputPerMillion: "3", OutputPerMillion: "15", CacheReadPerMillion: "0.30", CacheWritePerMillion: "3.75"},
	{ModelID: "claude-opus-4-5-20251101", DisplayName: "Claude Opus 4.5", InputPerMillion: "5", OutputPerMillion: "25", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25"},
	{ModelID: "claude-sonnet-4-5-20250929", DisplayName: "Claude Sonnet 4.5", InputPerMillion: "3", OutputPerMillion: "15", CacheReadPerMillion: "0.30", CacheWritePerMillion: "3.75"},
	{ModelID: "claude-haiku-4-5-20251001", DisplayName: "Claude Haiku 4.5", InputPerMillion: "1", OutputPerMillion: "5", CacheReadPerMillion: "0.10", CacheWritePerMillion: "1.25"},
	{ModelID: "claude-opus-4-20250514", DisplayName: "Claude Opus 4", InputPerMillion: "15", OutputPerMillion: "75", CacheReadPerMillion: "1.50", CacheWritePerMillion: "18.75"},
	{ModelID: "claude-opus-4-1-20250805", DisplayName: "Claude Opus 4.1", InputPerMillion: "15", OutputPerMillion: "75", CacheReadPerMillion: "1.50", CacheWritePerMillion: "18.75"},
	{ModelID: "claude-sonnet-4-20250514", DisplayName: "Claude Sonnet 4", InputPerMillion: "3", OutputPerMillion: "15", CacheReadPerMillion: "0.30", CacheWritePerMillion: "3.75"},
	{ModelID: "claude-3-5-haiku-20241022", DisplayName: "Claude 3.5 Haiku", InputPerMillion: "0.80", OutputPerMillion: "4", CacheReadPerMillion: "0.08", CacheWritePerMillion: "1"},
	{ModelID: "claude-3-5-sonnet-20241022", DisplayName: "Claude 3.5 Sonnet", InputPerMillion: "3", OutputPerMillion: "15", CacheReadPerMillion: "0.30", CacheWritePerMillion: "3.75"},
	// OpenAI GPT —— TOTAL 口径（input 含 cache_read+cache_creation）
	{ModelID: "gpt-5.6-sol", DisplayName: "GPT-5.6 Sol", InputPerMillion: "5", OutputPerMillion: "30", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-5.6-terra", DisplayName: "GPT-5.6 Terra", InputPerMillion: "2.50", OutputPerMillion: "15", CacheReadPerMillion: "0.25", CacheWritePerMillion: "3.125", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-5.6-luna", DisplayName: "GPT-5.6 Luna", InputPerMillion: "1", OutputPerMillion: "6", CacheReadPerMillion: "0.10", CacheWritePerMillion: "1.25", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-5.5", DisplayName: "GPT-5.5", InputPerMillion: "5", OutputPerMillion: "30", CacheReadPerMillion: "0.50", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-5.4", DisplayName: "GPT-5.4", InputPerMillion: "2.50", OutputPerMillion: "15", CacheReadPerMillion: "0.25", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-5.4-mini", DisplayName: "GPT-5.4 Mini", InputPerMillion: "0.75", OutputPerMillion: "4.50", CacheReadPerMillion: "0.075", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-5.4-nano", DisplayName: "GPT-5.4 Nano", InputPerMillion: "0.20", OutputPerMillion: "1.25", CacheReadPerMillion: "0.02", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-5.2", DisplayName: "GPT-5.2", InputPerMillion: "1.75", OutputPerMillion: "14", CacheReadPerMillion: "0.175", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-5.2-codex", DisplayName: "GPT-5.2 Codex", InputPerMillion: "1.75", OutputPerMillion: "14", CacheReadPerMillion: "0.175", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-5.3-codex", DisplayName: "GPT-5.3 Codex", InputPerMillion: "1.75", OutputPerMillion: "14", CacheReadPerMillion: "0.175", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-5.1", DisplayName: "GPT-5.1", InputPerMillion: "1.25", OutputPerMillion: "10", CacheReadPerMillion: "0.125", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-5.1-codex", DisplayName: "GPT-5.1 Codex", InputPerMillion: "1.25", OutputPerMillion: "10", CacheReadPerMillion: "0.125", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-5", DisplayName: "GPT-5", InputPerMillion: "1.25", OutputPerMillion: "10", CacheReadPerMillion: "0.125", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-5-codex", DisplayName: "GPT-5 Codex", InputPerMillion: "1.25", OutputPerMillion: "10", CacheReadPerMillion: "0.125", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-5-mini", DisplayName: "GPT-5 Mini", InputPerMillion: "0.25", OutputPerMillion: "2", CacheReadPerMillion: "0.025", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-5-nano", DisplayName: "GPT-5 Nano", InputPerMillion: "0.05", OutputPerMillion: "0.40", CacheReadPerMillion: "0.005", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "o3", DisplayName: "OpenAI o3", InputPerMillion: "2", OutputPerMillion: "8", CacheReadPerMillion: "0.50", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "o3-pro", DisplayName: "OpenAI o3-pro", InputPerMillion: "20", OutputPerMillion: "80", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "o3-mini", DisplayName: "OpenAI o3-mini", InputPerMillion: "0.55", OutputPerMillion: "2.20", CacheReadPerMillion: "0.55", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "o4-mini", DisplayName: "OpenAI o4-mini", InputPerMillion: "1.10", OutputPerMillion: "4.40", CacheReadPerMillion: "0.275", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "o1", DisplayName: "OpenAI o1", InputPerMillion: "15", OutputPerMillion: "60", CacheReadPerMillion: "7.50", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "o1-mini", DisplayName: "OpenAI o1-mini", InputPerMillion: "0.55", OutputPerMillion: "2.20", CacheReadPerMillion: "0.55", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-4.1", DisplayName: "GPT-4.1", InputPerMillion: "2", OutputPerMillion: "8", CacheReadPerMillion: "0.50", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-4.1-mini", DisplayName: "GPT-4.1 Mini", InputPerMillion: "0.40", OutputPerMillion: "1.60", CacheReadPerMillion: "0.10", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "gpt-4.1-nano", DisplayName: "GPT-4.1 Nano", InputPerMillion: "0.10", OutputPerMillion: "0.40", CacheReadPerMillion: "0.025", InputTokenSemantics: InputSemanticsTotal},
	{ModelID: "codex-mini", DisplayName: "Codex Mini", InputPerMillion: "0.75", OutputPerMillion: "3", CacheReadPerMillion: "0.025", InputTokenSemantics: InputSemanticsTotal},
	// Google Gemini —— legacy 口径
	{ModelID: "gemini-3.5-flash", DisplayName: "Gemini 3.5 Flash", InputPerMillion: "1.50", OutputPerMillion: "9.00", CacheReadPerMillion: "0.15"},
	{ModelID: "gemini-3.1-pro-preview", DisplayName: "Gemini 3.1 Pro Preview", InputPerMillion: "2", OutputPerMillion: "12", CacheReadPerMillion: "0.20"},
	{ModelID: "gemini-3.1-flash-lite", DisplayName: "Gemini 3.1 Flash Lite", InputPerMillion: "0.25", OutputPerMillion: "1.50", CacheReadPerMillion: "0.025"},
	{ModelID: "gemini-3-pro-preview", DisplayName: "Gemini 3 Pro Preview", InputPerMillion: "2", OutputPerMillion: "12", CacheReadPerMillion: "0.2"},
	{ModelID: "gemini-3-flash-preview", DisplayName: "Gemini 3 Flash Preview", InputPerMillion: "0.5", OutputPerMillion: "3", CacheReadPerMillion: "0.05"},
	{ModelID: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro", InputPerMillion: "1.25", OutputPerMillion: "10", CacheReadPerMillion: "0.125"},
	{ModelID: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash", InputPerMillion: "0.3", OutputPerMillion: "2.5", CacheReadPerMillion: "0.03"},
	{ModelID: "gemini-2.5-flash-lite", DisplayName: "Gemini 2.5 Flash Lite", InputPerMillion: "0.10", OutputPerMillion: "0.40", CacheReadPerMillion: "0.01"},
	{ModelID: "gemini-2.0-flash", DisplayName: "Gemini 2.0 Flash", InputPerMillion: "0.10", OutputPerMillion: "0.40", CacheReadPerMillion: "0.025"},
	// xAI Grok —— legacy 口径
	{ModelID: "grok-4.5", DisplayName: "Grok 4.5", InputPerMillion: "2", OutputPerMillion: "6", CacheReadPerMillion: "0.50"},
	{ModelID: "grok-4.3", DisplayName: "Grok 4.3", InputPerMillion: "1.25", OutputPerMillion: "2.50", CacheReadPerMillion: "0.20"},
	{ModelID: "grok-4", DisplayName: "Grok 4", InputPerMillion: "3", OutputPerMillion: "15", CacheReadPerMillion: "0.75"},
	{ModelID: "grok-3", DisplayName: "Grok 3", InputPerMillion: "3", OutputPerMillion: "15", CacheReadPerMillion: "0.75"},
	{ModelID: "grok-3-mini", DisplayName: "Grok 3 Mini", InputPerMillion: "0.25", OutputPerMillion: "0.50", CacheReadPerMillion: "0.075"},
	{ModelID: "grok-build-0.1", DisplayName: "Grok Build 0.1", InputPerMillion: "1", OutputPerMillion: "2", CacheReadPerMillion: "0.20"},
	// DeepSeek —— legacy 口径
	{ModelID: "deepseek-v3.2", DisplayName: "DeepSeek V3.2", InputPerMillion: "0.28", OutputPerMillion: "0.42", CacheReadPerMillion: "0.028"},
	{ModelID: "deepseek-v3.1", DisplayName: "DeepSeek V3.1", InputPerMillion: "0.55", OutputPerMillion: "1.67", CacheReadPerMillion: "0.055"},
	{ModelID: "deepseek-v3", DisplayName: "DeepSeek V3", InputPerMillion: "0.28", OutputPerMillion: "1.11", CacheReadPerMillion: "0.028"},
	{ModelID: "deepseek-chat", DisplayName: "DeepSeek Chat", InputPerMillion: "0.27", OutputPerMillion: "1.10", CacheReadPerMillion: "0.07"},
	{ModelID: "deepseek-reasoner", DisplayName: "DeepSeek Reasoner", InputPerMillion: "0.55", OutputPerMillion: "2.19", CacheReadPerMillion: "0.14"},
	{ModelID: "deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash", InputPerMillion: "0.14", OutputPerMillion: "0.28", CacheReadPerMillion: "0.0028"},
	{ModelID: "deepseek-v4-pro", DisplayName: "DeepSeek V4 Pro", InputPerMillion: "0.435", OutputPerMillion: "0.87", CacheReadPerMillion: "0.003625"},
	// Moonshot Kimi —— legacy 口径
	{ModelID: "kimi-k2-thinking", DisplayName: "Kimi K2 Thinking", InputPerMillion: "0.55", OutputPerMillion: "2.20", CacheReadPerMillion: "0.10"},
	{ModelID: "kimi-k2-0905", DisplayName: "Kimi K2", InputPerMillion: "0.55", OutputPerMillion: "2.20", CacheReadPerMillion: "0.10"},
	{ModelID: "kimi-k2-turbo", DisplayName: "Kimi K2 Turbo", InputPerMillion: "1.11", OutputPerMillion: "8.06", CacheReadPerMillion: "0.14"},
	{ModelID: "kimi-k2.5", DisplayName: "Kimi K2.5", InputPerMillion: "0.60", OutputPerMillion: "3.00", CacheReadPerMillion: "0.10"},
	{ModelID: "kimi-k2.6", DisplayName: "Kimi K2.6", InputPerMillion: "0.95", OutputPerMillion: "4.00", CacheReadPerMillion: "0.16"},
	{ModelID: "kimi-k2.7-code", DisplayName: "Kimi K2.7 Code", InputPerMillion: "0.95", OutputPerMillion: "4.00", CacheReadPerMillion: "0.19"},
	{ModelID: "kimi-k3", DisplayName: "Kimi K3", InputPerMillion: "3.00", OutputPerMillion: "15.00", CacheReadPerMillion: "0.30"},
	// ByteDance Doubao —— legacy 口径
	{ModelID: "doubao-seed-2-1-pro", DisplayName: "Doubao Seed 2.1 Pro", InputPerMillion: "0.84", OutputPerMillion: "4.2", CacheReadPerMillion: "0.17"},
	{ModelID: "doubao-seed-2-1-turbo", DisplayName: "Doubao Seed 2.1 Turbo", InputPerMillion: "0.42", OutputPerMillion: "2.1", CacheReadPerMillion: "0.08"},
	{ModelID: "doubao-seed-code", DisplayName: "Doubao Seed Code", InputPerMillion: "0.17", OutputPerMillion: "1.11", CacheReadPerMillion: "0.02"},
	{ModelID: "doubao-seed-2-0-pro", DisplayName: "Doubao Seed 2.0 Pro", InputPerMillion: "0.47", OutputPerMillion: "2.37", CacheReadPerMillion: "0.09"},
	{ModelID: "doubao-seed-2-0-code", DisplayName: "Doubao Seed 2.0 Code", InputPerMillion: "0.47", OutputPerMillion: "2.37", CacheReadPerMillion: "0.09"},
	{ModelID: "doubao-seed-2-0-lite", DisplayName: "Doubao Seed 2.0 Lite", InputPerMillion: "0.08", OutputPerMillion: "0.50", CacheReadPerMillion: "0.017"},
	{ModelID: "doubao-seed-2-0-mini", DisplayName: "Doubao Seed 2.0 Mini", InputPerMillion: "0.03", OutputPerMillion: "0.31", CacheReadPerMillion: "0.0056"},
	// Qwen —— legacy 口径
	{ModelID: "qwen3.7-max", DisplayName: "Qwen3.7 Max", InputPerMillion: "2.50", OutputPerMillion: "7.50", CacheReadPerMillion: "0.25"},
	{ModelID: "qwen3.7-plus", DisplayName: "Qwen3.7 Plus", InputPerMillion: "0.40", OutputPerMillion: "1.60", CacheReadPerMillion: "0.08"},
	{ModelID: "qwen3-max", DisplayName: "Qwen3 Max", InputPerMillion: "0.78", OutputPerMillion: "3.90"},
	{ModelID: "qwen3-235b-a22b", DisplayName: "Qwen3 235B-A22B", InputPerMillion: "0.70", OutputPerMillion: "8.40"},
	{ModelID: "qwen3-coder-plus", DisplayName: "Qwen3 Coder Plus", InputPerMillion: "0.65", OutputPerMillion: "3.25", CacheReadPerMillion: "0.13"},
	{ModelID: "qwen3-coder-480b", DisplayName: "Qwen3 Coder 480B", InputPerMillion: "0.65", OutputPerMillion: "3.25"},
	{ModelID: "qwen3-coder-flash", DisplayName: "Qwen3 Coder Flash", InputPerMillion: "0.195", OutputPerMillion: "0.975", CacheReadPerMillion: "0.039"},
	{ModelID: "qwq-plus", DisplayName: "QwQ Plus", InputPerMillion: "0.80", OutputPerMillion: "2.40"},
	{ModelID: "qwq-32b", DisplayName: "QwQ 32B", InputPerMillion: "0.20", OutputPerMillion: "0.60"},
	{ModelID: "qwen3-32b", DisplayName: "Qwen3 32B", InputPerMillion: "0.16", OutputPerMillion: "0.64"},
	// GLM —— legacy 口径
	{ModelID: "glm-4.7", DisplayName: "GLM-4.7", InputPerMillion: "0.6", OutputPerMillion: "2.2", CacheReadPerMillion: "0.11"},
	{ModelID: "glm-4.6", DisplayName: "GLM-4.6", InputPerMillion: "0.6", OutputPerMillion: "2.2", CacheReadPerMillion: "0.11"},
	{ModelID: "glm-5", DisplayName: "GLM-5", InputPerMillion: "1", OutputPerMillion: "3.2", CacheReadPerMillion: "0.2"},
	{ModelID: "glm-5.1", DisplayName: "GLM-5.1", InputPerMillion: "1.4", OutputPerMillion: "4.4", CacheReadPerMillion: "0.26"},
	{ModelID: "glm-5.2", DisplayName: "GLM-5.2", InputPerMillion: "1.4", OutputPerMillion: "4.4", CacheReadPerMillion: "0.26"},
	// MiniMax —— legacy 口径
	{ModelID: "minimax-m2.1", DisplayName: "MiniMax M2.1", InputPerMillion: "0.27", OutputPerMillion: "0.95", CacheReadPerMillion: "0.03"},
	{ModelID: "minimax-m2", DisplayName: "MiniMax M2", InputPerMillion: "0.27", OutputPerMillion: "0.95", CacheReadPerMillion: "0.03"},
	{ModelID: "minimax-m2.5", DisplayName: "MiniMax M2.5", InputPerMillion: "0.15", OutputPerMillion: "0.95", CacheReadPerMillion: "0.03"},
	{ModelID: "minimax-m3", DisplayName: "MiniMax M3", InputPerMillion: "0.60", OutputPerMillion: "2.40", CacheReadPerMillion: "0.12"},
	// Xiaomi MiMo —— legacy 口径
	{ModelID: "mimo-v2-flash", DisplayName: "MiMo V2 Flash", InputPerMillion: "0.09", OutputPerMillion: "0.29", CacheReadPerMillion: "0.009"},
	{ModelID: "mimo-v2-pro", DisplayName: "MiMo V2 Pro", InputPerMillion: "0.435", OutputPerMillion: "0.87", CacheReadPerMillion: "0.0036"},
	{ModelID: "mimo-v2.5", DisplayName: "MiMo V2.5", InputPerMillion: "0.14", OutputPerMillion: "0.29", CacheReadPerMillion: "0.0028"},
	{ModelID: "mimo-v2.5-pro", DisplayName: "MiMo V2.5 Pro", InputPerMillion: "0.435", OutputPerMillion: "0.87", CacheReadPerMillion: "0.0036"},
	// Step / Tencent Hunyuan / Mistral / Cohere —— legacy 口径
	{ModelID: "step-3.7-flash", DisplayName: "Step 3.7 Flash", InputPerMillion: "0.19", OutputPerMillion: "1.13", CacheReadPerMillion: "0.04"},
	{ModelID: "step-3.5-flash", DisplayName: "Step 3.5 Flash", InputPerMillion: "0.10", OutputPerMillion: "0.30", CacheReadPerMillion: "0.02"},
	{ModelID: "hunyuan-hy3", DisplayName: "Hunyuan Hy3", InputPerMillion: "0.14", OutputPerMillion: "0.56", CacheReadPerMillion: "0.035"},
	{ModelID: "mistral-medium-3.5", DisplayName: "Mistral Medium 3.5", InputPerMillion: "1.50", OutputPerMillion: "7.50"},
	{ModelID: "mistral-small-4", DisplayName: "Mistral Small 4", InputPerMillion: "0.10", OutputPerMillion: "0.30", CacheReadPerMillion: "0.01"},
	{ModelID: "mistral-large-3-2512", DisplayName: "Mistral Large 3", InputPerMillion: "0.50", OutputPerMillion: "1.50", CacheReadPerMillion: "0.05"},
	{ModelID: "codestral-2508", DisplayName: "Codestral", InputPerMillion: "0.30", OutputPerMillion: "0.90", CacheReadPerMillion: "0.03"},
	{ModelID: "command-a", DisplayName: "Cohere Command A", InputPerMillion: "2.50", OutputPerMillion: "10"},
	{ModelID: "command-r-plus", DisplayName: "Cohere Command R+", InputPerMillion: "2.50", OutputPerMillion: "10"},
	{ModelID: "command-r", DisplayName: "Cohere Command R", InputPerMillion: "0.15", OutputPerMillion: "0.60"},
}

// normalizePricingConfig 归一化定价配置：补默认倍率、按 seed 增量补齐缺失的内置模型。
// 用户已存在同 modelId 的记录不覆盖（INSERT OR IGNORE 语义）。
func normalizePricingConfig(input PricingConfig) PricingConfig {
	output := PricingConfig{
		DefaultCostMultiplier: strings.TrimSpace(input.DefaultCostMultiplier),
		Models:                input.Models,
	}
	if output.DefaultCostMultiplier == "" {
		output.DefaultCostMultiplier = "1"
	}
	if output.Models == nil {
		output.Models = []ModelPricing{}
	}
	existing := make(map[string]struct{}, len(output.Models))
	for _, m := range output.Models {
		existing[normalizePricingModelID(m.ModelID)] = struct{}{}
	}
	for _, seed := range pricingModelSeed {
		if _, ok := existing[normalizePricingModelID(seed.ModelID)]; !ok {
			output.Models = append(output.Models, seed)
			existing[normalizePricingModelID(seed.ModelID)] = struct{}{}
		}
	}
	return output
}

// normalizePricingModelID 归一化 model id 用于查找匹配：小写、去空白。
// 比对时用归一化后的 id，使 "Claude-Opus-4-7" 与 "claude-opus-4-7" 命中同一价目。
func normalizePricingModelID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// pricingNamespaceRe 匹配 provider 命名空间前缀（openai./anthropic./google./meta./...）。
// cc-switch 的 model_pricing_candidates 第一步就是剥掉它，让 "openai.gpt-5" 命中 "gpt-5"。
var pricingNamespaceRe = regexp.MustCompile(`^(openai|anthropic|google|meta|mistral|cohere|xai|bytedance|alibaba|moonshot|deepseek|zhipu|minimax|xiaomi|step|tencent|baichuan|yi|01-ai|nvidia|amazon|microsoft)\.`)

// pricingDateSuffixRe 匹配末尾的日期后缀（如 -20251101、-20251114），剥掉后让带日期的
// 快照 id（"claude-sonnet-4-6-20251114"）命中无日期的基线 id（"claude-sonnet-4-6"）。
var pricingDateSuffixRe = regexp.MustCompile(`-\d{6,8}$`)

// pricingVersionRe 匹配 -vN 版本后缀（-v1/-v2），剥掉让 "model-v2" 命中 "model"。
var pricingVersionRe = regexp.MustCompile(`-v\d+$`)

// pricingEffortSuffixRe 匹配 OpenAI Responses 推理努力后缀（-low/-medium/-high），
// 剥掉让 "o3-mini-high" 命中 "o3-mini"。
var pricingEffortSuffixRe = regexp.MustCompile(`-(low|medium|high)$`)

// modelPricingCandidates 生成查找候选列表：从原始 id 派生一组逐步宽松的归一化 id。
// 移植自 cc-switch services/model_pricing_candidates 的 candidates 生成逻辑。
// 关键：各变换（去命名空间/版本/日期/努力后缀/前缀回退）需相互组合，而非只对原始值各做一次。
// 否则 "openai.claude-opus-4-6-20251114" 去命名空间后是 "claude-opus-4-6-20251114"，
// 但去日期只对原始值做会得到 "openai.claude-opus-4-6"，永远凑不出 "claude-opus-4-6"。
// 这里用工作表把每一步的中间结果都再喂给后续变换，保证组合覆盖。
func modelPricingCandidates(modelID string) []string {
	raw := strings.ToLower(strings.TrimSpace(modelID))
	if raw == "" {
		return nil
	}
	seen := make(map[string]struct{})
	candidates := make([]string, 0, 12)
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		candidates = append(candidates, s)
	}
	add(raw)

	// transforms 是依次施加的变换链；每一步的输出都并入工作集，供后续步骤继续变换。
	transforms := []func(string) string{
		// 1) 去命名空间前缀（openai.gpt-5 → gpt-5）
		func(s string) string {
			if loc := pricingNamespaceRe.FindStringIndex(s); loc != nil {
				return s[loc[1]:]
			}
			return s
		},
		// 2) 去版本后缀（model-v2 → model）
		func(s string) string {
			return pricingVersionRe.ReplaceAllString(s, "")
		},
		// 3) 去日期后缀（claude-sonnet-4-6-20251114 → claude-sonnet-4-6）
		func(s string) string {
			return pricingDateSuffixRe.ReplaceAllString(s, "")
		},
		// 4) 去推理努力后缀（o3-mini-high → o3-mini）
		func(s string) string {
			return pricingEffortSuffixRe.ReplaceAllString(s, "")
		},
	}

	// 工作集：每施加一个变换，把新结果加入并作为后续变换的输入。
	work := []string{raw}
	for _, tf := range transforms {
		next := make([]string, 0, len(work))
		for _, s := range work {
			t := tf(s)
			if t != s {
				add(t)
				next = append(next, t)
			} else {
				next = append(next, s)
			}
		}
		// 保留变换后的集合继续下一步；未变的也留着以便其它变换作用。
		work = append(work, next...)
		// 去重 work，避免组合爆炸。
		seenWork := make(map[string]struct{}, len(work))
		dedup := work[:0]
		for _, s := range work {
			if _, ok := seenWork[s]; ok {
				continue
			}
			seenWork[s] = struct{}{}
			dedup = append(dedup, s)
		}
		work = dedup
	}

	// 5) 前缀回退：对去后缀后的每个候选，把 - 分隔的最后一段砍掉，逐步试更短前缀。
	//    命中家族通用价目（如某 provider 把整个 claude-opus-4 系列定同价）。
	prefixSources := append([]string{}, work...)
	for _, src := range prefixSources {
		segments := strings.Split(src, "-")
		for i := len(segments) - 1; i > 0; i-- {
			add(strings.Join(segments[:i], "-"))
		}
	}
	return candidates
}

// MatchPricingCandidates 是 modelPricingCandidates 的导出别名，
// 供其它包（如 fetch-models 的上下文窗口回填）复用同一套候选派生逻辑。
func MatchPricingCandidates(modelID string) []string {
	return modelPricingCandidates(modelID)
}

// MatchModelPricing 用候选匹配查定价：依次试 modelPricingCandidates 派生的每个候选 id，
// 命中第一个即返回。比旧的 lowercase+trim 严格相等更宽松，能匹配带命名空间/版本/日期/努力后缀的变体。
// 未命中返回 nil。
func (p *PricingConfig) MatchModelPricing(modelID string) *ModelPricing {
	if p == nil {
		return nil
	}
	candidates := modelPricingCandidates(modelID)
	if len(candidates) == 0 {
		return nil
	}
	// 预计算每个定价记录的归一化 id，避免在候选循环里重复 ToLower。
	index := make(map[string]int, len(p.Models))
	for i := range p.Models {
		key := normalizePricingModelID(p.Models[i].ModelID)
		if _, ok := index[key]; !ok {
			index[key] = i
		}
	}
	for _, c := range candidates {
		if i, ok := index[c]; ok {
			return &p.Models[i]
		}
	}
	return nil
}

// FindModelPricing 是 MatchModelPricing 的别名，保留向后兼容旧调用点。
// 行为已升级为候选匹配（原实现仅 lowercase+trim 严格相等）。
func (p *PricingConfig) FindModelPricing(modelID string) *ModelPricing {
	return p.MatchModelPricing(modelID)
}
