package config

import (
	"context"
	"sort"
	"strings"

	"cursor/internal/modelchannel"
	legacyruntime "cursor/internal/runtime"
)

const (
	defaultChannelTimeoutMS           = int((2 * 60 * 60) * 1000)
	defaultChannelContextWindowTokens = 200_000
	// defaultChannelMaxTokens 默认 128K（131072），对齐 2026 年主流旗舰的最大输出 token 上限：
	// Claude Opus 5 / 4.8 / 4.7 / 4.6、Sonnet 5 / 4.6 均支持 128K max_tokens（流式），
	// GPT-5.x 系列输出上限也在 64K~128K 区间。本地小模型（如 qwen 27B）即便上限更低，
	// provider 会自行截断，byok 设大不会出错。max_tokens 是 thinking + 输出文本的合计硬顶，
	// 不要超过此值以免撞 stop_reason: "max_tokens" 截断。
	defaultChannelMaxTokens      = 131_072
	defaultChannelThinkingBudget = 16_384
	defaultChannelAnthropicEffort = "xhigh"
)

func (manager *Manager) SelectChannelForModel(ctx context.Context, modelID string) (*legacyruntime.ResolvedChannel, error) {
	channels, err := manager.SelectChannelsForModel(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if len(channels) == 0 {
		return nil, legacyruntime.ErrChannelNotAvailable
	}
	return channels[0], nil
}

// SelectChannelsForModel 返回 modelID 命中的**全部**候选渠道（B2 failover 候选链）。
// 候选顺序：精确 ID → legacy ID → providerModelID（由 ResolveAdapterIndexes 保证），
// 再按 adapter.Priority 升序稳定排序（同 priority 保持配置顺序）。
// Enabled==false 的 adapter 不进入候选链（保留配置但不参与路由）。
// 熔断器不可用的候选不在此剔除——registry 在 Router 层持有，由 Router 排到末尾兜底。
func (manager *Manager) SelectChannelsForModel(_ context.Context, modelID string) ([]*legacyruntime.ResolvedChannel, error) {
	if manager == nil {
		return nil, legacyruntime.ErrChannelNotAvailable
	}
	adapters, err := NormalizeModelAdapterConfigs(manager.Current().ModelAdapters)
	if err != nil {
		return nil, err
	}
	return resolveModelAdapterChannels(adapters, modelID)
}

// resolveModelAdapterChannels 是 SelectChannelsForModel 的纯函数核心，便于测试。
func resolveModelAdapterChannels(adapters []ModelAdapterConfig, requestedModel string) ([]*legacyruntime.ResolvedChannel, error) {
	indexes := modelchannel.ResolveAdapterIndexes(
		adapters,
		requestedModel,
		func(adapter ModelAdapterConfig) string { return adapter.ID },
		func(adapter ModelAdapterConfig) string { return adapter.ModelID },
		func(adapter ModelAdapterConfig) string {
			return modelchannel.BuildLegacyChannelID(adapter.BaseURL, adapter.ModelID, adapter.APIKey, adapter.DisplayName)
		},
	)
	if len(indexes) == 0 {
		return nil, nil
	}
	// 收集 enabled 候选并按 Priority 升序稳定排序（同 priority 保持 ResolveAdapterIndexes 的命中顺序）。
	type indexed struct {
		idx      int
		priority int
	}
	enabled := make([]indexed, 0, len(indexes))
	for _, idx := range indexes {
		adapter := adapters[idx]
		if adapter.Enabled != nil && !*adapter.Enabled {
			continue
		}
		enabled = append(enabled, indexed{idx: idx, priority: adapter.Priority})
	}
	if len(enabled) == 0 {
		return nil, nil
	}
	sort.SliceStable(enabled, func(i, j int) bool {
		return enabled[i].priority < enabled[j].priority
	})
	channels := make([]*legacyruntime.ResolvedChannel, 0, len(enabled))
	for _, entry := range enabled {
		channels = append(channels, buildResolvedChannel(adapters[entry.idx]))
	}
	return channels, nil
}

// buildResolvedChannel 把单个 ModelAdapterConfig 构造为 ResolvedChannel（resolver 与 FixedChannelService 共享语义）。
func buildResolvedChannel(matched ModelAdapterConfig) *legacyruntime.ResolvedChannel {
	resolved := &legacyruntime.ResolvedChannel{
		ID:                          strings.TrimSpace(matched.ID),
		Name:                        strings.TrimSpace(matched.DisplayName),
		GroupName:                   "local",
		Code:                        strings.TrimSpace(matched.ID),
		Provider:                    strings.TrimSpace(matched.Type),
		BaseURL:                     strings.TrimSpace(matched.BaseURL),
		APIKey:                      strings.TrimSpace(matched.APIKey),
		Model:                       strings.TrimSpace(matched.ModelID),
		OpenAIEndpoint:              strings.TrimSpace(matched.OpenAIEndpoint),
		OpenAIExtraParamsEnabled:    matched.OpenAIExtraParamsEnabled,
		OpenAIExtraParamsJSON:       strings.TrimSpace(matched.OpenAIExtraParamsJSON),
		CustomHeadersEnabled:        matched.CustomHeadersEnabled,
		CustomHeadersJSON:           strings.TrimSpace(matched.CustomHeadersJSON),
		AnthropicExtraParamsEnabled: matched.AnthropicExtraParamsEnabled,
		AnthropicExtraParamsJSON:    strings.TrimSpace(matched.AnthropicExtraParamsJSON),
		TimeoutMS:                   defaultChannelTimeoutMS,
		ContextWindowTokens:         defaultChannelContextWindowTokens,
		MaxTokens:                   defaultChannelMaxTokens,
		ReasoningEffort:             strings.TrimSpace(matched.ReasoningEffort),
		AnthropicMaxTokens:          defaultChannelMaxTokens,
		AnthropicThinkingEffort:     defaultChannelAnthropicEffort,
		ThinkingEnabled:             true,
		ThinkingBudgetTokens:        defaultChannelThinkingBudget,
	}
	if matched.ContextWindowTokens > 0 {
		resolved.ContextWindowTokens = matched.ContextWindowTokens
	}
	if matched.MaxCompletionTokens > 0 {
		resolved.MaxTokens = matched.MaxCompletionTokens
	}
	if matched.AnthropicMaxTokens > 0 {
		resolved.AnthropicMaxTokens = matched.AnthropicMaxTokens
	}
	if matched.ThinkingBudgetTokens > 0 {
		resolved.ThinkingBudgetTokens = matched.ThinkingBudgetTokens
	}
	if strings.TrimSpace(matched.AnthropicThinkingEffort) != "" {
		resolved.AnthropicThinkingEffort = strings.TrimSpace(matched.AnthropicThinkingEffort)
	}
	return resolved
}
