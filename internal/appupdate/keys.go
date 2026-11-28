// Package appupdate 实现应用更新清单（version.json）的签名与验签。
//
// 信任模型：清单里的 sha256 与安装包下载地址来自同一个镜像族，镜像被攻破时
// 两者可以被同时替换——哈希因此不能单独当作完整性锚点。发布流水线用 Ed25519
// 私钥对 version.json 的**原始字节**签名，客户端只信任编译进来的公钥；验签
// 失败一律当作"没有更新"（失败关闭），绝不回退到未签名的清单。
//
// 信封格式：v1.<keyId>.<base64(64 字节签名)>，单行，尾部允许空白。
// keyId 可以是人工指定的（如 2026-11），也可以是公钥指纹（见 Fingerprint）：
// 客户端两套都收，换钥时追加公钥块即可，旧签名继续有效。
package appupdate

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"strings"
)

//go:embed update_public.pem
var embeddedPublicKeys []byte

var (
	// ErrUnsigned 表示清单没有附带签名资产。
	ErrUnsigned = errors.New("更新清单未签名")
	// ErrNoPublicKey 表示本构建没有内置任何公钥（发布配置缺失）。
	ErrNoPublicKey = errors.New("未内置更新公钥")
	// ErrUnknownKey 表示签名用的 keyId 不在内置公钥表里。
	ErrUnknownKey = errors.New("未知的签名密钥")
	// ErrBadSignature 表示签名与清单字节不匹配。
	ErrBadSignature = errors.New("签名校验失败")
	// ErrFormat 表示信封不是合法的 v1.<keyId>.<base64> 结构。
	ErrFormat = errors.New("签名格式非法")
)

// keys 是内置公钥表：keyId → 公钥。同一把钥匙同时按 PEM 头里的 Key-Id 和
// 公钥指纹登记，这样签名方写显式 id 或省略 id 都能验过。
var keys = loadKeys(embeddedPublicKeys)

func loadKeys(data []byte) map[string]ed25519.PublicKey {
	out := make(map[string]ed25519.PublicKey)
	for rest := data; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		pub, err := parsePublicKeyBlock(block)
		if err != nil {
			continue
		}
		if id := headerValue(block.Headers, "key-id"); id != "" {
			out[id] = pub
		}
		out[Fingerprint(pub)] = pub
	}
	return out
}

// ParsePublicKeyPEM 解析单块 PKIX 公钥 PEM（工具与 loadKeys 共用）。
func ParsePublicKeyPEM(data []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("公钥不是 PEM 格式")
	}
	return parsePublicKeyBlock(block)
}

func parsePublicKeyBlock(block *pem.Block) (ed25519.PublicKey, error) {
	if block.Type != "PUBLIC KEY" {
		return nil, errors.New("公钥 PEM 类型应为 PUBLIC KEY")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := parsed.(ed25519.PublicKey)
	if !ok || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("公钥不是 Ed25519")
	}
	return pub, nil
}

// headerValue 大小写不敏感地读 PEM 头（pem.Decode 保留原始大小写）。
func headerValue(headers map[string]string, want string) string {
	for key, value := range headers {
		if strings.EqualFold(strings.TrimSpace(key), want) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// Fingerprint 返回公钥指纹（SHA-256 前 8 字节的 hex，16 个字符）。
// 它被用作默认 keyId：不需要人工维护、也不会因为写错 id 让更新通道失效。
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// PublicKey 按 id 查内置公钥。
func PublicKey(keyID string) (ed25519.PublicKey, bool) {
	pub, ok := keys[keyID]
	return pub, ok
}

// KeyIDs 返回内置公钥的全部 id（排障与发布自检用；0 个表示本构建拒绝一切更新）。
func KeyIDs() []string {
	out := make([]string, 0, len(keys))
	for id := range keys {
		out = append(out, id)
	}
	return out
}
