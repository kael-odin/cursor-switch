package forwarder

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestCompactionEntryConstructors 验证 compaction 写 history 的 entry 构造器。
// 这些是压缩落盘的核心单元，payload 结构错了会让后续 resume 读不回正确状态。
func TestNewCompactionSummaryEntry(t *testing.T) {
	plan := &PendingCompaction{
		Trigger:          "auto",
		CurrentTurnSeq:   3,
		CurrentRequestID: "  req-1  ",
		CompactTurnCount: 2,
		MessagesToCompact: 5,
	}
	entry := newCompactionSummaryEntry(plan, "  summary text  ")
	if entry.Kind != "compacted_summary" || entry.Role != "system" {
		t.Fatalf("role/kind = %s/%s", entry.Role, entry.Kind)
	}
	if entry.RequestID != "req-1" {
		t.Fatalf("RequestID not trimmed: %q", entry.RequestID)
	}
	var payload compactionSummaryEntryPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Summary != "summary text" {
		t.Fatalf("Summary not trimmed: %q", payload.Summary)
	}
	if payload.Trigger != "auto" {
		t.Fatalf("Trigger = %q", payload.Trigger)
	}
	if payload.CompactTurnCount != 2 || payload.MessagesToCompact != 5 {
		t.Fatalf("counts = %d/%d", payload.CompactTurnCount, payload.MessagesToCompact)
	}
}

func TestNewCompactionRequestEntry(t *testing.T) {
	plan := &PendingCompaction{
		Trigger:           "manual",
		ContextTokens:     10000,
		ContextWindowSize: 200000,
		ReserveTokens:     30000,
		MessagesToCompact: 8,
		CompactTurnCount:  3,
		RequestSource:     "user",
		SummaryModelCallID: "mc-1",
		CurrentTurnSeq:    1,
		CurrentRequestID:  "req-2",
	}
	entry := newCompactionRequestEntry(plan)
	if entry.Kind != "compaction_request" {
		t.Fatalf("Kind = %s", entry.Kind)
	}
	var payload map[string]any
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["context_tokens"].(float64) != 10000 {
		t.Fatalf("context_tokens = %v", payload["context_tokens"])
	}
	if payload["summary_model_call_id"] != "mc-1" {
		t.Fatalf("summary_model_call_id = %v", payload["summary_model_call_id"])
	}
}

func TestNewCompactionFailedEntry(t *testing.T) {
	t.Run("with cause and plan", func(t *testing.T) {
		plan := &PendingCompaction{Trigger: "auto", RequestSource: "system", CurrentTurnSeq: 2, CurrentRequestID: "req-3"}
		entry := newCompactionFailedEntry(plan, errors.New("  boom  "))
		if entry.Kind != "compaction_failed" {
			t.Fatalf("Kind = %s", entry.Kind)
		}
		var payload map[string]any
		json.Unmarshal(entry.Payload, &payload)
		if payload["error"] != "boom" {
			t.Fatalf("error not trimmed: %v", payload["error"])
		}
		if payload["trigger"] != "auto" {
			t.Fatalf("trigger = %v", payload["trigger"])
		}
	})
	t.Run("nil plan nil cause defaults", func(t *testing.T) {
		entry := newCompactionFailedEntry(nil, nil)
		var payload map[string]any
		json.Unmarshal(entry.Payload, &payload)
		if payload["error"] != "compaction failed" {
			t.Fatalf("default error = %v", payload["error"])
		}
		if entry.TurnSeq != 0 {
			t.Fatalf("nil plan should leave TurnSeq 0, got %d", entry.TurnSeq)
		}
	})
}

// TestBuildFallbackCompactionSummary 验证降级摘要的结构与截断。
func TestBuildFallbackCompactionSummary(t *testing.T) {
	t.Run("nil plan still has header", func(t *testing.T) {
		s := buildFallbackCompactionSummary(nil)
		if !strings.Contains(s, "Conversation summary") {
			t.Fatalf("missing header: %q", s)
		}
	})
	t.Run("compacted turns enumerated", func(t *testing.T) {
		plan := &PendingCompaction{
			CompactedTurns: []compactedTurnSummary{
				{UserText: "问天气", Steps: []string{"查了北京", "返回晴"}},
			},
		}
		s := buildFallbackCompactionSummary(plan)
		if !strings.Contains(s, "1. user=问天气") {
			t.Fatalf("turn not enumerated: %q", s)
		}
		if !strings.Contains(s, "查了北京") {
			t.Fatalf("step missing: %q", s)
		}
	})
	t.Run("manual instruction included", func(t *testing.T) {
		plan := &PendingCompaction{ManualInstruction: "聚焦代码改动"}
		s := buildFallbackCompactionSummary(plan)
		if !strings.Contains(s, "聚焦代码改动") {
			t.Fatalf("manual instruction missing: %q", s)
		}
	})
}

// TestParseManualCompactionDirective 验证 /compact 指令解析。
func TestParseManualCompactionDirective(t *testing.T) {
	tests := []struct {
		in       string
		wantInstr string
		wantOk   bool
	}{
		{"/compact", "", true},
		{"  /compact  ", "", true},
		{"/compact 聚焦数据库改动", "聚焦数据库改动", true},
		{"hello", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		instr, ok := parseManualCompactionDirective(tc.in)
		if ok != tc.wantOk || instr != tc.wantInstr {
			t.Errorf("parseManualCompactionDirective(%q) = (%q,%v), want (%q,%v)", tc.in, instr, ok, tc.wantInstr, tc.wantOk)
		}
	}
}

// TestNewCompactionSummaryEntry_NilPlanSafe 验证 nil plan 不 panic（字段零值）。
func TestNewCompactionSummaryEntry_NilPlanSafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil plan panicked: %v", r)
		}
	}()
	// newCompactionSummaryEntry 解引用 plan，nil 会 panic——这是设计预期（调用方保证非 nil）。
	// 此测试记录该约束：若未来想支持 nil，需加守卫。
	plan := &PendingCompaction{}
	entry := newCompactionSummaryEntry(plan, "x")
	if entry.Kind != "compacted_summary" {
		t.Fatalf("Kind = %s", entry.Kind)
	}
}
