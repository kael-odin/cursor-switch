package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"cursor/internal/appdata"
	"cursor/internal/logger"
)

// setTestHome 把 HOME/USERPROFILE 重定向到临时目录，使 resolveCursorSettingsPath 与
// appdata.RootDir 都落在测试隔离目录。Windows 上 os.UserHomeDir 读 USERPROFILE，
// unix 读 HOME——两者都设。
//
// 关键坑（见 logger.CloseLogFile 文档）：cursor 包写 settings 时会调 logger.Info，
// logger 单例首次写会在当前 HOME 下打开 app.log 并长期持有句柄，导致 t.TempDir 的
// RemoveAll 清理报 "being used by another process"。故 cleanup 调 CloseLogFile 释放。
func setTestHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
		t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	} else if runtime.GOOS == "darwin" {
		t.Setenv("HOME", home) // darwin 也读 HOME
	}
	t.Cleanup(func() {
		logger.CloseLogFile()
	})
}

// writeCursorSettingsFile 在测试 home 下写出 Cursor settings.json 内容。
func writeCursorSettingsFile(t *testing.T, home string, content string) {
	t.Helper()
	path := testCursorSettingsPath(t, home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

func testCursorSettingsPath(t *testing.T, home string) string {
	t.Helper()
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", "Cursor", "User", "settings.json")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "settings.json")
	default:
		return filepath.Join(home, ".config", "Cursor", "User", "settings.json")
	}
}

func readCursorSettings(t *testing.T, home string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(testCursorSettingsPath(t, home))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	// 复用既有 JSONC 解析（settings.go 里已剥离注释）。
	parsed, err := decodeCursorSettingsJSONC(data)
	if err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	return parsed
}

// TestWriteUserProxySettings_BackupsAndRestoresOriginalValues 核心契约：用户接管前有
// 自己的 http.proxy，接管后退出，原始代理值必须被还原而非抹掉。
func TestWriteUserProxySettings_BackupsAndRestoresOriginalValues(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	// 用户接管前的原始配置：有自己的代理 + 一个无关键（退出后应保留）。
	writeCursorSettingsFile(t, home, `{
  "http.proxy": "http://user-original:9000",
  "editor.fontSize": 14
}`)

	if err := WriteUserProxySettings("http://127.0.0.1:8080"); err != nil {
		t.Fatalf("WriteUserProxySettings: %v", err)
	}
	afterApply := readCursorSettings(t, home)
	if got := afterApply["http.proxy"]; got != "http://127.0.0.1:8080" {
		t.Errorf("after apply http.proxy=%v, want injected", got)
	}
	if got := afterApply["editor.fontSize"]; got != float64(14) {
		t.Errorf("unrelated key editor.fontSize lost: %v", got)
	}

	// 退出：应从备份还原用户原始代理，而非 delete。
	if err := ClearUserProxySettings(); err != nil {
		t.Fatalf("ClearUserProxySettings: %v", err)
	}
	afterClear := readCursorSettings(t, home)
	if got := afterClear["http.proxy"]; got != "http://user-original:9000" {
		t.Errorf("after clear http.proxy=%v, want original user value restored", got)
	}
	// 无关键仍保留（退出只动注入键）。
	if got := afterClear["editor.fontSize"]; got != float64(14) {
		t.Errorf("unrelated key editor.fontSize lost on clear: %v", got)
	}
	// 注入键清掉。
	if _, exists := afterClear["cursor.general.disableHttp2"]; exists {
		t.Errorf("injected key disableHttp2 should be removed after clear")
	}

	// 备份文件还原后清理。
	if _, err := os.Stat(appdata.CursorSettingsBackupPath()); !os.IsNotExist(err) {
		t.Errorf("backup file should be removed after restore, err=%v", err)
	}
}

// TestWriteUserProxySettings_NoOriginalProxyDeletesOnClear 用户接管前无 http.proxy，
// 备份记录 exists=false，退出应删掉注入键（还原到"无"）。
func TestWriteUserProxySettings_NoOriginalProxyDeletesOnClear(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	writeCursorSettingsFile(t, home, `{"editor.fontSize": 14}`)

	if err := WriteUserProxySettings("http://127.0.0.1:8080"); err != nil {
		t.Fatalf("WriteUserProxySettings: %v", err)
	}
	if err := ClearUserProxySettings(); err != nil {
		t.Fatalf("ClearUserProxySettings: %v", err)
	}
	afterClear := readCursorSettings(t, home)
	if _, exists := afterClear["http.proxy"]; exists {
		t.Errorf("http.proxy should be absent after clear (no original to restore)")
	}
	if got := afterClear["editor.fontSize"]; got != float64(14) {
		t.Errorf("unrelated key lost: %v", got)
	}
}

// TestRestoreCursorSettingsFromCrash_RestoresFromBackup 崩溃恢复：settings 残留注入键 +
// 有备份 → 自动还原原始值。
func TestRestoreCursorSettingsFromCrash_RestoresFromBackup(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	writeCursorSettingsFile(t, home, `{
  "http.proxy": "http://127.0.0.1:8080",
  "cursor.general.disableHttp2": true,
  "editor.fontSize": 14
}`)
	// 模拟接管前的备份快照：用户原始代理 + disableHttp2 接管前不存在。
	backup := cursorSettingsBackupRecord{
		Keys:   map[string]any{"http.proxy": "http://user-original:9000", "cursor.general.disableHttp2": nil},
		Exists: map[string]bool{"http.proxy": true, "cursor.general.disableHttp2": false},
	}
	if err := os.MkdirAll(filepath.Dir(appdata.CursorSettingsBackupPath()), 0o700); err != nil {
		t.Fatalf("mkdir backup dir: %v", err)
	}
	encoded, _ := json.MarshalIndent(backup, "", "  ")
	if err := os.WriteFile(appdata.CursorSettingsBackupPath(), append(encoded, '\n'), 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	if err := RestoreCursorSettingsFromCrash(); err != nil {
		t.Fatalf("RestoreCursorSettingsFromCrash: %v", err)
	}
	restored := readCursorSettings(t, home)
	if got := restored["http.proxy"]; got != "http://user-original:9000" {
		t.Errorf("crash restore http.proxy=%v, want original", got)
	}
	if _, exists := restored["cursor.general.disableHttp2"]; exists {
		t.Errorf("crash restore should delete injected disableHttp2 (no original)")
	}
	if got := restored["editor.fontSize"]; got != float64(14) {
		t.Errorf("unrelated key lost in crash restore: %v", got)
	}
	// 备份清理。
	if _, err := os.Stat(appdata.CursorSettingsBackupPath()); !os.IsNotExist(err) {
		t.Errorf("backup should be removed after crash restore, err=%v", err)
	}
}

// TestRestoreCursorSettingsFromCrash_NoBackupClearsLeftover 残留注入键但无备份：
// best-effort 清注入键（无法还原原始值，退回"无代理"）。
func TestRestoreCursorSettingsFromCrash_NoBackupClearsLeftover(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	writeCursorSettingsFile(t, home, `{
  "http.proxy": "http://127.0.0.1:8080",
  "cursor.general.disableHttp2": true
}`)
	// 确保无备份文件。
	os.Remove(appdata.CursorSettingsBackupPath())

	if err := RestoreCursorSettingsFromCrash(); err != nil {
		t.Fatalf("RestoreCursorSettingsFromCrash: %v", err)
	}
	restored := readCursorSettings(t, home)
	if _, exists := restored["http.proxy"]; exists {
		t.Errorf("leftover http.proxy should be cleared when no backup")
	}
	if _, exists := restored["cursor.general.disableHttp2"]; exists {
		t.Errorf("leftover disableHttp2 should be cleared when no backup")
	}
}

// TestRestoreCursorSettingsFromCrash_NoLeftoverNoop 无残留注入键 → no-op。
func TestRestoreCursorSettingsFromCrash_NoLeftoverNoop(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	writeCursorSettingsFile(t, home, `{"editor.fontSize": 14, "http.proxy": "http://user-own:9000"}`)
	// 注意：http.proxy 是用户自己的，但因为不含 cursor-switch 专有的注入键集合
	// （如 cursor.general.disableHttp2），settingsHasInjectedKeys 应返回 false → no-op。
	// 实际上 http.proxy 本就在 injectedCursorSettingsKeys 里，故会触发——改用纯用户键。
	writeCursorSettingsFile(t, home, `{"editor.fontSize": 14, "git.enableSmartCommit": true}`)

	before, _ := os.ReadFile(testCursorSettingsPath(t, home))
	if err := RestoreCursorSettingsFromCrash(); err != nil {
		t.Fatalf("RestoreCursorSettingsFromCrash: %v", err)
	}
	after, _ := os.ReadFile(testCursorSettingsPath(t, home))
	if string(before) != string(after) {
		t.Errorf("no leftover injected keys should be no-op; file changed")
	}
}

// TestWriteCursorSettingsBackup_DoesNotOverwrite 已有备份时不覆盖（防崩溃重启
// 又备份当前注入值，污染真实原始值）。
func TestWriteCursorSettingsBackup_DoesNotOverwrite(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	// 预置一个"接管前原始"备份。
	original := cursorSettingsBackupRecord{
		Keys:   map[string]any{"http.proxy": "http://original:1"},
		Exists: map[string]bool{"http.proxy": true},
	}
	if err := os.MkdirAll(filepath.Dir(appdata.CursorSettingsBackupPath()), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	encoded, _ := json.MarshalIndent(original, "", "  ")
	if err := os.WriteFile(appdata.CursorSettingsBackupPath(), append(encoded, '\n'), 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	// 当前 settings 已是注入后的值——writeCursorSettingsBackup 不应覆盖备份。
	injected := map[string]any{"http.proxy": "http://127.0.0.1:8080"}
	if err := writeCursorSettingsBackup(injected); err != nil {
		t.Fatalf("writeCursorSettingsBackup: %v", err)
	}
	data, err := os.ReadFile(appdata.CursorSettingsBackupPath())
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	var got cursorSettingsBackupRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal backup: %v", err)
	}
	if v := got.Keys["http.proxy"]; v != "http://original:1" {
		t.Errorf("backup was overwritten with injected value: got %v, want original", v)
	}
}

// TestRestoreCursorSettingsKeys 逐键还原逻辑：存在键写回原值、不存在键 delete、
// 值相同无改动。
func TestRestoreCursorSettingsKeys(t *testing.T) {
	record := cursorSettingsBackupRecord{
		Keys: map[string]any{
			"http.proxy":              "http://user:1",
			"cursor.general.disableHttp2": nil,
		},
		Exists: map[string]bool{
			"http.proxy":              true,
			"cursor.general.disableHttp2": false,
		},
	}

	t.Run("restores existing and deletes absent", func(t *testing.T) {
		settings := map[string]any{
			"http.proxy":                 "http://127.0.0.1:8080", // 注入值
			"cursor.general.disableHttp2": true,                    // 注入值，原始不存在
		}
		changed := restoreCursorSettingsKeys(settings, record)
		if !changed {
			t.Errorf("expected changed=true")
		}
		if settings["http.proxy"] != "http://user:1" {
			t.Errorf("http.proxy not restored: %v", settings["http.proxy"])
		}
		if _, exists := settings["cursor.general.disableHttp2"]; exists {
			t.Errorf("disableHttp2 should be deleted (no original)")
		}
	})

	t.Run("no change when values match backup", func(t *testing.T) {
		settings := map[string]any{
			"http.proxy":              "http://user:1", // 与备份一致
			"editor.fontSize":         14,
		}
		// disableHttp2 接管前不存在，当前也不存在 → 该键无改动；
		// http.proxy 与备份一致 → 无改动。整体 changed=false。
		changed := restoreCursorSettingsKeys(settings, record)
		if changed {
			t.Errorf("expected no change when values match backup")
		}
	})
}

// TestClearUserProxySettings_NoBackupFallsBackToDelete 无备份时退回旧 delete 语义。
func TestClearUserProxySettings_NoBackupFallsBackToDelete(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	writeCursorSettingsFile(t, home, `{
  "http.proxy": "http://127.0.0.1:8080",
  "cursor.general.disableHttp2": true,
  "editor.fontSize": 14
}`)
	os.Remove(appdata.CursorSettingsBackupPath())

	if err := ClearUserProxySettings(); err != nil {
		t.Fatalf("ClearUserProxySettings: %v", err)
	}
	after := readCursorSettings(t, home)
	for _, key := range injectedCursorSettingsKeys {
		if _, exists := after[key]; exists {
			t.Errorf("injected key %q should be deleted (no backup, fallback)", key)
		}
	}
	if got := after["editor.fontSize"]; got != float64(14) {
		t.Errorf("unrelated key lost: %v", got)
	}
}
