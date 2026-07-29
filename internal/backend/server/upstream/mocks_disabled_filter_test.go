package upstream

import (
	"testing"

	legacyruntime "cursor/internal/runtime"
)

// TestCollectModelAdapterRefsFiltersDisabled 验证 F-08：
// disabled adapter 不进 UI 模型目录与默认选择列表。
//
// F-01（2026-07-29）：refs 现返回**逻辑 modelID**（非渠道 ID），故此处断言 modelID。
func TestCollectModelAdapterRefsFiltersDisabled(t *testing.T) {
	adapters := []legacyruntime.ModelAdapterConfig{
		{ID: "chan-A", ModelID: "gpt-5", Enabled: true},
		{ID: "chan-B", ModelID: "gpt-5", Enabled: false}, // disabled，应被过滤
		{ID: "chan-C", ModelID: "claude", Enabled: true},
		{ID: "chan-D", ModelID: "gemini", Enabled: false}, // disabled，应被过滤
		{ID: "", ModelID: "empty-id", Enabled: true},      // 空 ID 不再阻拦（F-01 用 modelID），但 modelID 非空仍进
	}
	refs := collectModelAdapterRefs(adapters)
	want := []string{"gpt-5", "claude", "empty-id"}
	if len(refs) != len(want) {
		t.Fatalf("got %v, want %v", refs, want)
	}
	for i, r := range refs {
		if r != want[i] {
			t.Errorf("refs[%d] = %q, want %q", i, r, want[i])
		}
	}
}

// TestCollectModelAdapterRefsAllDisabled 验证全部 disabled 时返回空——
// 此时 defaultModel 应回退为空串而非首个 disabled ID。
func TestCollectModelAdapterRefsAllDisabled(t *testing.T) {
	adapters := []legacyruntime.ModelAdapterConfig{
		{ID: "chan-A", ModelID: "gpt-5", Enabled: false},
		{ID: "chan-B", ModelID: "gpt-5", Enabled: false},
	}
	refs := collectModelAdapterRefs(adapters)
	if len(refs) != 0 {
		t.Fatalf("expected empty refs when all disabled, got %v", refs)
	}
}

// TestCollectModelAdapterRefsDedupesByModelID 验证 F-01：同 modelID 多 adapter
// 在 refs（defaultModel/fallbackModels）里只出现一次——选中任一都激活该 modelID
// 全部候选链，重复无意义。按 Priority 升序，主候选所属 modelID 排前作 defaultModel。
func TestCollectModelAdapterRefsDedupesByModelID(t *testing.T) {
	adapters := []legacyruntime.ModelAdapterConfig{
		{ID: "chan-B", ModelID: "gpt-5", Enabled: true, Priority: 1}, // 备
		{ID: "chan-A", ModelID: "gpt-5", Enabled: true, Priority: 0}, // 主（Priority 小）
		{ID: "chan-C", ModelID: "claude", Enabled: true, Priority: 0},
	}
	refs := collectModelAdapterRefs(adapters)
	// chan-A(P0) 先于 chan-B(P1)，gpt-5 去重只留一次，置首位；claude 次之。
	want := []string{"gpt-5", "claude"}
	if len(refs) != len(want) {
		t.Fatalf("got %v, want %v", refs, want)
	}
	for i, r := range refs {
		if r != want[i] {
			t.Errorf("refs[%d] = %q, want %q", i, r, want[i])
		}
	}
}

// TestBuildAvailableModelEntriesFiltersDisabled 验证 AvailableModels 的 models 列表也过滤 disabled。
//
// F-01：entry 的 name 现为逻辑 modelID（非渠道 ID）。
func TestBuildAvailableModelEntriesFiltersDisabled(t *testing.T) {
	adapters := []legacyruntime.ModelAdapterConfig{
		{ID: "chan-A", ModelID: "gpt-5", DisplayName: "GPT-5", TooltipData: "tip", Enabled: true},
		{ID: "chan-B", ModelID: "claude", DisplayName: "Claude", TooltipData: "tip", Enabled: false},
	}
	entries := buildAvailableModelEntries(adapters)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (disabled filtered), got %d", len(entries))
	}
	name, _ := entries[0]["name"].(string)
	if name != "gpt-5" {
		t.Errorf("expected name=gpt-5 (modelID, F-01), got %q", name)
	}
	serverModelName, _ := entries[0]["serverModelName"].(string)
	if serverModelName != "gpt-5" {
		t.Errorf("expected serverModelName=gpt-5 (F-01), got %q", serverModelName)
	}
}
