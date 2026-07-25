// model_fetch.go 实现「一键获取模型列表」：根据已填写的 baseURL+apiKey+type 调用
// provider 的 /v1/models（OpenAI 兼容）或 Anthropic 协议的等价端点，返回模型列表供用户挑选。
//
// 设计要点：
//   - 不要求 modelID（拉列表发生在用户选模型之前），故不能复用 NormalizeModelAdapterConfigs
//     （它要求 modelID/displayName/tooltipData 非空）。这里只用轻量校验：baseURL+apiKey+type。
//   - 复用 openai 走通时的鉴权/自定义头/HTTP client/错误格式化逻辑，避免重复实现。
//   - 候选 URL 回退：OpenAI 协议试 /v1/models → /models；Anthropic 协议先剥已知兼容后缀
//     （/anthropic、/api/claudecode、/api/coding 等）再拼 /v1/models，404/405 时试下一个。
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/modelchannel"
	"cursor/internal/netproxy"
)

const (
	modelFetchTimeout        = 20 * time.Second
	modelFetchMaxBodyBytes   = 1 << 20 // 1MiB，模型列表通常很小，截断防止异常大响应
	modelFetchUserAgent      = "cursor-byok/1.0 model-fetch"
	modelFetchAnthropicVer   = "2023-06-01"
)

// FetchedModel 表示从 provider /v1/models 拉回的单个模型。
type FetchedModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	OwnedBy     string `json:"ownedBy,omitempty"`
	// ContextWindowTokens 是从内置上下文窗口表反查到的窗口大小（0 表示未命中，需手填）。
	// provider 的 /v1/models 通常不返回窗口，这里用 models.dev 缓存表 + 候选匹配自动回填。
	ContextWindowTokens int64 `json:"contextWindowTokens,omitempty"`
}

// FetchedModelsPayload 是返回给前端的模型列表载荷，附带来源信息便于调试。
type FetchedModelsPayload struct {
	Models    []FetchedModel `json:"models"`
	SourceURL string         `json:"sourceURL"`
}

// knownAnthropicCompatSuffixes 列出 Anthropic 协议 provider 常见的兼容路径后缀。
// 剥离后才能拼出真正的 /v1/models。按最长优先排序，避免短后缀先误匹配。
// 移植自 cc-switch services/model_fetch.rs 的 KNOWN_COMPAT_SUFFIXES。
var knownAnthropicCompatSuffixes = func() []string {
	raw := []string{
		"/anthropic",
		"/api/claudecode",
		"/api/coding",
		"/api/claude",
		"/claudecode",
		"/coding",
		"/claude",
		"/v1/messages",
		"/messages",
	}
	sort.Slice(raw, func(i, j int) bool { return len(raw[i]) > len(raw[j]) })
	return raw
}()

// FetchProviderModels 调用 provider 的模型列表端点，返回去重排序后的模型清单。
// adapter 只需填好 type/baseURL/apiKey（+ 可选 customHeaders）；modelID 等字段可空。
func (s *ProxyService) FetchProviderModels(adapter serverconfig.ModelAdapterConfig) (FetchedModelsPayload, error) {
	providerType, baseURL, apiKey, customHeadersEnabled, customHeadersJSON, err := normalizeAdapterForModelFetch(adapter)
	if err != nil {
		return FetchedModelsPayload{}, err
	}

	candidates := buildModelsURLCandidates(providerType, baseURL)
	if len(candidates) == 0 {
		return FetchedModelsPayload{}, fmt.Errorf("无法根据接口地址生成模型列表 URL")
	}

	client := netproxy.NewHTTPClient(modelFetchTimeout)
	var lastErr error
	for _, candidate := range candidates {
		models, fetchErr := fetchModelsFromCandidate(client, candidate, providerType, apiKey, customHeadersEnabled, customHeadersJSON)
		if fetchErr == nil {
			enrichWithContextWindow(models)
			return FetchedModelsPayload{Models: models, SourceURL: candidate}, nil
		}
		lastErr = fetchErr
		// 404/405 表示这个候选路径不对，继续试下一个；其它错误（鉴权失败、网络）直接返回。
		if !isCandidateNotFoundErr(fetchErr) {
			return FetchedModelsPayload{}, fetchErr
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("未找到可用的模型列表端点")
	}
	return FetchedModelsPayload{}, lastErr
}

// normalizeAdapterForModelFetch 只校验拉取模型列表必需的字段，不要求 modelID/displayName。
func normalizeAdapterForModelFetch(adapter serverconfig.ModelAdapterConfig) (string, string, string, bool, string, error) {
	providerType := strings.ToLower(strings.TrimSpace(adapter.Type))
	if providerType != "openai" && providerType != "anthropic" {
		return "", "", "", false, "", fmt.Errorf("provider 类型仅支持 openai 或 anthropic")
	}
	baseURL, err := modelchannel.NormalizeBaseURL(adapter.BaseURL)
	if err != nil {
		return "", "", "", false, "", err
	}
	apiKey := strings.TrimSpace(adapter.APIKey)
	if apiKey == "" {
		return "", "", "", false, "", fmt.Errorf("访问密钥不能为空")
	}
	return providerType, baseURL, apiKey, adapter.CustomHeadersEnabled, adapter.CustomHeadersJSON, nil
}

// buildModelsURLCandidates 按 provider 协议生成候选模型列表 URL，优先级从高到低。
func buildModelsURLCandidates(providerType string, baseURL string) []string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil
	}
	if providerType == "openai" {
		// 用户可能已把完整路径写进 baseURL（如 .../v1/models），直接尊重。
		if strings.HasSuffix(strings.ToLower(base), "/models") {
			return []string{base}
		}
		return []string{base + "/v1/models", base + "/models"}
	}
	// anthropic：先剥兼容后缀，再拼 /v1/models
	stripped := stripAnthropicCompatSuffix(base)
	if stripped == "" {
		return nil
	}
	return []string{stripped + "/v1/models", stripped + "/models"}
}

// stripAnthropicCompatSuffix 剥离 baseURL 末尾的已知 Anthropic 兼容后缀（最长优先）。
func stripAnthropicCompatSuffix(base string) string {
	lower := strings.ToLower(base)
	for _, suffix := range knownAnthropicCompatSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return base[:len(base)-len(suffix)]
		}
	}
	return base
}

// isCandidateNotFoundErr 判断错误是否表示当前候选路径不存在（应继续试下一个）。
func isCandidateNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "status=404") || strings.Contains(msg, "status=405")
}

func fetchModelsFromCandidate(client *http.Client, url string, providerType string, apiKey string, customHeadersEnabled bool, customHeadersJSON string) ([]FetchedModel, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	if providerType == "anthropic" {
		modeladapter.ApplyAnthropicCompatibleAuthHeaders(req, apiKey)
		req.Header.Set("anthropic-version", modelFetchAnthropicVer)
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", modelFetchUserAgent)
	if err := modeladapter.ApplyCustomHeaders(req, customHeadersEnabled, customHeadersJSON); err != nil {
		return nil, fmt.Errorf("应用自定义请求头失败: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求模型列表失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, buildModelAdapterHTTPStatusError("fetch models", resp)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, modelFetchMaxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	models, err := parseModelsResponse(body)
	if err != nil {
		return nil, fmt.Errorf("解析模型列表失败: %w", err)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("provider 返回的模型列表为空")
	}
	return models, nil
}

// parseModelsResponse 解析 OpenAI 风格的 {data:[{id,...}]} 响应。
// 兼容 display_name（部分 provider）与 owned_by 字段。
func parseModelsResponse(body []byte) ([]FetchedModel, error) {
	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			OwnedBy     string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(payload.Data))
	out := make([]FetchedModel, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		display := strings.TrimSpace(item.DisplayName)
		if display == "" {
			display = id
		}
		out = append(out, FetchedModel{ID: id, DisplayName: display, OwnedBy: strings.TrimSpace(item.OwnedBy)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// enrichWithContextWindow 用内置上下文窗口表给每个拉回的模型回填 ContextWindowTokens。
// provider 的 /v1/models 几乎都不返回窗口；这里用 models.dev 缓存表 + 候选匹配反查，
// 命中则填入，未命中保持 0（前端显示「需手填」）。免去用户逐个手填上下文窗口的负担。
func enrichWithContextWindow(models []FetchedModel) {
	for i := range models {
		if models[i].ContextWindowTokens > 0 {
			continue
		}
		if v := lookupContextWindowTokens(models[i].ID); v > 0 {
			models[i].ContextWindowTokens = v
		}
	}
}
