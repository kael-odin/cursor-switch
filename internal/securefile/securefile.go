// Package securefile 提供敏感数据的受限权限文件/目录写入工具（F-18）。
//
// cursor-switch 的用户数据目录（~/.cursor-local-assistant-v2）下含 API key、
// 用户消息、上下文、工具结果、请求体等敏感内容。macOS/Linux 多用户环境下，
// 0644 文件 / 0755 目录允许其他本地用户读取这些数据。
//
// 统一约束：敏感文件 0600、敏感目录 0700。Windows 上 perm 位仅控制只读属性，
// 收紧到 0600/0700 在 Windows 上等价于去掉"只读"以外的额外限制——对单用户机器无副作用，
// 在 Unix 上则真正阻止其他用户读取。
package securefile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// FileMode 是敏感数据文件的权限（仅属主可读写）。
const FileMode os.FileMode = 0o600

// DirMode 是敏感数据目录的权限（仅属主可读写执行）。
const DirMode os.FileMode = 0o700

// MkdirAll 创建目录（含父目录），权限 0700。等价 os.MkdirAll(path, 0o700)，
// 但集中常量避免散落魔法数字。已有目录不受影响（os.MkdirAll 不改既有权限，见 EnsureTree）。
func MkdirAll(path string) error {
	if err := os.MkdirAll(path, DirMode); err != nil {
		return err
	}
	return EnsureMode(path, DirMode)
}

// WriteFile 写文件，权限 0600。若文件已存在且权限更宽，写后收紧到 0600。
// 注意：os.WriteFile 的 perm 仅在创建时生效，不收紧既有文件权限，故写后显式 chmod。
func WriteFile(path string, data []byte) error {
	if err := MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, FileMode); err != nil {
		return err
	}
	return EnsureMode(path, FileMode)
}

// WriteViaTemp 用「唯一临时文件 + 原子 rename」写文件，权限 0600，目录 0700。
// 避免写一半崩溃留下半截配置；写后对目标路径收紧权限（rename 保留临时文件的 0600）。
// tempSuffix 应含通配符或随机后缀以保证并发/重复调用唯一性，如 ".tmp"。
func WriteViaTemp(path, tempSuffix string, data []byte) error {
	if err := MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	tempPath := path + tempSuffix
	if err := os.WriteFile(tempPath, data, FileMode); err != nil {
		return err
	}
	if err := EnsureMode(tempPath, FileMode); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	// rename 后目标继承临时文件的 0600；既有目标若是 0644，rename 会替换为新节点，
	// 但保险起见再 EnsureMode 一次（同名 rename 在 Windows 上是替换语义）。
	return EnsureMode(path, FileMode)
}

// EnsureMode 确保 path 的权限为 mode（不放宽、不放宽到更宽）。
// 文件→FileMode、目录→DirMode 由调用方决定 mode。已等于或更严时 no-op。
// Windows 上 os.Chmod 到 0600/0700 实际只去掉只读位，对单用户无副作用。
func EnsureMode(path string, mode os.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() == mode.Perm() {
		return nil
	}
	return os.Chmod(path, mode)
}

// EnsureTree 递归收紧 root 下所有目录到 0700、文件到 0600（F-18 启动迁移）。
// 跳过符号链接（不改其目标）、跳过非常规文件（socket/device）。错误不中断，收集后返回首个。
// 用于启动时一次性修复既有宽松权限文件，无需改每个写入点。root 不存在视为 no-op。
func EnsureTree(root string) error {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
			return nil // 路径尚未创建，无需迁移
		}
		return err
	}
	var firstErr error
	if err := EnsureMode(root, DirMode); err != nil && firstErr == nil {
		firstErr = err
	}
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// 不跟随符号链接，避免改到树外文件。
			return nil
		}
		var mode os.FileMode
		if info.IsDir() {
			mode = DirMode
		} else if info.Mode().IsRegular() {
			mode = FileMode
		} else {
			return nil // socket/device 等
		}
		if err := EnsureMode(path, mode); err != nil && firstErr == nil {
			firstErr = err
		}
		return nil
	})
	if walkErr != nil && firstErr == nil {
		firstErr = walkErr
	}
	return firstErr
}

// CopyFileViaSecure 从 r 拷贝到 path，权限 0600、目录 0700。供迁移旧文件用。
func CopyFileViaSecure(path string, r io.Reader) error {
	if err := MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, FileMode)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return EnsureMode(path, FileMode)
}

// WriteViaTempSync 用「唯一临时文件 + Sync + 原子 rename + 目录 fsync」写文件（F-13 等价）。
//
// 与 WriteViaTemp 的区别：写完临时文件后显式 file.Sync() 把数据刷到稳定存储再 Close 再 rename，
// 并对父目录做 fsync（Unix），保证 rename 完成时数据与目录项均已持久——崩溃窗口内不会留下
// 零字节/截断的目标文件。tempPath 用 pid+纳秒后缀保证唯一，避免固定 .tmp 名在并发/重入时碰撞。
//
// mode 由调用方决定：敏感数据用 FileMode(0600)；Cursor settings.json 这类属于宿主应用
// 的用户配置文件应保持 0644（不可收紧到 0600，否则改变 Cursor 配置文件既有权限语义）。
// 已存在的目标文件若权限与 mode 不同，写后收紧到 mode（与 WriteFile 一致）。
func WriteViaTempSync(path string, data []byte, mode os.FileMode) error {
	if err := MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	var tempPath string
	var file *os.File
	var err error
	for attempt := 0; attempt < 32; attempt++ {
		tempPath = filepath.Join(dir, fmt.Sprintf("%s.tmp-%d-%d", base, os.Getpid(), time.Now().UnixNano()))
		file, err = os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return err
		}
	}
	if err != nil {
		return fmt.Errorf("exhausted temp file attempts: %w", err)
	}

	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	// F-13：rename 前 Sync 把数据刷到磁盘，避免 rename 后崩溃留下零字节目标。
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	// Windows 上同名 rename 是替换语义且原子；仍提供有限重试以应对目标被短暂占用。
	if err := renameWithRetry(tempPath, path); err != nil {
		return err
	}
	renamed = true
	if err := EnsureMode(path, mode); err != nil {
		return err
	}
	return syncDir(dir)
}

// TryCreateExclusive 以 O_CREATE|O_EXCL 独占创建 path 并写入 data（权限 0600）。
// 已存在返回 os.ErrExist（包裹在 err 里，调用方用 os.IsExist/ errors.Is(err, os.ErrExist) 判定）。
// 用于"仅当无备份时写"场景：把 Stat-then-Write 的 TOCTOU 收敛为单次原子创建——
// 创建失败即说明已有备份或并发写者，调用方据此保留不动。
func TryCreateExclusive(path string, data []byte) error {
	if err := MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, FileMode)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return EnsureMode(path, FileMode)
}

// renameWithRetry 在 Windows 上对 rename 做有限重试（目标可能被防病毒/编辑器短暂占用）。
func renameWithRetry(tempPath, path string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(tempPath, path)
	}
	delay := 10 * time.Millisecond
	var lastErr error
	for attempt := 0; attempt < 12; attempt++ {
		lastErr = os.Rename(tempPath, path)
		if lastErr == nil {
			return nil
		}
		if os.IsNotExist(lastErr) || attempt == 11 {
			break
		}
		time.Sleep(delay)
		if delay < 200*time.Millisecond {
			delay *= 2
		}
	}
	return lastErr
}

// syncDir 在 Unix 上 fsync 父目录（rename 的目录项持久化）；Windows no-op。
func syncDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
