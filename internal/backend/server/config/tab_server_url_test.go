package config

import "testing"

// TestNormalizeTabServerBaseURL 验证 Tab 服务端 URL 的 scheme/host 校验。
//
// 此前 TabServerBaseURL 只做 TrimSpace，没有 scheme 校验——用户填 "tab.example.com"
// （缺 scheme）或 "ftp://..." 会被照单全收，host.go 消费时行为未定义。这里改为：
// 空=放行（用内置默认）；非空必须 http/https + host 非空，否则报错让用户修正。
func TestNormalizeTabServerBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
		errSub  string
	}{
		{"empty_passes", "", "", false, ""},
		{"spaces_empty_passes", "   ", "", false, ""},
		{"valid_https_trimmed", "  https://tab.example.com  ", "https://tab.example.com", false, ""},
		{"valid_http", "http://localhost:8080", "http://localhost:8080", false, ""},
		{"missing_scheme_rejected", "tab.example.com", "", true, "http"},
		{"ftp_scheme_rejected", "ftp://tab.example.com", "", true, "http"},
		{"only_scheme_no_host_rejected", "https://", "", true, "host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeTabServerBaseURL(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (result %q)", tt.errSub, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestNormalizeConfigRejectsBadTabServerURL 验证整条 NormalizeConfig 链路拒绝坏 URL。
func TestNormalizeConfigRejectsBadTabServerURL(t *testing.T) {
	in := Config{
		Routing: RoutingConfig{
			Mode:             "local",
			TabServerBaseURL: "not-a-url",
		},
	}
	if _, err := NormalizeConfig(in); err == nil {
		t.Fatal("expected NormalizeConfig to reject non-URL TabServerBaseURL, got nil")
	}
}
