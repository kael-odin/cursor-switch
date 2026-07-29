package cursor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"cursor/internal/appdata"
	"cursor/internal/logger"
	"cursor/internal/securefile"
)

// injectedCursorSettingsKeys 表示当前模块中的 injectedCursorSettingsKeys 状态值。
var injectedCursorSettingsKeys = []string{
	"http.proxy",
	"http.proxyKerberosServicePrincipal",
	"http.proxySupport",
	"cursor.general.disableHttp2",
	"http.experimental.systemCertificatesV2",
}

// EnsureCACertFile 用于处理与 EnsureCACertFile 相关的逻辑。
func EnsureCACertFile(certPEM []byte, currentPath string) (string, error) {
	certPath := appdata.CACertFilePath()
	if samePath(strings.TrimSpace(currentPath), certPath) {
		if _, err := os.Stat(certPath); err == nil {
			logger.Infof("ensureCACertFile: reusing path=%s", certPath)
			return certPath, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return "", fmt.Errorf("创建证书配置目录失败: %w", err)
	}

	if existing, err := os.ReadFile(certPath); err == nil && bytes.Equal(existing, certPEM) {
		logger.Infof("ensureCACertFile: unchanged path=%s", certPath)
		return certPath, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("读取内置 CA 证书失败: %w", err)
	}

	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return "", fmt.Errorf("写入内置 CA 证书失败: %w", err)
	}
	sum := sha256.Sum256(certPEM)
	logger.Infof(
		"ensureCACertFile: wrote path=%s sha256=%s size=%d",
		certPath,
		strings.ToUpper(hex.EncodeToString(sum[:])),
		len(certPEM),
	)
	return certPath, nil
}

func samePath(left string, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

// SetSystemNodeExtraCACerts 用于处理与 SetSystemNodeExtraCACerts 相关的逻辑。
func SetSystemNodeExtraCACerts(caCertPath string) error {
	caCertPath = strings.TrimSpace(caCertPath)
	if caCertPath == "" {
		return errors.New("CA 证书路径为空")
	}
	if err := os.Setenv("NODE_EXTRA_CA_CERTS", caCertPath); err != nil {
		return fmt.Errorf("设置进程环境变量失败: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("launchctl", "setenv", "NODE_EXTRA_CA_CERTS", caCertPath).CombinedOutput()
		if err != nil {
			return fmt.Errorf("写入 macOS 用户环境变量失败: %v: %s", err, strings.TrimSpace(string(out)))
		}
	case "linux":
		// Linux 发行版环境变量持久化方式差异较大，这里先确保当前进程生效。
		logger.Infof("setSystemNodeExtraCACerts: linux detected, applied to current process only")
	default:
		return fmt.Errorf("不支持的系统: %s", runtime.GOOS)
	}

	logger.Infof("setSystemNodeExtraCACerts: NODE_EXTRA_CA_CERTS=%s", caCertPath)
	return nil
}

// ClearSystemNodeExtraCACerts 用于处理与 ClearSystemNodeExtraCACerts 相关的逻辑。
func ClearSystemNodeExtraCACerts() error {
	if err := os.Unsetenv("NODE_EXTRA_CA_CERTS"); err != nil {
		return fmt.Errorf("清理进程环境变量失败: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("launchctl", "unsetenv", "NODE_EXTRA_CA_CERTS").CombinedOutput()
		if err != nil {
			return fmt.Errorf("清理 macOS 用户环境变量失败: %v: %s", err, strings.TrimSpace(string(out)))
		}
	case "linux":
		logger.Infof("clearSystemNodeExtraCACerts: linux detected, cleared in current process only")
	default:
		return fmt.Errorf("不支持的系统: %s", runtime.GOOS)
	}

	logger.Infof("clearSystemNodeExtraCACerts: NODE_EXTRA_CA_CERTS cleared")
	return nil
}

// cursorSettingsBackupRecord 是接管前 settings.json 被 cursor-switch 覆盖的注入键原始值快照。
// A6：防止退出/崩溃后用户原始代理配置丢失。exists 标记某键接管前是否存在（false 表示
// 接管前该键不存在，还原时应 delete 而非写 null）。
type cursorSettingsBackupRecord struct {
	Keys   map[string]any `json:"keys"`
	Exists map[string]bool `json:"exists"`
}

// writeCursorSettingsBackup 把当前 settings 里被注入键的原始值快照写入备份文件。
// 仅当备份文件不存在时写——防止"接管→崩溃→重启→又备份当前(已注入)值"覆盖掉真实原始值。
// 调用方需保证传入的 settings 是注入前的原始内容。
func writeCursorSettingsBackup(settings map[string]any) error {
	backupPath := appdata.CursorSettingsBackupPath()
	if _, err := os.Stat(backupPath); err == nil {
		// 已有备份：保留原始快照，不覆盖。
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查 Cursor 配置备份状态失败: %w", err)
	}
	record := cursorSettingsBackupRecord{
		Keys:   make(map[string]any, len(injectedCursorSettingsKeys)),
		Exists: make(map[string]bool, len(injectedCursorSettingsKeys)),
	}
	for _, key := range injectedCursorSettingsKeys {
		value, exists := settings[key]
		record.Keys[key] = value
		record.Exists[key] = exists
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 Cursor 配置备份失败: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o700); err != nil {
		return fmt.Errorf("创建 Cursor 配置备份目录失败: %w", err)
	}
	if err := securefile.WriteFile(backupPath, encoded); err != nil {
		return fmt.Errorf("写入 Cursor 配置备份失败: %w", err)
	}
	logger.Infof("writeCursorSettingsBackup: backed up %d injected keys path=%s", len(injectedCursorSettingsKeys), backupPath)
	return nil
}

// readCursorSettingsBackup 读取并解析接管前的注入键原始值快照。无备份返回 (nil,false,nil)。
func readCursorSettingsBackup() (cursorSettingsBackupRecord, bool, error) {
	backupPath := appdata.CursorSettingsBackupPath()
	data, err := os.ReadFile(backupPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cursorSettingsBackupRecord{}, false, nil
		}
		return cursorSettingsBackupRecord{}, false, fmt.Errorf("读取 Cursor 配置备份失败: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return cursorSettingsBackupRecord{}, false, nil
	}
	var record cursorSettingsBackupRecord
	if err := json.Unmarshal(data, &record); err != nil {
		logger.Infof("readCursorSettingsBackup: backup corrupt, ignoring path=%s err=%v", backupPath, err)
		return cursorSettingsBackupRecord{}, false, nil
	}
	return record, true, nil
}

// removeCursorSettingsBackup 删除备份文件（成功还原后清理，避免堆积）。
func removeCursorSettingsBackup() {
	backupPath := appdata.CursorSettingsBackupPath()
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Infof("removeCursorSettingsBackup: failed path=%s err=%v", backupPath, err)
	}
}

// restoreCursorSettingsKeys 把 settings 里被注入键还原为备份记录的原始值。
// 接管前不存在的键 delete；存在的键写回原始值。返回是否有改动。
func restoreCursorSettingsKeys(settings map[string]any, record cursorSettingsBackupRecord) bool {
	changed := false
	for _, key := range injectedCursorSettingsKeys {
		originalValue := record.Keys[key]
		existed := record.Exists[key]
		currentValue, currentExists := settings[key]
		if existed {
			// 接管前该键存在，还原原始值。
			if !currentExists {
				settings[key] = originalValue
				changed = true
				continue
			}
			if !cursorSettingValueEqual(currentValue, originalValue) {
				settings[key] = originalValue
				changed = true
			}
		} else {
			// 接管前该键不存在，删掉注入值。
			if currentExists {
				delete(settings, key)
				changed = true
			}
		}
	}
	return changed
}

// cursorSettingValueEqual 比较两个 settings 值是否相等（json 反序列化后是
// map[string]any / []any / 基本类型，用 JSON 序列化比较规避类型差异）。
func cursorSettingValueEqual(a, b any) bool {
	aj, errA := json.Marshal(a)
	bj, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return bytes.Equal(aj, bj)
}

// settingsHasInjectedKeys 判断 settings 是否含任意注入键（用于崩溃恢复检测）。
func settingsHasInjectedKeys(settings map[string]any) bool {
	for _, key := range injectedCursorSettingsKeys {
		if _, exists := settings[key]; exists {
			return true
		}
	}
	return false
}

// RestoreCursorSettingsFromCrash 是 A6 崩溃恢复入口：检测 settings.json 残留注入键 +
// 存在接管前备份 → 上次非正常退出 → 据备份还原用户原始配置。无残留或无备份则 no-op。
// 供 ApplyCursorSettings 在注入前调用，避免在已污染的 settings 上再注入/覆盖原始值。
func RestoreCursorSettingsFromCrash() error {
	settingsPath, err := resolveCursorSettingsPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("读取 Cursor 配置失败: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	settings, err := decodeCursorSettingsJSONC(data)
	if err != nil {
		// 损坏的 settings 交给 WriteUserProxySettings 的既有 .corrupt.bak 路径处理，这里不干预。
		return nil
	}
	if !settingsHasInjectedKeys(settings) {
		return nil
	}
	record, hasBackup, err := readCursorSettingsBackup()
	if err != nil {
		return err
	}
	if !hasBackup {
		// 残留注入键但无备份：无法还原原始值，只能删注入键退回"无代理"状态（best-effort 自愈）。
		logger.Infof("restoreCursorSettingsFromCrash: leftover injected keys without backup, clearing path=%s", settingsPath)
		cleared := false
		for _, key := range injectedCursorSettingsKeys {
			if _, exists := settings[key]; exists {
				delete(settings, key)
				cleared = true
			}
		}
		if !cleared {
			return nil
		}
	} else {
		if !restoreCursorSettingsKeys(settings, record) {
			// 备份与当前值一致，无需改动；仍清理备份避免堆积。
			removeCursorSettingsBackup()
			return nil
		}
		logger.Infof("restoreCursorSettingsFromCrash: restored original settings from backup path=%s", settingsPath)
	}

	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 Cursor 配置失败: %w", err)
	}
	encoded = append(encoded, '\n')
	tempPath := settingsPath + ".tmp"
	if err := os.WriteFile(tempPath, encoded, 0o644); err != nil {
		return fmt.Errorf("写入 Cursor 配置临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, settingsPath); err != nil {
		return fmt.Errorf("保存 Cursor 配置失败: %w", err)
	}
	removeCursorSettingsBackup()
	return nil
}

// WriteUserProxySettings 用于处理与 WriteUserProxySettings 相关的逻辑。
//
// A6：注入前先把被覆盖键的原始值备份（仅首次，不覆盖已有备份），退出/崩溃时据备份还原，
// 防止用户原始代理配置丢失。注入前先调 RestoreCursorSettingsFromCrash 自愈上次崩溃残留。
func WriteUserProxySettings(proxyURL string) error {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return errors.New("代理地址为空")
	}

	settingsPath, err := resolveCursorSettingsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return fmt.Errorf("创建 Cursor 配置目录失败: %w", err)
	}

	settings := make(map[string]any)
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("读取 Cursor 配置失败: %w", err)
		}
	} else if len(bytes.TrimSpace(data)) > 0 {
		parsed, err := decodeCursorSettingsJSONC(data)
		if err != nil {
			backupPath := settingsPath + ".corrupt.bak"
			if renameErr := os.Rename(settingsPath, backupPath); renameErr != nil && !errors.Is(renameErr, os.ErrNotExist) {
				return fmt.Errorf("解析 Cursor 配置失败，且备份损坏配置失败: %w", renameErr)
			}
			logger.Infof("writeCursorUserProxySettings: backed up invalid settings path=%s backup=%s err=%v", settingsPath, backupPath, err)
			data = nil
		} else {
			settings = parsed
		}
	}

	// A6：注入前备份被覆盖键的原始值（仅当无已有备份时，防崩溃重启后覆盖真实原始值）。
	if err := writeCursorSettingsBackup(settings); err != nil {
		// 备份失败不阻断注入——注入是主流程，但记日志以便排查还原问题。
		logger.Infof("writeCursorUserProxySettings: backup original keys failed (non-fatal): %v", err)
	}

	settings["http.proxy"] = proxyURL
	settings["http.proxyKerberosServicePrincipal"] = proxyURL
	settings["http.proxySupport"] = "on"
	settings["cursor.general.disableHttp2"] = true
	settings["http.experimental.systemCertificatesV2"] = true

	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 Cursor 配置失败: %w", err)
	}
	encoded = append(encoded, '\n')

	if len(bytes.TrimSpace(data)) > 0 && bytes.Equal(data, encoded) {
		logger.Infof("writeCursorUserProxySettings: unchanged path=%s proxy=%s", settingsPath, proxyURL)
		return nil
	}

	tempPath := settingsPath + ".tmp"
	if err := os.WriteFile(tempPath, encoded, 0o644); err != nil {
		return fmt.Errorf("写入 Cursor 配置临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, settingsPath); err != nil {
		return fmt.Errorf("保存 Cursor 配置失败: %w", err)
	}

	logger.Infof("writeCursorUserProxySettings: path=%s proxy=%s", settingsPath, proxyURL)
	return nil
}

// ClearUserProxySettings 用于处理与 ClearUserProxySettings 相关的逻辑。
//
// A6：不再简单 delete 注入键，而是从接管前备份还原用户原始值——用户接管前的 http.proxy
// 等配置回来，而非被抹掉。无备份则退回旧语义（delete 注入键）。
func ClearUserProxySettings() error {
	settingsPath, err := resolveCursorSettingsPath()
	if err != nil {
		return err
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("读取 Cursor 配置失败: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}

	settings := make(map[string]any)
	parsed, err := decodeCursorSettingsJSONC(data)
	if err != nil {
		return fmt.Errorf("解析 Cursor 配置失败: %w", err)
	}
	settings = parsed

	record, hasBackup, err := readCursorSettingsBackup()
	if err != nil {
		return err
	}
	var changed bool
	if hasBackup {
		changed = restoreCursorSettingsKeys(settings, record)
		logger.Infof("clearCursorUserProxySettings: restored from backup path=%s changed=%t", settingsPath, changed)
	} else {
		// 无备份：旧语义，删注入键。
		for _, key := range injectedCursorSettingsKeys {
			if _, exists := settings[key]; exists {
				delete(settings, key)
				changed = true
			}
		}
		logger.Infof("clearCursorUserProxySettings: no backup, deleted injected keys path=%s changed=%t", settingsPath, changed)
	}
	if !changed {
		// 即便无改动，若有遗留备份也清理。
		removeCursorSettingsBackup()
		return nil
	}

	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 Cursor 配置失败: %w", err)
	}
	encoded = append(encoded, '\n')

	tempPath := settingsPath + ".tmp"
	if err := os.WriteFile(tempPath, encoded, 0o644); err != nil {
		return fmt.Errorf("写入 Cursor 配置临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, settingsPath); err != nil {
		return fmt.Errorf("保存 Cursor 配置失败: %w", err)
	}

	removeCursorSettingsBackup()
	logger.Infof("clearCursorUserProxySettings: restored path=%s", settingsPath)
	return nil
}

// resolveCursorSettingsPath 用于处理与 resolveCursorSettingsPath 相关的逻辑。
func resolveCursorSettingsPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户目录失败: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(homeDir, "Library", "Application Support", "Cursor", "User", "settings.json"), nil
	case "windows":
		appData := os.Getenv("APPDATA")
		if strings.TrimSpace(appData) == "" {
			appData = filepath.Join(homeDir, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Cursor", "User", "settings.json"), nil
	case "linux":
		configDir := os.Getenv("XDG_CONFIG_HOME")
		if strings.TrimSpace(configDir) == "" {
			configDir = filepath.Join(homeDir, ".config")
		}
		return filepath.Join(configDir, "Cursor", "User", "settings.json"), nil
	default:
		return "", fmt.Errorf("不支持的系统: %s", runtime.GOOS)
	}
}

// decodeCursorSettingsJSONC 用于处理与 decodeCursorSettingsJSONC 相关的逻辑。
func decodeCursorSettingsJSONC(data []byte) (map[string]any, error) {
	result := make(map[string]any)
	normalized, err := normalizeJSONC(data)
	if err != nil {
		return nil, err
	}
	normalized = bytes.TrimSpace(normalized)
	if len(normalized) == 0 {
		return result, nil
	}
	if err := json.Unmarshal(normalized, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// normalizeJSONC 用于处理与 normalizeJSONC 相关的逻辑。
func normalizeJSONC(data []byte) ([]byte, error) {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}
	withoutComments, err := stripJSONCComments(data)
	if err != nil {
		return nil, err
	}
	return stripJSONCTrailingCommas(withoutComments), nil
}

// stripJSONCComments 用于处理与 stripJSONCComments 相关的逻辑。
func stripJSONCComments(data []byte) ([]byte, error) {
	out := make([]byte, 0, len(data))
	inString := false
	inLineComment := false
	inBlockComment := false
	escaped := false

	for i := 0; i < len(data); i++ {
		ch := data[i]

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
				out = append(out, ch)
			}
			continue
		}
		if inBlockComment {
			if ch == '*' && i+1 < len(data) && data[i+1] == '/' {
				inBlockComment = false
				i++
				continue
			}
			if ch == '\n' {
				out = append(out, ch)
			}
			continue
		}
		if inString {
			out = append(out, ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}
		if ch == '/' && i+1 < len(data) {
			next := data[i+1]
			if next == '/' {
				inLineComment = true
				i++
				continue
			}
			if next == '*' {
				inBlockComment = true
				i++
				continue
			}
		}
		out = append(out, ch)
	}

	if inBlockComment {
		return nil, errors.New("JSONC 块注释未闭合")
	}
	return out, nil
}

// stripJSONCTrailingCommas 用于处理与 stripJSONCTrailingCommas 相关的逻辑。
func stripJSONCTrailingCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false

	for i := 0; i < len(data); i++ {
		ch := data[i]
		if inString {
			out = append(out, ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}

		if ch == ',' {
			j := i + 1
			for j < len(data) && isJSONWhitespace(data[j]) {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
		}

		out = append(out, ch)
	}

	return out
}

// isJSONWhitespace 用于处理与 isJSONWhitespace 相关的逻辑。
func isJSONWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n'
}

// ProxyURLFromListenAddr 用于处理与 ProxyURLFromListenAddr 相关的逻辑。
func ProxyURLFromListenAddr(listenAddr string) string {
	addr := strings.TrimSpace(listenAddr)
	if addr == "" {
		return "http://127.0.0.1:8080"
	}

	// :8189 -> 127.0.0.1:8189
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr
	}

	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
			host = "127.0.0.1"
		}
		return "http://" + net.JoinHostPort(host, port)
	}

	return "http://" + addr
}
