package bridge

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"cursor/internal/backend/server/config"
	"cursor/internal/historymetrics"
)

// TestValidatePositiveFiniteMultiplier (F-34) 验证倍率校验规则：
// 拒绝 NaN/Inf/零/负值与非法字符串，仅接受有限正数。
func TestValidatePositiveFiniteMultiplier(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid positive", "1.5", false},
		{"valid integer", "2", false},
		{"small positive", "0.01", false},
		{"empty", " ", true},
		{"non-numeric", "abc", true},
		{"zero", "0", true},
		{"negative", "-1", true},
		{"nan", "NaN", true},
		{"inf", "Inf", true},
		{"neg inf", "-Inf", true},
		{"plus inf", "+Inf", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validatePositiveFiniteMultiplier(c.input)
			if (err != nil) != c.wantErr {
				t.Fatalf("validatePositiveFiniteMultiplier(%q) err=%v wantErr=%v", c.input, err, c.wantErr)
			}
		})
	}
}

// TestParsePositiveFiniteFloatOr (F-34) 验证计算层倍率解析回落 fallback。
func TestParsePositiveFiniteFloatOr(t *testing.T) {
	if got := parsePositiveFiniteFloatOr("1.5", 1); got != 1.5 {
		t.Fatalf("got %v", got)
	}
	if got := parsePositiveFiniteFloatOr("0", 1); got != 1 {
		t.Fatalf("zero should fall back, got %v", got)
	}
	if got := parsePositiveFiniteFloatOr("-2", 1); got != 1 {
		t.Fatalf("negative should fall back, got %v", got)
	}
	if got := parsePositiveFiniteFloatOr("NaN", 1); got != 1 {
		t.Fatalf("NaN should fall back, got %v", got)
	}
	if got := parsePositiveFiniteFloatOr("Inf", 1); got != 1 {
		t.Fatalf("Inf should fall back, got %v", got)
	}
	if got := parsePositiveFiniteFloatOr("abc", 1); got != 1 {
		t.Fatalf("garbage should fall back, got %v", got)
	}
}

// TestParseFloatOrRejectsNonFinite (F-34) 验证价格解析拒绝 NaN/Inf（防 JSON/Wails 序列化失败）。
func TestParseFloatOrRejectsNonFinite(t *testing.T) {
	if got := parseFloatOr("NaN", 5); got != 5 {
		t.Fatalf("NaN should fall back, got %v", got)
	}
	if got := parseFloatOr("Inf", 5); got != 5 {
		t.Fatalf("Inf should fall back, got %v", got)
	}
	if got := parseFloatOr("-Inf", 5); got != 5 {
		t.Fatalf("-Inf should fall back, got %v", got)
	}
	// 0 与负价格合法（免费/回退模型），仅 NaN/Inf 拒绝。
	if got := parseFloatOr("0", 5); got != 0 {
		t.Fatalf("zero price should be allowed, got %v", got)
	}
	if got := parseFloatOr("-1", 5); got != -1 {
		t.Fatalf("negative price should be allowed (compute layer handles), got %v", got)
	}
}

// TestSetDefaultCostMultiplierRejectsNonPositive (F-34) 验证 SetDefaultCostMultiplier
// 拒绝零/负值/NaN/Inf，仅持久化有限正数——保存语义与计算层一致。
func TestSetDefaultCostMultiplierRejectsNonPositive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	store := config.NewStore(path, "")
	service := NewMetricsService(store)

	for _, bad := range []string{"0", "-1.5", "NaN", "Inf", "abc"} {
		err := service.SetDefaultCostMultiplier(bad)
		if err == nil {
			t.Fatalf("SetDefaultCostMultiplier(%q) should error", bad)
		}
	}
	// 合法值能保存。
	if err := service.SetDefaultCostMultiplier("1.5"); err != nil {
		t.Fatalf("SetDefaultCostMultiplier(1.5): %v", err)
	}
	cfg, _ := store.Load(context.Background())
	if cfg.Pricing.DefaultCostMultiplier != "1.5" {
		t.Fatalf("multiplier not persisted: got %q", cfg.Pricing.DefaultCostMultiplier)
	}
}

// TestComputeCostByModelClampsNonFiniteMultiplier (F-34) 验证 NaN/Inf/<=0 默认倍率
// 在计算层被回落为 1，不会传播为 NaN 成本。
func TestComputeCostByModelClampsNonFiniteMultiplier(t *testing.T) {
	// 构造一个 PricingSnapshot，DefaultCostMultiplier 为 NaN。
	pricing := PricingSnapshot{
		DefaultCostMultiplier: math.NaN(),
		Models: []TokenPricing{{
			ModelID:          "m1",
			InputPerMillion:  1,
			OutputPerMillion: 2,
		}},
	}
	byModel := []historymetrics.ModelUsage{{
		ModelID:     "m1",
		InputTokens: 1000,
		OutputTokens: 500,
	}}
	costs, total := computeCostByModel(byModel, pricing, nil)
	if len(costs) != 1 {
		t.Fatalf("expected 1 cost row, got %d", len(costs))
	}
	if math.IsNaN(costs[0].TotalCost) || math.IsInf(costs[0].TotalCost, 0) {
		t.Fatalf("total cost must be finite, got %v", costs[0].TotalCost)
	}
	if costs[0].CostMultiplier != 1 {
		t.Fatalf("NaN default multiplier should clamp to 1, got %v", costs[0].CostMultiplier)
	}
	if math.IsNaN(total) || math.IsInf(total, 0) {
		t.Fatalf("grand total must be finite, got %v", total)
	}
}

// validAdapterConfig 构造一个通过 NormalizeModelAdapterConfigs 校验的 ModelAdapterConfig。
// 仅 CostMultiplier / ModelID 不同，用于隔离倍率解析测试。
func validAdapterConfig(modelID, multiplier string) config.ModelAdapterConfig {
	return config.ModelAdapterConfig{
		DisplayName: modelID,
		Type:         "openai",
		BaseURL:      "https://example.com",
		APIKey:       "sk-test",
		TooltipData:  "tooltip",
		ModelID:      modelID,
		CostMultiplier: multiplier,
	}
}

// TestLoadAdapterMultipliersRejectsNonPositive (F-34) 验证 per-adapter 倍率
// NaN/Inf/<=0 被过滤，不进候选。走 store.Save（含 Normalize）保证真实读写路径。
func TestLoadAdapterMultipliersRejectsNonPositive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	store := config.NewStore(path, "")

	cfg := config.DefaultConfig()
	cfg.ModelAdapters = []config.ModelAdapterConfig{
		validAdapterConfig("good", "2.0"),
		validAdapterConfig("zero", "0"),
		validAdapterConfig("neg", "-1"),
		validAdapterConfig("nan", "NaN"),
	}
	if _, err := store.Save(context.Background(), cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	service := NewMetricsService(store)
	multipliers := service.loadAdapterMultipliers()
	if v, ok := multipliers["good"]; !ok || v != 2.0 {
		t.Fatalf("good multiplier should be 2.0, got %v ok=%v", v, ok)
	}
	for _, bad := range []string{"zero", "neg", "nan"} {
		if _, ok := multipliers[bad]; ok {
			t.Fatalf("invalid multiplier %q should be filtered out", bad)
		}
	}
}
