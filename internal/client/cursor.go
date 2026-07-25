package client

import (
	"fmt"
	goruntime "runtime"

	"cursor/internal/cursor"
)

// ApplyCursorSettings 用于处理与 ApplyCursorSettings 相关的逻辑。
func (s *ProxyService) ApplyCursorSettings() error {
	if s == nil || s.proxy == nil {
		return fmt.Errorf("proxy is not initialized")
	}
	s.caFileMu.Lock()
	caCertPath, err := cursor.EnsureCACertFile(s.caCertPEM, s.caFilePath)
	if err == nil {
		s.caFilePath = caCertPath
	}
	s.caFileMu.Unlock()
	if err != nil {
		return fmt.Errorf("ensure ca cert file: %w", err)
	}

	switch goruntime.GOOS {
	case "windows":
		if err := cursor.EnsureCACertInstalled(s.caCertPEM, caCertPath); err != nil {
			return fmt.Errorf("install ca cert: %w", err)
		}
	case "darwin":
		if err := cursor.EnsureCACertInstalled(s.caCertPEM, caCertPath); err != nil {
			return fmt.Errorf("install ca cert: %w", err)
		}
		if err := cursor.SetSystemNodeExtraCACerts(caCertPath); err != nil {
			return fmt.Errorf("set node extra ca certs: %w", err)
		}
	}

	if err := cursor.WriteUserProxySettings(cursor.ProxyURLFromListenAddr(s.proxy.Snapshot().ListenAddr)); err != nil {
		return err
	}
	s.setCursorSettingsApplied(true)
	return nil
}

// ClearCursorSettings 用于处理与 ClearCursorSettings 相关的逻辑。
// 注意：停止服务时不再卸载 CA 证书——CA 是每机器独立的，保留信任锚可让下次启动
// 直接复用、避免每次开关都弹 UAC。CA 仅在 byok 运行时被代理使用，停止后无法被利用。
// 如需彻底移除 CA，调用 UninstallCACert 手动卸载。
func (s *ProxyService) ClearCursorSettings() error {
	if goruntime.GOOS == "darwin" {
		if err := cursor.ClearSystemNodeExtraCACerts(); err != nil {
			return err
		}
	}
	if err := cursor.ClearUserProxySettings(); err != nil {
		return err
	}
	s.setCursorSettingsApplied(false)
	return nil
}

// UninstallCACert 手动从系统信任存储移除本机 CA（best-effort，需 UAC 提权）。
// 供 GUI「卸载证书」按钮调用，停止服务时不再自动卸载。
func (s *ProxyService) UninstallCACert() error {
	if s.caCertPEM == nil {
		return fmt.Errorf("ca cert not available")
	}
	return cursor.UninstallCACert(s.caCertPEM)
}

// GetDeviceID 用于处理与 GetDeviceID 相关的逻辑。
func (s *ProxyService) GetDeviceID() (string, error) {
	return cursor.GetDeviceID()
}
