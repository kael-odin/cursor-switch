package forwarder

import (
	"testing"

	"cursor/gen/agentv1"
)

// TestExtractCurrentTurnSelectedImages 验证图生图直取 inline 的入站快照：
// 从 UserMessage.SelectedContext.SelectedImages 提取 inline data 构造 []imageReference，
// 复用 F-30 上限（单图 4MB / 总 16MB / 最多 6 张），仅含 path 无 data 的条目被丢弃。
func TestExtractCurrentTurnSelectedImages(t *testing.T) {
	msg := &agentv1.UserMessage{
		SelectedContext: &agentv1.SelectedContext{
			SelectedImages: []*agentv1.SelectedImage{
				// 合法：inline data（PNG magic bytes）+ 声明 mime
				{Path: "/tmp/a.png", MimeType: "image/png", DataOrBlobId: &agentv1.SelectedImage_Data{Data: []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A}}},
				// 仅 path 无 data——必须丢弃（F-30：服务端不读文件系统）
				{Path: "/etc/passwd"},
				// 合法：blob_id_with_data 形态 + 声明 mime
				{Path: "/tmp/b.jpg", MimeType: "image/jpeg", DataOrBlobId: &agentv1.SelectedImage_BlobIdWithData_{BlobIdWithData: &agentv1.SelectedImage_BlobIdWithData{Data: []byte{0xFF, 0xD8, 0xFF, 0xE0}}}},
			},
		},
	}
	refs := extractCurrentTurnSelectedImages(msg)
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs (path-only dropped), got %d", len(refs))
	}
	if len(refs[0].data) != 6 {
		t.Fatalf("ref[0] data len = %d, want 6", len(refs[0].data))
	}
	// filename 应据 mime/嗅探推扩展名——PNG → .png
	if refs[0].filename != "reference.png" {
		t.Fatalf("ref[0] filename = %q, want reference.png", refs[0].filename)
	}
	if refs[1].filename != "reference.jpg" {
		t.Fatalf("ref[1] filename = %q, want reference.jpg (jpeg sniffed)", refs[1].filename)
	}
	// mimeType 应原样保留声明值，供 multipart part Content-Type 用（绝不落 octet-stream）。
	if refs[0].mimeType != "image/png" {
		t.Fatalf("ref[0] mimeType = %q, want image/png", refs[0].mimeType)
	}
	if refs[1].mimeType != "image/jpeg" {
		t.Fatalf("ref[1] mimeType = %q, want image/jpeg", refs[1].mimeType)
	}
	// 副本语义：改 refs 不应影响再次提取
	refs[0].data[0] = 0
	refs2 := extractCurrentTurnSelectedImages(msg)
	if refs2[0].data[0] != 0x89 {
		t.Fatalf("extract should return independent copies; got mutated data")
	}
}

// TestExtractCurrentTurnSelectedImagesNil 验证 nil 安全。
func TestExtractCurrentTurnSelectedImagesNil(t *testing.T) {
	if got := extractCurrentTurnSelectedImages(nil); got != nil {
		t.Fatalf("nil message should yield nil, got %v", got)
	}
	if got := extractCurrentTurnSelectedImages(&agentv1.UserMessage{}); got != nil {
		t.Fatalf("empty message should yield nil, got %v", got)
	}
}

// TestSnapshotCurrentTurnSelectedImages 验证 stream 快照读取：返回副本、nil 安全。
func TestSnapshotCurrentTurnSelectedImages(t *testing.T) {
	if got := snapshotCurrentTurnSelectedImages(nil); got != nil {
		t.Fatalf("nil stream should yield nil, got %v", got)
	}
	stream := &ActiveStream{}
	if got := snapshotCurrentTurnSelectedImages(stream); got != nil {
		t.Fatalf("empty stream should yield nil, got %v", got)
	}
	stream.mu.Lock()
	stream.CurrentTurnSelectedImages = []imageReference{
		{filename: "a.png", data: []byte("a")},
		{filename: "b.jpg", data: []byte("bb")},
	}
	stream.mu.Unlock()
	got := snapshotCurrentTurnSelectedImages(stream)
	if len(got) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(got))
	}
	// 副本：改 got 不影响 stream
	got[0].data[0] = 'x'
	stream.mu.Lock()
	if stream.CurrentTurnSelectedImages[0].data[0] != 'a' {
		t.Fatalf("snapshot should return a copy; stream data was mutated")
	}
	stream.mu.Unlock()
}

// TestImagePartContentType 验证 multipart image part 的 Content-Type 决定：
// 声明 mime 优先、否则字节嗅探，绝不返回非 image/*（octet-stream 会被上游 /v1/images/edits 400 拒绝）。
func TestImagePartContentType(t *testing.T) {
	cases := []struct {
		mime    string
		payload []byte
		want    string
	}{
		{"image/png", nil, "image/png"},
		{"image/jpeg", nil, "image/jpeg"},
		{"IMAGE/JPEG", nil, "image/jpeg"}, // 归一大小写
		{"  image/webp  ", nil, "image/webp"},
		{"", []byte{0x89, 0x50, 0x4E, 0x47}, "image/png"},      // PNG sniff
		{"", []byte{0xFF, 0xD8, 0xFF}, "image/jpeg"},            // JPEG sniff
		{"", []byte("plain text"), "image/png"},                // 非 image → 默认 png
		{"", nil, "image/png"},                                  // 全空 → 默认 png
		{"application/octet-stream", []byte{0x89, 0x50}, "image/png"}, // 声明非 image → 嗅探兜底 png
	}
	for _, c := range cases {
		if got := imagePartContentType(c.mime, c.payload); got != c.want {
			t.Fatalf("imagePartContentType(%q, %v) = %q, want %q", c.mime, c.payload, got, c.want)
		}
	}
}

// TestImageFilenameForMIME 验证 mime→扩展名映射与嗅探回退。
func TestImageFilenameForMIME(t *testing.T) {
	cases := []struct {
		mime    string
		payload []byte
		want    string
	}{
		{"image/png", nil, "reference.png"},
		{"image/jpeg", nil, "reference.jpg"},
		{"image/gif", nil, "reference.gif"},
		{"image/webp", nil, "reference.webp"},
		{"", []byte{0x89, 0x50, 0x4E, 0x47}, "reference.png"},   // PNG sniff
		{"", []byte{0xFF, 0xD8, 0xFF}, "reference.jpg"},          // JPEG sniff
		{"", []byte("text"), "reference.png"},                    // 非 image → 默认 png
		{"", nil, "reference.png"},                               // 全空 → 默认 png
	}
	for _, c := range cases {
		if got := imageFilenameForMIME(c.mime, c.payload); got != c.want {
			t.Fatalf("imageFilenameForMIME(%q, %v) = %q, want %q", c.mime, c.payload, got, c.want)
		}
	}
}
