package forwarder

import (
	"testing"
	"time"
)

// TestRecordTurnFinalizedSnapshotCarriesModelID 覆盖 N-12：
// recordTurnFinalizedSnapshot 落盘的 turn 事件必须携带 ModelID/ModelName/Provider
// （来自 LookupEvent 聚合的 provider_call 事件）。此前这三个字段恒空，
// dashboard 把 turn_finalized 当 legacy 语义处理：高缓存命中 turn 被误标
// CalibrationAnomaly，且该行 input 成本按 legacy 减 cacheRead 后 clamp 到 0，
// recent_events 表同一 turn 出现两条互相矛盾的成本行。
func TestRecordTurnFinalizedSnapshotCarriesModelID(t *testing.T) {
	dir := t.TempDir()
	store := NewUsageFileStore(dir)
	svc := &Service{usageStore: store}

	const requestID = "req-turn-modelid-1"
	at := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)

	// 先写一条带 ModelID 的 provider_call 事件（高缓存命中场景）。
	if err := store.UpsertEvent(usageFileEvent{
		EventID:         usageEventID(requestID, "call-1"),
		Kind:            usageEventKindProvider,
		At:              at,
		InputTokens:     200,
		OutputTokens:    50,
		CacheReadTokens: 1800,
		CacheWriteTokens: 0,
		UsagePresent:    true,
		ModelID:         "claude-opus-4-8",
		ModelName:       "Claude Opus 4.8",
		Provider:        "anthropic",
	}); err != nil {
		t.Fatalf("seed provider_call: %v", err)
	}

	// 触发 turn 快照写入（status=done）。
	if err := svc.recordTurnFinalizedSnapshot(nil, "conv-1", 1, requestID, "done", ""); err != nil {
		t.Fatalf("recordTurnFinalizedSnapshot: %v", err)
	}

	// 读回 turn 事件，断言 ModelID/ModelName/Provider 已带上。
	turnEventID := turnUsageEventID("conv-1", 1, requestID)
	turnEvent, ok, err := store.LookupEvent(turnEventID)
	if err != nil {
		t.Fatalf("LookupEvent turn: %v", err)
	}
	if !ok {
		t.Fatalf("turn event %q not found", turnEventID)
	}
	if turnEvent.ModelID != "claude-opus-4-8" {
		t.Errorf("turn event ModelID = %q, want claude-opus-4-8 (empty → dashboard legacy 误判)", turnEvent.ModelID)
	}
	if turnEvent.ModelName != "Claude Opus 4.8" {
		t.Errorf("turn event ModelName = %q, want Claude Opus 4.8", turnEvent.ModelName)
	}
	if turnEvent.Provider != "anthropic" {
		t.Errorf("turn event Provider = %q, want anthropic", turnEvent.Provider)
	}
	if turnEvent.Kind != usageEventKindTurn {
		t.Errorf("turn event Kind = %q, want %q", turnEvent.Kind, usageEventKindTurn)
	}
}
