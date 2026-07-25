package forwarder

import (
	"testing"
	"time"
)

// TestUsageDailyByModel verifies daily rollup carries a by_model dimension,
// so the dashboard can compute precise per-model daily cost instead of a
// weighted-average approximation.
func TestUsageDailyByModel(t *testing.T) {
	dir := t.TempDir()
	store := NewUsageFileStore(dir)
	at := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)

	// Insert a provider_call event for gpt-5.
	if err := store.UpsertEvent(usageFileEvent{
		EventID:     "evt-1",
		Kind:        usageEventKindProvider,
		At:          at,
		InputTokens: 1000,
		OutputTokens: 500,
		ModelID:     "gpt-5",
		ModelName:   "GPT-5",
		Provider:    "openai",
	}); err != nil {
		t.Fatalf("UpsertEvent evt-1: %v", err)
	}

	doc, err := readUsageFileDocument(store.path)
	if err != nil {
		t.Fatalf("readUsageFileDocument: %v", err)
	}
	if len(doc.Daily) != 1 {
		t.Fatalf("expect 1 daily entry, got %d", len(doc.Daily))
	}
	dm, ok := doc.Daily[0].ByModel["gpt-5"]
	if !ok {
		t.Fatalf("expect daily.by_model[gpt-5], got %v", doc.Daily[0].ByModel)
	}
	if dm.InputTokens != 1000 || dm.OutputTokens != 500 {
		t.Errorf("daily by_model tokens = (%d,%d), want (1000,500)", dm.InputTokens, dm.OutputTokens)
	}
	if dm.ProviderCalls != 1 {
		t.Errorf("daily by_model provider_calls = %d, want 1", dm.ProviderCalls)
	}

	// Insert another provider_call for a different model same day.
	if err := store.UpsertEvent(usageFileEvent{
		EventID:      "evt-2",
		Kind:         usageEventKindProvider,
		At:           at,
		InputTokens:  2000,
		OutputTokens: 100,
		ModelID:      "claude-opus-4-8",
		ModelName:    "Claude Opus 4.8",
		Provider:     "anthropic",
	}); err != nil {
		t.Fatalf("UpsertEvent evt-2: %v", err)
	}
	doc, _ = readUsageFileDocument(store.path)
	if len(doc.Daily[0].ByModel) != 2 {
		t.Errorf("expect 2 models in daily by_model, got %d", len(doc.Daily[0].ByModel))
	}
}

// TestUsageDailyByModelUpsertRollback verifies that upserting the same event ID
// with new values correctly rolls back the old daily by_model delta — not just
// the top-level totals. This is the bug class that the negateUsageFileDelta fix
// (preserving modelID on negation) addresses.
func TestUsageDailyByModelUpsertRollback(t *testing.T) {
	dir := t.TempDir()
	store := NewUsageFileStore(dir)
	at := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)

	// First write: 1000 input for gpt-5.
	if err := store.UpsertEvent(usageFileEvent{
		EventID:     "evt-1",
		Kind:        usageEventKindProvider,
		At:          at,
		InputTokens: 1000,
		ModelID:     "gpt-5",
	}); err != nil {
		t.Fatalf("UpsertEvent first: %v", err)
	}
	// Second write: same ID, corrected to 400 input (should subtract old, add new).
	if err := store.UpsertEvent(usageFileEvent{
		EventID:     "evt-1",
		Kind:        usageEventKindProvider,
		At:          at,
		InputTokens: 400,
		ModelID:     "gpt-5",
	}); err != nil {
		t.Fatalf("UpsertEvent second: %v", err)
	}

	doc, err := readUsageFileDocument(store.path)
	if err != nil {
		t.Fatalf("readUsageFileDocument: %v", err)
	}
	dm := doc.Daily[0].ByModel["gpt-5"]
	if dm.InputTokens != 400 {
		t.Errorf("after upsert, daily by_model input = %d, want 400 (rollback+reapply)", dm.InputTokens)
	}
	if dm.ProviderCalls != 1 {
		t.Errorf("after upsert, daily by_model provider_calls = %d, want 1", dm.ProviderCalls)
	}
	// Top-level by_model should also be rolled back (this was the pre-existing bug
	// that the same negate fix corrects).
	top := doc.ByModel["gpt-5"]
	if top.InputTokens != 400 {
		t.Errorf("after upsert, top-level by_model input = %d, want 400", top.InputTokens)
	}
}

// TestUsageDailyByModelAcrossDays verifies separate days get separate by_model buckets.
func TestUsageDailyByModelAcrossDays(t *testing.T) {
	dir := t.TempDir()
	store := NewUsageFileStore(dir)

	for i, day := range []int{25, 26} {
		at := time.Date(2026, 7, day, 10, 0, 0, 0, time.UTC)
		if err := store.UpsertEvent(usageFileEvent{
			EventID:     "evt-" + string(rune('a'+i)),
			Kind:        usageEventKindProvider,
			At:          at,
			InputTokens: int64(100 * (i + 1)),
			ModelID:     "gpt-5",
		}); err != nil {
			t.Fatalf("UpsertEvent day %d: %v", day, err)
		}
	}

	doc, err := readUsageFileDocument(store.path)
	if err != nil {
		t.Fatalf("readUsageFileDocument: %v", err)
	}
	if len(doc.Daily) != 2 {
		t.Fatalf("expect 2 daily entries, got %d", len(doc.Daily))
	}
	byDate := map[string]int64{}
	for _, d := range doc.Daily {
		byDate[d.Date] = d.ByModel["gpt-5"].InputTokens
	}
	if byDate["2026-07-25"] != 100 {
		t.Errorf("day 25 input = %d, want 100", byDate["2026-07-25"])
	}
	if byDate["2026-07-26"] != 200 {
		t.Errorf("day 26 input = %d, want 200", byDate["2026-07-26"])
	}
}
