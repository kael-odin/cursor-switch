package client

import (
	"context"

	"cursor/internal/appdata"
	serverconfig "cursor/internal/backend/server/config"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// UserConfig 定义了当前模块中的 UserConfig 类型。
type UserConfig = serverconfig.Config

// ConfigStore 返回底层配置存储，供其它服务（如 MetricsService 的定价 CRUD）共享读写 config.yaml。
func (s *ProxyService) ConfigStore() *serverconfig.Store {
	if s == nil {
		return nil
	}
	return s.store
}

// LoadUserConfig 用于处理与 LoadUserConfig 相关的逻辑。
func (s *ProxyService) LoadUserConfig() (UserConfig, error) {
	if s == nil {
		return serverconfig.DefaultConfig(), nil
	}
	app := application.Get()
	ctx := context.Background()
	if app != nil {
		ctx = app.Context()
	}
	if s.backendHost != nil {
		return s.backendHost.LoadConfig(ctx)
	}
	if s.store == nil {
		return serverconfig.DefaultConfig(), nil
	}
	return s.store.Load(ctx)
}

// SaveUserConfig 用于处理与 SaveUserConfig 相关的逻辑。
func (s *ProxyService) SaveUserConfig(cfg UserConfig) error {
	if s == nil {
		return nil
	}
	// F-35：串行化 Start/Stop/Save，避免 Save 与 Start/Stop 并发改 store + rebuild。
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	app := application.Get()
	ctx := context.Background()
	if app != nil {
		ctx = app.Context()
	}
	// F-02：改走 merge 路径而非整包替换——前端 payload 只含它管理的字段子集，
	// merge 保留 Pricing 等 backend 独占字段与 per-adapter CostMultiplier，
	// 避免每次保存清空定价/tab 地址/倍率。
	var (
		normalized UserConfig
		err        error
	)
	if s.backendHost != nil {
		normalized, err = s.backendHost.SaveUserConfigPatch(ctx, cfg)
	} else if s.store != nil {
		normalized, err = s.store.MergeUserPatch(ctx, cfg)
	} else {
		return nil
	}
	if err != nil {
		return err
	}
	s.emitUserConfigChanged(normalized)
	return nil
}

func (s *ProxyService) emitUserConfigChanged(cfg UserConfig) {
	app := application.Get()
	if app == nil {
		return
	}
	app.Event.Emit("user-config:changed", cfg)
}

// resolveUserConfigPath 用于处理与 resolveUserConfigPath 相关的逻辑。
func resolveUserConfigPath() string {
	return appdata.ConfigFilePath()
}

// resolveLogsRootPath 用于处理与 resolveLogsRootPath 相关的逻辑。
func resolveLogsRootPath() string {
	return appdata.LogsRootPath()
}

// ResolveLogsRootPath 用于处理与 ResolveLogsRootPath 相关的逻辑。
func ResolveLogsRootPath() string {
	return resolveLogsRootPath()
}

// ResolveSettingsRootPath 用于处理与 ResolveSettingsRootPath 相关的逻辑。
func ResolveSettingsRootPath() string {
	return appdata.RootDir()
}
