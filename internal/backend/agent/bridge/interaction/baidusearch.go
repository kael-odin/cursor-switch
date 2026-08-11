// baidusearch.go 实现 WebSearch 的百度搜索上游（免 key）。
//
// 审计「行为偏离-3」补充：duckduckgo 仍是默认免 key 路径；"baidu" 作为可选项接入，
// 对中文用户结果质量/时效性更好。百度失败或无结果时自动回退 DuckDuckGo（与上游 556ea21 一致）。
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
	baiduWebSearchBaseURL     = "https://www.baidu.com/s?ie=utf-8&tn=baidu&wd="
	baiduWebSearchHostURL     = "https://www.baidu.com"
	baiduSearchAbstractLimit  = 300
	baiduSearchReferenceLimit = 8
)

// executeBaiduSearch 是 "baidu" provider 的入口：先尝试百度搜索，失败或无结果时
// 回退 DuckDuckGo（免 key 兜底），保证搜索工具始终可用。
func (bridge *Bridge) executeBaiduSearch(searchTerm string) ([]*agentv1.WebSearchReference, string, error) {
	if strings.TrimSpace(searchTerm) == "" {
		return nil, "", fmt.Errorf("web search search_term is required")
	}
	client := bridge.httpClient
	if client == nil {
		client = netproxy.NewHTTPClient(15 * time.Second)
	}
	if references, payload, err := tryBaiduWebSearch(client, searchTerm); err == nil && len(references) > 0 {
		return references, payload, nil
	}
	return bridge.executeDuckDuckGoSearch(searchTerm)
}

// tryBaiduWebSearch 抓取百度搜索结果页并解析出引用列表。
func tryBaiduWebSearch(client *http.Client, searchTerm string) ([]*agentv1.WebSearchReference, string, error) {
	requestURL := baiduWebSearchBaseURL + neturl.QueryEscape(searchTerm)
	request, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/68.0.3440.106 Safari/537.36")
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,image/apng,*/*;q=0.8")
	request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	request.Header.Set("Referer", baiduWebSearchHostURL+"/")
	response, err := client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", fmt.Errorf("baidu http status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if err != nil {
		return nil, "", err
	}
	references := extractBaiduWebSearchReferences(string(body))
	if len(references) == 0 {
		return nil, "", fmt.Errorf("baidu returned no parseable results")
	}
	if len(references) > 5 {
		references = references[:5]
	}
	resolveBaiduWebSearchRedirects(client, references)
	return references, formatWebSearchPayload(searchTerm, references), nil
}

// extractBaiduWebSearchReferences 从百度搜索结果页 HTML 中解析出搜索结果列表。
func extractBaiduWebSearchReferences(body string) []*agentv1.WebSearchReference {
	document, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		return nil
	}
	references := make([]*agentv1.WebSearchReference, 0, baiduSearchReferenceLimit)
	document.Find("#content_left > div").EachWithBreak(func(_ int, selection *goquery.Selection) bool {
		if len(references) >= baiduSearchReferenceLimit {
			return false
		}
		if !selection.HasClass("c-container") {
			return true
		}
		title, resultURL, abstract := extractBaiduSearchResult(selection)
		if title == "" || resultURL == "" {
			return true
		}
		references = append(references, &agentv1.WebSearchReference{
			Title: title,
			Url:   normalizeBaiduSearchURL(resultURL),
			Chunk: truncateBaiduSearchAbstract(abstract),
		})
		return true
	})
	return references
}

// extractBaiduSearchResult 从单条百度搜索结果节点中提取标题、链接和摘要。
func extractBaiduSearchResult(selection *goquery.Selection) (string, string, string) {
	title := cleanBaiduSearchText(selection.Find("h3").First().Text())
	resultURL, _ := selection.Find("h3 a").First().Attr("href")
	if title == "" {
		title = firstBaiduSearchLine(selection.Text())
	}
	if resultURL == "" {
		resultURL, _ = selection.Find("a").First().Attr("href")
	}
	abstract := cleanBaiduSearchText(selection.Find(".c-abstract").First().Text())
	if abstract == "" {
		abstract = cleanBaiduSearchText(selection.ChildrenFiltered("div").First().Text())
	}
	if abstract == "" {
		abstract = baiduSearchTextAfterFirstLine(selection.Text())
	}
	return title, strings.TrimSpace(resultURL), abstract
}

// normalizeBaiduSearchURL 把百度返回的相对或协议省略链接归一化为绝对 URL。
func normalizeBaiduSearchURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, "//") {
		return "https:" + rawURL
	}
	if strings.HasPrefix(rawURL, "/") {
		return baiduWebSearchHostURL + rawURL
	}
	return rawURL
}

// resolveBaiduWebSearchRedirects 把百度跳转链接解析为最终目标地址，就地更新引用列表。
func resolveBaiduWebSearchRedirects(client *http.Client, references []*agentv1.WebSearchReference) {
	for _, reference := range references {
		if reference == nil {
			continue
		}
		reference.Url = resolveBaiduRedirectURL(client, reference.GetUrl())
	}
}

// resolveBaiduRedirectURL 判断链接是否是百度跳转链接，并尝试解析出真实目标。
func resolveBaiduRedirectURL(client *http.Client, rawURL string) string {
	resultURL := normalizeBaiduSearchURL(rawURL)
	if !isBaiduRedirectURL(resultURL) {
		return resultURL
	}
	redirectClient := baiduRedirectHTTPClient(client)
	if location := requestBaiduRedirectLocation(redirectClient, http.MethodHead, resultURL); location != "" {
		return location
	}
	if location := requestBaiduRedirectLocation(redirectClient, http.MethodGet, resultURL); location != "" {
		return location
	}
	return resultURL
}

// baiduRedirectHTTPClient 基于基础 client 构造一个不自动跟随重定向的短超时客户端。
func baiduRedirectHTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	client := *base
	if client.Timeout == 0 || client.Timeout > 6*time.Second {
		client.Timeout = 6 * time.Second
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &client
}

// requestBaiduRedirectLocation 发起一次请求并读取响应头中的重定向目标地址。
func requestBaiduRedirectLocation(client *http.Client, method string, rawURL string) string {
	request, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		return ""
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0.0.0 Safari/537.36")
	request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	request.Header.Set("Referer", baiduWebSearchHostURL+"/")
	response, err := client.Do(request)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	location := strings.TrimSpace(response.Header.Get("Location"))
	if location == "" {
		return ""
	}
	return resolveBaiduLocationURL(rawURL, location)
}

// resolveBaiduLocationURL 把响应头里的相对重定向地址解析为绝对地址。
func resolveBaiduLocationURL(baseURL string, location string) string {
	parsedLocation, err := neturl.Parse(location)
	if err != nil {
		return location
	}
	if parsedLocation.IsAbs() {
		return parsedLocation.String()
	}
	parsedBase, err := neturl.Parse(baseURL)
	if err != nil {
		return location
	}
	return parsedBase.ResolveReference(parsedLocation).String()
}

// isBaiduRedirectURL 判断给定地址是否是百度域名下的跳转链接。
func isBaiduRedirectURL(rawURL string) bool {
	parsedURL, err := neturl.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsedURL.Hostname())
	path := strings.ToLower(parsedURL.EscapedPath())
	return (host == "baidu.com" || strings.HasSuffix(host, ".baidu.com")) && strings.HasPrefix(path, "/link")
}

// truncateBaiduSearchAbstract 按字符数截断摘要文本，避免结果过长。
func truncateBaiduSearchAbstract(value string) string {
	value = cleanBaiduSearchText(value)
	if baiduSearchAbstractLimit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= baiduSearchAbstractLimit {
		return value
	}
	return string(runes[:baiduSearchAbstractLimit])
}

// firstBaiduSearchLine 返回文本中第一个非空行。
func firstBaiduSearchLine(value string) string {
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r", "\n"), "\n") {
		line = cleanBaiduSearchText(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// baiduSearchTextAfterFirstLine 返回除第一个非空行外剩余文本的拼接结果。
func baiduSearchTextAfterFirstLine(value string) string {
	nonEmpty := make([]string, 0, 8)
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r", "\n"), "\n") {
		line = cleanBaiduSearchText(line)
		if line != "" {
			nonEmpty = append(nonEmpty, line)
		}
	}
	if len(nonEmpty) <= 1 {
		return ""
	}
	return cleanBaiduSearchText(strings.Join(nonEmpty[1:], " "))
}

// cleanBaiduSearchText 折叠多余空白并去除首尾空格。
func cleanBaiduSearchText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}
