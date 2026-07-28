package config

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// TestStoreUpdateAtomicConcurrent 验证 F-03：并发 Update 改不同字段互不覆盖。
//
// 此前 Load 和 Save 各自加锁，完整读改写不在同一临界区——并发改不同字段时
// 后写者基于陈旧基线覆盖先写者。Update 在同一 store.mu 临界区内 Load-Modify-Save，
// 两个并发事务各改一个字段，最终两个字段都应保留。
func TestStoreUpdateAtomicConcurrent(t *testing.T) {
	store := NewStore(t.TempDir()+"/config.yaml", "")
	ctx := context.Background()

	// 初始写入基线。
	if _, err := store.Save(ctx, DefaultConfig()); err != nil {
		t.Fatal(err)
	}

	const goroutines = 2
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make(chan error, goroutines)

	// 事务 A：设 Log=true
	go func() {
		defer wg.Done()
		_, err := store.Update(ctx, func(cfg *Config) error {
			cfg.Log = true
			return nil
		})
		errs <- err
	}()
	// 事务 B：设 LastAgentModelHash="hash-B"
	go func() {
		defer wg.Done()
		_, err := store.Update(ctx, func(cfg *Config) error {
			cfg.LastAgentModelHash = "hash-B"
			return nil
		})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Update returned error: %v", err)
		}
	}

	final, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !final.Log {
		t.Error("F-03 FAIL: Log was overwritten by concurrent Update (expected true)")
	}
	if final.LastAgentModelHash != "hash-B" {
		t.Errorf("F-03 FAIL: LastAgentModelHash lost (got %q, want hash-B)", final.LastAgentModelHash)
	}
}

// TestStoreUpdateMutatorErrorRollsBack 验证 mutator 返回 error 时不写回。
func TestStoreUpdateMutatorErrorRollsBack(t *testing.T) {
	store := NewStore(t.TempDir()+"/config.yaml", "")
	ctx := context.Background()
	if _, err := store.Save(ctx, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	sentinelErr := fmt.Errorf("intentional abort")
	// mutator 改了字段但返回 error——必须回滚，磁盘值不变。
	_, err := store.Update(ctx, func(cfg *Config) error {
		cfg.Log = true
		return sentinelErr
	})
	if err != sentinelErr {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	final, _ := store.Load(ctx)
	if final.Log {
		t.Error("mutator error should have rolled back, but Log was persisted")
	}
}

// TestStoreUpdatePreservesUnrelatedFields 验证 Update 以磁盘最新为基线，
// 只改 mutator 动的字段——这正是 F-02 后端 merge 的基础。
func TestStoreUpdatePreservesUnrelatedFields(t *testing.T) {
	store := NewStore(t.TempDir()+"/config.yaml", "")
	ctx := context.Background()

	// 先用 Save 写入 Log=true, hash="pre-existing"
	cfg := DefaultConfig()
	cfg.Log = true
	cfg.LastAgentModelHash = "pre-existing"
	if _, err := store.Save(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	// Update 只改 Log，不应动 LastAgentModelHash（模拟前端整包保存改走后端 merge）。
	if _, err := store.Update(ctx, func(cfg *Config) error {
		cfg.Log = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	final, _ := store.Load(ctx)
	if final.Log {
		t.Error("expected Log=false after Update")
	}
	if final.LastAgentModelHash != "pre-existing" {
		t.Errorf("F-02 merge base: unrelated field lost (got %q, want pre-existing)",
			final.LastAgentModelHash)
	}
}

// TestManagerSaveLastAgentModelHashConcurrentWithUpdate 验证改用 Update 后，
// 两个 Update 路径并发不互覆盖（F-03 的承诺）。
//
// 注意：manager.Save（前端整包保存）仍是非原子的整包替换语义，与 Update 并发时
// 仍可能覆盖——那是 F-02（前端整包丢字段）的范畴，需前端改走 patch 或后端 merge，
// 不在本测试承诺内。本测试只验证两个 Update 事务之间的原子性。
func TestManagerSaveLastAgentModelHashConcurrentWithUpdate(t *testing.T) {
	store := NewStore(t.TempDir()+"/config.yaml", "")
	ctx := context.Background()
	if _, err := store.Save(ctx, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(ctx, store)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	// A：Update 路径保存 hash
	go func() {
		defer wg.Done()
		_ = manager.SaveLastAgentModelHash(ctx, "hash-from-A")
	}()
	// B：另一 Update 路径改 Log
	go func() {
		defer wg.Done()
		_, _ = manager.Update(ctx, func(cfg *Config) error {
			cfg.Log = true
			return nil
		})
	}()
	wg.Wait()

	final := manager.Current()
	if final.LastAgentModelHash != "hash-from-A" {
		t.Errorf("F-03 FAIL: hash lost under concurrent Update (got %q)", final.LastAgentModelHash)
	}
	if !final.Log {
		t.Errorf("F-03 FAIL: Log lost under concurrent Update")
	}
}

// --- F-02: 前端整包保存 merge ---

// validAdapter 构造一个能通过 NormalizeModelAdapterConfigs 校验的 openai adapter。
func validAdapter(displayName, modelID string) ModelAdapterConfig {
	enabled := true
	return ModelAdapterConfig{
		DisplayName:     displayName,
		Type:            "openai",
		BaseURL:         "https://api.example.com",
		APIKey:          "sk-test-key",
		TooltipData:     "tip",
		ModelID:         modelID,
		ReasoningEffort: "medium",
		OpenAIEndpoint:  "/v1/chat/completions",
		Enabled:         &enabled,
	}
}

// TestMergeUserPatchPreservesPricing 验证前端整包 patch 不携带 pricing 时，
// merge 保留磁盘 pricing（S15 的 InputTokenSemantics 等靠此保留）。
func TestMergeUserPatchPreservesPricing(t *testing.T) {
	store := NewStore(t.TempDir()+"/config.yaml", "")
	ctx := context.Background()

	// 磁盘基线：非空 pricing + 一个 adapter。
	base := DefaultConfig()
	base.Pricing = PricingConfig{
		DefaultCostMultiplier: "1.5",
		Models: []ModelPricing{
			{ModelID: "gpt-5", InputPerMillion: "5", OutputPerMillion: "25", InputTokenSemantics: "TOTAL"},
		},
	}
	base.ModelAdapters = []ModelAdapterConfig{validAdapter("A", "gpt-5")}
	if _, err := store.Save(ctx, base); err != nil {
		t.Fatal(err)
	}

	// 前端 patch：只有它管理的字段，Pricing 是零值（前端从不携带）。
	patch := Config{
		Log:                       true,
		ProviderStreamIdleTimeout: 60,
		BackendListenAddr:         DefaultBackendListenAddr,
		ProxyListenAddr:           DefaultProxyListenAddr,
		Routing:                   RoutingConfig{Mode: DefaultRoutingMode},
		ModelAdapters:             []ModelAdapterConfig{validAdapter("A", "gpt-5")},
	}

	merged, err := store.MergeUserPatch(ctx, patch)
	if err != nil {
		t.Fatalf("MergeUserPatch: %v", err)
	}
	if merged.Pricing.DefaultCostMultiplier != "1.5" {
		t.Errorf("F-02 FAIL: pricing.DefaultCostMultiplier lost (got %q)", merged.Pricing.DefaultCostMultiplier)
	}
	// NormalizeConfig 会把内置 seed 价目表补进 Pricing.Models（INSERT OR IGNORE 语义），
	// 所以这里不能断言 len==1；改断言用户编辑过的 gpt-5 条目（含 InputTokenSemantics）保留。
	var gpt5 *ModelPricing
	for i := range merged.Pricing.Models {
		if merged.Pricing.Models[i].ModelID == "gpt-5" {
			gpt5 = &merged.Pricing.Models[i]
			break
		}
	}
	if gpt5 == nil {
		t.Fatalf("F-02 FAIL: user-edited pricing entry gpt-5 lost entirely")
	}
	if gpt5.InputPerMillion != "5" || gpt5.OutputPerMillion != "25" || gpt5.InputTokenSemantics != "TOTAL" {
		t.Errorf("F-02 FAIL: gpt-5 pricing fields lost (got %+v)", gpt5)
	}
}

// TestMergeUserPatchInheritsAdapterCostMultiplier 验证 patch adapter 的 CostMultiplier
// 为空时，从磁盘旧列表同身份键 adapter 继承；改 baseURL（新身份键）则无继承。
func TestMergeUserPatchInheritsAdapterCostMultiplier(t *testing.T) {
	store := NewStore(t.TempDir()+"/config.yaml", "")
	ctx := context.Background()

	base := DefaultConfig()
	a := validAdapter("A", "gpt-5")
	a.CostMultiplier = "1.5"
	b := validAdapter("B", "claude")
	b.CostMultiplier = "2.0"
	base.ModelAdapters = []ModelAdapterConfig{a, b}
	if _, err := store.Save(ctx, base); err != nil {
		t.Fatal(err)
	}

	// patch：A 不带 costMultiplier（应继承 1.5）；B 改了 baseURL（新身份键，不继承）。
	patchA := validAdapter("A", "gpt-5")
	patchA.CostMultiplier = "" // 前端 normalizer 不带此字段
	patchB := validAdapter("B", "claude")
	patchB.BaseURL = "https://api.different.com" // 新身份键
	patchB.CostMultiplier = ""
	patch := Config{
		BackendListenAddr: DefaultBackendListenAddr,
		ProxyListenAddr:   DefaultProxyListenAddr,
		Routing:            RoutingConfig{Mode: DefaultRoutingMode},
		ModelAdapters:     []ModelAdapterConfig{patchA, patchB},
	}

	merged, err := store.MergeUserPatch(ctx, patch)
	if err != nil {
		t.Fatalf("MergeUserPatch: %v", err)
	}
	byName := map[string]string{}
	for _, ad := range merged.ModelAdapters {
		byName[ad.DisplayName] = ad.CostMultiplier
	}
	if byName["A"] != "1.5" {
		t.Errorf("F-02 FAIL: adapter A costMultiplier not inherited (got %q, want 1.5)", byName["A"])
	}
	if byName["B"] != "" {
		t.Errorf("F-02 FAIL: adapter B changed identity (baseURL) should NOT inherit (got %q)", byName["B"])
	}
}

// TestMergeUserPatchPreservesTabServerBaseURLWhenPatchOmits 验证即便前端未做
// carry-through（patch routing 只带 mode），merge 也用 patch 值覆盖——
// 这要求前端必须 carry-through tabServerBaseURL，否则会被清空。
// 此测试锁定 merge 语义：patch.Routing.TabServerBaseURL 直接覆盖 dst（前端负责携带）。
func TestMergeUserPatchPreservesTabServerBaseURLWhenPatchOmits(t *testing.T) {
	store := NewStore(t.TempDir()+"/config.yaml", "")
	ctx := context.Background()

	base := DefaultConfig()
	base.Routing.TabServerBaseURL = "https://tab.example.com"
	base.ModelAdapters = []ModelAdapterConfig{validAdapter("A", "gpt-5")}
	if _, err := store.Save(ctx, base); err != nil {
		t.Fatal(err)
	}

	// 场景 A：前端 carry-through 后 patch 带 tabServerBaseURL → 保留。
	patchWith := Config{
		BackendListenAddr: DefaultBackendListenAddr,
		ProxyListenAddr:   DefaultProxyListenAddr,
		Routing:            RoutingConfig{Mode: DefaultRoutingMode, TabServerBaseURL: "https://tab.example.com"},
		ModelAdapters:     []ModelAdapterConfig{validAdapter("A", "gpt-5")},
	}
	merged, err := store.MergeUserPatch(ctx, patchWith)
	if err != nil {
		t.Fatalf("MergeUserPatch: %v", err)
	}
	if merged.Routing.TabServerBaseURL != "https://tab.example.com" {
		t.Errorf("F-02: tabServerBaseURL lost when patch carries it (got %q)",
			merged.Routing.TabServerBaseURL)
	}

	// 场景 B：前端漏带（patch 只带 mode）→ merge 覆盖为空。
	// 这是前端必须 carry-through 的契约证明：后端 merge 忠实反映 patch 值。
	patchWithout := Config{
		BackendListenAddr: DefaultBackendListenAddr,
		ProxyListenAddr:   DefaultProxyListenAddr,
		Routing:           RoutingConfig{Mode: DefaultRoutingMode},
		ModelAdapters:     []ModelAdapterConfig{validAdapter("A", "gpt-5")},
	}
	merged2, err := store.MergeUserPatch(ctx, patchWithout)
	if err != nil {
		t.Fatalf("MergeUserPatch: %v", err)
	}
	if merged2.Routing.TabServerBaseURL != "" {
		t.Errorf("F-02: merge should reflect patch value (got %q, want empty when patch omits)",
			merged2.Routing.TabServerBaseURL)
	}
}
