package interaction

import (
	"errors"
	"testing"

	"cursor/internal/safehttp"
)

// TestDispatchWebSearchMissingAPIKey 验证选了需 key 的 provider 但未填 key 时，
// 返回 errWebSearchAPIKeyMissing（缺 key 显式告警而非静默失败，审计「行为偏离-3」）。
func TestDispatchWebSearchMissingAPIKey(t *testing.T) {
	cases := []string{"bing", "serper", "tavily"}
	for _, provider := range cases {
		t.Run(provider, func(t *testing.T) {
			bridge := NewBridge(func() WebToolsConfig {
				return WebToolsConfig{WebSearchProvider: provider} // 无 key
			})
			_, _, err := bridge.dispatchWebSearch("test query")
			if !errors.Is(err, errWebSearchAPIKeyMissing) {
				t.Fatalf("provider=%s: expected errWebSearchAPIKeyMissing, got %v", provider, err)
			}
		})
	}
}

// TestDispatchWebSearchDuckDuckGoFallback 验证空 provider/非认可值回退 duckduckgo
// （不报缺 key 错，走免 key 降级路径）。
func TestDispatchWebSearchDuckDuckGoFallback(t *testing.T) {
	for _, provider := range []string{"", "unknown", "duckduckgo"} {
		t.Run("provider="+provider, func(t *testing.T) {
			bridge := NewBridge(func() WebToolsConfig {
				return WebToolsConfig{WebSearchProvider: provider}
			})
			// duckduckgo 路径会真发 HTTP，必然失败（无网/非 2xx）；
			// 这里只断言它不返回缺 key 错误（说明走了 duckduckgo 分支）。
			_, _, err := bridge.dispatchWebSearch("test query")
			if errors.Is(err, errWebSearchAPIKeyMissing) {
				t.Fatalf("provider=%s: should not report missing API key for duckduckgo fallback", provider)
			}
			// err 非 nil 是预期的（网络不可达），但不应是缺 key 类。
			_ = err
		})
	}
}

// TestParseBingResults 验证 Bing JSON 解析：标题/URL/snippet 抽取 + 空/缺字段过滤。
// 解析与 HTTP 解耦（HTTP 请求逻辑由 httpGetJSON 覆盖，解析纯函数单测）。
func TestParseBingResults(t *testing.T) {
	items := []struct {
		Name    string `json:"name"`
		URL     string `json:"url"`
		Snippet string `json:"snippet"`
	}{
		{"Result One", "https://example.com/1", "first snippet"},
		{"", "https://example.com/empty-title", "should skip empty title"},
		{"Result Three", "", "should skip empty url"},
		{"Result Two", "https://example.com/2", "second snippet"},
	}
	refs := parseBingReferences(items, "hello")
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs after filtering empties, got %d", len(refs))
	}
	if refs[0].Title != "Result One" || refs[0].Url != "https://example.com/1" || refs[0].Chunk != "first snippet" {
		t.Fatalf("refs[0] mismatch: %+v", refs[0])
	}
	if refs[1].Title != "Result Two" {
		t.Fatalf("refs[1] mismatch: %+v", refs[1])
	}
}

// TestWebFetchHostAllowlistResolve 验证白名单 host 让 safehttp 放行内网解析路径。
// 命中白名单的 host，ResolveAndValidateHost 不再因"非公网"拒绝（即便解析失败也走原 DNS 错误，
// 不报 SSRF 拒绝）。这里用字面私网 IP 验证：白名单内放行，白名单外拒绝。
func TestWebFetchHostAllowlistResolve(t *testing.T) {
	// 字面私网 IP，默认拒绝。
	safehttp.SetHostAllowlist(nil)
	if _, err := safehttp.ResolveAndValidateHost("192.168.1.1"); err == nil {
		t.Fatal("private IP should be rejected without allowlist")
	}
	// 白名单加入该 host（字面 IP 形式），应放行。
	safehttp.SetHostAllowlist([]string{"192.168.1.1"})
	if _, err := safehttp.ResolveAndValidateHost("192.168.1.1"); err != nil {
		t.Fatalf("allowlisted private IP should pass, got %v", err)
	}
	// 清空白名单后恢复拒绝。
	safehttp.SetHostAllowlist(nil)
	if _, err := safehttp.ResolveAndValidateHost("192.168.1.1"); err == nil {
		t.Fatal("private IP should be rejected after allowlist cleared")
	}
}

// TestWebFetchHostAllowlistValidation 验证 validateWebFetchURL 在白名单生效时放行内网字面 host。
func TestWebFetchHostAllowlistValidation(t *testing.T) {
	safehttp.SetHostAllowlist([]string{"wiki.internal.corp"})
	defer safehttp.SetHostAllowlist(nil)
	// 白名单 host：URL 合法（不因"非公网"被 isBlockedWebFetchHost 拒）。
	// 注意：resolveAndValidateHost 会真解析 DNS，内网域名解析可能失败——
	// 此处只断言不被 isBlockedWebFetchHost 的字面拒绝挡住（错误若出现应是 DNS 类）。
	parsed, err := validateWebFetchURL("https://wiki.internal.corp/page")
	if err != nil {
		// 内网域名在 CI 解析失败是预期的；只要错误不是"not public-web accessible"字面拒绝即可。
		if msg := err.Error(); containsString(msg, "not public-web accessible") {
			t.Fatalf("allowlisted host should not hit public-web rejection, got %q", msg)
		}
	} else if parsed.Hostname() != "wiki.internal.corp" {
		t.Fatalf("parsed host mismatch: %s", parsed.Hostname())
	}
}

func containsString(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return len(needle) == 0
}

// TestParseDuckDuckGoInstantAnswer 验证 DuckDuckGo Instant Answer JSON 解析：
//   - AbstractText+AbstractURL 作为首条（标题取 Heading，回退 host）
//   - RelatedTopics 叶子项按 " - " 拆标题/snippet
//   - 嵌套 Topics/RelatedTopics 递归收集
//   - 同 URL 去重
func TestParseDuckDuckGoInstantAnswer(t *testing.T) {
	body := []byte(`{
		"Heading": "Go (programming language)",
		"AbstractText": "Go is a statically typed, compiled language.",
		"AbstractURL": "https://en.wikipedia.org/wiki/Go",
		"RelatedTopics": [
			{"Text": "The Go Blog - official blog", "FirstURL": "https://go.dev/blog/"},
			{"Text": "", "FirstURL": "", "Topics": [
				{"Text": "A Tour of Go - interactive tour", "FirstURL": "https://go.dev/tour/"},
				{"Text": "no separator here", "FirstURL": "https://example.com/no-sep"}
			]},
			{"Text": "duplicate url", "FirstURL": "https://go.dev/blog/"}
		]
	}`)
	refs := parseDuckDuckGoInstantAnswer(body)
	if refs == nil {
		t.Fatal("expected non-nil refs")
	}
	// Abstract + blog + tour + no-sep = 4（blog 重复项去重）。
	if len(refs) != 4 {
		t.Fatalf("expected 4 refs after dedupe, got %d", len(refs))
	}
	// 首条是 Abstract。
	if refs[0].Title != "Go (programming language)" || refs[0].Url != "https://en.wikipedia.org/wiki/Go" {
		t.Fatalf("abstract ref mismatch: %+v", refs[0])
	}
	// " - " 拆分：标题 = "The Go Blog"，snippet = "official blog"。
	if refs[1].Title != "The Go Blog" || refs[1].Chunk != "official blog" {
		t.Fatalf("blog ref title/snippet split mismatch: %+v", refs[1])
	}
	// 嵌套 Topics 收集到位。
	if refs[2].Url != "https://go.dev/tour/" {
		t.Fatalf("nested tour ref missing: %+v", refs[2])
	}
	// 无 " - " 分隔：标题回退 host。
	if refs[3].Title != "example.com" || refs[3].Chunk != "no separator here" {
		t.Fatalf("no-sep fallback mismatch: title=%q chunk=%q", refs[3].Title, refs[3].Chunk)
	}
}

// TestParseDuckDuckGoInstantAnswerEmpty 验证空/无效 JSON 返回 nil（触发回退 HTML）。
func TestParseDuckDuckGoInstantAnswerEmpty(t *testing.T) {
	if refs := parseDuckDuckGoInstantAnswer([]byte(`{}`)); refs != nil {
		t.Fatalf("empty payload should return nil, got %d refs", len(refs))
	}
	if refs := parseDuckDuckGoInstantAnswer([]byte(`not json`)); refs != nil {
		t.Fatalf("invalid json should return nil, got %d refs", len(refs))
	}
}

// TestParseDuckDuckGoInstantAnswerHeadingFallbackToHost 验证无 Heading 时标题回退 host。
func TestParseDuckDuckGoInstantAnswerHeadingFallbackToHost(t *testing.T) {
	body := []byte(`{
		"AbstractText": "abstract without heading",
		"AbstractURL": "https://docs.example.org/page"
	}`)
	refs := parseDuckDuckGoInstantAnswer(body)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Title != "docs.example.org" {
		t.Fatalf("expected host fallback title, got %q", refs[0].Title)
	}
}
