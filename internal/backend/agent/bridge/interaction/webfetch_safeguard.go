// webfetch_safeguard.go 是 F-24 SSRF 防护逻辑的薄包装层。
//
// 实现已抽到 internal/safehttp 包并导出，供 interaction（WebFetch 工具）与
// forwarder（@docs 上传抓取正文，N-21）共用同一份 SSRF 防护——避免复制漂移
// 导致某类私网地址漏判而构成 SSRF 绕过（审计 N-14 高风险）。
//
// 本文件保留未导出的包装函数名（isPublicWebFetchIP / resolveAndValidateHost /
// newSSRFSafeDialContext / newSSRFSafeTransport），使既有 bridge.go 调用点与
// webfetch_safeguard_test.go 无需改动；它们仅转发到 safehttp。
//
// 防护策略文档见 internal/safehttp/safehttp.go。
package interaction

import (
	"context"
	"net"
	"net/http"

	"cursor/internal/safehttp"
)

func isPublicWebFetchIP(ip net.IP) bool { return safehttp.IsPublicIP(ip) }

func resolveAndValidateHost(host string) (net.IP, error) {
	return safehttp.ResolveAndValidateHost(host)
}

func newSSRFSafeDialContext() func(ctx context.Context, network, address string) (net.Conn, error) {
	return safehttp.NewSSRFSafeDialContext()
}

func newSSRFSafeTransport(base *http.Transport) *http.Transport {
	return safehttp.NewSSRFSafeTransport(base)
}
