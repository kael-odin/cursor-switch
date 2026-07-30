package upstream

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cursor/internal/backend/server"
	legacyruntime "cursor/internal/runtime"
)

const (
	HeaderRawServerURL = server.HeaderServerUpstreamURL

	// upstreamRedirectClientTimeout 是 CredentialOriginalCursor 转发自建 no-redirect 客户端时
	// 使用的超时。Host 在 rebuildLocked 注入的普通客户端用 30s，此处对齐，避免官方控制面转发
	// 因无超时而永久挂起。
	upstreamRedirectClientTimeout = 30 * time.Second

	// upstreamStreamHeaderTimeout 是流式 CredentialOriginalCursor 客户端（run_sse/bidi_append
	// 切 upstream）的「响应头等待」超时。流式响应体本身不受超时约束（推理可数分钟），仅约束
	// 首字节响应头到达前的等待，防止上游挂起时无限阻塞。30s 足以覆盖建连后到首字节的正常延迟。
	upstreamStreamHeaderTimeout = 30 * time.Second
)

type SystemSettingService interface {
	ResolveModelAdapters(context.Context) ([]legacyruntime.ModelAdapterConfig, error)
}

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type Dependencies struct {
	SystemSettingService SystemSettingService
	HTTPClient           HTTPClient
	LogRoot              string
	Routes               []Route
}

type RequestContext struct {
	ResponseWriter http.ResponseWriter
	Request        *http.Request
	StartedAt      time.Time
	RawURL         string
	TargetURL      *url.URL
	Method         string
	Headers        http.Header
	ContentType    string
	RequestBody    []byte
	Mode           server.ExecutionMode
	Deps           *Dependencies
	HTTPRequestID  string
	// Credentials 是 backend 中间件捕获的 Cursor 真实凭证，供 CredentialOriginalCursor 恢复。
	Credentials server.CapturedCredentials
}

// CredentialPolicy 决定出站上游请求如何携带鉴权凭证。
type CredentialPolicy uint8

const (
	// CredentialNone 默认策略：不携带任何 Cursor 凭证（Authorization / Cookie / checksum 均剥离）。
	// 适用于本地模型/数据面、第三方 provider、tab 服务等，防止凭证泄漏。
	CredentialNone CredentialPolicy = iota
	// CredentialOriginalCursor：恢复 backend 捕获的 Cursor 真实凭证，
	// 仅当最终上游目标为原始 HTTPS *.cursor.sh 且与凭证绑定目标一致时生效。
	// 用于官方账号/marketplace/customize 等控制面接口的登录态透传。
	CredentialOriginalCursor
)

type ForwardOptions struct {
	BodyOverride []byte
	PatchHeaders func(headers http.Header)
	// Credential 指定出站凭证策略。默认 CredentialNone。
	Credential CredentialPolicy
	// Stream 标记流式响应（SSE / Connect 流）。CredentialOriginalCursor + Stream 时
	// 改用无端到端超时的流式 NoRedirect 客户端，防 http.Client.Timeout 截断长流。
	Stream bool
}

type ForwardMeta struct {
	StatusCode   int
	Status       string
	ContentType  string
	ResponseSize int64
}

type Matcher interface {
	Match(path string) bool
}

type Exact string

func (m Exact) Match(path string) bool { return path == string(m) }

type Prefix string

func (m Prefix) Match(path string) bool {
	value := string(m)
	return value != "" && strings.HasPrefix(path, value)
}

type Wildcard struct{}

func (Wildcard) Match(string) bool { return true }

type RouteHandler func(reqCtx *RequestContext, route *Route) error

type Route struct {
	Name               string
	Pattern            string
	Matcher            Matcher
	ConsoleLog         bool
	StatusCode         int
	JSONBody           map[string]any
	MockProtoType      string
	MockPayloadBuilder func(*RequestContext) (map[string]any, error)
	Handler            RouteHandler
}

func BuildChannelCallError(statusCode int, forwardErr error) (string, string) {
	if forwardErr != nil {
		return "UPSTREAM_REQUEST_FAILED", strings.TrimSpace(forwardErr.Error())
	}
	if statusCode >= 200 && statusCode < 300 {
		return "", ""
	}
	if statusCode <= 0 {
		return "UPSTREAM_STATUS_UNKNOWN", ""
	}
	return "UPSTREAM_STATUS_" + strconv.Itoa(statusCode), ""
}

func ReadStringAny(data map[string]any, keys ...string) string {
	if data == nil {
		return ""
	}
	for _, key := range keys {
		value, ok := data[key]
		if !ok || value == nil {
			continue
		}
		if text, ok := value.(string); ok {
			return text
		}
	}
	return ""
}

func ReadMapAny(data map[string]any, keys ...string) map[string]any {
	if data == nil {
		return nil
	}
	for _, key := range keys {
		value, ok := data[key]
		if !ok || value == nil {
			continue
		}
		if mapped, ok := value.(map[string]any); ok {
			return mapped
		}
	}
	return nil
}

func CloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
