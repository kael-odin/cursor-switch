package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

const (
	// InjectAccountEmail 表示本地模式模拟账号的 email。
	InjectAccountEmail = "cursor@ai.com"
	// InjectAuthToken 表示本地模式模拟账号的 token。
	InjectAuthToken = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJmYWtlLWN1cnNvci1sb2NhbC11c2VyIiwiZW1haWwiOiJjdXJzb3JAYWkuY29tIiwidHlwZSI6InNlc3Npb24iLCJpc3MiOiJjdXJzb3ItY2xpZW50Iiwic2NvcGUiOiJvcGVuaWQgcHJvZmlsZSBlbWFpbCIsImV4cCI6NDA3MDkwODgwMH0.fake-local-state-token"
	// LocalRelayToken 用于 local 模式下，backend 回源 cursor.sh 时覆盖 Authorization。
	// 该值必须与 InjectAuthToken 一致：它代表注入 Cursor state.vscdb 的同一账号 token，
	// 回源到官方 cursor.sh 时作为该账号的鉴权凭据。
	LocalRelayToken = InjectAuthToken
)

// loopbackTokenOnce 保证进程内只生成一次本地 loopback 鉴权 token。
// ponytail: sync.Once 懒生成，进程级随机，mitm 注入 + backend 校验，防止本机其它进程裸调 backend 的 cursor.sh 兼容端点。
var (
	loopbackTokenOnce  sync.Once
	loopbackTokenValue string
)

// LoopbackToken 返回进程启动时随机生成的 loopback 鉴权 token。
// mitm 转发请求到 backend 时注入此 token，backend 中间件校验它，
// 拒绝本机其它进程未经 mitm 直接调用 backend 的 cursor.sh 兼容路由。
func LoopbackToken() string {
	loopbackTokenOnce.Do(func() {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			// rand 失败极罕见；退化到固定前缀 + 时间无关的兜底仍优于无鉴权。
			loopbackTokenValue = "cursor-byok-loopback-fallback"
			return
		}
		loopbackTokenValue = hex.EncodeToString(raw)
	})
	return loopbackTokenValue
}

// LoopbackAuthorization 返回注入 backend 请求的完整 Authorization 头值。
func LoopbackAuthorization() string {
	return "Bearer " + LoopbackToken()
}
