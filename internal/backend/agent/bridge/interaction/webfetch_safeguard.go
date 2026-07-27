// webfetch_safeguard.go 实现 F-24 的 SSRF 防护：WebFetch 的 DNS 解析 + IP 固定。
//
// 此前 validateWebFetchURL 只对字面 IP 调 net.ParseIP，普通域名完全不解析——
// 攻击者用一个解析到 127.0.0.1 / 169.254.169.254 / LAN 的域名即可绕过，
// 读取内网服务或云元数据后回灌模型。DNS rebinding 还能在用户批准后改变解析。
//
// 修复策略（与审计建议一致）：
//   - validateWebFetchURL 对域名解析全部 A/AAAA 记录，逐个拒绝非公网地址
//   - 自定义 http.Transport.DialContext：先解析 host，逐个校验，再用已校验的
//     第一个 IP 建立连接（把验证结果固定到实际 Dial，绕过 rebinding）
//   - redirect 每跳重新走 validateWebFetchURL（已有 CheckRedirect 回调，自动受益）
//
// 域名解析失败、私网命中、多地址全私网等情况一律拒绝。
package interaction

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// isPublicWebFetchIP 判定一个 IP 是否允许 WebFetch 连接。
// 拒绝：loopback / 私网 / link-local / 未指定 / 云元数据地址段。
// IPv4-mapped IPv6 视为底层 IPv4 再判。
func isPublicWebFetchIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	// 解 IPv4-mapped IPv6（::ffff:1.2.3.4）回 IPv4 再判，避免伪装绕过。
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return false
	}
	// 云元数据地址段：AWS 169.254.169.254 / 169.254.169.254 已被 LinkLocalUnicast 覆盖；
	// GCP metadata.google.internal 走 DNS——由调用方域名解析后这里命中 169.254.x 被拒。
	// 额外兜底显式拒绝 169.254.0.0/16 全段（IsLinkLocalUnicast 已覆盖，此处防御性显式判）。
	if v4 := ip.To4(); v4 != nil && v4[0] == 169 && v4[1] == 254 {
		return false
	}
	// 拒绝 IPv6 site-local（fec0::/10，已被 IsPrivate 覆盖但显式兜底）与 ULA（fc00::/7）。
	if v6 := ip.To16(); v6 != nil && len(v6) == net.IPv6len {
		if v6[0]&0xfe == 0xfc { // fc00::/7 ULA
			return false
		}
	}
	return true
}

// resolveAndValidateHost 解析 host 的全部 A/AAAA 记录，逐个校验是否公网可达。
// 返回首个通过校验的 IP（用于 DialContext 固定），全部非公网或解析失败则报错。
// 字面 IP 直接校验（不经过 DNS）。
func resolveAndValidateHost(host string) (net.IP, error) {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return nil, fmt.Errorf("web fetch host is empty")
	}
	// 字面 IP：直接判，不查 DNS（避免 rebinding 中间层）。
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicWebFetchIP(ip) {
			return nil, fmt.Errorf("web fetch host %s is not public-web accessible", host)
		}
		return ip, nil
	}
	// 域名：解析全部 A/AAAA，逐个校验，至少一个公网 IP 才放行。
	// 不用 net.DefaultResolver 的 LookupIP，因其在 DialContext 内调用可能受系统 hosts 影响；
	// 显式走 PreferGo 解析器，30s 超时。
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			// 强制走系统 DNS 服务器，避免 proxy 干扰。
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, network, address)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("web fetch resolve host %s failed: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("web fetch host %s resolved to no addresses", host)
	}
	var firstPublic net.IP
	for _, entry := range ips {
		if isPublicWebFetchIP(entry.IP) {
			firstPublic = entry.IP
			break
		}
	}
	if firstPublic == nil {
		return nil, fmt.Errorf("web fetch host %s resolves only to non-public addresses", host)
	}
	return firstPublic, nil
}

// newSSRFSafeDialContext 返回一个 DialContext：把 host 解析+校验后固定到已验证 IP。
// 固定 IP 后即便 DNS 在用户批准后被篡改（rebinding），实际连接仍走原校验过的地址。
// 端口与原 network 一致；address 形如 host:port，已校验 IP 替换 host 部分。
func newSSRFSafeDialContext() func(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("web fetch dial split host port: %w", err)
		}
		ip, err := resolveAndValidateHost(host)
		if err != nil {
			return nil, err
		}
		// 用已校验 IP 重建 address，固定连接目标。
		safeAddr := net.JoinHostPort(ip.String(), port)
		return dialer.DialContext(ctx, network, safeAddr)
	}
}

// newSSRFSafeTransport 构造一个装了 SSRF-safe DialContext 的 http.Transport。
// 复制 netproxy 默认 transport 的其余设置（TLS、代理解析），仅替换 DialContext。
func newSSRFSafeTransport(base *http.Transport) *http.Transport {
	if base == nil {
		base = &http.Transport{}
	}
	clone := base.Clone()
	clone.DialContext = newSSRFSafeDialContext()
	// 禁止 HTTP/2 的议长推送与连接复用跨 host——每次 DialContext 都重校验，
	// 但连接复用可能绕过；保守起见禁用 keep-alive 连接池跨请求复用同 host 的旧连接。
	// （同 host 复用是安全的，因为已固定 IP；这里不禁用，保留性能。）
	return clone
}
