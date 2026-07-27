package modelchannel

import (
	"reflect"
	"testing"
)

type testAdapter struct {
	ID            string
	Model         string
	Legacy        string
}

func testID(a testAdapter) string             { return a.ID }
func testModel(a testAdapter) string          { return a.Model }
func testLegacy(a testAdapter) string         { return a.Legacy }

func TestResolveAdapterIndexSingleMatch(t *testing.T) {
	adapters := []testAdapter{
		{ID: "a1", Model: "gpt-5"},
		{ID: "a2", Model: "claude-opus-4-7"},
	}
	idx, ok := ResolveAdapterIndex(adapters, "a2", testID, testModel)
	if !ok || idx != 1 {
		t.Errorf("ResolveAdapterIndex(a2) = %d,%v want 1,true", idx, ok)
	}
}

func TestResolveAdapterIndexAmbiguousRejects(t *testing.T) {
	// 同 modelID 两个 adapter → 单数版拒绝（这是现有保护语义）。
	adapters := []testAdapter{
		{ID: "a1", Model: "gpt-5"},
		{ID: "a2", Model: "gpt-5"},
	}
	if _, ok := ResolveAdapterIndex(adapters, "gpt-5", testID, testModel); ok {
		t.Errorf("ResolveAdapterIndex should reject ambiguous modelID match")
	}
}

func TestResolveAdapterIndexesReturnsAll(t *testing.T) {
	// 同 modelID 两个 adapter → 复数版返回全部（B2 候选链）。
	adapters := []testAdapter{
		{ID: "a1", Model: "gpt-5"},
		{ID: "a2", Model: "gpt-5"},
		{ID: "a3", Model: "claude"},
	}
	got := ResolveAdapterIndexes(adapters, "gpt-5", testID, testModel)
	want := []int{0, 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ResolveAdapterIndexes(gpt-5) = %v want %v", got, want)
	}
}

func TestResolveAdapterIndexesExactIDFirst(t *testing.T) {
	// 精确 ID 命中应排最前，即使 modelID 也匹配。
	adapters := []testAdapter{
		{ID: "a1", Model: "gpt-5"},  // modelID 命中
		{ID: "gpt-5", Model: "other"}, // 精确 ID 命中
	}
	got := ResolveAdapterIndexes(adapters, "gpt-5", testID, testModel)
	want := []int{1, 0} // 精确 ID(1) 在前，modelID(0) 在后
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ResolveAdapterIndexes exact-id-first = %v want %v", got, want)
	}
}

func TestResolveAdapterIndexesLegacyID(t *testing.T) {
	adapters := []testAdapter{
		{ID: "a1", Model: "gpt-5", Legacy: "legacy-gpt"},
		{ID: "a2", Model: "gpt-5", Legacy: "legacy-gpt"},
	}
	got := ResolveAdapterIndexes(adapters, "legacy-gpt", testID, testModel, testLegacy)
	want := []int{0, 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ResolveAdapterIndexes(legacy) = %v want %v", got, want)
	}
}

func TestResolveAdapterIndexesDedupes(t *testing.T) {
	// 同一 index 同时被精确 ID 和 modelID 命中，只入列一次。
	adapters := []testAdapter{
		{ID: "gpt-5", Model: "gpt-5"},
	}
	got := ResolveAdapterIndexes(adapters, "gpt-5", testID, testModel)
	want := []int{0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ResolveAdapterIndexes dedup = %v want %v", got, want)
	}
}

func TestResolveAdapterIndexesEmpty(t *testing.T) {
	if got := ResolveAdapterIndexes(nil, "gpt-5", testID, testModel); len(got) != 0 {
		t.Errorf("nil adapters should return empty, got %v", got)
	}
	adapters := []testAdapter{{ID: "a1", Model: "gpt-5"}}
	if got := ResolveAdapterIndexes(adapters, "nonexistent", testID, testModel); len(got) != 0 {
		t.Errorf("no match should return empty, got %v", got)
	}
}

func TestResolveAdapterIndexesMetaAlias(t *testing.T) {
	// meta alias (auto/default/fast) 取 adapters[0] 的 ID。
	adapters := []testAdapter{
		{ID: "first", Model: "gpt-5"},
		{ID: "second", Model: "gpt-5"},
	}
	got := ResolveAdapterIndexes(adapters, "auto", testID, testModel)
	// target 变成 "first"，匹配第 0 个的 ID。
	want := []int{0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("meta alias = %v want %v", got, want)
	}
}
