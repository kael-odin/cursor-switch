package interaction

import (
	"net"
	"testing"
)

// TestIsPublicWebFetchIP 验证 F-24 的 IP 公网判定。
func TestIsPublicWebFetchIP(t *testing.T) {
	cases := []struct {
		name string
		ip   string
		want bool
	}{
		// 公网放行
		{"public v4", "8.8.8.8", true},
		{"public v4 github", "140.82.112.3", true},
		{"public v6", "2606:4700:4700::1111", true},
		// loopback 拒
		{"loopback v4", "127.0.0.1", false},
		{"loopback v4 alt", "127.1.2.3", false},
		{"loopback v6", "::1", false},
		// 私网拒
		{"private 10", "10.0.0.1", false},
		{"private 172", "172.16.0.1", false},
		{"private 192", "192.168.1.1", false},
		// link-local 拒
		{"link-local v4", "169.254.169.254", false}, // 云元数据地址
		{"link-local v6", "fe80::1", false},
		// link-local multicast
		{"ll multicast", "224.0.0.1", false},
		// 未指定
		{"unspecified v4", "0.0.0.0", false},
		{"unspecified v6", "::", false},
		// IPv4-mapped IPv6 伪装——必须按底层 IPv4 判
		{"v4-mapped loopback", "::ffff:127.0.0.1", false},
		{"v4-mapped private", "::ffff:10.0.0.1", false},
		{"v4-mapped metadata", "::ffff:169.254.169.254", false},
		{"v4-mapped public", "::ffff:8.8.8.8", true},
		// ULA v6
		{"ula v6", "fd00::1", false},
		// nil
		{"nil", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var ip net.IP
			if c.ip != "" {
				ip = net.ParseIP(c.ip)
				if ip == nil {
					t.Fatalf("bad test ip %q", c.ip)
				}
			}
			if got := isPublicWebFetchIP(ip); got != c.want {
				t.Errorf("isPublicWebFetchIP(%s) = %v, want %v", c.ip, got, c.want)
			}
		})
	}
}

// TestResolveAndValidateHostLiteralIP 验证字面 IP 路径不查 DNS、直接判。
func TestResolveAndValidateHostLiteralIP(t *testing.T) {
	// 公网字面 IP 放行
	ip, err := resolveAndValidateHost("8.8.8.8")
	if err != nil {
		t.Fatalf("expected 8.8.8.8 ok, got %v", err)
	}
	if ip == nil {
		t.Fatal("expected non-nil ip")
	}
	// 私网字面 IP 拒绝
	if _, err := resolveAndValidateHost("127.0.0.1"); err == nil {
		t.Error("expected 127.0.0.1 rejected")
	}
	if _, err := resolveAndValidateHost("169.254.169.254"); err == nil {
		t.Error("expected metadata addr rejected")
	}
	if _, err := resolveAndValidateHost("10.0.0.5"); err == nil {
		t.Error("expected private 10.x rejected")
	}
	if _, err := resolveAndValidateHost("::1"); err == nil {
		t.Error("expected ::1 rejected")
	}
	// IPv4-mapped 伪装拒绝
	if _, err := resolveAndValidateHost("::ffff:127.0.0.1"); err == nil {
		t.Error("expected v4-mapped loopback rejected")
	}
}

// TestResolveAndValidateHostEmpty 验证空 host 防御。
func TestResolveAndValidateHostEmpty(t *testing.T) {
	if _, err := resolveAndValidateHost(""); err == nil {
		t.Error("expected empty host rejected")
	}
	if _, err := resolveAndValidateHost("   "); err == nil {
		t.Error("expected whitespace host rejected")
	}
}

// TestValidateWebFetchURLRejectsPrivateLiteral 验证 URL 层字面私网 IP 被拒。
func TestValidateWebFetchURLRejectsPrivateLiteral(t *testing.T) {
	bad := []string{
		"http://127.0.0.1/",
		"http://localhost/",
		"http://10.0.0.1/",
		"http://192.168.1.1/",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]/",
	}
	for _, u := range bad {
		if _, err := validateWebFetchURL(u); err == nil {
			t.Errorf("expected %q rejected, got nil", u)
		}
	}
}
