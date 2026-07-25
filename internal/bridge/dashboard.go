// dashboard.go 实现使用统计仪表盘后端：从 usage.json 读取全量数据，
// 按日趋势/模型/Provider 聚合，并套用定价×倍率计算成本，返回给前端 ECharts 仪表盘。
//
// 设计：不引入 SQLite。byok 已有 usage.json 全量持久化（totals/daily/by_model 永久累加，
// recent_events 限 500 条），cost 引擎 computeCostByModel 已完整。本文件只补三件事：
// 1) daily 按日成本计算（原 usage.json daily 只有 token，没有 cost）
// 2) provider 维度聚合（从 by_model.Provider 字段派生）
// 3) recent_events 转成前端可渲染的日志条目（含单次成本估算）
package bridge

import (
	"sort"
	"strings"
	"time"

	"cursor/internal/appdata"
	"cursor/internal/historymetrics"
)

// UsageDashboard 是使用统计仪表盘的完整载荷。
type UsageDashboard struct {
	UpdatedAt time.Time          `json:"updatedAt"`
	Totals    UsageDashboardTotals `json:"totals"`
	Daily     []UsageDashboardDaily `json:"daily"`
	ByModel   []UsageDashboardModelStat `json:"byModel"`
	ByProvider []UsageDashboardProviderStat `json:"byProvider"`
	RecentEvents []UsageDashboardEvent `json:"recentEvents"`
	// DefaultCostMultiplier 是当前全局倍率，前端用于显示/调整。
	DefaultCostMultiplier float64 `json:"defaultCostMultiplier"`
}

// UsageDashboardTotals 是全量累计。
type UsageDashboardTotals struct {
	ProviderCalls     int64   `json:"providerCalls"`
	TurnsTotal        int64   `json:"turnsTotal"`
	ValidTurnsTotal   int64   `json:"validTurnsTotal"`
	InvalidTurnsTotal int64   `json:"invalidTurnsTotal"`
	InputTokens       int64   `json:"inputTokens"`
	OutputTokens      int64   `json:"outputTokens"`
	CacheReadTokens   int64   `json:"cacheReadTokens"`
	CacheWriteTokens  int64   `json:"cacheWriteTokens"`
	TotalTokens       int64   `json:"totalTokens"`
	// RealTotalTokens = fresh_input + output + cache_creation + cache_read。
	// 参考 cc-switch derive_real_total_and_hit_rate 的「真实消耗 token」口径。
	RealTotalTokens int64 `json:"realTotalTokens"`
	CacheHitRate    *float64 `json:"cacheHitRate"`
	TotalCostUSD    float64 `json:"totalCostUSD"`
}

// UsageDashboardDaily 是单日聚合（含成本）。
type UsageDashboardDaily struct {
	Date             string  `json:"date"`
	ProviderCalls    int64   `json:"providerCalls"`
	InputTokens     int64   `json:"inputTokens"`
	OutputTokens    int64   `json:"outputTokens"`
	CacheReadTokens  int64   `json:"cacheReadTokens"`
	CacheWriteTokens int64   `json:"cacheWriteTokens"`
	TotalTokens     int64   `json:"totalTokens"`
	RealTotalTokens int64   `json:"realTotalTokens"`
	CostUSD         float64 `json:"costUSD"`
}

// UsageDashboardModelStat 是单模型统计（含成本 + 平均成本/请求）。
type UsageDashboardModelStat struct {
	ModelID          string  `json:"modelId"`
	ModelName        string  `json:"modelName,omitempty"`
	Provider         string  `json:"provider,omitempty"`
	ProviderCalls    int64   `json:"providerCalls"`
	InputTokens      int64   `json:"inputTokens"`
	OutputTokens     int64   `json:"outputTokens"`
	CacheReadTokens  int64   `json:"cacheReadTokens"`
	CacheWriteTokens int64   `json:"cacheWriteTokens"`
	TotalTokens      int64   `json:"totalTokens"`
	RealTotalTokens  int64   `json:"realTotalTokens"`
	InputCost        float64 `json:"inputCost"`
	OutputCost       float64 `json:"outputCost"`
	CacheReadCost    float64 `json:"cacheReadCost"`
	CacheWriteCost   float64 `json:"cacheWriteCost"`
	TotalCost        float64 `json:"totalCost"`
	CostMultiplier   float64 `json:"costMultiplier"`
	AvgCostPerRequest float64 `json:"avgCostPerRequest"`
}

// UsageDashboardProviderStat 是单 provider 统计（聚合其所有模型）。
type UsageDashboardProviderStat struct {
	Provider        string  `json:"provider"`
	ProviderCalls   int64   `json:"providerCalls"`
	InputTokens     int64   `json:"inputTokens"`
	OutputTokens    int64   `json:"outputTokens"`
	CacheReadTokens int64   `json:"cacheReadTokens"`
	CacheWriteTokens int64  `json:"cacheWriteTokens"`
	TotalTokens     int64   `json:"totalTokens"`
	RealTotalTokens int64   `json:"realTotalTokens"`
	TotalCost       float64 `json:"totalCost"`
}

// UsageDashboardEvent 是单条请求日志（含成本估算）。
type UsageDashboardEvent struct {
	EventID          string    `json:"eventId"`
	Kind             string    `json:"kind"`
	Status           string    `json:"status"`
	At               time.Time `json:"at"`
	ModelID          string    `json:"modelId,omitempty"`
	ModelName        string    `json:"modelName,omitempty"`
	Provider         string    `json:"provider,omitempty"`
	InputTokens      int64     `json:"inputTokens"`
	OutputTokens     int64     `json:"outputTokens"`
	CacheReadTokens  int64     `json:"cacheReadTokens"`
	CacheWriteTokens int64     `json:"cacheWriteTokens"`
	TotalTokens      int64     `json:"totalTokens"`
	RealTotalTokens  int64     `json:"realTotalTokens"`
	UsagePresent     bool      `json:"usagePresent"`
	CostUSD          float64   `json:"costUSD"`
}

// GetUsageDashboard 返回使用统计仪表盘的完整数据。
func (service *MetricsService) GetUsageDashboard() (UsageDashboard, error) {
	if err := appdata.EnsureAssistantHome(); err != nil {
		return UsageDashboard{}, err
	}
	raw, err := historymetrics.LoadUsageDocument(appdata.UsageFilePath())
	if err != nil {
		return UsageDashboard{}, err
	}
	pricing := service.loadPricing()
	adapterMultipliers := service.loadAdapterMultipliers()
	defaultMultiplier := pricing.DefaultCostMultiplier
	if defaultMultiplier <= 0 {
		defaultMultiplier = 1
	}

	byModel := buildDashboardModelStats(raw.ByModel, pricing, adapterMultipliers, defaultMultiplier)
	byProvider := aggregateDashboardByProvider(byModel)
	daily := buildDashboardDaily(raw.Daily, pricing, adapterMultipliers, defaultMultiplier)
	events := buildDashboardEvents(raw.RecentEvents, pricing, adapterMultipliers, defaultMultiplier)

	totals := UsageDashboardTotals{
		ProviderCalls:     raw.Totals.ProviderCalls,
		TurnsTotal:        raw.Totals.TurnsTotal,
		ValidTurnsTotal:   raw.Totals.ValidTurnsTotal,
		InvalidTurnsTotal: raw.Totals.InvalidTurnsTotal,
		InputTokens:       raw.Totals.InputTokens,
		OutputTokens:      raw.Totals.OutputTokens,
		CacheReadTokens:   raw.Totals.CacheReadTokens,
		CacheWriteTokens:  raw.Totals.CacheWriteTokens,
		TotalTokens:       raw.Totals.TotalTokens,
		RealTotalTokens:   realTotalTokens(raw.Totals.InputTokens, raw.Totals.OutputTokens, raw.Totals.CacheReadTokens, raw.Totals.CacheWriteTokens),
		CacheHitRate:      cacheHitRate(raw.Totals.InputTokens, raw.Totals.CacheReadTokens),
		TotalCostUSD:      sumModelCosts(byModel),
	}

	return UsageDashboard{
		UpdatedAt:            raw.UpdatedAt,
		Totals:               totals,
		Daily:                daily,
		ByModel:             byModel,
		ByProvider:          byProvider,
		RecentEvents:        events,
		DefaultCostMultiplier: defaultMultiplier,
	}, nil
}

// realTotalTokens 计算「真实消耗 token」= fresh_input + output + cache_creation + cache_read。
// 参考 cc-switch derive_real_total_and_hit_rate。
func realTotalTokens(input, output, cacheRead, cacheWrite int64) int64 {
	return input + output + cacheWrite + cacheRead
}

// cacheHitRate = cacheRead / (fresh_input + cache_creation + cache_read)。
// 与 historymetrics.cacheHitRateFromTotals 口径略不同（分母不含 output），这里用 cc-switch 口径。
func cacheHitRate(input, cacheRead int64) *float64 {
	denom := input + cacheRead
	if denom <= 0 {
		return nil
	}
	v := float64(cacheRead) / float64(denom)
	return &v
}

// buildDashboardModelStats 把 by_model 聚合转成含成本的模型统计。
func buildDashboardModelStats(byModel []historymetrics.UsageDashboardRawModelAggregate, pricing PricingSnapshot, adapterMultipliers map[string]float64, defaultMultiplier float64) []UsageDashboardModelStat {
	out := make([]UsageDashboardModelStat, 0, len(byModel))
	for _, m := range byModel {
		price := findPrice(pricing.Models, m.ModelID)
		multiplier := defaultMultiplier
		if am, ok := adapterMultipliers[strings.ToLower(strings.TrimSpace(m.ModelID))]; ok && am > 0 {
			multiplier = am
		}
		stat := UsageDashboardModelStat{
			ModelID:          m.ModelID,
			ModelName:        m.ModelName,
			Provider:         m.Provider,
			ProviderCalls:    m.ProviderCalls,
			InputTokens:      m.InputTokens,
			OutputTokens:     m.OutputTokens,
			CacheReadTokens:  m.CacheReadTokens,
			CacheWriteTokens: m.CacheWriteTokens,
			TotalTokens:      m.TotalTokens,
			RealTotalTokens:  realTotalTokens(m.InputTokens, m.OutputTokens, m.CacheReadTokens, m.CacheWriteTokens),
			CostMultiplier:   multiplier,
			InputCost:        float64(billableInputTokens(m.InputTokens, m.CacheReadTokens, m.CacheWriteTokens, price.InputTokenSemantics)) / 1_000_000 * price.InputPerMillion,
			OutputCost:       float64(m.OutputTokens) / 1_000_000 * price.OutputPerMillion,
			CacheReadCost:    float64(m.CacheReadTokens) / 1_000_000 * price.CacheReadPerMillion,
			CacheWriteCost:   float64(m.CacheWriteTokens) / 1_000_000 * price.CacheWritePerMillion,
		}
		stat.TotalCost = (stat.InputCost + stat.OutputCost + stat.CacheReadCost + stat.CacheWriteCost) * multiplier
		if m.ProviderCalls > 0 {
			stat.AvgCostPerRequest = stat.TotalCost / float64(m.ProviderCalls)
		}
		out = append(out, stat)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TotalCost > out[j].TotalCost })
	return out
}

// aggregateDashboardByProvider 从模型统计派生 provider 维度聚合。
func aggregateDashboardByProvider(byModel []UsageDashboardModelStat) []UsageDashboardProviderStat {
	bucket := make(map[string]*UsageDashboardProviderStat)
	for _, m := range byModel {
		key := strings.TrimSpace(m.Provider)
		if key == "" {
			key = "未知"
		}
		p, ok := bucket[key]
		if !ok {
			p = &UsageDashboardProviderStat{Provider: key}
			bucket[key] = p
		}
		p.ProviderCalls += m.ProviderCalls
		p.InputTokens += m.InputTokens
		p.OutputTokens += m.OutputTokens
		p.CacheReadTokens += m.CacheReadTokens
		p.CacheWriteTokens += m.CacheWriteTokens
		p.TotalTokens += m.TotalTokens
		p.RealTotalTokens += m.RealTotalTokens
		p.TotalCost += m.TotalCost
	}
	out := make([]UsageDashboardProviderStat, 0, len(bucket))
	for _, p := range bucket {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TotalCost > out[j].TotalCost })
	return out
}

// buildDashboardDaily 把按日 token 聚合转成含成本的日趋势。
func buildDashboardDaily(daily []historymetrics.UsageDashboardRawDaily, pricing PricingSnapshot, adapterMultipliers map[string]float64, defaultMultiplier float64) []UsageDashboardDaily {
	out := make([]UsageDashboardDaily, 0, len(daily))
	for _, d := range daily {
		// daily 聚合没有按模型拆分，无法精确套 per-model 价；用全局默认倍率 + 加权平均价近似。
		// 精确按模型日成本需要 daily 维度存 by_model，留作后续 rollup 优化。
		out = append(out, UsageDashboardDaily{
			Date:             d.Date,
			ProviderCalls:    d.ProviderCalls,
			InputTokens:      d.InputTokens,
			OutputTokens:     d.OutputTokens,
			CacheReadTokens:  d.CacheReadTokens,
			CacheWriteTokens: d.CacheWriteTokens,
			TotalTokens:      d.TotalTokens,
			RealTotalTokens:  realTotalTokens(d.InputTokens, d.OutputTokens, d.CacheReadTokens, d.CacheWriteTokens),
			CostUSD:          estimateDailyCost(d, pricing, defaultMultiplier),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

// estimateDailyCost 用全局默认倍率 + 所有已配定价的加权均价近似日成本。
// 这是不精确的（daily 不含模型维度）；仅用于趋势图，精确成本看 byModel。
func estimateDailyCost(d historymetrics.UsageDashboardRawDaily, pricing PricingSnapshot, defaultMultiplier float64) float64 {
	if len(pricing.Models) == 0 {
		return 0
	}
	var weightedInput, weightedOutput, weightedCacheRead, weightedCacheWrite float64
	var totalWeight float64
	for _, m := range pricing.Models {
		// 用 input 价做权重近似（高价模型权重高）
		w := m.InputPerMillion
		if w <= 0 {
			continue
		}
		weightedInput += m.InputPerMillion * w
		weightedOutput += m.OutputPerMillion * w
		weightedCacheRead += m.CacheReadPerMillion * w
		weightedCacheWrite += m.CacheWritePerMillion * w
		totalWeight += w
	}
	if totalWeight <= 0 {
		return 0
	}
	input := float64(d.InputTokens) / 1_000_000 * (weightedInput / totalWeight)
	output := float64(d.OutputTokens) / 1_000_000 * (weightedOutput / totalWeight)
	cacheRead := float64(d.CacheReadTokens) / 1_000_000 * (weightedCacheRead / totalWeight)
	cacheWrite := float64(d.CacheWriteTokens) / 1_000_000 * (weightedCacheWrite / totalWeight)
	return (input + output + cacheRead + cacheWrite) * defaultMultiplier
}

// buildDashboardEvents 把 recent_events 转成含成本估算的日志条目。
func buildDashboardEvents(events []historymetrics.UsageDashboardRawEvent, pricing PricingSnapshot, adapterMultipliers map[string]float64, defaultMultiplier float64) []UsageDashboardEvent {
	out := make([]UsageDashboardEvent, 0, len(events))
	for _, e := range events {
		price := findPrice(pricing.Models, e.ModelID)
		multiplier := defaultMultiplier
		if am, ok := adapterMultipliers[strings.ToLower(strings.TrimSpace(e.ModelID))]; ok && am > 0 {
			multiplier = am
		}
		inputCost := float64(billableInputTokens(e.InputTokens, e.CacheReadTokens, e.CacheWriteTokens, price.InputTokenSemantics)) / 1_000_000 * price.InputPerMillion
		outputCost := float64(e.OutputTokens) / 1_000_000 * price.OutputPerMillion
		cacheReadCost := float64(e.CacheReadTokens) / 1_000_000 * price.CacheReadPerMillion
		cacheWriteCost := float64(e.CacheWriteTokens) / 1_000_000 * price.CacheWritePerMillion
		out = append(out, UsageDashboardEvent{
			EventID:          e.EventID,
			Kind:             e.Kind,
			Status:           e.Status,
			At:               e.At,
			ModelID:          e.ModelID,
			ModelName:        e.ModelName,
			Provider:         e.Provider,
			InputTokens:      e.InputTokens,
			OutputTokens:     e.OutputTokens,
			CacheReadTokens:  e.CacheReadTokens,
			CacheWriteTokens: e.CacheWriteTokens,
			TotalTokens:      e.TotalTokens,
			RealTotalTokens:  realTotalTokens(e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.CacheWriteTokens),
			UsagePresent:     e.UsagePresent,
			CostUSD:          (inputCost + outputCost + cacheReadCost + cacheWriteCost) * multiplier,
		})
	}
	return out
}

func sumModelCosts(stats []UsageDashboardModelStat) float64 {
	var total float64
	for _, s := range stats {
		total += s.TotalCost
	}
	return total
}
