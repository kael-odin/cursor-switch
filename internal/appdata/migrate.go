package appdata

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"cursor/internal/securefile"
)

func ensureAssistantHome() error {
	migrateLegacyAssistantHome()
	// F-18：敏感数据目录 0700。此前 0755 允许 macOS/Linux 多用户环境其他本地用户列出/进入。
	if err := os.MkdirAll(RootDir(), securefile.DirMode); err != nil {
		return fmt.Errorf("create assistant home: %w", err)
	}
	if err := os.MkdirAll(DataRootPath(), securefile.DirMode); err != nil {
		return fmt.Errorf("create data root: %w", err)
	}
	if err := os.MkdirAll(HistoryRootPath(), securefile.DirMode); err != nil {
		return fmt.Errorf("create history root: %w", err)
	}
	if err := os.MkdirAll(RulesRootPath(), securefile.DirMode); err != nil {
		return fmt.Errorf("create rules root: %w", err)
	}
	if err := os.MkdirAll(LogsRootPath(), securefile.DirMode); err != nil {
		return fmt.Errorf("create logs root: %w", err)
	}
	// F-18 启动迁移：一次性收紧既有宽松权限文件（旧版以 0644/0755 写入的 config、
	// messages、app.log、usage 等）。root 不存在视为 no-op。错误不阻断启动。
	_ = securefile.EnsureTree(RootDir())
	return nil
}

func EnsureAssistantHome() error {
	return ensureAssistantHome()
}

func migrateLegacyAssistantHome() {
	legacyRoot := legacyRootDir()
	copyLegacyFile(filepath.Join(legacyRoot, "config.yaml"), filepath.Join(RootDir(), "config.yaml"))
	copyLegacyRules(filepath.Join(legacyRoot, "rules"), RulesRootPath())
	_ = os.RemoveAll(legacyRoot)
}

func copyLegacyRules(sourceRoot string, targetRoot string) {
	_ = filepath.Walk(sourceRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return nil
		}
		targetPath := filepath.Join(targetRoot, rel)
		if info.IsDir() {
			_ = os.MkdirAll(targetPath, securefile.DirMode)
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		copyLegacyFile(path, targetPath)
		return nil
	})
}

func copyLegacyFile(sourcePath string, targetPath string) {
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return
	}
	defer sourceFile.Close()

	info, err := sourceFile.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	// F-18：迁移目标用 0600（目录 0700），避免把旧版 0644 的 config/messages 权限原样带过来。
	if err := securefile.MkdirAll(filepath.Dir(targetPath)); err != nil {
		return
	}
	targetFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, securefile.FileMode)
	if err != nil {
		return
	}
	defer targetFile.Close()
	_, _ = io.Copy(targetFile, sourceFile)
	_ = securefile.EnsureMode(targetPath, securefile.FileMode)
}
