package historymetrics

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
	ProviderCalls   int64  `json:"providerCalls"`
	InputTokens     int64  `json:"inputTokens"`
	OutputTokens    int64  `json:"outputTokens"`
	CacheReadTokens int64  `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
	TotalTokens     int64  `json:"totalTokens"`
}

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
