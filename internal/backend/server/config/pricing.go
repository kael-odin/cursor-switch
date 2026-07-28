package config

import (
	"regexp"
	"strconv"
	"strings"
)

// InputTokenSemantics 描述一家 provider 的 input token 计费口径。
// 不同 provider 对「input token 是否包含缓存部分」计费方式不同，cc-switch 把它分成三类：
//   - FRESH：input 按原样计费（缓存部分单独按 cache_read/cache_write 价计）。
//   - TOTAL：input 已包含 cache_read + cache_creation，需从 input 里减掉这两部分再按 input 价计费，
//     否则会把缓存部分既按 input 价又按 cache 价重复计费。
//   - legacy（默认）：input 已包含 cache_read，需从 input 里减掉 cache_read 再按 input 价计费。
//
// 关键：这是「落盘 usage.json 的 input 字段」的口径，不是 provider API 原始 usage 的口径。
// cursor-switch 的 adapter 在落盘前已统一把 input 折算成 fresh_input（Anthropic 原样存，OpenAI 存
// prompt-cached），所以内置 seed（pricingModelSeed）全部标 FRESH 与落盘口径一致。TOTAL/legacy
// 仅留给用户自定义模型——若用户接的 provider 透传原始 usage 未被 adapter 折算（罕见），可手动标 TOTAL/legacy。
//
// 参考 cc-switch usage_stats.rs 的 input_token_semantics（cc-switch 在 SQL 数据层做 fresh_input 归一化，
// cursor-switch 在 adapter 层做等价归一化）。
type InputTokenSemantics string

const (
	InputSemanticsLegacy InputTokenSemantics = ""       // legacy：input 含 cache_read，减 cache_read
	InputSemanticsFresh  InputTokenSemantics = "FRESH" // FRESH：input 原样计费
	InputSemanticsTotal  InputTokenSemantics = "TOTAL" // TOTAL：input 含 cache_read+cache_creation，减两者
)

// NormalizeInputTokenSemantics 把任意输入归一为合法语义值。
// 空串/大小写变体/已知值原样归一；未知值回落 legacy（与计算层 switch default 行为一致，向后兼容旧配置）。
// 供 F-16 在 pricing 更新时显式校验：避免任意字符串写入配置后在序列化/前端展示中暴露脏值。
func NormalizeInputTokenSemantics(value string) InputTokenSemantics {
	switch InputTokenSemantics(strings.ToUpper(strings.TrimSpace(value))) {
	case InputSemanticsFresh, InputSemanticsTotal:
		// 已知非空语义——保留 trim 后的大写形式。
		trimmed := strings.ToUpper(strings.TrimSpace(value))
		return InputTokenSemantics(trimmed)
	default:
		return InputSemanticsLegacy
	}
}

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
	// IsBuiltin 标记该记录是否来自内置 seed 价目表（pricingModelSeed）。
	// normalizePricingConfig 给 seed 补齐的记录打 true；用户自定义/编辑新增的为 false。
	// F-17：内置记录不物理删除，只设 Disabled=true（tombstone），避免 seed 在下次 normalize 时补回。
	IsBuiltin bool `json:"isBuiltin,omitempty" yaml:"isBuiltin,omitempty"`
	// Disabled 是内置记录的"逻辑删除"标记（tombstone）。
	// F-17：用户在前端"删除"内置模型时实际置 true；normalize 不再补回该 modelId 的 seed；
	// 成本计算按零价（成本 0）处理，与 UI 承诺"删除后按 0 计算"对齐。仅对内置记录有意义。
	// "恢复默认价"清除此标记并重置为 seed 值。自定义记录用物理删除，不走此字段。
	Disabled bool `json:"disabled,omitempty" yaml:"disabled,omitempty"`
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
// InputTokenSemantics 标注规则（按 adapter 实际存储口径，非 provider 原始 usage 口径）：
// cursor-switch 的所有 adapter 在落盘 usage.json 前已把 input 折算成 fresh_input：
//   - Anthropic adapter（anthropic.go applyUsage）原样存 api 返回的 input_tokens，
//     而 Anthropic API 的 input_tokens 本就只算非缓存部分（= fresh），故落盘即 fresh。
//   - OpenAI adapter（openai.go applyUsage，Chat/Responses 两处）存 prompt_tokens - cached_tokens，
//     即从含缓存的 prompt 里减掉 cached_tokens 得 fresh。
// 所有 OpenAI 兼容 provider（DeepSeek/Gemini/Grok/Kimi/Qwen/GLM/MiniMax/MiMo/Doubao/…）
// 都经 openai.go 这一条路，落盘 input 一律是 fresh。Anthropic 走 anthropic.go 也是 fresh。
//
// 故 seed 统一标 FRESH——与落盘口径一致，billableInputTokens 走 FRESH 分支原样返回 input，
// 不再对 cache 做减法（避免重复扣 cache 导致成本低估）；isCalibrationAnomaly 走 FRESH 分支
// 恒返回 false（fresh input 无「未正确包含缓存」概念），消除 487 条误报。
// 这等价于 cc-switch 在 SQL 数据层把所有 provider 归一到 fresh_input 的做法。
// （legacy/TOTAL 语义仍保留在计算层供用户自定义模型按其 provider 原始口径标注，只是内置 seed 不再用。）
var pricingModelSeed = []ModelPricing{
	// Anthropic Claude —— adapter 落盘 fresh input（FRESH）
	{ModelID: "claude-fable-5", DisplayName: "Claude Fable 5", InputPerMillion: "10", OutputPerMillion: "50", CacheReadPerMillion: "1.00", CacheWritePerMillion: "12.50", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "claude-mythos-5", DisplayName: "Claude Mythos 5", InputPerMillion: "10", OutputPerMillion: "50", CacheReadPerMillion: "1.00", CacheWritePerMillion: "12.50", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "claude-opus-5", DisplayName: "Claude Opus 5", InputPerMillion: "5", OutputPerMillion: "25", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "claude-opus-4-8", DisplayName: "Claude Opus 4.8", InputPerMillion: "5", OutputPerMillion: "25", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "claude-sonnet-5", DisplayName: "Claude Sonnet 5", InputPerMillion: "3", OutputPerMillion: "15", CacheReadPerMillion: "0.30", CacheWritePerMillion: "3.75", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "claude-opus-4-7", DisplayName: "Claude Opus 4.7", InputPerMillion: "5", OutputPerMillion: "25", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "claude-opus-4-6", DisplayName: "Claude Opus 4.6", InputPerMillion: "5", OutputPerMillion: "25", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "claude-opus-4-6-20260206", DisplayName: "Claude Opus 4.6", InputPerMillion: "5", OutputPerMillion: "25", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6", InputPerMillion: "3", OutputPerMillion: "15", CacheReadPerMillion: "0.30", CacheWritePerMillion: "3.75", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "claude-sonnet-4-6-20260217", DisplayName: "Claude Sonnet 4.6", InputPerMillion: "3", OutputPerMillion: "15", CacheReadPerMillion: "0.30", CacheWritePerMillion: "3.75", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "claude-opus-4-5-20251101", DisplayName: "Claude Opus 4.5", InputPerMillion: "5", OutputPerMillion: "25", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "claude-sonnet-4-5-20250929", DisplayName: "Claude Sonnet 4.5", InputPerMillion: "3", OutputPerMillion: "15", CacheReadPerMillion: "0.30", CacheWritePerMillion: "3.75", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "claude-haiku-4-5-20251001", DisplayName: "Claude Haiku 4.5", InputPerMillion: "1", OutputPerMillion: "5", CacheReadPerMillion: "0.10", CacheWritePerMillion: "1.25", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "claude-opus-4-20250514", DisplayName: "Claude Opus 4", InputPerMillion: "15", OutputPerMillion: "75", CacheReadPerMillion: "1.50", CacheWritePerMillion: "18.75", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "claude-opus-4-1-20250805", DisplayName: "Claude Opus 4.1", InputPerMillion: "15", OutputPerMillion: "75", CacheReadPerMillion: "1.50", CacheWritePerMillion: "18.75", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "claude-sonnet-4-20250514", DisplayName: "Claude Sonnet 4", InputPerMillion: "3", OutputPerMillion: "15", CacheReadPerMillion: "0.30", CacheWritePerMillion: "3.75", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "claude-3-5-haiku-20241022", DisplayName: "Claude 3.5 Haiku", InputPerMillion: "0.80", OutputPerMillion: "4", CacheReadPerMillion: "0.08", CacheWritePerMillion: "1", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "claude-3-5-sonnet-20241022", DisplayName: "Claude 3.5 Sonnet", InputPerMillion: "3", OutputPerMillion: "15", CacheReadPerMillion: "0.30", CacheWritePerMillion: "3.75", InputTokenSemantics: InputSemanticsFresh},
	// OpenAI GPT —— adapter 落盘 fresh input（prompt - cached，FRESH）
	{ModelID: "gpt-5.6-sol", DisplayName: "GPT-5.6 Sol", InputPerMillion: "5", OutputPerMillion: "30", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5.6-terra", DisplayName: "GPT-5.6 Terra", InputPerMillion: "2.50", OutputPerMillion: "15", CacheReadPerMillion: "0.25", CacheWritePerMillion: "3.125", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5.6-luna", DisplayName: "GPT-5.6 Luna", InputPerMillion: "1", OutputPerMillion: "6", CacheReadPerMillion: "0.10", CacheWritePerMillion: "1.25", InputTokenSemantics: InputSemanticsFresh},
	// GPT-5.6 裸名 + effort 后缀别名（cc-switch v3.18.0）：裸 gpt-5.6 是 sol 的官方别名；
	// effort 后缀对齐 gpt-5.5 系列记账形态，价目同 sol。7 段剥离会把 gpt-5.6-high 剥到 gpt-5.6,
	// 但表里需有裸名兜底，否则 miss。
	{ModelID: "gpt-5.6", DisplayName: "GPT-5.6 Sol", InputPerMillion: "5", OutputPerMillion: "30", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5.6-low", DisplayName: "GPT-5.6 Sol", InputPerMillion: "5", OutputPerMillion: "30", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5.6-medium", DisplayName: "GPT-5.6 Sol", InputPerMillion: "5", OutputPerMillion: "30", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5.6-high", DisplayName: "GPT-5.6 Sol", InputPerMillion: "5", OutputPerMillion: "30", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5.6-xhigh", DisplayName: "GPT-5.6 Sol", InputPerMillion: "5", OutputPerMillion: "30", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5.6-minimal", DisplayName: "GPT-5.6 Sol", InputPerMillion: "5", OutputPerMillion: "30", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5.5", DisplayName: "GPT-5.5", InputPerMillion: "5", OutputPerMillion: "30", CacheReadPerMillion: "0.50", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5.4", DisplayName: "GPT-5.4", InputPerMillion: "2.50", OutputPerMillion: "15", CacheReadPerMillion: "0.25", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5.4-mini", DisplayName: "GPT-5.4 Mini", InputPerMillion: "0.75", OutputPerMillion: "4.50", CacheReadPerMillion: "0.075", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5.4-nano", DisplayName: "GPT-5.4 Nano", InputPerMillion: "0.20", OutputPerMillion: "1.25", CacheReadPerMillion: "0.02", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5.2", DisplayName: "GPT-5.2", InputPerMillion: "1.75", OutputPerMillion: "14", CacheReadPerMillion: "0.175", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5.2-codex", DisplayName: "GPT-5.2 Codex", InputPerMillion: "1.75", OutputPerMillion: "14", CacheReadPerMillion: "0.175", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5.3-codex", DisplayName: "GPT-5.3 Codex", InputPerMillion: "1.75", OutputPerMillion: "14", CacheReadPerMillion: "0.175", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5.1", DisplayName: "GPT-5.1", InputPerMillion: "1.25", OutputPerMillion: "10", CacheReadPerMillion: "0.125", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5.1-codex", DisplayName: "GPT-5.1 Codex", InputPerMillion: "1.25", OutputPerMillion: "10", CacheReadPerMillion: "0.125", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5", DisplayName: "GPT-5", InputPerMillion: "1.25", OutputPerMillion: "10", CacheReadPerMillion: "0.125", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5-codex", DisplayName: "GPT-5 Codex", InputPerMillion: "1.25", OutputPerMillion: "10", CacheReadPerMillion: "0.125", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5-mini", DisplayName: "GPT-5 Mini", InputPerMillion: "0.25", OutputPerMillion: "2", CacheReadPerMillion: "0.025", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-5-nano", DisplayName: "GPT-5 Nano", InputPerMillion: "0.05", OutputPerMillion: "0.40", CacheReadPerMillion: "0.005", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "o3", DisplayName: "OpenAI o3", InputPerMillion: "2", OutputPerMillion: "8", CacheReadPerMillion: "0.50", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "o3-pro", DisplayName: "OpenAI o3-pro", InputPerMillion: "20", OutputPerMillion: "80", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "o3-mini", DisplayName: "OpenAI o3-mini", InputPerMillion: "0.55", OutputPerMillion: "2.20", CacheReadPerMillion: "0.55", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "o4-mini", DisplayName: "OpenAI o4-mini", InputPerMillion: "1.10", OutputPerMillion: "4.40", CacheReadPerMillion: "0.275", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "o1", DisplayName: "OpenAI o1", InputPerMillion: "15", OutputPerMillion: "60", CacheReadPerMillion: "7.50", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "o1-mini", DisplayName: "OpenAI o1-mini", InputPerMillion: "0.55", OutputPerMillion: "2.20", CacheReadPerMillion: "0.55", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-4.1", DisplayName: "GPT-4.1", InputPerMillion: "2", OutputPerMillion: "8", CacheReadPerMillion: "0.50", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-4.1-mini", DisplayName: "GPT-4.1 Mini", InputPerMillion: "0.40", OutputPerMillion: "1.60", CacheReadPerMillion: "0.10", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gpt-4.1-nano", DisplayName: "GPT-4.1 Nano", InputPerMillion: "0.10", OutputPerMillion: "0.40", CacheReadPerMillion: "0.025", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "codex-mini", DisplayName: "Codex Mini", InputPerMillion: "0.75", OutputPerMillion: "3", CacheReadPerMillion: "0.025", InputTokenSemantics: InputSemanticsFresh},
	// Google Gemini —— adapter 落盘 fresh input（FRESH）
	{ModelID: "gemini-3.5-flash", DisplayName: "Gemini 3.5 Flash", InputPerMillion: "1.50", OutputPerMillion: "9.00", CacheReadPerMillion: "0.15", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gemini-3.1-pro-preview", DisplayName: "Gemini 3.1 Pro Preview", InputPerMillion: "2", OutputPerMillion: "12", CacheReadPerMillion: "0.20", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gemini-3.1-flash-lite", DisplayName: "Gemini 3.1 Flash Lite", InputPerMillion: "0.25", OutputPerMillion: "1.50", CacheReadPerMillion: "0.025", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gemini-3-pro-preview", DisplayName: "Gemini 3 Pro Preview", InputPerMillion: "2", OutputPerMillion: "12", CacheReadPerMillion: "0.2", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gemini-3-flash-preview", DisplayName: "Gemini 3 Flash Preview", InputPerMillion: "0.5", OutputPerMillion: "3", CacheReadPerMillion: "0.05", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro", InputPerMillion: "1.25", OutputPerMillion: "10", CacheReadPerMillion: "0.125", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash", InputPerMillion: "0.3", OutputPerMillion: "2.5", CacheReadPerMillion: "0.03", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gemini-2.5-flash-lite", DisplayName: "Gemini 2.5 Flash Lite", InputPerMillion: "0.10", OutputPerMillion: "0.40", CacheReadPerMillion: "0.01", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "gemini-2.0-flash", DisplayName: "Gemini 2.0 Flash", InputPerMillion: "0.10", OutputPerMillion: "0.40", CacheReadPerMillion: "0.025", InputTokenSemantics: InputSemanticsFresh},
	// xAI Grok —— adapter 落盘 fresh input（FRESH）
	{ModelID: "grok-4.5", DisplayName: "Grok 4.5", InputPerMillion: "2", OutputPerMillion: "6", CacheReadPerMillion: "0.50", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "grok-4.3", DisplayName: "Grok 4.3", InputPerMillion: "1.25", OutputPerMillion: "2.50", CacheReadPerMillion: "0.20", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "grok-4", DisplayName: "Grok 4", InputPerMillion: "3", OutputPerMillion: "15", CacheReadPerMillion: "0.75", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "grok-3", DisplayName: "Grok 3", InputPerMillion: "3", OutputPerMillion: "15", CacheReadPerMillion: "0.75", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "grok-3-mini", DisplayName: "Grok 3 Mini", InputPerMillion: "0.25", OutputPerMillion: "0.50", CacheReadPerMillion: "0.075", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "grok-build-0.1", DisplayName: "Grok Build 0.1", InputPerMillion: "1", OutputPerMillion: "2", CacheReadPerMillion: "0.20", InputTokenSemantics: InputSemanticsFresh},
	// DeepSeek —— adapter 落盘 fresh input（FRESH）
	{ModelID: "deepseek-v3.2", DisplayName: "DeepSeek V3.2", InputPerMillion: "0.28", OutputPerMillion: "0.42", CacheReadPerMillion: "0.028", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "deepseek-v3.1", DisplayName: "DeepSeek V3.1", InputPerMillion: "0.55", OutputPerMillion: "1.67", CacheReadPerMillion: "0.055", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "deepseek-v3", DisplayName: "DeepSeek V3", InputPerMillion: "0.28", OutputPerMillion: "1.11", CacheReadPerMillion: "0.028", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "deepseek-chat", DisplayName: "DeepSeek Chat", InputPerMillion: "0.27", OutputPerMillion: "1.10", CacheReadPerMillion: "0.07", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "deepseek-reasoner", DisplayName: "DeepSeek Reasoner", InputPerMillion: "0.55", OutputPerMillion: "2.19", CacheReadPerMillion: "0.14", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash", InputPerMillion: "0.14", OutputPerMillion: "0.28", CacheReadPerMillion: "0.0028", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "deepseek-v4-pro", DisplayName: "DeepSeek V4 Pro", InputPerMillion: "0.435", OutputPerMillion: "0.87", CacheReadPerMillion: "0.003625", InputTokenSemantics: InputSemanticsFresh},
	// Moonshot Kimi —— adapter 落盘 fresh input（FRESH）
	{ModelID: "kimi-k2-thinking", DisplayName: "Kimi K2 Thinking", InputPerMillion: "0.55", OutputPerMillion: "2.20", CacheReadPerMillion: "0.10", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "kimi-k2-0905", DisplayName: "Kimi K2", InputPerMillion: "0.55", OutputPerMillion: "2.20", CacheReadPerMillion: "0.10", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "kimi-k2-turbo", DisplayName: "Kimi K2 Turbo", InputPerMillion: "1.11", OutputPerMillion: "8.06", CacheReadPerMillion: "0.14", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "kimi-k2.5", DisplayName: "Kimi K2.5", InputPerMillion: "0.60", OutputPerMillion: "3.00", CacheReadPerMillion: "0.10", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "kimi-k2.6", DisplayName: "Kimi K2.6", InputPerMillion: "0.95", OutputPerMillion: "4.00", CacheReadPerMillion: "0.16", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "kimi-k2.7-code", DisplayName: "Kimi K2.7 Code", InputPerMillion: "0.95", OutputPerMillion: "4.00", CacheReadPerMillion: "0.19", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "kimi-k3", DisplayName: "Kimi K3", InputPerMillion: "3.00", OutputPerMillion: "15.00", CacheReadPerMillion: "0.30", InputTokenSemantics: InputSemanticsFresh},
	// ByteDance Doubao —— adapter 落盘 fresh input（FRESH）
	{ModelID: "doubao-seed-2-1-pro", DisplayName: "Doubao Seed 2.1 Pro", InputPerMillion: "0.84", OutputPerMillion: "4.2", CacheReadPerMillion: "0.17", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "doubao-seed-2-1-turbo", DisplayName: "Doubao Seed 2.1 Turbo", InputPerMillion: "0.42", OutputPerMillion: "2.1", CacheReadPerMillion: "0.08", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "doubao-seed-code", DisplayName: "Doubao Seed Code", InputPerMillion: "0.17", OutputPerMillion: "1.11", CacheReadPerMillion: "0.02", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "doubao-seed-2-0-pro", DisplayName: "Doubao Seed 2.0 Pro", InputPerMillion: "0.47", OutputPerMillion: "2.37", CacheReadPerMillion: "0.09", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "doubao-seed-2-0-code", DisplayName: "Doubao Seed 2.0 Code", InputPerMillion: "0.47", OutputPerMillion: "2.37", CacheReadPerMillion: "0.09", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "doubao-seed-2-0-lite", DisplayName: "Doubao Seed 2.0 Lite", InputPerMillion: "0.08", OutputPerMillion: "0.50", CacheReadPerMillion: "0.017", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "doubao-seed-2-0-mini", DisplayName: "Doubao Seed 2.0 Mini", InputPerMillion: "0.03", OutputPerMillion: "0.31", CacheReadPerMillion: "0.0056", InputTokenSemantics: InputSemanticsFresh},
	// Qwen —— adapter 落盘 fresh input（FRESH）
	{ModelID: "qwen3.7-max", DisplayName: "Qwen3.7 Max", InputPerMillion: "2.50", OutputPerMillion: "7.50", CacheReadPerMillion: "0.25", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "qwen3.7-plus", DisplayName: "Qwen3.7 Plus", InputPerMillion: "0.40", OutputPerMillion: "1.60", CacheReadPerMillion: "0.08", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "qwen3-max", DisplayName: "Qwen3 Max", InputPerMillion: "0.78", OutputPerMillion: "3.90", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "qwen3-235b-a22b", DisplayName: "Qwen3 235B-A22B", InputPerMillion: "0.70", OutputPerMillion: "8.40", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "qwen3-coder-plus", DisplayName: "Qwen3 Coder Plus", InputPerMillion: "0.65", OutputPerMillion: "3.25", CacheReadPerMillion: "0.13", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "qwen3-coder-480b", DisplayName: "Qwen3 Coder 480B", InputPerMillion: "0.65", OutputPerMillion: "3.25", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "qwen3-coder-flash", DisplayName: "Qwen3 Coder Flash", InputPerMillion: "0.195", OutputPerMillion: "0.975", CacheReadPerMillion: "0.039", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "qwq-plus", DisplayName: "QwQ Plus", InputPerMillion: "0.80", OutputPerMillion: "2.40", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "qwq-32b", DisplayName: "QwQ 32B", InputPerMillion: "0.20", OutputPerMillion: "0.60", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "qwen3-32b", DisplayName: "Qwen3 32B", InputPerMillion: "0.16", OutputPerMillion: "0.64", InputTokenSemantics: InputSemanticsFresh},
	// GLM —— adapter 落盘 fresh input（FRESH）
	{ModelID: "glm-4.7", DisplayName: "GLM-4.7", InputPerMillion: "0.6", OutputPerMillion: "2.2", CacheReadPerMillion: "0.11", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "glm-4.6", DisplayName: "GLM-4.6", InputPerMillion: "0.6", OutputPerMillion: "2.2", CacheReadPerMillion: "0.11", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "glm-5", DisplayName: "GLM-5", InputPerMillion: "1", OutputPerMillion: "3.2", CacheReadPerMillion: "0.2", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "glm-5.1", DisplayName: "GLM-5.1", InputPerMillion: "1.4", OutputPerMillion: "4.4", CacheReadPerMillion: "0.26", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "glm-5.2", DisplayName: "GLM-5.2", InputPerMillion: "1.4", OutputPerMillion: "4.4", CacheReadPerMillion: "0.26", InputTokenSemantics: InputSemanticsFresh},
	// MiniMax —— adapter 落盘 fresh input（FRESH）
	{ModelID: "minimax-m2.1", DisplayName: "MiniMax M2.1", InputPerMillion: "0.27", OutputPerMillion: "0.95", CacheReadPerMillion: "0.03", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "minimax-m2", DisplayName: "MiniMax M2", InputPerMillion: "0.27", OutputPerMillion: "0.95", CacheReadPerMillion: "0.03", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "minimax-m2.5", DisplayName: "MiniMax M2.5", InputPerMillion: "0.15", OutputPerMillion: "0.95", CacheReadPerMillion: "0.03", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "minimax-m3", DisplayName: "MiniMax M3", InputPerMillion: "0.60", OutputPerMillion: "2.40", CacheReadPerMillion: "0.12", InputTokenSemantics: InputSemanticsFresh},
	// Xiaomi MiMo —— adapter 落盘 fresh input（FRESH）
	{ModelID: "mimo-v2-flash", DisplayName: "MiMo V2 Flash", InputPerMillion: "0.09", OutputPerMillion: "0.29", CacheReadPerMillion: "0.009", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "mimo-v2-pro", DisplayName: "MiMo V2 Pro", InputPerMillion: "0.435", OutputPerMillion: "0.87", CacheReadPerMillion: "0.0036", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "mimo-v2.5", DisplayName: "MiMo V2.5", InputPerMillion: "0.14", OutputPerMillion: "0.29", CacheReadPerMillion: "0.0028", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "mimo-v2.5-pro", DisplayName: "MiMo V2.5 Pro", InputPerMillion: "0.435", OutputPerMillion: "0.87", CacheReadPerMillion: "0.0036", InputTokenSemantics: InputSemanticsFresh},
	// Step / Tencent Hunyuan / Mistral / Cohere —— adapter 落盘 fresh input（FRESH）
	{ModelID: "step-3.7-flash", DisplayName: "Step 3.7 Flash", InputPerMillion: "0.19", OutputPerMillion: "1.13", CacheReadPerMillion: "0.04", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "step-3.5-flash", DisplayName: "Step 3.5 Flash", InputPerMillion: "0.10", OutputPerMillion: "0.30", CacheReadPerMillion: "0.02", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "hunyuan-hy3", DisplayName: "Hunyuan Hy3", InputPerMillion: "0.14", OutputPerMillion: "0.56", CacheReadPerMillion: "0.035", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "mistral-medium-3.5", DisplayName: "Mistral Medium 3.5", InputPerMillion: "1.50", OutputPerMillion: "7.50", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "mistral-small-4", DisplayName: "Mistral Small 4", InputPerMillion: "0.10", OutputPerMillion: "0.30", CacheReadPerMillion: "0.01", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "mistral-large-3-2512", DisplayName: "Mistral Large 3", InputPerMillion: "0.50", OutputPerMillion: "1.50", CacheReadPerMillion: "0.05", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "codestral-2508", DisplayName: "Codestral", InputPerMillion: "0.30", OutputPerMillion: "0.90", CacheReadPerMillion: "0.03", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "command-a", DisplayName: "Cohere Command A", InputPerMillion: "2.50", OutputPerMillion: "10", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "command-r-plus", DisplayName: "Cohere Command R+", InputPerMillion: "2.50", OutputPerMillion: "10", InputTokenSemantics: InputSemanticsFresh},
	{ModelID: "command-r", DisplayName: "Cohere Command R", InputPerMillion: "0.15", OutputPerMillion: "0.60", InputTokenSemantics: InputSemanticsFresh},
}

// FindBuiltinSeed 按 modelId（大小写不敏感）查内置 seed 价目记录。
// 命中返回该 seed 的副本（IsBuiltin=true）+ true；未命中返回零值 + false。
// F-17：RestoreDefaultPricing 用此把用户编辑过/逻辑删除的内置记录重置回 seed 原值。
func FindBuiltinSeed(modelID string) (ModelPricing, bool) {
	normalized := normalizePricingModelID(modelID)
	for _, seed := range pricingModelSeed {
		if normalizePricingModelID(seed.ModelID) == normalized {
			seed.IsBuiltin = true
			return seed, true
		}
	}
	return ModelPricing{}, false
}

// normalizePricingConfig 归一化定价配置：补默认倍率、按 seed 增量补齐缺失的内置模型。
// 用户已存在同 modelId 的记录不覆盖（INSERT OR IGNORE 语义）。
// F-17：内置模型被用户"删除"后实际置 Disabled=true（tombstone），seed 不再补回该 modelId，
// 避免"删除→Save→normalize 补回"循环让删除失效。旧配置无 IsBuiltin 字段，命中 seed 的记录补 true。
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
	seedByID := make(map[string]ModelPricing, len(pricingModelSeed))
	for _, seed := range pricingModelSeed {
		seedByID[normalizePricingModelID(seed.ModelID)] = seed
	}
	// existing 记录每个 modelId 在用户配置中的出现状态：
	// - 真正存在（无论 enabled/disabled）→ seed 不补回（避免重复）；
	// - 内置记录的 IsBuiltin 字段在遍历时按"是否命中 seed"回填，兼容旧配置。
	existing := make(map[string]struct{}, len(output.Models))
	for i := range output.Models {
		m := &output.Models[i]
		id := normalizePricingModelID(m.ModelID)
		existing[id] = struct{}{}
		if seed, isSeed := seedByID[id]; isSeed {
			// 命中 seed 的记录标记为内置（旧配置无此字段时回填；用户编辑过的内置记录也保持内置）。
			m.IsBuiltin = true
			// 口径迁移：内置记录的 InputTokenSemantics 是 seed 事实（非用户定价编辑覆盖范围）。
			// 早期 seed 把 OpenAI 标 TOTAL、Anthropic 标 legacy，但 adapter 落盘的 input 一律是 fresh，
			// TOTAL/legacy 会让 billableInputTokens 重复扣 cache 导致成本低估 + isCalibrationAnomaly
			// 持续误报。此处把命中 seed 的内置记录统一迁移到 seed 当前语义（现全为 FRESH），
			// 用户编辑过的价格/倍率不受影响——只迁语义标签。
			if m.InputTokenSemantics != seed.InputTokenSemantics {
				m.InputTokenSemantics = seed.InputTokenSemantics
			}
		}
	}
	for _, seed := range pricingModelSeed {
		if _, ok := existing[normalizePricingModelID(seed.ModelID)]; !ok {
			seed.IsBuiltin = true
			output.Models = append(output.Models, seed)
		}
	}
	return output
}

// normalizePricingModelID 归一化 model id 用于查找匹配：小写、去空白。
// 比对时用归一化后的 id，使 "Claude-Opus-4-7" 与 "claude-opus-4-7" 命中同一价目。
func normalizePricingModelID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// pricingNamespaceMarkers 是已知 provider 命名空间前缀（openai./anthropic./google./...）。
// cc-switch 的 strip_known_model_namespace 第一步就是剥掉它，让 "openai.gpt-5" 命中 "gpt-5"。
var pricingNamespaceMarkers = []string{
	"openai.", "anthropic.", "google.", "moonshot.", "moonshotai.",
	"bedrock.", "global.", "meta.", "mistral.", "cohere.", "xai.",
	"bytedance.", "alibaba.", "deepseek.", "zhipu.", "minimax.",
	"xiaomi.", "step.", "tencent.", "baichuan.", "yi.", "01-ai.",
	"nvidia.", "amazon.", "microsoft.",
}

// claudeDesktopNonAnthropicMarkers 列出 Claude Desktop 网关给非 Anthropic 模型加的 "claude-" 前缀后缀。
// 如 "claude-gpt-5" 实际是 OpenAI GPT-5，应剥掉 "claude-" 命中 "gpt-5" 的价目。
// 仅当 "claude-" 后跟这些 marker 时才剥——否则 "claude-opus-4-7" 这种真 Anthropic 模型不能剥。
var claudeDesktopNonAnthropicMarkers = []string{
	"abab", "ark-code", "arctic", "astron", "codex", "command-r",
	"deepseek", "doubao", "ernie", "gemini", "gemma", "glm", "gpt",
	"grok", "hermes", "hy3", "hunyuan", "jamba", "kimi", "lfm",
	"llama", "longcat", "mercury", "mimo", "minimax", "mistral",
	"mixtral", "moonshot", "nemotron", "nova-", "openai", "qianfan",
	"qwen", "seed-", "solar", "stepfun",
}

// pricingDateSuffixRe 匹配末尾 8 位 YYYYMMDD 日期后缀（如 -20251114）。
// 6 位 YYMMDD 和 ISO -YYYY-MM-DD 由 stripModelDateSuffix 单独做月日校验剥离，
// 避免误伤非日期的 6 位版本号。
var pricingDateSuffixRe = regexp.MustCompile(`-\d{8}$`)

// pricingISODateSuffixRe 匹配末尾 ISO 日期 -YYYY-MM-DD（如 -2025-09-29）。
var pricingISODateSuffixRe = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)

// pricingVersionRe 匹配 -vN 版本后缀（-v1/-v2），剥掉让 "model-v2" 命中 "model"。
var pricingVersionRe = regexp.MustCompile(`-v\d+$`)

// pricingEffortSuffixRe 匹配推理努力后缀（-minimal/-low/-medium/-high/-xhigh），
// 剥掉让 "o3-mini-high" / "gpt-5.6-xhigh" 命中基线 id。
var pricingEffortSuffixRe = regexp.MustCompile(`-(minimal|low|medium|high|xhigh)$`)

// pricingYYMMDDSuffixRe 匹配末尾 6 位数字，由 stripModelYYMMDD 做月日校验后剥离。
var pricingYYMMDDSuffixRe = regexp.MustCompile(`-\d{6}$`)

// modelPricingCandidates 生成查找候选列表：从原始 id 派生一组逐步宽松的归一化 id。
// 移植自 cc-switch services/model_pricing_candidates 的 BFS 候选生成（A7 对齐 7 段剥离）。
//
// 关键：各变换（去命名空间/claude-desktop 前缀/bedrock 版本/日期/努力后缀/点转横线）需相互组合，
// 而非只对原始值各做一次。否则 "openai.claude-opus-4-6-20251114" 去命名空间后是
// "claude-opus-4-6-20251114"，但去日期只对原始值做会得到 "openai.claude-opus-4-6"，
// 永远凑不出 "claude-opus-4-6"。这里用 BFS：每个候选都施加全部 6 个 strip 变换，
// 新结果入队继续被后续变换作用，保证组合覆盖。
func modelPricingCandidates(modelID string) []string {
	cleaned := cleanModelIDForPricing(modelID)
	if cleaned == "" || isPlaceholderPricingModel(cleaned) {
		return nil
	}

	seen := make(map[string]struct{})
	candidates := make([]string, 0, 16)
	push := func(s string) bool {
		s = strings.TrimSpace(s)
		if s == "" {
			return false
		}
		if _, ok := seen[s]; ok {
			return false
		}
		seen[s] = struct{}{}
		candidates = append(candidates, s)
		return true
	}

	// BFS 工作队列：pop 一个候选，施加全部 strip 变换，新结果 push 入队。
	queue := []string{cleaned}
	for len(queue) > 0 {
		candidate := queue[0]
		queue = queue[1:]
		if !push(candidate) {
			continue
		}
		// 6 段剥离（顺序无关，因 BFS 会组合）：每个 strip 独立 push。
		if s := stripKnownModelNamespace(candidate); s != "" && s != candidate {
			queue = append(queue, s)
		}
		if s := stripClaudeDesktopNonAnthropicPrefix(candidate); s != "" && s != candidate {
			queue = append(queue, s)
		}
		if s := stripBedrockVersionSuffix(candidate); s != "" && s != candidate {
			queue = append(queue, s)
		}
		if s := stripModelDateSuffix(candidate); s != "" && s != candidate {
			queue = append(queue, s)
		}
		if s := stripReasoningEffortSuffix(candidate); s != "" && s != candidate {
			queue = append(queue, s)
		}
		// claude 点号转横线：Claude Desktop 的 "claude.opus.4.7" → "claude-opus-4-7"。
		// 仅对 claude. 前缀生效（避免误伤 meta.llama-4 这类 provider.点号 id）。
		if strings.HasPrefix(candidate, "claude.") {
			queue = append(queue, strings.ReplaceAll(candidate, ".", "-"))
		}
	}

	// 前缀回退（cursor-switch 超集，cc-switch 用 should_try_pricing_prefix_match 限定家族）：
	// 对每个候选把 - 分隔的最后段逐步砍掉，命中家族通用价目（如某 provider 把整个
	// claude-opus-4 系列定同价）。不限定家族，覆盖更广，最坏只是多几个 miss 候选。
	for _, src := range append([]string{}, candidates...) {
		segments := strings.Split(src, "-")
		for i := len(segments) - 1; i > 0; i-- {
			push(strings.Join(segments[:i], "-"))
		}
	}
	return candidates
}

// cleanModelIDForPricing 归一化 model id 用于候选派生：
//   - 取最后一个 "/" 后的部分（去 provider 路径前缀）
//   - 取第一个 ":" 前的部分（去 openrouter ":free" 等变体标记）
//   - "@" → "-"（bedrock cross-region 注入点等）
//   - 小写、去空白
func cleanModelIDForPricing(modelID string) string {
	s := strings.TrimSpace(strings.ToLower(modelID))
	if s == "" {
		return ""
	}
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		s = s[idx+1:]
	}
	if idx := strings.Index(s, ":"); idx >= 0 {
		s = s[:idx]
	}
	s = strings.ReplaceAll(s, "@", "-")
	return strings.TrimSpace(s)
}

// isPlaceholderPricingModel 判定是否为不应参与定价匹配的占位 id（cc-switch 同名逻辑）。
// 这类 id（如 "auto"/"default"/"unknown"）即便生成候选也无意义，直接返回空。
func isPlaceholderPricingModel(modelID string) bool {
	switch strings.TrimSpace(modelID) {
	case "", "auto", "default", "unknown", "none", "n/a", "null":
		return true
	}
	return false
}

// stripKnownModelNamespace 剥离已知 provider 命名空间前缀。
// 优先用 rfind("claude-")：任何 "xxx/claude-yyy" 或 "openai.claude-yyy" 都截到 "claude-yyy"，
// 让带路径前缀的 Claude 模型名命中基线。其次剥 marker 前缀（openai./anthropic./...）。
func stripKnownModelNamespace(modelID string) string {
	if pos := strings.LastIndex(modelID, "claude-"); pos > 0 {
		return modelID[pos:]
	}
	for _, marker := range pricingNamespaceMarkers {
		if strings.HasPrefix(modelID, marker) {
			return strings.TrimPrefix(modelID, marker)
		}
	}
	return ""
}

// stripClaudeDesktopNonAnthropicPrefix 剥离 Claude Desktop 给非 Anthropic 模型加的 "claude-" 前缀。
// "claude-gpt-5" → "gpt-5"，但 "claude-opus-4-7" 不剥（opus 是 Anthropic）。
func stripClaudeDesktopNonAnthropicPrefix(modelID string) string {
	rest := strings.TrimPrefix(modelID, "claude-")
	if rest == modelID {
		return ""
	}
	for _, marker := range claudeDesktopNonAnthropicMarkers {
		if strings.HasPrefix(rest, marker) {
			return rest
		}
	}
	return ""
}

// stripBedrockVersionSuffix 剥离 bedrock 版本后缀 -vN（如 "model-v1" → "model"）。
func stripBedrockVersionSuffix(modelID string) string {
	if loc := pricingVersionRe.FindStringIndex(modelID); loc != nil && loc[0] > 0 {
		return modelID[:loc[0]]
	}
	return ""
}

// stripModelDateSuffix 剥离末尾日期后缀。支持三种形态：
//   - ISO -YYYY-MM-DD（11 位带横线）
//   - 8 位 YYYYMMDD（如 -20250615）
//   - 6 位 YYMMDD（如 -260628；额外校验月 01-12、日 01-31，避免误伤版本号）
func stripModelDateSuffix(modelID string) string {
	if s := pricingISODateSuffixRe.FindString(modelID); s != "" {
		return strings.TrimSuffix(modelID, s)
	}
	if s := pricingDateSuffixRe.FindString(modelID); s != "" {
		return strings.TrimSuffix(modelID, s)
	}
	loc := pricingYYMMDDSuffixRe.FindStringIndex(modelID)
	if loc == nil {
		return ""
	}
	suffix := modelID[loc[0]+1:] // 去掉前导 '-'
	month, _ := strconv.Atoi(suffix[2:4])
	day, _ := strconv.Atoi(suffix[4:6])
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return ""
	}
	return modelID[:loc[0]]
}

// stripReasoningEffortSuffix 剥离推理努力后缀 -minimal/-low/-medium/-high/-xhigh。
func stripReasoningEffortSuffix(modelID string) string {
	if loc := pricingEffortSuffixRe.FindStringIndex(modelID); loc != nil && loc[0] > 0 {
		return modelID[:loc[0]]
	}
	return ""
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
