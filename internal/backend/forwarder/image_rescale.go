package forwarder

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"log"

	_ "image/gif" // 注册 gif 解码

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // 注册 webp 解码
)

// imageRescaleMaxEdge 是超限缩放时长边的目标上限（px）。
// 对齐 Claude 官方在 >20 图请求时的单图建议：每边 ≤ 2000px。
// 见 https://platform.claude.com/docs/en/docs/build-with-claude/vision （Request limits）。
const imageRescaleMaxEdge = 2000

// rescaleImageIfNeeded 解码图片字节，当原图字节超过 maxBytes 或长边超过 maxEdge 时，
// 用 CatmullRom 缩放到长边 ≤ maxEdge 并重编码，保留可用性。
//
// 设计目标：替换原先 data[:maxBytes] 的裸字节截断——二进制中间切片会损坏 PNG/JPEG 完整性
// 致上游解码失败。缩放重编码产出的是合法完整图像。
//
// 返回值：
//   - newData：重编码后的字节（未触发缩放时为原 data）
//   - newMIME：重编码后的 MIME（可能从 webp/gif 转为 image/png 或 image/jpeg；未触发时为原 mime）
//   - rescaled：是否实际做了缩放重编码
//   - err：解码/编码失败时返回非 nil
//
// MIME 变化由下游 imagePartContentType + imageFilenameForMIME 据字节嗅探正确处理，消费方无需感知。
// 解码失败时返回 (nil, "", false, err)，调用方据此「整张丢弃 + log」而非裸截断。
func rescaleImageIfNeeded(data []byte, mime string, maxBytes int, maxEdge int) ([]byte, string, bool, error) {
	if len(data) == 0 {
		return data, mime, false, nil
	}

	// 未超字节且无法判断尺寸时，不阻塞——但需解码才能知长边。
	// 统一策略：只要字节超限 OR 能解码且长边超限，就缩放。
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		// 解码失败（非图片或损坏）：无法缩放，返回错误让调用方丢弃整张，绝不裸截断。
		return nil, "", false, fmt.Errorf("decode image (mime=%s): %w", mime, err)
	}

	bounds := img.Bounds()
	longEdge := bounds.Dx()
	if bounds.Dy() > longEdge {
		longEdge = bounds.Dy()
	}

	needRescale := len(data) > maxBytes || (maxEdge > 0 && longEdge > maxEdge)
	if !needRescale {
		return data, mime, false, nil
	}

	// 计算目标尺寸：长边压到 maxEdge，等比缩放。
	targetLong := maxEdge
	if targetLong <= 0 || targetLong > longEdge {
		targetLong = longEdge // 不放大
	}
	scale := float64(targetLong) / float64(longEdge)
	dstW := maxInt(1, int(float64(bounds.Dx())*scale))
	dstH := maxInt(1, int(float64(bounds.Dy())*scale))

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)

	// 重编码：带 alpha 通道 → png；否则 jpeg（质量 85，体积更小）。
	// webp/gif 无法用标准库重编码，这里统一转 png/jpeg（已与用户确认转码保留可用性）。
	var buf bytes.Buffer
	newMIME := "image/jpeg"
	if hasAlpha(img) {
		newMIME = "image/png"
	}
	result, err := encodeRescaled(dst, newMIME, &buf)
	if err != nil {
		return nil, "", false, err
	}

	// 编码后若仍超 maxBytes（典型：高熵 PNG 即使缩放也压不下来），降级重试：
	// 1) 若原本是 png，先降到 jpeg q85（高熵内容 jpeg 体积远小于 png，代价是丢 alpha——
	//    合成白底避免透明区变黑）；2) 再降到 jpeg q60。
	// 目标是产出一个 ≤ maxBytes 的合法图，优于「裸截断损坏」或「整张丢弃无图可用」。
	if len(result) > maxBytes {
		result, newMIME = shrinkRescaled(dst, result, newMIME, maxBytes)
	}

	log.Printf("forwarder image rescale: format=%s mime=%s→%s %dx%d→%dx%d bytes=%d→%d (maxBytes=%d maxEdge=%d)",
		format, mime, newMIME, bounds.Dx(), bounds.Dy(), dstW, dstH, len(data), len(result), maxBytes, maxEdge)
	return result, newMIME, true, nil
}

// encodeRescaled 把缩放后的 RGBA 图按 mime 编码到 buf。
func encodeRescaled(dst *image.RGBA, mime string, buf *bytes.Buffer) ([]byte, error) {
	buf.Reset()
	switch mime {
	case "image/png":
		if err := png.Encode(buf, dst); err != nil {
			return nil, fmt.Errorf("encode png: %w", err)
		}
	default:
		if err := jpeg.Encode(buf, dst, &jpeg.Options{Quality: 85}); err != nil {
			return nil, fmt.Errorf("encode jpeg: %w", err)
		}
	}
	return buf.Bytes(), nil
}

// shrinkRescaled 在首次编码仍超 maxBytes 时逐级降级，返回 (新字节, 新MIME)。
// png→jpeg(白底,q85)→jpeg(q60)。各步失败则保留上一步结果（已是合法图，最差仍可外发）。
func shrinkRescaled(dst *image.RGBA, prev []byte, prevMIME string, maxBytes int) ([]byte, string) {
	// 若已是 jpeg，直接降质量到 q60。
	if prevMIME == "image/jpeg" {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 60}); err == nil && len(buf.Bytes()) < len(prev) {
			return buf.Bytes(), "image/jpeg"
		}
		return prev, prevMIME
	}
	// png → jpeg：高熵内容 jpeg 体积远小。先合成白底（避免透明区在 jpeg 里变黑）。
	flat := flattenOnWhite(dst)
	for _, q := range []int{85, 60} {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: q}); err == nil && len(buf.Bytes()) <= maxBytes {
			return buf.Bytes(), "image/jpeg"
		}
	}
	// 仍超 maxBytes：返回 jpeg q60（最小那次），虽超限但合法完整，优于裸截断。
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: 60}); err == nil && len(buf.Bytes()) < len(prev) {
		return buf.Bytes(), "image/jpeg"
	}
	return prev, prevMIME
}

// flattenOnWhite 把 RGBA 图合成到白底上，返回不透明 RGBA（供 jpeg 编码，避免透明区变黑）。
func flattenOnWhite(src *image.RGBA) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := src.At(x, y).RGBA()
			// alpha 混合白底（255,255,255）。RGBA() 返回 16 位预乘范围。
			ra := float64(a) / 0xffff
			dr := byte(float64(r)/0xffff*ra + (1-ra)*1.0*255)
			dg := byte(float64(g)/0xffff*ra + (1-ra)*1.0*255)
			db := byte(float64(bl)/0xffff*ra + (1-ra)*1.0*255)
			dst.SetRGBA(x, y, color.RGBA{R: dr, G: dg, B: db, A: 255})
		}
	}
	return dst
}

// hasAlpha 判断图像是否含透明通道（决定重编码为 png 还是 jpeg）。
func hasAlpha(img image.Image) bool {
	bounds := img.Bounds()
	// 采样而非全像素扫描，避免大图 O(n) 开销；角点 + 中心几个点足够判断典型情况。
	points := [][2]int{
		{bounds.Min.X, bounds.Min.Y},
		{bounds.Max.X - 1, bounds.Min.Y},
		{bounds.Min.X, bounds.Max.Y - 1},
		{bounds.Max.X - 1, bounds.Max.Y - 1},
		{(bounds.Min.X + bounds.Max.X) / 2, (bounds.Min.Y + bounds.Max.Y) / 2},
	}
	for _, p := range points {
		if _, _, _, a := img.At(p[0], p[1]).RGBA(); a != 0xffff {
			return true
		}
	}
	// RGBA/NRGBA 模型本身就带 alpha，即使采样点都不透明也按有 alpha 处理（避免误转 jpeg 丢通道）。
	switch img.ColorModel() {
	case color.RGBAModel, color.NRGBAModel, color.RGBA64Model, color.NRGBA64Model:
		return true
	}
	return false
}

// maxInt 返回两整数较大者（避免引入 math 仅为此）。
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
