package server

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const HeaderServerUpstreamURL = "X-Server-Upstream-URL"

type SourceKind string

const (
	SourceNative SourceKind = "native"
	SourceMITM   SourceKind = "mitm"
)

type ProtocolClass string

const (
	ProtocolHTTP          ProtocolClass = "http"
	ProtocolConnectUnary  ProtocolClass = "connect_unary"
	ProtocolConnectStream ProtocolClass = "connect_stream"
)

type Context struct {
	Writer    http.ResponseWriter
	Request   *http.Request
	RouteName string
	Source    SourceKind
	Protocol  ProtocolClass
	StartedAt time.Time

	UpstreamURL *url.URL
	Mode        ExecutionMode
	LastError   error

	// Credentials 保存从 MITM 转发请求中捕获的 Cursor 真实凭证。
	// 捕获后立即从 Request.Header 删除，本地 handler 不可见；
	// 仅 OriginalCursor 出站策略在校验目标后恢复给官方 cursor.sh。
	Credentials CapturedCredentials

	Logger *slog.Logger
}

// CapturedCredentials 是从 Cursor 客户端请求捕获、绑定到原始上游目标的真实凭证。
// 用 *Present 布尔区分「原本不存在」与「空值」，恢复时必须保持这一区别。
type CapturedCredentials struct {
	Authorization        string
	AuthorizationPresent bool
	Cookies              []string
	Checksum             string
	ChecksumPresent      bool
	// BoundTarget 是这些凭证绑定的规范化原始目标（scheme://host[:port]）。
	// 恢复凭证前必须校验最终上游目标与之一致，防止凭证泄漏到第三方。
	BoundTarget string
}

func newContext(writer http.ResponseWriter, request *http.Request, route Route) *Context {
	return &Context{
		Writer:    writer,
		Request:   request,
		RouteName: route.Name,
		Protocol:  route.Protocol,
		StartedAt: time.Now(),
		Logger:    slog.Default(),
		Mode:      ModeLocal,
	}
}

func (ctx *Context) ParseUpstreamURL() error {
	if ctx == nil || ctx.Request == nil {
		return nil
	}
	rawURL := strings.TrimSpace(ctx.Request.Header.Get(HeaderServerUpstreamURL))
	if rawURL == "" {
		ctx.Source = SourceNative
		ctx.UpstreamURL = nil
		return nil
	}
	parsed, err := ParseAndValidateRawURL(rawURL)
	if err != nil {
		return err
	}
	ctx.Source = SourceMITM
	ctx.UpstreamURL = parsed
	return nil
}

// CaptureAndStripCredentials 从当前请求头捕获 Cursor 真实凭证到 ctx.Credentials，
// 绑定到已解析的原始上游目标，然后从 Request.Header 删除这些敏感头，
// 使本地 handler 与默认转发链都看不到它们。仅 MITM 来源的请求会捕获。
func (ctx *Context) CaptureAndStripCredentials() {
	if ctx == nil || ctx.Request == nil {
		return
	}
	header := ctx.Request.Header
	if ctx.Source == SourceMITM && ctx.UpstreamURL != nil {
		creds := CapturedCredentials{BoundTarget: boundTargetFromURL(ctx.UpstreamURL)}
		if values, ok := header["Authorization"]; ok {
			creds.AuthorizationPresent = true
			if len(values) > 0 {
				creds.Authorization = values[0]
			}
		}
		if cookies, ok := header["Cookie"]; ok {
			creds.Cookies = append(creds.Cookies, cookies...)
		}
		if values := header.Values("x-cursor-checksum"); len(values) > 0 {
			creds.ChecksumPresent = true
			creds.Checksum = values[0]
		} else if _, ok := header["X-Cursor-Checksum"]; ok {
			creds.ChecksumPresent = true
		}
		ctx.Credentials = creds
	}
	// 无论来源，都从请求头剥离敏感凭证，避免泄漏给本地 handler 或默认转发。
	header.Del("Authorization")
	header.Del("Cookie")
	header.Del("x-cursor-checksum")
}

// boundTargetFromURL 返回规范化的 scheme://host[:port]，用于凭证目标绑定校验。
func boundTargetFromURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}
