// web_tools_probe.go 实现 Web 工具配置的「测试」按钮：对 Tab 服务、WebSearch、WebFetch
// 各做一次轻量探活，返回成功/失败 + 友好摘要，让用户在保存前确认配置可用。
//
// 设计要点（对齐 TestModelAdapter 的 UX：先测试后保存）：
//   - 前端把当前表单值（未必已保存）作为参数传入，探活不读 store——与 model 测试一致。
//   - 探活只验证「能不能通」，不追求与 agent 运行时完全等价；搜索/抓取的完整语义仍由
//     agent/bridge/interaction 在真实工具调用时执行。这里只复刻请求形状做连通性检查，
//     刻意轻量、自包含，不跨包依赖 interaction（避免 client→agent/bridge 循环依赖）。
//   - 缺 key 的需 key provider 直接友好报错（与运行时 errWebSearchAPIKeyMissing 同语义）。
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"cursor/internal/netproxy"
)

const (
	webToolsProbeTimeout      = 20 * time.Second
	webToolsProbeSearchTerm   = "cursor"
	webToolsProbeFetchURL     = "https://example.com/"
	webToolsProbeUserAgent    = "cursor-switch/1.0 web-tools-probe"
	webToolsProbeMaxBodyBytes = 1 << 20 // 1MiB，探活响应通常很小，截断防异常大响应
)

// WebToolsProbeKind 标识被探活的 Web 工具类别。
type WebToolsProbeKind string

const (
	WebToolsProbeKindTabServer  WebToolsProbeKind = "tabserver"
	WebToolsProbeKindWebSearch  WebToolsProbeKind = "websearch"
	WebToolsProbeKindWebFetch   WebToolsProbeKind = "webfetch"
)

// WebToolsProbeResult 是一次 Web 工具探活结果。前端按 Status 渲染成功/失败，Detail 作摘要。
type WebToolsProbeResult struct {
	Kind   WebToolsProbeKind `json:"kind"`
	Status string            `json:"status"` // success | error
	Detail string            `json:"detail"` // 成功给简短可达摘要；失败给友好原因
}

// WebToolsProbeRequest 是前端传入的探活请求，携带三类工具各自的当前表单值。
type WebToolsProbeRequest struct {
	Kind             WebToolsProbeKind `json:"kind"`
	TabServerBaseURL string            `json:"tabServerBaseURL,omitempty"`
	WebSearchProvider string            `json:"webSearchProvider,omitempty"`
	WebSearchAPIKey  string            `json:"webSearchAPIKey,omitempty"`
}

// TestWebTools 对 Tab 服务 / WebSearch / WebFetch 做一次连通性探活。
//
// 前端在 Config 页给三类工具各放一个「测试」按钮，把当前表单值打包成 request 传入；
// 后端按 Kind 分派：tabserver=GET baseURL（预期 2xx/3xx 即可达）；websearch=按 provider
// 发一次真实搜索（duckduckgo 免 key，bing/serper/tavily 用 key）；webfetch=GET 固定稳定 URL。
// 不读 store，全凭入参——与 TestModelAdapter「测的是表单当前值」语义一致。
func (s *ProxyService) TestWebTools(request WebToolsProbeRequest) (WebToolsProbeResult, error) {
	kind := strings.ToLower(strings.TrimSpace(string(request.Kind)))
	result := WebToolsProbeResult{Kind: WebToolsProbeKind(kind)}
	ctx, cancel := context.WithTimeout(context.Background(), webToolsProbeTimeout)
	defer cancel()

	var detail string
	var err error
	switch WebToolsProbeKind(kind) {
	case WebToolsProbeKindTabServer:
		detail, err = probeTabServer(ctx, request.TabServerBaseURL)
	case WebToolsProbeKindWebSearch:
		detail, err = probeWebSearch(ctx, request.WebSearchProvider, request.WebSearchAPIKey)
	case WebToolsProbeKindWebFetch:
		detail, err = probeWebFetch(ctx)
	default:
		err = fmt.Errorf("unknown web tools probe kind: %q", request.Kind)
	}

	if err != nil {
		result.Status = "error"
		result.Detail = err.Error()
		return result, nil
	}
	result.Status = "success"
	result.Detail = detail
	return result, nil
}

// probeTabServer 对 Tab 服务端 baseURL 做一次 GET，2xx/3xx 即视为可达。
// 空 baseURL 视为「直连官方上游」——探活直接报成功并说明语义。
func probeTabServer(ctx context.Context, baseURL string) (string, error) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return "未配置自建 Tab 服务地址，将直连官方上游（依赖本机 Cursor 账号）", nil
	}
	parsed, err := neturl.Parse(trimmed)
	if err != nil || (strings.ToLower(parsed.Scheme) != "http" && strings.ToLower(parsed.Scheme) != "https") {
		return "", fmt.Errorf("Tab 服务地址无效，需以 http:// 或 https:// 开头")
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("Tab 服务地址缺少 host")
	}
	client := netproxy.NewHTTPClientNoRedirect(webToolsProbeTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, trimmed, nil)
	if err != nil {
		return "", fmt.Errorf("构建请求失败: %w", err)
	}
	req.Header.Set("User-Agent", webToolsProbeUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("无法连接 Tab 服务: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, webToolsProbeMaxBodyBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("Tab 服务返回 HTTP %d", resp.StatusCode)
	}
	return fmt.Sprintf("Tab 服务可达（HTTP %d）", resp.StatusCode), nil
}

// probeWebSearch 按当前 provider+key 发一次真实搜索（固定搜索词），成功返回结果摘要。
// duckduckgo 免 key；bing/serper/tavily 缺 key 直接友好报错（与运行时同语义）。
func probeWebSearch(ctx context.Context, provider, apiKey string) (string, error) {
	p := strings.ToLower(strings.TrimSpace(provider))
	client := netproxy.NewHTTPClientNoRedirect(webToolsProbeTimeout)
	switch p {
	case "bing", "serper", "tavily", "baidu":
	default:
		p = "duckduckgo"
	}
	switch p {
	case "bing":
		if strings.TrimSpace(apiKey) == "" {
			return "", fmt.Errorf("Bing 需要订阅 key（Ocp-Apim-Subscription-Key），请先填写")
		}
		count, err := probeBingSearch(ctx, client, apiKey)
		if err != nil {
			return "", fmt.Errorf("Bing 搜索失败: %w", err)
		}
		return fmt.Sprintf("Bing 搜索成功，返回 %d 条结果", count), nil
	case "serper":
		if strings.TrimSpace(apiKey) == "" {
			return "", fmt.Errorf("Serper 需要 API key，请先填写")
		}
		count, err := probeSerperSearch(ctx, client, apiKey)
		if err != nil {
			return "", fmt.Errorf("Serper 搜索失败: %w", err)
		}
		return fmt.Sprintf("Serper 搜索成功，返回 %d 条结果", count), nil
	case "tavily":
		if strings.TrimSpace(apiKey) == "" {
			return "", fmt.Errorf("Tavily 需要 API key，请先填写")
		}
		count, err := probeTavilySearch(ctx, client, apiKey)
		if err != nil {
			return "", fmt.Errorf("Tavily 搜索失败: %w", err)
		}
		return fmt.Sprintf("Tavily 搜索成功，返回 %d 条结果", count), nil
	case "baidu":
		// baidu 免 key：直接抓搜索页，2xx 即可达（运行时解析失败会回退 DDG）。
		if err := probeBaiduSearch(ctx, client); err != nil {
			return "", fmt.Errorf("百度搜索失败: %w", err)
		}
		return "百度搜索端点可达（免 key，失败自动回退 DuckDuckGo）", nil
	default:
		// duckduckgo 免 key 探活：直接抓 Instant Answer JSON 端点，返回 200 即可达。
		if err := probeDuckDuckGoSearch(ctx, client); err != nil {
			return "", fmt.Errorf("DuckDuckGo 搜索失败: %w", err)
		}
		return "DuckDuckGo 搜索端点可达（免 key）", nil
	}
}

// probeBingSearch 探活 Bing Web Search API v7，返回结果条数。
func probeBingSearch(ctx context.Context, client *http.Client, apiKey string) (int, error) {
	requestURL := "https://api.bing.microsoft.com/v7.0/search?count=5&q=" + neturl.QueryEscape(webToolsProbeSearchTerm)
	var payload struct {
		WebPages struct {
			Value []struct {
				Name string `json:"name"`
				URL  string `json:"url"`
			} `json:"value"`
		} `json:"webPages"`
	}
	if err := probeGetJSON(ctx, client, requestURL, map[string]string{"Ocp-Apim-Subscription-Key": apiKey}, &payload); err != nil {
		return 0, err
	}
	return len(payload.WebPages.Value), nil
}

// probeSerperSearch 探活 Serper（Google Search API 包装），返回 organic 条数。
func probeSerperSearch(ctx context.Context, client *http.Client, apiKey string) (int, error) {
	requestURL := "https://google.serper.dev/search?num=5&q=" + neturl.QueryEscape(webToolsProbeSearchTerm)
	var payload struct {
		Organic []struct {
			Title string `json:"title"`
			Link  string `json:"link"`
		} `json:"organic"`
	}
	if err := probeGetJSON(ctx, client, requestURL, map[string]string{"X-API-KEY": apiKey, "User-Agent": "cursor-local-agent/1.0"}, &payload); err != nil {
		return 0, err
	}
	return len(payload.Organic), nil
}

// probeTavilySearch 探活 Tavily Search API，返回 results 条数。
func probeTavilySearch(ctx context.Context, client *http.Client, apiKey string) (int, error) {
	requestURL := "https://api.tavily.com/search?max_results=5&query=" + neturl.QueryEscape(webToolsProbeSearchTerm)
	var payload struct {
		Results []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"results"`
	}
	if err := probeGetJSON(ctx, client, requestURL, map[string]string{"Authorization": "Bearer " + apiKey}, &payload); err != nil {
		return 0, err
	}
	return len(payload.Results), nil
}

// probeDuckDuckGoSearch 探活 DuckDuckGo Instant Answer JSON 端点，200+非空即可达。
func probeDuckDuckGoSearch(ctx context.Context, client *http.Client) error {
	requestURL := "https://api.duckduckgo.com/?q=" + neturl.QueryEscape(webToolsProbeSearchTerm) + "&format=json&no_html=1"
	var payload map[string]any
	return probeGetJSON(ctx, client, requestURL, nil, &payload)
}

// probeBaiduSearch 探活百度搜索端点：GET 搜索页，2xx 即视为可达。
// 百度返回 HTML（非 JSON），故不复用 probeGetJSON；运行时解析失败会回退 DDG。
func probeBaiduSearch(ctx context.Context, client *http.Client) error {
	requestURL := "https://www.baidu.com/s?ie=utf-8&tn=baidu&wd=" + neturl.QueryEscape(webToolsProbeSearchTerm)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, webToolsProbeMaxBodyBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http status %d", resp.StatusCode)
	}
	return nil
}

// probeGetJSON 发 GET 请求并以 JSON 解析到 out。超时由 ctx 控制。
// 4xx/5xx 返回携带状态码的错误；非 JSON 体报解码错误。
func probeGetJSON(ctx context.Context, client *http.Client, requestURL string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	if _, ok := headers["User-Agent"]; !ok {
		req.Header.Set("User-Agent", webToolsProbeUserAgent)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, webToolsProbeMaxBodyBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http status %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// probeWebFetch 抓取固定稳定 URL，2xx 且正文非空即视为 WebFetch 链路可达。
// 这不测 WebFetch 的 SSRF 白名单（白名单在运行时按 agent 传入 URL 判定），
// 只验证「本进程能正常出网抓取」——白名单配置本身由 normalizeWebToolsConfig 保证合法。
func probeWebFetch(ctx context.Context) (string, error) {
	client := netproxy.NewHTTPClientNoRedirect(webToolsProbeTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, webToolsProbeFetchURL, nil)
	if err != nil {
		return "", fmt.Errorf("构建请求失败: %w", err)
	}
	req.Header.Set("User-Agent", webToolsProbeUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("WebFetch 出网失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, webToolsProbeMaxBodyBytes))
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("WebFetch 目标返回 HTTP %d", resp.StatusCode)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return "", fmt.Errorf("WebFetch 目标返回空正文")
	}
	return fmt.Sprintf("WebFetch 链路可达（抓到 %d 字节）", len(body)), nil
}
