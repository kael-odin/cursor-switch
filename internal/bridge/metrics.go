package bridge

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"cursor/internal/appdata"
	"cursor/internal/backend/server/config"
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
	// EstimatedCostUSD 是按模型定价 + 倍率估算的总成本（美元）。
	EstimatedCostUSD *float64 `json:"estimatedCostUSD"`
	// CostByModel 是按模型拆分的成本明细。
	CostByModel []ModelCost `json:"costByModel"`
}

// TokenPricing 定义单个模型的 token 单价（每百万 token，美元）。
// 与 config.ModelPricing 对应，供前端展示/编辑。
type TokenPricing struct {
	ModelID           string  `json:"modelId"`
	DisplayName       string  `json:"displayName"`
	InputPerMillion   float64 `json:"inputPerMillion"`
	OutputPerMillion  float64 `json:"outputPerMillion"`
	CacheReadPerMillion  float64 `json:"cacheReadPerMillion"`
	CacheWritePerMillion float64 `json:"cacheWritePerMillion"`
}

// PricingSnapshot 是定价配置的完整快照：全局倍率 + 全部模型定价。
type PricingSnapshot struct {
	DefaultCostMultiplier float64       `json:"defaultCostMultiplier"`
	Models                []TokenPricing `json:"models"`
}

// ModelCost 是单模型的成本估算明细。
type ModelCost struct {
	ModelID            string  `json:"modelId"`
	ModelName          string  `json:"modelName,omitempty"`
	InputTokens        int64   `json:"inputTokens"`
	OutputTokens       int64   `json:"outputTokens"`
	CacheReadTokens    int64   `json:"cacheReadTokens"`
	CacheWriteTokens   int64   `json:"cacheWriteTokens"`
	InputCost          float64 `json:"inputCost"`
	OutputCost         float64 `json:"outputCost"`
	CacheReadCost      float64 `json:"cacheReadCost"`
	CacheWriteCost     float64 `json:"cacheWriteCost"`
	TotalCost          float64 `json:"totalCost"`
	CostMultiplier     float64 `json:"costMultiplier"`
}

// MetricsService 定义首页统计与成本定价相关的 Wails service。
type MetricsService struct {
	store *config.Store
}

// NewMetricsService 创建首页统计 service。
// store 用于读写定价配置（config.yaml 的 pricing 段）。可为 nil（退化到默认价）。
func NewMetricsService(store *config.Store) *MetricsService {
	return &MetricsService{store: store}
}

// GetHomeMetricsSummary 返回首页展示的全量历史统计摘要 + 按模型成本估算。
func (service *MetricsService) GetHomeMetricsSummary() (HomeMetricsSummary, error) {
	if err := appdata.EnsureAssistantHome(); err != nil {
		return HomeMetricsSummary{}, err
	}

	summary, err := historymetrics.LoadUsageSummary(appdata.UsageFilePath())
	if err != nil {
		return HomeMetricsSummary{}, err
	}
	result := HomeMetricsSummary{
		ProviderCallsTotal: summary.ProviderCallsTotal,
		TurnsTotal:         summary.TurnsTotal,
		ValidTurnsTotal:    summary.ValidTurnsTotal,
		InvalidTurnsTotal:  summary.InvalidTurnsTotal,
		RequestTokensTotal: summary.RequestTokensTotal,
		PromptTokensTotal:  summary.PromptTokensTotal,
		CacheReadTokens:    summary.CacheReadTokens,
		CacheWriteTokens:   summary.CacheWriteTokens,
		CacheHitRate:       summary.CacheHitRate,
	}
	pricing := service.loadPricing()
	adapterMultipliers := service.loadAdapterMultipliers()
	costByModel, total := computeCostByModel(summary.ByModel, pricing, adapterMultipliers)
	result.CostByModel = costByModel
	result.EstimatedCostUSD = &total
	return result, nil
}

// GetTokenPricing 已废弃：保留兼容旧前端调用，返回首个模型或默认价。
// 新前端应改用 GetPricingSnapshot 获取完整定价表。
func (service *MetricsService) GetTokenPricing() TokenPricing {
	pricing := service.loadPricing()
	if len(pricing.Models) > 0 {
		return pricing.Models[0]
	}
	return TokenPricing{
		ModelID:           "claude-opus-4-7",
		DisplayName:       "Claude Opus 4.7",
		InputPerMillion:   5,
		OutputPerMillion:  25,
		CacheReadPerMillion:  0.5,
		CacheWritePerMillion: 6.25,
	}
}

// GetPricingSnapshot 返回完整定价配置（全局倍率 + 全部模型定价）。
func (service *MetricsService) GetPricingSnapshot() (PricingSnapshot, error) {
	return service.loadPricing(), nil
}

// UpdateModelPricing 新增或更新单个模型定价（upsert）。
func (service *MetricsService) UpdateModelPricing(pricing TokenPricing) error {
	if service.store == nil {
		return fmt.Errorf("config store unavailable")
	}
	modelID := strings.TrimSpace(pricing.ModelID)
	if modelID == "" {
		return fmt.Errorf("modelId 不能为空")
	}
	ctx := context.Background()
	cfg, err := service.store.Load(ctx)
	if err != nil {
		return err
	}
	updated := config.ModelPricing{
		ModelID:              modelID,
		DisplayName:          strings.TrimSpace(pricing.DisplayName),
		InputPerMillion:      strconv.FormatFloat(pricing.InputPerMillion, 'f', -1, 64),
		OutputPerMillion:     strconv.FormatFloat(pricing.OutputPerMillion, 'f', -1, 64),
		CacheReadPerMillion:  strconv.FormatFloat(pricing.CacheReadPerMillion, 'f', -1, 64),
		CacheWritePerMillion: strconv.FormatFloat(pricing.CacheWritePerMillion, 'f', -1, 64),
	}
	found := false
	for i := range cfg.Pricing.Models {
		if strings.EqualFold(strings.TrimSpace(cfg.Pricing.Models[i].ModelID), modelID) {
			cfg.Pricing.Models[i] = updated
			found = true
			break
		}
	}
	if !found {
		cfg.Pricing.Models = append(cfg.Pricing.Models, updated)
	}
	_, err = service.store.Save(ctx, cfg)
	return err
}

// DeleteModelPricing 删除单个模型定价记录。
func (service *MetricsService) DeleteModelPricing(modelID string) error {
	if service.store == nil {
		return fmt.Errorf("config store unavailable")
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return fmt.Errorf("modelId 不能为空")
	}
	ctx := context.Background()
	cfg, err := service.store.Load(ctx)
	if err != nil {
		return err
	}
	next := make([]config.ModelPricing, 0, len(cfg.Pricing.Models))
	for _, m := range cfg.Pricing.Models {
		if !strings.EqualFold(strings.TrimSpace(m.ModelID), modelID) {
			next = append(next, m)
		}
	}
	cfg.Pricing.Models = next
	_, err = service.store.Save(ctx, cfg)
	return err
}

// SetDefaultCostMultiplier 设置全局默认成本倍率。
func (service *MetricsService) SetDefaultCostMultiplier(value string) error {
	if service.store == nil {
		return fmt.Errorf("config store unavailable")
	}
	value = strings.TrimSpace(value)
	if _, err := strconv.ParseFloat(value, 64); err != nil {
		return fmt.Errorf("倍率必须是数字: %w", err)
	}
	ctx := context.Background()
	cfg, err := service.store.Load(ctx)
	if err != nil {
		return err
	}
	cfg.Pricing.DefaultCostMultiplier = value
	_, err = service.store.Save(ctx, cfg)
	return err
}

// loadPricing 读取定价配置快照。store 缺失时返回默认价。
func (service *MetricsService) loadPricing() PricingSnapshot {
	cfg := service.loadConfig()
	return toPricingSnapshot(cfg.Pricing)
}

// loadAdapterMultipliers 读取 per-adapter 成本倍率（按归一化 modelId 索引）。
// 仅返回显式设置了 CostMultiplier 的适配器。
func (service *MetricsService) loadAdapterMultipliers() map[string]float64 {
	cfg := service.loadConfig()
	out := make(map[string]float64)
	for _, a := range cfg.ModelAdapters {
		value := strings.TrimSpace(a.CostMultiplier)
		if value == "" {
			continue
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || parsed <= 0 {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(a.ModelID))] = parsed
	}
	return out
}

// loadConfig 读取完整配置；store 缺失或出错时返回默认配置。
func (service *MetricsService) loadConfig() config.Config {
	if service.store == nil {
		return config.DefaultConfig()
	}
	ctx := context.Background()
	cfg, err := service.store.Load(ctx)
	if err != nil {
		return config.DefaultConfig()
	}
	return cfg
}

// toPricingSnapshot 把 config.PricingConfig 转成前端用的 PricingSnapshot（字符串价 → float64）。
func toPricingSnapshot(in config.PricingConfig) PricingSnapshot {
	out := PricingSnapshot{
		DefaultCostMultiplier: parseFloatOr(in.DefaultCostMultiplier, 1),
		Models:                make([]TokenPricing, 0, len(in.Models)),
	}
	for _, m := range in.Models {
		out.Models = append(out.Models, TokenPricing{
			ModelID:              m.ModelID,
			DisplayName:          m.DisplayName,
			InputPerMillion:      parseFloatOr(m.InputPerMillion, 0),
			OutputPerMillion:     parseFloatOr(m.OutputPerMillion, 0),
			CacheReadPerMillion:  parseFloatOr(m.CacheReadPerMillion, 0),
			CacheWritePerMillion: parseFloatOr(m.CacheWritePerMillion, 0),
		})
	}
	return out
}

// computeCostByModel 按模型用量 × 定价 × 倍率计算成本。返回明细 + 总成本。
// multiplier 解析优先级：per-adapter（adapterMultipliers 按 modelId 查）> 全局默认 > 1。
func computeCostByModel(byModel []historymetrics.ModelUsage, pricing PricingSnapshot, adapterMultipliers map[string]float64) ([]ModelCost, float64) {
	defaultMultiplier := pricing.DefaultCostMultiplier
	if defaultMultiplier <= 0 {
		defaultMultiplier = 1
	}
	costs := make([]ModelCost, 0, len(byModel))
	var total float64
	for _, usage := range byModel {
		price := findPrice(pricing.Models, usage.ModelID)
		multiplier := defaultMultiplier
		if am, ok := adapterMultipliers[strings.ToLower(strings.TrimSpace(usage.ModelID))]; ok && am > 0 {
			multiplier = am
		}
		cost := ModelCost{
			ModelID:          usage.ModelID,
			ModelName:        usage.ModelName,
			InputTokens:      usage.InputTokens,
			OutputTokens:     usage.OutputTokens,
			CacheReadTokens:  usage.CacheReadTokens,
			CacheWriteTokens: usage.CacheWriteTokens,
			CostMultiplier:   multiplier,
		}
		cost.InputCost = float64(usage.InputTokens) / 1_000_000 * price.InputPerMillion
		cost.OutputCost = float64(usage.OutputTokens) / 1_000_000 * price.OutputPerMillion
		cost.CacheReadCost = float64(usage.CacheReadTokens) / 1_000_000 * price.CacheReadPerMillion
		cost.CacheWriteCost = float64(usage.CacheWriteTokens) / 1_000_000 * price.CacheWritePerMillion
		cost.TotalCost = (cost.InputCost + cost.OutputCost + cost.CacheReadCost + cost.CacheWriteCost) * multiplier
		costs = append(costs, cost)
		total += cost.TotalCost
	}
	return costs, total
}

// findPrice 按 modelId 归一化匹配定价，未命中返回零价（成本 0）。
func findPrice(models []TokenPricing, modelID string) TokenPricing {
	want := strings.ToLower(strings.TrimSpace(modelID))
	for _, m := range models {
		if strings.ToLower(strings.TrimSpace(m.ModelID)) == want {
			return m
		}
	}
	return TokenPricing{ModelID: modelID}
}

func parseFloatOr(value string, fallback float64) float64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
}
