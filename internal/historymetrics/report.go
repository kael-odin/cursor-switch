package historymetrics

import "time"

type Summary struct {
	ProviderCallsTotal int      `json:"providerCallsTotal"`
	TurnsTotal         int      `json:"turnsTotal"`
	ValidTurnsTotal    int      `json:"validTurnsTotal"`
	InvalidTurnsTotal  int      `json:"invalidTurnsTotal"`
	RequestTokensTotal int64    `json:"requestTokensTotal"`
	PromptTokensTotal  int64    `json:"promptTokensTotal"`
	CacheReadTokens    int64    `json:"cacheReadTokens"`
	CacheWriteTokens   int64    `json:"cacheWriteTokens"`
	CacheHitRate       *float64 `json:"cacheHitRate"`
	// ByModel 按模型聚合的 token 用量，用于按模型成本估算。
	ByModel []ModelUsage `json:"byModel"`
}

// ModelUsage 是单模型的 token 聚合。
type ModelUsage struct {
	ModelID         string `json:"modelId"`
	ModelName       string `json:"modelName,omitempty"`
	Provider        string `json:"provider,omitempty"`
	ProviderCalls   int64  `json:"providerCalls"`
	InputTokens     int64  `json:"inputTokens"`
	OutputTokens    int64  `json:"outputTokens"`
	CacheReadTokens int64  `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
	TotalTokens     int64  `json:"totalTokens"`
}

// UsageDashboardRaw 是 usage.json 的完整读取视图，供仪表盘 service 进一步加工。
type UsageDashboardRaw struct {
	UpdatedAt     time.Time                 `json:"updatedAt"`
	Totals        usageFileTotals           `json:"totals"`
	Daily         []usageFileDaily          `json:"daily"`
	RecentEvents  []usageFileEvent          `json:"recentEvents"`
	ByModel       []usageFileModelAggregate `json:"byModel"`
}

// 导出类型别名，供 bridge.dashboard 跨包引用（Go 不允许跨包用未导出类型）。
type UsageDashboardRawTotals = usageFileTotals
type UsageDashboardRawDaily = usageFileDaily
type UsageDashboardRawEvent = usageFileEvent
type UsageDashboardRawModelAggregate = usageFileModelAggregate

type Totals struct {
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	PromptTokensTotal  int64
	RequestTokensTotal int64
}

func cacheHitRateFromTotals(totals Totals) *float64 {
	inputCacheTokensTotal := totals.CacheReadTokens + totals.InputTokens
	if inputCacheTokensTotal <= 0 {
		return nil
	}
	value := float64(totals.CacheReadTokens) / float64(inputCacheTokensTotal)
	return &value
}
