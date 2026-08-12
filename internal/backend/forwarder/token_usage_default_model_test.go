package forwarder

import (
	"testing"
	"time"
)

// TestRecordTurnUsageDefaultModelUsesResolvedModel (P0-2 残项) 验证请求模型为元别名 "default" 时，
// provider_call 用量事件的 ModelID / by_model 桶键回退到实际服务模型（usage.Model = adapter
// TurnFinished 的 req.ProviderModelID），而非字面 "default"。
//
// 背景：客户端未指定具体模型时 intent.ModelID 被置为 "default"，stream.ModelID 即 "default"。
// 修复前事件落盘 ModelID="default"，by_model 聚合把所有走默认模型的调用混进同一桶——不同真实模型
// （gpt-5/claude…）token 加总后按单一价格计成本，成本失真。adapter 侧 currentModel 取的是
// req.ProviderModelID（经 applyChannelToRequest 填充的 channel.Model），所以 usage.Model 恒为真实模型。
func TestRecordTurnUsageDefaultModelUsesResolvedModel(t *testing.T) {
	store := NewUsageFileStore(t.TempDir())
	svc := &Service{usageStore: store}

	now := time.Now().UTC()
	stream := &ActiveStream{ModelID: "default", CreatedAt: now, UpdatedAt: now}
	usage := turnUsageSnapshot{
		Model:        "gpt-5",
		Provider:     "openai",
		InputTokens:  1000,
		OutputTokens: 200,
		UsagePresent: true,
	}
	if err := svc.recordTurnUsageSnapshot(stream, "", 1, "req-default", "call-1", "completed", usage, "", false); err != nil {
		t.Fatalf("recordTurnUsageSnapshot: %v", err)
	}

	doc, err := readUsageFileDocument(store.path)
	if err != nil {
		t.Fatalf("read usage doc: %v", err)
	}
	if len(doc.RecentEvents) != 1 {
		t.Fatalf("recent events = %d, want 1", len(doc.RecentEvents))
	}
	ev := doc.RecentEvents[0]
	if ev.ModelID != "gpt-5" {
		t.Errorf("provider_call ModelID = %q, want %q (resolved actual model, not the 'default' alias)", ev.ModelID, "gpt-5")
	}
	if ev.ModelName != "gpt-5" {
		t.Errorf("provider_call ModelName = %q, want %q", ev.ModelName, "gpt-5")
	}
	if _, mixed := doc.ByModel["default"]; mixed {
		t.Error("by_model must not contain a 'default' bucket — different models would be mixed")
	}
	agg, ok := doc.ByModel["gpt-5"]
	if !ok {
		t.Fatalf("by_model missing gpt-5 bucket: %+v", doc.ByModel)
	}
	if agg.ProviderCalls != 1 || agg.InputTokens != 1000 || agg.OutputTokens != 200 {
		t.Errorf("by_model[gpt-5] aggregate = %+v, want ProviderCalls=1 InputTokens=1000 OutputTokens=200", agg)
	}
}

// TestRecordTurnUsageConcreteModelStaysConcrete 回归保护：stream.ModelID 已是具体模型时
// （客户端显式选择，非 "default" 别名）ModelID 原样落盘，不被 usage.Model 覆盖。
func TestRecordTurnUsageConcreteModelStaysConcrete(t *testing.T) {
	store := NewUsageFileStore(t.TempDir())
	svc := &Service{usageStore: store}

	now := time.Now().UTC()
	stream := &ActiveStream{ModelID: "gpt-5", CreatedAt: now, UpdatedAt: now}
	usage := turnUsageSnapshot{
		Model:        "gpt-5",
		Provider:     "openai",
		InputTokens:  500,
		UsagePresent: true,
	}
	if err := svc.recordTurnUsageSnapshot(stream, "", 1, "req-concrete", "call-1", "completed", usage, "", false); err != nil {
		t.Fatalf("recordTurnUsageSnapshot: %v", err)
	}

	doc, err := readUsageFileDocument(store.path)
	if err != nil {
		t.Fatalf("read usage doc: %v", err)
	}
	if len(doc.RecentEvents) != 1 || doc.RecentEvents[0].ModelID != "gpt-5" {
		t.Errorf("concrete model event = %+v, want ModelID gpt-5", doc.RecentEvents)
	}
}
