package server

import (
	"cursor/internal/logger"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"

	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/relayauth"
	legacyruntime "cursor/internal/runtime"
)

func Recover() Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx *Context) (err error) {
			defer func() {
				if recovered := recover(); recovered != nil {
					err = fmt.Errorf("panic: %v\n%s", recovered, string(debug.Stack()))
				}
			}()
			return next(ctx)
		}
	}
}

func ServerContext() Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx *Context) error {
			if ctx == nil {
				return fmt.Errorf("server context is nil")
			}
			if err := ctx.ParseUpstreamURL(); err != nil {
				return err
			}
			// 捕获 Cursor 真实凭证到 ctx 并从请求头剥离，本地 handler 不可见。
			ctx.CaptureAndStripCredentials()
			return next(ctx)
		}
	}
}

func PolicyMiddleware(configs *serverconfig.Manager) Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx *Context) error {
			ctx.Mode = parseExecutionMode(configs.RouteMode(ctx.UpstreamURL != nil))
			logger.Infof("ctx.Mode=%s upstream=%t", ctx.Mode, ctx.UpstreamURL != nil)
			return next(ctx)
		}
	}
}

// healthzPath 与 internal/backend/host.go 的 healthPath 保持一致，loopback 鉴权对其放行。
const loopbackAuthExemptPath = "/healthz"

// ErrRelayUnauthorized 表示请求缺少或携带无效的 MITM relay proof。映射到 401。
var ErrRelayUnauthorized = errors.New("relay auth: unauthorized")

// LoopbackAuth 校验请求携带的进程级 relay proof 私有头，拒绝本机其它进程未经 mitm 直接
// 调用 backend 的 cursor.sh 兼容路由。校验通过后立即删除 proof 头，避免其泄漏到下游或上游。
//
// 关键变更：不再用 Authorization 承载内部信任 —— Cursor 客户端的真实 Authorization 保留，
// 由 ServerContext 捕获到 ctx.Credentials。
func LoopbackAuth() Middleware {
	proof, err := relayauth.New()
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx *Context) error {
			if ctx == nil || ctx.Request == nil {
				return fmt.Errorf("relay auth: nil request")
			}
			if ctx.Request.URL.Path == loopbackAuthExemptPath {
				return next(ctx)
			}
			if err != nil || proof == nil {
				return fmt.Errorf("relay auth: proof unavailable: %w", ErrRelayUnauthorized)
			}
			provided := strings.TrimSpace(ctx.Request.Header.Get(relayauth.HeaderRelayProof))
			// proof 头无论校验结果都必须删除，绝不能进入 handler 或转发链。
			ctx.Request.Header.Del(relayauth.HeaderRelayProof)
			if !proof.Verify(provided) {
				return fmt.Errorf("%w", ErrRelayUnauthorized)
			}
			return next(ctx)
		}
	}
}

func ErrorEncoder() Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx *Context) error {
			if ctx != nil {
				ctx.LastError = nil
			}
			if err := next(ctx); err != nil {
				if ctx != nil {
					ctx.LastError = err
				}
				if ctx == nil || ctx.Writer == nil {
					return err
				}
				writeServerError(ctx.Writer, err)
				return nil
			}
			return nil
		}
	}
}

func writeServerError(writer http.ResponseWriter, err error) {
	if responseWriterHasWrittenHeader(writer) {
		return
	}
	status := http.StatusBadGateway
	message := "bad gateway"
	switch {
	case err == nil:
		status = http.StatusOK
		message = ""
	case errors.Is(err, ErrRelayUnauthorized):
		status = http.StatusUnauthorized
		message = "unauthorized"
	case strings.TrimSpace(err.Error()) == "empty raw url":
		status = http.StatusBadRequest
		message = "invalid raw url"
	case errors.Is(err, ErrInvalidBidiAppendPayload):
		status = http.StatusBadRequest
		message = "invalid bidi append payload"
	case errors.Is(err, legacyruntime.ErrInvalidSystemSetting):
		status = http.StatusInternalServerError
		message = "invalid system setting"
	case errors.Is(err, legacyruntime.ErrChannelNotAvailable):
		status = http.StatusServiceUnavailable
		message = "no available channel"
	}
	http.Error(writer, message, status)
}
