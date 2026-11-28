package main

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yourname/freev6/internal/appupdate"
)

// end-to-end at the CLI layer: 生成密钥 → 签名 → 用同一把公钥回验。
// 注意这里用工具刚生成的公钥做验签，而不是内置公钥——内置公钥由发布流程维护，
// 测试不该依赖仓库里是否已经配置了生产密钥。
func TestGenerateSignVerifyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "signing-key.pem")
	pubPath := filepath.Join(dir, "public.pem")

	if err := generateKey(keyPath, pubPath, "2026-11"); err != nil {
		t.Fatalf("generateKey: %v", err)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("private key not written: %v", err)
	}
	// Windows 不支持 POSIX 权限位（OpenFile 的 0600 只映射到只读属性），
	// 私钥在本机靠用户目录 ACL 保护，这里只对类 Unix 平台断言。
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("private key must not be group/world readable: %v", info.Mode().Perm())
	}

	manifestPath := filepath.Join(dir, "version.json")
	manifest := []byte(`{"version":"0.5.7","download":"https://github.com/o/r/a.exe","sha256":"` + strings.Repeat("ab", 32) + `"}` + "\n")
	if err := os.WriteFile(manifestPath, manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	sigPath := filepath.Join(dir, "version.json.sig")
	if err := signManifest(manifestPath, sigPath, keyPath, "2026-11"); err != nil {
		t.Fatalf("signManifest: %v", err)
	}

	envelope, err := os.ReadFile(sigPath)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	// 只做一次"公钥 → 内存"的转换：generateKey 写出的 PEM 就是客户端要内置的那份。
	pub, err := appupdate.ParsePublicKeyPEM(pubPEM)
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}
	keySet := map[string]ed25519.PublicKey{"2026-11": pub}
	if err := appupdate.VerifyWith(keySet, manifest, envelope); err != nil {
		t.Fatalf("round-tripped signature rejected: %v", err)
	}

	// 清单改一个字节必须失效。
	bad := append([]byte(nil), manifest...)
	bad[10] ^= 0x01
	if err := appupdate.VerifyWith(keySet, bad, envelope); err == nil {
		t.Fatal("tampered manifest must not verify")
	}
}

// 生成密钥时拒绝覆盖已有私钥（误覆盖等于丢钥）。
func TestGenerateKeyRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "signing-key.pem")
	if err := generateKey(keyPath, "", ""); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	if err := generateKey(keyPath, "", ""); err == nil {
		t.Fatal("second generate must refuse to overwrite")
	}
}

// 参数校验：缺参数必须报错，而不是"用空清单签名"。
func TestRunRequiresArguments(t *testing.T) {
	cases := [][]string{
		{},                                 // 什么都不给
		{"-gen-key"},                       // 缺 -key-out
		{"-verify", "-in", "version.json"}, // 缺 -sig
		{"-in", "version.json"},            // 缺 -key-file
		{"-verify", "-in", "a", "-sig", "b", "extra"}, // 多余参数
	}
	for _, args := range cases {
		if err := run(args); err == nil {
			t.Errorf("expected error for args %v", args)
		}
	}
}
