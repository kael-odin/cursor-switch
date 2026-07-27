package upstream

import (
	"context"
	"encoding/json"
	"html"
	"strings"
	"time"

	legacyruntime "cursor/internal/runtime"
)

const (
	availableModelsDisableUnusedHours = 2400000
	availableModelsUpgradeHours       = 2

	modelRuntimeThinkingEffortParameterID = "thinking_effort"

	// localUltraPaymentID 仅作为 Statsig bootstrap mock 的稳定匿名用户 ID（非账号身份）。
	localUltraPaymentID = "local_ultra"

	// localStatsigUserEmail 是 Statsig bootstrap mock 使用的匿名占位 email。
	// byok 不再注入真实/假账号身份，Statsig 仅需一个稳定匿名 ID 即可返回本地 gate。
	localStatsigUserEmail = "local@cursor-switch.local"

	bootstrapStatsigGlassModeAvailableGate           = "glass_mode_available"
	bootstrapStatsigGlassOpenAgentInWindowGate       = "glass.enable_open_agent_in_window"
	bootstrapStatsigOpenAgentsTitlebarGate           = "glass_open_agents_titlebar_button"
	bootstrapStatsigOpenAgentWindowTopGate           = "open_agent_window_top"
	bootstrapStatsigOpenAgentWindowBottomGate        = "open_agent_window_bottom_convo"
	bootstrapStatsigNALAgentRetriesGate              = "nal_agent_retries"
	bootstrapStatsigNALFreshRetryIDsGate             = "nal_fresh_retry_ids"
	bootstrapStatsigUseModelParametersGate           = "use_model_parameters"
	bootstrapStatsigUseReactModelPickerGate          = "use_react_model_picker"
	bootstrapStatsigIDECmdEnterSubmitGate            = "ide_cmd_enter_submit"
	bootstrapStatsigContextVisualizerGate            = "context_visualizer"
	bootstrapStatsigWysiwygMarkdownGate              = "wysiwyg_markdown"
	bootstrapStatsigWysiwygMarkdownDefaultGate       = "wysiwyg_markdown_default"
	bootstrapStatsigSubagentSupportInterrupt         = "subagent_support_interrupt"
	bootstrapStatsigExplicitSubagentModels           = "explicit_subagent_models"
	bootstrapStatsigMcpDirectClientToolFetch         = "mcp_direct_client_tool_fetch"
	bootstrapStatsigGlassCustomThemeSupport          = "glass_custom_theme_support"
	bootstrapStatsigGlassAutomationsUI               = "glass_automations_ui"
	bootstrapStatsigTerminalUI2                      = "terminal_ui_2"
	bootstrapStatsigDisableTerminalOutputUIStreaming = "disable_terminal_output_ui_streaming"
	bootstrapStatsigBrowserCanvas                    = "browser_canvas"
	bootstrapStatsigEnableMultitaskMode              = "enable_multitask_mode"
	bootstrapStatsigDecomposeAlwaysLocalExtHostGate  = "decompose_always_local_ext_host"
	bootstrapStatsigCursorExtensionsIsolationV2Gate  = "cursor_extensions_isolation_v2"
	bootstrapStatsigCursorAgentWorkerExtension       = "enable_cursor_agent_worker_extension"
	bootstrapStatsigExperimentName                   = "free_user_model_picker"
	bootstrapStatsigVariantParam                     = "variant"
	bootstrapStatsigVariantControl                   = "control"
	bootstrapStatsigVariantLockedPicker              = "locked_picker"
	bootstrapStatsigVariantGrayedModels              = "grayed_models"
	bootstrapStatsigProductTipsConfigName            = "product_tips_config"
	bootstrapStatsigIdleExtensionHostKiller          = "idle_extension_host_killer_config"
	bootstrapStatsigIdleMinutesToKill                = "idleMinutesToKillExtensionHost"
	bootstrapStatsigFreeMemoryPercentageToKill       = "freeMemoryPercentageToKillExtensionHost"
	bootstrapStatsigHTTP2PingConfig                  = "http2_ping_config"
	bootstrapStatsigHTTP1KeepaliveConfig             = "http1_keepalive_config"
	bootstrapStatsigHTTP2AgentPoolConfig             = "http2_agent_connection_pool_config"
	bootstrapStatsigCanvasPromptTextConfig           = "canvas_prompt_text_config"
	bootstrapStatsigEditorBugbotConfig               = "editor_bugbot_config"
	bootstrapStatsigExtensionMonitorControl          = "extension_monitor_control"
	bootstrapStatsigExtensionSignatureBypass         = "extension_signature_verification_bypass_list"
	bootstrapStatsigGCTraceControl                   = "gc_trace_control"
	bootstrapStatsigInlineDiffPerformance            = "inline_diff_performance_config"
	bootstrapStatsigLeakedDisposablesTracker         = "leaked_disposables_tracker"
	bootstrapStatsigMcpIPCTimeouts                   = "mcp_ipc_timeouts"
	bootstrapStatsigMcpWakeProbeConfig               = "mcp_wake_probe_config"
	bootstrapStatsigNALStallDetectorTimeout          = "nal_stall_detector_timeout_config"
	bootstrapStatsigSimulatedThinkingErrorTimeout    = "simulated_thinking_error_timeout"
	bootstrapStatsigPlaywrightLogConfigs             = "playwright_log_configs"
	bootstrapStatsigRetryInterceptorParams           = "retry_interceptor_params_config"
	bootstrapStatsigSandboxNetworkAllowlist          = "sandbox_default_network_allowlist"
	bootstrapStatsigUpdatePromptConfig               = "update_prompt_config"
	bootstrapStatsigLocalDefaultRule                 = "local_default"
)

type statsigSecondaryExposure struct {
	Gate           string `json:"gate,omitempty"`
	GateValue      string `json:"gateValue,omitempty"`
	GateValueSnake string `json:"gate_value,omitempty"`
	RuleID         string `json:"ruleID,omitempty"`
	RuleIDSnake    string `json:"rule_id,omitempty"`
}

type statsigDynamicConfigTemplate struct {
	Name                               string                     `json:"name"`
	Value                              map[string]any             `json:"value"`
	RuleID                             string                     `json:"rule_id"`
	RuleIDCamel                        string                     `json:"ruleID"`
	GroupName                          string                     `json:"group_name"`
	GroupNameCamel                     string                     `json:"groupName"`
	SecondaryExposures                 []statsigSecondaryExposure `json:"secondary_exposures"`
	SecondaryExposuresCamel            []statsigSecondaryExposure `json:"secondaryExposures"`
	UndelegatedSecondaryExposures      []statsigSecondaryExposure `json:"undelegated_secondary_exposures"`
	UndelegatedSecondaryExposuresCamel []statsigSecondaryExposure `json:"undelegatedSecondaryExposures"`
	IsDeviceBased                      bool                       `json:"is_device_based"`
	IsDeviceBasedCamel                 bool                       `json:"isDeviceBased"`
	IsExperimentActive                 bool                       `json:"is_experiment_active"`
	IsExperimentActiveCamel            bool                       `json:"isExperimentActive"`
	IsUserInExperiment                 bool                       `json:"is_user_in_experiment"`
	IsUserInExperimentCamel            bool                       `json:"isUserInExperiment"`
}

type statsigBootstrapTemplate struct {
	FeatureGates   map[string]map[string]any               `json:"feature_gates"`
	DynamicConfigs map[string]statsigDynamicConfigTemplate `json:"dynamic_configs"`
	LayerConfigs   map[string]map[string]any               `json:"layer_configs"`
	User           map[string]any                          `json:"user"`
	HasUpdates     bool                                    `json:"has_updates"`
	HashUsed       string                                  `json:"hash_used"`
	SDKParams      map[string]any                          `json:"sdkParams"`
	Time           int64                                   `json:"time"`
}

var bootstrapStatsigTemplate = statsigBootstrapTemplate{
	FeatureGates: map[string]map[string]any{
		bootstrapStatsigGlassModeAvailableGate:           buildEnabledStatsigGate(bootstrapStatsigGlassModeAvailableGate),
		bootstrapStatsigGlassOpenAgentInWindowGate:       buildEnabledStatsigGate(bootstrapStatsigGlassOpenAgentInWindowGate),
		bootstrapStatsigOpenAgentsTitlebarGate:           buildEnabledStatsigGate(bootstrapStatsigOpenAgentsTitlebarGate),
		bootstrapStatsigOpenAgentWindowTopGate:           buildEnabledStatsigGate(bootstrapStatsigOpenAgentWindowTopGate),
		bootstrapStatsigOpenAgentWindowBottomGate:        buildEnabledStatsigGate(bootstrapStatsigOpenAgentWindowBottomGate),
		bootstrapStatsigNALAgentRetriesGate:              buildEnabledStatsigGate(bootstrapStatsigNALAgentRetriesGate),
		bootstrapStatsigNALFreshRetryIDsGate:             buildEnabledStatsigGate(bootstrapStatsigNALFreshRetryIDsGate),
		bootstrapStatsigUseModelParametersGate:           buildEnabledStatsigGate(bootstrapStatsigUseModelParametersGate),
		bootstrapStatsigUseReactModelPickerGate:          buildEnabledStatsigGate(bootstrapStatsigUseReactModelPickerGate),
		bootstrapStatsigIDECmdEnterSubmitGate:            buildEnabledStatsigGate(bootstrapStatsigIDECmdEnterSubmitGate),
		bootstrapStatsigContextVisualizerGate:            buildEnabledStatsigGate(bootstrapStatsigContextVisualizerGate),
		bootstrapStatsigWysiwygMarkdownGate:              buildEnabledStatsigGate(bootstrapStatsigWysiwygMarkdownGate),
		bootstrapStatsigWysiwygMarkdownDefaultGate:       buildEnabledStatsigGate(bootstrapStatsigWysiwygMarkdownDefaultGate),
		bootstrapStatsigSubagentSupportInterrupt:         buildEnabledStatsigGate(bootstrapStatsigSubagentSupportInterrupt),
		bootstrapStatsigExplicitSubagentModels:           buildEnabledStatsigGate(bootstrapStatsigExplicitSubagentModels),
		bootstrapStatsigMcpDirectClientToolFetch:         buildEnabledStatsigGate(bootstrapStatsigMcpDirectClientToolFetch),
		bootstrapStatsigGlassCustomThemeSupport:          buildEnabledStatsigGate(bootstrapStatsigGlassCustomThemeSupport),
		bootstrapStatsigGlassAutomationsUI:               buildEnabledStatsigGate(bootstrapStatsigGlassAutomationsUI),
		bootstrapStatsigTerminalUI2:                      buildEnabledStatsigGate(bootstrapStatsigTerminalUI2),
		bootstrapStatsigDisableTerminalOutputUIStreaming: buildEnabledStatsigGate(bootstrapStatsigDisableTerminalOutputUIStreaming),
		bootstrapStatsigBrowserCanvas:                    buildEnabledStatsigGate(bootstrapStatsigBrowserCanvas),
		bootstrapStatsigEnableMultitaskMode:              buildEnabledStatsigGate(bootstrapStatsigEnableMultitaskMode),
		bootstrapStatsigDecomposeAlwaysLocalExtHostGate:  buildDisabledStatsigGate(bootstrapStatsigDecomposeAlwaysLocalExtHostGate),
		bootstrapStatsigCursorExtensionsIsolationV2Gate:  buildDisabledStatsigGate(bootstrapStatsigCursorExtensionsIsolationV2Gate),
		bootstrapStatsigCursorAgentWorkerExtension:       buildEnabledStatsigGate(bootstrapStatsigCursorAgentWorkerExtension),
	},
	DynamicConfigs: map[string]statsigDynamicConfigTemplate{
		bootstrapStatsigExperimentName: buildStatsigDynamicConfig(
			bootstrapStatsigExperimentName,
			map[string]any{bootstrapStatsigVariantParam: bootstrapStatsigVariantControl},
			bootstrapStatsigVariantControl,
		),
		bootstrapStatsigProductTipsConfigName: buildStatsigDynamicConfig(
			bootstrapStatsigProductTipsConfigName,
			map[string]any{
				"tips": []map[string]any{},
				"config": map[string]any{
					"intervalMs":       8000,
					"minClientVersion": "",
				},
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigIdleExtensionHostKiller: buildStatsigDynamicConfig(
			bootstrapStatsigIdleExtensionHostKiller,
			map[string]any{
				bootstrapStatsigIdleMinutesToKill:          0,
				bootstrapStatsigFreeMemoryPercentageToKill: 0,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigCanvasPromptTextConfig: buildStatsigDynamicConfig(
			bootstrapStatsigCanvasPromptTextConfig,
			buildCanvasPromptTextConfigValue(),
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigEditorBugbotConfig: buildStatsigDynamicConfig(
			bootstrapStatsigEditorBugbotConfig,
			map[string]any{
				"model":              "claude-4-5-sonnet-20250929",
				"iterations":         0,
				"agentic_iterations": 1,
				"agentic_model":      "claude-4.5-haiku",
				"context_lines":      10,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigExtensionMonitorControl: buildStatsigDynamicConfig(
			bootstrapStatsigExtensionMonitorControl,
			map[string]any{
				"local_enabled":              false,
				"backend_reporting_enabled":  false,
				"subsample_polling_rate_sec": 0,
				"sample_polling_rate_min":    0,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigExtensionSignatureBypass: buildStatsigDynamicConfig(
			bootstrapStatsigExtensionSignatureBypass,
			map[string]any{
				"extensionIds": []string{
					"nromanov.dotrush",
					"ms-python.python",
					"typescriptteam.native-preview",
					"typespec.typespec-vscode",
					"ms-toolsai.jupyter",
					"k3ndr1ckfu.tcl-language-support-for-vscode",
					"amiq.dvt",
				},
				"remoteVerificationMinVersion": "2.25.0",
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigGCTraceControl: buildStatsigDynamicConfig(
			bootstrapStatsigGCTraceControl,
			map[string]any{
				"enabled":            false,
				"drain_interval_sec": 120,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigInlineDiffPerformance: buildStatsigDynamicConfig(
			bootstrapStatsigInlineDiffPerformance,
			map[string]any{
				"maxDecorations": 100,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigLeakedDisposablesTracker: buildStatsigDynamicConfig(
			bootstrapStatsigLeakedDisposablesTracker,
			map[string]any{
				"enabled":          false,
				"reportIntervalMs": 60000,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigMcpIPCTimeouts: buildStatsigDynamicConfig(
			bootstrapStatsigMcpIPCTimeouts,
			map[string]any{
				"metadata_timeout_ms":           10000,
				"lifecycle_timeout_ms":          10000,
				"dashboard_timeout_ms":          10000,
				"recovery_per_retry_timeout_ms": 10000,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigMcpWakeProbeConfig: buildStatsigDynamicConfig(
			bootstrapStatsigMcpWakeProbeConfig,
			map[string]any{
				"probeOnFocus":              true,
				"probeOnBrowserOnline":      true,
				"probeOnElapsedTimeGap":     true,
				"elapsedTimeGapThresholdMs": 300000,
				"focusProbeDebounceMs":      60000,
				"onlineProbeDebounceMs":     5000,
				"resumeProbeDebounceMs":     5000,
				"startupGraceMs":            15000,
				"minProbeIntervalMs":        30000,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigNALStallDetectorTimeout: buildStatsigDynamicConfig(
			bootstrapStatsigNALStallDetectorTimeout,
			map[string]any{
				"advisoryTimeoutMs": 20000,
				"failTimeoutMs":     30000,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigSimulatedThinkingErrorTimeout: buildStatsigDynamicConfig(
			bootstrapStatsigSimulatedThinkingErrorTimeout,
			map[string]any{
				"timeout_ms": 120000,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigPlaywrightLogConfigs: buildStatsigDynamicConfig(
			bootstrapStatsigPlaywrightLogConfigs,
			map[string]any{
				"logSizeThreshold": 25000,
				"logPreviewLines":  25,
				"logPreviewChars":  25000,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigRetryInterceptorParams: buildStatsigDynamicConfig(
			bootstrapStatsigRetryInterceptorParams,
			map[string]any{},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigUpdatePromptConfig: buildStatsigDynamicConfig(
			bootstrapStatsigUpdatePromptConfig,
			map[string]any{
				"min_hours_between_prompts": 48,
				"max_prompts_per_version":   3,
				"max_prompts_per_day":       1,
				"snooze_duration_hours":     72,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigHTTP2PingConfig: buildStatsigDynamicConfig(
			bootstrapStatsigHTTP2PingConfig,
			map[string]any{
				"enabled":                 []string{},
				"pingIdleConnection":      nil,
				"pingIntervalMs":          nil,
				"pingTimeoutMs":           nil,
				"idleConnectionTimeoutMs": nil,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigHTTP1KeepaliveConfig: buildStatsigDynamicConfig(
			bootstrapStatsigHTTP1KeepaliveConfig,
			map[string]any{
				"keepAliveInitialDelayMs": nil,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigHTTP2AgentPoolConfig: buildStatsigDynamicConfig(
			bootstrapStatsigHTTP2AgentPoolConfig,
			map[string]any{
				"poolSize": 4,
			},
			bootstrapStatsigLocalDefaultRule,
		),
	},
	LayerConfigs: map[string]map[string]any{},
	User: map[string]any{
		"userID": localUltraPaymentID,
		"email":  localStatsigUserEmail,
		"customIDs": map[string]string{
			"localUserID": localUltraPaymentID,
		},
	},
	HasUpdates: true,
	HashUsed:   "none",
	SDKParams: map[string]any{
		"stableID":                  localUltraPaymentID,
		"disableDiagnosticsLogging": true,
	},
}

func buildStatsigDynamicConfig(name string, value map[string]any, ruleID string) statsigDynamicConfigTemplate {
	name = strings.TrimSpace(name)
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		ruleID = bootstrapStatsigLocalDefaultRule
	}
	exposures := []statsigSecondaryExposure{}
	return statsigDynamicConfigTemplate{
		Name:                               name,
		Value:                              value,
		RuleID:                             ruleID,
		RuleIDCamel:                        ruleID,
		GroupName:                          ruleID,
		GroupNameCamel:                     ruleID,
		SecondaryExposures:                 exposures,
		SecondaryExposuresCamel:            exposures,
		UndelegatedSecondaryExposures:      exposures,
		UndelegatedSecondaryExposuresCamel: exposures,
		IsDeviceBased:                      false,
		IsDeviceBasedCamel:                 false,
		IsExperimentActive:                 false,
		IsExperimentActiveCamel:            false,
		IsUserInExperiment:                 false,
		IsUserInExperimentCamel:            false,
	}
}

func buildCanvasPromptTextConfigValue() map[string]any {
	return map[string]any{
		"skillDescription": "A Cursor Canvas is a live React app that the user can open beside the chat. You MUST use a canvas when the agent produces a standalone analytical artifact \u2014 quantitative analyses, billing investigations, security audits, architecture reviews, data-heavy content, timelines, charts, tables, interactive explorations, repeatable tools, or any response that benefits from visual layout. Especially prefer a canvas when presenting results from MCP tools (Datadog, Databricks, Linear, Sentry, Slack, etc.) where the data is the deliverable \u2014 render it in a rich canvas rather than dumping it into a markdown table or code block. If you catch yourself about to write a markdown table, stop and use a canvas instead. You MUST also read this skill whenever you create, edit, or debug any .canvas.tsx file.",
		"errorFixPromptTemplate": strings.Join([]string{
			"The canvas at `{canvasPath}` has the following error:",
			"",
			`"""`,
			"{errorMessage}",
			`"""`,
			"",
			"Check if the canvas SDK has changed since this canvas was created.",
			"Update the canvas to use the latest SDK components according to the supplied documentation in the canvas skill.",
		}, "\n"),
		"welcomePageEnabled":     true,
		"marketplaceCategoryKey": "canvas-featured",
		"marketplaceMaxCards":    4,
	}
}

func buildEnabledStatsigGate(name string) map[string]any {
	return buildStatsigGate(name, true, "local_enabled")
}

func buildDisabledStatsigGate(name string) map[string]any {
	return buildStatsigGate(name, false, "local_disabled")
}

func buildStatsigGate(name string, value bool, ruleID string) map[string]any {
	return map[string]any{
		"name":                            name,
		"value":                           value,
		"rule_id":                         ruleID,
		"ruleID":                          ruleID,
		"group_name":                      ruleID,
		"groupName":                       ruleID,
		"secondary_exposures":             []statsigSecondaryExposure{},
		"secondaryExposures":              []statsigSecondaryExposure{},
		"undelegated_secondary_exposures": []statsigSecondaryExposure{},
		"undelegatedSecondaryExposures":   []statsigSecondaryExposure{},
		"is_device_based":                 false,
		"isDeviceBased":                   false,
		"id_type":                         "userID",
		"idType":                          "userID",
	}
}

func buildServerTimePayload(*RequestContext) (map[string]any, error) {
	now := float64(time.Now().UnixMilli())
	return map[string]any{
		"receiveTimestamp":  now,
		"transmitTimestamp": now,
	}, nil
}

func buildServerConfigPayload(*RequestContext) (map[string]any, error) {
	return map[string]any{
		"configVersion": "local_cli_sandbox_defaults_disabled_v2",
		// "http2Config":              "HTTP2_CONFIG_FORCE_ALL_DISABLED",
		"cliSandboxDefaultEnabled": true,
	}, nil
}

func buildAvailableModelsPayload(reqCtx *RequestContext) (map[string]any, error) {
	adapters, err := loadConfiguredModelAdapters(reqCtx)
	if err != nil {
		return nil, err
	}
	modelRefs := collectModelAdapterRefs(adapters)
	defaultModel := ""
	if len(modelRefs) > 0 {
		defaultModel = modelRefs[0]
	}
	return map[string]any{
		"backgroundComposerModelConfig": map[string]any{
			"bestOfNDefaultModels": append([]string(nil), modelRefs...),
			"defaultModel":         defaultModel,
			"fallbackModels":       append([]string(nil), modelRefs...),
		},
		"cmdKModelConfig": map[string]any{
			"defaultModel":   defaultModel,
			"fallbackModels": append([]string(nil), modelRefs...),
		},
		"composerModelConfig": map[string]any{
			"bestOfNDefaultModels": append([]string(nil), modelRefs...),
			"defaultModel":         defaultModel,
			"fallbackModels":       append([]string(nil), modelRefs...),
		},
		"deepSearchModelConfig": map[string]any{
			"defaultModel": defaultModel,
		},
		"disableUnusedModelsAfterNHours": availableModelsDisableUnusedHours,
		"models":                         buildAvailableModelEntries(adapters),
		"planExecutionModelConfig": map[string]any{
			"defaultModel":   defaultModel,
			"fallbackModels": append([]string(nil), modelRefs...),
		},
		"quickAgentModelConfig": map[string]any{
			"defaultModel": defaultModel,
		},
		"specModelConfig": map[string]any{
			"defaultModel": defaultModel,
		},
		"useModelParameters":                true,
		"upgradeUnchangedModelsAfterNHours": availableModelsUpgradeHours,
	}, nil
}

func buildDefaultModelNudgeDataPayload(reqCtx *RequestContext) (map[string]any, error) {
	adapters, err := loadConfiguredModelAdapters(reqCtx)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"modelsWithNoDefaultSwitch": collectModelAdapterRefs(adapters),
		"nudgeDate":                 "0",
	}, nil
}

// buildGetDefaultModelPayload 返回对话界面当前选中的默认模型。
// Cursor 用 GetDefaultModel 确定下拉里高亮/选中的模型；若该接口 404，
// 对话界面会回退到 auto 且无法选择 byok 自定义模型。这里返回 byok 第一个模型，
// 与 AvailableModels 的 defaultModel 保持一致，使 byok 模型成为默认选中项。
func buildGetDefaultModelPayload(reqCtx *RequestContext) (map[string]any, error) {
	adapters, err := loadConfiguredModelAdapters(reqCtx)
	if err != nil {
		return nil, err
	}
	defaultModel := ""
	if refs := collectModelAdapterRefs(adapters); len(refs) > 0 {
		defaultModel = refs[0]
	}
	return map[string]any{
		"model":              defaultModel,
		"thinkingModel":      defaultModel,
		"maxMode":            false,
		"nextDefaultSetDate": "",
	}, nil
}

func buildBootstrapStatsigPayload(reqCtx *RequestContext) (map[string]any, error) {
	generatedAtMs := uint64(time.Now().UnixMilli())
	authID := resolveBootstrapStatsigAuthID(reqCtx)
	configJSON, err := buildBootstrapStatsigConfigJSON(int64(generatedAtMs), authID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"config":        string(configJSON),
		"generatedAtMs": generatedAtMs,
	}, nil
}

func buildFirstWindowStatsigDecisionPayload(*RequestContext) (map[string]any, error) {
	return map[string]any{
		"variant": bootstrapStatsigVariantControl,
		"reason":  bootstrapStatsigLocalDefaultRule,
	}, nil
}

// 权益接口本地 mock：这几个接口若走官方透传，真实账号套餐会用 allowedModelIds/
// allowedModelTags 把模型选择器锁定（锁 auto），使 byok 自定义模型不可选。
// 因此 mock 成「无限制」：allowedModelIds/allowedModelTags 为空 = 不限制可用模型。
// 这与 byok 定位一致——用用户自己的 key/模型，不依赖 Cursor 套餐权益。
// marketplace/auth/login 仍走官方透传，不受影响。

func buildDashboardUsageLimitStatusAndActiveGrantsPayload(*RequestContext) (map[string]any, error) {
	return map[string]any{
		"usageLimitPolicyStatus": map[string]any{
			"isInSlowPool":           false,
			"features":               map[string]string{},
			"canConfigureSpendLimit": true,
			"hasPendingRequest":      false,
			"allowedModelIds":        []string{}, // 空 = 不限制可选模型（解锁 key）
			"allowedModelTags":       []string{}, // 空 = 不限制可选模型标签
		},
		"activeGrants": []map[string]any{},
	}, nil
}

func buildDashboardPlanInfoPayload(*RequestContext) (map[string]any, error) {
	return map[string]any{
		"planInfo": map[string]any{
			"planName":            "Cursor Pro",
			"includedAmountCents": 0,
			"price":               "",
			"billingCycleEnd":     time.Now().Add(10 * 365 * 24 * time.Hour).UnixMilli(),
		},
	}, nil
}

func buildDashboardCurrentPeriodUsagePayload(*RequestContext) (map[string]any, error) {
	billingCycleStart := time.Now().Add(-30 * 24 * time.Hour).UnixMilli()
	billingCycleEnd := time.Now().Add(10 * 365 * 24 * time.Hour).UnixMilli()
	return map[string]any{
		"autoModelSelectedDisplayMessage":  "",
		"billingCycleEnd":                  billingCycleEnd,
		"billingCycleStart":                billingCycleStart,
		"displayMessage":                   "",
		"displayThreshold":                 99999999,
		"enabled":                          false,
		"namedModelSelectedDisplayMessage": "",
		"planUsage": map[string]any{
			"apiPercentUsed":   0,
			"apiSpend":         0,
			"autoPercentUsed":  0,
			"autoSpend":        0,
			"includedSpend":    0,
			"limit":            0,
			"remaining":        0,
			"remainingBonus":   false,
			"totalPercentUsed": 0,
			"totalSpend":       0,
		},
		"spendLimitUsage": map[string]any{
			"limitType": "user",
		},
	}, nil
}

// buildDashboardIsOnNewPricingPayload：定价状态走本地 mock。真实账号若返回旧定价（isOnNewPricing=false），
// 会触发旧的模型限制策略，重新锁定模型选择器。mock 成新定价 + 自动溢出，与无限制模型策略一致。
func buildDashboardIsOnNewPricingPayload(*RequestContext) (map[string]any, error) {
	return map[string]any{
		"isOnNewPricing":                  true,
		"isOptedOut":                      false,
		"hasAutoSpillover":                true,
		"hasTieredSelfServeTeamSpillover": false,
	}, nil
}

// 说明：stripe 订阅状态走本地静态 mock（host.go 的 MockJSONAction + JSONBody），
// 与 GetPlanInfo/GetCurrentPeriodUsage 保持一致的 Pro + active + 无限制。
// GetMe 走官方透传（真实账号身份），不在此 mock，避免左下角账号名闪动。
// GetMeResponse 不含套餐字段，透传真实身份不与本地 mock 的订阅状态矛盾。

func buildDashboardGetMePayload(*RequestContext) (map[string]any, error) {
	// 不伪造具体身份：email/姓名留空，Cursor 会用真实登录态显示。
	// 此接口仅用于满足「已登录」结构，真正身份由 /auth/* 透传提供。
	return map[string]any{
		"authId":            localUltraPaymentID,
		"userId":            1,
		"email":             "",
		"firstName":         "",
		"lastName":          "",
		"createdAt":         time.Now().UTC().Format(time.RFC3339),
		"isEnterpriseUser":  false,
		"teamName":          "",
		"emailDomainType":   "personal",
		"country":           "US",
		"profilePictureUrl": "",
	}, nil
}

// 说明：其余 Dashboard 账号/marketplace 接口仍走官方透传（真实 Cursor 账号）。

func loadConfiguredModelAdapters(reqCtx *RequestContext) ([]legacyruntime.ModelAdapterConfig, error) {
	if reqCtx == nil || reqCtx.Deps == nil || reqCtx.Deps.SystemSettingService == nil {
		return []legacyruntime.ModelAdapterConfig{}, nil
	}
	ctx := context.Background()
	if reqCtx.Request != nil {
		ctx = reqCtx.Request.Context()
	}
	return reqCtx.Deps.SystemSettingService.ResolveModelAdapters(ctx)
}

func buildAvailableModelEntries(adapters []legacyruntime.ModelAdapterConfig) []map[string]any {
	if len(adapters) == 0 {
		return []map[string]any{}
	}
	output := make([]map[string]any, 0, len(adapters))
	for _, adapter := range adapters {
		// F-08：disabled adapter 不进 UI 模型目录，与 collectModelAdapterRefs / resolver 同口径。
		if !adapter.Enabled {
			continue
		}
		channelID := strings.TrimSpace(adapter.ID)
		displayName := strings.TrimSpace(adapter.DisplayName)
		modelID := strings.TrimSpace(adapter.ModelID)
		tooltipData := strings.TrimSpace(adapter.TooltipData)
		if channelID == "" || modelID == "" {
			continue
		}
		modelDisplayName := displayName
		if modelDisplayName == "" {
			modelDisplayName = modelID
		}
		defaultThinkingEffort := defaultThinkingEffortForAdapter(adapter)
		output = append(output, map[string]any{
			"clientDisplayName":                  displayName,
			"defaultOn":                          true,
			"degradationStatus":                  "DEGRADATION_STATUS_UNSPECIFIED",
			"inputboxShortModelName":             displayName,
			"isRecommendedForBackgroundComposer": false,
			"name":                               channelID,
			"namedModelSectionIndex":             1,
			"parameterDefinitions":               buildThinkingEffortParameterDefinitions(adapter.Type),
			"serverModelName":                    channelID,
			"supportsAgent":                      true,
			"supportsImages":                     true,
			"supportsMaxMode":                    false,
			"supportsNonMaxMode":                 true,
			"supportsPlanMode":                   true,
			"supportsSandboxing":                 true,
			"supportsThinking":                   true,
			"tagline":                            thinkingEffortDisplayName(defaultThinkingEffort),
			"tooltipData": map[string]any{
				"markdownContent": tooltipData,
			},
			"tooltipDataForMaxMode": map[string]any{
				"markdownContent": tooltipData,
			},
			"variants": buildThinkingEffortVariants(adapter.Type, channelID, modelDisplayName, tooltipData, defaultThinkingEffort),
		})
	}
	return output
}

func buildThinkingEffortParameterDefinitions(adapterType string) []map[string]any {
	values := thinkingEffortValuesForAdapter(adapterType)
	options := make([]map[string]any, 0, len(values))
	for _, value := range values {
		options = append(options, map[string]any{
			"displayName":        thinkingEffortDisplayName(value),
			"increasesModelCost": value == "xhigh" || value == "max",
			"value":              value,
		})
	}
	return []map[string]any{{
		"id":                  modelRuntimeThinkingEffortParameterID,
		"isCycleableByHotkey": true,
		"markdownTooltip":     "Controls the model thinking intensity for this run.",
		"name":                "Thinking intensity",
		"parameterType": map[string]any{
			"enumParameter": map[string]any{
				"values": options,
			},
		},
	}}
}

func buildThinkingEffortVariants(adapterType string, channelID string, modelDisplayName string, tooltipData string, defaultThinkingEffort string) []map[string]any {
	values := orderThinkingEffortValues(thinkingEffortValuesForAdapter(adapterType), defaultThinkingEffort)
	channelID = strings.TrimSpace(channelID)
	modelDisplayName = strings.TrimSpace(modelDisplayName)
	variants := make([]map[string]any, 0, len(values))
	for _, value := range values {
		effortDisplayName := thinkingEffortDisplayName(value)
		variantDisplayName := buildThinkingEffortVariantDisplayName(modelDisplayName, value)
		variant := map[string]any{
			"displayName":              variantDisplayName,
			"displayNameOutsidePicker": variantDisplayName,
			"isDefaultNonMaxConfig":    value == defaultThinkingEffort,
			"isMaxMode":                false,
			"parameterValues":          []map[string]any{{"id": modelRuntimeThinkingEffortParameterID, "value": value}},
		}
		if normalizeAvailableModelThinkingEffort(value, true, "") != "disabled" {
			variant["tagline"] = effortDisplayName
		}
		if channelID != "" {
			variant["variantStringRepresentation"] = channelID + ":" + value
		}
		if strings.TrimSpace(tooltipData) != "" {
			variant["tooltipData"] = map[string]any{"markdownContent": tooltipData}
		}
		variants = append(variants, variant)
	}
	return variants
}

func buildThinkingEffortVariantDisplayName(modelDisplayName string, effortValue string) string {
	modelDisplayName = html.EscapeString(strings.TrimSpace(modelDisplayName))
	if normalizeAvailableModelThinkingEffort(effortValue, true, "") == "disabled" {
		return modelDisplayName
	}
	effortDisplayName := thinkingEffortDisplayName(effortValue)
	effortDisplayName = html.EscapeString(strings.TrimSpace(effortDisplayName))
	if modelDisplayName == "" {
		return `<span class="ui-model-picker__item-tagline" style="color: var(--cursor-text-secondary); white-space: nowrap;">:icon-brain: ` + effortDisplayName + `</span>`
	}
	return modelDisplayName + ` <span class="ui-model-picker__item-tagline" style="color: var(--cursor-text-secondary); white-space: nowrap;">:icon-brain: ` + effortDisplayName + `</span>`
}

func thinkingEffortValuesForAdapter(adapterType string) []string {
	values := []string{"disabled", "low", "medium", "high", "xhigh"}
	if adapterType := strings.ToLower(strings.TrimSpace(adapterType)); adapterType == "openai" || adapterType == "anthropic" {
		values = append(values, "max")
	}
	return values
}

func orderThinkingEffortValues(values []string, defaultValue string) []string {
	defaultValue = strings.ToLower(strings.TrimSpace(defaultValue))
	output := make([]string, 0, len(values))
	for _, value := range values {
		if strings.EqualFold(value, defaultValue) {
			output = append(output, value)
			break
		}
	}
	for _, value := range values {
		if !strings.EqualFold(value, defaultValue) {
			output = append(output, value)
		}
	}
	return output
}

func defaultThinkingEffortForAdapter(adapter legacyruntime.ModelAdapterConfig) string {
	if strings.EqualFold(strings.TrimSpace(adapter.Type), "anthropic") {
		return normalizeAvailableModelThinkingEffort(adapter.AnthropicThinkingEffort, true, "xhigh")
	}
	return normalizeAvailableModelThinkingEffort(adapter.ReasoningEffort, true, "medium")
}

func normalizeAvailableModelThinkingEffort(raw string, allowMax bool, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "disabled", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(raw))
	case "disable", "off", "none", "false", "no", "0":
		return "disabled"
	case "max":
		if allowMax {
			return "max"
		}
		return fallback
	default:
		return fallback
	}
}

func thinkingEffortDisplayName(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "disabled":
		return "Disabled"
	case "low":
		return "Low"
	case "medium":
		return "Medium"
	case "high":
		return "High"
	case "xhigh":
		return "XHigh"
	case "max":
		return "Max"
	default:
		return strings.TrimSpace(value)
	}
}

// collectModelAdapterRefs 返回已启用 adapter 的渠道 ID 列表，用于 UI 模型目录与默认选择。
//
// F-08：必须过滤 Enabled==false 的 adapter——否则 disabled adapter 仍会出现在
// AvailableModels / GetDefaultModel 的模型列表与 defaultModel 字段里，UI 可选中
// 一个运行时 resolver 拒绝的模型，或把第一项 disabled adapter 当默认。此处与
// config/resolver.go 的 SelectChannelsForModel 保持同一过滤口径（enabled 才进候选链）。
func collectModelAdapterRefs(adapters []legacyruntime.ModelAdapterConfig) []string {
	output := make([]string, 0, len(adapters))
	for _, adapter := range adapters {
		if !adapter.Enabled {
			continue
		}
		channelID := strings.TrimSpace(adapter.ID)
		if channelID == "" {
			continue
		}
		output = append(output, channelID)
	}
	return output
}

func resolveBootstrapStatsigAuthID(reqCtx *RequestContext) string {
	// byok 不再有假账号；中间件也已剥离 Authorization。Statsig bootstrap 只需稳定匿名 ID。
	return localUltraPaymentID
}

// 说明：authIDFromBearer / authIDFromJWT 已移除——它们曾从假账号 JWT 解析 sub 作为 Statsig ID。
// 现在中间件已剥离 Authorization，Statsig 仅用稳定匿名 ID。

func buildBootstrapStatsigConfigJSON(nowMs int64, authID string) ([]byte, error) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		authID = localUltraPaymentID
	}
	template := bootstrapStatsigTemplate
	template.Time = nowMs
	template.User = map[string]any{
		"userID": authID,
		"email":  localStatsigUserEmail,
		"customIDs": map[string]string{
			"localUserID": authID,
		},
	}

	// This template mirrors the Statsig initialize/bootstrap response shape that
	// the bundled client reads for experiments. hash_used stays "none" so the
	// experiment can be looked up by its plain name without spec hashing.
	//
	// Cursor currently branches on free_user_model_picker.variant. Known values
	// are "control", "locked_picker", and "grayed_models". Keep this template
	// centralized and update it first if the bundled Statsig bootstrap shape changes.
	return json.Marshal(template)
}
