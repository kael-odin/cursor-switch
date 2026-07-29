package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cursor/internal/appdata"
	"cursor/internal/logger"
)

// setTestHome 把 HOME/USERPROFILE 重定向到临时目录，使 resolveCursorSettingsPath 与
// appdata.RootDir 都落在测试隔离目录。Windows 上 os.UserHomeDir 读 USERPROFILE，
// unix 读 HOME——两者都设。
//
// Linux 关键坑：resolveCursorSettingsPath 在 linux 优先读 XDG_CONFIG_HOME，仅当其
// 为空才回退 $HOME/.config。CI ubuntu runner 部分镜像 export 了 XDG_CONFIG_HOME
// 指向 /home/runner/.config，若不 unset，测试设的 HOME 会被 XDG 优先级绕过——
// 生产代码写到 /home/runner/.config/Cursor/... 而非 t.TempDir，测试全红且污染 runner
// 家目录。故 linux 下显式 unset XDG_CONFIG_HOME，强制回退到 HOME/.config（与
// testCursorSettingsPath 的 linux 分支一致）。
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
	} else {
		// linux: 强制 XDG_CONFIG_HOME 指向测试隔离目录，覆盖 runner 可能预设的值，
		// 使 resolveCursorSettingsPath 与 testCursorSettingsPath 同源。
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
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
// N-57：用户只自设 http.proxy（不含 cursor-switch 专有键 disableHttp2 /
// systemCertificatesV2）时，settingsHasInjectedKeys 应返回 false → no-op，
// 不误清用户自设代理。此前用规避方式绕过（改纯用户键），N-57 修复判定逻辑后
// 用真实场景正面验证。
func TestRestoreCursorSettingsFromCrash_NoLeftoverNoop(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	writeCursorSettingsFile(t, home, `{"editor.fontSize": 14, "http.proxy": "http://user-own:9000"}`)

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

// TestSettingsHasInjectedKeys_OnlyUserProxyNotMisjudged 覆盖 N-57：
// 用户只自设 http.proxy（无 cursor-switch 专有键）→ settingsHasInjectedKeys 返回 false，
// 不被误判为注入残留。只有 disableHttp2 / systemCertificatesV2 这类专有键存在才返回 true。
func TestSettingsHasInjectedKeys_OnlyUserProxyNotMisjudged(t *testing.T) {
	// 用户自设代理，无任何 cursor-switch 专有键 → false。
	userOnly := map[string]any{
		"http.proxy":      "http://user-own:9000",
		"http.proxySupport": "on",
		"editor.fontSize": 14,
	}
	if settingsHasInjectedKeys(userOnly) {
		t.Errorf("user-only http.proxy should not be misjudged as injected leftover")
	}

	// 含 cursor-switch 专有键 → true（确为注入残留）。
	withExclusive := map[string]any{
		"http.proxy":                    "http://127.0.0.1:8080",
		"cursor.general.disableHttp2":   true,
	}
	if !settingsHasInjectedKeys(withExclusive) {
		t.Errorf("settings with disableHttp2 should be detected as injected leftover")
	}

	// 另一个专有键 systemCertificatesV2 → true。
	withCertV2 := map[string]any{
		"http.experimental.systemCertificatesV2": true,
	}
	if !settingsHasInjectedKeys(withCertV2) {
		t.Errorf("settings with systemCertificatesV2 should be detected as injected leftover")
	}
}

// TestWriteUserProxySettings_CorruptSettingsFailsNotOverwrites 覆盖 N-25：
// settings.json 损坏时——(1) 不继续在空 settings 上注入（返回 error）；
// (2) 损坏现场备份到带时间戳的 .corrupt.YYYYMMDD-HHMMSS.bak 而非固定 .corrupt.bak。
func TestWriteUserProxySettings_CorruptSettingsFailsNotOverwrites(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	settingsPath := testCursorSettingsPath(t, home)
	writeCursorSettingsFile(t, home, `{not valid json`)

	err := WriteUserProxySettings("http://127.0.0.1:8080")
	if err == nil {
		t.Fatalf("expected error on corrupt settings.json, got nil")
	}

	// 损坏现场应被 rename 到带时间戳的备份（而非固定 .corrupt.bak，避免覆盖既有备份）。
	entries, err := os.ReadDir(filepath.Dir(settingsPath))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	foundCorruptBackup := false
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "settings.json.corrupt.") && strings.HasSuffix(name, ".bak") {
			foundCorruptBackup = true
			// 固定名 .corrupt.bak 不应再出现。
			if name == "settings.json.corrupt.bak" {
				t.Errorf("corrupt backup should use timestamped name, got fixed .corrupt.bak")
			}
			break
		}
	}
	if !foundCorruptBackup {
		t.Errorf("corrupt settings.json should be backed up to timestamped .corrupt.*.bak, dir=%v", entries)
	}

	// 原 settings.json 不应被注入（注入会清空用户全部配置只留注入键）。
	// 损坏现场被 rename 走，原路径要么不存在、要么不应含注入键。
	if _, statErr := os.Stat(settingsPath); statErr == nil {
		data, readErr := os.ReadFile(settingsPath)
		if readErr == nil {
			parsed, parseErr := decodeCursorSettingsJSONC(data)
			if parseErr == nil {
				if _, exists := parsed["cursor.general.disableHttp2"]; exists {
					t.Errorf("corrupt settings should not be overwritten with injected keys")
				}
			}
		}
	}
}

// TestWriteCursorSettingsBackup_ExclusiveCreateNoOverwrite 覆盖 N-11：
// 备份用 O_EXCL 独占创建——已有备份时不覆盖（TOCTOU 收敛为单次原子创建）。
// 并发两次 writeCursorSettingsBackup（不同 settings）应只有第一次写入生效。
func TestWriteCursorSettingsBackup_ExclusiveCreateNoOverwrite(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	// 第一次：备份原始值。
	original := map[string]any{"http.proxy": "http://original:1"}
	if err := writeCursorSettingsBackup(original); err != nil {
		t.Fatalf("first writeCursorSettingsBackup: %v", err)
	}

	// 第二次：当前已是注入值，不应覆盖。
	injected := map[string]any{"http.proxy": "http://127.0.0.1:8080"}
	if err := writeCursorSettingsBackup(injected); err != nil {
		t.Fatalf("second writeCursorSettingsBackup: %v", err)
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
		t.Errorf("exclusive create should prevent overwrite: got %v, want original", v)
	}
}
