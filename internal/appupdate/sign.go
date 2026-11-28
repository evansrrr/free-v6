package appupdate

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// Sign 对清单的**原始字节**签名，返回 "v1.<keyId>.<base64>" 信封。
//
// 签名对象必须是即将上传的那份字节：任何行尾/编码归一（另存为、CRLF 转换、
// 文本模式传输）都会让客户端验签失败。keyID 为空时用公钥指纹。
func Sign(manifest []byte, privateKey ed25519.PrivateKey, keyID string) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("私钥长度非法")
	}
	if len(manifest) == 0 {
		return nil, errors.New("清单为空")
	}
	pub, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("私钥类型非法")
	}
	id := strings.TrimSpace(keyID)
	if id == "" {
		id = Fingerprint(pub)
	}
	// 点号是信封分隔符，空白会在传输/粘贴中被吞掉：两者都不允许出现在 id 里。
	if strings.ContainsAny(id, ".\r\n \t") {
		return nil, fmt.Errorf("keyId 不能包含点号或空白: %q", id)
	}
	signature := ed25519.Sign(privateKey, manifest)
	envelope := envelopePrefix + "." + id + "." + base64.StdEncoding.EncodeToString(signature) + "\n"
	return []byte(envelope), nil
}

// GenerateKey 生成一对 Ed25519 密钥（仅签名工具使用）。
func GenerateKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// ParsePrivateKey 解析 PKCS#8 的 Ed25519 私钥 PEM（"PRIVATE KEY"）。
// OpenSSL 3 / Go 的 x509.MarshalPKCS8PrivateKey 都是这个形状；
// PKCS#1 不适用于 Ed25519，故意不支持。
func ParsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("私钥不是 PEM 格式")
	}
	if block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("私钥 PEM 类型应为 PRIVATE KEY，实际为 %q", block.Type)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析 PKCS#8 私钥: %w", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("私钥不是 Ed25519")
	}
	return key, nil
}

// MarshalPrivateKeyPEM 把私钥序列化为 PKCS#8 PEM（只在生成密钥时用）。
func MarshalPrivateKeyPEM(key ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// MarshalPublicKeyPEM 输出带 Key-Id 头的公钥 PEM，直接追加到
// internal/appupdate/update_public.pem 即可被客户端识别。
func MarshalPublicKeyPEM(pub ed25519.PublicKey, keyID string) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	block := &pem.Block{Type: "PUBLIC KEY", Bytes: der}
	if id := strings.TrimSpace(keyID); id != "" {
		block.Headers = map[string]string{"Key-Id": id}
	}
	return pem.EncodeToMemory(block), nil
}
