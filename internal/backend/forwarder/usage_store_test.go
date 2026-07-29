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

// TestUsageEventIndexSurvivesRecentEventsTruncation 是 H5 的回归测试：
// RecentEvents 被截断到 500 条后，老事件的 requestID 仍必须在 EventIndex 里可查，
// 否则 recordTurnFinalizedSnapshot 用老 requestID LookupEvent 时聚合到 0 token，
// 导致 turn 计费漏算。
func TestUsageEventIndexSurvivesRecentEventsTruncation(t *testing.T) {
	dir := t.TempDir()
	store := NewUsageFileStore(dir)
	base := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)

	// 第一条：requestID = "req-old"，1000 input。
	old := usageFileEvent{
		EventID:      "req-old",
		Kind:         usageEventKindProvider,
		At:           base,
		InputTokens:  1000,
		OutputTokens: 100,
		ModelID:      "gpt-5",
	}
	if err := store.UpsertEvent(old); err != nil {
		t.Fatalf("UpsertEvent old: %v", err)
	}

	// 灌入超过 usageRecentEventLimit 条新事件，把 "req-old" 挤出 RecentEvents 列表。
	for i := 0; i < usageRecentEventLimit+5; i++ {
		evt := usageFileEvent{
			EventID: "req-fill-" + itoa(i),
			Kind:     usageEventKindProvider,
			At:       base.Add(time.Duration(i+1) * time.Second),
			InputTokens: 10,
			ModelID:  "gpt-5",
		}
		if err := store.UpsertEvent(evt); err != nil {
			t.Fatalf("UpsertEvent fill %d: %v", i, err)
		}
	}

	// "req-old" 应已不在 RecentEvents 里（被挤出）。
	doc, err := readUsageFileDocument(store.path)
	if err != nil {
		t.Fatalf("readUsageFileDocument: %v", err)
	}
	for _, e := range doc.RecentEvents {
		if e.EventID == "req-old" {
			t.Fatalf("req-old should have been truncated out of RecentEvents")
		}
	}

	// 但 EventIndex 必须仍能查到它——这是 H5 修复的核心。
	agg, ok, err := store.LookupEvent("req-old")
	if err != nil {
		t.Fatalf("LookupEvent req-old: %v", err)
	}
	if !ok {
		t.Fatalf("LookupEvent req-old = not found; EventIndex must survive RecentEvents truncation (H5)")
	}
	if agg.InputTokens != 1000 {
		t.Errorf("LookupEvent req-old input = %d, want 1000 (full aggregation, not 0)", agg.InputTokens)
	}
}

// TestPruneUsageEventIndexLimit 验证 EventIndex 超出上限时按 At 淘汰最老的、保留最近的。
func TestPruneUsageEventIndexLimit(t *testing.T) {
	// 构造 usageEventIndexLimit+10 条事件，时间递增。
	index := make(map[string]usageFileEvent, usageEventIndexLimit+10)
	base := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	for i := 0; i < usageEventIndexLimit+10; i++ {
		id := "evt-" + itoa(i)
		index[id] = usageFileEvent{
			EventID: id,
			At:      base.Add(time.Duration(i) * time.Second),
		}
	}
	pruned := pruneUsageEventIndex(index)
	if len(pruned) != usageEventIndexLimit {
		t.Errorf("pruned size = %d, want %d", len(pruned), usageEventIndexLimit)
	}
	// 最老的 evt-0 应被淘汰，最新的 evt-(limit+9) 应保留。
	if _, ok := pruned["evt-0"]; ok {
		t.Errorf("evt-0 (oldest) should have been pruned")
	}
	if _, ok := pruned["evt-"+itoa(usageEventIndexLimit+9)]; !ok {
		t.Errorf("evt-(limit+9) (newest) should have been kept")
	}
}

// itoa 是避免在测试里引入 strconv 的本地最小实现。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// --- F-05: legacy usage 升级一次性构建完整索引 ---

// writeRawUsageDocument 直接把 doc 写盘，绕过 UpsertEvent 的增量逻辑，
// 用于手造 legacy 文件（EventIndex 空 + RecentEvents 非空）测 F-05 回填。
func writeRawUsageDocument(t *testing.T, path string, doc usageFileDocument) {
	t.Helper()
	if err := writeJSONFileAtomic(path, doc); err != nil {
		t.Fatalf("writeRawUsageDocument: %v", err)
	}
}

func TestReadUsageDocumentBackfillsLegacyIndex(t *testing.T) {
	dir := t.TempDir()
	store := NewUsageFileStore(dir)
	base := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	// 手造 legacy 文件：SchemaVersion=1、EventIndex=nil、RecentEvents 含 3 条不同 eventID。
	legacy := usageFileDocument{
		SchemaVersion: 1,
		UpdatedAt:     base,
		RecentEvents: []usageFileEvent{
			{EventID: "req-a", Kind: usageEventKindProvider, At: base, InputTokens: 100, ModelID: "gpt-5"},
			{EventID: "req-b", Kind: usageEventKindProvider, At: base.Add(time.Second), InputTokens: 200, ModelID: "gpt-5"},
			{EventID: "req-c", Kind: usageEventKindProvider, At: base.Add(2 * time.Second), InputTokens: 300, ModelID: "gpt-5"},
		},
	}
	writeRawUsageDocument(t, store.path, legacy)

	doc, err := readUsageFileDocument(store.path)
	if err != nil {
		t.Fatalf("readUsageFileDocument: %v", err)
	}
	if len(doc.EventIndex) != 3 {
		t.Fatalf("F-05: backfilled EventIndex len = %d, want 3", len(doc.EventIndex))
	}
	for _, e := range legacy.RecentEvents {
		got, ok := doc.EventIndex[e.EventID]
		if !ok {
			t.Errorf("F-05: EventIndex missing backfilled entry %q", e.EventID)
			continue
		}
		if got.InputTokens != e.InputTokens {
			t.Errorf("F-05: EventIndex[%q].InputTokens = %d, want %d", e.EventID, got.InputTokens, e.InputTokens)
		}
	}
}

func TestReadUsageDocumentPreservesExistingIndex(t *testing.T) {
	dir := t.TempDir()
	store := NewUsageFileStore(dir)
	base := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	// EventIndex 已有 req-a（非空），且 RecentEvents 含一条 EventIndex 里没有的 req-b。
	// 回填只补空索引——非空索引不应被 RecentEvents 覆盖。
	legacy := usageFileDocument{
		SchemaVersion: 1,
		UpdatedAt:     base,
		RecentEvents: []usageFileEvent{
			{EventID: "req-a", Kind: usageEventKindProvider, At: base, InputTokens: 100, ModelID: "gpt-5"},
			{EventID: "req-b", Kind: usageEventKindProvider, At: base.Add(time.Second), InputTokens: 200, ModelID: "gpt-5"},
		},
		EventIndex: map[string]usageFileEvent{
			"req-a": {EventID: "req-a", Kind: usageEventKindProvider, At: base, InputTokens: 999, ModelID: "gpt-5"},
		},
	}
	writeRawUsageDocument(t, store.path, legacy)

	doc, err := readUsageFileDocument(store.path)
	if err != nil {
		t.Fatalf("readUsageFileDocument: %v", err)
	}
	// req-a 应保留 EventIndex 里的原值（999），不被 RecentEvents 的 100 覆盖。
	if got := doc.EventIndex["req-a"]; got.InputTokens != 999 {
		t.Errorf("F-05: existing index req-a should be preserved, InputTokens=%d want 999", got.InputTokens)
	}
	// req-b 不应被回填（索引非空时不触发回填）。
	if _, ok := doc.EventIndex["req-b"]; ok {
		t.Error("F-05: req-b should NOT be backfilled when EventIndex already non-empty (only backfill empty index)")
	}
}

func TestReadUsageDocumentBackfillPersistsOnNextWrite(t *testing.T) {
	dir := t.TempDir()
	store := NewUsageFileStore(dir)
	base := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	legacy := usageFileDocument{
		SchemaVersion: 1,
		UpdatedAt:     base,
		RecentEvents: []usageFileEvent{
			{EventID: "req-old", Kind: usageEventKindProvider, At: base, InputTokens: 500, ModelID: "gpt-5"},
		},
	}
	writeRawUsageDocument(t, store.path, legacy)

	// 读一次触发回填（内存态），再写一个新 eventID——写路径读盘得回填 doc，写出持久化。
	newEvt := usageFileEvent{
		EventID:     "req-new",
		Kind:        usageEventKindProvider,
		At:          base.Add(time.Second),
		InputTokens: 50,
		ModelID:     "gpt-5",
	}
	if err := store.UpsertEvent(newEvt); err != nil {
		t.Fatalf("UpsertEvent new: %v", err)
	}

	doc, err := readUsageFileDocument(store.path)
	if err != nil {
		t.Fatalf("readUsageFileDocument: %v", err)
	}
	if _, ok := doc.EventIndex["req-old"]; !ok {
		t.Error("F-05: backfilled req-old should persist after next write")
	}
	if _, ok := doc.EventIndex["req-new"]; !ok {
		t.Error("F-05: newly written req-new should be in EventIndex")
	}
}

func TestReadUsageDocumentBackfillPrunedToLimit(t *testing.T) {
	dir := t.TempDir()
	store := NewUsageFileStore(dir)
	base := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	// RecentEvents 上限 500，远小于 usageEventIndexLimit=5000，故回填后等长。
	events := make([]usageFileEvent, 0, usageRecentEventLimit)
	for i := 0; i < usageRecentEventLimit; i++ {
		events = append(events, usageFileEvent{
			EventID:     "req-" + itoa(i),
			Kind:        usageEventKindProvider,
			At:          base.Add(time.Duration(i) * time.Second),
			InputTokens: 1,
			ModelID:     "gpt-5",
		})
	}
	writeRawUsageDocument(t, store.path, usageFileDocument{
		SchemaVersion: 1,
		UpdatedAt:     base,
		RecentEvents:  events,
	})

	doc, err := readUsageFileDocument(store.path)
	if err != nil {
		t.Fatalf("readUsageFileDocument: %v", err)
	}
	if len(doc.EventIndex) != usageRecentEventLimit {
		t.Errorf("F-05: backfilled index len = %d, want %d (no loss, no over-prune)", len(doc.EventIndex), usageRecentEventLimit)
	}
	if len(doc.EventIndex) > usageEventIndexLimit {
		t.Errorf("F-05: index must not exceed usageEventIndexLimit %d, got %d", usageEventIndexLimit, len(doc.EventIndex))
	}
}

func TestLookupEventHitsBackfilledIndexNotLinearScan(t *testing.T) {
	dir := t.TempDir()
	store := NewUsageFileStore(dir)
	base := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	// legacy 文件：req-old 在 RecentEvents 里、EventIndex 空。
	writeRawUsageDocument(t, store.path, usageFileDocument{
		SchemaVersion: 1,
		UpdatedAt:     base,
		RecentEvents: []usageFileEvent{
			{EventID: "req-old", Kind: usageEventKindProvider, At: base, InputTokens: 777, ModelID: "gpt-5"},
		},
	})

	// 回填后 LookupEvent 应直接命中索引（聚合值与原事件一致）。
	agg, ok, err := store.LookupEvent("req-old")
	if err != nil {
		t.Fatalf("LookupEvent req-old: %v", err)
	}
	if !ok {
		t.Fatal("F-05: req-old should be found via backfilled index")
	}
	if agg.InputTokens != 777 {
		t.Errorf("F-05: req-old aggregated input = %d, want 777", agg.InputTokens)
	}

	// 验证回填已写入磁盘：读回的 doc.EventIndex 非空（LookupEvent 触发了读+回填，
	// 但 LookupEvent 不写盘——回填只在内存。此处只断言内存态索引非空，持久化由
	// 下一次 UpsertEvent 保证，见 TestReadUsageDocumentBackfillPersistsOnNextWrite）。
	// 为避免误读契约，这里只断言 LookupEvent 返回正确，不额外断言磁盘态。
	_ = agg
}

// TestUpsertTurnFinalizedAggregatesInOneTransaction 覆盖 N-28：
// UpsertTurnFinalized 在单次 locked 读改写内，从同一内存 doc 的 EventIndex 聚合
// provider_call 事件回填到 turn 事件，不再先 LookupEvent（全量读）再 UpsertEvent（又一次全量读）。
// 语义与原 LookupEvent+UpsertEvent 一致：turn 事件继承聚合的 token 数与模型归属（N-12）。
func TestUpsertTurnFinalizedAggregatesInOneTransaction(t *testing.T) {
	dir := t.TempDir()
	store := NewUsageFileStore(dir)
	at := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	// 两条同 requestID 前缀的 provider_call 子事件（B2 failover 链各候选）。
	if err := store.UpsertEvent(usageFileEvent{
		EventID: "req-1::call-A", Kind: usageEventKindProvider, At: at,
		InputTokens: 1000, OutputTokens: 200, ModelID: "gpt-5", ModelName: "GPT-5", Provider: "openai",
	}); err != nil {
		t.Fatalf("UpsertEvent call-A: %v", err)
	}
	if err := store.UpsertEvent(usageFileEvent{
		EventID: "req-1::call-B", Kind: usageEventKindProvider, At: at.Add(time.Second),
		InputTokens: 500, OutputTokens: 100, ModelID: "gpt-5", ModelName: "GPT-5", Provider: "openai",
	}); err != nil {
		t.Fatalf("UpsertEvent call-B: %v", err)
	}

	turnEventID := "turn::conv-1::1"
	if err := store.UpsertTurnFinalized(usageFileEvent{
		EventID: turnEventID,
		Kind:    usageEventKindTurn,
		Status:  usageTurnStatusDone,
		At:      at.Add(2 * time.Second),
	}, "req-1"); err != nil {
		t.Fatalf("UpsertTurnFinalized: %v", err)
	}

	turn, ok, err := store.LookupEvent(turnEventID)
	if err != nil || !ok {
		t.Fatalf("LookupEvent turn: ok=%v err=%v", ok, err)
	}
	// 聚合两条 provider_call：input 1500, output 300。
	if turn.InputTokens != 1500 {
		t.Errorf("turn InputTokens = %d, want 1500 (aggregated)", turn.InputTokens)
	}
	if turn.OutputTokens != 300 {
		t.Errorf("turn OutputTokens = %d, want 300 (aggregated)", turn.OutputTokens)
	}
	// N-12：模型/Provider 归属必须继承。
	if turn.ModelID != "gpt-5" || turn.Provider != "openai" {
		t.Errorf("turn model/provider = (%q,%q), want (gpt-5,openai)", turn.ModelID, turn.Provider)
	}
	if turn.Kind != usageEventKindTurn {
		t.Errorf("turn Kind = %q, want %q", turn.Kind, usageEventKindTurn)
	}
}

// TestLookupEventExactKeyShortCircuit 覆盖 N-28：精确匹配走直查热路径，
// 不依赖前缀扫描。单条 provider_call（eventID == requestID）LookupEvent 应 O(1) 命中。
func TestLookupEventExactKeyShortCircuit(t *testing.T) {
	dir := t.TempDir()
	store := NewUsageFileStore(dir)
	at := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	if err := store.UpsertEvent(usageFileEvent{
		EventID: "req-exact", Kind: usageEventKindProvider, At: at,
		InputTokens: 4321, ModelID: "claude-5",
	}); err != nil {
		t.Fatalf("UpsertEvent: %v", err)
	}
	agg, ok, err := store.LookupEvent("req-exact")
	if err != nil || !ok {
		t.Fatalf("LookupEvent req-exact: ok=%v err=%v", ok, err)
	}
	if agg.InputTokens != 4321 {
		t.Errorf("exact lookup input = %d, want 4321", agg.InputTokens)
	}
	if agg.ModelID != "claude-5" {
		t.Errorf("exact lookup model = %q, want claude-5", agg.ModelID)
	}
}
