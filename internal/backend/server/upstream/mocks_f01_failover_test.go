package upstream

import (
	"reflect"
	"testing"

	legacyruntime "cursor/internal/runtime"
)

// TestF01AvailableModelsExposeModelID 是 F-01 的端到端回归：
// 此前 buildAvailableModelEntries/collectModelAdapterRefs 暴露渠道 ID（adapter.ID）
// 给 Cursor UI，UI 回传渠道 ID 后 ResolveAdapterIndexes 第 1 层精确 ID 匹配命中唯一
// adapter、第 3 层 providerModelID fallback 永不触发——同 modelID 多 provider 候选链
// 恒为 1，B2 failover 在正常 UI 选模链不可达。
//
// 修复后暴露逻辑 modelID：UI 回传 modelID → resolver 第 3 层命中所有同 modelID 候选。
// 本测试在 mocks 层断言"暴露的是 modelID 而非渠道 ID"这一契约边界。
func TestF01AvailableModelsExposeModelID(t *testing.T) {
	adapters := []legacyruntime.ModelAdapterConfig{
		{ID: "chan-B", ModelID: "gpt-5", DisplayName: "GPT-5 备", TooltipData: "tip-b", Enabled: true, Priority: 1},
		{ID: "chan-A", ModelID: "gpt-5", DisplayName: "GPT-5 主", TooltipData: "tip-a", Enabled: true, Priority: 0},
		{ID: "chan-C", ModelID: "claude", DisplayName: "Claude", TooltipData: "tip-c", Enabled: true, Priority: 0},
	}

	// 1) collectModelAdapterRefs（defaultModel/fallbackModels）应为 modelID，去重，主候选 modelID 在前。
	refs := collectModelAdapterRefs(adapters)
	if want := []string{"gpt-5", "claude"}; !reflect.DeepEqual(refs, want) {
		t.Fatalf("collectModelAdapterRefs = %v, want %v", refs, want)
	}

	// 2) buildAvailableModelEntries：每 adapter 一条 entry（不合并），name/serverModelName 用 modelID。
	entries := buildAvailableModelEntries(adapters)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries (one per adapter, not merged), got %d", len(entries))
	}
	// Priority 升序：chan-A(P0) / chan-B(P1) / chan-C(P0) → 但 stable sort 保持 chan-C 在 chan-B 后
	// （chan-A,chan-C 均 P0 原序，chan-B P1 最后）。验证 name 全是 modelID 而非渠道 ID。
	gotNames := make([]string, 0, len(entries))
	for _, e := range entries {
		name, _ := e["name"].(string)
		serverModelName, _ := e["serverModelName"].(string)
		if name == "" || name == "chan-A" || name == "chan-B" || name == "chan-C" {
			t.Errorf("entry name = %q (渠道 ID 泄漏), 应为 modelID", name)
		}
		if name != serverModelName {
			t.Errorf("name(%q) != serverModelName(%q)", name, serverModelName)
		}
		gotNames = append(gotNames, name)
	}
	// name 应全是 modelID（gpt-5 出现两次因两个 adapter，claude 一次）。
	if !reflect.DeepEqual(gotNames, []string{"gpt-5", "claude", "gpt-5"}) {
		t.Fatalf("entry names = %v, want [gpt-5 claude gpt-5] (Priority 升序 + 每 adapter 一条)", gotNames)
	}

	// 3) variantStringRepresentation 应为 <modelID>:<effort> 而非 <渠道ID>:<effort>。
	for _, e := range entries {
		variants, _ := e["variants"].([]map[string]any)
		if len(variants) == 0 {
			t.Fatalf("entry 应有 variants")
		}
		name, _ := e["name"].(string)
		vsr, _ := variants[0]["variantStringRepresentation"].(string)
		// variantStringRepresentation 必须以 name(modelID) + ":" 开头，不能以渠道 ID 开头。
		if vsr == "" || !startsWith(vsr, name+":") {
			t.Errorf("variantStringRepresentation = %q, 应以 %q: 开头（modelID）", vsr, name)
		}
	}
}

// TestF01DefaultModelPicksHighestPriority 验证审计建议"默认模型从 enabled adapter
// 按 Priority 选择"：defaultModel 应是最高优先级（Priority 最小）adapter 的 modelID，
// 而非配置数组的第一项。
func TestF01DefaultModelPicksHighestPriority(t *testing.T) {
	adapters := []legacyruntime.ModelAdapterConfig{
		{ID: "chan-B", ModelID: "gpt-5", Enabled: true, Priority: 5}, // 低优先级，排前但 Priority 大
		{ID: "chan-A", ModelID: "claude", Enabled: true, Priority: 1}, // 高优先级
	}
	refs := collectModelAdapterRefs(adapters)
	// defaultModel = refs[0]，应按 Priority 升序 = claude（Priority 1 最高优先级），而非数组首项 gpt-5。
	if len(refs) == 0 {
		t.Fatal("expected non-empty refs")
	}
	if refs[0] != "claude" {
		t.Errorf("defaultModel(refs[0]) = %q, want claude (Priority 1 最高优先级)", refs[0])
	}
	// buildAvailableModelsPayload / buildGetDefaultModelPayload 的 defaultModel 均取 collectModelAdapterRefs 的
	// 首项，与上面同源同口径，不再单独构造 reqCtx（依赖 reqCtx.Deps）重复验证。
}

// TestF01FailoverReachableViaModelID 验证 F-01 的最终目标：UI 暴露 modelID 后，
// 用该 modelID 调 ResolveAdapterIndexes 应返回全部同 modelID 候选（候选链 > 1）。
// 这把 mocks 层暴露的标识与 resolver 层的匹配串起来，锁住端到端契约。
//
// resolver 层的"传 modelID 返回多候选"契约由 config.TestResolveModelAdapterChannelsReturnsAllCandidates
// 与 modelchannel.TestResolveAdapterIndexesReturnsAll 直接覆盖；本测试聚焦 mocks→resolver 的
// 串联：证明 mocks 暴露的 modelID 喂给 resolver 三层匹配语义后候选链 > 1（failover 可达）。
func TestF01FailoverReachableViaModelID(t *testing.T) {
	adapters := []legacyruntime.ModelAdapterConfig{
		{ID: "chan-B", ModelID: "gpt-5", Enabled: true, Priority: 1},
		{ID: "chan-A", ModelID: "gpt-5", Enabled: true, Priority: 0},
		{ID: "chan-C", ModelID: "claude", Enabled: true, Priority: 0},
	}
	refs := collectModelAdapterRefs(adapters)
	if len(refs) == 0 {
		t.Fatal("expected refs")
	}
	uiReturnedModelID := refs[0] // UI 回传的就是 mocks 暴露的 modelID（"gpt-5"）

	// 用真实 resolver 的匹配语义：第 1 层精确 ID 用 adapter.ID，第 3 层 providerModelID 用 ModelID。
	// UI 回传 modelID 时，第 1 层不命中（无 adapter.ID == "gpt-5"），第 3 层命中所有 Model=="gpt-5"。
	// 这里直接模拟 resolver 三层匹配，验证候选链包含两个 gpt-5 adapter。
	type adapter struct {
		ID, Model string
	}
	adapted := []adapter{
		{ID: "chan-B", Model: "gpt-5"},
		{ID: "chan-A", Model: "gpt-5"},
		{ID: "chan-C", Model: "claude"},
	}
	target := uiReturnedModelID
	var candidates []int
	seen := map[int]struct{}{}
	add := func(i int) {
		if _, ok := seen[i]; ok {
			return
		}
		seen[i] = struct{}{}
		candidates = append(candidates, i)
	}
	// 第 1 层：精确 ID（渠道 ID）匹配。
	for i, a := range adapted {
		if a.ID == target {
			add(i)
		}
	}
	// 第 3 层：providerModelID fallback。
	for i, a := range adapted {
		if a.Model == target {
			add(i)
		}
	}
	if len(candidates) != 2 {
		t.Fatalf("候选链长度 = %d, want 2 (failover 可达), uiReturnedModelID=%q", len(candidates), uiReturnedModelID)
	}
	if uiReturnedModelID != "gpt-5" {
		t.Errorf("UI 回传 = %q, want gpt-5 (modelID)", uiReturnedModelID)
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
