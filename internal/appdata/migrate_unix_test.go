//go:build !windows

package appdata

import "os"

// canReadDespiteMode000 报告 chmod 000 的文件是否仍可读（root 或忽略 unix 权限的 fs）。
// 用于 F-29 测试判定：若可读则无法模拟"源不可读"的复制失败，跳过该用例而非误报。
func canReadDespiteMode000(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && len(data) > 0
}
