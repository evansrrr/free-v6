package appupdate

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
)

// testKeys 生成一次性密钥对，返回可注入的公钥表与"签名字节"的函数。
func testKeys(t *testing.T) (map[string]ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return map[string]ed25519.PublicKey{"test": pub}, priv
}

const sampleManifest = `{"version":"0.5.7","download":"https://github.com/o/r/releases/download/v0.5.7/a.exe","sha256":"` +
	"8e51a4ad7ad6fb3041f4686bc79c5cd0c19553922fbc7346ceda3c7d11ca3e57" + `"}`

func TestVerifyAcceptsValidSignature(t *testing.T) {
	keySet, priv := testKeys(t)
	envelope, err := Sign([]byte(sampleManifest), priv, "test")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !strings.HasPrefix(string(envelope), "v1.test.") {
		t.Fatalf("envelope must carry the key id: %q", envelope)
	}
	if err := VerifyWith(keySet, []byte(sampleManifest), envelope); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
}

// 改一个字节就必须拒绝：这正是"镜像替换清单"的场景。
func TestVerifyRejectsTamperedManifest(t *testing.T) {
	keySet, priv := testKeys(t)
	envelope, err := Sign([]byte(sampleManifest), priv, "test")
	if err != nil {
		t.Fatal(err)
	}
	tampered := []byte(strings.Replace(sampleManifest, "0.5.7", "0.5.8", 1))
	if err := VerifyWith(keySet, tampered, envelope); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered manifest must fail with ErrBadSignature, got %v", err)
	}
}

func TestVerifyRejectsUnknownKey(t *testing.T) {
	keySet, priv := testKeys(t)
	envelope, err := Sign([]byte(sampleManifest), priv, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyWith(map[string]ed25519.PublicKey{"other": keySet["test"]}, []byte(sampleManifest), envelope); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("unknown key id must be rejected, got %v", err)
	}
}

func TestVerifyRejectsMalformedEnvelopes(t *testing.T) {
	keySet, priv := testKeys(t)
	valid, err := Sign([]byte(sampleManifest), priv, "test")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimSpace(string(valid))
	parts := strings.SplitN(body, ".", 3)

	cases := map[string]string{
		"empty":            "",
		"only whitespace":  "  \n\t ",
		"missing parts":    "v1.test",
		"wrong version":    "v2.test." + parts[2],
		"empty key id":     "v1.." + parts[2],
		"not base64":       "v1.test.!!!!",
		"truncated sig":    "v1.test." + parts[2][:len(parts[2])/2],
		"extra dot suffix": body + ".junk",
	}
	for name, envelope := range cases {
		if err := VerifyWith(keySet, []byte(sampleManifest), []byte(envelope)); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

// 空公钥表（仓库里还没放公钥）必须失败关闭，不能"没有公钥就放行"。
func TestVerifyFailsClosedWithoutKeys(t *testing.T) {
	_, priv := testKeys(t)
	envelope, err := Sign([]byte(sampleManifest), priv, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyWith(nil, []byte(sampleManifest), envelope); !errors.Is(err, ErrNoPublicKey) {
		t.Fatalf("empty key set must fail closed, got %v", err)
	}
	if err := VerifyWith(map[string]ed25519.PublicKey{}, []byte(sampleManifest), envelope); !errors.Is(err, ErrNoPublicKey) {
		t.Fatalf("empty key set must fail closed, got %v", err)
	}
}

// 省略 keyId 时用公钥指纹；两端（签名工具 / 客户端）必须算出同一个 id。
func TestSignUsesFingerprintWhenKeyIDOmitted(t *testing.T) {
	_, priv := testKeys(t)
	pub := priv.Public().(ed25519.PublicKey)
	envelope, err := Sign([]byte(sampleManifest), priv, "")
	if err != nil {
		t.Fatal(err)
	}
	id := strings.SplitN(strings.TrimSpace(string(envelope)), ".", 3)[1]
	if id != Fingerprint(pub) {
		t.Fatalf("derived id %q != fingerprint %q", id, Fingerprint(pub))
	}
	if err := VerifyWith(map[string]ed25519.PublicKey{id: pub}, []byte(sampleManifest), envelope); err != nil {
		t.Fatalf("fingerprint id must verify: %v", err)
	}
}

// PEM 往返：签名工具写出的公钥（带 Key-Id 头）必须能被 loadKeys 读回来，
// 否则"生成密钥 → 粘进仓库"这一步会静默失效。
func TestPublicKeyPEMRoundTrips(t *testing.T) {
	_, priv := testKeys(t)
	pub := priv.Public().(ed25519.PublicKey)
	pemBytes, err := MarshalPublicKeyPEM(pub, "2026-11")
	if err != nil {
		t.Fatal(err)
	}
	loaded := loadKeys(pemBytes)
	if len(loaded) != 2 {
		t.Fatalf("expected the key under both ids, got %d entries", len(loaded))
	}
	envelope, err := Sign([]byte(sampleManifest), priv, "2026-11")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyWith(loaded, []byte(sampleManifest), envelope); err != nil {
		t.Fatalf("round-tripped PEM must verify: %v", err)
	}
	// 只写指纹 id 也能验：客户端同时登记两套 id。
	byFingerprint, err := Sign([]byte(sampleManifest), priv, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyWith(loaded, []byte(sampleManifest), byFingerprint); err != nil {
		t.Fatalf("fingerprint id must verify against loaded keys: %v", err)
	}
}

// 没有任何 PEM 块（仓库初始状态）时不能 panic，必须得到空表。
func TestLoadKeysToleratesCommentOnlyFile(t *testing.T) {
	loaded := loadKeys([]byte("# 还没有配置公钥\n"))
	if len(loaded) != 0 {
		t.Fatalf("expected empty key set, got %d", len(loaded))
	}
}

func TestPrivateKeyPEMRoundTrips(t *testing.T) {
	_, priv := testKeys(t)
	pemBytes, err := MarshalPrivateKeyPEM(priv)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePrivateKey(pemBytes)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !parsed.Equal(priv) {
		t.Fatal("parsed private key differs")
	}
	if _, err := ParsePrivateKey([]byte("not a pem")); err == nil {
		t.Fatal("garbage must be rejected")
	}
}

func TestSignRejectsBadInput(t *testing.T) {
	_, priv := testKeys(t)
	if _, err := Sign(nil, priv, "test"); err == nil {
		t.Fatal("empty manifest must be rejected")
	}
	if _, err := Sign([]byte("{}"), nil, "test"); err == nil {
		t.Fatal("empty key must be rejected")
	}
	if _, err := Sign([]byte("{}"), priv, "bad.id"); err == nil {
		t.Fatal("key id containing a dot must be rejected")
	}
	if _, err := Sign([]byte("{}"), priv, "bad id"); err == nil {
		t.Fatal("key id containing a space must be rejected")
	}
}
