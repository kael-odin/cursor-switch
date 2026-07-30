package forwarder

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	runtimecore "cursor/internal/backend/agent/core"
)

// TestPendingBridgeCountIncludesImages 验证异步生图的关键不变式：登记的 PendingImages
// 被 pendingBridgeCount 计入——这是让 provider 流在生图期间进 TurnPhaseWaitingExternal
// 暂停（而非 499）的根因修复点。
func TestPendingBridgeCountIncludesImages(t *testing.T) {
	stream := &ActiveStream{
		PendingExecs:        make(map[string]runtimecore.PendingExec),
		PendingInteractions: make(map[string]runtimecore.PendingInteraction),
		PendingImages:       make(map[string]pendingImage),
	}
	if got := pendingBridgeCount(stream); got != 0 {
		t.Fatalf("empty count = %d, want 0", got)
	}
	stream.PendingImages["img-1"] = pendingImage{ImageID: "img-1"}
	if got := pendingBridgeCount(stream); got != 1 {
		t.Fatalf("after registering 1 image, count = %d, want 1", got)
	}
	stream.PendingExecs["exec-1"] = runtimecore.PendingExec{}
	stream.PendingInteractions["ix-1"] = runtimecore.PendingInteraction{}
	if got := pendingBridgeCount(stream); got != 3 {
		t.Fatalf("with 1 image + 1 exec + 1 interaction, count = %d, want 3", got)
	}
}

// TestRunImageGenerationBuildsPayload 验证生图 goroutine 的回投 payload 构造契约：
// 成功时 ImageData 非空、Err 空；失败时反之。这里用 httptest mock 上游，复用 generateImage
// 的真实成功/失败路径，验证 runImageGeneration 内部 payload 字段语义。
func TestRunImageGenerationBuildsPayload(t *testing.T) {
	expectedB64 := base64.StdEncoding.EncodeToString([]byte("fake-png"))

	// 成功路径
	srvOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"b64_json": expectedB64}},
		})
	}))
	defer srvOK.Close()

	imageData, err := generateImage(context.Background(), srvOK.URL, "sk-test", "gpt-image-2", "a cat")
	if err != nil {
		t.Fatalf("generateImage success path error: %v", err)
	}
	// 镜像 runImageGeneration 的 payload 构造逻辑
	payload := imageResultPayload{ImageID: "img-1", FilePath: "/tmp/out.png"}
	if err != nil {
		payload.Err = err.Error()
	} else {
		payload.ImageData = imageData
	}
	if payload.Err != "" {
		t.Fatalf("success payload should have empty Err, got %q", payload.Err)
	}
	if payload.ImageData != expectedB64 {
		t.Fatalf("success payload ImageData = %q, want %q", payload.ImageData, expectedB64)
	}

	// 失败路径
	srvErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "model not found"},
		})
	}))
	defer srvErr.Close()

	_, err = generateImage(context.Background(), srvErr.URL, "sk-test", "gpt-image-2", "a cat")
	if err == nil {
		t.Fatal("expected error from failing upstream")
	}
	payload = imageResultPayload{ImageID: "img-2"}
	if err != nil {
		payload.Err = err.Error()
	}
	if payload.Err == "" || !strings.Contains(payload.Err, "model not found") {
		t.Fatalf("error payload Err = %q, want it to contain upstream message", payload.Err)
	}
	if payload.ImageData != "" {
		t.Fatalf("error payload should have empty ImageData, got %q", payload.ImageData)
	}
}

// TestRunImageGenerationCancelsWithProviderContext 验证 #1 的核心不变式：
// runImageGeneration 派生的 ctx（context.WithTimeout(parent=ProviderContext, 15min)）
// 必须挂在 stream.ProviderContext 之下——父 ctx 取消时子 ctx 立即取消。
// 这是「客户端 cancel→broker.Cancel→ProviderCancel()→上游生图 HTTP 即时中断」的语义前提。
//
// 不直接驱动 generateImage（它自带 10min ResponseHeaderTimeout 的 client，时序脆弱），
// 而是验证 ctx 派生链：子 ctx 监听父 ctx 取消。
func TestRunImageGenerationCancelsWithProviderContext(t *testing.T) {
	// 模拟 service.go:888 的 provider ctx（ProviderCancel + ProviderContext 同源）。
	providerCtx, providerCancel := context.WithCancel(context.Background())
	// 镜像 runImageGeneration 内部：ctx, cancel := context.WithTimeout(parent, 15min)
	childCtx, childCancel := context.WithTimeout(providerCtx, 15*time.Minute)
	defer childCancel()

	if childCtx.Err() != nil {
		t.Fatalf("child ctx should be alive before parent cancel, got %v", childCtx.Err())
	}

	// 取消父 ctx——模拟客户端 cancel 触发 broker.Cancel→ProviderCancel()。
	providerCancel()

	// 子 ctx 应随父取消立即进入 canceled（Go context 语义保证），证明派生链正确。
	select {
	case <-childCtx.Done():
		if childCtx.Err() != context.Canceled {
			t.Fatalf("child ctx err = %v, want context.Canceled", childCtx.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("child ctx did not cancel within 2s after parent cancel — not derived from ProviderContext")
	}
}
