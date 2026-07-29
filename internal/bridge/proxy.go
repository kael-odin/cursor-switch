package bridge

import (
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/certs"
	"cursor/internal/client"
	"cursor/internal/cursor"
	"cursor/internal/mitm"
	"runtime"
)

// Public DTOs remain in package main for Wails service compatibility.
// ProxyState 定义了当前模块中的 ProxyState 类型。
type ProxyState = client.ProxyState

// UserConfig 定义了当前模块中的 UserConfig 类型。
type UserConfig = client.UserConfig

// ModelAdapterConfig 定义模型测速使用的模型配置结构。
type ModelAdapterConfig = serverconfig.ModelAdapterConfig

// ModelAdapterTestResult 定义一次模型测速结果。
type ModelAdapterTestResult = client.ModelAdapterTestResult

// FetchedModel 表示从 provider 拉回的单个模型。
type FetchedModel = client.FetchedModel

// FetchedModelsPayload 是模型列表载荷。
type FetchedModelsPayload = client.FetchedModelsPayload

// ModelAdapterTestResultsPayload 定义测速结果事件载荷。
type ModelAdapterTestResultsPayload = client.ModelAdapterTestResultsPayload

// ProxyService 定义了当前模块中的 ProxyService 类型。
type ProxyService struct {
	// core 表示当前声明中的 core。
	core *client.ProxyService
}

// NewProxyService 用于处理与 NewProxyService 相关的逻辑。
func NewProxyService(proxy *mitm.ProxyServer, certManager *certs.Manager, caCertPEM []byte) *ProxyService {
	return &ProxyService{core: client.NewProxyService(proxy, certManager, caCertPEM)}
}

// StartProxy 用于处理与 StartProxy 相关的逻辑。
func (s *ProxyService) StartProxy() (ProxyState, error) {
	return s.core.StartProxy()
}

// StopProxy 用于处理与 StopProxy 相关的逻辑。
func (s *ProxyService) StopProxy() (ProxyState, error) {
	return s.core.StopProxy()
}

// GetState 用于处理与 GetState 相关的逻辑。
func (s *ProxyService) GetState() ProxyState {
	return s.core.GetState()
}

// ClearLastError 用于处理与 ClearLastError 相关的逻辑。
func (s *ProxyService) ClearLastError() ProxyState {
	return s.core.ClearLastError()
}

// SetBaseURL 用于处理与 SetBaseURL 相关的逻辑。
func (s *ProxyService) SetBaseURL(baseURL string) (ProxyState, error) {
	return s.core.SetBaseURL(baseURL)
}

// LoadUserConfig 用于处理与 LoadUserConfig 相关的逻辑。
func (s *ProxyService) LoadUserConfig() (UserConfig, error) {
	return s.core.LoadUserConfig()
}

// SaveUserConfig 用于处理与 SaveUserConfig 相关的逻辑。
func (s *ProxyService) SaveUserConfig(cfg UserConfig) error {
	return s.core.SaveUserConfig(cfg)
}

// TestModelAdapter 用于处理与 TestModelAdapter 相关的逻辑。
func (s *ProxyService) TestModelAdapter(adapter ModelAdapterConfig) (ModelAdapterTestResult, error) {
	return s.core.TestModelAdapter(adapter)
}

// GetModelAdapterTestResults 用于处理与 GetModelAdapterTestResults 相关的逻辑。
func (s *ProxyService) GetModelAdapterTestResults() []ModelAdapterTestResult {
	return s.core.GetModelAdapterTestResults()
}

// FetchProviderModels 调用 provider 的模型列表端点，供「一键获取模型列表」使用。
func (s *ProxyService) FetchProviderModels(adapter ModelAdapterConfig) (FetchedModelsPayload, error) {
	return s.core.FetchProviderModels(adapter)
}

// GetDeviceID 用于处理与 GetDeviceID 相关的逻辑。
func (s *ProxyService) GetDeviceID() (string, error) {
	return s.core.GetDeviceID()
}

// ConfigStore 返回底层配置存储，供 MetricsService 等共享读写 config.yaml。
func (s *ProxyService) ConfigStore() *serverconfig.Store {
	return s.core.ConfigStore()
}

// ApplyCursorSettings 用于处理与 ApplyCursorSettings 相关的逻辑。
func (s *ProxyService) ApplyCursorSettings() error {
	return s.core.ApplyCursorSettings()
}

// ClearCursorSettings 用于处理与 ClearCursorSettings 相关的逻辑。
func (s *ProxyService) ClearCursorSettings() error {
	return s.core.ClearCursorSettings()
}

// UninstallCACert 手动卸载本机 CA（供 GUI「卸载证书」按钮调用）。
func (s *ProxyService) UninstallCACert() error {
	return s.core.UninstallCACert()
}

// ShutdownForQuit 用于处理与 ShutdownForQuit 相关的逻辑。
func (s *ProxyService) ShutdownForQuit() {
	s.core.ShutdownForQuit()
}

// IsWindows 用于处理与 IsWindows 相关的逻辑。
func (s *ProxyService) IsWindows() bool {
	return runtime.GOOS == "windows"
}

// GetCursorAccountStatus 只读探测 Cursor 客户端是否登录了官方账号。
// 用于前端"Tab 补全依赖官方账号"的缺失告警：未登录时 Tab 补全/Git 消息等
// 依赖官方账号的能力不可用，前端明确告警而非静默失败。
func (s *ProxyService) GetCursorAccountStatus() (cursor.CursorAccountStatus, error) {
	status := cursor.ProbeCursorAccount()
	return status, nil
}
