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
	"io"
	"os"
	"path/filepath"
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
