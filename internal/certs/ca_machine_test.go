package certs

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"testing"
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
