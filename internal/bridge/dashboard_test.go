package bridge

import (
	"encoding/json"
	"testing"

	"cursor/internal/backend/server/config"
	"cursor/internal/historymetrics"
)

func TestBillableInputTokens(t *testing.T) {
	cases := []struct {
		name      string
		input     int64
		cacheRead int64
		cacheW    int64
		sem       string
		want      int64
	}{
		{"fresh unchanged", 1000, 200, 100, string(config.InputSemanticsFresh), 1000},
		{"total subtracts both", 1000, 200, 100, string(config.InputSemanticsTotal), 700},
		{"legacy subtracts read only", 1000, 200, 100, "", 800},
		{"total clamps negative", 100, 200, 100, string(config.InputSemanticsTotal), 0},
		{"legacy clamps negative", 50, 200, 0, "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := billableInputTokens(c.input, c.cacheRead, c.cacheW, c.sem)
			if got != c.want {
				t.Errorf("billableInputTokens(%d,%d,%d,%q)=%d want %d", c.input, c.cacheRead, c.cacheW, c.sem, got, c.want)
			}
		})
	}
}

func TestRealTotalTokensForModel(t *testing.T) {
	// real_total = fresh_input + output + cacheWrite + cacheRead
	cases := []struct {
		name      string
		input     int64
		output    int64
		cacheRead int64
		cacheW    int64
		sem       string
		want      int64
	}{
		// FRESH: fresh_input=input -> 1000+50+100+200 = 1350
		{"fresh", 1000, 50, 200, 100, string(config.InputSemanticsFresh), 1350},
		// TOTAL: fresh_input=1000-200-100=700 -> 700+50+100+200 = 1050
		// 旧版错误算法 input+output+cacheW+cacheRead = 1350 会重复计 cache 3 次。
		{"total no double count", 1000, 50, 200, 100, string(config.InputSemanticsTotal), 1050},
		// legacy: fresh_input=1000-200=800 -> 800+50+100+200 = 1150
		{"legacy no double count read", 1000, 50, 200, 100, "", 1150},
		// TOTAL clamp: fresh_input=0 -> 0+50+100+200 = 350
		{"total clamp", 100, 50, 200, 100, string(config.InputSemanticsTotal), 350},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := realTotalTokensForModel(c.input, c.output, c.cacheRead, c.cacheW, c.sem)
			if got != c.want {
				t.Errorf("realTotalTokensForModel(...,%q)=%d want %d", c.sem, got, c.want)
			}
		})
	}
}

func TestRealTotalTokensForModelDoesNotDoubleCountCache(t *testing.T) {
	// 关键回归：TOTAL 语义下旧 realTotalTokens 会把 cache 计 3 次。
	// input=1000 内含 cacheRead=200 + cacheW=100（即 fresh=700）。
	// 正确 real_total = 700 + 0 + 100 + 200 = 1000。
	// 旧错误 = 1000 + 0 + 100 + 200 = 1300（虚高 300）。
	got := realTotalTokensForModel(1000, 0, 200, 100, string(config.InputSemanticsTotal))
	if got != 1000 {
		t.Errorf("TOTAL real_total = %d, want 1000 (cache must not be triple-counted)", got)
	}
}

func TestSumModelRealTokens(t *testing.T) {
	stats := []UsageDashboardModelStat{
		{RealTotalTokens: 1000},
		{RealTotalTokens: 2000},
		{RealTotalTokens: 0},
	}
	if got := sumModelRealTokens(stats); got != 3000 {
		t.Errorf("sumModelRealTokens=%d want 3000", got)
	}
}

// realTotalTokensFromDailyByModel 的 ByModel 分支需要构造 historymetrics 未导出类型
// 的 map 值，无法在包外用字面量命名。改用 JSON 反序列化构造 daily 实例。
func TestRealTotalTokensFromDailyByModel(t *testing.T) {
	pricing := PricingSnapshot{
		Models: []TokenPricing{
			{ModelID: "gpt-5", InputTokenSemantics: string(config.InputSemanticsTotal)},
			{ModelID: "claude", InputTokenSemantics: string(config.InputSemanticsFresh)},
		},
	}

	// 旧版 usage.json：无 ByModel，回退裸 realTotalTokens = 100+50+30+20 = 200
	rawFallback := `{"date":"2026-07-26","input_tokens":100,"output_tokens":50,"cache_read_tokens":30,"cache_write_tokens":20}`
	var dFallback historymetrics.UsageDashboardRawDaily
	if err := json.Unmarshal([]byte(rawFallback), &dFallback); err != nil {
		t.Fatalf("unmarshal fallback: %v", err)
	}
	if got := realTotalTokensFromDailyByModel(dFallback, pricing); got != 200 {
		t.Errorf("fallback realTotalTokens=%d want 200", got)
	}

	// 有 ByModel：gpt-5 TOTAL fresh=1000-200-100=700 -> 700+50+100+200=1050
	//            claude FRESH fresh=500 -> 500+30+50+10=590
	// 合计 1640。若用裸算法会重复计 cache。
	rawByModel := `{
		"date":"2026-07-26",
		"by_model": {
			"gpt-5":   {"model_id":"gpt-5","input_tokens":1000,"output_tokens":50,"cache_read_tokens":200,"cache_write_tokens":100},
			"claude":  {"model_id":"claude","input_tokens":500,"output_tokens":30,"cache_read_tokens":50,"cache_write_tokens":10}
		}
	}`
	var dByModel historymetrics.UsageDashboardRawDaily
	if err := json.Unmarshal([]byte(rawByModel), &dByModel); err != nil {
		t.Fatalf("unmarshal by_model: %v", err)
	}
	if got := realTotalTokensFromDailyByModel(dByModel, pricing); got != 1640 {
		t.Errorf("by_model aggregate realTotalTokens=%d want 1640", got)
	}
}
