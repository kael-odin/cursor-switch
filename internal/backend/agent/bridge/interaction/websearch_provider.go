// websearch_provider.go 实现 WebSearch 的可配置多 provider 上游。
//
// 审计「行为偏离-3」：原 executeWebSearch 硬编码 DuckDuckGo HTML 抓取
// （https://html.duckduckgo.com/html/?q=），易被封禁、结果质量/时效性差。
// 改为按 WebToolsConfig.WebSearchProvider 分派，分两层：
//
//   - 免 key 层（duckduckgo / baidu / bing 无 key）走「链式回退」：provider 单选即「链首」，
//     失败或无结果按固定兜底顺序试其余免 key 引擎，首个返回非空结果的引擎胜出
//     （executeFreeSearchChain）。链尾固定 duckduckgo → bing → baidu：
//     duckduckgo 走稳定 Instant Answer JSON，bing 是 HTML 抓取，baidu 英文结果最弱垫底。
//     中文查询选 baidu 作链首质量最佳；英文/混搜选 bing 或 duckduckgo。
//     amadeus 浏览器版的「必应>DDG>百度」是数据中心 IP 被百度风控逼出来的，
//     本机住宅 IP 不适用，故 baidu 允许靠前。
//   - BYOK 层（bing 有 key / serper / tavily）：走各家官方 HTTPS GET + JSON，缺 key 返回
//     errWebSearchAPIKeyMissing 显式告警（而非静默失败）；调用失败即报错，不静默降级到
//     免费层——用户为结果质量付费，静默替换是正确性 bug。
//
// 缺 provider（空或非认可值）一律回退 duckduckgo 作链首。
package interaction

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"cursor/gen/agentv1"
	"cursor/internal/netproxy"
)

// errWebSearchAPIKeyMissing 在用户选了需 key 的 provider 但未填 key 时返回。
// 调用方把它作为工具结果回给模型，让用户明确知道 WebSearch 因缺 key 不可用。
var errWebSearchAPIKeyMissing = fmt.Errorf("configured web search provider requires an API key; configure webSearchAPIKey or switch to duckduckgo")

// dispatchWebSearch 按当前配置选择搜索路径执行搜索。
// searchTerm 已由调用方 trim 过。返回 references（≤5）+ payload。
//
// 分派语义：
//   - serper / tavily：BYOK，缺 key → errWebSearchAPIKeyMissing；调用失败响亮报错，不回退免费层。
//   - bing：有 key 走官方 API v7（BYOK）；无 key 走免费 HTML 抓取并作为免 key 链首。
//   - baidu / duckduckgo / 空 / 非认可值：免 key 链式回退，链首分别 baidu / duckduckgo / duckduckgo。
func (bridge *Bridge) dispatchWebSearch(searchTerm string) ([]*agentv1.WebSearchReference, string, error) {
	cfg := bridge.currentWebTools()
	switch strings.ToLower(strings.TrimSpace(cfg.WebSearchProvider)) {
	case "serper":
		if strings.TrimSpace(cfg.WebSearchAPIKey) == "" {
			return nil, "", errWebSearchAPIKeyMissing
		}
		return executeSerperSearch(bridge.httpClient, searchTerm, cfg.WebSearchAPIKey)
	case "tavily":
		if strings.TrimSpace(cfg.WebSearchAPIKey) == "" {
			return nil, "", errWebSearchAPIKeyMissing
		}
		return executeTavilySearch(bridge.httpClient, searchTerm, cfg.WebSearchAPIKey)
	case "bing":
		// 双模：有 key → 官方 API（失败响亮不回退）；无 key → 免费 HTML 抓取为免 key 链首。
		if strings.TrimSpace(cfg.WebSearchAPIKey) != "" {
			return executeBingSearch(bridge.httpClient, searchTerm, cfg.WebSearchAPIKey)
		}
		return bridge.executeFreeSearchChain("bing", searchTerm)
	case "baidu":
		return bridge.executeFreeSearchChain("baidu", searchTerm)
	default:
		// duckduckgo / 空 / 非认可值：免 key 降级路径，duckduckgo 为链首。
		return bridge.executeFreeSearchChain("duckduckgo", searchTerm)
	}
}

// webSearchAbstractLimit 是搜索摘要按 rune 截断的上限（百度/必应共用，对齐 amadeus 的 300）。
const webSearchAbstractLimit = 300

// freeWebSearchFallbackOrder 是免 key 链的固定兜底顺序（链首排除后按此补位）。
// 考量见包注释：duckduckgo（稳定 JSON）→ bing（HTML 抓取）→ baidu（英文结果最弱）。
var freeWebSearchFallbackOrder = []string{"duckduckgo", "bing", "baidu"}

// freeWebSearchChain 返回免 key 链的实际执行顺序：链首（用户选中的 provider）在前，
// 其余引擎按 freeWebSearchFallbackOrder 固定顺序补位。链首非免 key 值（未知值）一律
// 归一为 duckduckgo，保证 executeFreeSearchEngine 只收得免 key 引擎。
func freeWebSearchChain(head string) []string {
	switch head {
	case "baidu", "bing":
	default:
		head = "duckduckgo"
	}
	engines := make([]string, 0, len(freeWebSearchFallbackOrder))
	engines = append(engines, head)
	for _, engine := range freeWebSearchFallbackOrder {
		if engine != head {
			engines = append(engines, engine)
		}
	}
	return engines
}

// executeFreeSearchChain 在免 key 引擎间按 freeWebSearchChain 的顺序逐个尝试，
// 首个返回非空结果的引擎胜出。全链失败返回聚合错误（含各引擎原因），绝不静默返回空——
// 调用方把它作为工具结果回给模型，让用户知道搜索为何不可用。
func (bridge *Bridge) executeFreeSearchChain(head string, searchTerm string) ([]*agentv1.WebSearchReference, string, error) {
	if strings.TrimSpace(searchTerm) == "" {
		return nil, "", fmt.Errorf("web search search_term is required")
	}
	failures := make([]string, 0, len(freeWebSearchFallbackOrder))
	for _, engine := range freeWebSearchChain(head) {
		references, payload, err := bridge.executeFreeSearchEngine(engine, searchTerm)
		if err == nil && len(references) > 0 {
			return references, payload, nil
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s(%v)", engine, err))
		}
	}
	return nil, "", fmt.Errorf("web search failed on all free engines: %s", strings.Join(failures, "; "))
}

// executeFreeSearchEngine 执行单个免 key 引擎。engine ∈ {duckduckgo, bing, baidu}。
func (bridge *Bridge) executeFreeSearchEngine(engine string, searchTerm string) ([]*agentv1.WebSearchReference, string, error) {
	switch engine {
	case "baidu":
		client := bridge.httpClient
		if client == nil {
			client = netproxy.NewHTTPClient(15 * time.Second)
		}
		return tryBaiduWebSearch(client, searchTerm)
	case "bing":
		return executeBingHTMLSearch(bridge.httpClient, searchTerm)
	default:
		return bridge.executeDuckDuckGoSearch(searchTerm)
	}
}

// cleanWebSearchText 折叠空白并去除首尾空格（搜索摘要通用清洗，百度/必应共用）。
func cleanWebSearchText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

// truncateWebSearchAbstract 按 rune 数截断摘要，超限截断到 limit（百度/必应共用）。
func truncateWebSearchAbstract(value string, limit int) string {
	value = cleanWebSearchText(value)
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// httpGetJSON 发 GET 请求并以 JSON 解析到 out。超时由 client 控制。
func httpGetJSON(client *http.Client, requestURL string, headers map[string]string, out any) error {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	request, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("web search http status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// limitWebSearchReferences 裁剪到最多 5 条，与 DuckDuckGo 路径口径一致。
func limitWebSearchReferences(references []*agentv1.WebSearchReference) []*agentv1.WebSearchReference {
	if len(references) > 5 {
		references = references[:5]
	}
	return references
}

// executeBingSearch 调 Bing Web Search API v7。
// 文档：GET https://api.bing.microsoft.com/v7.0/search?q=...&count=5，header Ocp-Apim-Subscription-Key。
func executeBingSearch(client *http.Client, searchTerm, apiKey string) ([]*agentv1.WebSearchReference, string, error) {
	requestURL := "https://api.bing.microsoft.com/v7.0/search?count=5&q=" + neturl.QueryEscape(searchTerm)
	var payload struct {
		WebPages struct {
			Value []struct {
				Name    string `json:"name"`
				URL     string `json:"url"`
				Snippet string `json:"snippet"`
			} `json:"value"`
		} `json:"webPages"`
	}
	if err := httpGetJSON(client, requestURL, map[string]string{"Ocp-Apim-Subscription-Key": apiKey}, &payload); err != nil {
		return nil, "", err
	}
	references := parseBingReferences(payload.WebPages.Value, searchTerm)
	if len(references) == 0 {
		return nil, "", fmt.Errorf("web search returned no parseable results")
	}
	references = limitWebSearchReferences(references)
	return references, formatWebSearchPayload(searchTerm, references), nil
}

// parseBingReferences 把 Bing webPages.value 转成 WebSearchReference 列表。
// 抽出为纯函数便于单测（HTTP 请求逻辑与解析解耦）。
func parseBingReferences(items []struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}, searchTerm string) []*agentv1.WebSearchReference {
	references := make([]*agentv1.WebSearchReference, 0, len(items))
	for _, item := range items {
		title := strings.TrimSpace(item.Name)
		link := strings.TrimSpace(item.URL)
		if title == "" || link == "" {
			continue
		}
		references = append(references, &agentv1.WebSearchReference{
			Title: title,
			Url:   link,
			Chunk: strings.TrimSpace(item.Snippet),
		})
	}
	return references
}

// executeSerperSearch 调 Serper（Google Search API 包装）。
// 文档：GET https://google.serper.dev/search?q=...，header X-API-KEY。
func executeSerperSearch(client *http.Client, searchTerm, apiKey string) ([]*agentv1.WebSearchReference, string, error) {
	requestURL := "https://google.serper.dev/search?num=5&q=" + neturl.QueryEscape(searchTerm)
	var payload struct {
		Organic []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"organic"`
	}
	if err := httpGetJSON(client, requestURL, map[string]string{"X-API-KEY": apiKey, "User-Agent": "cursor-local-agent/1.0"}, &payload); err != nil {
		return nil, "", err
	}
	references := make([]*agentv1.WebSearchReference, 0, len(payload.Organic))
	for _, item := range payload.Organic {
		title := strings.TrimSpace(item.Title)
		link := strings.TrimSpace(item.Link)
		if title == "" || link == "" {
			continue
		}
		references = append(references, &agentv1.WebSearchReference{
			Title: title,
			Url:   link,
			Chunk: strings.TrimSpace(item.Snippet),
		})
	}
	if len(references) == 0 {
		return nil, "", fmt.Errorf("web search returned no parseable results")
	}
	references = limitWebSearchReferences(references)
	return references, formatWebSearchPayload(searchTerm, references), nil
}

// executeTavilySearch 调 Tavily Search API。
// 文档：GET https://api.tavily.com/search?query=...&max_results=5，header Authorization Bearer。
func executeTavilySearch(client *http.Client, searchTerm, apiKey string) ([]*agentv1.WebSearchReference, string, error) {
	requestURL := "https://api.tavily.com/search?max_results=5&query=" + neturl.QueryEscape(searchTerm)
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := httpGetJSON(client, requestURL, map[string]string{"Authorization": "Bearer " + apiKey}, &payload); err != nil {
		return nil, "", err
	}
	references := make([]*agentv1.WebSearchReference, 0, len(payload.Results))
	for _, item := range payload.Results {
		title := strings.TrimSpace(item.Title)
		link := strings.TrimSpace(item.URL)
		if title == "" || link == "" {
			continue
		}
		references = append(references, &agentv1.WebSearchReference{
			Title: title,
			Url:   link,
			Chunk: strings.TrimSpace(item.Content),
		})
	}
	if len(references) == 0 {
		return nil, "", fmt.Errorf("web search returned no parseable results")
	}
	references = limitWebSearchReferences(references)
	return references, formatWebSearchPayload(searchTerm, references), nil
}

// duckDuckGoRelatedTopic 映射 DuckDuckGo Instant Answer JSON 的 RelatedTopics 条目。
// 条目有两种形态：叶子（有 Text+FirstURL）或分支（有 Topics 嵌套子条目）。
// Topic 字段名对所有层级相同（DuckDuckGo 复用同一结构），故用自引用切片递归。
type duckDuckGoRelatedTopic struct {
	Text     string                   `json:"Text"`
	FirstURL string                   `json:"FirstURL"`
	Topics   []duckDuckGoRelatedTopic `json:"Topics"`
	// RelatedTopics 嵌套（某些条目把子项放在 RelatedTopics 而非 Topics）。
	RelatedTopics []duckDuckGoRelatedTopic `json:"RelatedTopics"`
}

type duckDuckGoInstantAnswerPayload struct {
	AbstractText  string                   `json:"AbstractText"`
	AbstractURL   string                   `json:"AbstractURL"`
	Heading       string                   `json:"Heading"`
	RelatedTopics []duckDuckGoRelatedTopic `json:"RelatedTopics"`
}

// parseDuckDuckGoInstantAnswer 解析 Instant Answer JSON 为 WebSearchReference 列表。
// 抽出为纯函数便于单测（与 HTTP 解耦）。
//
// 提取顺序：
//  1. 若有 AbstractText+AbstractURL，作为首条（摘要类 IA，通常是 Wikipedia 摘要）。
//  2. 递归遍历 RelatedTopics（含嵌套 Topics/RelatedTopics），取 Text+FirstURL 叶子。
//     Text 形如 "Title - snippet"，按首个 " - " 拆为标题与 snippet；无分隔则整段做 snippet、URL host 做标题。
func parseDuckDuckGoInstantAnswer(body []byte) []*agentv1.WebSearchReference {
	var payload duckDuckGoInstantAnswerPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	references := make([]*agentv1.WebSearchReference, 0, 8)
	// Abstract 摘要作为首条。
	if strings.TrimSpace(payload.AbstractText) != "" && strings.TrimSpace(payload.AbstractURL) != "" {
		title := strings.TrimSpace(payload.Heading)
		if title == "" {
			title = hostFromURL(payload.AbstractURL)
		}
		references = append(references, &agentv1.WebSearchReference{
			Title: title,
			Url:   strings.TrimSpace(payload.AbstractURL),
			Chunk: strings.TrimSpace(payload.AbstractText),
		})
	}
	// 递归收集叶子条目。
	collectDuckDuckGoTopics(payload.RelatedTopics, &references)
	if len(references) == 0 {
		return nil
	}
	// 去重（同一 URL 可能出现在 Abstract + RelatedTopics）。
	return dedupeWebSearchReferences(references)
}

// collectDuckDuckGoTopics 递归收集叶子 Topic（有 Text+FirstURL），跳过纯分支节点。
// 同时探测嵌套 Topics 与 RelatedTopics 两种容器（DuckDuckGo 两种写法都出现过）。
func collectDuckDuckGoTopics(topics []duckDuckGoRelatedTopic, out *[]*agentv1.WebSearchReference) {
	for _, topic := range topics {
		text := strings.TrimSpace(topic.Text)
		url := strings.TrimSpace(topic.FirstURL)
		if text != "" && url != "" {
			title, snippet := splitDuckDuckGoTopicText(text, url)
			*out = append(*out, &agentv1.WebSearchReference{
				Title: title,
				Url:   url,
				Chunk: snippet,
			})
		}
		// 递归两种嵌套容器。
		if len(topic.Topics) > 0 {
			collectDuckDuckGoTopics(topic.Topics, out)
		}
		if len(topic.RelatedTopics) > 0 {
			collectDuckDuckGoTopics(topic.RelatedTopics, out)
		}
	}
}

// splitDuckDuckGoTopicText 把 DuckDuckGo 的 "Title - snippet" 文本拆为标题与 snippet。
// 无 " - " 分隔时：整段做 snippet，URL host 做标题。
func splitDuckDuckGoTopicText(text, url string) (title, snippet string) {
	if idx := strings.Index(text, " - "); idx > 0 {
		return strings.TrimSpace(text[:idx]), strings.TrimSpace(text[idx+3:])
	}
	return hostFromURL(url), text
}

// hostFromURL 从 URL 提取 host 做兜底标题；解析失败回退原 URL。
func hostFromURL(rawURL string) string {
	parsed, err := neturl.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || parsed.Host == "" {
		return strings.TrimSpace(rawURL)
	}
	return parsed.Host
}

// dedupeWebSearchReferences 按 URL 去重，保留首次出现项。
func dedupeWebSearchReferences(references []*agentv1.WebSearchReference) []*agentv1.WebSearchReference {
	seen := make(map[string]struct{}, len(references))
	deduped := references[:0]
	for _, ref := range references {
		if ref == nil {
			continue
		}
		key := strings.TrimSpace(ref.Url)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, ref)
	}
	return deduped
}
