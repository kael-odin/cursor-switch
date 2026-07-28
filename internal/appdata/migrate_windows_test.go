//go:build windows

package appdata

// canReadDespiteMode000 在 Windows 上恒真（unix 权限位无意义），但 Windows 路径
// 已在调用方 t.Skip，此 stub 仅保证符号存在。
func canReadDespiteMode000(path string) bool {
	return true
}
