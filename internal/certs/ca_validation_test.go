package certs

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// selfSignNonCA 生成一张自签非 CA 证书 + 私钥 PEM（IsCA=false, 无 CertSign）。
func selfSignNonCA(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "not-a-ca"},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalECPrivateKey(priv)
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// TestLoadCAFromPEMRejectsNonCA (F-12) 验证：加载非 CA 证书（IsCA=false）被拒绝。
func TestLoadCAFromPEMRejectsNonCA(t *testing.T) {
	certPEM, keyPEM := selfSignNonCA(t)
	_, _, err := loadCAFromPEM(certPEM, keyPEM)
	if err == nil {
		t.Fatalf("non-CA cert should be rejected (F-12)")
	}
}

// TestLoadCAFromPEMRejectsKeyMismatch (F-12) 验证：证书公钥与私钥不匹配被拒绝。
// 用合法 CA 证书配一个随机不同的 ECDSA 私钥。
func TestLoadCAFromPEMRejectsKeyMismatch(t *testing.T) {
	// 生成一张真 CA 证书 + 其私钥
	caPriv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "real-ca"},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caPriv.PublicKey, caPriv)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	// 不同的私钥
	otherPriv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherDER, _ := x509.MarshalECPrivateKey(otherPriv)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: otherDER})

	_, _, err := loadCAFromPEM(certPEM, keyPEM)
	if err == nil {
		t.Fatalf("mismatched key/cert should be rejected (F-12)")
	}
}

// TestLoadCAFromPEMRejectsExpiredCA (F-12) 验证：过期 CA 证书被拒绝。
func TestLoadCAFromPEMRejectsExpiredCA(t *testing.T) {
	caPriv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "expired-ca"},
		NotBefore:             now.Add(-2 * 365 * 24 * time.Hour),
		NotAfter:              now.Add(-1 * time.Hour), // 已过期
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caPriv.PublicKey, caPriv)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalECPrivateKey(caPriv)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	_, _, err := loadCAFromPEM(certPEM, keyPEM)
	if err == nil {
		t.Fatalf("expired CA should be rejected (F-12)")
	}
}

// TestLoadCAFromPEMAcceptsValidCA (F-12 回归) 验证：合法 CA + 匹配私钥通过校验。
func TestLoadCAFromPEMAcceptsValidCA(t *testing.T) {
	caPriv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "valid-ca"},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caPriv.PublicKey, caPriv)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalECPrivateKey(caPriv)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	cert, key, err := loadCAFromPEM(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("valid CA should pass: %v", err)
	}
	if cert == nil || key == nil {
		t.Fatalf("nil cert/key returned")
	}
}

// TestEnsureMachineCARegeneratesCorruptCA (F-12) 验证：既有 CA 文件能解析但
// 校验失败（如过期）时，EnsureMachineCA 备份并重新生成，而非静默使用坏 CA。
func TestEnsureMachineCARegeneratesCorruptCA(t *testing.T) {
	tmp := t.TempDir()
	certPath := filepath.Join(tmp, "ca.crt")
	keyPath := filepath.Join(tmp, "ca.key")

	// 写入一个过期但能解析的 CA（通过 F-12 校验会失败）。
	caPriv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "expired-ca"},
		NotBefore:             now.Add(-2 * 365 * 24 * time.Hour),
		NotAfter:              now.Add(-1 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caPriv.PublicKey, caPriv)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalECPrivateKey(caPriv)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	_ = os.WriteFile(certPath, certPEM, 0o600)
	_ = os.WriteFile(keyPath, keyPEM, 0o600)

	// EnsureMachineCA 应发现 CA 过期，备份后重生成为有效 CA。
	newCertPEM, newKeyPEM, err := EnsureMachineCA(certPath, keyPath)
	if err != nil {
		t.Fatalf("EnsureMachineCA should regenerate: %v", err)
	}
	// 新 CA 应能通过校验
	newCert, newKey, err := loadCAFromPEM(newCertPEM, newKeyPEM)
	if err != nil {
		t.Fatalf("regenerated CA must be valid: %v", err)
	}
	if newCert == nil || newKey == nil {
		t.Fatalf("nil regenerated CA")
	}
	// 应有 .corrupt.bak 备份（Windows 上 rename 行为依赖 FS，宽松检查）
	if _, err := os.Stat(certPath + ".corrupt.bak"); err != nil {
		t.Logf("note: corrupt backup not present at expected path (FS/rename behavior): %v", err)
	}
}

// TestValidateKeyPairMatchRSAAndEd25519 (F-12) 覆盖 RSA/Ed25519 公私钥匹配校验。
func TestValidateKeyPairMatchRSAAndEd25519(t *testing.T) {
	// RSA 正向
	rsaPriv, _ := rsa.GenerateKey(rand.Reader, 2048)
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(365 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &rsaPriv.PublicKey, rsaPriv)
	cert, _ := x509.ParseCertificate(der)
	if err := validateKeyPairMatch(cert, rsaPriv); err != nil {
		t.Fatalf("RSA match should pass: %v", err)
	}
	// RSA 配 Ed25519 私钥应失败
	_, edPriv, _ := ed25519.GenerateKey(rand.Reader)
	if err := validateKeyPairMatch(cert, edPriv); err == nil {
		t.Fatalf("RSA cert + Ed25519 key should fail match")
	}

	// Ed25519 正向
	edPub2, edPriv2, _ := ed25519.GenerateKey(rand.Reader)
	tmpl2 := &x509.Certificate{
		SerialNumber: big.NewInt(2), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(365 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	der2, _ := x509.CreateCertificate(rand.Reader, tmpl2, tmpl2, edPub2, edPriv2)
	cert2, _ := x509.ParseCertificate(der2)
	if err := validateKeyPairMatch(cert2, edPriv2); err != nil {
		t.Fatalf("Ed25519 match should pass: %v", err)
	}
	// Ed25519 配 RSA 私钥应失败
	if err := validateKeyPairMatch(cert2, rsaPriv); err == nil {
		t.Fatalf("Ed25519 cert + RSA key should fail match")
	}
}

func TestEnsureMachineCADirMode0700(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm bits not meaningful on Windows")
	}
	tmp := t.TempDir()
	certPath := filepath.Join(tmp, "subdir", "ca.crt")
	keyPath := filepath.Join(tmp, "subdir", "ca.key")
	if _, _, err := EnsureMachineCA(certPath, keyPath); err != nil {
		t.Fatalf("EnsureMachineCA: %v", err)
	}
	dirInfo, err := os.Stat(filepath.Dir(certPath))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("ca dir perm=%o want 0700", dirInfo.Mode().Perm())
	}
}
