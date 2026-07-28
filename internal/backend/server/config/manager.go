package config

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	legacyruntime "cursor/internal/runtime"
)

const configHotReloadMinInterval = 500 * time.Millisecond

type Manager struct {
	store       *Store
	current     atomic.Pointer[Config]
	listenersMu sync.RWMutex
	listeners   []func(Config)
	reloadMu    sync.Mutex
	snapshot    fileSnapshot
	lastReload  time.Time
	reloadError string
}

func NewManager(ctx context.Context, store *Store) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("config store is required")
	}
	cfg, err := store.Load(ctx)
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		store:    store,
		snapshot: store.snapshot(),
	}
	manager.setCurrent(cfg)
	return manager, nil
}

func (manager *Manager) Current() Config {
	if manager == nil {
		return DefaultConfig()
	}
	manager.reloadIfChanged(context.Background())
	return manager.currentConfig()
}

func (manager *Manager) currentConfig() Config {
	if manager == nil {
		return DefaultConfig()
	}
	if current := manager.current.Load(); current != nil {
		return *current
	}
	return DefaultConfig()
}

func (manager *Manager) Load(ctx context.Context) (Config, error) {
	if manager == nil {
		return DefaultConfig(), nil
	}
	manager.reloadIfChanged(ctx)
	return manager.currentConfig(), nil
}

func (manager *Manager) Save(ctx context.Context, cfg Config) (Config, error) {
	if manager == nil || manager.store == nil {
		return Config{}, fmt.Errorf("config manager is not initialized")
	}
	normalized, err := manager.store.Save(ctx, cfg)
	if err != nil {
		return Config{}, err
	}
	manager.setCurrent(normalized)
	manager.reloadMu.Lock()
	manager.snapshot = manager.store.snapshot()
	manager.lastReload = time.Now()
	manager.reloadError = ""
	manager.reloadMu.Unlock()
	manager.notify(normalized)
	return normalized, nil
}

// Update 在 Store 锁内事务中执行 Load-Modify-Save（F-03）。
//
// mutator 接收以磁盘最新配置为基线的 *Config，修改后由 Store 原子写回，
// 消除此前 Load/Save 分锁导致的并发覆盖。写回后同步 current 快照与监听器。
func (manager *Manager) Update(ctx context.Context, mutator func(*Config) error) (Config, error) {
	if manager == nil || manager.store == nil {
		return Config{}, fmt.Errorf("config manager is not initialized")
	}
	normalized, err := manager.store.Update(ctx, mutator)
	if err != nil {
		return Config{}, err
	}
	manager.setCurrent(normalized)
	manager.reloadMu.Lock()
	manager.snapshot = manager.store.snapshot()
	manager.lastReload = time.Now()
	manager.reloadError = ""
	manager.reloadMu.Unlock()
	manager.notify(normalized)
	return normalized, nil
}

// MergeUserPatch 把前端整包 patch merge 到磁盘最新配置（F-02）。
// 在 Store.Update 锁内事务完成 Load-Merge-Save：前端管理字段覆盖、Pricing 等后端独占字段
// 保留磁盘值、per-adapter CostMultiplier 按身份键从磁盘旧值继承。
// 消除前端整包保存丢弃 pricing/tabServerBaseURL/costMultiplier 的问题，且与并发
// Pricing/hash 更新互不覆盖（F-03 事务）。
func (manager *Manager) MergeUserPatch(ctx context.Context, patch Config) (Config, error) {
	if manager == nil || manager.store == nil {
		return Config{}, fmt.Errorf("config manager is not initialized")
	}
	return manager.Update(ctx, func(cfg *Config) error {
		mergeUserPatchInto(cfg, patch)
		return nil
	})
}

func (manager *Manager) LastAgentModelHash() string {
	if manager == nil {
		return ""
	}
	return strings.TrimSpace(manager.Current().LastAgentModelHash)
}

func (manager *Manager) SaveLastAgentModelHash(ctx context.Context, value string) error {
	if manager == nil {
		return fmt.Errorf("config manager is not initialized")
	}
	normalizedValue := strings.TrimSpace(value)
	// F-03：改用 Update 锁内事务——此前 Current()+Save() 不在同一临界区，
	// 与并发的 Pricing/前端保存互相覆盖会丢失 hash 或丢失对方字段。
	_, err := manager.Update(ctx, func(cfg *Config) error {
		if strings.TrimSpace(cfg.LastAgentModelHash) == normalizedValue {
			// 值未变，仍走正常写回路径（幂等）；用 sentinel 提示调用方无需变更。
			return errConfigUnchanged
		}
		cfg.LastAgentModelHash = normalizedValue
		return nil
	})
	if err != nil && err != errConfigUnchanged {
		return err
	}
	return nil
}

// errConfigUnchanged 是 SaveLastAgentModelHash 的内部 sentinel：
// mutator 判定无需变更时返回，Update 仍会写回一次（幂等），调用方视为成功。
var errConfigUnchanged = errors.New("config unchanged")

func (manager *Manager) ProviderStreamIdleTimeout(ctx context.Context) time.Duration {
	if manager == nil {
		return time.Duration(DefaultProviderStreamIdleTimeoutSeconds) * time.Second
	}
	manager.reloadIfChanged(ctx)
	seconds := normalizeProviderStreamIdleTimeout(manager.currentConfig().ProviderStreamIdleTimeout)
	return time.Duration(seconds) * time.Second
}

func (manager *Manager) IsObservabilityLogEnabled(ctx context.Context) bool {
	if manager == nil {
		return false
	}
	manager.reloadIfChanged(ctx)
	return manager.currentConfig().Log
}

func (manager *Manager) Subscribe(listener func(Config)) func() {
	if manager == nil || listener == nil {
		return func() {}
	}
	manager.listenersMu.Lock()
	manager.listeners = append(manager.listeners, listener)
	index := len(manager.listeners) - 1
	manager.listenersMu.Unlock()
	return func() {
		manager.listenersMu.Lock()
		defer manager.listenersMu.Unlock()
		if index < 0 || index >= len(manager.listeners) {
			return
		}
		manager.listeners[index] = nil
	}
}

func (manager *Manager) LegacyRuntimeSnapshot(_ context.Context) (legacyruntime.RuntimeConfigSnapshot, error) {
	cfg := manager.Current()
	adapters := make([]legacyruntime.ModelAdapterConfig, 0, len(cfg.ModelAdapters))
	for _, item := range cfg.ModelAdapters {
		adapters = append(adapters, legacyruntime.ModelAdapterConfig{
			ID:                       item.ID,
			DisplayName:              item.DisplayName,
			Type:                     item.Type,
			BaseURL:                  item.BaseURL,
			APIKey:                   item.APIKey,
			TooltipData:              item.TooltipData,
			ModelID:                  item.ModelID,
			ReasoningEffort:          item.ReasoningEffort,
			OpenAIEndpoint:           item.OpenAIEndpoint,
			OpenAIExtraParamsEnabled: item.OpenAIExtraParamsEnabled,
			OpenAIExtraParamsJSON:    item.OpenAIExtraParamsJSON,
			ContextWindowTokens:      item.ContextWindowTokens,
			MaxCompletionTokens:      item.MaxCompletionTokens,
			AnthropicMaxTokens:       item.AnthropicMaxTokens,
			AnthropicThinkingEffort:  item.AnthropicThinkingEffort,
			ThinkingBudgetTokens:     item.ThinkingBudgetTokens,
			Priority:                 item.Priority,
			Enabled:                  item.Enabled == nil || *item.Enabled,
			Weight:                   item.Weight,
		})
	}
	return legacyruntime.RuntimeConfigSnapshot{
		ObservabilityLogEnabled:   cfg.Log,
		ProviderStreamIdleTimeout: cfg.ProviderStreamIdleTimeout,
		ModelAdapters:             adapters,
	}, nil
}

func (manager *Manager) RouteMode(hasUpstreamURL bool) string {
	if !hasUpstreamURL {
		return DefaultRoutingMode
	}
	if manager == nil {
		return DefaultRoutingMode
	}
	mode := normalizeRoutingMode(manager.Current().Routing.Mode)
	if mode == "" {
		return DefaultRoutingMode
	}
	return mode
}

func (manager *Manager) setCurrent(cfg Config) {
	next := cfg
	manager.current.Store(&next)
}

func (manager *Manager) reloadIfChanged(ctx context.Context) {
	if manager == nil || manager.store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	manager.reloadMu.Lock()
	if !manager.lastReload.IsZero() && now.Sub(manager.lastReload) < configHotReloadMinInterval {
		manager.reloadMu.Unlock()
		return
	}
	manager.lastReload = now
	nextSnapshot := manager.store.snapshot()
	if nextSnapshot == manager.snapshot {
		manager.reloadMu.Unlock()
		return
	}
	cfg, err := manager.store.Load(ctx)
	if err != nil {
		errText := err.Error()
		if errText != manager.reloadError {
			log.Printf("config hot reload skipped path=%s error=%v", manager.store.Path(), err)
			manager.reloadError = errText
		}
		manager.reloadMu.Unlock()
		return
	}
	manager.snapshot = nextSnapshot
	manager.reloadError = ""
	manager.setCurrent(cfg)
	manager.reloadMu.Unlock()
	manager.notify(cfg)
}

func (manager *Manager) notify(cfg Config) {
	manager.listenersMu.RLock()
	listeners := append([]func(Config){}, manager.listeners...)
	manager.listenersMu.RUnlock()
	for _, listener := range listeners {
		if listener != nil {
			listener(cfg)
		}
	}
}
