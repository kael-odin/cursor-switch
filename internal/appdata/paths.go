package appdata

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	appDirName       = ".cursor-local-assistant-v2"
	legacyAppDirName = ".cursor-local-assistant"
)

// RootDir 返回应用配置根目录。
func RootDir() string {
	return appRootDir(appDirName)
}

func legacyRootDir() string {
	return appRootDir(legacyAppDirName)
}

func appRootDir(dirName string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return dirName
	}
	return filepath.Join(homeDir, dirName)
}

// ConfigFilePath 返回统一用户配置文件路径。
func ConfigFilePath() string {
	return filepath.Join(RootDir(), "config.yaml")
}

func DataRootPath() string {
	return filepath.Join(RootDir(), "data")
}

func HistoryRootPath() string {
	return filepath.Join(RootDir(), "history")
}

func UsageFilePath() string {
	return filepath.Join(HistoryRootPath(), "usage.json")
}

func AdsRootPath() string {
	return filepath.Join(DataRootPath(), "ads")
}

func CodebaseIndexRootPath() string {
	return filepath.Join(DataRootPath(), "codebase-index")
}

func DocsIndexRootPath() string {
	return filepath.Join(DataRootPath(), "docs-index")
}

func RulesRootPath() string {
	return filepath.Join(RootDir(), "rules")
}

// LogsRootPath 返回统一日志根目录路径。
func LogsRootPath() string {
	return filepath.Join(RootDir(), "logs")
}

// CACertFilePath 返回注入给宿主的 CA 文件路径。
func CACertFilePath() string {
	return filepath.Join(DataRootPath(), "ca.crt")
}

// CAKeyFilePath 返回本机 CA 私钥路径。该文件由本应用首次启动时生成，
// 永不进 git，权限 0600。
func CAKeyFilePath() string {
	return filepath.Join(DataRootPath(), "ca.key")
}

// CursorSettingsBackupPath 返回宿主 Cursor settings.json 注入键原始值的备份路径。
// cursor-switch 接管时会改写用户 Cursor 的 settings.json（注入 http.proxy 等键）；
// 为防止退出/崩溃后用户原始代理配置丢失，接管前把被覆盖键的原始值备份到此文件，
// 退出或崩溃恢复时据此还原。文件位于权限 0700 的 DataRoot 下，不进 git。
func CursorSettingsBackupPath() string {
	return filepath.Join(DataRootPath(), "cursor-settings-backup.json")
}
