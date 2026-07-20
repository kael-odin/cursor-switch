package certs

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
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
		return cert, nil
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
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
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

	leafPrivateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	leafPublicKey := &leafPrivateKey.PublicKey

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

	switch keyBlock.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, nil, err
		}
		return caCert, key, nil
	case "EC PRIVATE KEY":
		key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, nil, err
		}
		return caCert, key, nil
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, nil, err
		}
		return caCert, key, nil
	default:
		return nil, nil, errors.New("unsupported CA key format")
	}
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
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return nil, nil, fmt.Errorf("create ca dir: %w", err)
	}

	existingCert, existingKey, loadErr := loadCAPEMFromFiles(certPath, keyPath)
	if loadErr == nil {
		return existingCert, existingKey, nil
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
		Organization: []string{"cursor-byok"},
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

	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, nil, fmt.Errorf("write ca cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		_ = os.Remove(certPath)
		return nil, nil, fmt.Errorf("write ca key: %w", err)
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
