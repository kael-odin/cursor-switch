package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"

	"cursor/internal/modelchannel"
)

const (
	DefaultBackendListenAddr                = "127.0.0.1:18090"
	DefaultProxyListenAddr                  = "127.0.0.1:18080"
	DefaultFrontendBaseURL                  = "http://127.0.0.1"
	DefaultRoutingMode                      = "local"
	DefaultProviderStreamIdleTimeoutSeconds = 240
	MinProviderStreamIdleTimeoutSeconds     = 30
)

type ModelAdapterConfig struct {
	ID                          string `json:"id,omitempty" yaml:"-"`
	DisplayName                 string `json:"displayName" yaml:"displayName"`
	Type                        string `json:"type" yaml:"type"`
	// ProviderLabel 是该适配器在使用统计里展示的 provider 标签（如 deepseek/qwen/glm）。
	// 空则回退到 Type（openai/anthropic）。type 是协议选择器（wire 协议），label 是品牌标签，
	// 二者独立——接 deepseek 走 openai 协议时 type=openai、providerLabel=deepseek，
	// 使用统计的 by-provider 表按 label 归类而非全归到 openai。
	ProviderLabel               string `json:"providerLabel,omitempty" yaml:"providerLabel,omitempty"`
	// ImageModelID 是该适配器用于图像生成的模型标识（如 gpt-image-2）。
	// 空则回退 ModelID。GenerateImage 工具调用时用此模型打 {baseURL}/v1/images/generations。
	// 与 ModelID（chat 模型）独立——同一 adapter 既能 chat（ModelID）又能生图（ImageModelID）。
	ImageModelID                string `json:"imageModelID,omitempty" yaml:"imageModelID,omitempty"`
	// Role 标识该 adapter 用途：chat（仅聊天，默认）、image（仅生图）、both（既能 chat 又能生图）。
	// Role==image 的 adapter 可独立配置（不依赖任何 chat adapter 的 ModelID 命中）——
	// resolveImageChannel 找不到挂了 ImageModelID 的 chat adapter 时，按 Role==image/both 兜底取它。
	// 为兼容旧配置，Role==image 且 ModelID 为空但 ImageModelID 非空时，normalize 把 ModelID 兜底成 ImageModelID，
	// 绕过 ModelID 必填校验；否则 Role 默认 chat，ModelID 必填（现状不变）。
	Role                        string `json:"role,omitempty" yaml:"role,omitempty"`
	BaseURL                     string `json:"baseURL" yaml:"baseURL"`
	APIKey                      string `json:"apiKey" yaml:"apiKey"`
	TooltipData                 string `json:"tooltipData" yaml:"tooltipData"`
	ModelID                     string `json:"modelID" yaml:"modelID"`
	ReasoningEffort             string `json:"reasoningEffort" yaml:"reasoningEffort"`
	OpenAIEndpoint              string `json:"openAIEndpoint" yaml:"openAIEndpoint"`
	OpenAIExtraParamsEnabled    bool   `json:"openAIExtraParamsEnabled" yaml:"openAIExtraParamsEnabled"`
	OpenAIExtraParamsJSON       string `json:"openAIExtraParamsJSON" yaml:"openAIExtraParamsJSON"`
	CustomHeadersEnabled        bool   `json:"customHeadersEnabled" yaml:"customHeadersEnabled"`
	CustomHeadersJSON           string `json:"customHeadersJSON" yaml:"customHeadersJSON"`
	AnthropicExtraParamsEnabled bool   `json:"anthropicExtraParamsEnabled" yaml:"anthropicExtraParamsEnabled"`
	AnthropicExtraParamsJSON    string `json:"anthropicExtraParamsJSON" yaml:"anthropicExtraParamsJSON"`
	ContextWindowTokens         int    `json:"contextWindowTokens" yaml:"contextWindowTokens"`
	MaxCompletionTokens         int    `json:"maxCompletionTokens" yaml:"maxCompletionTokens"`
	AnthropicMaxTokens          int    `json:"anthropicMaxTokens" yaml:"anthropicMaxTokens"`
	AnthropicThinkingEffort     string `json:"anthropicThinkingEffort,omitempty" yaml:"anthropicThinkingEffort,omitempty"`
	ThinkingBudgetTokens        int    `json:"thinkingBudgetTokens" yaml:"thinkingBudgetTokens"`
	// CostMultiplier 是该适配器的成本倍率覆盖（字符串，如 "1.5"）。空则用全局默认倍率。
	CostMultiplier string `json:"costMultiplier,omitempty" yaml:"costMultiplier,omitempty"`
	// Priority 是 B2 failover 候选链排序优先级：数字小的优先（默认 0）。
	// 同 modelID 的多个 enabled adapter 按 Priority 升序组成候选链，主候选失败后按序尝试备选。
	Priority int `json:"priority,omitempty" yaml:"priority,omitempty"`
	// Enabled 控制该 adapter 是否进入候选链（默认 true）。false 的 adapter 不参与路由但保留配置。
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	// Weight 预留给 WeightedRoundRobin 策略（本期 B2 只做 Failover，先收字段）。
	Weight int `json:"weight,omitempty" yaml:"weight,omitempty"`
}

type RoutingConfig struct {
	Mode string `json:"mode" yaml:"mode"`
	// TabServerBaseURL 控制 tab 补全/git 消息流量的上游地址（H1）。
	// 空 = 禁用第三方 tab server 重定向，回退到官方 api2.cursor.sh 透传（走用户自己的 Cursor 账号）。
	// 非空 = 把 StreamCpp/CppConfig/WriteGitCommitMessage 等流量导向该地址。
	// 历史默认值 "https://tab.leokun.cn" 是上游作者的共享池；用户可填自建 cursor-tab-server 地址或留空。
	TabServerBaseURL string `json:"tabServerBaseURL" yaml:"tabServerBaseURL"`
	// PerNamespace 是按路由名（namespace）的模式覆盖，审计第二部分「优先级 2」能力损失优化。
	// 全局 Mode 是粗粒度开关（要么全 byok 本地，要么全直连 cursor）；PerNamespace 让单个路由面
	// 独立选择 local（byok 本地重建）/ upstream（透传到用户本人 cursor 账号），不改变其它面。
	// key = route name（见 host.go 的 server.Name(...)，如 run_sse / bidi_append /
	// repository_status / cpp_service 等）；value = "local" | "upstream"。
	// 未列出的路由跟随全局 Mode。仅对 Local/Upstream 可切的路由有意义——officialProcedure
	// 把 Local 与 Upstream 设为同一 DirectAction，覆盖对其无影响（恒走透传）。
	// 例如：路由模式保持 local（byok），单独把 codebase 索引透传到 cursor 云——
	// perNamespace: { repository_status: upstream }。
	PerNamespace map[string]string `json:"perNamespace,omitempty" yaml:"perNamespace,omitempty"`
}

type HomeMetricsConfig struct {
	IncludeCacheWriteInHitRate bool `json:"includeCacheWriteInHitRate" yaml:"includeCacheWriteInHitRate"`
}

// WebToolsConfig 控制 agent 交互桥的两个外网工具：WebSearch 与 WebFetch。
//
// 审计「行为偏离-3」：WebSearch 原硬编码 DuckDuckGo HTML 抓取（易被封、质量差），
// WebFetch 的 SSRF 防护硬性拒绝所有非公网 IP，企业内网 Wiki/Confluence 被一刀切。
// 此 struct 把两者改为可配置（用户 BYOK 搜索 key + 内网 host 白名单），缺配置时
// 回退既有安全/降级行为——不破坏默认安全基线。
type WebToolsConfig struct {
	// WebSearchProvider 选择 WebSearch 上游："" / "duckduckgo"（默认，免 key 降级质量）、
	// "bing" / "serper" / "tavily"（需对应 APIKey，BYOK）。空与非认可值都回退 duckduckgo。
	WebSearchProvider string `json:"webSearchProvider,omitempty" yaml:"webSearchProvider,omitempty"`
	// WebSearchAPIKey 是 provider 的 API key（Bing/Serper/Tavily 必填）。
	// duckduckgo 不用 key。明文存配置文件——与 ModelAdapterConfig.apiKey 同口径，本机存储。
	WebSearchAPIKey string `json:"webSearchAPIKey,omitempty" yaml:"webSearchAPIKey,omitempty"`
	// WebFetchHostAllowlist 是 WebFetch 允许放行的 host 白名单（精确域名/IP，小写）。
	// 命中白名单的 host 绕过 safehttp 的私网/loopback 拒绝（企业内网 Wiki/Confluence 场景）。
	// 默认空 = 保持现 SSRF 硬拒绝行为（最安全）。仅在用户显式放行时叠加放行。
	WebFetchHostAllowlist []string `json:"webFetchHostAllowlist,omitempty" yaml:"webFetchHostAllowlist,omitempty"`
}

type Config struct {
	Log                       bool                 `json:"log" yaml:"log"`
	ProviderStreamIdleTimeout int                  `json:"providerStreamIdleTimeout" yaml:"providerStreamIdleTimeout"`
	BackendListenAddr         string               `json:"backendListenAddr" yaml:"backendListenAddr"`
	ProxyListenAddr           string               `json:"proxyListenAddr" yaml:"proxyListenAddr"`
	ModelAdapters             []ModelAdapterConfig `json:"modelAdapters" yaml:"modelAdapters"`
	Routing                   RoutingConfig        `json:"routing" yaml:"routing"`
	HomeMetrics               HomeMetricsConfig    `json:"homeMetrics" yaml:"homeMetrics"`
	WebTools                  WebToolsConfig       `json:"webTools" yaml:"webTools"`
	Pricing                   PricingConfig        `json:"pricing" yaml:"pricing"`
	LastAgentModelHash        string               `json:"lastAgentModelHash" yaml:"lastAgentModelHash"`
}

func DefaultConfig() Config {
	return Config{
		Log:                       false,
		ProviderStreamIdleTimeout: DefaultProviderStreamIdleTimeoutSeconds,
		BackendListenAddr:         DefaultBackendListenAddr,
		ProxyListenAddr:           DefaultProxyListenAddr,
		ModelAdapters:             []ModelAdapterConfig{},
		Routing: RoutingConfig{
			Mode: DefaultRoutingMode,
		},
	}
}

func NormalizeConfig(input Config) (Config, error) {
	output := DefaultConfig()
	output.Log = input.Log
	output.ProviderStreamIdleTimeout = normalizeProviderStreamIdleTimeout(input.ProviderStreamIdleTimeout)
	backendListenAddr, err := normalizeListenAddr(input.BackendListenAddr, DefaultBackendListenAddr, "backendListenAddr")
	if err != nil {
		return Config{}, err
	}
	proxyListenAddr, err := normalizeListenAddr(input.ProxyListenAddr, DefaultProxyListenAddr, "proxyListenAddr")
	if err != nil {
		return Config{}, err
	}
	output.BackendListenAddr = backendListenAddr
	output.ProxyListenAddr = proxyListenAddr
	output.HomeMetrics.IncludeCacheWriteInHitRate = input.HomeMetrics.IncludeCacheWriteInHitRate
	output.Pricing = normalizePricingConfig(input.Pricing)
	output.LastAgentModelHash = strings.TrimSpace(input.LastAgentModelHash)
	output.Routing.Mode = normalizeRoutingMode(input.Routing.Mode)
	if output.Routing.Mode == "" {
		output.Routing.Mode = DefaultRoutingMode
	}
	output.Routing.TabServerBaseURL, err = normalizeTabServerBaseURL(input.Routing.TabServerBaseURL)
	if err != nil {
		return Config{}, err
	}
	output.Routing.PerNamespace = normalizePerNamespace(input.Routing.PerNamespace)
	output.WebTools = normalizeWebToolsConfig(input.WebTools)
	adapters, err := NormalizeModelAdapterConfigs(input.ModelAdapters)
	if err != nil {
		return Config{}, err
	}
	output.ModelAdapters = adapters
	return output, nil
}

func NormalizeModelAdapterConfigs(input []ModelAdapterConfig) ([]ModelAdapterConfig, error) {
	if len(input) == 0 {
		return []ModelAdapterConfig{}, nil
	}

	normalized := make([]ModelAdapterConfig, 0, len(input))
	seenChannelIDs := make(map[string]struct{}, len(input))
	for _, item := range input {
		baseURL, err := modelchannel.NormalizeBaseURL(item.BaseURL)
		if err != nil {
			return nil, err
		}
		nextType := normalizeModelAdapterType(item.Type)
		next := ModelAdapterConfig{
			DisplayName:          strings.TrimSpace(item.DisplayName),
			Type:                 nextType,
			ProviderLabel:        strings.TrimSpace(item.ProviderLabel),
			ImageModelID:         strings.TrimSpace(item.ImageModelID),
			Role:                 normalizeAdapterRole(item.Role),
			BaseURL:              baseURL,
			APIKey:               strings.TrimSpace(item.APIKey),
			TooltipData:          strings.TrimSpace(item.TooltipData),
			ModelID:              strings.TrimSpace(item.ModelID),
			ReasoningEffort:      normalizeReasoningEffort(item.ReasoningEffort),
			OpenAIEndpoint:       modelchannel.NormalizeOpenAIEndpoint(item.Type, item.OpenAIEndpoint),
			ContextWindowTokens:  normalizeMaxCompletionTokens(item.ContextWindowTokens),
			MaxCompletionTokens:  normalizeMaxCompletionTokens(item.MaxCompletionTokens),
			AnthropicMaxTokens:   normalizeMaxCompletionTokens(item.AnthropicMaxTokens),
			ThinkingBudgetTokens: normalizeMaxCompletionTokens(item.ThinkingBudgetTokens),
		}
		if next.Type == "openai" {
			next.OpenAIExtraParamsEnabled = item.OpenAIExtraParamsEnabled
			next.OpenAIExtraParamsJSON = strings.TrimSpace(item.OpenAIExtraParamsJSON)
		} else if next.Type == "anthropic" {
			next.AnthropicThinkingEffort = normalizeAnthropicThinkingEffort(item.AnthropicThinkingEffort)
			next.AnthropicExtraParamsEnabled = item.AnthropicExtraParamsEnabled
			next.AnthropicExtraParamsJSON = strings.TrimSpace(item.AnthropicExtraParamsJSON)
		}
		next.CustomHeadersEnabled = item.CustomHeadersEnabled
		next.CustomHeadersJSON = strings.TrimSpace(item.CustomHeadersJSON)
		next.CostMultiplier = strings.TrimSpace(item.CostMultiplier)
		// B2 failover 候选链字段：Priority 排序、Enabled 开关、Weight 预留。
		next.Priority = item.Priority
		if item.Enabled == nil {
			enabled := true
			next.Enabled = &enabled
		} else {
			next.Enabled = item.Enabled
		}
		next.Weight = item.Weight
		// 纯 image adapter（Role==image 且 ModelID 空）允许用 ImageModelID 兜底 ModelID，
		// 绕过 ModelID 必填——这类 adapter 只服务生图，不参与 chat 路由。
		if next.Role == "image" && strings.TrimSpace(next.ModelID) == "" && strings.TrimSpace(next.ImageModelID) != "" {
			next.ModelID = strings.TrimSpace(next.ImageModelID)
		}
		switch {
		case next.DisplayName == "":
			return nil, errors.New("模型适配器 displayName 不能为空")
		case next.Type == "":
			return nil, errors.New("模型适配器 type 仅支持 openai 或 anthropic")
		case next.APIKey == "":
			return nil, errors.New("模型适配器 apiKey 不能为空")
		case next.TooltipData == "":
			return nil, errors.New("模型适配器 tooltipData 不能为空")
		case next.Role == "image" && strings.TrimSpace(next.ImageModelID) == "":
			return nil, errors.New("role=image 的模型适配器 imageModelID 不能为空")
		case next.ModelID == "":
			return nil, errors.New("模型适配器 modelID 不能为空")
		case next.Type == "openai" && next.ReasoningEffort == "":
			return nil, errors.New("模型适配器 reasoningEffort 仅支持 low、medium、high、xhigh、max")
		case next.Type == "openai" && next.OpenAIEndpoint == "":
			return nil, errors.New("模型适配器 openAIEndpoint 仅支持 /v1/responses、/v1/chat/completions 或 /custom（自定义路径）")
		case next.Type == "openai" && next.OpenAIExtraParamsEnabled:
			if err := validateJSONMap(next.OpenAIExtraParamsJSON, "openAIExtraParamsJSON"); err != nil {
				return nil, err
			}
		case next.CustomHeadersEnabled:
			if err := validateHeadersJSON(next.CustomHeadersJSON); err != nil {
				return nil, err
			}
		case next.Type == "anthropic" && next.AnthropicExtraParamsEnabled:
			if err := validateJSONMap(next.AnthropicExtraParamsJSON, "anthropicExtraParamsJSON"); err != nil {
				return nil, err
			}
		case next.Type == "anthropic" && next.AnthropicThinkingEffort == "":
			return nil, errors.New("模型适配器 anthropicThinkingEffort 仅支持 low、medium、high、xhigh、max")
		}
		next.ID = modelchannel.BuildChannelID(next.BaseURL, next.ModelID, next.APIKey, next.DisplayName, next.OpenAIEndpoint)
		if _, exists := seenChannelIDs[next.ID]; exists {
			return nil, errors.New("模型适配器渠道不能重复，请检查 url、modelID、apiKey、displayName、endpoint 组合")
		}
		seenChannelIDs[next.ID] = struct{}{}
		normalized = append(normalized, next)
	}
	return normalized, nil
}

// adapterIdentityKey 是 merge 时匹配"同一 adapter"的身份键（F-02）。
// 与 NormalizeModelAdapterConfigs 的去重键同口径（baseURL/apiKey/displayName/modelID/endpoint 组合），
// 用于在前端整包保存时把磁盘旧 adapter 的 CostMultiplier 继承到 patch 里同身份的 adapter。
// 用 trim 后的原值拼接，不做 baseURL 归一化（patch 与磁盘旧值都未 normalize，原值比对即可；
// 归一化在 merge 之后的 NormalizeConfig 统一做）。
func adapterIdentityKey(a ModelAdapterConfig) string {
	return strings.Join([]string{
		strings.TrimSpace(a.DisplayName),
		strings.TrimSpace(a.Type),
		strings.TrimSpace(a.BaseURL),
		strings.TrimSpace(a.APIKey),
		strings.TrimSpace(a.ModelID),
		strings.TrimSpace(a.OpenAIEndpoint),
	}, "|")
}

// mergeUserPatchInto 把前端整包 patch merge 到磁盘最新配置 dst（F-02）。
//
// 前端 payload 只含它管理的字段子集（normalizeConfig/normalizeModelAdapter 产出的精简对象）。
// merge 策略：
//   - 前端管理字段直接覆盖 dst：Log/ProviderStreamIdleTimeout/BackendListenAddr/
//     ProxyListenAddr/Routing.Mode/Routing.TabServerBaseURL/HomeMetrics/LastAgentModelHash/ModelAdapters。
//   - 后端独占字段保留 dst：Pricing（前端从不携带，整块不动；S15 的 InputTokenSemantics 等靠此保留）。
//   - adapter 列表整体替换，但每个 patch adapter 的 CostMultiplier 若为空，则从 dst 旧列表里
//     同身份键的 adapter 继承（前端 normalizer 不带 costMultiplier，避免每次保存清空倍率）。
//
// merge 后由调用方（Store.Update）走 NormalizeConfig 兜底校验/默认值。
func mergeUserPatchInto(dst *Config, patch Config) {
	if dst == nil {
		return
	}
	dst.Log = patch.Log
	dst.ProviderStreamIdleTimeout = patch.ProviderStreamIdleTimeout
	dst.BackendListenAddr = patch.BackendListenAddr
	dst.ProxyListenAddr = patch.ProxyListenAddr
	dst.Routing.Mode = patch.Routing.Mode
	dst.Routing.TabServerBaseURL = patch.Routing.TabServerBaseURL
	// PerNamespace：前端管理的整块覆盖（与 Mode/TabServerBaseURL 一致）。
	// patch.Routing.PerNamespace 为 nil 时不动 dst（前端 payload 未带此字段则保留旧值）。
	// 非 nil（含空 map）覆盖，随后 NormalizeConfig 会清洗/归一。
	if patch.Routing.PerNamespace != nil {
		dst.Routing.PerNamespace = patch.Routing.PerNamespace
	}
	dst.HomeMetrics = patch.HomeMetrics
	dst.WebTools = patch.WebTools
	dst.LastAgentModelHash = patch.LastAgentModelHash

	// adapter CostMultiplier 继承：先建 dst 旧列表的身份键 → 旧倍率索引。
	legacyMultiplier := make(map[string]string, len(dst.ModelAdapters))
	for _, a := range dst.ModelAdapters {
		if cm := strings.TrimSpace(a.CostMultiplier); cm != "" {
			legacyMultiplier[adapterIdentityKey(a)] = cm
		}
	}
	patchedAdapters := make([]ModelAdapterConfig, len(patch.ModelAdapters))
	for i, a := range patch.ModelAdapters {
		if strings.TrimSpace(a.CostMultiplier) == "" {
			if cm, ok := legacyMultiplier[adapterIdentityKey(a)]; ok {
				a.CostMultiplier = cm
			}
		}
		patchedAdapters[i] = a
	}
	dst.ModelAdapters = patchedAdapters
	// Pricing 整块保留 dst，不动。
}

func validateJSONMap(value string, fieldName string) error {
	text := strings.TrimSpace(value)
	if text == "" {
		return fmt.Errorf("模型适配器 %s 不能为空", fieldName)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return fmt.Errorf("模型适配器 %s 必须是合法 JSON 对象", fieldName)
	}
	if parsed == nil {
		return fmt.Errorf("模型适配器 %s 必须是 JSON 对象", fieldName)
	}
	return nil
}

func validateHeadersJSON(value string) error {
	text := strings.TrimSpace(value)
	if err := validateJSONMap(text, "customHeadersJSON"); err != nil {
		return err
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return errors.New("模型适配器 customHeadersJSON 的值必须是字符串")
	}
	for key := range parsed {
		if strings.TrimSpace(key) == "" {
			return errors.New("模型适配器 customHeadersJSON 的请求头名称不能为空")
		}
	}
	return nil
}

func normalizeReasoningEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "medium":
		return "medium"
	case "low", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeAnthropicThinkingEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "xhigh":
		return "xhigh"
	case "low", "medium", "high", "max":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeListenAddr(value string, defaultValue string, fieldName string) (string, error) {
	addr := strings.TrimSpace(value)
	if addr == "" {
		addr = defaultValue
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("%s 必须是 host:port 格式", fieldName)
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("%s host 不能为空", fieldName)
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return "", fmt.Errorf("%s port 必须在 1-65535 之间", fieldName)
	}
	// backend / proxy 只在本机 loopback 通信，且承载 MITM→backend 的信任 proof 与
	// 捕获到的真实 Cursor 凭证。绑定到非 loopback 地址会把内部信任面暴露到网络，
	// 因此强制 loopback。历史通配地址（0.0.0.0 / ::）一次性改写为 127.0.0.1 并保留端口。
	host = enforceLoopbackHost(host, fieldName)
	if !isLoopbackHost(host) {
		return "", fmt.Errorf("%s 必须绑定本机回环地址（127.0.0.1 或 ::1），当前 %q 不被允许", fieldName, host)
	}
	return net.JoinHostPort(host, strconv.Itoa(parsedPort)), nil
}

// enforceLoopbackHost 把历史通配地址一次性归一到 IPv4 loopback；其余原样返回交由校验。
func enforceLoopbackHost(host, fieldName string) string {
	switch host {
	case "0.0.0.0", "::", "[::]":
		log.Printf("config: %s 原绑定 %q 已被强制改写为 127.0.0.1（仅允许 loopback）", fieldName, host)
		return "127.0.0.1"
	default:
		return host
	}
}

// isLoopbackHost 仅接受字面 loopback IP（127.0.0.0/8 或 ::1），拒绝主机名/局域网/公网地址。
func isLoopbackHost(host string) bool {
	trimmed := strings.Trim(strings.TrimSpace(host), "[]")
	ip := net.ParseIP(trimmed)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func normalizeProviderStreamIdleTimeout(value int) int {
	if value <= 0 {
		return DefaultProviderStreamIdleTimeoutSeconds
	}
	if value < MinProviderStreamIdleTimeoutSeconds {
		return MinProviderStreamIdleTimeoutSeconds
	}
	return value
}

func normalizeMaxCompletionTokens(value int) int {
	if value <= 0 {
		return 0
	}
	return value
}

func normalizeModelAdapterType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "openai":
		return "openai"
	case "anthropic":
		return "anthropic"
	default:
		return ""
	}
}

// normalizeAdapterRole 限定 adapter Role 为 chat/image/both，空/非法默认 chat（现状行为）。
// chat：仅聊天（ModelID 必填）；image：仅生图（ImageModelID 必填，ModelID 可空→兜底成 ImageModelID）；
// both：既能 chat 又能生图（ModelID 与 ImageModelID 各自必填）。
func normalizeAdapterRole(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image":
		return "image"
	case "both":
		return "both"
	default:
		return "chat"
	}
}

func normalizeRoutingMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "local":
		return "local"
	case "upstream":
		return "upstream"
	default:
		return ""
	}
}

// normalizePerNamespace 过滤并归一化 per-namespace 路由覆盖表：
// 丢弃空 key、非法值；合法值仅 local/upstream。"auto"（跟随全局）等价于不列出，故也丢弃以保持表精简。
// 全部丢弃后返回 nil（向后兼容：旧配置无此字段，序列化也不多一个空 map）。
func normalizePerNamespace(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cleaned := make(map[string]string, len(input))
	for name, mode := range input {
		key := strings.TrimSpace(name)
		if key == "" {
			continue
		}
		normalized := normalizeRoutingMode(mode)
		// normalizeRoutingMode 把 "" 归一为 "local"，但这里空值语义应是"不覆盖/跟随全局"，
		// 故非法值与空值都跳过（不写入表），让该路由回退到全局 Mode。
		if strings.TrimSpace(mode) == "" {
			continue
		}
		if normalized == "" {
			continue
		}
		cleaned[key] = normalized
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

// normalizeWebToolsConfig 归一 WebSearch/WebFetch 工具配置。
// Provider 仅认 duckduckgo/bing/serper/tavily 四值，其余（含空）回退 duckduckgo（免 key）。
// Allowlist 去重 + 去空白 + 小写，空表归 nil（保持现 SSRF 硬拒绝基线）。
func normalizeWebToolsConfig(input WebToolsConfig) WebToolsConfig {
	provider := strings.ToLower(strings.TrimSpace(input.WebSearchProvider))
	switch provider {
	case "bing", "serper", "tavily", "duckduckgo":
	default:
		provider = "duckduckgo"
	}
	allowlist := []string{}
	seen := make(map[string]struct{}, len(input.WebFetchHostAllowlist))
	for _, host := range input.WebFetchHostAllowlist {
		trimmed := strings.ToLower(strings.TrimSpace(host))
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		allowlist = append(allowlist, trimmed)
	}
	if len(allowlist) == 0 {
		allowlist = nil
	}
	return WebToolsConfig{
		WebSearchProvider:     provider,
		WebSearchAPIKey:       strings.TrimSpace(input.WebSearchAPIKey),
		WebFetchHostAllowlist: allowlist,
	}
}

// normalizeTabServerBaseURL 归一 Tab 服务端 URL：去空白，空则放行（表示用内置默认）。
// 非空时必须能 url.Parse 且 scheme 为 http/https、host 非空——否则报错让用户修正，
// 不静默清空（静默清空会让错配的 tab 服务对前端表现为“用了默认”，难排查）。
func normalizeTabServerBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("tabServerBaseURL 无效: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("tabServerBaseURL 必须以 http:// 或 https:// 开头，得到 %q", trimmed)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("tabServerBaseURL 缺少 host: %q", trimmed)
	}
	return trimmed, nil
}
