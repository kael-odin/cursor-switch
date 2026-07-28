package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Store struct {
	path     string
	logsRoot string
	mu       sync.Mutex
}

type fileSnapshot struct {
	exists  bool
	modTime int64
	size    int64
}

func NewStore(path string, logsRoot string) *Store {
	return &Store{
		path:     strings.TrimSpace(path),
		logsRoot: strings.TrimSpace(logsRoot),
	}
}

func (store *Store) Path() string {
	if store == nil {
		return ""
	}
	return store.path
}

func (store *Store) LogsRoot() string {
	if store == nil {
		return ""
	}
	return store.logsRoot
}

func (store *Store) snapshot() fileSnapshot {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return fileSnapshot{}
	}
	info, err := os.Stat(store.path)
	if err != nil {
		return fileSnapshot{}
	}
	return fileSnapshot{
		exists:  true,
		modTime: info.ModTime().UnixNano(),
		size:    info.Size(),
	}
}

func (store *Store) Load(_ context.Context) (Config, error) {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return DefaultConfig(), nil
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	data, err := os.ReadFile(store.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			defaultConfig := DefaultConfig()
			if err := store.saveLocked(defaultConfig); err != nil {
				return DefaultConfig(), err
			}
			return defaultConfig, nil
		}
		return DefaultConfig(), fmt.Errorf("读取用户配置失败: %w", err)
	}

	var current Config
	if err := yaml.Unmarshal(data, &current); err != nil {
		return DefaultConfig(), fmt.Errorf("解析用户配置失败: %w", err)
	}
	normalized, err := NormalizeConfig(current)
	if err != nil {
		return DefaultConfig(), err
	}
	if shouldPersistNormalizedConfig(data, current, normalized) {
		if err := store.saveLocked(normalized); err != nil {
			return DefaultConfig(), err
		}
	}
	return normalized, nil
}

func (store *Store) Save(_ context.Context, cfg Config) (Config, error) {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return Config{}, errors.New("配置存储未初始化")
	}

	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		return Config{}, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if err := store.saveLocked(normalized); err != nil {
		return Config{}, err
	}
	return normalized, nil
}

// Update 在同一把 store.mu 临界区内执行 Load-Modify-Save（F-03）。
//
// 此前 Load 和 Save 各自加锁，完整读改写不在同一临界区——前端保存、Pricing CRUD
// 与 lastAgentModelHash 更新并发时，后写者会基于陈旧基线覆盖先写者的字段。
// Update 把 mutator 闭包置于锁内：以磁盘最新配置为基线，mutator 修改后立即写回，
// 消除 Load/Save 间隙的竞态。mutator 返回 error 时整体回滚（不写）。
//
// 返回写回并 normalize 后的配置。F-02 的后端 merge 也可基于此：前端整包保存
// 改走 Update，在 mutator 内以磁盘最新值为基线 merge 前端字段，避免丢弃未知字段。
func (store *Store) Update(_ context.Context, mutator func(*Config) error) (Config, error) {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return Config{}, errors.New("配置存储未初始化")
	}
	if mutator == nil {
		return Config{}, errors.New("配置更新 mutator 为空")
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	// 锁内读取最新配置作为基线（与 Load 同语义，但不触发"缺失则写默认"以外的副作用）。
	var baseline Config
	data, err := os.ReadFile(store.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			baseline = DefaultConfig()
			if saveErr := store.saveLocked(baseline); saveErr != nil {
				return Config{}, saveErr
			}
		} else {
			return Config{}, fmt.Errorf("读取用户配置失败: %w", err)
		}
	} else {
		if err := yaml.Unmarshal(data, &baseline); err != nil {
			return Config{}, fmt.Errorf("解析用户配置失败: %w", err)
		}
		baseline, err = NormalizeConfig(baseline)
		if err != nil {
			return Config{}, err
		}
	}

	// mutator 在基线副本上修改。
	if err := mutator(&baseline); err != nil {
		return Config{}, err
	}

	normalized, err := NormalizeConfig(baseline)
	if err != nil {
		return Config{}, err
	}
	if err := store.saveLocked(normalized); err != nil {
		return Config{}, err
	}
	return normalized, nil
}

func (store *Store) saveLocked(normalized Config) error {
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		return fmt.Errorf("创建用户配置目录失败: %w", err)
	}

	data, err := yaml.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("序列化用户配置失败: %w", err)
	}

	tempPath := store.path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0o644); err != nil {
		return fmt.Errorf("写入临时配置失败: %w", err)
	}
	if err := os.Rename(tempPath, store.path); err != nil {
		return fmt.Errorf("保存用户配置失败: %w", err)
	}
	return nil
}

func shouldPersistNormalizedConfig(raw []byte, current Config, normalized Config) bool {
	if !yamlHasKey(raw, "backendListenAddr") || !yamlHasKey(raw, "proxyListenAddr") {
		return true
	}
	if current.BackendListenAddr != normalized.BackendListenAddr || current.ProxyListenAddr != normalized.ProxyListenAddr {
		return true
	}
	if current.ProviderStreamIdleTimeout == normalized.ProviderStreamIdleTimeout {
		return false
	}
	return yamlHasKey(raw, "providerStreamIdleTimeout")
}

func yamlHasKey(raw []byte, key string) bool {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return false
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return false
	}
	mapping := root.Content[0]
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return true
		}
	}
	return false
}
