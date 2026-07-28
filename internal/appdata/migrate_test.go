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
	setTestHome(t, tmp)

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

// --- F-29: 旧目录迁移不删唯一副本 ---

// setTestHome 把 os.UserHomeDir() 重定向到临时目录（F-29 测试用）。
// Windows 上 os.UserHomeDir() 读 USERPROFILE 而非 HOME，需同时设置两者。
func setTestHome(t *testing.T, tmp string) {
	t.Helper()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
}

// TestMigrateLegacyPreservesOldDirOnSuccess (F-29) 验证迁移成功后旧目录被 rename
// 成 .migrated-bak 备份（而非 RemoveAll），新目录有内容 + 标记文件存在。
func TestMigrateLegacyPreservesOldDirOnSuccess(t *testing.T) {
	tmp := t.TempDir()
	setTestHome(t, tmp)

	// 造旧目录：config.yaml + rules/a.txt
	legacy := legacyRootDir()
	_ = os.MkdirAll(filepath.Join(legacy, "rules"), 0o755)
	_ = os.WriteFile(filepath.Join(legacy, "config.yaml"), []byte("log: true"), 0o644)
	_ = os.WriteFile(filepath.Join(legacy, "rules", "a.txt"), []byte("rule-a"), 0o644)

	if err := EnsureAssistantHome(); err != nil {
		t.Fatalf("EnsureAssistantHome: %v", err)
	}

	// 新目录有迁移过来的内容。
	got, _ := os.ReadFile(filepath.Join(RootDir(), "config.yaml"))
	if string(got) != "log: true" {
		t.Errorf("F-29: migrated config.yaml content mismatch: %q", got)
	}
	gotRule, _ := os.ReadFile(filepath.Join(RulesRootPath(), "a.txt"))
	if string(gotRule) != "rule-a" {
		t.Errorf("F-29: migrated rule a.txt mismatch: %q", gotRule)
	}

	// 标记文件存在。
	if ok, _ := fileExists(migrationMarkerPath()); !ok {
		t.Error("F-29: migration marker should exist after success")
	}

	// 旧目录被改名（不存在于原路径），备份目录存在且内容一致。
	if ok, _ := fileExists(legacy); ok {
		t.Error("F-29: legacy dir should have been renamed away, not remain at original path")
	}
	bak := legacyBackupDir()
	if ok, _ := fileExists(bak); !ok {
		t.Fatalf("F-29: backup dir %s should exist", bak)
	}
	bakCfg, _ := os.ReadFile(filepath.Join(bak, "config.yaml"))
	if string(bakCfg) != "log: true" {
		t.Errorf("F-29: backup config.yaml mismatch: %q", bakCfg)
	}
}

// TestMigrateLegacyDoesNotOverwriteExistingTarget (F-29) 验证新目录已有 config.yaml
// 时不被旧目录内容覆盖（用户在用新目录）。
func TestMigrateLegacyDoesNotOverwriteExistingTarget(t *testing.T) {
	tmp := t.TempDir()
	setTestHome(t, tmp)

	// 先建新目录并写一个用户正在用的 config.yaml。
	if err := EnsureAssistantHome(); err != nil {
		t.Fatalf("first EnsureAssistantHome: %v", err)
	}
	newCfg := filepath.Join(RootDir(), "config.yaml")
	_ = os.WriteFile(newCfg, []byte("log: false"), 0o600)

	// 再造旧目录 config.yaml（内容不同）。
	legacy := legacyRootDir()
	_ = os.MkdirAll(legacy, 0o755)
	_ = os.WriteFile(filepath.Join(legacy, "config.yaml"), []byte("log: true"), 0o644)

	if err := EnsureAssistantHome(); err != nil {
		t.Fatalf("second EnsureAssistantHome: %v", err)
	}

	// 新目录 config.yaml 内容不变（未被覆盖）。
	got, _ := os.ReadFile(newCfg)
	if string(got) != "log: false" {
		t.Errorf("F-29: existing config.yaml should not be overwritten, got %q", got)
	}
}

// TestMigrateLegacyKeepsOldDirOnCopyFailure (F-29) 验证复制失败时旧目录不被删/改名、
// 标记不写、启动不阻断。用 chmod 000 制造源不可读（非 root Unix）。
func TestMigrateLegacyKeepsOldDirOnCopyFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based read denial not meaningful on Windows")
	}
	tmp := t.TempDir()
	setTestHome(t, tmp)

	legacy := legacyRootDir()
	_ = os.MkdirAll(legacy, 0o755)
	// 源 config.yaml 存在但不可读（chmod 000）。
	srcCfg := filepath.Join(legacy, "config.yaml")
	_ = os.WriteFile(srcCfg, []byte("log: true"), 0o644)
	_ = os.Chmod(srcCfg, 0o000)
	defer func() { _ = os.Chmod(srcCfg, 0o644) }()

	// 确认 chmod 000 真能拒绝读（root/特殊 fs 会绕过，此时跳过而非误报）。
	if canReadDespiteMode000(srcCfg) {
		t.Skip("running as root or fs ignores unix perms; can't simulate read failure")
	}

	if err := EnsureAssistantHome(); err != nil {
		t.Fatalf("EnsureAssistantHome should not block startup on migrate failure: %v", err)
	}

	// 旧目录仍存在（未被删/改名）。
	if ok, _ := fileExists(legacy); !ok {
		t.Error("F-29: legacy dir should still exist after copy failure (was deleted/renamed)")
	}
	// 标记不应存在（迁移没成功）。
	if ok, _ := fileExists(migrationMarkerPath()); ok {
		t.Error("F-29: marker should NOT exist after copy failure")
	}
	// 备份目录不应存在。
	if ok, _ := fileExists(legacyBackupDir()); ok {
		t.Error("F-29: backup dir should NOT exist after copy failure")
	}
}

// TestMigrateLegacySkipsWhenMarkerExists (F-29) 验证标记存在时整次跳过（幂等）：
// 旧目录原样未被触碰、不被改名。
func TestMigrateLegacySkipsWhenMarkerExists(t *testing.T) {
	tmp := t.TempDir()
	setTestHome(t, tmp)

	// 先建新目录 + 写标记。
	if err := EnsureAssistantHome(); err != nil {
		t.Fatalf("first EnsureAssistantHome: %v", err)
	}
	_ = os.WriteFile(migrationMarkerPath(), []byte("pre-existing-marker"), 0o600)

	// 造旧目录（标记存在时应被跳过，旧目录不应被改名）。
	legacy := legacyRootDir()
	_ = os.MkdirAll(legacy, 0o755)
	_ = os.WriteFile(filepath.Join(legacy, "config.yaml"), []byte("log: true"), 0o644)

	if err := EnsureAssistantHome(); err != nil {
		t.Fatalf("second EnsureAssistantHome: %v", err)
	}

	// 旧目录仍在原路径（未被改名成备份）。
	if ok, _ := fileExists(legacy); !ok {
		t.Error("F-29: legacy dir should remain in place when marker exists (was renamed)")
	}
	if ok, _ := fileExists(legacyBackupDir()); ok {
		t.Error("F-29: backup dir should NOT be created when marker exists")
	}
}

// TestMigrateLegacyNoOpWhenNoLegacyDir (F-29) 验证旧目录不存在时不报错、无副作用。
func TestMigrateLegacyNoOpWhenNoLegacyDir(t *testing.T) {
	tmp := t.TempDir()
	setTestHome(t, tmp)

	if err := EnsureAssistantHome(); err != nil {
		t.Fatalf("EnsureAssistantHome: %v", err)
	}
	// 旧目录不存在 → 不写标记、不建备份。
	if ok, _ := fileExists(migrationMarkerPath()); ok {
		t.Error("F-29: marker should not be written when no legacy dir exists")
	}
	if ok, _ := fileExists(legacyBackupDir()); ok {
		t.Error("F-29: backup dir should not exist when no legacy dir exists")
	}
}
