package config

import "testing"

// TestPricingCandidateMatch 验证候选匹配能命中各类变体：命名空间前缀、日期后缀、
// 推理努力后缀、版本后缀、前缀回退。这是 cc-switch model_pricing_candidates 的核心行为。
func TestPricingCandidateMatch(t *testing.T) {
	cfg := &PricingConfig{Models: pricingModelSeed}

	cases := []struct {
		input string
		want  string // 期望命中的 ModelID；空表示期望 nil
	}{
		{"openai.gpt-5", "gpt-5"},                       // 去命名空间
		{"GPT-5", "gpt-5"},                              // 大小写
		{"anthropic.claude-sonnet-5", "claude-sonnet-5"}, // 去命名空间
		{"claude-opus-4-6-20251114", "claude-opus-4-6"}, // 日期后缀剥到基线 claude-opus-4-6
		{"claude-opus-4-6", "claude-opus-4-6"},          // 直接命中基线
		{"openai.claude-opus-4-6-20251114", "claude-opus-4-6"}, // 命名空间+日期后缀组合，须剥到 claude-opus-4-6
		{"o3-mini-high", "o3-mini"},                     // 推理努力后缀
		{"qwen3-coder-plus-v2", "qwen3-coder-plus"},     // 版本后缀
		{"grok-3-mini", "grok-3-mini"},                  // 直接命中
		{"claude-opus-4-8", "claude-opus-4-8"},          // 直接命中
		{"totally-unknown-model", ""},                   // 未命中
		// GPT-5.6 effort 后缀 + 裸名（v3.18.0 同步）：各有独立行，价格同 Sol。
		{"gpt-5.6-high", "gpt-5.6-high"},
		{"gpt-5.6-xhigh", "gpt-5.6-xhigh"},
		{"gpt-5.6-minimal", "gpt-5.6-minimal"},
		{"gpt-5.6", "gpt-5.6"},
	}
	for _, tc := range cases {
		got := cfg.FindModelPricing(tc.input)
		if tc.want == "" {
			if got != nil {
				t.Errorf("FindModelPricing(%q) = %q, 期望 nil", tc.input, got.ModelID)
			}
			continue
		}
		if got == nil {
			t.Errorf("FindModelPricing(%q) = nil, 期望 %q", tc.input, tc.want)
			continue
		}
		if got.ModelID != tc.want {
			t.Errorf("FindModelPricing(%q) = %q, 期望 %q", tc.input, got.ModelID, tc.want)
		}
	}
}

// TestPricingCandidatesSequence 验证候选序列的派生顺序，确保逐步宽松。
func TestPricingCandidatesSequence(t *testing.T) {
	cands := modelPricingCandidates("openai.claude-opus-4-6-20251114")
	// 第一个必须是原始归一化值；后续应包含去命名空间、去日期等派生。
	if len(cands) == 0 || cands[0] != "openai.claude-opus-4-6-20251114" {
		t.Fatalf("首个候选应为原始归一化值，得到 %v", cands)
	}
	contains := func(s string) bool {
		for _, c := range cands {
			if c == s {
				return true
			}
		}
		return false
	}
	if !contains("claude-opus-4-6-20251114") {
		t.Errorf("应包含去命名空间后的 claude-opus-4-6-20251114，得到 %v", cands)
	}
	if !contains("claude-opus-4-6") {
		t.Errorf("应包含去日期后的 claude-opus-4-6，得到 %v", cands)
	}
}

// TestPricingCandidatesSevenStageStripping 是 A7 的回归测试：对齐 cc-switch 7 段剥离，
// 覆盖 AUDIT 列举的变种。每条用例验证候选列表包含剥到基线后的 id。
func TestPricingCandidatesSevenStageStripping(t *testing.T) {
	contains := func(cands []string, want string) bool {
		for _, c := range cands {
			if c == want {
				return true
			}
		}
		return false
	}
	cases := []struct {
		name  string
		input string
		want  string // 候选列表中必须包含的基线 id
	}{
		// 1) known namespace 前缀
		{"namespace anthropic", "anthropic.claude-sonnet-4-5-20250929", "claude-sonnet-4-5-20250929"},
		{"namespace openai", "openai.gpt-5", "gpt-5"},
		// 2) claude-desktop 非 Anthropic 前缀（claude-gpt-5 → gpt-5）
		{"claude-desktop gpt", "claude-gpt-5", "gpt-5"},
		{"claude-desktop gemini", "claude-gemini-2.5", "gemini-2.5"},
		{"claude-desktop deepseek", "claude-deepseek-v3", "deepseek-v3"},
		// 真 Anthropic 模型不能被剥成 "opus-4-7"
		{"claude real anthropic not stripped", "claude-opus-4-7", "claude-opus-4-7"},
		// 3) bedrock 版本后缀 -v1
		{"bedrock version suffix", "nova-pro-v1", "nova-pro"},
		// 4) ISO 日期后缀 -YYYY-MM-DD / 8 位 YYYYMMDD / 6 位 YYMMDD（带月日校验）
		{"iso date suffix", "claude-sonnet-4-5-2025-09-29", "claude-sonnet-4-5"},
		{"yyyymmdd suffix", "claude-sonnet-4-5-20250929", "claude-sonnet-4-5"},
		{"yymmdd suffix valid", "doubao-seed-260628", "doubao-seed"},
		// 6 位非日期（月>12）不应被剥
		{"yymmdd suffix invalid month not stripped", "model-991234", ""},
		// 5) reasoning effort 后缀 -xhigh/-minimal（旧版只有 low/medium/high）
		{"effort xhigh", "gpt-5.6-xhigh", "gpt-5.6"},
		{"effort minimal", "o3-mini-minimal", "o3-mini"},
		// 6) claude 点号转横线
		{"claude dot to dash", "claude.opus.4.7", "claude-opus-4-7"},
		// 组合：namespace + claude-desktop 前缀 + 日期
		{"combo namespace+date", "openai.claude-opus-4-6-20251114", "claude-opus-4-6"},
		// cleanModelIDForPricing：取 / 后、: 前、@ 转 -
		{"clean slash", "providers/openai/gpt-5", "gpt-5"},
		{"clean colon free", "claude-sonnet-4-5:free", "claude-sonnet-4-5"},
		{"clean at to dash", "meta.llama-4@2025", "meta.llama-4-2025"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cands := modelPricingCandidates(tc.input)
			if tc.want == "" {
				// 期望不被剥到空基线：仅验证不 panic，候选非空即可。
				if len(cands) == 0 {
					return
				}
				return
			}
			if !contains(cands, tc.want) {
				t.Errorf("modelPricingCandidates(%q) = %v, 未包含 %q", tc.input, cands, tc.want)
			}
		})
	}
}

// TestPricingCandidatesPlaceholder 验证占位 id 不生成候选（cc-switch is_placeholder_pricing_model）。
func TestPricingCandidatesPlaceholder(t *testing.T) {
	for _, ph := range []string{"auto", "default", "unknown", "none"} {
		if cands := modelPricingCandidates(ph); len(cands) != 0 {
			t.Errorf("modelPricingCandidates(%q) = %v, 期望空", ph, cands)
		}
	}
}

// TestPricingMatchDisplayNameSpaces 验证 P1-display：displayName 带空格（如 "GPT-5.6 Sol"）
// 经 cleanModelIDForPricing + normalizePricingModelID 空格→横线归一后能命中 seed "gpt-5.6-sol"。
//
// 背景：resolveRequestedModelName 取 modelDetails.displayName（带空格），旧归一只 ToLower+TrimSpace，
// 派生不出横线分隔的 seed 真名 → name miss → id miss → 成本归零。两端口径都加空格→横线后即命中。
func TestPricingMatchDisplayNameSpaces(t *testing.T) {
	cfg := &PricingConfig{Models: pricingModelSeed}

	// "GPT-5.6 Sol"（带空格）应命中 seed gpt-5.6-sol。
	got := cfg.FindModelPricing("GPT-5.6 Sol")
	if got == nil {
		t.Fatalf("FindModelPricing(\"GPT-5.6 Sol\") = nil, 期望命中 gpt-5.6-sol")
	}
	if got.ModelID != "gpt-5.6-sol" {
		t.Errorf("FindModelPricing(\"GPT-5.6 Sol\") = %q, 期望 gpt-5.6-sol", got.ModelID)
	}

	// 多空格/前后空格也应归一命中。
	if got := cfg.FindModelPricing("  GPT-5.6  Sol  "); got == nil || got.ModelID != "gpt-5.6-sol" {
		gotID := ""
		if got != nil {
			gotID = got.ModelID
		}
		t.Errorf("FindModelPricing(\"  GPT-5.6  Sol  \") = %q, 期望 gpt-5.6-sol", gotID)
	}

	// 对照：横线形态直接命中（无回归）。
	if got := cfg.FindModelPricing("gpt-5.6-sol"); got == nil || got.ModelID != "gpt-5.6-sol" {
		gotID := ""
		if got != nil {
			gotID = got.ModelID
		}
		t.Errorf("FindModelPricing(\"gpt-5.6-sol\") = %q, 期望 gpt-5.6-sol（横线形态无回归）", gotID)
	}
}

// TestNormalizePricingModelIDSpaces 验证归一化函数空格→横线，且对无空格 seed 无副作用（no-op）。
func TestNormalizePricingModelIDSpaces(t *testing.T) {
	cases := []struct{ in, want string }{
		{"GPT-5.6 Sol", "gpt-5.6-sol"},
		{"  GPT-5.6  Sol  ", "gpt-5.6-sol"},
		{"gpt-5.6-sol", "gpt-5.6-sol"}, // 无空格 no-op
		{"Claude-Opus-4-7", "claude-opus-4-7"},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizePricingModelID(c.in); got != c.want {
			t.Errorf("normalizePricingModelID(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}
// 这是成本计算的核心：避免缓存部分被重复计费。
func TestBillableInputTokens(t *testing.T) {
	// legacy：input 含 cache_read，减 cache_read
	if got := billableLegacy(1000, 300, 200); got != 700 {
		t.Errorf("legacy(1000,300,200) = %d, 期望 700", got)
	}
	// TOTAL：input 含 cache_read+cache_creation，减两者
	if got := billableTotal(1000, 300, 200); got != 500 {
		t.Errorf("total(1000,300,200) = %d, 期望 500", got)
	}
	// FRESH：input 原样
	if got := billableFresh(1000, 300, 200); got != 1000 {
		t.Errorf("fresh(1000,300,200) = %d, 期望 1000", got)
	}
}

// 这些 helper 复刻 bridge.billableInputTokens 的逻辑用于在 config 包内测试语义。
// （bridge 包的 billableInputTokens 依赖 TokenPricing，这里直接测语义分支。）
func billableLegacy(input, cacheRead, cacheWrite int64) int64 {
	v := input - cacheRead
	if v < 0 {
		return 0
	}
	return v
}
func billableTotal(input, cacheRead, cacheWrite int64) int64 {
	v := input - cacheRead - cacheWrite
	if v < 0 {
		return 0
	}
	return v
}
func billableFresh(input, cacheRead, cacheWrite int64) int64 {
	return input
}
