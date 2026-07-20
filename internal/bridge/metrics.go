package bridge

import (
	"cursor/internal/appdata"
	"cursor/internal/historymetrics"
)

// HomeMetricsSummary 定义首页展示的历史统计摘要。
type HomeMetricsSummary struct {
	ProviderCallsTotal int      `json:"providerCallsTotal"`
	TurnsTotal         int      `json:"turnsTotal"`
	ValidTurnsTotal    int      `json:"validTurnsTotal"`
	InvalidTurnsTotal  int      `json:"invalidTurnsTotal"`
	RequestTokensTotal int64    `json:"requestTokensTotal"`
	PromptTokensTotal  int64    `json:"promptTokensTotal"`
	CacheReadTokens    int64    `json:"cacheReadTokens"`
	CacheWriteTokens   int64    `json:"cacheWriteTokens"`
	CacheHitRate       *float64 `json:"cacheHitRate"`
}

// TokenPricing 定义首页成本估算所用的 token 单价（每百万 token，美元）。
// ponytail: 定价写在 getter 里；定价变动频率低，无需配置文件。若未来按 adapter 差异化定价，挪到 server/config。
type TokenPricing struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	ModelLabel string  `json:"modelLabel"`
}

// MetricsService 定义首页统计相关的 Wails service。
type MetricsService struct{}

// NewMetricsService 创建首页统计 service。
func NewMetricsService() *MetricsService {
	return &MetricsService{}
}

// GetHomeMetricsSummary 返回首页展示的全量历史统计摘要。
func (service *MetricsService) GetHomeMetricsSummary() (HomeMetricsSummary, error) {
	if err := appdata.EnsureAssistantHome(); err != nil {
		return HomeMetricsSummary{}, err
	}

	summary, err := historymetrics.LoadUsageSummary(appdata.UsageFilePath())
	if err != nil {
		return HomeMetricsSummary{}, err
	}
	return HomeMetricsSummary{
		ProviderCallsTotal: summary.ProviderCallsTotal,
		TurnsTotal:         summary.TurnsTotal,
		ValidTurnsTotal:    summary.ValidTurnsTotal,
		InvalidTurnsTotal:  summary.InvalidTurnsTotal,
		RequestTokensTotal: summary.RequestTokensTotal,
		PromptTokensTotal:  summary.PromptTokensTotal,
		CacheReadTokens:    summary.CacheReadTokens,
		CacheWriteTokens:   summary.CacheWriteTokens,
		CacheHitRate:       summary.CacheHitRate,
	}, nil
}

// GetTokenPricing 返回首页成本估算所用的 token 单价。
func (service *MetricsService) GetTokenPricing() TokenPricing {
	return TokenPricing{
		Input:      5,
		Output:     25,
		CacheRead:  0.5,
		CacheWrite: 6.25,
		ModelLabel: "Claude Opus 4.7",
	}
}
