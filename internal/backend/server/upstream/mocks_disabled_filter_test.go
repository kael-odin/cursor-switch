package upstream

import (
	"testing"

	legacyruntime "cursor/internal/runtime"
)

// TestCollectModelAdapterRefsFiltersDisabled 验证 F-08：
// disabled adapter 不进 UI 模型目录与默认选择列表。
func TestCollectModelAdapterRefsFiltersDisabled(t *testing.T) {
	adapters := []legacyruntime.ModelAdapterConfig{
		{ID: "chan-A", ModelID: "gpt-5", Enabled: true},
		{ID: "chan-B", ModelID: "gpt-5", Enabled: false}, // disabled，应被过滤
		{ID: "chan-C", ModelID: "claude", Enabled: true},
		{ID: "chan-D", ModelID: "gemini", Enabled: false}, // disabled，应被过滤
		{ID: "", ModelID: "empty-id", Enabled: true},      // 空 ID，应被过滤
	}
	refs := collectModelAdapterRefs(adapters)
	want := []string{"chan-A", "chan-C"}
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

// TestBuildAvailableModelEntriesFiltersDisabled 验证 AvailableModels 的 models 列表也过滤 disabled。
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
	if name != "chan-A" {
		t.Errorf("expected chan-A, got %q", name)
	}
}
