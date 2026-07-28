package certs

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cursor/internal/securefile"

	"github.com/denisbrodbeck/machineid"
)

// Manager 定义了当前模块中的 Manager 类型。
type Manager struct {
	// caCert 表示当前声明中的 caCert。
	caCert *x509.Certificate
	// caKey 表示当前声明中的 caKey。
	caKey crypto.PrivateKey

	// mu 表示当前声明中的 mu。
	mu sync.Mutex
	// cache 表示当前声明中的 cache。
	cache map[string]*tls.Certificate
}

// NewManager 用于处理与 NewManager 相关的逻辑。
func NewManager(caCertPath, caKeyPath string) (*Manager, error) {
	certPEM, keyPEM, err := loadCAPEMFromFiles(caCertPath, caKeyPath)
	if err != nil {
		return nil, err
	}
	return NewManagerFromPEM(certPEM, keyPEM)
}

// NewManagerFromPEM 用于处理与 NewManagerFromPEM 相关的逻辑。
func NewManagerFromPEM(caCertPEM, caKeyPEM []byte) (*Manager, error) {
	caCert, caKey, err := loadCAFromPEM(caCertPEM, caKeyPEM)
	if err != nil {
		return nil, err
	}
	return &Manager{caCert: caCert, caKey: caKey, cache: make(map[string]*tls.Certificate)}, nil
}

// CATLSCertificate 用于处理与 CATLSCertificate 相关的逻辑。
func (m *Manager) CATLSCertificate() (*tls.Certificate, error) {
	if m == nil || m.caCert == nil || m.caKey == nil {
		return nil, errors.New("CA is not initialized")
	}
	return &tls.Certificate{
		Certificate: [][]byte{append([]byte(nil), m.caCert.Raw...)},
		PrivateKey:  m.caKey,
		Leaf:        m.caCert,
	}, nil
}

// CertificateForServerName 用于处理与 CertificateForServerName 相关的逻辑。
func (m *Manager) CertificateForServerName(serverName string) (*tls.Certificate, error) {
	host := normalizeHost(serverName)
	if host == "" {
		return nil, errors.New("empty server name")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if cert, ok := m.cache[host]; ok {
		// M7: cache 命中时检查 leaf NotAfter，过期则重签（旧实现永久缓存，1 年后用过期证书握手）。
		if cert != nil && cert.Leaf != nil && time.Now().Before(cert.Leaf.NotAfter) {
			return cert, nil
		}
		// 过期或 Leaf 缺失：落到下面重签并覆盖 cache。
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	leaf := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   host,
			Organization: []string{"Cursor Local Proxy"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		// ECDSA 只用 DigitalSignature；KeyEncipherment 是 RSA key transport 专用，ECDSA 无意义。
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if len(m.caCert.SubjectKeyId) > 0 {
		leaf.AuthorityKeyId = append([]byte(nil), m.caCert.SubjectKeyId...)
	}

	if ip := net.ParseIP(host); ip != nil {
		leaf.IPAddresses = []net.IP{ip}
	} else {
		leaf.DNSNames = []string{host}
	}

	// M7: leaf key 改 ECDSA P-256——比 RSA-2048 签发更快、证书更小，TLS 握手延迟更低。
	leafPrivateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	leafPublicKey := leafPrivateKey.Public()

	der, err := x509.CreateCertificate(rand.Reader, leaf, m.caCert, leafPublicKey, m.caKey)
	if err != nil {
		return nil, err
	}

	leafCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	chainPEM := append([]byte(nil), leafCertPEM...)
	chainPEM = append(chainPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: m.caCert.Raw})...)

	keyPEM, err := marshalPrivateKeyPEM(leafPrivateKey)
	if err != nil {
		return nil, err
	}

	pair, err := tls.X509KeyPair(chainPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	parsedLeaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	pair.Leaf = parsedLeaf

	m.cache[host] = &pair
	return &pair, nil
}

// marshalPrivateKeyPEM 用于处理与 marshalPrivateKeyPEM 相关的逻辑。
func marshalPrivateKeyPEM(key any) ([]byte, error) {
	switch k := key.(type) {
	case *rsa.PrivateKey:
		return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}), nil
	case *ecdsa.PrivateKey:
		der, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			return nil, err
		}
		return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
	case ed25519.PrivateKey:
		der, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			return nil, err
		}
		return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
	default:
		return nil, errors.New("unsupported private key type")
	}
}

// loadCAPEMFromFiles 用于处理与 loadCAPEMFromFiles 相关的逻辑。
func loadCAPEMFromFiles(certPath, keyPath string) ([]byte, []byte, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	return certPEM, keyPEM, nil
}

// loadCAFromPEM 用于处理与 loadCAFromPEM 相关的逻辑。
func loadCAFromPEM(certPEM, keyPEM []byte) (*x509.Certificate, crypto.PrivateKey, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, errors.New("invalid CA cert PEM")
	}
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, errors.New("invalid CA key PEM")
	}

	var caKey crypto.PrivateKey
	switch keyBlock.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, nil, err
		}
		caKey = key
	case "EC PRIVATE KEY":
		key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, nil, err
		}
		caKey = key
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, nil, err
		}
		caKey = key
	default:
		return nil, nil, errors.New("unsupported CA key format")
	}

	// F-12：加载后校验 CA 属性，防用户把"叶子证书"或"过期/损坏 CA"当 CA 用，
	// 导致 leaf 签发静默失败或签出无效证书。
	if err := validateCALoaded(caCert, caKey); err != nil {
		return nil, nil, err
	}
	return caCert, caKey, nil
}

// validateCALoaded 校验加载到的 CA 证书 + 私钥对满足 CA 使用前提（F-12）：
//   - 公私钥匹配：证书的 PublicKey 与从私钥导出的公钥一致。不匹配说明 cert/key 来自不同对，
//     用此 key 签发的 leaf 不会被此 cert 验证通过。
//   - IsCA=true 且 KeyUsage 含 CertSign：CA 必须具备签发能力。
//   - NotAfter 未过期：过期 CA 签发的 leaf 在多数 TLS 校验链里会连带失败。
//
// 不匹配/非 CA/过期均拒绝，调用方（EnsureMachineCA）会落到重新生成路径。
func validateCALoaded(caCert *x509.Certificate, caKey crypto.PrivateKey) error {
	if !caCert.IsCA {
		return errors.New("CA cert is not a CA (IsCA=false)")
	}
	if caCert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return errors.New("CA cert lacks KeyUsageCertSign")
	}
	if time.Now().After(caCert.NotAfter) {
		return fmt.Errorf("CA cert expired at %s", caCert.NotAfter.Format(time.RFC3339))
	}
	if err := validateKeyPairMatch(caCert, caKey); err != nil {
		return err
	}
	return nil
}

// validateKeyPairMatch 比对证书公钥与私钥导出公钥，确认两者属同一密钥对。
func validateKeyPairMatch(caCert *x509.Certificate, caKey crypto.PrivateKey) error {
	switch pub := caCert.PublicKey.(type) {
	case *rsa.PublicKey:
		priv, ok := caKey.(*rsa.PrivateKey)
		if !ok {
			return errors.New("CA cert is RSA but key is not RSA private key")
		}
		if pub.N.Cmp(priv.N) != 0 || pub.E != priv.E {
			return errors.New("CA cert/key public key mismatch (RSA)")
		}
	case *ecdsa.PublicKey:
		priv, ok := caKey.(*ecdsa.PrivateKey)
		if !ok {
			return errors.New("CA cert is ECDSA but key is not ECDSA private key")
		}
		if pub.X.Cmp(priv.X) != 0 || pub.Y.Cmp(priv.Y) != 0 {
			return errors.New("CA cert/key public key mismatch (ECDSA)")
		}
	case ed25519.PublicKey:
		priv, ok := caKey.(ed25519.PrivateKey)
		if !ok {
			return errors.New("CA cert is Ed25519 but key is not Ed25519 private key")
		}
		if !pub.Equal(priv.Public()) {
			return errors.New("CA cert/key public key mismatch (Ed25519)")
		}
	default:
		return errors.New("unsupported CA public key type")
	}
	return nil
}

// normalizeHost 用于处理与 normalizeHost 相关的逻辑。
func normalizeHost(serverName string) string {
	serverName = strings.TrimSpace(serverName)
	if strings.Contains(serverName, ":") {
		h, _, err := net.SplitHostPort(serverName)
		if err == nil {
			serverName = h
		}
	}
	return serverName
}

// EnsureMachineCA 确保本机 CA 证书与私钥存在于 certPath/keyPath。
// 若两文件都已存在则读回；否则生成一份本机独立的自签 CA（RSA-4096，10 年）并写盘（私钥 0600）。
// 返回证书与私钥的 PEM。该机制取代了历史上嵌入共享 CA：每台机器一份独立 CA，
// 私钥从不入库、不进二进制，避免一份泄露导致全球所有安装的信任锚失效。
func EnsureMachineCA(certPath, keyPath string) (certPEM, keyPEM []byte, err error) {
	if certPath == "" || keyPath == "" {
		return nil, nil, errors.New("ca cert/key path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return nil, nil, fmt.Errorf("create ca dir: %w", err)
	}

	// F-18：CA 目录收紧到 0700（含 cert 和 key），防止其他本地用户进入目录。
	_ = securefile.EnsureMode(filepath.Dir(certPath), 0o700)

	existingCert, existingKey, loadErr := loadCAPEMFromFiles(certPath, keyPath)
	if loadErr == nil {
		// F-12：既有文件能解析——loadCAFromPEM 会做 IsCA/KeyUsage/NotAfter/公私钥匹配校验，
		// 校验失败 loadErr != nil 走到下面重新生成。成功则返回。
		if _, _, err := loadCAFromPEM(existingCert, existingKey); err != nil {
			// 既有 CA 已损坏/过期/不匹配：备份后重建，不静默覆盖。
			_ = os.Rename(certPath, certPath+".corrupt.bak")
			_ = os.Rename(keyPath, keyPath+".corrupt.bak")
			loadErr = err
		} else {
			return existingCert, existingKey, nil
		}
	}
	// 部分存在（只有证书或只有私钥）视为损坏，重新生成覆盖。
	if !os.IsNotExist(loadErr) && !errors.Is(loadErr, os.ErrNotExist) {
		// 文件存在但读取/解析失败：备份后重建，避免静默覆盖用户数据。
		_ = os.Rename(certPath, certPath+".corrupt.bak")
		_ = os.Rename(keyPath, keyPath+".corrupt.bak")
	}

	caKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, nil, fmt.Errorf("generate ca key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate ca serial: %w", err)
	}

	machineTag := machineTag()
	subject := pkix.Name{
		CommonName:   "Cursor Local Machine CA",
		Organization: []string{"cursor-switch"},
		OrganizationalUnit: []string{machineTag},
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               subject,
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create ca certificate: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM, err = marshalPrivateKeyPEM(caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal ca key: %w", err)
	}

	// F-12：原子写——先写临时文件再 rename，防止写一半崩溃留下半截 CA。
	// cert 公开可读（0644），key 仅属主（0600，沿用既有）；目录 0700。
	tempCert := certPath + ".tmp"
	tempKey := keyPath + ".tmp"
	if err := os.WriteFile(tempCert, certPEM, 0o644); err != nil {
		_ = os.Remove(tempCert)
		_ = os.Remove(tempKey)
		return nil, nil, fmt.Errorf("write ca cert: %w", err)
	}
	if err := os.WriteFile(tempKey, keyPEM, 0o600); err != nil {
		_ = os.Remove(tempCert)
		_ = os.Remove(tempKey)
		return nil, nil, fmt.Errorf("write ca key: %w", err)
	}
	// F-18：key 显式 chmod 0600，收紧既有或被 umask 放宽的情况。
	_ = securefile.EnsureMode(tempKey, 0o600)
	if err := os.Rename(tempCert, certPath); err != nil {
		_ = os.Remove(tempCert)
		_ = os.Remove(tempKey)
		return nil, nil, fmt.Errorf("rename ca cert: %w", err)
	}
	if err := os.Rename(tempKey, keyPath); err != nil {
		_ = os.Remove(tempKey)
		return nil, nil, fmt.Errorf("rename ca key: %w", err)
	}

	// 复用既有校验路径，确保写盘的 CA 能被正常加载。
	if _, _, err := loadCAFromPEM(certPEM, keyPEM); err != nil {
		return nil, nil, fmt.Errorf("verify generated ca: %w", err)
	}
	return certPEM, keyPEM, nil
}

// machineTag 返回本机标识的短哈希，嵌入 CA Subject 用于区分不同机器，失败时回退到占位串。
func machineTag() string {
	if id, err := machineid.ProtectedID("cursor-byok-ca"); err == nil && id != "" {
		if len(id) > 16 {
			return id[:16]
		}
		return id
	}
	return "unknown-machine"
}
