package updater

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cursor/internal/buildinfo"
)

// platformKeyForTest 返回当前测试运行机的平台键，用于构造 manifest 的 platforms map，
// 确保 fetchUpdateInfo 的平台查找步骤命中。
func platformKeyForTest(t *testing.T) string {
	t.Helper()
	key, err := currentPlatformKey()
	if err != nil {
		t.Fatalf("currentPlatformKey: %v", err)
	}
	return key
}

// signManifestForTest 用 priv 对 m 签名（canonical = signature 清空 + compact JSON），
// 与 scripts/release sign 子命令、verifyManifestSignature 完全一致。
func signManifestForTest(t *testing.T, m *manifest, priv ed25519.PrivateKey) {
	t.Helper()
	m.Signature = ""
	canonical, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal canonical: %v", err)
	}
	m.Signature = hex.EncodeToString(ed25519.Sign(priv, canonical))
}

// TestFetchUpdateInfoRejectsTamperedVersionBeforeTrust (F-27) 验证：公钥已配置时，
// 即便 manifest 把 Version 伪造成"已是最新版本"（<= current），验签失败仍先一步拒绝，
// 不会被当作"无更新"而冻结提示。
func TestFetchUpdateInfoRejectsTamperedVersionBeforeTrust(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	origKey := releasePublicKeyHex
	releasePublicKeyHex = hex.EncodeToString(pub)
	defer func() { releasePublicKeyHex = origKey }()
	origVer := buildinfo.Version
	buildinfo.Version = "9.9.9" // 当前版本极高
	defer func() { buildinfo.Version = origVer }()

	// 构造一个"无更新"manifest（Version 0.0.1 < 9.9.9），然后篡改 Version 字段但不重签。
	m := &manifest{
		Version:     "0.0.1",
		ReleaseDate: "2026-07-28T00:00:00Z",
		Platforms: map[string]manifestPlatform{
			"windows-amd64": {URL: "https://example.com/a.zip", Size: 10, Checksum: "sha256:00"},
		},
	}
	signManifestForTest(t, m, priv)
	// 签名后再篡改 Version——签名不再匹配。这是 F-27 的攻击面：
	// 攻击者伪造"低版本"或"同版本"manifest，期望被当作"已是最新"。
	m.Version = "9.9.9"

	body, _ := json.Marshal(m)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	mgr := newTestManager(http.DefaultClient, srv.URL+"/")
	_, err = mgr.fetchUpdateInfo(context.Background())
	if err == nil {
		t.Fatalf("tampered manifest must be rejected before trusting Version (F-27), got nil err")
	}
	if !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatalf("expected signature verification failure, got: %v", err)
	}
}

// TestFetchUpdateInfoAcceptsSignedNewerManifest (F-27 回归) 验证：正确签名的更高版本 manifest
// 通过验签并返回非 nil UpdateInfo——F-27 重排后正常路径不破坏。
func TestFetchUpdateInfoAcceptsSignedNewerManifest(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	origKey := releasePublicKeyHex
	releasePublicKeyHex = hex.EncodeToString(pub)
	defer func() { releasePublicKeyHex = origKey }()
	origVer := buildinfo.Version
	buildinfo.Version = "0.0.1"
	defer func() { buildinfo.Version = origVer }()

	m := &manifest{
		Version:     "9.9.9",
		ReleaseDate: "2026-07-28T00:00:00Z",
		Platforms: map[string]manifestPlatform{
			platformKeyForTest(t): {URL: "https://example.com/a.tar.gz", Size: 10, Checksum: "sha256:00"},
		},
	}
	signManifestForTest(t, m, priv)
	body, _ := json.Marshal(m)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	mgr := newTestManager(http.DefaultClient, srv.URL+"/")
	info, err := mgr.fetchUpdateInfo(context.Background())
	if err != nil {
		t.Fatalf("signed newer manifest should pass: %v", err)
	}
	if info == nil || info.Version != "9.9.9" {
		t.Fatalf("expected UpdateInfo v9.9.9, got %+v", info)
	}
}

// TestFetchUpdateInfoRejectsOversizedManifest (F-25) 验证：manifest Content-Length
// 超过 maxManifestBytes 时直接拒绝，防恶意更新服务耗尽内存。
func TestFetchUpdateInfoRejectsOversizedManifest(t *testing.T) {
	// 无需配置公钥——体积检查发生在验签之前。
	oversize := make([]byte, maxManifestBytes+1)
	for i := range oversize {
		oversize[i] = 'a'
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(oversize)))
		w.Write(oversize)
	}))
	defer srv.Close()

	mgr := newTestManager(http.DefaultClient, srv.URL+"/")
	_, err := mgr.fetchUpdateInfo(context.Background())
	if err == nil {
		t.Fatalf("oversized manifest must be rejected (F-25)")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected too-large error, got: %v", err)
	}
}

// TestDownloadUpdateRejectsSizeMismatch (F-25) 验证：manifest 声明的 asset.Size
// 与实际下载字节数不符时拒绝，防资产被截断或额外注入。
// 用"声明 10，实际 5"测过小分支（ContentLength < limit 不触发 too-large，走 size mismatch）。
func TestDownloadUpdateRejectsSizeMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 5))
	}))
	defer srv.Close()

	info := &UpdateInfo{
		Version:     "9.9.9",
		PlatformKey: "windows-amd64",
		Asset: manifestPlatform{
			URL:      srv.URL + "/asset",
			Size:     10, // 声明 10 字节，实际收到 5
			Checksum: "sha256:00",
		},
	}
	mgr := newTestManager(http.DefaultClient, srv.URL+"/")
	_, err := mgr.downloadUpdate(context.Background(), info)
	if err == nil {
		t.Fatalf("size mismatch must be rejected (F-25)")
	}
	if !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("expected size mismatch error, got: %v", err)
	}
}

// TestDownloadUpdateRejectsContentLengthOverLimit (F-25) 验证：Content-Length 声明
// 超过全局上限时直接拒绝，避免无谓下载。
func TestDownloadUpdateRejectsContentLengthOverLimit(t *testing.T) {
	info := &UpdateInfo{
		Version:     "9.9.9",
		PlatformKey: "windows-amd64",
		Asset: manifestPlatform{
			URL:      "https://example.com/a",
			Size:     0, // 不声明 size → 走 maxAssetBytes 全局上限
			Checksum: "",
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", maxAssetBytes+1))
	}))
	defer srv.Close()
	// 把 URL 指向本地服务器以便命中 Content-Length 检查路径。
	info.Asset.URL = srv.URL + "/big"

	mgr := newTestManager(http.DefaultClient, srv.URL+"/")
	_, err := mgr.downloadUpdate(context.Background(), info)
	if err == nil {
		t.Fatalf("Content-Length over limit must be rejected (F-25)")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected too-large error, got: %v", err)
	}
}
