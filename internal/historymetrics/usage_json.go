package historymetrics

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

// usageFileDocument 是 ~/.cursor-local-assistant-v2/history/usage.json 的读取视图。
// 字段与 forwarder.usageFileDocument 对齐（仅读取仪表盘需要的列）。
type usageFileDocument struct {
	SchemaVersion int                       `json:"schema_version"`
	UpdatedAt     time.Time                 `json:"updated_at"`
	Totals       usageFileTotals           `json:"totals"`
	Daily        []usageFileDaily          `json:"daily"`
	RecentEvents []usageFileEvent          `json:"recent_events"`
	ByModel      map[string]usageFileModelAggregate `json:"by_model,omitempty"`
}

type usageFileTotals struct {
	ProviderCalls     int64 `json:"provider_calls"`
	TurnsTotal        int64 `json:"turns_total"`
	ValidTurnsTotal   int64 `json:"valid_turns_total"`
	InvalidTurnsTotal int64 `json:"invalid_turns_total"`
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CacheReadTokens   int64 `json:"cache_read_tokens"`
	CacheWriteTokens  int64 `json:"cache_write_tokens"`
	TotalTokens       int64 `json:"total_tokens"`
}

type usageFileDaily struct {
	Date              string `json:"date"`
	ProviderCalls     int64  `json:"provider_calls"`
	TurnsTotal        int64  `json:"turns_total"`
	ValidTurnsTotal   int64  `json:"valid_turns_total"`
	InvalidTurnsTotal int64  `json:"invalid_turns_total"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	CacheReadTokens   int64  `json:"cache_read_tokens"`
	CacheWriteTokens  int64  `json:"cache_write_tokens"`
	TotalTokens       int64  `json:"total_tokens"`
	// ByModel 是该日按 model_id 拆分的 token 用量，用于精确按模型日成本计算。
	// 旧版 usage.json 此字段为空，仪表盘会回退到加权均价近似。
	ByModel map[string]usageFileDailyModel `json:"by_model,omitempty"`
}

// usageFileDailyModel 是单日单模型的 token 聚合（与 forwarder.usageFileDailyModel 对齐）。
type usageFileDailyModel struct {
	ModelID          string `json:"model_id"`
	ProviderCalls    int64  `json:"provider_calls"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
}

type usageFileEvent struct {
	EventID          string    `json:"event_id"`
	Kind             string    `json:"kind,omitempty"`
	Status           string    `json:"status,omitempty"`
	At               time.Time `json:"at"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
	TotalTokens      int64     `json:"total_tokens"`
	UsagePresent     bool      `json:"usage_present"`
	ModelID          string    `json:"model_id,omitempty"`
	ModelName        string    `json:"model_name,omitempty"`
	Provider         string    `json:"provider,omitempty"`
}

type usageFileModelAggregate struct {
	ModelID          string `json:"model_id"`
	ModelName        string `json:"model_name,omitempty"`
	Provider         string `json:"provider,omitempty"`
	ProviderCalls    int64  `json:"provider_calls"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
}

// LoadUsageSummary 返回首页展示的全量历史统计摘要（向后兼容）。
func LoadUsageSummary(path string) (Summary, error) {
	doc, err := loadUsageDocument(path)
	if err != nil {
		return Summary{}, err
	}
	totals := Totals{
		InputTokens:        doc.Totals.InputTokens,
		OutputTokens:       doc.Totals.OutputTokens,
		CacheReadTokens:    doc.Totals.CacheReadTokens,
		CacheWriteTokens:   doc.Totals.CacheWriteTokens,
		PromptTokensTotal:  doc.Totals.InputTokens + doc.Totals.CacheReadTokens + doc.Totals.CacheWriteTokens,
		RequestTokensTotal: doc.Totals.TotalTokens,
	}
	byModel := make([]ModelUsage, 0, len(doc.ByModel))
	for _, m := range doc.ByModel {
		byModel = append(byModel, ModelUsage{
			ModelID:          m.ModelID,
			ModelName:        m.ModelName,
			Provider:         m.Provider,
			ProviderCalls:    m.ProviderCalls,
			InputTokens:      m.InputTokens,
			OutputTokens:     m.OutputTokens,
			CacheReadTokens:  m.CacheReadTokens,
			CacheWriteTokens: m.CacheWriteTokens,
			TotalTokens:      m.TotalTokens,
		})
	}
	return Summary{
		ProviderCallsTotal: int(doc.Totals.ProviderCalls),
		TurnsTotal:         int(doc.Totals.TurnsTotal),
		ValidTurnsTotal:    int(doc.Totals.ValidTurnsTotal),
		InvalidTurnsTotal:  int(doc.Totals.InvalidTurnsTotal),
		RequestTokensTotal: totals.RequestTokensTotal,
		PromptTokensTotal:  totals.PromptTokensTotal,
		CacheReadTokens:    totals.CacheReadTokens,
		CacheWriteTokens:   totals.CacheWriteTokens,
		CacheHitRate:       cacheHitRateFromTotals(totals),
		ByModel:            byModel,
	}, nil
}

// LoadUsageDocument 返回完整的 usage.json 视图，供使用统计仪表盘使用。
// 含 totals / daily / by_model / recent_events，按日期与模型排序后返回。
func LoadUsageDocument(path string) (*UsageDashboardRaw, error) {
	doc, err := loadUsageDocument(path)
	if err != nil {
		return nil, err
	}
	raw := &UsageDashboardRaw{
		UpdatedAt:     doc.UpdatedAt,
		Totals:        doc.Totals,
		Daily:         append([]usageFileDaily(nil), doc.Daily...),
		RecentEvents:  append([]usageFileEvent(nil), doc.RecentEvents...),
	}
	for _, m := range doc.ByModel {
		raw.ByModel = append(raw.ByModel, m)
	}
	sort.Slice(raw.Daily, func(i, j int) bool { return raw.Daily[i].Date < raw.Daily[j].Date })
	sort.Slice(raw.ByModel, func(i, j int) bool {
		return raw.ByModel[i].ModelID < raw.ByModel[j].ModelID
	})
	// recent_events 倒序（最新在前）
	sort.Slice(raw.RecentEvents, func(i, j int) bool { return raw.RecentEvents[i].At.After(raw.RecentEvents[j].At) })
	return raw, nil
}

func loadUsageDocument(path string) (*usageFileDocument, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &usageFileDocument{}, nil
		}
		return nil, fmt.Errorf("read usage file: %w", err)
	}
	var doc usageFileDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode usage file: %w", err)
	}
	if doc.ByModel == nil {
		doc.ByModel = map[string]usageFileModelAggregate{}
	}
	if doc.Daily == nil {
		doc.Daily = []usageFileDaily{}
	}
	if doc.RecentEvents == nil {
		doc.RecentEvents = []usageFileEvent{}
	}
	return &doc, nil
}
