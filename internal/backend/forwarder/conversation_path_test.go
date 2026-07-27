package forwarder

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateConversationIDRejectsTraversal 验证 F-04：conversation ID 目录逃逸防护。
func TestValidateConversationIDRejectsTraversal(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"normal id", "abc123", true},
		{"uuid", "12faf918-6325-4ed9-900f-2fd8c1c75888", true},
		{"empty", "", false},
		{"whitespace only", "   ", false},
		{"forward slash", "a/b", false},
		{"backslash", "a\\b", false},
		{"dot segment", ".", false},
		{"dotdot segment", "..", false},
		{"dotdot prefix", "..hidden", true}, // 不是分隔符也不是纯 ..，Clean 不变，放行（不构成逃逸）
		{"leading slash absolute", "/etc", false},
		{"trailing slash", "abc/", false},
		{"double slash", "a//b", false},
		{"dot segment with slash", "./a", false},
		{"dotdot with slash", "../a", false},
		{"windows drive", "C:file", true}, // ":" 不被 validateConversationID 拒绝（仅 sanitize 处理），且不构成逃逸
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := validateConversationID(c.in)
			if c.ok {
				if err != nil {
					t.Fatalf("expected ok, got error: %v (in=%q)", err, c.in)
				}
				if got != strings.TrimSpace(c.in) {
					t.Errorf("expected normalized %q, got %q", strings.TrimSpace(c.in), got)
				}
			} else {
				if err == nil {
					t.Fatalf("expected rejection for %q, got %q", c.in, got)
				}
			}
		})
	}
}

// TestValidateConversationIDPathStaysInRoot 验证拼到 store.root 后仍在根内。
func TestValidateConversationIDPathStaysInRoot(t *testing.T) {
	root := "/tmp/history"
	cases := []string{"abc", "12faf918-6325-4ed9-900f-2fd8c1c75888"}
	for _, id := range cases {
		validated, err := validateConversationID(id)
		if err != nil {
			t.Fatalf("valid id %q rejected: %v", id, err)
		}
		full := filepath.Join(root, validated)
		rel, err := filepath.Rel(root, full)
		if err != nil {
			t.Fatalf("Rel error for %q: %v", id, err)
		}
		if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("id %q escapes root (rel=%q)", id, rel)
		}
	}
}

// TestSanitizeArtifactNameRejectsTraversal 验证 debug artifact 名也拦 "."/".."。
func TestSanitizeArtifactNameRejectsTraversal(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"abc", "abc"},
		{"a/b", "a_b"},
		{"a\\b", "a_b"},
		{"", "unknown"},
		{".", "unknown"},
		{"..", "unknown"},
		{"   ", "unknown"},
	}
	for _, c := range cases {
		got := sanitizeArtifactName(c.in)
		if got != c.want {
			t.Errorf("sanitizeArtifactName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestResolveConversationPathSafe 验证二次 Rel 校验在合法 ID 上正常工作。
func TestResolveConversationPathSafe(t *testing.T) {
	store := &ConversationFileStore{root: "/tmp/history"}
	got, err := store.resolveConversationPath("abc123", "context.json")
	if err != nil {
		t.Fatalf("expected ok, got: %v", err)
	}
	want := filepath.Clean("/tmp/history/abc123/context.json")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestResolveConversationPathRejectsTraversal 验证逃逸输入被拦。
func TestResolveConversationPathRejectsTraversal(t *testing.T) {
	store := &ConversationFileStore{root: "/tmp/history"}
	for _, id := range []string{"", ".", "..", "../etc", "/etc"} {
		if _, err := store.resolveConversationPath(id, "context.json"); err == nil {
			t.Errorf("expected rejection for %q, got nil", id)
		}
	}
}
