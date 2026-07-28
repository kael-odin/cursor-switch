package server

import (
	"net/http"

	"cursor/internal/logger"
)

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
