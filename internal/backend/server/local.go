package server

import (
	"net/http"

	"cursor/internal/logger"
)

// HealthzPath 是内置后端健康检查端点路径，单一事实源（审计 M10）。
// internal/backend/host.go 在此路由 Health() handler 并用于 HealthCheck 探测；
// internal/backend/server/middleware.go 的 LoopbackAuth 对此路径放行（健康检查不带 relay proof）。
// 三处引用同一常量，避免此前「middleware 与 host 各定义一份 /healthz 靠注释保持一致」——
// 若 healthPath 改了而 exemptPath 没跟上，LoopbackAuth 会拦截健康检查导致 HealthCheck 误判后端宕机。
const HealthzPath = "/healthz"

func Health() HandlerFunc {
	return func(ctx *Context) error {
		if ctx == nil || ctx.Writer == nil {
			return nil
		}
		if ctx.Request != nil && ctx.Request.Method != http.MethodGet {
			http.Error(ctx.Writer, "method not allowed", http.StatusMethodNotAllowed)
			return nil
		}
		if ctx.Request != nil {
			logger.Infof("内置后端 healthz 命中 remote_addr=%s user_agent=%s", ctx.Request.RemoteAddr, ctx.Request.UserAgent())
		}
		ctx.Writer.WriteHeader(http.StatusOK)
		_, _ = ctx.Writer.Write([]byte("ok"))
		return nil
	}
}

// connectReadMaxBytes 是 connectRPC 入站请求体的字节上限（F-28）。
// connectrpc v1.19.1 无 WithReadMaxBytes 选项，在 http.Handler 边界用
// http.MaxBytesReader 包 body，超限由 net/http 自动写 413 并返回 MaxBytesError。
// 32MiB 与 upstream compat route 的 LimitReader 同口径。
const connectReadMaxBytes = 32 << 20

func HTTPHandlerAction(handler http.Handler) HandlerFunc {
	return func(ctx *Context) error {
		if ctx == nil {
			return nil
		}
		if handler == nil {
			return nil
		}
		if ctx.Request != nil && ctx.Request.Body != nil {
			// F-28：所有 connect handler 经此挂载，统一在边界加 body 上限，
			// 防止恶意/异常大请求体耗尽 backend 内存。
			ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, connectReadMaxBytes)
		}
		handler.ServeHTTP(ctx.Writer, ctx.Request)
		return nil
	}
}
