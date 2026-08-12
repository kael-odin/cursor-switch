package forwarder

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestPNG 写一个合法 PNG 文件到 path。noise 为 true 时填充随机像素（编码后体量接近 raw，
// 用于构造 >10MB 单图上限、<32MB 读盘硬上限的大图），否则 8x8 纯色小图。
func writeTestPNG(t *testing.T, path string, noise bool) {
	t.Helper()
	side := 8
	if noise {
		side = 2600 // 2600² RGBA 随机噪声 PNG ≈27MB（不可压缩），命中 rescale 分支且不触发 32MB 读盘硬上限
	}
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	if noise {
		rng := rand.New(rand.NewSource(42))
		raw := make([]byte, side*side*4)
		_, _ = rng.Read(raw)
		copy(img.Pix, raw)
	} else {
		for y := 0; y < side; y++ {
			for x := 0; x < side; x++ {
				img.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode png %s: %v", path, err)
	}
}

// TestLoadReferenceImagesSizeLimits 验证 loadReferenceImages 的 F-30 对齐防护（#14）：
// 参考图路径不再是「无上限 ReadFile 后原样外发」，而是复用内联路径的预算约束——
// 单图 ≤10MB（超限缩放重编码）、总预算 32MB、最多 6 张、读盘前 stat 拦截超大文件。
func TestLoadReferenceImagesSizeLimits(t *testing.T) {
	root := t.TempDir()
	stream := &ActiveStream{WorkspacePaths: []string{root}}

	t.Run("small valid png passes through unchanged", func(t *testing.T) {
		writeTestPNG(t, filepath.Join(root, "small.png"), false)
		refs, err := loadReferenceImages(stream, []string{"small.png"})
		if err != nil {
			t.Fatalf("loadReferenceImages: %v", err)
		}
		if len(refs) != 1 {
			t.Fatalf("refs = %d, want 1", len(refs))
		}
		if refs[0].filename != "small.png" {
			t.Errorf("filename = %q, want small.png", refs[0].filename)
		}
		if len(refs[0].data) == 0 {
			t.Errorf("data empty, want PNG bytes")
		}
		// 未超限不缩放：mimeType 保持空，由下游 imagePartContentType 字节嗅探
		if refs[0].mimeType != "" {
			t.Errorf("mimeType = %q, want empty (no rescale needed)", refs[0].mimeType)
		}
	})

	t.Run("file over read cap dropped before read", func(t *testing.T) {
		// 33MB 非图片 blob：stat 硬上限（总预算 32MB）拦截，不读盘不解码
		big := filepath.Join(root, "huge.bin")
		if err := os.WriteFile(big, make([]byte, 33*1024*1024), 0o644); err != nil {
			t.Fatalf("write huge: %v", err)
		}
		_, err := loadReferenceImages(stream, []string{"huge.bin"})
		if err == nil || !strings.Contains(err.Error(), "dropped") {
			t.Fatalf("expected all-dropped error, got %v", err)
		}
	})

	t.Run("oversized file mixed with valid file keeps valid", func(t *testing.T) {
		writeTestPNG(t, filepath.Join(root, "ok.png"), false)
		big := filepath.Join(root, "huge2.bin")
		if err := os.WriteFile(big, make([]byte, 33*1024*1024), 0o644); err != nil {
			t.Fatalf("write huge2: %v", err)
		}
		refs, err := loadReferenceImages(stream, []string{"ok.png", "huge2.bin"})
		if err != nil {
			t.Fatalf("partial drop should not error: %v", err)
		}
		if len(refs) != 1 || refs[0].filename != "ok.png" {
			t.Errorf("refs = %+v, want only ok.png", refs)
		}
	})

	t.Run("oversized png rescaled under per-image cap", func(t *testing.T) {
		writeTestPNG(t, filepath.Join(root, "big.png"), true)
		info, err := os.Stat(filepath.Join(root, "big.png"))
		if err != nil {
			t.Fatalf("stat big.png: %v", err)
		}
		if info.Size() <= int64(promptGuardSelectedImageMaxBytes) {
			t.Fatalf("fixture big.png only %d bytes, need > %d to trigger rescale", info.Size(), promptGuardSelectedImageMaxBytes)
		}
		refs, err := loadReferenceImages(stream, []string{"big.png"})
		if err != nil {
			t.Fatalf("loadReferenceImages: %v", err)
		}
		if len(refs) != 1 {
			t.Fatalf("refs = %d, want 1", len(refs))
		}
		if len(refs[0].data) > promptGuardSelectedImageMaxBytes {
			t.Errorf("rescaled ref bytes = %d, want ≤ %d", len(refs[0].data), promptGuardSelectedImageMaxBytes)
		}
		if refs[0].mimeType == "" {
			t.Errorf("rescaled ref mimeType = %q, want set (png/jpeg)", refs[0].mimeType)
		}
	})

	t.Run("count capped at six", func(t *testing.T) {
		paths := make([]string, 0, promptGuardSelectedImageMaxCount+1)
		for i := 1; i <= promptGuardSelectedImageMaxCount+1; i++ {
			name := fmt.Sprintf("ref%d.png", i)
			writeTestPNG(t, filepath.Join(root, name), false)
			paths = append(paths, name)
		}
		refs, err := loadReferenceImages(stream, paths)
		if err != nil {
			t.Fatalf("count-cap should not error: %v", err)
		}
		if len(refs) != promptGuardSelectedImageMaxCount {
			t.Fatalf("refs = %d, want %d", len(refs), promptGuardSelectedImageMaxCount)
		}
		if refs[len(refs)-1].filename != "ref6.png" {
			t.Errorf("last kept = %q, want ref6.png (7th dropped)", refs[len(refs)-1].filename)
		}
	})
}
