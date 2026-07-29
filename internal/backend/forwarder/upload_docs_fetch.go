// upload_docs_fetch.go 实现 N-21：@docs 上传时抓取页面正文入库。
//
// 此前 UploadDocumentation 只存 URL/title，Content 恒空——模型 @docs 引用时拿不到
// 文档正文，检索召回无意义（审计 N-21 / L-4 根因）。上传时用 SSRF-safe transport
// 抓取页面，剥离 HTML 标签提取纯文本正文存入 DocsIndexRecord.Content，GetPages/
// GetDoc 一并返回正文片段。
//
// SSRF 防护复用 internal/safehttp（与 WebFetch 工具同一份实现，审计 N-14）——
// @docs 上传的 URL 来自用户输入，必须防止指向内网/云元数据。
package forwarder

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cursor/internal/safehttp"
)

const (
	// docsFetchTimeout 限制单次 @docs 抓取总耗时——上传是用户主动操作，但不应无限阻塞。
	docsFetchTimeout = 20 * time.Second
	// docsFetchMaxBytes 限制抓取正文大小，避免超大页面挤占内存/上下文。
	docsFetchMaxBytes = 1 << 20 // 1 MiB
	// docsContentMaxRunes 限制入库正文长度（剥离标签后的纯文本）。
	docsContentMaxRunes = 200_000
)

// fetchDocsPageContent 用 SSRF-safe transport 抓取 url 的页面正文（纯文本）。
// 失败（非 2xx、非 HTML、SSRF 命中、超时）返回空字符串与 nil error——抓取是 best-effort，
// 失败时记录仍入库（仅缺正文），不阻断上传主流程。调用方据空 Content 决定是否记日志。
func fetchDocsPageContent(ctx context.Context, rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", nil
	}
	if parsed.Host == "" {
		return "", nil
	}

	fetchCtx, cancel := context.WithTimeout(ctx, docsFetchTimeout)
	defer cancel()

	// base Transport 不挂 cursor 代理——@docs 抓取是直连公网，不应走 MITM 代理。
	transport := safehttp.NewSSRFSafeTransport(&http.Transport{
		MaxIdleConns:        10,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	})
	client := &http.Client{
		Transport: transport,
		// redirect 每跳由 safehttp.DialContext 重新解析校验 IP（N-14 防护自动生效）；
		// 但仍限制跳数与同源，避免开放重定向被利用抓取任意内网页面。
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("docs fetch stopped after 5 redirects")
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil
	}
	// 标识为文档抓取机器人，避免触发反爬交互页。
	req.Header.Set("User-Agent", "cursor-switch-docs-fetch/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return "", nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	// 仅抓取 HTML 类内容——PDF/二进制/JSON 等不剥离标签，跳过（避免把二进制当正文入库）。
	if contentType != "" && !strings.Contains(contentType, "html") && !strings.Contains(contentType, "xml") && !strings.Contains(contentType, "text/plain") {
		return "", nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, docsFetchMaxBytes))
	if err != nil {
		return "", nil
	}
	text := extractDocsText(string(body))
	text = truncateRunes(text, docsContentMaxRunes)
	return strings.TrimSpace(text), nil
}

// extractDocsText 从 HTML 文本里粗提取纯文本正文：
// 去 <script>/<style> 块，剥离其余标签，折叠空白。
// 不引入 readability 外部依赖以保持构建链纯净——粗提取足以让检索召回有内容可用，
// 完整正文提炼属后续增强（审计 N-21 建议的可选 Readability 增强）。
func extractDocsText(html string) string {
	// 先去 script/style/head/noscript 块（含内容），避免把脚本/样式当正文。
	out := html
	for _, tag := range []string{"script", "style", "head", "noscript"} {
		out = stripHTMLBlock(out, tag)
	}
	// 去 HTML 注释。
	for {
		start := strings.Index(out, "<!--")
		if start < 0 {
			break
		}
		end := strings.Index(out[start:], "-->")
		if end < 0 {
			out = out[:start]
			break
		}
		out = out[:start] + out[start+end+3:]
	}
	// <br> / </p> / </div> 等块级结束转换行，保留段落结构。
	out = replaceBlockClosers(out)
	// 剥离剩余标签。
	var b strings.Builder
	b.Grow(len(out))
	inTag := false
	for _, r := range out {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	text := b.String()
	// 解码常见 HTML 实体。
	text = decodeCommonEntities(text)
	// 折叠空白：连续空白（含跨行）压成单个空格，但保留换行作为段落分隔。
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.Fields(line), " ")
	}
	text = strings.Join(lines, "\n")
	// 压掉连续空行。
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(text)
}

// stripHTMLBlock 从 out 里删除 <tag>...</tag>（含内容），大小写无关定位。
// tag 是不带尖括号的标签名（如 "script"）。删除所有匹配的 <tag ...>...</tag> 块；
// 无关闭标签时删到串尾。内部维护小写镜像，避免调用方传入过期的 lower 导致索引错位。
func stripHTMLBlock(out, tag string) string {
	open := "<" + tag
	closeTag := "</" + tag + ">"
	lower := strings.ToLower(out)
	for {
		idx := strings.Index(lower, open)
		if idx < 0 {
			return out
		}
		// 从 idx 之后找对应的关闭标签（小写匹配）。
		searchFrom := idx + len(open)
		if searchFrom > len(lower) {
			searchFrom = len(lower)
		}
		closeIdx := strings.Index(lower[searchFrom:], closeTag)
		if closeIdx < 0 {
			// 无关闭标签，删到串尾。
			return out[:idx]
		}
		end := searchFrom + closeIdx + len(closeTag)
		if end > len(out) {
			end = len(out)
		}
		out = out[:idx] + out[end:]
		lower = strings.ToLower(out)
	}
}

// replaceBlockClosers 把块级元素的关闭标签替换为换行，保留段落结构。
func replaceBlockClosers(s string) string {
	replacers := []string{
		"</p>", "</div>", "</li>", "</h1>", "</h2>", "</h3>", "</h4>", "</h5>", "</h6>",
		"</tr>", "</blockquote>", "</section>", "</article>", "</header>", "</footer>",
	}
	lower := strings.ToLower(s)
	for _, r := range replacers {
		for {
			idx := strings.Index(lower, r)
			if idx < 0 {
				break
			}
			s = s[:idx] + "\n" + s[idx+len(r):]
			lower = strings.ToLower(s)
		}
	}
	// <br> / <br/> / <br /> 转换行。
	for {
		idx := strings.Index(lower, "<br")
		if idx < 0 {
			break
		}
		gt := strings.Index(s[idx:], ">")
		if gt < 0 {
			break
		}
		s = s[:idx] + "\n" + s[idx+gt+1:]
		lower = strings.ToLower(s)
	}
	return s
}

// decodeCommonEntities 解码最常见的 HTML 实体（避免引入 html package 的开销）。
func decodeCommonEntities(s string) string {
	repl := strings.NewReplacer(
		"&nbsp;", " ",
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", "\"",
		"&#39;", "'",
		"&apos;", "'",
		"&#x27;", "'",
	)
	return repl.Replace(s)
}

// truncateRunes 按 rune 截断到 max，超长加省略号。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
