package config

import "testing"

func TestNormalizeListenAddrAcceptsLoopback(t *testing.T) {
	cases := []string{
		"127.0.0.1:18090",
		"127.0.0.1:1",
		"[::1]:18080",
	}
	for _, c := range cases {
		got, err := normalizeListenAddr(c, DefaultBackendListenAddr, "test")
		if err != nil {
			t.Fatalf("normalizeListenAddr(%q) unexpected error: %v", c, err)
		}
		if got == "" {
			t.Fatalf("normalizeListenAddr(%q) returned empty", c)
		}
	}
}

func TestNormalizeListenAddrRewritesWildcard(t *testing.T) {
	// 0.0.0.0 / :: 通配一次性改写为 127.0.0.1，端口保留。
	got, err := normalizeListenAddr("0.0.0.0:18090", DefaultBackendListenAddr, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "127.0.0.1:18090" {
		t.Fatalf("wildcard rewrite = %q, want 127.0.0.1:18090", got)
	}
}

func TestNormalizeListenAddrRejectsNonLoopback(t *testing.T) {
	cases := []string{
		"192.168.1.10:18090", // LAN
		"10.0.0.5:18090",     // private
		"8.8.8.8:18090",      // public
		"example.com:18090",  // hostname
		"0.0.0.0:0",          // invalid port anyway
	}
	for _, c := range cases {
		if _, err := normalizeListenAddr(c, DefaultBackendListenAddr, "test"); err == nil {
			t.Fatalf("normalizeListenAddr(%q) expected error, got nil", c)
		}
	}
}

func TestNormalizeConfigEnforcesLoopback(t *testing.T) {
	_, err := NormalizeConfig(Config{
		BackendListenAddr: "0.0.0.0:18090",
		ProxyListenAddr:   "192.168.0.2:18080",
	})
	if err == nil {
		t.Fatal("NormalizeConfig accepted a non-loopback proxy addr")
	}
}
