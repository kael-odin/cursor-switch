package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"cursor/internal/appdata"
	"cursor/internal/backend/forwarder"
	"cursor/internal/backend/server"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/backend/server/upstream"
	"cursor/internal/logger"
	"cursor/internal/netproxy"
	legacyruntime "cursor/internal/runtime"
)

const healthPath = "/healthz"

// tab server 地址由 config.Routing.TabServerBaseURL 控制（H1）：
//   - 空：禁用第三方 tab server 重定向，tab 补全/git 消息流量透传官方 api2.cursor.sh（走用户自己 Cursor 账号）。
//   - 非空：流量导向该地址（如自建 cursor-tab-server，或历史默认的作者共享池 tab.leokun.cn）。

type Host struct {
	store      *serverconfig.Store
	listenAddr string
	configs    *serverconfig.Manager
	healthHTTP *http.Client

	runMu      sync.RWMutex
	httpServer *http.Server

	lastRunErr error

	mux http.Handler
}

func NewHost(store *serverconfig.Store) (*Host, error) {
	if store == nil {
		return nil, fmt.Errorf("backend config store is required")
	}
	configs, err := serverconfig.NewManager(context.Background(), store)
	if err != nil {
		return nil, err
	}
	cfg := configs.Current()
	host := &Host{
		store:      store,
		listenAddr: cfg.BackendListenAddr,
		configs:    configs,
		healthHTTP: newLoopbackHTTPClient(),
	}
	if err := host.rebuild(cfg); err != nil {
		return nil, err
	}
	return host, nil
}

func (host *Host) ConfigManager() *serverconfig.Manager {
	if host == nil {
		return nil
	}
	return host.configs
}

func (host *Host) LoadConfig(ctx context.Context) (serverconfig.Config, error) {
	if host == nil || host.configs == nil {
		return serverconfig.DefaultConfig(), nil
	}
	return host.configs.Load(ctx)
}

func (host *Host) SaveConfig(ctx context.Context, cfg serverconfig.Config) (serverconfig.Config, error) {
	if host == nil || host.configs == nil {
		return serverconfig.Config{}, fmt.Errorf("backend config manager is not initialized")
	}
	normalized, err := host.configs.Save(ctx, cfg)
	if err != nil {
		return serverconfig.Config{}, err
	}
	// F-35：读 httpServer 在 runMu 内，与 Start/Stop 串行（此前未持锁读造成数据竞争）。
	// rebuild 自带 runMu.Lock()，故此处只在锁内读状态、锁外决定是否 rebuild，避免嵌套。
	host.runMu.Lock()
	needsRebuild := host.httpServer == nil
	host.runMu.Unlock()
	if needsRebuild {
		if rebuildErr := host.rebuild(normalized); rebuildErr != nil {
			return serverconfig.Config{}, rebuildErr
		}
	}
	return normalized, nil
}

func (host *Host) ListenAddr() string {
	if host == nil {
		return ""
	}
	host.runMu.RLock()
	defer host.runMu.RUnlock()
	return host.listenAddr
}

func (host *Host) BaseURL() string {
	listenAddr := strings.TrimSpace(host.ListenAddr())
	if listenAddr == "" {
		return ""
	}
	return "http://" + listenAddr
}

func (host *Host) IsRunning() bool {
	if host == nil {
		return false
	}
	host.runMu.RLock()
	defer host.runMu.RUnlock()
	return host.httpServer != nil
}

func (host *Host) LastRunError() error {
	if host == nil {
		return nil
	}
	host.runMu.RLock()
	defer host.runMu.RUnlock()
	return host.lastRunErr
}

func (host *Host) Start() error {
	if host == nil {
		return fmt.Errorf("backend host is nil")
	}
	cfg := host.configs.Current()

	host.runMu.Lock()
	defer host.runMu.Unlock()
	if host.httpServer != nil {
		return fmt.Errorf("backend is already running")
	}
	if err := host.rebuildLocked(cfg); err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              host.listenAddr,
		Handler:           host.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	listener, err := net.Listen("tcp", host.listenAddr)
	if err != nil {
		host.lastRunErr = fmt.Errorf("监听内置后端 %s 失败: %w", host.listenAddr, err)
		return host.lastRunErr
	}
	host.listenAddr = listener.Addr().String()
	host.httpServer = httpServer
	host.lastRunErr = nil
	logger.Infof("内置后端监听成功 listen_addr=%s", host.listenAddr)

	go func(serverInstance *http.Server, serverListener net.Listener) {
		logger.Infof("内置后端开始提供服务 listen_addr=%s", serverListener.Addr().String())
		if err := serverInstance.Serve(serverListener); err != nil && err != http.ErrServerClosed {
			runErr := fmt.Errorf("内置后端在 %s 上异常退出: %w", serverListener.Addr().String(), err)
			host.runMu.Lock()
			if host.httpServer == serverInstance {
				host.httpServer = nil
			}
			host.lastRunErr = runErr
			host.runMu.Unlock()
			logger.Errorf("%v", runErr)
		}
	}(httpServer, listener)
	return nil
}

func (host *Host) Stop(ctx context.Context) error {
	if host == nil {
		return nil
	}
	host.runMu.Lock()
	serverInstance := host.httpServer
	host.httpServer = nil
	host.runMu.Unlock()
	if serverInstance == nil {
		return nil
	}
	err := serverInstance.Shutdown(ctx)
	return err
}

func (host *Host) HealthCheck(ctx context.Context) error {
	if host == nil {
		return fmt.Errorf("backend host is nil")
	}
	if runErr := host.LastRunError(); runErr != nil {
		return runErr
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, host.BaseURL()+healthPath, nil)
	if err != nil {
		return err
	}
	client := host.healthHTTP
	if client == nil {
		client = newLoopbackHTTPClient()
	}
	response, err := client.Do(request)
	if err != nil {
		inProcessErr := host.InProcessHealthCheck()
		if inProcessErr == nil {
			logger.Errorf("内置后端进程内健康检查成功，但 loopback 访问失败 base_url=%s err=%v", host.BaseURL(), err)
			return fmt.Errorf("内置后端进程内健康检查成功，但本机 loopback 访问失败: %w", err)
		}
		logger.Errorf("内置后端 loopback 与进程内健康检查均失败 loopback_err=%v in_process_err=%v", err, inProcessErr)
		if runErr := host.LastRunError(); runErr != nil {
			return runErr
		}
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("内置后端健康检查返回状态码 %d", response.StatusCode)
	}
	return nil
}

func (host *Host) InProcessHealthCheck() error {
	if host == nil {
		return fmt.Errorf("backend host is nil")
	}
	if host.mux == nil {
		return fmt.Errorf("backend handler is nil")
	}
	request := httptest.NewRequest(http.MethodGet, "http://inprocess"+healthPath, nil)
	recorder := httptest.NewRecorder()
	host.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		return fmt.Errorf("in-process health status %d", recorder.Code)
	}
	body := strings.TrimSpace(recorder.Body.String())
	if body != "ok" {
		return fmt.Errorf("in-process health body %q", body)
	}
	logger.Infof("内置后端进程内健康检查成功")
	return nil
}

func newLoopbackHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: (&net.Dialer{
				Timeout:   1 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:   false,
			MaxIdleConns:        1,
			MaxIdleConnsPerHost: 1,
			IdleConnTimeout:     30 * time.Second,
		},
	}
}

func (host *Host) rebuild(cfg serverconfig.Config) error {
	host.runMu.Lock()
	defer host.runMu.Unlock()
	return host.rebuildLocked(cfg)
}

func (host *Host) rebuildLocked(cfg serverconfig.Config) error {
	host.listenAddr = cfg.BackendListenAddr
	agentModule := forwarder.NewModule(appdata.HistoryRootPath(), host.configs)
	legacyBidiAppendProcedure := "/aiserver.v1.BidiService/BidiAppend"
	legacyRunSSEProcedure := "/agent.v1.AgentService/RunSSE"
	routeDeps := upstream.Dependencies{
		SystemSettingService: &serverSystemSettings{configs: host.configs},
		HTTPClient:           netproxy.NewHTTPClient(30 * time.Second),
	}

	host.mux = server.New(
		server.Use(
			server.Recover(),
			server.LoopbackAuth(),
			server.ServerContext(),
			server.PolicyMiddleware(host.configs),
			server.ErrorEncoder(),
		),
		server.GET(healthPath,
			server.Name("healthz"),
			server.HTTP(),
			server.Local(server.Health()),
		),
		server.POST(legacyBidiAppendProcedure,
			server.Name("bidi_append"),
			server.ConnectUnary(),
			server.Local(server.HTTPHandlerAction(agentModule.LocalBidiHandler)),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "bidi_append",
			})),
		),
		server.POST(legacyRunSSEProcedure,
			server.Name("run_sse"),
			server.ConnectStream(),
			server.Local(server.HTTPHandlerAction(agentModule.LocalRunSSE)),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "run_sse",
			})),
		),
		server.POST("/aiserver.v1.AiService/ServerTime",
			server.Name("server_time"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "server_time",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.ServerTimeResponse",
				MockBuilder:   upstream.ServerTimeMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "server_time",
			})),
		),
		server.POST("/aiserver.v1.AiService/GetServerConfig",
			server.Name("server_config"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "server_config",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetServerConfigResponse",
				MockBuilder:   upstream.ServerConfigMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "server_config",
			})),
		),
		server.POST("/aiserver.v1.AiService/AvailableModels",
			server.Name("available_models"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "available_models",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.AvailableModelsResponse",
				MockBuilder:   upstream.AvailableModelsMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "available_models",
			})),
		),
		server.POST("/aiserver.v1.AiService/GetDefaultModelNudgeData",
			server.Name("default_model_nudge"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "default_model_nudge",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetDefaultModelNudgeDataResponse",
				MockBuilder:   upstream.DefaultModelNudgeMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "default_model_nudge",
			})),
		),
		// GetDefaultModel 返回对话界面当前选中的模型。若命中 AiService/* catch-all 返回 404，
		// 对话界面会锁 auto 且无法选择 byok 自定义模型，故在此显式本地 mock，返回 byok 默认模型。
		server.POST("/aiserver.v1.AiService/GetDefaultModel",
			server.Name("get_default_model"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "get_default_model",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetDefaultModelResponse",
				MockBuilder:   upstream.GetDefaultModelMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "get_default_model",
			})),
		),
		// Statsig 走官方透传：真实账号的 statsig 决定 UI gate 与模型选择器行为。
		// 此前用本地 mock 会注入 free_user_model_picker/locked_picker 实验，导致对话界面锁 auto、
		// 且 marketplace 完整 Discover 界面（分类/搜索/browse）因缺少官方 gate 不渲染。
		// byok 模型路由不依赖 statsig（靠 AvailableModels/GetDefaultModel mock + adapter 配置），切透传安全。
		officialProcedure("/aiserver.v1.AnalyticsService/BootstrapStatsig", "bootstrap_statsig", server.ConnectUnary(), routeDeps),
		officialProcedure("/aiserver.v1.AnalyticsService/GetFirstWindowStatsigDecision", "first_window_statsig_decision", server.ConnectUnary(), routeDeps),
		officialProcedure("/oauth/token", "oauth_token", server.HTTP(), routeDeps),
		officialProcedure("/aiserver.v1.AuthService/GetEmail", "auth_service_get_email", server.ConnectUnary(), routeDeps),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/StreamCpp", "ai_stream_cpp", server.ConnectStream(), routeDeps, host.configs),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/StreamNextCursorPrediction", "ai_stream_next_cursor_prediction", server.ConnectStream(), routeDeps, host.configs),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/GetCppEditClassification", "ai_get_cpp_edit_classification", server.ConnectUnary(), routeDeps, host.configs),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/RefreshTabContext", "ai_refresh_tab_context", server.ConnectUnary(), routeDeps, host.configs),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/CppConfig", "ai_cpp_config", server.ConnectUnary(), routeDeps, host.configs),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/CppEditHistoryStatus", "ai_cpp_edit_history_status", server.ConnectUnary(), routeDeps, host.configs),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/CppAppend", "ai_cpp_append", server.ConnectUnary(), routeDeps, host.configs),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/CppEditHistoryAppend", "ai_cpp_edit_history_append", server.ConnectUnary(), routeDeps, host.configs),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/ReportAiCodeChangeMetrics", "ai_report_ai_code_change_metrics", server.ConnectUnary(), routeDeps, host.configs),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/WriteGitCommitMessage", "ai_write_git_commit_message", server.ConnectUnary(), routeDeps, host.configs),
		tabServerUpstreamProcedure("/aiserver.v1.AiService/WriteGitBranchName", "ai_write_git_branch_name", server.ConnectUnary(), routeDeps, host.configs),
		repositoryServiceProcedure(forwarder.RepositoryServiceFastRepoInitHandshakeV2Procedure, "repository_fast_repo_init_handshake_v2", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceFastRepoInitHandshakeProcedure, "repository_fast_repo_init_handshake", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceFastRepoSyncCompleteProcedure, "repository_fast_repo_sync_complete", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceSyncMerkleSubtreeV2Procedure, "repository_sync_merkle_subtree_v2", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceSyncMerkleSubtreeProcedure, "repository_sync_merkle_subtree", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceFastUpdateFileV2Procedure, "repository_fast_update_file_v2", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceFastUpdateFileProcedure, "repository_fast_update_file", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceEnsureIndexCreatedProcedure, "repository_ensure_index_created", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceGetCopyStatusProcedure, "repository_get_copy_status", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceGetUploadLimitsProcedure, "repository_get_upload_limits", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceGetNumFilesToSendProcedure, "repository_get_num_files_to_send", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceGetAvailableChunkingStrategiesProcedure, "repository_get_available_chunking_strategies", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceGetHighLevelFolderDescriptionProcedure, "repository_get_high_level_folder_description", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceRepositoryStatusProcedure, "repository_status", server.ConnectUnary(), agentModule, routeDeps),
		repositoryServiceProcedure(forwarder.RepositoryServiceBatchRepositoryStatusProcedure, "repository_batch_status", server.ConnectUnary(), agentModule, routeDeps),
		uploadServiceProcedure(forwarder.UploadServiceUploadDocumentationProcedure, "upload_documentation", server.ConnectUnary(), agentModule, routeDeps),
		uploadServiceProcedure(forwarder.UploadServiceGetDocProcedure, "upload_get_doc", server.ConnectUnary(), agentModule, routeDeps),
		uploadServiceProcedure(forwarder.UploadServiceGetPagesProcedure, "upload_get_pages", server.ConnectUnary(), agentModule, routeDeps),
		uploadServiceProcedure(forwarder.UploadServiceUploadedStatusProcedure, "upload_uploaded_status", server.ConnectUnary(), agentModule, routeDeps),
		server.Any("/aiserver.v1.AiService/*",
			server.Name("ai_service"),
			server.HTTP(),
			server.Local(server.HTTPHandlerAction(agentModule.AiHandler)),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "ai_service",
			})),
		),
		tabServerUpstreamProcedure("/aiserver.v1.CppService/AvailableModels", "cpp_available_models", server.ConnectUnary(), routeDeps, host.configs),
		tabServerUpstreamProcedure("/aiserver.v1.CppService/RecordCppFate", "cpp_record_cpp_fate", server.ConnectUnary(), routeDeps, host.configs),
		server.Any("/aiserver.v1.CppService/*",
			server.Name("cpp_service"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				http.NotFound(ctx.Writer, ctx.Request)
				return nil
			}),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "cpp_service",
			})),
		),
		tabServerUpstreamProcedure("/aiserver.v1.FileSyncService/FSSyncFile", "file_sync_sync_file", server.ConnectUnary(), routeDeps, host.configs),
		tabServerUpstreamProcedure("/aiserver.v1.FileSyncService/FSIsEnabledForUser", "file_sync_is_enabled_for_user", server.ConnectUnary(), routeDeps, host.configs),
		tabServerUpstreamProcedure("/aiserver.v1.FileSyncService/FSConfig", "file_sync_config", server.ConnectUnary(), routeDeps, host.configs),
		tabServerUpstreamProcedure("/aiserver.v1.FileSyncService/FSUploadFile", "file_sync_upload_file", server.ConnectUnary(), routeDeps, host.configs),
		server.Any("/aiserver.v1.FileSyncService/*",
			server.Name("file_sync"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				http.NotFound(ctx.Writer, ctx.Request)
				return nil
			}),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "file_sync",
			})),
		),
		server.POST("/aiserver.v1.DashboardService/GetTokenUsage",
			server.Name("dashboard_token_usage"),
			server.HTTP(),
			server.Local(server.HTTPHandlerAction(agentModule.AiHandler)),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "dashboard_token_usage",
			})),
		),
		server.POST("/aiserver.v1.DashboardService/GetGlassEarlyPreviewEnrollment",
			server.Name("dashboard_glass_early_preview_enrollment"),
			server.ConnectUnary(),
			server.Local(server.HTTPHandlerAction(agentModule.AiHandler)),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "dashboard_glass_early_preview_enrollment",
			})),
		),
		// 权益接口走本地 mock（无限制）：真实账号套餐会用 allowedModelIds 锁定模型选择器，
		// 导致对话界面锁 auto、byok 模型不可选。mock 成无限制即解锁。marketplace/auth 仍透传。
		server.POST("/aiserver.v1.DashboardService/GetCurrentPeriodUsage",
			server.Name("dashboard_current_period_usage"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_current_period_usage",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetCurrentPeriodUsageResponse",
				MockBuilder:   upstream.DashboardCurrentPeriodUsageMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "dashboard_current_period_usage",
			})),
		),
		officialProcedure("/aiserver.v1.DashboardService/GetTeams", "dashboard_get_teams", server.ConnectUnary(), routeDeps),
		officialProcedure("/aiserver.v1.DashboardService/GetManagedSkills", "dashboard_get_managed_skills", server.ConnectUnary(), routeDeps),
		officialProcedure("/aiserver.v1.DashboardService/GetEffectiveUserPlugins", "dashboard_get_effective_user_plugins", server.ConnectUnary(), routeDeps),
		officialProcedure("/aiserver.v1.DashboardService/ListMarketplaces", "dashboard_list_marketplaces", server.ConnectUnary(), routeDeps),
		officialProcedure("/aiserver.v1.DashboardService/ListMarketplacePlugins", "dashboard_list_marketplace_plugins", server.ConnectUnary(), routeDeps),
		officialProcedure("/aiserver.v1.DashboardService/RegisterMarketplaceAndPlugins", "dashboard_register_marketplace_and_plugins", server.ConnectUnary(), routeDeps),
		officialProcedure("/aiserver.v1.DashboardService/GetMe", "dashboard_get_me", server.ConnectUnary(), routeDeps),
		officialProcedure("/aiserver.v1.DashboardService/GetUserPrivacyMode", "dashboard_user_privacy_mode", server.ConnectUnary(), routeDeps),
		server.POST("/aiserver.v1.DashboardService/GetPlanInfo",
			server.Name("dashboard_plan_info"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_plan_info",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetPlanInfoResponse",
				MockBuilder:   upstream.DashboardPlanInfoMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "dashboard_plan_info",
			})),
		),
		server.POST("/aiserver.v1.DashboardService/GetUsageLimitStatusAndActiveGrants",
			server.Name("dashboard_usage_limit_status"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_usage_limit_status",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.GetUsageLimitStatusAndActiveGrantsResponse",
				MockBuilder:   upstream.DashboardUsageLimitStatusAndActiveGrantsMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "dashboard_usage_limit_status",
			})),
		),
		server.POST("/aiserver.v1.DashboardService/IsOnNewPricing",
			server.Name("dashboard_is_on_new_pricing"),
			server.ConnectUnary(),
			server.Local(upstream.MockProtoAction(routeDeps, upstream.CompatRouteConfig{
				Name:          "dashboard_is_on_new_pricing",
				StatusCode:    http.StatusOK,
				MockProtoType: "aiserver.v1.IsOnNewPricingResponse",
				MockBuilder:   upstream.DashboardIsOnNewPricingMockBuilder,
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "dashboard_is_on_new_pricing",
			})),
		),
		// Dashboard 控制面兜底：除上面显式声明的本地数据路由（GetTokenUsage / GlassEarlyPreview）外，
		// 其余全部官方透传，覆盖 InstallUserPlugin / GetAvailableMcpServers / GetTeamCommands /
		// ClientAction / 卸载 / 更新 / Hooks / Skills 等 customize 接口，无需逐个补 mock。
		officialAnyProcedure("/aiserver.v1.DashboardService/*", "dashboard", server.HTTP(), routeDeps),
		server.Any("/aiserver.v1.NetworkService/*",
			server.Name("network_service"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				http.NotFound(ctx.Writer, ctx.Request)
				return nil
			}),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "network_service",
			})),
		),
		server.Any("/aiserver.v1.InAppAdService/*",
			server.Name("in_app_ad"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				http.NotFound(ctx.Writer, ctx.Request)
				return nil
			}),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "in_app_ad",
			})),
		),
		// Stripe 订阅状态走本地 mock（Pro + active + 付费有效），与 GetPlanInfo/GetCurrentPeriodUsage
		// 保持一致的无限制套餐。若走透传，真实账号套餐会与本地 mock 的无限制用量矛盾，
		// 触发 Cursor 疯狂轮询 stripe_profile/GetTeams 并重新锁定模型选择器（锁 auto）。
		// 身份接口 GetMe/GetEmail 仍透传真实账号（不携带套餐字段，不矛盾、不闪动）。
		server.Any("/auth/full_stripe_profile",
			server.Name("auth_full_stripe_profile"),
			server.HTTP(),
			server.Local(upstream.MockJSONAction(routeDeps, upstream.CompatRouteConfig{
				Name:       "auth_full_stripe_profile",
				StatusCode: http.StatusOK,
				JSONBody: map[string]any{
					"membershipType":          "pro",
					"subscriptionStatus":      "active",
					"lastPaymentFailed":       false,
					"pendingCancellationDate": "",
					"daysRemainingOnTrial":    0,
					"paymentId":               "local_ultra",
				},
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "auth_full_stripe_profile",
			})),
		),
		// stripe_profile 返回原始 paymentId 字符串（非 JSON 对象），用直接写入。
		server.Any("/auth/stripe_profile",
			server.Name("auth_stripe_profile"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				body, _ := json.Marshal("local_ultra")
				ctx.Writer.Header().Set("content-type", "application/json")
				ctx.Writer.WriteHeader(http.StatusOK)
				_, _ = ctx.Writer.Write(body)
				return nil
			}),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "auth_stripe_profile",
			})),
		),
		server.Any("/auth/has_valid_payment_method",
			server.Name("auth_has_valid_payment_method"),
			server.HTTP(),
			server.Local(upstream.MockJSONAction(routeDeps, upstream.CompatRouteConfig{
				Name:       "auth_has_valid_payment_method",
				StatusCode: http.StatusOK,
				JSONBody: map[string]any{
					"hasValidPaymentMethod": true,
				},
			})),
			server.Upstream(upstream.DirectAction(routeDeps, upstream.CompatRouteConfig{
				Name: "auth_has_valid_payment_method",
			})),
		),
		officialAnyProcedure("/auth/poll", "auth_poll", server.HTTP(), routeDeps),
		officialAnyProcedure("/auth/logout", "auth_logout", server.HTTP(), routeDeps),
		// /auth/* 兜底：登录回调、profile、支付、poll、logout 等全部官方透传，
		// 使 byok 开启时也能正常登录 / 退出 / 重新登录真实 Cursor 账号。
		officialAnyProcedure("/auth/*", "auth_proxy", server.HTTP(), routeDeps),
		// 未被 byok 接管的官方 Cursor 控制面服务：统一官方透传，恢复真实登录态。
		// 覆盖 customize 相关的 plugins / marketplace / background composer / chat 等服务，
		// 无需逐个补 mock，未来 Cursor 新增接口也自动透传。
		officialAnyProcedure("/aiserver.v1.PluginsService/*", "official_plugins_service", server.HTTP(), routeDeps),
		officialAnyProcedure("/aiserver.v1.MarketplaceService/*", "official_marketplace_service", server.HTTP(), routeDeps),
		officialAnyProcedure("/aiserver.v1.ChatService/*", "official_chat_service", server.HTTP(), routeDeps),
		officialAnyProcedure("/aiserver.v1.HealthService/*", "official_health_service", server.HTTP(), routeDeps),
		officialAnyProcedure("/aiserver.v1.MetricsService/*", "official_metrics_service", server.HTTP(), routeDeps),
		officialAnyProcedure("/aiserver.v1.BackgroundComposerService/*", "official_background_composer_service", server.HTTP(), routeDeps),
	)

	return nil
}

func directUpstreamProcedure(pattern string, name string, protocol server.RouteOption, deps upstream.Dependencies) server.Option {
	direct := upstream.DirectAction(deps, upstream.CompatRouteConfig{Name: name})
	action := func(ctx *server.Context) error {
		if ctx != nil && ctx.UpstreamURL == nil && ctx.Request != nil && ctx.Request.URL != nil {
			targetURL := *ctx.Request.URL
			targetURL.Scheme = "https"
			targetURL.Host = "api2.cursor.sh:443"
			ctx.UpstreamURL = &targetURL
		}
		return direct(ctx)
	}
	return server.POST(pattern,
		server.Name(name),
		protocol,
		server.Local(action),
		server.Upstream(action),
	)
}

// officialProcedure 注册一个官方控制面透传路由：无论 local / upstream 模式，
// 都以 CredentialOriginalCursor 策略回源到 Cursor 官方后端，恢复用户真实登录态。
// 用于 marketplace / customize / 账号 / 登录 等接口，让 byok 不再 mock 官方身份。
func officialProcedure(pattern string, name string, protocol server.RouteOption, deps upstream.Dependencies) server.Option {
	action := upstream.DirectAction(deps, upstream.CompatRouteConfig{
		Name:       name,
		Credential: upstream.CredentialOriginalCursor,
	})
	return server.POST(pattern,
		server.Name(name),
		protocol,
		server.Local(action),
		server.Upstream(action),
	)
}

// officialAnyProcedure 与 officialProcedure 相同，但匹配所有 HTTP 方法（含 GET / catch-all）。
func officialAnyProcedure(pattern string, name string, protocol server.RouteOption, deps upstream.Dependencies) server.Option {
	action := upstream.DirectAction(deps, upstream.CompatRouteConfig{
		Name:       name,
		Credential: upstream.CredentialOriginalCursor,
	})
	return server.Any(pattern,
		server.Name(name),
		protocol,
		server.Local(action),
		server.Upstream(action),
	)
}

func repositoryServiceProcedure(pattern string, name string, protocol server.RouteOption, module *forwarder.Module, deps upstream.Dependencies) server.Option {
	localAction := server.HTTPHandlerAction(module.RepositoryServiceHandler)
	upstreamAction := upstream.DirectAction(deps, upstream.CompatRouteConfig{Name: name})
	return server.POST(pattern,
		server.Name(name),
		protocol,
		server.Local(localAction),
		server.Upstream(upstreamAction),
	)
}

func uploadServiceProcedure(pattern string, name string, protocol server.RouteOption, module *forwarder.Module, deps upstream.Dependencies) server.Option {
	localAction := server.HTTPHandlerAction(module.UploadServiceHandler)
	upstreamAction := upstream.DirectAction(deps, upstream.CompatRouteConfig{Name: name})
	return server.POST(pattern,
		server.Name(name),
		protocol,
		server.Local(localAction),
		server.Upstream(upstreamAction),
	)
}

func tabServerUpstreamProcedure(pattern string, name string, protocol server.RouteOption, deps upstream.Dependencies, configs *serverconfig.Manager) server.Option {
	direct := upstream.DirectAction(deps, upstream.CompatRouteConfig{Name: name})
	action := func(ctx *server.Context) error {
		// H1: tab server 地址由 config 控制。空 = 禁用第三方重定向，回退官方 api2.cursor.sh 透传。
		baseURLStr := ""
		if configs != nil {
			baseURLStr = strings.TrimSpace(configs.Current().Routing.TabServerBaseURL)
		}
		if baseURLStr != "" && ctx != nil && ctx.Request != nil && ctx.Request.URL != nil {
			if target := resolveTabUpstreamURL(ctx.Request.URL, baseURLStr); target != nil {
				ctx.UpstreamURL = target
			}
		}
		return direct(ctx)
	}
	return server.POST(pattern,
		server.Name(name),
		protocol,
		server.Local(action),
		server.Upstream(action),
	)
}

// resolveTabUpstreamURL 把请求 URL 的 scheme/host 替换为 tab server 地址，保留 path/query。
// baseURLStr 为空或非法时返回 nil（透传官方兜底）。
func resolveTabUpstreamURL(reqURL *url.URL, baseURLStr string) *url.URL {
	if strings.TrimSpace(baseURLStr) == "" {
		return nil
	}
	baseURL, err := url.Parse(baseURLStr)
	if err != nil || baseURL == nil || baseURL.Host == "" {
		return nil
	}
	targetURL := *reqURL
	targetURL.Scheme = baseURL.Scheme
	targetURL.Host = baseURL.Host
	return &targetURL
}

type serverSystemSettings struct {
	configs *serverconfig.Manager
}

func (settings *serverSystemSettings) ResolveModelAdapters(ctx context.Context) ([]legacyruntime.ModelAdapterConfig, error) {
	snapshot, err := settings.configs.LegacyRuntimeSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snapshot.ModelAdapters, nil
}
