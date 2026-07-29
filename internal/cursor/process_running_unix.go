//go:build !windows

package cursor

import (
	"os/exec"
	"strings"
)

// isCursorProcessRunning 检测 Cursor 进程是否正在运行（按可执行名匹配）。
// 用于 P0-4：RepairLegacyInjectedIdentity 在 Cursor 运行时会与 Cursor 竞争
// state.vscdb 写锁，可能损坏用户状态库或被 SQLITE_BUSY 拒绝。检测到运行中则
// 跳过修复（下次未运行时再修）。
func isCursorProcessRunning() bool {
	// pgrep -i cursor 覆盖 darwin/linux；忽略大小写匹配进程名含 cursor。
	out, err := exec.Command("pgrep", "-i", "cursor").Output()
	if err != nil {
		// pgrep 退出码 1 表示无匹配（未运行），非错误。
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}
