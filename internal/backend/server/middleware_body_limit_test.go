package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWriteServerErrorMapsBodyTooLargeTo413 (F-28) 验证 writeServerError 把
// ErrCompatRouteBodyTooLarge 映射为 413 Request Entity Too Large。
func TestWriteServerErrorMapsBodyTooLargeTo413(t *testing.T) {
	rec := httptest.NewRecorder()
	writeServerError(rec, ErrCompatRouteBodyTooLarge)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestWriteServerErrorMapsBidiPayloadTo400 验证 ErrInvalidBidiAppendPayload 仍映射为 400（回归保险）。
func TestWriteServerErrorMapsBidiPayloadTo400(t *testing.T) {
	rec := httptest.NewRecorder()
	writeServerError(rec, ErrInvalidBidiAppendPayload)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
