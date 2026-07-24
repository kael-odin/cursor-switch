package cursor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"cursor/internal/logger"

	_ "modernc.org/sqlite"
)

const (
	cursorStateMembershipType      = "ultra"
	cursorStateSubscriptionStatus  = "active"
	cursorStateDefaultSignUpType   = "Google"
	cursorStateSQLiteBusyTimeoutMS = 2000
	cursorStateDBRelativePath      = "Cursor/User/globalStorage/state.vscdb"
	cursorStateDarwinRelativePath  = "Library/Application Support/Cursor/User/globalStorage/state.vscdb"
	cursorStateLinuxRelativePath   = ".config/Cursor/User/globalStorage/state.vscdb"
	cursorStateStatsigBootstrapKey = "workbench.experiments.statsigBootstrap"
)

var cursorStateDisabledStatsigGates = []string{
	"decompose_always_local_ext_host",
	"cursor_extensions_isolation_v2",
}

// InjectCursorUserInfo synchronizes the Cursor user-level auth cache used by the
// Settings page. It does not modify the installed Cursor app bundle.
func InjectCursorUserInfo(email, token string) error {
	stateDBPath, err := resolveCursorStateDBPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(stateDBPath), 0o755); err != nil {
		return fmt.Errorf("创建 Cursor 状态目录失败: %w", err)
	}

	values := buildCursorAuthStateValues(email, token)
	if err := syncCursorAuthStateDB(stateDBPath, values); err != nil {
		return fmt.Errorf("同步 Cursor 状态库失败 path=%s: %w", stateDBPath, err)
	}

	logger.Infof(
		"injectCursorUserInfo synced path=%s email=%s membership=%s subscription=%s disabled_statsig_gates=%s",
		stateDBPath,
		values["cursorAuth/cachedEmail"],
		values["cursorAuth/stripeMembershipType"],
		values["cursorAuth/stripeSubscriptionStatus"],
		strings.Join(cursorStateDisabledStatsigGates, ","),
	)
	return nil
}

func buildCursorAuthStateValues(email, token string) map[string]string {
	email = strings.TrimSpace(email)
	token = strings.TrimSpace(token)

	return map[string]string{
		"cursorAuth/accessToken":              token,
		"cursorAuth/cachedEmail":              email,
		"cursorAuth/cachedSignUpType":         cursorStateDefaultSignUpType,
		"cursorAuth/refreshToken":             token,
		"cursorAuth/stripeMembershipType":     cursorStateMembershipType,
		"cursorAuth/stripeSubscriptionStatus": cursorStateSubscriptionStatus,
	}
}

func syncCursorAuthStateDB(path string, values map[string]string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", cursorStateSQLiteBusyTimeoutMS)); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)"); err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	stmt, err := tx.PrepareContext(ctx, "INSERT OR REPLACE INTO ItemTable(key, value) VALUES(?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, key := range keys {
		if _, err := stmt.ExecContext(ctx, key, values[key]); err != nil {
			return err
		}
	}

	if err := disableCursorStatsigGates(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func disableCursorStatsigGates(ctx context.Context, tx *sql.Tx) error {
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT value FROM ItemTable WHERE key = ?", cursorStateStatsigBootstrapKey).Scan(&raw)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("解析 Cursor Statsig bootstrap 失败: %w", err)
	}

	featureGates, _ := payload["feature_gates"].(map[string]any)
	if featureGates == nil {
		featureGates = map[string]any{}
		payload["feature_gates"] = featureGates
	}

	hashUsed, _ := payload["hash_used"].(string)
	for _, gate := range cursorStateDisabledStatsigGates {
		disableCursorStatsigGate(featureGates, gate)
		if strings.EqualFold(hashUsed, "djb2") {
			disableCursorStatsigGate(featureGates, cursorStateDJB2Hash(gate))
		}
	}

	updated, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("编码 Cursor Statsig bootstrap 失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE ItemTable SET value = ? WHERE key = ?", updated, cursorStateStatsigBootstrapKey); err != nil {
		return err
	}
	return nil
}

func disableCursorStatsigGate(featureGates map[string]any, key string) {
	gate, _ := featureGates[key].(map[string]any)
	if gate == nil {
		gate = map[string]any{
			"name":       key,
			"rule_id":    "local_disabled",
			"ruleID":     "local_disabled",
			"group_name": "local_disabled",
			"groupName":  "local_disabled",
			"id_type":    "userID",
			"idType":     "userID",
		}
		featureGates[key] = gate
	}
	gate["value"] = false
}

func cursorStateDJB2Hash(value string) string {
	var hash uint32
	for _, b := range []byte(value) {
		hash = hash*31 + uint32(b)
	}
	return fmt.Sprintf("%d", hash)
}

func resolveCursorStateDBPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户目录失败: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(homeDir, filepath.FromSlash(cursorStateDarwinRelativePath)), nil
	case "windows":
		appData := strings.TrimSpace(os.Getenv("APPDATA"))
		if appData == "" {
			appData = filepath.Join(homeDir, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Cursor", "User", "globalStorage", "state.vscdb"), nil
	case "linux":
		configDir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
		if configDir == "" {
			return filepath.Join(homeDir, filepath.FromSlash(cursorStateLinuxRelativePath)), nil
		}
		return filepath.Join(configDir, filepath.FromSlash(cursorStateDBRelativePath)), nil
	default:
		return "", fmt.Errorf("不支持的系统: %s", runtime.GOOS)
	}
}

// legacyFakeEmail / legacyFakeToken 是历史版本注入 Cursor state.vscdb 的假账号指纹。
// 这两个值必须与旧版 runtime.InjectAccountEmail / InjectAuthToken 完全一致，
// 用于识别「确由旧版 byok 写入」的假身份，避免误删用户真实账号。
const (
	legacyFakeEmail = "cursor@ai.com"
	legacyFakeToken = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJmYWtlLWN1cnNvci1sb2NhbC11c2VyIiwiZW1haWwiOiJjdXJzb3JAYWkuY29tIiwidHlwZSI6InNlc3Npb24iLCJpc3MiOiJjdXJzb3ItY2xpZW50Iiwic2NvcGUiOiJvcGVuaWQgcHJvZmlsZSBlbWFpbCIsImV4cCI6NDA3MDkwODgwMH0.fake-local-state-token"
)

// legacyInjectedKeys 是旧版一次性写入的六个 cursorAuth 字段，修复时逐个按值条件删除。
var legacyInjectedKeys = []string{
	"cursorAuth/accessToken",
	"cursorAuth/refreshToken",
	"cursorAuth/cachedEmail",
	"cursorAuth/cachedSignUpType",
	"cursorAuth/stripeMembershipType",
	"cursorAuth/stripeSubscriptionStatus",
}

// RepairLegacyInjectedIdentity 修复历史遗留的假账号注入。
//
// 严格 fail-safe：仅当 state.vscdb 已存在，且 accessToken/refreshToken/cachedEmail 三者
// 同时精确等于旧版假指纹时，才认定为「旧版注入的假账号」，随后事务性删除仍等于旧假值的
// 六个 cursorAuth 字段并清理由旧版写入的 Statsig gate 覆盖。任何字段一旦变成真实值都不删。
// 绝不创建库/表；库不存在或未匹配指纹时零写入直接返回。
func RepairLegacyInjectedIdentity() error {
	stateDBPath, err := resolveCursorStateDBPath()
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(stateDBPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil // 库不存在：无需修复
		}
		return fmt.Errorf("读取 Cursor 状态库失败 path=%s: %w", stateDBPath, statErr)
	}

	db, err := sql.Open("sqlite", stateDBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", cursorStateSQLiteBusyTimeoutMS)); err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	matched, err := legacyFakeIdentityFingerprintMatches(ctx, tx)
	if err != nil {
		return err
	}
	if !matched {
		return nil // 非旧版假指纹（含真实账号）：零写入
	}

	// 逐字段按「键 + 值仍等于旧假值」条件删除，防止删掉登录后写入的真实值。
	deletePairs := map[string]string{
		"cursorAuth/accessToken":              legacyFakeToken,
		"cursorAuth/refreshToken":             legacyFakeToken,
		"cursorAuth/cachedEmail":              legacyFakeEmail,
		"cursorAuth/cachedSignUpType":         cursorStateDefaultSignUpType,
		"cursorAuth/stripeMembershipType":     cursorStateMembershipType,
		"cursorAuth/stripeSubscriptionStatus": cursorStateSubscriptionStatus,
	}
	for _, key := range legacyInjectedKeys {
		wantValue := deletePairs[key]
		if _, err := tx.ExecContext(ctx, "DELETE FROM ItemTable WHERE key = ? AND value = ?", key, wantValue); err != nil {
			return err
		}
	}

	// 清理旧版写入的 Statsig gate 覆盖：仅当 bootstrap 中相应 gate 带 local_disabled 标记时移除，
	// 使 Cursor 重新从官方拉取真实 gate。
	if err := clearLegacyStatsigGateOverrides(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	logger.Infof("repairLegacyInjectedIdentity: 已清理历史假账号注入 path=%s，请重新登录 Cursor 账号", stateDBPath)
	return nil
}

// legacyFakeIdentityFingerprintMatches 判断三个核心字段是否同时等于旧版假指纹。
func legacyFakeIdentityFingerprintMatches(ctx context.Context, tx *sql.Tx) (bool, error) {
	access, err := readItemTableValue(ctx, tx, "cursorAuth/accessToken")
	if err != nil {
		return false, err
	}
	refresh, err := readItemTableValue(ctx, tx, "cursorAuth/refreshToken")
	if err != nil {
		return false, err
	}
	email, err := readItemTableValue(ctx, tx, "cursorAuth/cachedEmail")
	if err != nil {
		return false, err
	}
	return access == legacyFakeToken && refresh == legacyFakeToken && email == legacyFakeEmail, nil
}

// readItemTableValue 读取单个 ItemTable 值；不存在返回空串。
func readItemTableValue(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT value FROM ItemTable WHERE key = ?", key).Scan(&raw)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return string(raw), nil
}

// clearLegacyStatsigGateOverrides 移除旧版写入、带 local_disabled 标记的 gate 覆盖。
func clearLegacyStatsigGateOverrides(ctx context.Context, tx *sql.Tx) error {
	raw, err := readItemTableValue(ctx, tx, cursorStateStatsigBootstrapKey)
	if err != nil {
		return err
	}
	if raw == "" {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil // bootstrap 无法解析：不冒险改写
	}
	featureGates, _ := payload["feature_gates"].(map[string]any)
	if featureGates == nil {
		return nil
	}
	changed := false
	for key, value := range featureGates {
		gate, ok := value.(map[string]any)
		if !ok {
			continue
		}
		// 仅移除 byok 旧版打的 local_disabled 标记 gate。
		if ruleID, _ := gate["rule_id"].(string); ruleID == "local_disabled" {
			delete(featureGates, key)
			changed = true
			continue
		}
		if ruleID, _ := gate["ruleID"].(string); ruleID == "local_disabled" {
			delete(featureGates, key)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	updated, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE ItemTable SET value = ? WHERE key = ?", updated, cursorStateStatsigBootstrapKey); err != nil {
		return err
	}
	return nil
}
