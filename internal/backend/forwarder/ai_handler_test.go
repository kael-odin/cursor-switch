package forwarder

import (
	"testing"
)

// TestConversationIDForRequestIndex 是 H4 的回归测试：写入带 RequestID 的 entry 后，
// 反向索引应能 O(1) 定位会话，lookupThoughtAnnotation 不必全量扫描所有会话。
func TestConversationIDForRequestIndex(t *testing.T) {
	dir := t.TempDir()
	store := NewConversationFileStore(dir)

	// 两个会话，各带一个 requestID。
	convA := "conv-a"
	convB := "conv-b"
	entriesA := []HistoryEntry{
		newMetadataEntry(1, "req-a-1", "thought_annotation", map[string]any{
			"kind":    "summary_completed",
			"thought": "thinking about A",
		}),
	}
	entriesB := []HistoryEntry{
		newMetadataEntry(1, "req-b-1", "thought_annotation", map[string]any{
			"kind":    "summary_completed",
			"thought": "thinking about B",
		}),
	}
	if _, _, err := store.AppendEntries(convA, entriesA); err != nil {
		t.Fatalf("AppendEntries A: %v", err)
	}
	if _, _, err := store.AppendEntries(convB, entriesB); err != nil {
		t.Fatalf("AppendEntries B: %v", err)
	}

	// 反向索引应把两个 requestID 分别映射到正确的会话。
	if cid, ok := store.ConversationIDForRequest("req-a-1"); !ok || cid != convA {
		t.Errorf("ConversationIDForRequest(req-a-1) = (%q,%v), want (%q,true)", cid, ok, convA)
	}
	if cid, ok := store.ConversationIDForRequest("req-b-1"); !ok || cid != convB {
		t.Errorf("ConversationIDForRequest(req-b-1) = (%q,%v), want (%q,true)", cid, ok, convB)
	}
	if _, ok := store.ConversationIDForRequest("req-missing"); ok {
		t.Errorf("ConversationIDForRequest(req-missing) should miss")
	}
}

// TestLookupThoughtAnnotationUsesIndex 验证 lookupThoughtAnnotation 通过反向索引命中正确会话，
// 返回该会话的 thought，且不串到其它会话。
func TestLookupThoughtAnnotationUsesIndex(t *testing.T) {
	dir := t.TempDir()
	store := NewConversationFileStore(dir)
	service := newServiceWithDependencies(store, nil, nil, nil, nil)

	// 会话 A 带 thought；会话 B 带 thought；会话 C 只有普通 entry（命中 request 但无 thought）。
	if _, _, err := store.AppendEntries("conv-a", []HistoryEntry{
		newMetadataEntry(1, "req-a", "thought_annotation", map[string]any{
			"kind":    "summary_completed",
			"thought": "alpha thought",
		}),
	}); err != nil {
		t.Fatalf("AppendEntries A: %v", err)
	}
	if _, _, err := store.AppendEntries("conv-b", []HistoryEntry{
		newMetadataEntry(1, "req-b", "thought_annotation", map[string]any{
			"kind":    "summary_completed",
			"thought": "beta thought",
		}),
	}); err != nil {
		t.Fatalf("AppendEntries B: %v", err)
	}
	if _, _, err := store.AppendEntries("conv-c", []HistoryEntry{
		{TurnSeq: 1, RequestID: "req-c", Role: "user", Kind: "message", Payload: []byte("{}")},
	}); err != nil {
		t.Fatalf("AppendEntries C: %v", err)
	}

	// 命中带 thought 的会话：返回该 thought（不是 defaultSummaryCompletedThought）。
	got, ok, err := service.lookupThoughtAnnotation("req-b")
	if err != nil || !ok {
		t.Fatalf("lookupThoughtAnnotation(req-b) = (%q,%v,%v), want (thought,true,nil)", got, ok, err)
	}
	if got != "beta thought" {
		t.Errorf("lookupThoughtAnnotation(req-b) = %q, want %q", got, "beta thought")
	}

	// 命中无 thought 的会话：回退 defaultSummaryCompletedThought。
	got, ok, err = service.lookupThoughtAnnotation("req-c")
	if err != nil || !ok {
		t.Fatalf("lookupThoughtAnnotation(req-c) = (%q,%v,%v), want (default,true,nil)", got, ok, err)
	}
	if got != defaultSummaryCompletedThought {
		t.Errorf("lookupThoughtAnnotation(req-c) = %q, want defaultSummaryCompletedThought", got)
	}

	// 完全未命中的 requestID：返回 not found。
	_, ok, err = service.lookupThoughtAnnotation("req-missing")
	if err != nil || ok {
		t.Errorf("lookupThoughtAnnotation(req-missing) = (_, %v, %v), want (_, false, nil)", ok, err)
	}
}

// TestLookupThoughtAnnotationFallbackFullScan 验证索引未命中时（冷启动 / 跨进程），
// 全量扫描兜底仍能找到 thought——正确性不依赖索引。
func TestLookupThoughtAnnotationFallbackFullScan(t *testing.T) {
	dir := t.TempDir()
	store := NewConversationFileStore(dir)
	service := newServiceWithDependencies(store, nil, nil, nil, nil)

	if _, _, err := store.AppendEntries("conv-x", []HistoryEntry{
		newMetadataEntry(1, "req-x", "thought_annotation", map[string]any{
			"kind":    "summary_completed",
			"thought": "scanned thought",
		}),
	}); err != nil {
		t.Fatalf("AppendEntries: %v", err)
	}

	// 手动清空反向索引，模拟冷启动 / 索引丢失。
	store.requestIndex.Range(func(k, v any) bool {
		store.requestIndex.Delete(k)
		return true
	})

	// 索引未命中 -> 全量扫描兜底仍能找到。
	got, ok, err := service.lookupThoughtAnnotation("req-x")
	if err != nil || !ok {
		t.Fatalf("lookupThoughtAnnotation(req-x) fallback = (%q,%v,%v), want (thought,true,nil)", got, ok, err)
	}
	if got != "scanned thought" {
		t.Errorf("lookupThoughtAnnotation(req-x) fallback = %q, want %q", got, "scanned thought")
	}
}

// TestLookupThoughtAnnotationFallbackRecencyShortCircuit 覆盖 N-29：
// 索引 miss 兜底按 recency 扫描，且某会话 hitReq 后短路停扫。
// 两个会话各持有一个 requestID；清空反向索引后查 req-in-conv-y，
// 应通过兜底找到（即便 conv-y 字母序在 conv-z 之后）。再查一个不存在的
// requestID，应扫完全部会话后正确返回 not-found（不误命中）。
func TestLookupThoughtAnnotationFallbackRecencyShortCircuit(t *testing.T) {
	dir := t.TempDir()
	store := NewConversationFileStore(dir)
	service := newServiceWithDependencies(store, nil, nil, nil, nil)

	// conv-z 写在前（字母序靠前），conv-y 写在后（字母序靠后但 recency 更新）。
	// 两者各带一个 thought_annotation。
	if _, _, err := store.AppendEntries("conv-z", []HistoryEntry{
		newMetadataEntry(1, "req-in-conv-z", "thought_annotation", map[string]any{
			"kind": "summary_completed", "thought": "thought-z",
		}),
	}); err != nil {
		t.Fatalf("AppendEntries conv-z: %v", err)
	}
	if _, _, err := store.AppendEntries("conv-y", []HistoryEntry{
		newMetadataEntry(1, "req-in-conv-y", "thought_annotation", map[string]any{
			"kind": "summary_completed", "thought": "thought-y",
		}),
	}); err != nil {
		t.Fatalf("AppendEntries conv-y: %v", err)
	}

	// 清空反向索引模拟冷启动。
	store.requestIndex.Range(func(k, v any) bool {
		store.requestIndex.Delete(k)
		return true
	})

	// 查 req-in-conv-y：兜底应找到 thought-y（recency 排序不影响正确性，目标会话终被扫到）。
	got, ok, err := service.lookupThoughtAnnotation("req-in-conv-y")
	if err != nil || !ok {
		t.Fatalf("lookupThoughtAnnotation(req-in-conv-y) = (%q,%v,%v), want (thought-y,true,nil)", got, ok, err)
	}
	if got != "thought-y" {
		t.Errorf("lookupThoughtAnnotation(req-in-conv-y) = %q, want thought-y", got)
	}

	// 查不存在的 requestID：扫完全部会话后正确返回 not-found。
	got, ok, err = service.lookupThoughtAnnotation("req-does-not-exist")
	if err != nil {
		t.Fatalf("lookupThoughtAnnotation(missing) err = %v", err)
	}
	if ok {
		t.Errorf("lookupThoughtAnnotation(missing) = (%q,true), want not-found", got)
	}
}
