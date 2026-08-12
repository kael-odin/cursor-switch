package bridge

import (
	"testing"

	"cursor/internal/backend/server/config"
)

// TestUnknownModelFallsBackToFreshSemantics 验证 F-2：未知模型（无定价记录）回退必须显式 FRESH。
//
// 背景：adapter 落盘前已统一把 input 折算成 fresh_input（openai 存 prompt-cached、anthropic 存
// 排除 cache 的 input_tokens），所以落盘 usage 的 input 永远不含 cache。旧实现未命中定价返回空语义，
// 计算层把空语义当 legacy → input 再减一次 cacheRead → 双减（input 成本漏算、realTotalTokens 失真、
// 可被 clamp 到 0）。未知模型无用户配置意图，必须按 FRESH 计。
func TestUnknownModelFallsBackToFreshSemantics(t *testing.T) {
	models := []TokenPricing{
		{ModelID: "gpt-5.6-sol", InputPerMillion: 5, OutputPerMillion: 30, CacheReadPerMillion: 0.5, InputTokenSemantics: string(config.InputSemanticsFresh)},
	}
	got := findPriceForModel(models, "unknown-hash", "unknown-model-name")
	if got.InputTokenSemantics != string(config.InputSemanticsFresh) {
		t.Fatalf("unknown model semantics = %q, want %q", got.InputTokenSemantics, config.InputSemanticsFresh)
	}

	input, output, cacheRead, cacheWrite := int64(1000), int64(200), int64(300), int64(0)
	billable := billableInputTokens(input, cacheRead, cacheWrite, got.InputTokenSemantics)
	if billable != input {
		t.Errorf("billableInputTokens = %d, want fresh %d (no cacheRead subtraction)", billable, input)
	}
	realTotal := realTotalTokensForModel(input, output, cacheRead, cacheWrite, got.InputTokenSemantics)
	if want := input + output + cacheRead + cacheWrite; realTotal != want {
		t.Errorf("realTotalTokens = %d, want %d (cache counted exactly once)", realTotal, want)
	}
	if isCalibrationAnomaly(input, cacheRead, cacheWrite, got.InputTokenSemantics) {
		t.Error("FRESH semantics must never flag calibration anomaly")
	}
}

// TestKnownModelEmptySemanticsKeepsLegacy 验证 F-2 边界：已知模型（有定价记录）的空语义保持 legacy，
// 不因 F-2 修复改变用户对自定义 provider 的显式 legacy 配置。
func TestKnownModelEmptySemanticsKeepsLegacy(t *testing.T) {
	models := []TokenPricing{
		{ModelID: "custom-model", InputPerMillion: 1, OutputPerMillion: 2},
	}
	got := findPriceForModel(models, "custom-model", "")
	if got.InputTokenSemantics != "" {
		t.Fatalf("known model with empty semantics should stay legacy, got %q", got.InputTokenSemantics)
	}
	input, cacheRead, cacheWrite := int64(1000), int64(300), int64(0)
	if billable := billableInputTokens(input, cacheRead, cacheWrite, got.InputTokenSemantics); billable != input-cacheRead {
		t.Errorf("legacy billable = %d, want %d (input - cacheRead)", billable, input-cacheRead)
	}
}
