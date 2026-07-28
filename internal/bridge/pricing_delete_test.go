// pricing_delete_test.go 验证 F-17：内置 pricing 删除后不被 seed 补回（逻辑删除 + tombstone），
// 自定义 pricing 物理删除，disabled 模型成本按 0 计，恢复默认价重置 seed 值。
package bridge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"cursor/internal/backend/server/config"
)

// seedPricingIntoStore 触发一次 Save 让 NormalizeConfig 把内置 seed 补进 store。
// 空文件首 Load 返回 DefaultConfig()（Pricing.Models=nil，不经 normalize），seed 只在 Save 时补。
func seedPricingIntoStore(t *testing.T, store *config.Store) {
	t.Helper()
	ctx := context.Background()
	cfg, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("seed setup Load: %v", err)
	}
	if _, err := store.Save(ctx, cfg); err != nil {
		t.Fatalf("seed setup Save: %v", err)
	}
}

// findPricingEntry 在快照里按 modelId 找记录（大小写不敏感）。
func findPricingEntry(t *testing.T, snap PricingSnapshot, modelID string) (TokenPricing, bool) {
	t.Helper()
	for _, m := range snap.Models {
		if strings.EqualFold(m.ModelID, modelID) {
			return m, true
		}
	}
	return TokenPricing{}, false
}

// TestF17DeleteBuiltinMarksDisabledNotPhysicalDelete 是 F-17 核心：
// 删除内置模型后记录仍在（Disabled=true），且重新 Load（触发 normalize）不会被 seed 补回成 enabled。
// 修复前：Delete 后 Save 内 normalize 把删的内置 modelId 当缺失补回，删除在同一 Save 内失效。
func TestF17DeleteBuiltinMarksDisabledNotPhysicalDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	store := config.NewStore(path, "")
	service := NewMetricsService(store)
	seedPricingIntoStore(t, store)

	const modelID = "gpt-5" // 内置 seed 模型
	if err := service.DeleteModelPricing(modelID); err != nil {
		t.Fatalf("DeleteModelPricing: %v", err)
	}

	snap := service.loadPricing()
	entry, ok := findPricingEntry(t, snap, modelID)
	if !ok {
		t.Fatal("F-17 FAIL: 内置模型被物理删除了——应保留为 Disabled=true tombstone")
	}
	if !entry.IsBuiltin {
		t.Error("F-17 FAIL: 删除后 IsBuiltin 应保持 true")
	}
	if !entry.Disabled {
		t.Fatal("F-17 FAIL: 删除后 Disabled 应为 true，实际仍 enabled——seed 补回让删除失效")
	}
}

// TestF17DeleteBuiltinSurvivesReload 验证 disabled 状态跨 Load 持久化（normalize 不补回）。
func TestF17DeleteBuiltinSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	store := config.NewStore(path, "")
	service := NewMetricsService(store)
	seedPricingIntoStore(t, store)

	const modelID = "claude-opus-5"
	if err := service.DeleteModelPricing(modelID); err != nil {
		t.Fatalf("DeleteModelPricing: %v", err)
	}

	// 新 store 重新 Load（模拟重启），normalize 应跳过 disabled 的内置 modelId。
	store2 := config.NewStore(path, "")
	service2 := NewMetricsService(store2)
	snap := service2.loadPricing()
	entry, ok := findPricingEntry(t, snap, modelID)
	if !ok || !entry.Disabled {
		t.Fatalf("F-17 FAIL: reload 后内置模型应仍 Disabled=true，got ok=%v entry=%+v", ok, entry)
	}
}

// TestF17DeleteCustomPhysicallyRemoves 验证自定义模型（非内置）物理删除。
func TestF17DeleteCustomPhysicallyRemoves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	store := config.NewStore(path, "")
	service := NewMetricsService(store)

	const modelID = "my-custom-model"
	if err := service.UpdateModelPricing(TokenPricing{
		ModelID:          modelID,
		DisplayName:      "Custom",
		InputPerMillion:  1.0,
		OutputPerMillion: 2.0,
	}); err != nil {
		t.Fatalf("UpdateModelPricing: %v", err)
	}
	if err := service.DeleteModelPricing(modelID); err != nil {
		t.Fatalf("DeleteModelPricing: %v", err)
	}

	snap := service.loadPricing()
	if _, ok := findPricingEntry(t, snap, modelID); ok {
		t.Fatal("F-17 FAIL: 自定义模型应被物理删除，实际仍存在")
	}
}

// TestF17DisabledModelCostsZero 验证 disabled 内置模型成本按 0 计算。
func TestF17DisabledModelCostsZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	store := config.NewStore(path, "")
	service := NewMetricsService(store)
	seedPricingIntoStore(t, store)

	const modelID = "gpt-5"
	// 先确认未禁用时能查到价（非零）。
	snap := service.loadPricing()
	entry, _ := findPricingEntry(t, snap, modelID)
	if entry.InputPerMillion == 0 && entry.OutputPerMillion == 0 {
		t.Fatalf("setup: gpt-5 内置价不应全零，got %+v", entry)
	}

	if err := service.DeleteModelPricing(modelID); err != nil {
		t.Fatalf("DeleteModelPricing: %v", err)
	}

	snap = service.loadPricing()
	price := findPrice(snap.Models, modelID)
	if !price.Disabled {
		t.Fatal("F-17 FAIL: findPrice 应返回 Disabled=true")
	}
	if price.InputPerMillion != 0 || price.OutputPerMillion != 0 {
		t.Fatalf("F-17 FAIL: disabled 模型应零价，got input=%v output=%v",
			price.InputPerMillion, price.OutputPerMillion)
	}
}

// TestF17RestoreDefaultResetsToSeed 验证恢复默认价重置为 seed 原值并清 Disabled。
func TestF17RestoreDefaultResetsToSeed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	store := config.NewStore(path, "")
	service := NewMetricsService(store)

	const modelID = "gpt-5"
	seed, _ := config.FindBuiltinSeed(modelID)

	// 先禁用，再恢复。
	if err := service.DeleteModelPricing(modelID); err != nil {
		t.Fatalf("DeleteModelPricing: %v", err)
	}
	if err := service.RestoreDefaultPricing(modelID); err != nil {
		t.Fatalf("RestoreDefaultPricing: %v", err)
	}

	snap := service.loadPricing()
	entry, ok := findPricingEntry(t, snap, modelID)
	if !ok {
		t.Fatal("F-17 FAIL: 恢复后记录应存在")
	}
	if entry.Disabled {
		t.Error("F-17 FAIL: 恢复后 Disabled 应清除")
	}
	if !entry.IsBuiltin {
		t.Error("F-17 FAIL: 恢复后 IsBuiltin 应为 true")
	}
	// 价格应回到 seed 原值。
	seedSnap := toPricingSnapshot(config.PricingConfig{Models: []config.ModelPricing{seed}})
	seedEntry, _ := findPricingEntry(t, seedSnap, modelID)
	if entry.InputPerMillion != seedEntry.InputPerMillion || entry.OutputPerMillion != seedEntry.OutputPerMillion {
		t.Errorf("F-17 FAIL: 恢复后价格应=seed 原值，got input=%v output=%v (seed input=%v output=%v)",
			entry.InputPerMillion, entry.OutputPerMillion, seedEntry.InputPerMillion, seedEntry.OutputPerMillion)
	}
}

// TestF17RestoreRejectsCustomModel 验证非内置模型无默认可恢复，返回错误。
func TestF17RestoreRejectsCustomModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	store := config.NewStore(path, "")
	service := NewMetricsService(store)

	if err := service.RestoreDefaultPricing("totally-custom-no-seed"); err == nil {
		t.Fatal("F-17 FAIL: 自定义模型应无法恢复默认价，应返回错误")
	}
}

// TestF17NormalizeBackfillsIsBuiltinForLegacyConfig 验证旧配置（无 IsBuiltin 字段）
// 经 normalize 后命中 seed 的记录补 IsBuiltin=true，自定义记录保持 false。
func TestF17NormalizeBackfillsIsBuiltinForLegacyConfig(t *testing.T) {
	// 模拟旧配置：一条内置 modelId（无 IsBuiltin）+ 一条自定义（无 IsBuiltin）。
	legacy := config.PricingConfig{
		DefaultCostMultiplier: "1",
		Models: []config.ModelPricing{
			{ModelID: "gpt-5", InputPerMillion: "999", OutputPerMillion: "999"}, // 命中 seed
			{ModelID: "my-custom", InputPerMillion: "1", OutputPerMillion: "2"},  // 不命中
		},
	}
	normalized, err := config.NormalizeConfig(config.Config{Pricing: legacy, Routing: config.RoutingConfig{Mode: config.DefaultRoutingMode}})
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}

	var gpt5, custom *config.ModelPricing
	for i := range normalized.Pricing.Models {
		switch normalized.Pricing.Models[i].ModelID {
		case "gpt-5":
			gpt5 = &normalized.Pricing.Models[i]
		case "my-custom":
			custom = &normalized.Pricing.Models[i]
		}
	}
	if gpt5 == nil {
		t.Fatal("gpt-5 entry missing")
	}
	if !gpt5.IsBuiltin {
		t.Error("F-17 FAIL: 命中 seed 的旧记录应回填 IsBuiltin=true")
	}
	if gpt5.InputPerMillion != "999" {
		t.Errorf("F-17 FAIL: 用户编辑过的内置记录价格不应被 seed 覆盖，got %q", gpt5.InputPerMillion)
	}
	if custom == nil {
		t.Fatal("custom entry missing")
	}
	if custom.IsBuiltin {
		t.Error("F-17 FAIL: 自定义记录 IsBuiltin 应保持 false")
	}
}

// TestF17NormalizeDoesNotReAddDisabledBuiltin 验证 normalize 不补回被 disabled 的内置 modelId。
func TestF17NormalizeDoesNotReAddDisabledBuiltin(t *testing.T) {
	cfg := config.PricingConfig{
		DefaultCostMultiplier: "1",
		Models: []config.ModelPricing{
			{ModelID: "gpt-5", IsBuiltin: true, Disabled: true, InputPerMillion: "5", OutputPerMillion: "25"},
		},
	}
	normalized, err := config.NormalizeConfig(config.Config{Pricing: cfg, Routing: config.RoutingConfig{Mode: config.DefaultRoutingMode}})
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}

	count := 0
	var entry *config.ModelPricing
	for i := range normalized.Pricing.Models {
		if normalized.Pricing.Models[i].ModelID == "gpt-5" {
			count++
			entry = &normalized.Pricing.Models[i]
		}
	}
	if count != 1 {
		t.Fatalf("F-17 FAIL: disabled 内置 modelId 不应被 seed 补回，应有 1 条实际 %d 条", count)
	}
	if entry != nil && !entry.Disabled {
		t.Error("F-17 FAIL: disabled 标记不应被 normalize 清除")
	}
}
