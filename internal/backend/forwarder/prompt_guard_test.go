package forwarder

import (
	"bytes"
	"image"
	"testing"

	"cursor/gen/agentv1"
)

// newImageWithData 构造带内联 data 的 SelectedImage（oneof 字段需用 wrapper 类型）。
func newImageWithPathAndData(path string, data []byte) *agentv1.SelectedImage {
	return &agentv1.SelectedImage{
		Path:           path,
		DataOrBlobId:   &agentv1.SelectedImage_Data{Data: data},
	}
}

func newImageWithBlobData(path string, data []byte) *agentv1.SelectedImage {
	return &agentv1.SelectedImage{
		Path:         path,
		DataOrBlobId: &agentv1.SelectedImage_BlobIdWithData_{BlobIdWithData: &agentv1.SelectedImage_BlobIdWithData{Data: data}},
	}
}

// TestGuardSelectedImagesStripsPathAndDropsPathOnly 验证 F-30：
// 仅含 path 无内联 data 的 SelectedImage 被丢弃，且 Path 字段被清空。
func TestGuardSelectedImagesStripsPathAndDropsPathOnly(t *testing.T) {
	images := []*agentv1.SelectedImage{
		// 合法：带内联 data（但同时给了 path，应被清空）
		newImageWithPathAndData("/tmp/should-be-stripped.png", []byte{0x89, 0x50, 0x4E, 0x47}),
		// 非法：仅 path 无 data——必须丢弃，不得触发服务端读文件
		{Path: "/etc/passwd"},
		// 合法：blob_id_with_data 形态
		newImageWithBlobData("/tmp/x.jpg", []byte("fake-jpeg")),
	}
	got := guardSelectedImages(images)
	if len(got) != 2 {
		t.Fatalf("expected 2 images (path-only dropped), got %d", len(got))
	}
	for _, img := range got {
		if img.GetPath() != "" {
			t.Errorf("Path must be stripped, got %q", img.GetPath())
		}
		if len(img.GetData()) == 0 {
			t.Errorf("every kept image must have inline Data")
		}
	}
}

// TestGuardSelectedImageCountLimit 验证数量上限。
func TestGuardSelectedImageCountLimit(t *testing.T) {
	images := make([]*agentv1.SelectedImage, 0, promptGuardSelectedImageMaxCount+3)
	for i := 0; i < promptGuardSelectedImageMaxCount+3; i++ {
		images = append(images, newImageWithPathAndData("", []byte{0x89, 0x50}))
	}
	got := guardSelectedImages(images)
	if len(got) != promptGuardSelectedImageMaxCount {
		t.Fatalf("expected %d images, got %d", promptGuardSelectedImageMaxCount, len(got))
	}
}

// TestGuardSelectedImageSizeLimit 验证单图字节上限——超 10MB 走缩放重编码（长边≤2000px），
// 输出仍是合法可解码图且 < maxBytes，而非裸字节截断。
func TestGuardSelectedImageSizeLimit(t *testing.T) {
	// 构造一张真实可解码且 > 10MB 的 PNG：高熵像素（逐像素变化）使 PNG 无法有效压缩。
	// 3000x3000 RGBA = 36MB 原始，高熵 PNG 编码后远超 10MB。
	oversized := encodeNoisyPNG(t, 3000, 3000)
	if len(oversized) <= promptGuardSelectedImageMaxBytes {
		t.Fatalf("test fixture too small: %d bytes, need > %d", len(oversized), promptGuardSelectedImageMaxBytes)
	}
	got := guardSelectedImages([]*agentv1.SelectedImage{newImageWithPathAndData("", oversized)})
	if len(got) != 1 {
		t.Fatalf("expected 1 image, got %d", len(got))
	}
	data := got[0].GetData()
	if len(data) == 0 {
		t.Fatalf("expected non-empty image data")
	}
	if len(data) >= len(oversized) {
		t.Errorf("rescaled bytes %d should be smaller than original %d", len(data), len(oversized))
	}
	if len(data) > promptGuardSelectedImageMaxBytes {
		t.Errorf("rescaled bytes %d must be <= maxBytes %d", len(data), promptGuardSelectedImageMaxBytes)
	}
	// 缩放后必须是合法可解码图（裸截断会损坏二进制，解码必失败）。
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Errorf("rescaled image must be decodable, got err: %v (raw truncation would break it)", err)
	}
	// 缩放后长边 ≤ 2000。
	if img != nil {
		b := img.Bounds()
		if b.Dx() > imageRescaleMaxEdge || b.Dy() > imageRescaleMaxEdge {
			t.Errorf("long edge must be <= %d after rescale, got %dx%d", imageRescaleMaxEdge, b.Dx(), b.Dy())
		}
	}
}

// TestGuardSelectedImagesTotalBudget 验证总量预算——多张超 10MB 大图缩放后总量不超过 32MB。
func TestGuardSelectedImagesTotalBudget(t *testing.T) {
	// 每张 >10MB 的高熵 PNG（3000x3000），缩放后变 2000x2000 级别（远小于 10MB）。
	// 用 4 张验证总量预算（4×缩放后 << 32MB）。
	single := encodeNoisyPNG(t, 3000, 3000)
	images := []*agentv1.SelectedImage{
		newImageWithPathAndData("", single),
		newImageWithPathAndData("", single),
		newImageWithPathAndData("", single),
		newImageWithPathAndData("", single),
	}
	got := guardSelectedImages(images)
	total := 0
	for _, img := range got {
		total += len(img.GetData())
	}
	if total > promptGuardSelectedImagesTotalBytes {
		t.Errorf("total %d exceeds budget %d", total, promptGuardSelectedImagesTotalBytes)
	}
}

// TestGuardSelectedImagesNil 验证 nil / 空输入防御。
func TestGuardSelectedImagesNil(t *testing.T) {
	if got := guardSelectedImages(nil); got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
	if got := guardSelectedImages([]*agentv1.SelectedImage{}); got != nil {
		t.Errorf("expected nil for empty input, got %v", got)
	}
}

// TestNormalizeUserMessageForStorageStripsImagePath 验证端到端：
// 经 normalizeUserMessageForStorage 后，SelectedImages 的 Path 被清空。
func TestNormalizeUserMessageForStorageStripsImagePath(t *testing.T) {
	msg := &agentv1.UserMessage{
		Text: "hi",
		SelectedContext: &agentv1.SelectedContext{
			SelectedImages: []*agentv1.SelectedImage{
				newImageWithPathAndData("/secret/path.png", []byte{0x89, 0x50, 0x4E, 0x47}),
			},
		},
	}
	normalized := normalizeUserMessageForStorage(msg)
	images := normalized.GetSelectedContext().GetSelectedImages()
	if len(images) != 1 {
		t.Fatalf("expected 1 image retained, got %d", len(images))
	}
	if images[0].GetPath() != "" {
		t.Errorf("Path must be stripped after normalize, got %q", images[0].GetPath())
	}
}
