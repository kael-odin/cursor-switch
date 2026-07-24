package upstream

import (
	"net/http"
	"net/url"
	"testing"

	"cursor/internal/backend/server"
	"cursor/internal/relayauth"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	return u
}

func credsFor(target string) server.CapturedCredentials {
	return server.CapturedCredentials{
		Authorization:        "Bearer real-token",
		AuthorizationPresent: true,
		Cookies:              []string{"WorkosCursorSessionToken=abc"},
		Checksum:             "chk",
		ChecksumPresent:      true,
		BoundTarget:          target,
	}
}

func TestRestoreOriginalCursorSuccess(t *testing.T) {
	target := mustURL(t, "https://api2.cursor.sh/aiserver.v1.DashboardService/InstallUserPlugin")
	reqCtx := &RequestContext{TargetURL: target, Credentials: credsFor("https://api2.cursor.sh")}
	req, _ := http.NewRequest(http.MethodPost, target.String(), nil)

	if err := restoreOriginalCursorCredentials(req, reqCtx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Header.Get("Authorization") != "Bearer real-token" {
		t.Fatal("Authorization not restored")
	}
	if req.Header.Get("Cookie") != "WorkosCursorSessionToken=abc" {
		t.Fatal("Cookie not restored")
	}
	if req.Header.Get("x-cursor-checksum") != "chk" {
		t.Fatal("checksum not restored")
	}
}

func TestRestoreRefusesNonHTTPS(t *testing.T) {
	target := mustURL(t, "http://api2.cursor.sh/x")
	reqCtx := &RequestContext{TargetURL: target, Credentials: credsFor("http://api2.cursor.sh")}
	req, _ := http.NewRequest(http.MethodPost, target.String(), nil)
	if err := restoreOriginalCursorCredentials(req, reqCtx); err == nil {
		t.Fatal("expected refusal for non-https target")
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("credentials leaked on refused non-https target")
	}
}

func TestRestoreRefusesNonCursorHost(t *testing.T) {
	target := mustURL(t, "https://evil.example.com/x")
	reqCtx := &RequestContext{TargetURL: target, Credentials: credsFor("https://api2.cursor.sh")}
	req, _ := http.NewRequest(http.MethodPost, target.String(), nil)
	if err := restoreOriginalCursorCredentials(req, reqCtx); err == nil {
		t.Fatal("expected refusal for non-cursor host")
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("credentials leaked to non-cursor host")
	}
}

func TestRestoreRefusesBoundTargetMismatch(t *testing.T) {
	// 目标是 cursor.sh 子域，但凭证绑定的是另一个主机 → 拒绝，防止跨目标重放。
	target := mustURL(t, "https://api3.cursor.sh/x")
	reqCtx := &RequestContext{TargetURL: target, Credentials: credsFor("https://api2.cursor.sh")}
	req, _ := http.NewRequest(http.MethodPost, target.String(), nil)
	if err := restoreOriginalCursorCredentials(req, reqCtx); err == nil {
		t.Fatal("expected refusal for bound target mismatch")
	}
}

func TestRestoreRefusesNonStandardPort(t *testing.T) {
	target := mustURL(t, "https://api2.cursor.sh:8443/x")
	reqCtx := &RequestContext{TargetURL: target, Credentials: credsFor("https://api2.cursor.sh:8443")}
	req, _ := http.NewRequest(http.MethodPost, target.String(), nil)
	if err := restoreOriginalCursorCredentials(req, reqCtx); err == nil {
		t.Fatal("expected refusal for non-standard port")
	}
}

func TestScrubReservedHeadersRemovesAll(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer x")
	h.Set("Cookie", "a=b")
	h.Set("x-cursor-checksum", "c")
	h.Set("Proxy-Authorization", "p")
	h.Set(relayauth.HeaderRelayProof, "proof")
	h.Set(HeaderRawServerURL, "https://x")
	h.Set("X-Keep", "keep")

	scrubReservedHeaders(h)

	for _, k := range []string{"Authorization", "Cookie", "x-cursor-checksum", "Proxy-Authorization", relayauth.HeaderRelayProof, HeaderRawServerURL} {
		if h.Get(k) != "" {
			t.Fatalf("header %q not scrubbed", k)
		}
	}
	if h.Get("X-Keep") != "keep" {
		t.Fatal("non-reserved header wrongly removed")
	}
}

func TestIsCursorHost(t *testing.T) {
	yes := []string{"cursor.sh", "api2.cursor.sh", "api3.cursor.sh", "CURSOR.SH"}
	no := []string{"", "cursor.sh.evil.com", "notcursor.sh.example.com", "example.com", "tab.leokun.cn"}
	for _, h := range yes {
		if !isCursorHost(h) {
			t.Fatalf("isCursorHost(%q) = false, want true", h)
		}
	}
	for _, h := range no {
		if isCursorHost(h) {
			t.Fatalf("isCursorHost(%q) = true, want false", h)
		}
	}
}
