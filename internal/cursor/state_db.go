package cursor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// InjectCursorUserInfo 已移除（P0-4/P3-1 死代码清理）。
// 旧版 StartProxy 每次无条件写入假 Ultra 身份会覆盖用户真实 state.vscdb；
// 新架构下真实 Cursor 账号是唯一权威，byok 不再写任何假身份。
// 历史残留由 RepairLegacyInjectedIdentity 安全清理（仅匹配旧假指纹才删）。

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

// CursorAccountStatus 是 Cursor 账号登录状态的只读探针结果。
type CursorAccountStatus struct {
	// AccountPresent 表示 state.vscdb 中存在 cursorAuth/accessToken 且非空。
	// false 意味着 Cursor 客户端未登录账号——Tab 补全 / Git 消息等依赖官方账号
	// 的能力将不可用，前端应明确告警而非静默失败（能力缺失标注）。
	AccountPresent bool `json:"accountPresent"`
	// Email 是缓存邮箱（脱敏：仅前缀，用于提示"已登录 xxx@..."）。无则空串。
	Email string `json:"email"`
	// DBExists 表示 state.vscdb 文件是否存在。false 通常意味着 Cursor 从未启动过。
	DBExists bool `json:"dbExists"`
	// ProbeError 探针自身出错时的只读错误信息（前端可降级展示）。
	ProbeError string `json:"probeError,omitempty"`
}

// ProbeCursorAccount 只读探测 Cursor 客户端是否登录了官方账号。
// 仅 SELECT、不开写事务、不修改任何数据——即便 Cursor 进程正在运行也安全（只读连接
// 不会与 Cursor 的写事务竞争损坏库）。用于前端"Tab 补全依赖官方账号"的缺失告警。
func ProbeCursorAccount() CursorAccountStatus {
	stateDBPath, err := resolveCursorStateDBPath()
	if err != nil {
		return CursorAccountStatus{ProbeError: err.Error()}
	}
	status := CursorAccountStatus{}
	if _, statErr := os.Stat(stateDBPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return status // DBExists=false, AccountPresent=false
		}
		status.ProbeError = statErr.Error()
		return status
	}
	status.DBExists = true

	db, err := sql.Open("sqlite", stateDBPath)
	if err != nil {
		status.ProbeError = err.Error()
		return status
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", cursorStateSQLiteBusyTimeoutMS)); err != nil {
		status.ProbeError = err.Error()
		return status
	}
	// 只读查询：accessToken 非空即视为已登录。不读 token 原文返回前端。
	access, err := readItemTableValueReadOnly(ctx, db, "cursorAuth/accessToken")
	if err != nil {
		status.ProbeError = err.Error()
		return status
	}
	if strings.TrimSpace(access) == "" {
		return status // AccountPresent=false
	}
	status.AccountPresent = true
	if email, err := readItemTableValueReadOnly(ctx, db, "cursorAuth/cachedEmail"); err == nil {
		status.Email = maskEmail(strings.TrimSpace(email))
	}
	return status
}

// readItemTableValueReadOnly 用只读连接查询单个 ItemTable 值；不存在返回空串。
// 与 readItemTableValue（事务内）区分，避免开写事务。
func readItemTableValueReadOnly(ctx context.Context, db *sql.DB, key string) (string, error) {
	var raw []byte
	err := db.QueryRowContext(ctx, "SELECT value FROM ItemTable WHERE key = ?", key).Scan(&raw)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return string(raw), nil
}

// maskEmail 脱敏邮箱：保留 @ 后域名与前 1 字符，避免把完整邮箱回传前端日志。
func maskEmail(email string) string {
	if email == "" {
		return ""
	}
	at := strings.IndexByte(email, '@')
	if at <= 0 || at >= len(email)-1 {
		// 无 @ 或格式异常：只露首字符。
		if len(email) <= 1 {
			return email
		}
		return string(email[0]) + "***"
	}
	return string(email[0]) + "***" + email[at:]
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
	// P0-4：若 Cursor 进程正在运行，它会持有 state.vscdb 的写锁或与之交错写。
	// 此时并发修复既可能被 SQLITE_BUSY 拒绝（假账号残留不被清理），更可能在
	// schema/BLOB 编码上与 Cursor 冲突导致用户状态库损坏。检测到运行中则跳过，
	// 待下次 Cursor 未运行时（正常 StartProxy 时机）再修。绝不冒险并发写。
	if isCursorProcessRunning() {
		logger.Infof("repairLegacyInjectedIdentity: Cursor 进程运行中，跳过 state.vscdb 修复以防并发写损坏（下次未运行时再修）")
		return nil
	}
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
