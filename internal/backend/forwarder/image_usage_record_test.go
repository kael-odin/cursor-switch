package forwarder

import (
	"testing"
	"time"
)

// TestImageCallRecordedAsZeroTokenProviderCall 验证 F-3「仅记调用（成本仍 $0）」的落盘契约：
// handleImageResult 写出的 image provider_call 事件（token 全 0、UsagePresent=false）经
// UpsertEvent 持久化后，必须计入 ProviderCalls（Totals/Daily/ByModel）而四类 token 归零——
// 生图调用在使用统计里可见（调用数/时间线/按模型与 provider 归属），但不产生任何成本。
// 事件构造与 handleImageResult 完全同形：EventID 用 usageEventID(requestID, "img:"+ImageID) 派生。
func TestImageCallRecordedAsZeroTokenProviderCall(t *testing.T) {
	store := NewUsageFileStore(t.TempDir())
	at := time.Now().UTC()

	event := usageFileEvent{
		EventID:      usageEventID("req-img", "img:img-1"),
		Kind:         usageEventKindProvider,
		Status:       "completed",
		At:           at,
		UsagePresent: false,
		ModelID:      "gpt-image-2",
		ModelName:    "gpt-image-2",
		Provider:     "openai",
	}
	if err := store.UpsertEvent(event); err != nil {
		t.Fatalf("UpsertEvent image call: %v", err)
	}

	doc, err := readUsageFileDocument(store.path)
	if err != nil {
		t.Fatalf("read usage doc: %v", err)
	}
	if doc.Totals.ProviderCalls != 1 {
		t.Errorf("Totals.ProviderCalls = %d, want 1 (call must be counted)", doc.Totals.ProviderCalls)
	}
	if doc.Totals.InputTokens != 0 || doc.Totals.OutputTokens != 0 ||
		doc.Totals.CacheReadTokens != 0 || doc.Totals.CacheWriteTokens != 0 || doc.Totals.TotalTokens != 0 {
		t.Errorf("Totals tokens must stay 0, got in/out/cr/cw/total = %d/%d/%d/%d/%d",
			doc.Totals.InputTokens, doc.Totals.OutputTokens, doc.Totals.CacheReadTokens,
			doc.Totals.CacheWriteTokens, doc.Totals.TotalTokens)
	}

	agg, ok := doc.ByModel["gpt-image-2"]
	if !ok {
		t.Fatalf("ByModel missing gpt-image-2: %+v", doc.ByModel)
	}
	if agg.ProviderCalls != 1 {
		t.Errorf("ByModel.ProviderCalls = %d, want 1 (model attribution)", agg.ProviderCalls)
	}
	if agg.Provider != "openai" {
		t.Errorf("ByModel.Provider = %q, want openai", agg.Provider)
	}
	if agg.InputTokens != 0 || agg.OutputTokens != 0 || agg.TotalTokens != 0 {
		t.Errorf("ByModel tokens must stay 0, got in/out/total = %d/%d/%d", agg.InputTokens, agg.OutputTokens, agg.TotalTokens)
	}

	if len(doc.Daily) != 1 {
		t.Fatalf("Daily length = %d, want 1", len(doc.Daily))
	}
	if doc.Daily[0].ProviderCalls != 1 {
		t.Errorf("Daily.ProviderCalls = %d, want 1", doc.Daily[0].ProviderCalls)
	}
	if dm, ok := doc.Daily[0].ByModel["gpt-image-2"]; !ok || dm.ProviderCalls != 1 {
		t.Errorf("Daily.ByModel[gpt-image-2].ProviderCalls = %+v, want 1", doc.Daily[0].ByModel["gpt-image-2"])
	}

	if len(doc.RecentEvents) != 1 || doc.RecentEvents[0].UsagePresent || doc.RecentEvents[0].ModelID != "gpt-image-2" {
		t.Errorf("RecentEvents entry wrong: %+v", doc.RecentEvents)
	}
}

// TestImageEventIDNeverCollidesWithChatEventID 验证 F-3 的关键不变式：
// 生图事件 ID（requestID + "img:" + ImageID）与同 request 的 chat provider_call 事件 ID
// （usageEventID(requestID, modelCallID)）必须互异，且 "img:" 前缀使 usageEventID 永不回退成
// 裸 requestID——否则 image 事件会覆盖同 request 的 chat 计费事件（EventIndex 同 key upsert
// 先 negate 旧值再 apply 新值，chat 的 token 与调用计数被整体抹掉，成本漏算）。
func TestImageEventIDNeverCollidesWithChatEventID(t *testing.T) {
	requestID := "req-1"
	chatEventID := usageEventID(requestID, "call_chat_001")
	imgEventID := usageEventID(requestID, "img:img-1")
	if chatEventID == imgEventID {
		t.Fatalf("image event ID %q collides with chat event ID %q", imgEventID, chatEventID)
	}
	if imgEventID == requestID {
		t.Fatal("image event ID collapsed to bare requestID — would overwrite chat accounting")
	}

	// ImageID 取极端值（=requestID、空、常规）也不允许回退成裸 requestID。
	for _, imageID := range []string{requestID, "", "img-1"} {
		if eid := usageEventID(requestID, "img:"+imageID); eid == requestID {
			t.Fatalf("image event ID for imageID=%q collapsed to bare requestID", imageID)
		}
	}
	// 同一 request 内两张图事件 ID 也必须互异（否则后一张覆盖前一张）。
	if a, b := usageEventID(requestID, "img:img-1"), usageEventID(requestID, "img:img-2"); a == b {
		t.Fatal("two distinct image calls in one request produced the same event ID")
	}
}
