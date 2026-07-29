//go:build windows

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
	// tasklist 输出进程名列，过滤含 cursor 的行（忽略大小写）。
	out, err := exec.Command("tasklist", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "cursor.exe") || strings.Contains(lower, "\"cursor\"") {
			return true
		}
	}
	return false
}
