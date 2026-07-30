package bridge

import (
	"testing"

	"cursor/internal/backend/server/config"
	"cursor/internal/historymetrics"
)

// TestFindPriceForModelPrefersName 验证成本引擎能跨过 model_id 哈希命中真名定价。
//
// 背景：usage.json 的 by_model.model_id 存的是通道/适配器哈希（如 57693a3f4c14f02b），
// 而定价表里是模型真名（如 gpt-5.6-sol）。旧实现直接用 model_id 调 findPrice →
// 候选匹配派生不出真名 → 全 miss → 零价 → 总成本 $0.00。
// findPriceForModel 应先用 model_name 命中真名价，再回退 model_id。
func TestFindPriceForModelPrefersName(t *testing.T) {
	models := []TokenPricing{
		{ModelID: "gpt-5.6-sol", InputPerMillion: 5, OutputPerMillion: 30, CacheReadPerMillion: 0.5, InputTokenSemantics: string(config.InputSemanticsFresh)},
	}

	// model_id 是哈希，model_name 是真名 → 应命中 gpt-5.6-sol 的 $5/$30。
	got := findPriceForModel(models, "57693a3f4c14f02b", "gpt-5.6-sol")
	if got.InputPerMillion != 5 || got.OutputPerMillion != 30 {
		t.Fatalf("expected name match to hit gpt-5.6-sol price (5/30), got input=%v output=%v",
			got.InputPerMillion, got.OutputPerMillion)
	}

	// 无 model_name 时回退到 model_id 匹配（这里 model_id 直接就是定价表里的 id）。
	got2 := findPriceForModel(models, "gpt-5.6-sol", "")
	if got2.InputPerMillion != 5 {
		t.Fatalf("fallback to model_id match failed, got input=%v", got2.InputPerMillion)
	}

	// 既无 name 又 miss id → 零价（不凭空造价）。
	got3 := findPriceForModel(models, "unknown-hash", "")
	if got3.InputPerMillion != 0 || got3.OutputPerMillion != 0 {
		t.Fatalf("miss should yield zero price, got input=%v output=%v",
			got3.InputPerMillion, got3.OutputPerMillion)
	}
}

// TestComputeCostByModelMatchesByName 验证 by_model 聚合（model_id=哈希）能算出非零成本。
func TestComputeCostByModelMatchesByName(t *testing.T) {
	pricing := PricingSnapshot{
		DefaultCostMultiplier: 1,
		Models: []TokenPricing{
			{ModelID: "gpt-5.6-sol", InputPerMillion: 5, OutputPerMillion: 30, CacheReadPerMillion: 0.5, InputTokenSemantics: string(config.InputSemanticsFresh)},
		},
	}
	byModel := []historymetrics.ModelUsage{
		{ModelID: "57693a3f4c14f02b", ModelName: "gpt-5.6-sol", InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 0, CacheWriteTokens: 0},
	}
	_, total := computeCostByModel(byModel, pricing, nil)
	// 1M input × $5 + 1M output × $30 = $35
	if total <= 0 {
		t.Fatalf("cost should be non-zero when model_name matches pricing, got $%.4f", total)
	}
	// 允许浮点误差，期望 ~35
	if total < 34.99 || total > 35.01 {
		t.Fatalf("expected ~$35.00 (1M*$5 + 1M*$30), got $%.4f", total)
	}
}

// TestBuildDashboardModelStatsUsesName 验证仪表盘模型统计在 model_id 是哈希时仍能算出真实成本。
func TestBuildDashboardModelStatsUsesName(t *testing.T) {
	pricing := PricingSnapshot{
		DefaultCostMultiplier: 1,
		Models: []TokenPricing{
			{ModelID: "gpt-5.6-sol", InputPerMillion: 5, OutputPerMillion: 30, InputTokenSemantics: string(config.InputSemanticsFresh)},
		},
	}
	raw := []historymetrics.UsageDashboardRawModelAggregate{
		{ModelID: "57693a3f4c14f02b", ModelName: "gpt-5.6-sol", InputTokens: 1_000_000, OutputTokens: 1_000_000},
	}
	stats := buildDashboardModelStats(raw, pricing, nil, 1)
	if len(stats) != 1 {
		t.Fatalf("expected 1 stat, got %d", len(stats))
	}
	if stats[0].TotalCost <= 0 {
		t.Fatalf("expected non-zero cost via name match, got $%.4f", stats[0].TotalCost)
	}
}

// TestBuildModelNameByID 验证 model_id→model_name 映射构造正确。
func TestBuildModelNameByID(t *testing.T) {
	raw := []historymetrics.UsageDashboardRawModelAggregate{
		{ModelID: "hash1", ModelName: "gpt-5.6-sol"},
		{ModelID: "hash2", ModelName: "mimo-v2.5-pro-ultraspeed"},
		{ModelID: "hash3", ModelName: ""}, // 无名，不入映射
	}
	m := buildModelNameByID(raw)
	if m["hash1"] != "gpt-5.6-sol" {
		t.Fatalf("hash1 should map to gpt-5.6-sol, got %q", m["hash1"])
	}
	if m["hash2"] != "mimo-v2.5-pro-ultraspeed" {
		t.Fatalf("hash2 mapping wrong, got %q", m["hash2"])
	}
	if _, ok := m["hash3"]; ok {
		t.Fatal("hash3 has empty name, should not be in map")
	}
}
