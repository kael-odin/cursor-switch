package bridge

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"cursor/internal/backend/server/config"
)

// writeInitialPricingConfig 写一份带 TOTAL 语义的自定义模型定价到配置文件，
// 用于 F-16 回归测试：UpdateModelPricing 不应丢失原 InputTokenSemantics。
func writeInitialPricingConfig(t *testing.T, path string, semantics config.InputTokenSemantics) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Pricing.Models = append(cfg.Pricing.Models, config.ModelPricing{
		ModelID:              "test-f16-model",
		DisplayName:          "F16 Test",
		InputPerMillion:      "1.0",
		OutputPerMillion:     "2.0",
		CacheReadPerMillion:  "0.1",
		CacheWritePerMillion: "0.2",
		InputTokenSemantics:  semantics,
	})
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal cfg: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
}

// TestUpdateModelPricingPreservesInputTokenSemantics (F-16) 验证更新已有 TOTAL 模型的
// 价格（payload 不带语义）时，原 TOTAL 语义被保留——否则编辑后退化到 legacy 成本口径。
func TestUpdateModelPricingPreservesInputTokenSemantics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeInitialPricingConfig(t, path, config.InputSemanticsTotal)

	store := config.NewStore(path, "")
	service := NewMetricsService(store)

	// 模拟前端只改价格、不带 InputTokenSemantics 的常见路径。
	if err := service.UpdateModelPricing(TokenPricing{
		ModelID:          "test-f16-model",
		DisplayName:      "F16 Test Updated",
		InputPerMillion:  5.0,
		OutputPerMillion: 9.0,
	}); err != nil {
		t.Fatalf("UpdateModelPricing: %v", err)
	}

	cfg, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var found *config.ModelPricing
	for i := range cfg.Pricing.Models {
		if cfg.Pricing.Models[i].ModelID == "test-f16-model" {
			found = &cfg.Pricing.Models[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("model not found after update")
	}
	if found.InputTokenSemantics != config.InputSemanticsTotal {
		t.Fatalf("F-16 regression: InputTokenSemantics lost after price edit; got %q want %q", found.InputTokenSemantics, config.InputSemanticsTotal)
	}
	if found.InputPerMillion != "5" {
		t.Fatalf("price not updated: got %q", found.InputPerMillion)
	}
}

// TestUpdateModelPricingPreservesFreshSemantics 验证 FRESH 语义同样被保留。
func TestUpdateModelPricingPreservesFreshSemantics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeInitialPricingConfig(t, path, config.InputSemanticsFresh)
	store := config.NewStore(path, "")
	service := NewMetricsService(store)

	if err := service.UpdateModelPricing(TokenPricing{
		ModelID:         "test-f16-model",
		InputPerMillion: 7.0,
	}); err != nil {
		t.Fatalf("UpdateModelPricing: %v", err)
	}
	cfg, _ := store.Load(context.Background())
	for _, m := range cfg.Pricing.Models {
		if m.ModelID == "test-f16-model" {
			if m.InputTokenSemantics != config.InputSemanticsFresh {
				t.Fatalf("FRESH semantics lost: got %q", m.InputTokenSemantics)
			}
			return
		}
	}
	t.Fatalf("model not found")
}

// TestUpdateModelPricingExplicitSemanticsOverrides 验证 payload 显式带语义时覆盖原值（含归一化）。
func TestUpdateModelPricingExplicitSemanticsOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeInitialPricingConfig(t, path, config.InputSemanticsLegacy)
	store := config.NewStore(path, "")
	service := NewMetricsService(store)

	if err := service.UpdateModelPricing(TokenPricing{
		ModelID:             "test-f16-model",
		InputPerMillion:     3.0,
		InputTokenSemantics: "total", // 小写应被归一化为 TOTAL
	}); err != nil {
		t.Fatalf("UpdateModelPricing: %v", err)
	}
	cfg, _ := store.Load(context.Background())
	for _, m := range cfg.Pricing.Models {
		if m.ModelID == "test-f16-model" {
			if m.InputTokenSemantics != config.InputSemanticsTotal {
				t.Fatalf("explicit semantics not normalized/persisted: got %q want %q", m.InputTokenSemantics, config.InputSemanticsTotal)
			}
			return
		}
	}
	t.Fatalf("model not found")
}

// TestUpdateModelPricingRejectsInvalidSemanticsAsLegacy 验证非法语义值回落 legacy（不写入脏值）。
func TestUpdateModelPricingRejectsInvalidSemanticsAsLegacy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// 新建模型，payload 带非法语义。
	store := config.NewStore(path, "")
	service := NewMetricsService(store)

	if err := service.UpdateModelPricing(TokenPricing{
		ModelID:             "test-f16-new",
		InputPerMillion:     3.0,
		InputTokenSemantics: "GARBAGE_VALUE",
	}); err != nil {
		t.Fatalf("UpdateModelPricing: %v", err)
	}
	cfg, _ := store.Load(context.Background())
	for _, m := range cfg.Pricing.Models {
		if m.ModelID == "test-f16-new" {
			if m.InputTokenSemantics != config.InputSemanticsLegacy {
				t.Fatalf("invalid semantics should normalize to legacy, got %q", m.InputTokenSemantics)
			}
			return
		}
	}
	t.Fatalf("new model not found")
}

// TestNormalizeInputTokenSemantics 验证 config 层归一化辅助函数。
func TestNormalizeInputTokenSemantics(t *testing.T) {
	cases := []struct {
		in   string
		want config.InputTokenSemantics
	}{
		{"", config.InputSemanticsLegacy},
		{"FRESH", config.InputSemanticsFresh},
		{"fresh", config.InputSemanticsFresh},
		{"  total  ", config.InputSemanticsTotal},
		{"TOTAL", config.InputSemanticsTotal},
		{"garbage", config.InputSemanticsLegacy},
		{"legacy", config.InputSemanticsLegacy},
	}
	for _, c := range cases {
		got := config.NormalizeInputTokenSemantics(c.in)
		if got != c.want {
			t.Errorf("NormalizeInputTokenSemantics(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
