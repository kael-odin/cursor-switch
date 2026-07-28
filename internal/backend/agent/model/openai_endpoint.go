// openai_endpoint.go 实现 OpenAI 兼容 provider 的 endpoint URL 解析与归一化。从 openai.go 拆出。
package modeladapter

import (
	"net/url"
	"strings"

	"cursor/internal/modelchannel"
)

// joinEndpointPath 把 endpointPath 追加到 baseURL 的 path 部分，保留 query/fragment（F-23）。
// 此前用 base + endpoint 字符串拼接：含 query 的 baseURL（如 Azure 风格
// https://host/openai?api-version=2024）会把 endpoint 落进 query 值里，fragment 后的路径也会丢。
// 现用 net/url 拆 Path/RawQuery/Fragment，endpoint 只拼到 path，query/fragment 原样保留。
// endpointPath 必须以 "/" 开头（由调用方保证）。url.Parse 失败时 fallback 到旧拼接，不退步。
func joinEndpointPath(baseURL string, endpointPath string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		// 异常态：baseURL 进来前应已过 NormalizeBaseURL 校验，parse 失败属退化，保持旧行为。
		return strings.TrimRight(baseURL, "/") + endpointPath
	}
	path := strings.TrimRight(parsed.Path, "/")
	parsed.Path = path + endpointPath
	// 手动重组而非 u.String()：后者会对 path 做 RFC escape，可能与"原样保留"行为有细微差异。
	// 手动拼 Scheme/Host/Path/RawQuery/Fragment 最可控且可断言。
	result := parsed.Scheme + "://" + parsed.Host + parsed.Path
	if parsed.RawQuery != "" {
		result += "?" + parsed.RawQuery
	}
	if parsed.Fragment != "" {
		result += "#" + parsed.Fragment
	}
	return result
}

func OpenAIEndpointURL(baseURL string, endpoint string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	normalizedEndpoint := strings.TrimSpace(endpoint)
	if normalizedEndpoint == "" {
		normalizedEndpoint = modelchannel.OpenAIEndpointResponses
	}
	if !strings.HasPrefix(normalizedEndpoint, "/") {
		normalizedEndpoint = "/" + normalizedEndpoint
	}
	// F-23：词法短路判定（已含 endpoint / 版本段）在 path 部分上做，避免含 query/fragment 的
	// baseURL 让 HasSuffix 失效（如 .../chat/completions?api-version=x 的 suffix 是 query）。
	basePath, baseQuery, baseFragment := splitURLPathQueryFragment(base)
	// 规则0：自定义路径模式
	// - baseURL 已含 endpoint 后缀（/chat/completions 或 /responses）→ 直接用 base
	// - 否则追加 /chat/completions（默认协议形态，覆盖 Z.AI /v4 等场景）
	if normalizedEndpoint == modelchannel.OpenAIEndpointCustom {
		if OpenAIEndpointFromBaseURL(basePath) != "" {
			return base
		}
		return joinEndpointPath(baseURL, "/chat/completions")
	}
	// 规则1：baseURL 已含 endpoint 后缀 → 直接用 base（query/fragment 原样）
	if OpenAIEndpointFromBaseURL(basePath) != "" {
		return base
	}
	// 规则2：baseURL 以 /vN 结尾时，剥离 endpoint 的版本前缀（/v1/、/v2/ 等）
	// 这样 base=.../v4 + endpoint=/v1/chat/completions → .../v4/chat/completions
	if _, ok := trailingVersionSegment(basePath); ok {
		if rest, stripped := stripEndpointVersionPrefix(normalizedEndpoint); stripped {
			return joinPathQueryFragment(basePath, rest, baseQuery, baseFragment)
		}
	}
	// 规则3：兜底原样拼接（endpoint 只拼到 path，query/fragment 保留）
	return joinEndpointPath(baseURL, normalizedEndpoint)
}

// splitURLPathQueryFragment 把已 trim 的 base URL 拆成 path / rawQuery / fragment 三段。
// 不用 url.Parse 以保留原始大小写与不转义形态；query/fragment 在 base 上按首个 ? 和首个 # 切分。
// 用于让词法短路判定（HasSuffix 等）只在 path 上工作，不受 query/fragment 干扰（F-23）。
func splitURLPathQueryFragment(base string) (path, query, fragment string) {
	path = base
	if i := strings.Index(path, "#"); i >= 0 {
		fragment = path[i+1:]
		path = path[:i]
	}
	if i := strings.Index(path, "?"); i >= 0 {
		query = path[i+1:]
		path = path[:i]
	}
	return path, query, fragment
}

// joinPathQueryFragment 重组 path + endpoint 后缀 + query + fragment（F-23）。
// 与 joinEndpointPath 配对：当已拆出 path/query/fragment 时直接重组，避免重复 parse。
func joinPathQueryFragment(basePath, endpointSuffix, query, fragment string) string {
	path := strings.TrimRight(basePath, "/") + endpointSuffix
	result := path
	if query != "" {
		result += "?" + query
	}
	if fragment != "" {
		result += "#" + fragment
	}
	return result
}

// trailingVersionSegment 检测 URL 末尾是否以 /vN 形式结尾（N 为数字），
// 返回版本段（如 "v4"）和是否匹配。用于通用版本段去重。
func trailingVersionSegment(base string) (string, bool) {
	idx := strings.LastIndex(base, "/")
	if idx < 0 {
		return "", false
	}
	seg := base[idx+1:]
	if len(seg) < 2 || seg[0] != 'v' {
		return "", false
	}
	for i := 1; i < len(seg); i++ {
		if seg[i] < '0' || seg[i] > '9' {
			return "", false
		}
	}
	return seg, true
}

// stripEndpointVersionPrefix 剥离 endpoint 路径开头的版本段前缀（/vN/），
// 返回剩余路径和是否成功剥离。
// /v1/chat/completions → ("/chat/completions", true)
// /chat/completions    → ("", false)
func stripEndpointVersionPrefix(endpoint string) (string, bool) {
	if len(endpoint) < 4 || endpoint[0] != '/' || endpoint[1] != 'v' {
		return "", false
	}
	i := 2
	for i < len(endpoint) && endpoint[i] >= '0' && endpoint[i] <= '9' {
		i++
	}
	if i == 2 || i >= len(endpoint) || endpoint[i] != '/' {
		return "", false
	}
	return endpoint[i:], true
}

func ResolveOpenAIEndpoint(baseURL string, endpoint string) string {
	if endpointFromURL := OpenAIEndpointFromBaseURL(baseURL); endpointFromURL != "" {
		return endpointFromURL
	}
	return modelchannel.NormalizeOpenAIEndpoint("openai", endpoint)
}

func OpenAIEndpointFromBaseURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(strings.ToLower(baseURL)), "/")
	switch {
	case strings.HasSuffix(base, "/responses"):
		return modelchannel.OpenAIEndpointResponses
	case strings.HasSuffix(base, "/chat/completions"):
		return modelchannel.OpenAIEndpointChatCompletions
	default:
		return ""
	}
}

func ProviderURLHasEndpoint(baseURL string, endpoints ...string) bool {
	base := strings.TrimRight(strings.TrimSpace(strings.ToLower(baseURL)), "/")
	if base == "" {
		return false
	}
	for _, endpoint := range endpoints {
		normalizedEndpoint := strings.TrimRight(strings.TrimSpace(strings.ToLower(endpoint)), "/")
		if normalizedEndpoint == "" {
			continue
		}
		if !strings.HasPrefix(normalizedEndpoint, "/") {
			normalizedEndpoint = "/" + normalizedEndpoint
		}
		if strings.HasSuffix(base, normalizedEndpoint) {
			return true
		}
	}
	return false
}
