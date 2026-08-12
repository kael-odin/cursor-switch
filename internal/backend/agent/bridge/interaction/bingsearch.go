// bingsearch.go 实现 WebSearch 的免 key 必应 HTML 抓取上游（免 key 链的成员之一）。
//
// 背景：WebToolsConfig 的 "bing" provider 原本只走官方 Web Search API v7（BYOK，需
// Ocp-Apim-Subscription-Key）。免 key 必应 HTML 抓取给「bing 优先」场景一个免 key 选择——
// 有 key 仍走官方 API（质量更高，见 executeBingSearch），无 key 时回退 HTML 抓取并作为
// 免 key 链首（见 executeFreeSearchChain / dispatchWebSearch 的 bing 双模分派）。
//
// 选择器移植自 amadeus（D:\New-Files\amadeus.html parseBingSearch）：
// li.b_algo → h2 a（标题/URL）、.b_caption p / .b_snippet（摘要，截 300 字符）。
// 解析失败返回错误让免 key 链继续试下一引擎，绝不静默返回空。
package interaction

import (
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"cursor/gen/agentv1"
	"cursor/internal/netproxy"
)

const (
	bingWebSearchBaseURL = "https://www.bing.com/search?q="
	bingWebSearchHostURL = "https://www.bing.com"
)

// executeBingHTMLSearch 抓取必应搜索结果页并解析引用列表（免 key）。
func executeBingHTMLSearch(client *http.Client, searchTerm string) ([]*agentv1.WebSearchReference, string, error) {
	if strings.TrimSpace(searchTerm) == "" {
		return nil, "", fmt.Errorf("web search search_term is required")
	}
	if client == nil {
		client = netproxy.NewHTTPClient(15 * time.Second)
	}
	requestURL := bingWebSearchBaseURL + neturl.QueryEscape(searchTerm)
	request, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	response, err := client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", fmt.Errorf("bing http status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if err != nil {
		return nil, "", err
	}
	references := parseBingHTMLReferences(string(body))
	if len(references) == 0 {
		return nil, "", fmt.Errorf("bing returned no parseable results")
	}
	references = limitWebSearchReferences(references)
	return references, formatWebSearchPayload(searchTerm, references), nil
}

// parseBingHTMLReferences 从必应搜索结果页解析 li.b_algo 条目（标题/URL/摘要）。
// 选择器移植自 amadeus parseBingSearch；摘要截 webSearchAbstractLimit 字符。
// 纯函数便于单测（与 HTTP 解耦）。
func parseBingHTMLReferences(body string) []*agentv1.WebSearchReference {
	document, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		return nil
	}
	references := make([]*agentv1.WebSearchReference, 0, 8)
	document.Find("li.b_algo").Each(func(_ int, selection *goquery.Selection) {
		anchor := selection.Find("h2 a").First()
		title := cleanWebSearchText(anchor.Text())
		url := normalizeBingSearchURL(anchor.AttrOr("href", ""))
		if title == "" || url == "" {
			return
		}
		abstract := ""
		if snippet := selection.Find(".b_caption p, .b_snippet").First(); snippet != nil {
			abstract = truncateWebSearchAbstract(snippet.Text(), webSearchAbstractLimit)
		}
		references = append(references, &agentv1.WebSearchReference{
			Title: title,
			Url:   url,
			Chunk: abstract,
		})
	})
	return references
}

// normalizeBingSearchURL 把必应返回的协议省略/相对链接归一化为绝对 URL
// （对齐 amadeus normalizeSearchUrl 对必应 href 的处理，防畸形引用进 payload）。
func normalizeBingSearchURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	if strings.HasPrefix(rawURL, "//") {
		return "https:" + rawURL
	}
	if strings.HasPrefix(rawURL, "/") {
		return bingWebSearchHostURL + rawURL
	}
	return rawURL
}
