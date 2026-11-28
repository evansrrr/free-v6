package appupdate

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
)

// envelopePrefix 是当前唯一的签名信封版本。换算法（例如换到 Ed25519ph 或
// 后量子方案）时新增前缀，客户端按前缀分派，旧清单继续可验。
const envelopePrefix = "v1"

// Verify 用内置公钥校验 version.json 的签名信封。
//
// 调用约定：先 Verify，再 json.Unmarshal。顺序反过来等于让未签名的字节
// 参与了任何解析逻辑，"先验签后解析"是这条链路的硬约束。
func Verify(manifest, envelope []byte) error {
	return VerifyWith(keys, manifest, envelope)
}

// EnvelopeKeyID 返回信封里的 keyId；格式不合法时返回空串。
// 只用于日志与发布自检——判定是否可信仍然只走 Verify。
func EnvelopeKeyID(envelope []byte) string {
	parts := strings.SplitN(strings.TrimSpace(string(envelope)), ".", 3)
	if len(parts) != 3 || parts[0] != envelopePrefix {
		return ""
	}
	return parts[1]
}

// VerifyWith 用给定公钥表验签。生产路径只用 Verify；测试与密钥轮换工具
// 通过它注入临时密钥，避免为了可测性把内置的那把公钥放宽。
func VerifyWith(keySet map[string]ed25519.PublicKey, manifest, envelope []byte) error {
	text := strings.TrimSpace(string(envelope))
	if text == "" {
		return ErrUnsigned
	}
	if len(keySet) == 0 {
		return ErrNoPublicKey
	}
	parts := strings.SplitN(text, ".", 3)
	if len(parts) != 3 || parts[0] != envelopePrefix || parts[1] == "" {
		return ErrFormat
	}
	pub, ok := keySet[parts[1]]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownKey, parts[1])
	}
	raw, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil || len(raw) != ed25519.SignatureSize {
		return ErrFormat
	}
	if !ed25519.Verify(pub, manifest, raw) {
		return ErrBadSignature
	}
	return nil
}
