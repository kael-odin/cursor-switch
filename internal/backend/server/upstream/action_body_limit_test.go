package upstream

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"cursor/internal/backend/server"
)

// TestCompatRouteRejectsBodyOverLimit (F-28) 验证 compat 路由入站 body 超过 maxCompatRouteBodyBytes
// 时返回包装 server.ErrCompatRouteBodyTooLarge 的错误，而非用无界 io.ReadAll 耗尽内存。
func TestCompatRouteRejectsBodyOverLimit(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), int(maxCompatRouteBodyBytes)+1)
	ctx := &server.Context{
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(oversized)),
	}
	_, _, err := newCompatRouteObjects(ctx, Dependencies{}, CompatRouteConfig{Name: "test"})
	if err == nil {
		t.Fatalf("expected body-too-large error, got nil")
	}
	if !errors.Is(err, server.ErrCompatRouteBodyTooLarge) {
		t.Fatalf("expected ErrCompatRouteBodyTooLarge, got %v", err)
	}
}

// TestCompatRouteAcceptsBodyAtLimit 验证恰好在上限内的 body 被接受。
func TestCompatRouteAcceptsBodyAtLimit(t *testing.T) {
	atLimit := bytes.Repeat([]byte("x"), int(maxCompatRouteBodyBytes))
	ctx := &server.Context{
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(atLimit)),
	}
	reqCtx, _, err := newCompatRouteObjects(ctx, Dependencies{}, CompatRouteConfig{Name: "test"})
	if err != nil {
		t.Fatalf("body at limit should be accepted, got %v", err)
	}
	if len(reqCtx.RequestBody) != int(maxCompatRouteBodyBytes) {
		t.Fatalf("request body length mismatch: got %d", len(reqCtx.RequestBody))
	}
}
