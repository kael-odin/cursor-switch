package appdata

import (
	"fmt"
	"log"
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

// migrationMarkerPath 返回迁移完成标记文件路径（F-29）。
// 标记存在则跳过整次 legacy 迁移，避免重复复制/重复备份。
func migrationMarkerPath() string {
	return filepath.Join(RootDir(), ".legacy-migrated")
}

// legacyBackupDir 返回旧目录迁移成功后的备份路径（F-29）。
// 迁移成功后把旧目录 rename 到此路径保留可恢复副本，而非 RemoveAll。
func legacyBackupDir() string {
	return legacyRootDir() + ".migrated-bak"
}

// migrateLegacyAssistantHome 把旧版 ~/.cursor-local-assistant 目录的配置/规则
// 迁移到新目录 ~/.cursor-local-assistant-v2（F-29 重写）。
//
// 此前实现复制后无条件 os.RemoveAll 旧目录、且复制错误全被忽略——磁盘满/权限失败/
// 进程崩溃时可同时覆盖新目录内容并删除旧目录唯一副本，丢失 config/rules。
//
// 新策略（两阶段 + 完成标记 + 备份保留）：
//  1. 若新目录已存在 .legacy-migrated 标记 → 整次跳过（幂等）。
//  2. 阶段一：复制 config.yaml + rules/* 到新目录。不覆盖已存在目标（用户在用新目录），
//     每文件临时文件 + fsync + 大小校验 + 原子 rename，错误透传不静默。
//  3. 阶段二：全部成功后写 .legacy-migrated 标记，再把旧目录 rename 成 .migrated-bak
//     保留可恢复备份；rename 失败则保留旧目录原样（不删）。
//  4. 任一步失败：不写标记、不删/不改名旧目录，记日志，启动继续。
//
// 此函数不返回 error：迁移失败不阻断启动（用户仍能用新目录默认配置启动），
// 旧目录保留供手动排查/重试。
func migrateLegacyAssistantHome() {
	legacyRoot := legacyRootDir()

	// 标记存在 → 已迁移过，跳过。
	if markerExists, _ := fileExists(migrationMarkerPath()); markerExists {
		return
	}
	// 旧目录不存在 → 无可迁移，no-op。
	legacyExists, _ := fileExists(legacyRoot)
	if !legacyExists {
		return
	}

	// 阶段一：复制 config.yaml + rules。
	if err := copyLegacyFile(filepath.Join(legacyRoot, "config.yaml"), filepath.Join(RootDir(), "config.yaml")); err != nil {
		log.Printf("legacy migrate: copy config.yaml failed (keep old dir): %v", err)
		return
	}
	if err := copyLegacyRules(filepath.Join(legacyRoot, "rules"), RulesRootPath()); err != nil {
		log.Printf("legacy migrate: copy rules failed (keep old dir): %v", err)
		return
	}

	// 阶段二：全部成功 → 写标记 + 备份旧目录。
	if err := securefile.WriteFile(migrationMarkerPath(), []byte(legacyRoot+"\n")); err != nil {
		log.Printf("legacy migrate: write marker failed (keep old dir): %v", err)
		return
	}
	// 备份旧目录：rename 原子且廉价（同分区）。失败则保留旧目录原样，绝不 RemoveAll。
	if err := os.Rename(legacyRoot, legacyBackupDir()); err != nil {
		log.Printf("legacy migrate: backup rename failed (old dir left in place): %v", err)
		return
	}
	log.Printf("legacy migrate: success, old dir backed up to %s", legacyBackupDir())
}

// fileExists 报告路径是否存在（文件或目录）。
func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func copyLegacyRules(sourceRoot string, targetRoot string) error {
	return filepath.Walk(sourceRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// F-29：Walk 错误透传，不再 return nil 吞掉。
			return fmt.Errorf("walk %s: %w", path, err)
		}
		if info == nil {
			return nil
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return fmt.Errorf("rel %s: %w", path, err)
		}
		targetPath := filepath.Join(targetRoot, rel)
		if info.IsDir() {
			if err := securefile.MkdirAll(targetPath); err != nil {
				return fmt.Errorf("mkdir %s: %w", targetPath, err)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			// 跳过非常规文件（symlink/socket 等），不复制也不报错。
			return nil
		}
		return copyLegacyFile(path, targetPath)
	})
}

// copyLegacyFile 把单个旧文件复制到新路径（F-29）。
// 目标已存在则跳过（不覆盖用户在用的新目录文件）。源不存在/非常规则跳过。
func copyLegacyFile(sourcePath string, targetPath string) error {
	sourceExists, err := fileExists(sourcePath)
	if err != nil {
		return fmt.Errorf("stat source %s: %w", sourcePath, err)
	}
	if !sourceExists {
		return nil
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("stat source %s: %w", sourcePath, err)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	// 不覆盖已存在的目标：用户可能已在用新目录的 config.yaml。
	if targetExists, _ := fileExists(targetPath); targetExists {
		return nil
	}
	return copyFileAtomic(sourcePath, targetPath, info.Size())
}

// copyFileAtomic 原子复制：读全量 → 临时文件 → fsync → 大小校验 → rename → EnsureMode（F-29）。
// 迁移文件是小配置/规则，全量读到内存安全。size 用于写后校验防截断。
func copyFileAtomic(sourcePath string, targetPath string, sourceSize int64) error {
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read source %s: %w", sourcePath, err)
	}
	// 写后大小校验：读到的字节数应与 stat 一致（防读途中源被改/截断）。
	if int64(len(data)) != sourceSize {
		return fmt.Errorf("source %s size mismatch: stat=%d read=%d", sourcePath, sourceSize, len(data))
	}

	if err := securefile.MkdirAll(filepath.Dir(targetPath)); err != nil {
		return fmt.Errorf("mkdir target dir: %w", err)
	}
	tempPath := targetPath + ".migrate.tmp"
	tmp, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, securefile.FileMode)
	if err != nil {
		return fmt.Errorf("open temp %s: %w", tempPath, err)
	}
	written, err := tmp.Write(data)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("write temp %s: %w", tempPath, err)
	}
	// fsync 落盘，防崩溃留下半截文件。
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("sync temp %s: %w", tempPath, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close temp %s: %w", tempPath, err)
	}
	// 写后大小校验：防磁盘满/截断。
	if written != len(data) {
		_ = os.Remove(tempPath)
		return fmt.Errorf("short write %s: written=%d want=%d", tempPath, written, len(data))
	}
	// 原子替换。调用方已确保目标不存在（copyLegacyFile 前置跳过已存在目标）；
	// 即便竞态下目标已出现，rename 在目标不存在时是纯 rename、存在时是替换语义，
	// 但我们依赖前置跳过保证不覆盖。
	if err := os.Rename(tempPath, targetPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("rename temp→target: %w", err)
	}
	return securefile.EnsureMode(targetPath, securefile.FileMode)
}
