package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestEnsureMachineCA_GeneratesAndReloads(t *testing.T) {
	tmp := t.TempDir()
	certPath := filepath.Join(tmp, "ca.crt")
	keyPath := filepath.Join(tmp, "ca.key")

	certPEM1, keyPEM1, err := EnsureMachineCA(certPath, keyPath)
	if err != nil {
		t.Fatalf("first EnsureMachineCA: %v", err)
	}
	if len(certPEM1) == 0 || len(keyPEM1) == 0 {
		t.Fatalf("empty PEM returned")
	}

	// files exist with correct permissions (POSIX only; Windows ignores 0600)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(keyPath)
		if err != nil {
			t.Fatalf("stat key: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("key file perm = %o, want 0600", perm)
		}
	}

	// cert is a valid CA
	block, _ := pem.Decode(certPEM1)
	if block == nil {
		t.Fatalf("cert PEM invalid")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	if !cert.IsCA {
		t.Fatalf("generated cert IsCA=false")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Fatalf("generated cert missing CertSign usage")
	}

	// second call reloads same CA (no regeneration)
	certPEM2, keyPEM2, err := EnsureMachineCA(certPath, keyPath)
	if err != nil {
		t.Fatalf("second EnsureMachineCA: %v", err)
	}
	if string(certPEM1) != string(certPEM2) {
		t.Fatalf("second call regenerated cert (should reload)")
	}
	if string(keyPEM1) != string(keyPEM2) {
		t.Fatalf("second call regenerated key (should reload)")
	}
}

// TestLeafCertECDSAAndExpiryRefresh 是 M7 的回归测试：
//  1. leaf 证书用 ECDSA P-256（不是 RSA-2048）
//  2. cache 检查 NotAfter，过期 leaf 会被重签
func TestLeafCertECDSAAndExpiryRefresh(t *testing.T) {
	tmp := t.TempDir()
	certPath := filepath.Join(tmp, "ca.crt")
	keyPath := filepath.Join(tmp, "ca.key")
	caCertPEM, caKeyPEM, err := EnsureMachineCA(certPath, keyPath)
	if err != nil {
		t.Fatalf("EnsureMachineCA: %v", err)
	}
	manager, err := NewManagerFromPEM(caCertPEM, caKeyPEM)
	if err != nil {
		t.Fatalf("NewManagerFromPEM: %v", err)
	}

	pair, err := manager.CertificateForServerName("example.com")
	if err != nil {
		t.Fatalf("CertificateForServerName: %v", err)
	}
	if pair.Leaf == nil {
		t.Fatalf("leaf not parsed")
	}
	// ECDSA P-256 leaf key（不是 RSA）。
	ecdsaKey, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("leaf key type = %T, want *ecdsa.PrivateKey", pair.PrivateKey)
	}
	if ecdsaKey.Curve != elliptic.P256() {
		t.Errorf("leaf curve = %v, want P-256", ecdsaKey.Curve)
	}
	// leaf 不含 KeyEncipherment（ECDSA 无意义），只 DigitalSignature。
	if pair.Leaf.KeyUsage&x509.KeyUsageKeyEncipherment != 0 {
		t.Errorf("leaf has KeyUsageKeyEncipherment, ECDSA should not")
	}
	if pair.Leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Errorf("leaf missing KeyUsageDigitalSignature")
	}

	// 第二次调用应命中 cache 返回同一对象（未过期）。
	pair2, err := manager.CertificateForServerName("example.com")
	if err != nil {
		t.Fatalf("second CertificateForServerName: %v", err)
	}
	if pair2 != pair {
		t.Errorf("cache miss on second call (should return same cached cert when not expired)")
	}

	// M7 关键：手动把 cache 里的 leaf 改成已过期，验证下次调用重签而非返回过期证书。
	manager.mu.Lock()
	expired := *pair
	expiredLeaf := *pair.Leaf
	expiredLeaf.NotAfter = time.Now().Add(-1 * time.Hour) // 已过期
	expired.Leaf = &expiredLeaf
	manager.cache["example.com"] = &expired
	manager.mu.Unlock()

	pair3, err := manager.CertificateForServerName("example.com")
	if err != nil {
		t.Fatalf("refresh CertificateForServerName: %v", err)
	}
	if pair3.Leaf.NotAfter.Before(time.Now()) {
		t.Errorf("expired leaf was not re-issued; NotAfter still %v", pair3.Leaf.NotAfter)
	}
}
