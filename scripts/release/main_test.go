package main

import (
	"strings"
	"testing"
)

// TestRewriteManifestVersionsPreservesCommonControls 锁定 sync-versions 的 manifest 修复：
// 主 assemblyIdentity（com.cursor.wuxianxubei，不带 publicKeyToken）的 version 跟项目版本走，
// 但 <dependency> 里的 Microsoft.Windows.Common-Controls（带 publicKeyToken）version 必须恒为
// 6.0.0.0（Windows 通用控件 6.0 标准版本号，与项目版本无关）——此前 sync-versions 的正则
// 把它也同步成项目版本（2.0.x），属 bug。
func TestRewriteManifestVersionsPreservesCommonControls(t *testing.T) {
	manifest := []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly manifestVersion="1.0" xmlns="urn:schemas-microsoft-com:asm.v1">
    <assemblyIdentity type="win32" name="com.cursor.wuxianxubei" version="1.2.3" processorArchitecture="*"/>
    <dependency>
        <dependentAssembly>
            <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0" processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/>
        </dependentAssembly>
    </dependency>
</assembly>`)

	out, ok := rewriteManifestVersions(manifest, "2.0.4")
	if !ok {
		t.Fatal("expected ok=true (main assemblyIdentity found)")
	}
	s := string(out)

	// 1) 主 assemblyIdentity version 改成 2.0.4。
	if !strings.Contains(s, `name="com.cursor.wuxianxubei" version="2.0.4"`) {
		t.Errorf("main assemblyIdentity version not updated to 2.0.4:\n%s", s)
	}
	if strings.Contains(s, `name="com.cursor.wuxianxubei" version="1.2.3"`) {
		t.Errorf("old main version 1.2.3 still present:\n%s", s)
	}

	// 2) Common-Controls 保留 6.0.0.0，不被改成 2.0.4。
	if !strings.Contains(s, `name="Microsoft.Windows.Common-Controls" version="6.0.0.0"`) {
		t.Errorf("Common-Controls version not preserved as 6.0.0.0:\n%s", s)
	}
	if strings.Contains(s, `Microsoft.Windows.Common-Controls" version="2.0.4"`) {
		t.Errorf("Common-Controls version wrongly changed to 2.0.4:\n%s", s)
	}
}

// TestRewriteManifestVersionsIdempotent 幂等性：已是目标 version 的 manifest 再跑一次不报错、
// 内容不变（此前实现在「已是正确 version」时 exitf 失败，是 bug）。
func TestRewriteManifestVersionsIdempotent(t *testing.T) {
	manifest := []byte(`<assemblyIdentity type="win32" name="com.cursor.wuxianxubei" version="2.0.4" processorArchitecture="*"/>`)
	out, ok := rewriteManifestVersions(manifest, "2.0.4")
	if !ok {
		t.Fatal("expected ok=true on idempotent re-run")
	}
	if string(out) != string(manifest) {
		t.Errorf("idempotent re-run changed content:\nwant: %s\ngot:  %s", manifest, out)
	}
}

// TestRewriteManifestVersionsNoMainAssemblyIdentity 主 assemblyIdentity 缺失时返回 ok=false。
func TestRewriteManifestVersionsNoMainAssemblyIdentity(t *testing.T) {
	// 只有带 publicKeyToken 的 Common-Controls，没有主 assemblyIdentity。
	manifest := []byte(`<assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0" publicKeyToken="6595b64144ccf1df"/>`)
	_, ok := rewriteManifestVersions(manifest, "2.0.4")
	if ok {
		t.Error("expected ok=false when no main (non-publicKeyToken) assemblyIdentity present")
	}
}
