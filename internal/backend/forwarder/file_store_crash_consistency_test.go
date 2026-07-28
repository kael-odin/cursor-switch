package forwarder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestF13CrashConsistencyNewContextOldState 模拟 F-13 崩溃场景：
// writeContextLocked 写了新 context（Version=N+1），进程在 writeConversationMetaLocked
// 前退出，留下"新 context + 旧 state"（state.ContextVersion 仍是 N）。
//
// 修复前：readConversationLocked 不校验版本，旧 state 的运行时字段（loop 状态/todos/
// plans/lastProviderCall）会与新条目脱节地继续使用——状态回退/重复追加。
// 修复后：检测到 ContextVersion 不一致，作废旧 state 运行时派生字段，normalizeLoadedConversation
// 从新条目重建 loop 状态与 ContextVersion。
func TestF13CrashConsistencyNewContextOldState(t *testing.T) {
	store := NewConversationFileStore(t.TempDir())
	convID := "f13-crash-test"

	// 1. 正常写两轮条目——context 与 state 一致，version 推进。
	if _, _, err := store.AppendEntries(convID, []HistoryEntry{
		{Seq: 1, TurnSeq: 1, Role: "user", Kind: "user_message", Payload: json.RawMessage(`{}`), CreatedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
	consistent, err := store.LoadConversation(convID)
	if err != nil {
		t.Fatal(err)
	}
	consistentVersion := consistent.ContextVersion
	// 旧 state 携带的"陈旧"运行时字段——给 state 塞一些会脱节的值。
	// 用真正持久化（不被 refreshConversationRuntimeState 重新派生清空）的字段：
	// LastProviderCall / TokenDetailsUsedTokens / AutoCompactionPending。
	if _, err := store.UpdateConversationMeta(convID, func(c *ConversationFile) error {
		c.CurrentLoopStatus = "running"
		c.CurrentRequestID = "stale-req"
		c.CurrentTurnSeq = 99
		c.LastProviderCall = &ConversationProviderCall{ModelCallID: "stale-call", Model: "stale-model"}
		c.TokenDetailsUsedTokens = 9999
		c.AutoCompactionPending = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// 2. 模拟崩溃：直接写一个"新 context"（Version 更高、含新条目），state 不动。
	//    这正是 writeContextLocked 成功、writeConversationMetaLocked 未执行的状态。
	newEntry := HistoryEntry{
		Seq: 2, TurnSeq: 2, Role: "user", Kind: "user_message",
		Payload: json.RawMessage(`{}`), CreatedAt: time.Now().UTC(),
	}
	newEntries := []HistoryEntry{newEntry}
	newContext := conversationContextFile{
		SchemaVersion:  conversationSchemaVersion,
		ConversationID: convID,
		Version:        consistentVersion + 1, // 比 state 旧值高
		UpdatedAt:      time.Now().UTC(),
		Items:          newEntries,
	}
	contextBody, err := json.Marshal(newContext)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.contextPath(convID), contextBody, 0o644); err != nil {
		t.Fatal(err)
	}

	// 3. 加载——应检测不一致并作废旧运行时字段、以新 context 条目为准。
	loaded, err := store.LoadConversation(convID)
	if err != nil {
		t.Fatal(err)
	}
	// 条目应以新 context 为准（崩溃已写入的新条目生效，旧条目被覆盖）。
	if len(loaded.Entries) != 1 || loaded.Entries[0].Seq != 2 {
		t.Fatalf("F-13 FAIL: entries should reflect new context (got %d entries, first seq=%d)", len(loaded.Entries), loaded.Entries[0].Seq)
	}
	// ContextVersion 应从新条目重建（=2），而非旧 state 的值。
	if loaded.ContextVersion != 2 {
		t.Errorf("F-13 FAIL: ContextVersion should be rebuilt from new entries (got %d, want 2)", loaded.ContextVersion)
	}
	// 旧 state 的陈旧运行时字段必须被作废。
	if loaded.CurrentLoopStatus == "running" || loaded.CurrentRequestID == "stale-req" || loaded.CurrentTurnSeq == 99 {
		t.Errorf("F-13 FAIL: stale loop state from old state survived crash mismatch: status=%q req=%q turn=%d",
			loaded.CurrentLoopStatus, loaded.CurrentRequestID, loaded.CurrentTurnSeq)
	}
	if loaded.LastProviderCall != nil {
		t.Errorf("F-13 FAIL: stale LastProviderCall should be invalidated, got %+v", loaded.LastProviderCall)
	}
	if loaded.TokenDetailsUsedTokens != 0 {
		t.Errorf("F-13 FAIL: stale TokenDetailsUsedTokens should be invalidated, got %d", loaded.TokenDetailsUsedTokens)
	}
	if loaded.AutoCompactionPending {
		t.Errorf("F-13 FAIL: stale AutoCompactionPending should be invalidated")
	}
	// 稳定元数据应保留（Mode 不随条目变）。
	if loaded.Mode == "" {
		t.Errorf("F-13 FAIL: stable metadata (Mode) should be preserved across crash reconciliation")
	}
	// NextEntrySeq 应从新条目重建（=3）。
	if loaded.NextEntrySeq != 3 {
		t.Errorf("F-13 FAIL: NextEntrySeq should be rebuilt (got %d, want 3)", loaded.NextEntrySeq)
	}
}

// TestF13ConsistentLoadUnchanged 验证 context 与 state 版本一致时（正常路径）
// 不触发作废——条目与元数据正常保留。todos/plans/loop 状态本就由
// refreshConversationRuntimeState/deriveConversationLoopState 从条目派生，一致路径
// 下派生结果与磁盘 state 吻合，不应被作废逻辑误清。
func TestF13ConsistentLoadUnchanged(t *testing.T) {
	store := NewConversationFileStore(t.TempDir())
	convID := "f13-consistent"

	if _, _, err := store.AppendEntries(convID, []HistoryEntry{
		{Seq: 1, TurnSeq: 1, Role: "user", Kind: "user_message", Payload: json.RawMessage(`{}`), CreatedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.LoadConversation(convID)
	if err != nil {
		t.Fatal(err)
	}
	// 一致路径：条目保留、ContextVersion 与条目一致。
	if len(loaded.Entries) != 1 {
		t.Errorf("consistent load should preserve entries, got %d", len(loaded.Entries))
	}
	if loaded.ContextVersion != 1 {
		t.Errorf("consistent load ContextVersion=%d, want 1", loaded.ContextVersion)
	}
	// loop 状态应由条目派生为 idle（无活跃 request）。
	if loaded.CurrentLoopStatus != "idle" {
		t.Errorf("consistent load should derive idle loop status, got %q", loaded.CurrentLoopStatus)
	}
	// 关键：一致路径不应误清 NextEntrySeq。
	if loaded.NextEntrySeq != 2 {
		t.Errorf("consistent load NextEntrySeq=%d, want 2", loaded.NextEntrySeq)
	}
}

// TestF13ContextVersionMismatchUnit 单元测试 version mismatch 判定逻辑。
func TestF13ContextVersionMismatchUnit(t *testing.T) {
	cases := []struct {
		name           string
		stateVer       int64
		ctxVer         int64
		stateConvID    string
		ctxConvID      string
		wantMismatch   bool
	}{
		{"both zero empty", 0, 0, "x", "", false},
		{"equal versions", 5, 5, "x", "x", false},
		{"state older", 3, 5, "x", "x", true},
		{"state newer", 5, 3, "x", "x", true},
		{"conversation id mismatch", 5, 5, "x", "y", true},
		{"context conversation id empty tolerated", 5, 5, "x", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := contextVersionMismatch(c.stateVer, c.ctxVer, c.stateConvID, c.ctxConvID)
			if got != c.wantMismatch {
				t.Errorf("contextVersionMismatch(%d,%d,%q,%q) = %v, want %v",
					c.stateVer, c.ctxVer, c.stateConvID, c.ctxConvID, got, c.wantMismatch)
			}
		})
	}
}

// TestF13WriteJSONFileAtomicSyncsBeforeRename 间接验证 F-13 的 Sync 修复：
// 写入后文件内容立即可读且非空（Sync 不破坏正常路径）。完整崩溃耐久度需 OS 级
// 断电模拟，这里只覆盖功能正确性。
func TestF13WriteJSONFileAtomicSyncsBeforeRename(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "atomic.json")
	payload := map[string]any{"k": "v", "n": 42}
	if err := writeJSONFileAtomic(target, payload); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("written file not valid JSON after Sync+rename: %v", err)
	}
	if got["k"] != "v" || got["n"] != float64(42) {
		t.Errorf("written payload mismatch: %v", got)
	}
}
