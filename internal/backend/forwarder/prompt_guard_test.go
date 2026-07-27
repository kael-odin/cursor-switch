package forwarder

import (
	"bytes"
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

// TestGuardSelectedImageSizeLimit 验证单图字节上限——超限被截断到上限。
func TestGuardSelectedImageSizeLimit(t *testing.T) {
	oversized := bytes.Repeat([]byte{0x41}, promptGuardSelectedImageMaxBytes+1024)
	got := guardSelectedImages([]*agentv1.SelectedImage{newImageWithPathAndData("", oversized)})
	if len(got) != 1 {
		t.Fatalf("expected 1 image, got %d", len(got))
	}
	if len(got[0].GetData()) != promptGuardSelectedImageMaxBytes {
		t.Errorf("expected truncated to %d bytes, got %d", promptGuardSelectedImageMaxBytes, len(got[0].GetData()))
	}
}

// TestGuardSelectedImagesTotalBudget 验证总量预算——多张大图不超过总量。
func TestGuardSelectedImagesTotalBudget(t *testing.T) {
	single := bytes.Repeat([]byte{0x42}, promptGuardSelectedImageMaxBytes) // 4MB
	images := []*agentv1.SelectedImage{
		newImageWithPathAndData("", single),
		newImageWithPathAndData("", single),
		newImageWithPathAndData("", single),
		newImageWithPathAndData("", single), // 4×4MB=16MB，正好达到上限
		newImageWithPathAndData("", single), // 第 5 张会超总量——应被截断或丢弃
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
