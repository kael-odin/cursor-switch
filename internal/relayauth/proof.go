// Package relayauth 提供本进程 MITM → backend 之间的信任证明。
//
// 设计：MITM 与 backend 同进程、仅 loopback 通信。原先用 HTTP Authorization 头承载
// 内部 loopback token，导致 Cursor 客户端的真实 Authorization 在 MITM 边界被覆盖丢失，
// 无法把真实登录态透传到官方 cursor.sh 控制面（marketplace / 登录 / 账号等）。
//
// 现改为：内部信任走独立的私有头 X-Cursor-BYOK-Relay-Proof，Authorization/Cookie/
// x-cursor-checksum 保留 Cursor 原值由 backend 捕获到请求上下文，仅在显式的
// OriginalCursor 出站策略下、且目标为原始 HTTPS *.cursor.sh 时才恢复到上游请求。
// 私有 proof 永不离开 backend 边界。
package relayauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"sync"
)

// HeaderRelayProof 是 MITM 注入、backend 校验的进程级信任头。
// 该头必须由 backend 在路由处理前删除，且永远不得转发给任何上游。
const HeaderRelayProof = "X-Cursor-BYOK-Relay-Proof"

// Proof 承载进程级随机信任凭据。一次创建，进程内共享。
type Proof struct {
	value string
}

var (
	once      sync.Once
	proofInst *Proof
	proofErr  error
)

// New 返回进程级 Proof。随机源失败时返回错误（调用方应使启动失败，不得退化到固定值）。
func New() (*Proof, error) {
	once.Do(func() {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			proofErr = err
			return
		}
		proofInst = &Proof{value: hex.EncodeToString(raw)}
	})
	if proofErr != nil {
		return nil, proofErr
	}
	if proofInst == nil {
		return nil, errors.New("relayauth: proof not initialized")
	}
	return proofInst, nil
}

// HeaderValue 返回注入到 backend 请求的 proof 头值。
func (p *Proof) HeaderValue() string {
	if p == nil {
		return ""
	}
	return p.value
}

// Verify 常量时间校验提供的 proof 值是否与本进程 proof 一致。
// 空值或长度不等直接返回 false，避免泄露长度信息之外的细节。
func (p *Proof) Verify(provided string) bool {
	if p == nil || p.value == "" || provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(p.value), []byte(provided)) == 1
}
