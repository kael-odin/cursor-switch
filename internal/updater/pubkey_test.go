package updater

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// TestVerifyManifestSignature_RoundTrip mirrors the scripts/release sign subcommand:
// marshal canonical (signature cleared), sign with ed25519, then verify.
// Guards against layout drift between the signing tool and the verifier.
func TestVerifyManifestSignature_RoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	m := &manifest{
		Version:      "0.0.99",
		ReleaseDate:  "2026-07-20T00:00:00Z",
		ReleaseNotes: "test notes",
		Platforms: map[string]manifestPlatform{
			"windows-amd64": {URL: "https://example.com/a.zip", Size: 123, Checksum: "sha256:abc"},
		},
		Mandatory: false,
	}

	// canonical bytes = signature cleared + compact JSON, exactly as scripts/release sign does.
	m.Signature = ""
	canonical, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal canonical: %v", err)
	}
	m.Signature = hex.EncodeToString(ed25519.Sign(priv, canonical))

	// Temporarily install the public key so verify enforces it.
	orig := releasePublicKeyHex
	releasePublicKeyHex = hex.EncodeToString(pub)
	defer func() { releasePublicKeyHex = orig }()

	ok, err := verifyManifestSignature(m)
	if err != nil || !ok {
		t.Fatalf("verify failed: ok=%v err=%v", ok, err)
	}

	// Tamper: flip a byte in the signature → must fail.
	tampered := *m
	sigBytes, _ := hex.DecodeString(tampered.Signature)
	sigBytes[0] ^= 0x01
	tampered.Signature = hex.EncodeToString(sigBytes)
	ok2, err2 := verifyManifestSignature(&tampered)
	if ok2 || err2 == nil {
		t.Fatalf("tampered signature should fail: ok=%v err=%v", ok2, err2)
	}

	// Missing signature with key configured → fail.
	missing := *m
	missing.Signature = ""
	ok3, err3 := verifyManifestSignature(&missing)
	if ok3 || err3 == nil {
		t.Fatalf("missing signature should fail when key configured: ok=%v err=%v", ok3, err3)
	}
}

// TestVerifyManifestSignature_NoKeyAcceptsUnsigned confirms the compatibility window:
// with no public key configured, unsigned manifests are accepted (checksum-only).
func TestVerifyManifestSignature_NoKeyAcceptsUnsigned(t *testing.T) {
	orig := releasePublicKeyHex
	releasePublicKeyHex = ""
	defer func() { releasePublicKeyHex = orig }()

	m := &manifest{Version: "0.0.99", Signature: ""}
	ok, err := verifyManifestSignature(m)
	if err != nil || !ok {
		t.Fatalf("unsigned manifest should pass when no key configured: ok=%v err=%v", ok, err)
	}
}
