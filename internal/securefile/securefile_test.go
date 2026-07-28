package securefile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// skipOnWindows 跳过权限断言：Windows perm 位语义不同，0600/0700 不被保留。
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("perm bits not meaningful on Windows")
	}
}

func TestWriteFileCreates0600(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "secret.yaml")
	if err := WriteFile(path, []byte("apiKey: sk-test")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != FileMode {
		t.Fatalf("file perm = %o, want 0600", info.Mode().Perm())
	}
	// 目录应 0700
	dInfo, _ := os.Stat(filepath.Join(dir, "sub"))
	if dInfo.Mode().Perm() != DirMode {
		t.Fatalf("dir perm = %o, want 0700", dInfo.Mode().Perm())
	}
}

func TestWriteFileTightensExistingLooseFile(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// 先以宽松权限创建（模拟旧版 0644 文件）。
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 写回应把它收紧到 0600。
	if err := WriteFile(path, []byte("new")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != FileMode {
		t.Fatalf("loose file not tightened: perm=%o want 0600", info.Mode().Perm())
	}
}

func TestWriteViaTempTightens(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "atomic.yaml")
	if err := WriteViaTemp(path, ".tmp", []byte("data")); err != nil {
		t.Fatalf("WriteViaTemp: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != FileMode {
		t.Fatalf("perm=%o want 0600", info.Mode().Perm())
	}
	// 临时文件应已不存在
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file should be removed, err=%v", err)
	}
}

func TestEnsureTreeTightensExistingLooseFiles(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	// 造一批宽松文件/目录
	looseDir := filepath.Join(root, "data")
	_ = os.MkdirAll(looseDir, 0o755)
	_ = os.WriteFile(filepath.Join(looseDir, "config.yaml"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(looseDir, "messages.jsonl"), []byte("x"), 0o644)
	// root 自身也宽松
	_ = os.Chmod(root, 0o755)

	if err := EnsureTree(root); err != nil {
		t.Fatalf("EnsureTree: %v", err)
	}
	// root 0700
	rInfo, _ := os.Stat(root)
	if rInfo.Mode().Perm() != DirMode {
		t.Fatalf("root perm=%o want 0700", rInfo.Mode().Perm())
	}
	// data dir 0700
	dInfo, _ := os.Stat(looseDir)
	if dInfo.Mode().Perm() != DirMode {
		t.Fatalf("data dir perm=%o want 0700", dInfo.Mode().Perm())
	}
	// files 0600
	for _, name := range []string{"config.yaml", "messages.jsonl"} {
		fInfo, _ := os.Stat(filepath.Join(looseDir, name))
		if fInfo.Mode().Perm() != FileMode {
			t.Fatalf("%s perm=%o want 0600", name, fInfo.Mode().Perm())
		}
	}
}

func TestEnsureTreeSkipsSymlinks(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	target := filepath.Join(root, "outside-secret")
	_ = os.WriteFile(target, []byte("do-not-touch"), 0o600)
	// 在 root 内建符号链接指向外部文件
	link := filepath.Join(root, "link")
	_ = os.Symlink(target, link)
	_ = os.Chmod(target, 0o600) // 保持

	// 即使 target 当前 0600，EnsureTree 不应跟随链接去改 target。
	// 这里主要验证不 panic、不报错。
	if err := EnsureTree(root); err != nil {
		t.Fatalf("EnsureTree over symlink: %v", err)
	}
	// target 仍可读
	if _, err := os.ReadFile(target); err != nil {
		t.Fatalf("target unreadable after EnsureTree: %v", err)
	}
}

func TestEnsureModeNoOpWhenAlreadyStrict(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "strict")
	_ = os.WriteFile(path, []byte("x"), 0o600)
	if err := EnsureMode(path, FileMode); err != nil {
		t.Fatalf("EnsureMode: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != FileMode {
		t.Fatalf("perm changed unexpectedly: %o", info.Mode().Perm())
	}
}

func TestEnsureTreeNonexistentRoot(t *testing.T) {
	// 不存在的 root 应不报错返回（路径可能尚未创建）。
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := EnsureTree(missing); err != nil {
		t.Fatalf("EnsureTree on missing root should not error: %v", err)
	}
}
