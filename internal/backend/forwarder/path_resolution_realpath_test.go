package forwarder

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPathWithinWorkspaceRealRejectsSymlinkEscape 是 F-32 的核心回归：
// workspace 内一个指向区外的 symlink，词法 pathWithinWorkspace 会判其位于 workspace 内
// 而放行，realpath 版必须拒绝。
//
// 跳过条件：非超级用户在部分平台上无法创建 symlink（Windows 需开发者模式或管理员）。
// 若 CreateSymlink 失败则 t.Skip，不记为失败。
func TestPathWithinWorkspaceRealRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	escapeTarget := t.TempDir() // 区外目录
	escapeFile := filepath.Join(escapeTarget, "secret.txt")
	if err := os.WriteFile(escapeFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// workspace 内放一个 symlink 指向区外文件。
	linkInsideWorkspace := filepath.Join(root, "leak.txt")
	if err := os.Symlink(escapeFile, linkInsideWorkspace); err != nil {
		t.Skipf("cannot create symlink (need dev mode/admin on Windows): %v", err)
	}

	// 词法版会误判 leak.txt 在 workspace 内（它确实是 workspace 内的一个名字）。
	if !pathWithinWorkspace(linkInsideWorkspace, root) {
		t.Fatalf("lexical pathWithinWorkspace should treat symlink name as inside workspace (baseline assumption)")
	}
	// realpath 版必须识破 symlink 指向区外。
	if pathWithinWorkspaceReal(linkInsideWorkspace, root) {
		t.Fatalf("F-32 FAIL: pathWithinWorkspaceReal accepted symlink escaping workspace (link=%s -> %s)", linkInsideWorkspace, escapeFile)
	}
}

// TestPathWithinWorkspaceRealAcceptsLegitimateInsideWorkspace 验证 workspace 内的
// 真实文件（非 symlink 逃逸）realpath 版仍放行，且不存在的目标文件也能通过父目录校验。
func TestPathWithinWorkspaceRealAcceptsLegitimateInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	subdir := filepath.Join(root, "src")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(subdir, "main.go")
	if err := os.WriteFile(existing, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 已存在文件。
	if !pathWithinWorkspaceReal(existing, root) {
		t.Fatalf("F-32 FAIL: legitimate existing file rejected: %s", existing)
	}
	// 不存在的新文件——靠父目录 realpath 校验。
	newFile := filepath.Join(subdir, "new.go")
	if !pathWithinWorkspaceReal(newFile, root) {
		t.Fatalf("F-32 FAIL: legitimate new file (parent exists) rejected: %s", newFile)
	}
}

// TestPathWithinWorkspaceRealRejectsNonExistentRootOutside 验证 root 存在但候选
// 解析到完全不可达位置时 fail-closed。
func TestPathWithinWorkspaceRealRejectsNonExistentRootOutside(t *testing.T) {
	root := t.TempDir()
	// 一个绝对不存在的深度路径，其所有祖先也不存在——evalSymlinksAncestorAware 走到根。
	ghost := filepath.Join(root, "ghost1", "ghost2", "ghost3", "file.txt")
	// root 存在，所以走 realpath 分支；候选祖先链最终回退到 root 自身（存在），
	// 拼回 ghost1/... —— 词法上仍在 root 下，应放行（这是合法的"写一个深层新文件"）。
	if !pathWithinWorkspaceReal(ghost, root) {
		t.Fatalf("F-32 FAIL: deep new file under existing root should be allowed: %s", ghost)
	}
}

// TestEnsureDeletePathWithinWorkspaceFailClosedNoContext 验证缺失 workspace 上下文时
// Delete fail-closed（F-32：此前 Delete 原样下发，无任何围栏）。
func TestEnsureDeletePathWithinWorkspaceFailClosedNoContext(t *testing.T) {
	stream := &ActiveStream{}
	_, err := ensureDeletePathWithinWorkspace(stream, `/etc/passwd`)
	if err == nil {
		t.Fatal("F-32 FAIL: ensureDeletePathWithinWorkspace should fail-closed when workspace context missing")
	}
	if !strings.Contains(err.Error(), "workspace context missing") {
		t.Fatalf("expected fail-closed error, got: %v", err)
	}
}

// TestEnsureDeletePathWithinWorkspaceRejectsOutside 验证有 workspace 但路径在区外时拒绝。
func TestEnsureDeletePathWithinWorkspaceRejectsOutside(t *testing.T) {
	root := t.TempDir()
	if runtime.GOOS == "windows" {
		// 用一个肯定在 root 之外的绝对路径。
		outside := filepath.Join(os.TempDir(), "cursor-f32-outside-delete.txt")
		stream := &ActiveStream{WorkspacePaths: []string{root}}
		_, err := ensureDeletePathWithinWorkspace(stream, outside)
		if err == nil {
			t.Fatalf("F-32 FAIL: ensureDeletePathWithinWorkspace accepted outside path %s (root=%s)", outside, root)
		}
		return
	}
	stream := &ActiveStream{WorkspacePaths: []string{root}}
	_, err := ensureDeletePathWithinWorkspace(stream, "/etc/passwd")
	if err == nil {
		t.Fatal("F-32 FAIL: ensureDeletePathWithinWorkspace accepted /etc/passwd outside workspace")
	}
}

// TestEnsureDownloadPathWithinWorkspaceRejectsTraversal 验证 downloadPath 的 `..` 逃逸被拒。
func TestEnsureDownloadPathWithinWorkspaceRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	stream := &ActiveStream{WorkspacePaths: []string{root}}
	_, err := ensureDownloadPathWithinWorkspace(stream, filepath.Join(root, "..", "escape.txt"))
	if err == nil {
		t.Fatal("F-32 FAIL: ensureDownloadPathWithinWorkspace accepted parent-traversal path")
	}
}

// TestEnsureDownloadPathWithinWorkspaceAllowsEmpty 验证空 downloadPath（流式返回）放行。
func TestEnsureDownloadPathWithinWorkspaceAllowsEmpty(t *testing.T) {
	stream := &ActiveStream{}
	fenced, err := ensureDownloadPathWithinWorkspace(stream, "  ")
	if err != nil {
		t.Fatalf("empty downloadPath should be allowed (stream mode), got err: %v", err)
	}
	if fenced != "" {
		t.Fatalf("empty downloadPath should return empty fenced path, got %q", fenced)
	}
}

// TestEnsureWritePathWithinWorkspaceFailClosedNoContext 验证写入也 fail-closed
// （F-32：移除了此前"无上下文放行绝对路径"的回退）。
func TestEnsureWritePathWithinWorkspaceFailClosedNoContext(t *testing.T) {
	stream := &ActiveStream{}
	_, err := ensureWritePathWithinWorkspace(stream, `/tmp/evil.txt`)
	if err == nil {
		t.Fatal("F-32 FAIL: ensureWritePathWithinWorkspace should fail-closed when workspace context missing (old fallback removed)")
	}
	if !strings.Contains(err.Error(), "workspace context missing") {
		t.Fatalf("expected fail-closed error, got: %v", err)
	}
}

// TestRewriteDeletePathAndDownloadPath 验证 ArgsJSON 改写助手正确覆盖路径字段且保留其他字段。
func TestRewriteDeletePathAndDownloadPath(t *testing.T) {
	// Delete：保留 toolCallId 等未知字段，覆盖 path。
	deleteJSON := []byte(`{"path":"/etc/passwd","toolCallId":"call-1"}`)
	fencedDelete := filepath.Join(t.TempDir(), "ws", "file.txt")
	rewritten, err := rewriteDeletePath(deleteJSON, fencedDelete)
	if err != nil {
		t.Fatal(err)
	}
	gotPath := extractDeletePath(rewritten)
	if gotPath != fencedDelete {
		t.Fatalf("rewriteDeletePath: path not overwritten, got %q want %q", gotPath, fencedDelete)
	}
	gotDelete := extractDeletePath(rewritten)
	if gotDelete != fencedDelete {
		t.Fatalf("rewriteDeletePath roundtrip mismatch: %q", gotDelete)
	}

	// downloadPath：覆盖 downloadPath，保留 server/uri。
	dlJSON := []byte(`{"server":"fs","uri":"file:///x","downloadPath":"/tmp/evil"}`)
	rewrittenDL, err := rewriteDownloadPath(dlJSON, filepath.Join(t.TempDir(), "ws", "res.bin"))
	if err != nil {
		t.Fatal(err)
	}
	gotDL := extractDownloadPath(rewrittenDL)
	if !strings.HasSuffix(gotDL, "res.bin") {
		t.Fatalf("rewriteDownloadPath: downloadPath not overwritten, got %q", gotDL)
	}

	// 空 downloadPath：应移除键。
	rewrittenEmpty, err := rewriteDownloadPath(dlJSON, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := extractDownloadPath(rewrittenEmpty); got != "" {
		t.Fatalf("empty fenced downloadPath should remove key, got %q", got)
	}
}
