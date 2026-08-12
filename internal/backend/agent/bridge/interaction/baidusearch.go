// baidusearch.go 实现 WebSearch 的百度搜索上游（免 key，免 key 链的成员之一）。
//
// 审计「行为偏离-3」补充："baidu" 作为免 key 可选项接入，对中文用户结果质量/时效性更好。
// 百度失败或无结果时，由 websearch_provider.go 的免 key 链式回退接管（执行顺序经
// freeWebSearchChain 由 provider 决定，链尾固定 duckduckgo → bing → baidu），
// 不再在此处内联单级回退——避免链上重复尝试同一引擎。
package interaction

import (
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"

	"cursor/gen/agentv1"
)

const (
	baiduWebSearchBaseURL     = "https://www.baidu.com/s?ie=utf-8&tn=baidu&wd="
	baiduWebSearchHostURL     = "https://www.baidu.com"
	baiduSearchReferenceLimit = 8
)

// tryBaiduWebSearch 抓取百度搜索结果页并解析出引用列表。
// 由免 key 链的 executeFreeSearchEngine 直接调用（不再有单级回退包装）。
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
			Chunk: truncateWebSearchAbstract(abstract, webSearchAbstractLimit),
		})
		return true
	})
	return references
}

// extractBaiduSearchResult 从单条百度搜索结果节点中提取标题、链接和摘要。
func extractBaiduSearchResult(selection *goquery.Selection) (string, string, string) {
	title := cleanWebSearchText(selection.Find("h3").First().Text())
	resultURL, _ := selection.Find("h3 a").First().Attr("href")
	if title == "" {
		title = firstBaiduSearchLine(selection.Text())
	}
	if resultURL == "" {
		resultURL, _ = selection.Find("a").First().Attr("href")
	}
	abstract := cleanWebSearchText(selection.Find(".c-abstract").First().Text())
	if abstract == "" {
		abstract = cleanWebSearchText(selection.ChildrenFiltered("div").First().Text())
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
//
// 每个引用的解析独立——只写自己那一个元素的 Url，无共享写，并发安全；并行发起可消除
// N 次串行 HEAD/GET（各 ≤6s 超时）的累积延迟，最坏 N*12s 坍缩到 12s。http.Client 设计为
// 并发安全，可跨 goroutine 共用；信号量限并发（≤4），引用数量异常大时不爆 goroutine。
func resolveBaiduWebSearchRedirects(client *http.Client, references []*agentv1.WebSearchReference) {
	const maxConcurrentBaiduRedirects = 4
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentBaiduRedirects)
	for _, reference := range references {
		if reference == nil {
			continue
		}
		wg.Add(1)
		go func(ref *agentv1.WebSearchReference) {
			defer wg.Done()
			sem <- struct{}{} // 占一个并发槽
			defer func() { <-sem }()
			ref.Url = resolveBaiduRedirectURL(client, ref.GetUrl())
		}(reference)
	}
	wg.Wait()
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

// firstBaiduSearchLine 返回文本中第一个非空行。
func firstBaiduSearchLine(value string) string {
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r", "\n"), "\n") {
		line = cleanWebSearchText(line)
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
		line = cleanWebSearchText(line)
		if line != "" {
			nonEmpty = append(nonEmpty, line)
		}
	}
	if len(nonEmpty) <= 1 {
		return ""
	}
	return cleanWebSearchText(strings.Join(nonEmpty[1:], " "))
}
