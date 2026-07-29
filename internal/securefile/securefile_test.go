package securefile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// TestWriteViaTempSync_PreservesCallerMode 覆盖 N-10：
// 调用方指定 0644 时，写出的文件保持 0644（不收紧到 0600）——
// Cursor settings.json 是宿主应用的用户配置，权限语义不可改变。
func TestWriteViaTempSync_PreservesCallerMode(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "settings.json")
	if err := WriteViaTempSync(path, []byte(`{"http.proxy":"x"}`), 0o644); err != nil {
		t.Fatalf("WriteViaTempSync 0644: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("file perm = %o, want 0644 (caller-specified mode must be preserved)", info.Mode().Perm())
	}
	// 目录 0700。
	dInfo, _ := os.Stat(filepath.Join(dir, "sub"))
	if dInfo.Mode().Perm() != DirMode {
		t.Fatalf("dir perm = %o, want 0700", dInfo.Mode().Perm())
	}
}

// TestWriteViaTempSync_NoTempLeftBehind 覆盖 N-10/N-26：
// 写完后目录中不应残留 .tmp-* 临时文件（原子 rename 成功后清理）。
// 固定 .tmp 名在并发/重入下会碰撞——这里间接验证唯一 tmp 名 + rename 不残留。
func TestWriteViaTempSync_NoTempLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := WriteViaTempSync(path, []byte("payload"), 0o644); err != nil {
		t.Fatalf("WriteViaTempSync: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "settings.json.tmp-") {
			t.Errorf("temp file left behind after WriteViaTempSync: %s", e.Name())
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "payload" {
		t.Fatalf("content = %q, want %q", string(data), "payload")
	}
}

// TestWriteViaTempSync_OverwritesExistingAtomically 原子覆盖既有文件：
// 写入后内容完整替换，不出现部分写入。
func TestWriteViaTempSync_OverwritesExistingAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := WriteViaTempSync(path, []byte("old-content"), 0o644); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteViaTempSync(path, []byte("new-content"), 0o644); err != nil {
		t.Fatalf("second write: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "new-content" {
		t.Fatalf("content = %q, want new-content (atomic overwrite)", string(data))
	}
}

// TestTryCreateExclusive_FirstWinsSecondSkips 覆盖 N-11：
// O_EXCL 独占创建——第一次写入成功，第二次（已有文件）返回 ErrExist 而非覆盖。
func TestTryCreateExclusive_FirstWinsSecondSkips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.json")
	if err := TryCreateExclusive(path, []byte("original")); err != nil {
		t.Fatalf("first TryCreateExclusive: %v", err)
	}
	// 第二次尝试应失败（ErrExist），不覆盖原始内容。
	err := TryCreateExclusive(path, []byte("injected"))
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("second TryCreateExclusive should return ErrExist, got %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "original" {
		t.Errorf("exclusive create should not overwrite: got %q, want original", string(data))
	}
}
