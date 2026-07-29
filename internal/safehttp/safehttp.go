// Package safehttp 提供 SSRF 防护的 HTTP 抓取能力（F-24 抽出，供多包复用）。
//
// 此前 SSRF 防护逻辑（DNS 解析 + IP 固定 DialContext）只存在于
// internal/backend/agent/bridge/interaction/webfetch_safeguard.go，未导出且
// 绑定在 interaction 包里。N-21 的 @docs 上传需要抓取页面正文入库，若在
// forwarder 包另写一份抓取会复制 SSRF 防护逻辑、产生维护漂移——若漂移到
// 漏掉某类私网地址即构成 SSRF 绕过（审计 N-14 已确认这是高风险）。
//
// 故把 SSRF-safe 的 IP 校验、host 解析、DialContext、Transport 构造抽到本包
// 并导出，interaction 与 forwarder 共用同一份实现。interaction 包保留未导出
// 包装（isPublicWebFetchIP 等）以兼容既有测试与 bridge.go 调用点。
//
// 防护策略：
//   - IsPublicIP 拒绝 loopback/私网/link-local/未指定/云元数据地址段；
//   - ResolveAndValidateHost 解析全部 A/AAAA，逐个校验，返回首个公网 IP；
//   - NewSSRFSafeDialContext 把 host 解析+校验后固定到已验证 IP，绕过 DNS rebinding；
//   - redirect 每跳重新走 ResolveAndValidateHost（调用方的 CheckRedirect 自动受益）。
package safehttp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// hostAllowlist 是 WebFetch 显式放行的 host 白名单（审计「行为偏离-3」）。
// 默认空 = 保持现 SSRF 硬拒绝行为（最安全）；用户配置内网 Wiki/Confluence 等 host 后，
// 命中白名单的 host 在 ResolveAndValidateHost 里跳过 IsPublicIP 拒绝，允许内网抓取。
// 这是对硬编码安全基线的「用户显式放行叠加」，不是放开默认行为。
var (
	hostAllowlistMu sync.RWMutex
	hostAllowlist  map[string]struct{}
)

// SetHostAllowlist 替换全局 host 白名单。传入 nil/空 = 清空白名单（恢复硬拒绝）。
// host 由调用方去重/小写/去空白后再传入；本函数只做 TrimSpace + ToLower 兜底。
// 由 interaction.Bridge 在每次 WebFetch 前、根据当前 WebToolsConfig 同步设置。
func SetHostAllowlist(hosts []string) {
	next := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		trimmed := strings.ToLower(strings.TrimSpace(host))
		if trimmed != "" {
			next[trimmed] = struct{}{}
		}
	}
	hostAllowlistMu.Lock()
	hostAllowlist = next
	hostAllowlistMu.Unlock()
}

// IsHostAllowlisted 判定 host 是否在用户显式放行白名单内。host 已归一（小写/去空白）。
// 导出供 interaction.validateWebFetchURL 在字面 host 拒绝前查询（与 ResolveAndValidateHost
// 内部用同一全局表，保证两处判定一致，审计「行为偏离-3」）。
func IsHostAllowlisted(host string) bool {
	hostAllowlistMu.RLock()
	defer hostAllowlistMu.RUnlock()
	if len(hostAllowlist) == 0 {
		return false
	}
	_, ok := hostAllowlist[strings.ToLower(strings.TrimSpace(strings.Trim(host, "[]")))]
	return ok
}

// isHostAllowlisted 是 IsHostAllowlisted 的未导出别名，供包内调用。
func isHostAllowlisted(host string) bool { return IsHostAllowlisted(host) }

// IsPublicIP 判定一个 IP 是否允许安全抓取连接。
// 拒绝：loopback / 私网 / link-local / 未指定 / 云元数据地址段。
// IPv4-mapped IPv6 视为底层 IPv4 再判。
func IsPublicIP(ip net.IP) bool {
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
	// 云元数据地址段：AWS 169.254.169.254 已被 LinkLocalUnicast 覆盖；
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

// ResolveAndValidateHost 解析 host 的全部 A/AAAA 记录，逐个校验是否公网可达。
// 返回首个通过校验的 IP（用于 DialContext 固定），全部非公网或解析失败则报错。
// 字面 IP 直接校验（不经过 DNS）。
func ResolveAndValidateHost(host string) (net.IP, error) {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return nil, fmt.Errorf("web fetch host is empty")
	}
	// 用户显式放行白名单（审计「行为偏离-3」）：命中则跳过 IsPublicIP 拒绝，
	// 允许企业内网 Wiki/Confluence 等 host 抓取。仍走解析以拿首个 IP 固定（防 rebinding）。
	allowlisted := isHostAllowlisted(host)
	// 字面 IP：直接判，不查 DNS（避免 rebinding 中间层）。
	if ip := net.ParseIP(host); ip != nil {
		if !allowlisted && !IsPublicIP(ip) {
			return nil, fmt.Errorf("web fetch host %s is not public-web accessible", host)
		}
		return ip, nil
	}
	// 域名：解析全部 A/AAAA，逐个校验，至少一个公网 IP 才放行。
	// 白名单 host 放宽到「任一解析 IP 即可」（内网 IP 也接受）。
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
		if allowlisted || IsPublicIP(entry.IP) {
			firstPublic = entry.IP
			break
		}
	}
	if firstPublic == nil {
		return nil, fmt.Errorf("web fetch host %s resolves only to non-public addresses", host)
	}
	return firstPublic, nil
}

// NewSSRFSafeDialContext 返回一个 DialContext：把 host 解析+校验后固定到已验证 IP。
// 固定 IP 后即便 DNS 在用户批准后被篡改（rebinding），实际连接仍走原校验过的地址。
// 端口与原 network 一致；address 形如 host:port，已校验 IP 替换 host 部分。
func NewSSRFSafeDialContext() func(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("web fetch dial split host port: %w", err)
		}
		ip, err := ResolveAndValidateHost(host)
		if err != nil {
			return nil, err
		}
		// 用已校验 IP 重建 address，固定连接目标。
		safeAddr := net.JoinHostPort(ip.String(), port)
		return dialer.DialContext(ctx, network, safeAddr)
	}
}

// NewSSRFSafeTransport 构造一个装了 SSRF-safe DialContext 的 http.Transport。
// 复制 base transport 的其余设置（TLS、代理解析），仅替换 DialContext。
// base 为 nil 时用空 Transport。
func NewSSRFSafeTransport(base *http.Transport) *http.Transport {
	if base == nil {
		base = &http.Transport{}
	}
	clone := base.Clone()
	clone.DialContext = NewSSRFSafeDialContext()
	return clone
}
