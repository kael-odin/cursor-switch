package updater

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// releasePublicKeyHex 是用于校验 update.json manifest 签名的 ed25519 公钥（hex）。
// var 而非 const：便于测试注入，也便于未来在启动时从配置加载。
// 当该值为空时，更新器降级为仅校验 SHA256 checksum（保持与历史无签名 release 兼容），
// 并在日志中警告。当维护者生成签名密钥对并填入此值后，带签名的 manifest 会被严格校验，
// 任何篡改（即使 release token 泄露）都会因签名失败而被拒。
//
// 生成密钥对：go run ./scripts/release keypair（私钥本地保管，不入库）。
var releasePublicKeyHex = ""

// verifyManifestSignature 校验 manifest 的 ed25519 签名。
// 签名内容为 manifest 除 signature 外字段的 canonical JSON（字段按结构体顺序，无多余空白）。
// 返回：
//   - 公钥未配置且无签名：true（降级放行，仅 checksum 校验）
//   - 公钥已配置但签名缺失/无效：false
//   - 签名有效：true
func verifyManifestSignature(data *manifest) (bool, error) {
	sig := strings.TrimSpace(data.Signature)
	if releasePublicKeyHex == "" {
		// 公钥未配置：未签名 release 视为可接受（兼容期），仅依赖 checksum。
		return true, nil
	}
	if sig == "" {
		return false, errors.New("manifest missing required signature")
	}
	pubKeyBytes, err := hex.DecodeString(releasePublicKeyHex)
	if err != nil || len(pubKeyBytes) != ed25519.PublicKeySize {
		return false, errors.New("invalid release public key configured")
	}
	sigBytes, err := hex.DecodeString(sig)
	if err != nil {
		return false, errors.New("invalid manifest signature encoding")
	}
	signed, err := canonicalManifestBytes(data)
	if err != nil {
		return false, err
	}
	if !ed25519.Verify(ed25519.PublicKey(pubKeyBytes), signed, sigBytes) {
		return false, errors.New("manifest signature verification failed")
	}
	return true, nil
}

// canonicalManifestBytes 序列化 manifest 除 signature 外的字段，作为签名输入。
func canonicalManifestBytes(data *manifest) ([]byte, error) {
	clone := *data
	clone.Signature = ""
	raw, err := json.Marshal(clone)
	if err != nil {
		return nil, err
	}
	return raw, nil
}
