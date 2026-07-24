package server

import (
	"net/http"
	"net/url"
	"testing"
)

func newTestContext(headers map[string]string, upstream *url.URL, source SourceKind) *Context {
	req, _ := http.NewRequest(http.MethodPost, "/aiserver.v1.DashboardService/ListMarketplaces", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return &Context{
		Request:     req,
		Source:      source,
		UpstreamURL: upstream,
	}
}

func TestCaptureAndStripCredentialsMITM(t *testing.T) {
	target, _ := url.Parse("https://api2.cursor.sh/aiserver.v1.DashboardService/ListMarketplaces")
	ctx := newTestContext(map[string]string{
		"Authorization":    "Bearer real-cursor-token",
		"Cookie":           "WorkosCursorSessionToken=abc",
		"x-cursor-checksum": "chk123",
	}, target, SourceMITM)

	ctx.CaptureAndStripCredentials()

	// 捕获到 ctx.Credentials
	if !ctx.Credentials.AuthorizationPresent || ctx.Credentials.Authorization != "Bearer real-cursor-token" {
		t.Fatalf("Authorization not captured: %+v", ctx.Credentials)
	}
	if len(ctx.Credentials.Cookies) != 1 || ctx.Credentials.Cookies[0] != "WorkosCursorSessionToken=abc" {
		t.Fatalf("Cookie not captured: %+v", ctx.Credentials.Cookies)
	}
	if !ctx.Credentials.ChecksumPresent || ctx.Credentials.Checksum != "chk123" {
		t.Fatalf("checksum not captured: %+v", ctx.Credentials)
	}
	if ctx.Credentials.BoundTarget != "https://api2.cursor.sh" {
		t.Fatalf("bound target = %q, want https://api2.cursor.sh", ctx.Credentials.BoundTarget)
	}

	// 从请求头剥离
	if ctx.Request.Header.Get("Authorization") != "" {
		t.Fatal("Authorization not stripped from request header")
	}
	if ctx.Request.Header.Get("Cookie") != "" {
		t.Fatal("Cookie not stripped from request header")
	}
	if ctx.Request.Header.Get("x-cursor-checksum") != "" {
		t.Fatal("checksum not stripped from request header")
	}
}

func TestCaptureAndStripCredentialsNativeStripsButDoesNotCapture(t *testing.T) {
	ctx := newTestContext(map[string]string{
		"Authorization": "Bearer leaked",
		"Cookie":        "x=y",
	}, nil, SourceNative)

	ctx.CaptureAndStripCredentials()

	// 非 MITM 来源不捕获
	if ctx.Credentials.AuthorizationPresent {
		t.Fatal("native request should not capture credentials")
	}
	// 但仍剥离，避免泄漏给本地 handler / 默认转发
	if ctx.Request.Header.Get("Authorization") != "" || ctx.Request.Header.Get("Cookie") != "" {
		t.Fatal("native request headers not stripped")
	}
}

func TestCaptureDistinguishesAbsentChecksum(t *testing.T) {
	target, _ := url.Parse("https://api2.cursor.sh/x")
	ctx := newTestContext(map[string]string{
		"Authorization": "Bearer t",
	}, target, SourceMITM)

	ctx.CaptureAndStripCredentials()

	if ctx.Credentials.ChecksumPresent {
		t.Fatal("checksum should be absent when not sent")
	}
}
