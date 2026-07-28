package appdata

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestEnsureAssistantHomeCreates0700DirsAndMigrates (F-18) 验证启动迁移：
// 既有 0644/0755 的宽松文件在 EnsureAssistantHome 后被收紧到 0600/0700。
// 用临时 HOME 重定向 RootDir。
func TestEnsureAssistantHomeCreates0700DirsAndMigrates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm bits not meaningful on Windows")
	}
	tmp := t.TempDir()
	// RootDir 由 os.UserHomeDir() 推导（Unix 下读 HOME），重定向到临时目录隔离测试。
	t.Setenv("HOME", tmp)

	// 先在目标 root 下造一批宽松文件（模拟旧版残留），root 可能尚未存在——
	// 直接在 DataRootPath 下造，但 root 必须先存在才能写文件。
	// 用一次 EnsureAssistantHome 建出目录，再人为放宽，再调一次验证迁移。
	if err := EnsureAssistantHome(); err != nil {
		t.Fatalf("first EnsureAssistantHome: %v", err)
	}

	// 造一个宽松 config + 宽松 messages 文件 + 放宽目录权限
	looseFile := filepath.Join(DataRootPath(), "config.yaml")
	_ = os.WriteFile(looseFile, []byte("apiKey: sk-leak"), 0o644)
	_ = os.Chmod(looseFile, 0o644)
	// 把 data 目录本身也放宽到 0755
	_ = os.Chmod(DataRootPath(), 0o755)
	_ = os.Chmod(RootDir(), 0o755)

	// 再次 EnsureAssistantHome → 启动迁移 EnsureTree 应收紧。
	if err := EnsureAssistantHome(); err != nil {
		t.Fatalf("second EnsureAssistantHome: %v", err)
	}

	// root 0700
	rInfo, _ := os.Stat(RootDir())
	if rInfo.Mode().Perm() != 0o700 {
		t.Fatalf("root perm=%o want 0700", rInfo.Mode().Perm())
	}
	// data dir 0700
	dInfo, _ := os.Stat(DataRootPath())
	if dInfo.Mode().Perm() != 0o700 {
		t.Fatalf("data dir perm=%o want 0700", dInfo.Mode().Perm())
	}
	// loose config 0600
	fInfo, _ := os.Stat(looseFile)
	if fInfo.Mode().Perm() != 0o600 {
		t.Fatalf("config.yaml perm=%o want 0600", fInfo.Mode().Perm())
	}
}
