package safehttp

import (
	"net"
	"testing"
)

// TestIsPublicIP 覆盖 SSRF 防护的 IP 分类（N-14 抽出到 safehttp）：
// loopback/私网/link-local/未指定/云元数据一律拒绝；公网 IP 放行。
func TestIsPublicIP(t *testing.T) {
	deny := []string{
		"127.0.0.1", "10.0.0.5", "192.168.1.1", "172.16.0.1",
		"169.254.169.254", // 云元数据
		"0.0.0.0",
		"::1",           // IPv6 loopback
		"fe80::1",       // IPv6 link-local
		"fc00::1",       // IPv6 ULA
		"::ffff:127.0.0.1", // IPv4-mapped IPv6 loopback
	}
	for _, ip := range deny {
		if IsPublicIP(net.ParseIP(ip)) {
			t.Errorf("IsPublicIP(%s) = true, want false (should be denied)", ip)
		}
	}
	allow := []string{
		"8.8.8.8", "1.1.1.1", "203.0.113.1",
		"2606:4700:4700::1111", // 公网 IPv6
	}
	for _, ip := range allow {
		if !IsPublicIP(net.ParseIP(ip)) {
			t.Errorf("IsPublicIP(%s) = false, want true (should be allowed)", ip)
		}
	}
	if IsPublicIP(nil) {
		t.Errorf("IsPublicIP(nil) = true, want false")
	}
}

// TestResolveAndValidateHost_LiteralIP 字面 IP 直接校验，不查 DNS。
func TestResolveAndValidateHost_LiteralIP(t *testing.T) {
	if _, err := ResolveAndValidateHost("127.0.0.1"); err == nil {
		t.Errorf("loopback literal IP should be rejected")
	}
	if _, err := ResolveAndValidateHost("169.254.169.254"); err == nil {
		t.Errorf("metadata literal IP should be rejected")
	}
	if _, err := ResolveAndValidateHost("10.0.0.5"); err == nil {
		t.Errorf("private literal IP should be rejected")
	}
	// 公网字面 IP 应放行。
	if _, err := ResolveAndValidateHost("8.8.8.8"); err != nil {
		t.Errorf("public literal IP should be allowed, got %v", err)
	}
}

// TestResolveAndValidateHost_Empty 空/空白 host 应报错。
func TestResolveAndValidateHost_Empty(t *testing.T) {
	if _, err := ResolveAndValidateHost(""); err == nil {
		t.Errorf("empty host should error")
	}
	if _, err := ResolveAndValidateHost("   "); err == nil {
		t.Errorf("whitespace host should error")
	}
}

// TestNewSSRFSafeTransport_NilBase base 为 nil 时应使用空 Transport 不 panic。
func TestNewSSRFSafeTransport_NilBase(t *testing.T) {
	transport := NewSSRFSafeTransport(nil)
	if transport == nil {
		t.Fatal("transport should not be nil")
	}
	if transport.DialContext == nil {
		t.Errorf("DialContext should be set (SSRF-safe)")
	}
}
