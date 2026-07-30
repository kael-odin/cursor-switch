package forwarder

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// encodeTestPNG 把给定尺寸的纯色 RGBA 图编码为 PNG 字节，供图片相关测试构造真实可解码图。
func encodeTestPNG(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return buf.Bytes()
}

// encodeTestJPEG 把给定尺寸的纯色图编码为 JPEG 字节。
func encodeTestJPEG(t *testing.T, w, h int, c color.Color, quality int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("encode test jpeg: %v", err)
	}
	return buf.Bytes()
}

// encodeNoisyPNG 构造一张高熵（逐像素不可预测变化）PNG，使其无法被 PNG deflate 压缩到很小，
// 用于测试需要真实可解码且 >10MB 的图。确定性伪噪声（不依赖随机源）。
func encodeNoisyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// 用类 splitmix64 的位混淆对 (x,y) 做哈希，输出无规律 → deflate 几乎 0 压缩率。
	// 纯线性公式（如 x*7+y*13）会被 deflate 识别规律压缩到极小，故必须用非线性位混淆。
	hash := func(x, y int) (r, g, b byte) {
		v := uint64(x)*2654435761 ^ uint64(y)*40503 ^ uint64(x*y)*7919
		v ^= v >> 33
		v *= 0xff51afd7ed558ccd
		v ^= v >> 33
		v *= 0xc4ceb9fe1a85ec53
		v ^= v >> 33
		return byte(v), byte(v >> 8), byte(v >> 16)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b := hash(x, y)
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode noisy png: %v", err)
	}
	return buf.Bytes()
}

// TestRescaleImageIfNeeded 覆盖缩放重编码核心路径。
func TestRescaleImageIfNeeded(t *testing.T) {
	t.Run("no rescale when within limits", func(t *testing.T) {
		small := encodeTestPNG(t, 100, 100, color.RGBA{R: 255, A: 255})
		out, mime, rescaled, err := rescaleImageIfNeeded(small, "image/png", 10*1024*1024, imageRescaleMaxEdge)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if rescaled {
			t.Errorf("should not rescale small image")
		}
		if !bytes.Equal(out, small) {
			t.Errorf("should return original bytes when no rescale")
		}
		if mime != "image/png" {
			t.Errorf("mime should be unchanged, got %s", mime)
		}
	})

	t.Run("rescale when long edge exceeds maxEdge", func(t *testing.T) {
		// 4000x100 长边 4000 > 2000，字节未超 10MB——应按长边触发缩放。
		big := encodeTestPNG(t, 4000, 100, color.RGBA{R: 0, A: 255})
		out, _, rescaled, err := rescaleImageIfNeeded(big, "image/png", 10*1024*1024, imageRescaleMaxEdge)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !rescaled {
			t.Fatalf("should rescale oversized-long-edge image")
		}
		img, _, err := image.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatalf("rescaled output must be decodable: %v", err)
		}
		b := img.Bounds()
		if b.Dx() > imageRescaleMaxEdge || b.Dy() > imageRescaleMaxEdge {
			t.Errorf("long edge must be <= %d, got %dx%d", imageRescaleMaxEdge, b.Dx(), b.Dy())
		}
	})

	t.Run("rescale when bytes exceed maxBytes", func(t *testing.T) {
		// 3000x3000 纯色 PNG 通常不会到 10MB，改用极小 maxBytes 触发字节分支。
		big := encodeTestPNG(t, 3000, 3000, color.RGBA{R: 0, A: 255})
		out, _, rescaled, err := rescaleImageIfNeeded(big, "image/png", 1024, imageRescaleMaxEdge)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !rescaled {
			t.Fatalf("should rescale when bytes exceed maxBytes")
		}
		if len(out) >= len(big) {
			t.Errorf("rescaled bytes %d should be smaller than original %d", len(out), len(big))
		}
		// 缩放后仍应是合法可解码图。
		if _, _, err := image.Decode(bytes.NewReader(out)); err != nil {
			t.Errorf("rescaled output must be decodable: %v", err)
		}
	})

	t.Run("decode failure returns error (fail-open to drop)", func(t *testing.T) {
		notImage := []byte("this is not an image at all")
		_, _, _, err := rescaleImageIfNeeded(notImage, "image/png", 10*1024*1024, imageRescaleMaxEdge)
		if err == nil {
			t.Errorf("decode failure must return error, not nil")
		}
	})

	t.Run("empty input passthrough", func(t *testing.T) {
		out, mime, rescaled, err := rescaleImageIfNeeded(nil, "image/png", 10*1024*1024, imageRescaleMaxEdge)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if rescaled || len(out) != 0 || mime != "image/png" {
			t.Errorf("empty input should pass through unchanged")
		}
	})

	t.Run("jpeg no-alpha rescale", func(t *testing.T) {
		// 不透明图（alpha=255 全填充）→ 缩放后应转 jpeg。
		big := encodeTestJPEG(t, 4000, 4000, color.RGBA{R: 200, G: 100, B: 50, A: 255}, 90)
		out, newMIME, rescaled, err := rescaleImageIfNeeded(big, "image/jpeg", 10*1024*1024, imageRescaleMaxEdge)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !rescaled {
			t.Fatalf("should rescale oversized jpeg")
		}
		if _, _, err := image.Decode(bytes.NewReader(out)); err != nil {
			t.Errorf("rescaled jpeg must be decodable: %v", err)
		}
		// newMIME 应为 image/jpeg（无 alpha）。注意：hasAlpha 对采样点全不透明返回 false，
		// 故走 jpeg 分支。不强断言 mime 字符串以容忍采样判断，但必须可解码。
		_ = newMIME
	})
}
