package modeladapter

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestApplyCustomHeaders_BlocksSensitiveHeaders(t *testing.T) {
	tests := []struct {
		name       string
		headers    string
		wantSet    map[string]string // headers that SHOULD be applied
		wantBlocked []string          // headers that must NOT appear
	}{
		{
			name: "authorization blocked",
			headers: `{"Authorization":"Bearer evil","X-Custom":"v"}`,
			wantSet: map[string]string{"X-Custom": "v"},
			wantBlocked: []string{"Authorization"},
		},
		{
			name: "x-api-key case-insensitive blocked",
			headers: `{"X-API-KEY":"evil","Accept":"application/json"}`,
			wantSet: map[string]string{"Accept": "application/json"},
			wantBlocked: []string{"X-Api-Key"},
		},
		{
			name: "host and cookie blocked",
			headers: `{"Host":"evil.com","Cookie":"session=1","X-Trace":"t"}`,
			wantSet: map[string]string{"X-Trace": "t"},
			wantBlocked: []string{"Host", "Cookie"},
		},
		{
			name: "normal headers all applied",
			headers: `{"X-Trace":"t","X-Req-Id":"r"}`,
			wantSet: map[string]string{"X-Trace": "t", "X-Req-Id": "r"},
			wantBlocked: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://localhost", nil)
			if err := ApplyCustomHeaders(req, true, tc.headers); err != nil {
				t.Fatalf("ApplyCustomHeaders error: %v", err)
			}
			for k, v := range tc.wantSet {
				if got := req.Header.Get(k); got != v {
					t.Errorf("header %q = %q, want %q", k, got, v)
				}
			}
			for _, b := range tc.wantBlocked {
				if req.Header.Get(b) != "" {
					t.Errorf("blocked header %q should not be set, got %q", b, req.Header.Get(b))
				}
			}
		})
	}
}

func TestApplyCustomHeaders_DisabledNoop(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://localhost", nil)
	if err := ApplyCustomHeaders(req, false, `{"X-Custom":"v"}`); err != nil {
		t.Fatalf("error: %v", err)
	}
	if req.Header.Get("X-Custom") != "" {
		t.Fatalf("disabled ApplyCustomHeaders should not set headers")
	}
	_ = strings.TrimSpace // keep import if no other use
}
