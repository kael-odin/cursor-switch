package modeladapter

import (
	"strings"
	"testing"
)

// TestResolveImageContentRejectsPath 验证 F-30：服务端绝不读客户端提供的 Path。
// 即便 ImageContent 只带 path 不带 data，也必须报错而非 os.ReadFile。
func TestResolveImageContentRejectsPath(t *testing.T) {
	cases := []struct {
		name string
		img  *ImageContent
		// 期望：err 非空，且不返回任何 payload（避免文件内容外泄）
	}{
		{"path only", &ImageContent{Path: "/etc/passwd"}},
		{"path + empty data", &ImageContent{Path: "C:\\Windows\\System32\\drivers\\etc\\hosts", Data: nil}},
		{"symlink-ish path", &ImageContent{Path: "../../etc/shadow"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload, _, err := resolveImageContent(c.img)
			if err == nil {
				t.Fatalf("expected error for %q, got nil (payload len=%d)", c.name, len(payload))
			}
			if len(payload) != 0 {
				t.Errorf("expected no payload returned, got %d bytes", len(payload))
			}
			if !strings.Contains(err.Error(), "F-30") && !strings.Contains(err.Error(), "inline data") {
				t.Errorf("error should mention inline data requirement, got: %v", err)
			}
		})
	}
}

// TestResolveImageContentAcceptsInlineData 验证内联 data 仍正常工作。
func TestResolveImageContentAcceptsInlineData(t *testing.T) {
	data := []byte{0x89, 0x50, 0x4E, 0x47} // PNG 魔数前 4 字节
	payload, mime, err := resolveImageContent(&ImageContent{Data: data, MIMEType: "image/png"})
	if err != nil {
		t.Fatalf("expected ok for inline data, got: %v", err)
	}
	if string(payload) != string(data) {
		t.Errorf("payload mismatch")
	}
	if mime != "image/png" {
		t.Errorf("expected image/png, got %q", mime)
	}
}

// TestResolveImageContentNilImage 验证 nil 防御。
func TestResolveImageContentNilImage(t *testing.T) {
	if _, _, err := resolveImageContent(nil); err == nil {
		t.Fatal("expected error for nil image")
	}
}
