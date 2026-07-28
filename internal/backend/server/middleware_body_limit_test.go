package server

import (
	"bytes"
	"errors"
	"io"
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

// TestHTTPHandlerActionLimitsBody (F-28) 验证 HTTPHandlerAction 用 http.MaxBytesReader
// 包了入站 body：handler 读超过 connectReadMaxBytes 的 body 时拿到 MaxBytesError，
// 且 net/http 自动写 413。connectrpc v1.19.1 无 WithReadMaxBytes，靠此边界包装兜底。
func TestHTTPHandlerActionLimitsBody(t *testing.T) {
	// dummy handler 读完全部 body，记录是否拿到 MaxBytesError。
	var sawMaxBytesErr bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				sawMaxBytesErr = true
			}
		}
	})

	rec := httptest.NewRecorder()
	body := bytes.NewReader(make([]byte, connectReadMaxBytes+1))
	req := httptest.NewRequest(http.MethodPost, "/", body)
	ctx := newContext(rec, req, Route{})

	action := HTTPHandlerAction(handler)
	if err := action(ctx); err != nil {
		t.Fatalf("HTTPHandlerAction returned error: %v", err)
	}

	if !sawMaxBytesErr {
		t.Fatal("F-28 FAIL: handler did not receive http.MaxBytesError for oversized body")
	}
}

// TestHTTPHandlerActionAllowsWithinLimit (F-28) 验证不超限的 body 正常读完整，
// 不被误截断。
func TestHTTPHandlerActionAllowsWithinLimit(t *testing.T) {
	var readAll bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("unexpected read error: %v", err)
		}
		if len(data) == 1024 {
			readAll = true
		}
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(make([]byte, 1024)))
	ctx := newContext(rec, req, Route{})

	if err := HTTPHandlerAction(handler)(ctx); err != nil {
		t.Fatalf("HTTPHandlerAction returned error: %v", err)
	}
	if !readAll {
		t.Error("F-28 FAIL: small body should be read in full without truncation")
	}
}
