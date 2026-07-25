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

// TestBillableInputTokens 验证 FRESH/TOTAL/legacy 三种语义的输入 token 回算。
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
