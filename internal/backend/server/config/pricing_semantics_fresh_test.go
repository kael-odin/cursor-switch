package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestSeedPricingAllFresh 锁定 token 口径修复（2026-07-28）：
// adapter（anthropic.go / openai.go）落盘的 input 一律是 fresh_input，故内置 seed
// 必须全部标 FRESH——否则 billableInputTokens 会重复扣 cache 导致成本低估，且
// isCalibrationAnomaly 会持续误报「487 条口径异常」。
//
// 该测试是回归保险：任何新增 seed 漏标 FRESH 都会立刻红。
func TestSeedPricingAllFresh(t *testing.T) {
	for _, seed := range pricingModelSeed {
		if seed.InputTokenSemantics != InputSemanticsFresh {
			t.Errorf("seed %q: InputTokenSemantics=%q, want %q (adapter 落盘即 fresh，详见 pricing.go 头注释)",
				seed.ModelID, seed.InputTokenSemantics, InputSemanticsFresh)
		}
	}
}

// TestNormalizeMigratesBuiltinSemanticsToFresh 锁定口径迁移逻辑：
// 旧版本把 OpenAI seed 标 TOTAL、Anthropic seed 标 legacy（空串）。用户磁盘配置里这些
// 内置记录残留 TOTAL/legacy 语义。NormalizeConfig 必须把它们迁到 seed 当前语义（FRESH），
// 否则修复只对新用户生效，老用户的 487 条误报不消失。
//
// 迁移只动语义标签，不动用户编辑过的价格/倍率（IsBuiltin 记录的 seed 事实）。
func TestNormalizeMigratesBuiltinSemanticsToFresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// 模拟旧配置：gpt-5.6-sol 残留 TOTAL，claude-opus-5 残留 legacy（空串），
	// 外加一个用户自定义模型带 TOTAL（非 seed，应原样保留不被迁）。
	old := DefaultConfig()
	old.Pricing = PricingConfig{
		DefaultCostMultiplier: "1",
		Models: []ModelPricing{
			{ModelID: "gpt-5.6-sol", InputPerMillion: "9.9", OutputPerMillion: "30", InputTokenSemantics: InputSemanticsTotal},
			{ModelID: "claude-opus-5", InputPerMillion: "5", OutputPerMillion: "25", CacheReadPerMillion: "0.5", CacheWritePerMillion: "6.25"},
			{ModelID: "my-custom-model", InputPerMillion: "1", OutputPerMillion: "2", InputTokenSemantics: InputSemanticsTotal},
		},
	}
	data, err := yaml.Marshal(old)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	store := NewStore(path, "")
	cfg, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	find := func(id string) *ModelPricing {
		for i := range cfg.Pricing.Models {
			if cfg.Pricing.Models[i].ModelID == id {
				return &cfg.Pricing.Models[i]
			}
		}
		return nil
	}

	// 1) 内置 gpt-5.6-sol：TOTAL → 迁到 FRESH，但用户编辑过的价格保留。
	if m := find("gpt-5.6-sol"); m == nil {
		t.Fatal("gpt-5.6-sol missing")
	} else {
		if m.InputTokenSemantics != InputSemanticsFresh {
			t.Errorf("gpt-5.6-sol semantics not migrated: got %q want %q", m.InputTokenSemantics, InputSemanticsFresh)
		}
		if m.InputPerMillion != "9.9" {
			t.Errorf("gpt-5.6-sol user-edited price lost: got %q want 9.9", m.InputPerMillion)
		}
		if !m.IsBuiltin {
			t.Error("gpt-5.6-sol should be IsBuiltin=true")
		}
	}

	// 2) 内置 claude-opus-5：legacy（空串）→ 迁到 FRESH。
	if m := find("claude-opus-5"); m == nil {
		t.Fatal("claude-opus-5 missing")
	} else if m.InputTokenSemantics != InputSemanticsFresh {
		t.Errorf("claude-opus-5 semantics not migrated: got %q want %q", m.InputTokenSemantics, InputSemanticsFresh)
	}

	// 3) 自定义模型（非 seed）：TOTAL 原样保留——迁移只作用于命中 seed 的内置记录。
	if m := find("my-custom-model"); m == nil {
		t.Fatal("my-custom-model missing")
	} else if m.InputTokenSemantics != InputSemanticsTotal {
		t.Errorf("custom model semantics should NOT be migrated (non-seed): got %q want %q",
			m.InputTokenSemantics, InputSemanticsTotal)
	}
}

// TestDisabledBuiltinAlsoMigratedToFresh 确认逻辑删除（Disabled=true）的内置记录
// 也被迁到 FRESH——否则用户禁用过的内置模型重新启用时仍带旧语义。
func TestDisabledBuiltinAlsoMigratedToFresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	old := DefaultConfig()
	old.Pricing = PricingConfig{
		DefaultCostMultiplier: "1",
		Models: []ModelPricing{
			{ModelID: "gpt-5.6-sol", InputPerMillion: "5", OutputPerMillion: "30", InputTokenSemantics: InputSemanticsTotal, IsBuiltin: true, Disabled: true},
		},
	}
	data, _ := yaml.Marshal(old)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	store := NewStore(path, "")
	cfg, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, m := range cfg.Pricing.Models {
		if m.ModelID == "gpt-5.6-sol" {
			if !m.Disabled {
				t.Error("disabled builtin should stay disabled")
			}
			if m.InputTokenSemantics != InputSemanticsFresh {
				t.Errorf("disabled builtin semantics not migrated: got %q want %q", m.InputTokenSemantics, InputSemanticsFresh)
			}
			return
		}
	}
	t.Fatal("gpt-5.6-sol not found")
}
