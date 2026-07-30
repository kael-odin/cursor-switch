package forwarder

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEditImageSuccess 验证图生图：调 /v1/images/edits multipart，成功返回 base64。
//
// 图生图在文生图之后补：reference_image_paths 非空时走 edits 端点。这里 mock 上游，
// 确认 multipart 含 model/prompt/n/response_format 字段 + image 文件 part，且解析 b64_json。
func TestEditImageSuccess(t *testing.T) {
	expectedB64 := "ZmFrZS1wbmctZWRpdA=="
	var gotFields map[string]string
	var gotImagePart []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/images/edits") {
			t.Errorf("unexpected path %q, want /v1/images/edits", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-edit" {
			t.Errorf("auth = %q, want Bearer sk-edit", r.Header.Get("Authorization"))
		}
		// 解析 multipart
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		gotFields = map[string]string{}
		for _, key := range []string{"model", "prompt", "n", "response_format"} {
			gotFields[key] = r.FormValue(key)
		}
		file, header, err := r.FormFile("image")
		if err != nil {
			t.Fatalf("FormFile image: %v", err)
		}
		defer file.Close()
		gotImagePart, _ = io.ReadAll(file)
		_ = header
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"b64_json": expectedB64}},
		})
	}))
	defer srv.Close()

	refs := []imageReference{{filename: "cat.png", data: []byte("raw-png-bytes")}}
	got, err := editImage(context.Background(), srv.URL, "sk-edit", "gpt-image-2", "add a hat", refs)
	if err != nil {
		t.Fatalf("editImage error: %v", err)
	}
	if got != expectedB64 {
		t.Fatalf("got %q, want %q", got, expectedB64)
	}
	if gotFields["model"] != "gpt-image-2" {
		t.Errorf("model field = %q, want gpt-image-2", gotFields["model"])
	}
	if gotFields["prompt"] != "add a hat" {
		t.Errorf("prompt field = %q, want 'add a hat'", gotFields["prompt"])
	}
	if gotFields["response_format"] != "b64_json" {
		t.Errorf("response_format = %q, want b64_json", gotFields["response_format"])
	}
	if string(gotImagePart) != "raw-png-bytes" {
		t.Errorf("image part = %q, want 'raw-png-bytes'", string(gotImagePart))
	}
}

// TestEditImageAPIError 验证 edits 端点报错时返回友好错误（提 error.message + 状态码）。
func TestEditImageAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "invalid api key"},
		})
	}))
	defer srv.Close()

	refs := []imageReference{{filename: "cat.png", data: []byte("x")}}
	_, err := editImage(context.Background(), srv.URL, "sk-bad", "gpt-image-2", "edit", refs)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid api key") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error %q should contain upstream message + 401", err.Error())
	}
}

// TestEditImageNoReferences 验证空参考图切片直接报错（不发包）。
func TestEditImageNoReferences(t *testing.T) {
	_, err := editImage(context.Background(), "http://example.com", "sk", "gpt-image-2", "edit", nil)
	if err == nil || !strings.Contains(err.Error(), "at least one reference image") {
		t.Fatalf("expected no-references error, got %v", err)
	}
}

// TestLoadReferenceImages 验证参考图按工作区路径解析并读取字节。
// 路径相对工作区根 → join 后能读到；找不到的路径友好报错。
func TestLoadReferenceImages(t *testing.T) {
	root := t.TempDir()
	imgPath := filepath.Join(root, "ref.png")
	if err := os.WriteFile(imgPath, []byte("png-bytes"), 0o644); err != nil {
		t.Fatalf("write ref: %v", err)
	}
	stream := &ActiveStream{WorkspacePaths: []string{root}}

	refs, err := loadReferenceImages(stream, []string{"ref.png"})
	if err != nil {
		t.Fatalf("loadReferenceImages error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].filename != "ref.png" {
		t.Errorf("filename = %q, want ref.png", refs[0].filename)
	}
	if string(refs[0].data) != "png-bytes" {
		t.Errorf("data = %q, want png-bytes", string(refs[0].data))
	}

	// 绝对路径也该能用
	refs2, err := loadReferenceImages(stream, []string{imgPath})
	if err != nil {
		t.Fatalf("absolute path error: %v", err)
	}
	if string(refs2[0].data) != "png-bytes" {
		t.Errorf("absolute data = %q", string(refs2[0].data))
	}

	// 找不到的路径 → 友好报错，提到原路径
	_, err = loadReferenceImages(stream, []string{"nope.png"})
	if err == nil || !strings.Contains(err.Error(), "nope.png") {
		t.Fatalf("expected not-found error mentioning nope.png, got %v", err)
	}
}
