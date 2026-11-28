// Command freev6-update-sign 生成、使用更新清单签名密钥。
//
// 三种用法：
//
//	# 1) 一次性生成密钥对（私钥只落本地文件，公钥打印到 stdout）
//	freev6-update-sign -gen-key -key-id 2026-11 -key-out update-signing-key.pem
//
//	# 2) 签名（发布流水线里用；私钥来自 GitHub Secret 写出的临时文件）
//	freev6-update-sign -in version.json -out version.json.sig \
//	    -key-file update-signing-key.pem -key-id 2026-11
//
//	# 3) 回验（上传后自检 / 本机排障）
//	freev6-update-sign -verify -in version.json -sig version.json.sig
//
// 私钥只从 -key-file 读，不从环境变量或参数读：参数会进进程列表，
// 环境变量会被子进程继承。
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yourname/freev6/internal/appupdate"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "freev6-update-sign: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("freev6-update-sign", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)

	in := flags.String("in", "", "要签名/校验的清单文件（version.json）")
	out := flags.String("out", "version.json.sig", "签名输出路径")
	sig := flags.String("sig", "", "校验模式下的签名文件路径")
	keyFile := flags.String("key-file", "", "Ed25519 私钥 PEM 路径")
	keyID := flags.String("key-id", "", "签名信封里的 keyId（省略则用公钥指纹）")
	verify := flags.Bool("verify", false, "只校验 -in 与 -sig，不签名")
	genKey := flags.Bool("gen-key", false, "生成一对新密钥")
	keyOut := flags.String("key-out", "", "生成密钥时私钥的输出路径（必须不存在）")
	pubOut := flags.String("pub-out", "", "生成密钥时公钥 PEM 的输出路径（省略则打印到 stdout）")

	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("多余的参数: %v", flags.Args())
	}

	switch {
	case *genKey:
		if strings.TrimSpace(*keyOut) == "" {
			return errors.New("-gen-key 需要 -key-out 指定私钥输出路径")
		}
		return generateKey(*keyOut, *pubOut, *keyID)
	case *verify:
		if strings.TrimSpace(*in) == "" || strings.TrimSpace(*sig) == "" {
			return errors.New("-verify 需要 -in 与 -sig")
		}
		return verifySignature(*in, *sig)
	default:
		if strings.TrimSpace(*in) == "" || strings.TrimSpace(*keyFile) == "" {
			return errors.New("签名需要 -in 与 -key-file（生成密钥用 -gen-key）")
		}
		return signManifest(*in, *out, *keyFile, *keyID)
	}
}

func generateKey(keyPath, pubPath, keyID string) error {
	pub, priv, err := appupdate.GenerateKey()
	if err != nil {
		return err
	}
	privatePEM, err := appupdate.MarshalPrivateKeyPEM(priv)
	if err != nil {
		return err
	}
	// O_EXCL：绝不覆盖已存在的私钥文件（误覆盖等于丢钥）。
	file, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s 已存在，拒绝覆盖（请先确认它不是唯一的备份）", keyPath)
		}
		return fmt.Errorf("写入私钥: %w", err)
	}
	if _, err := file.Write(privatePEM); err != nil {
		_ = file.Close()
		return fmt.Errorf("写入私钥: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}

	publicPEM, err := appupdate.MarshalPublicKeyPEM(pub, keyID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(pubPath) != "" {
		if err := writeFileAtomic(pubPath, publicPEM, 0o644); err != nil {
			return fmt.Errorf("写入公钥: %w", err)
		}
	} else {
		// stdout 只放公钥 PEM：可以直接追加进 update_public.pem。
		fmt.Print(string(publicPEM))
	}
	id := strings.TrimSpace(keyID)
	if id == "" {
		id = appupdate.Fingerprint(pub)
	}
	fmt.Fprintf(os.Stderr, "已生成密钥对：keyId=%s fingerprint=%s 私钥=%s\n", id, appupdate.Fingerprint(pub), keyPath)
	fmt.Fprintln(os.Stderr, "下一步：私钥内容存进 GitHub Secret FREEV6_UPDATE_SIGNING_KEY 并做离线备份，然后删除本地私钥文件。")
	return nil
}

func signManifest(inPath, outPath, keyPath, keyID string) error {
	manifest, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("读取清单: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("读取私钥: %w", err)
	}
	priv, err := appupdate.ParsePrivateKey(keyPEM)
	if err != nil {
		return err
	}
	envelope, err := appupdate.Sign(manifest, priv, keyID)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(outPath, envelope, 0o644); err != nil {
		return fmt.Errorf("写入签名: %w", err)
	}
	fmt.Fprintf(os.Stderr, "已签名 %s → %s（keyId=%s，%d 字节）\n",
		inPath, outPath, appupdate.EnvelopeKeyID(envelope), len(strings.TrimSpace(string(envelope))))
	return nil
}

func verifySignature(inPath, sigPath string) error {
	manifest, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("读取清单: %w", err)
	}
	envelope, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("读取签名: %w", err)
	}
	if err := appupdate.Verify(manifest, envelope); err != nil {
		return fmt.Errorf("验签失败: %w", err)
	}
	fmt.Printf("signature OK (key id %s, %d bytes manifest)\n", appupdate.EnvelopeKeyID(envelope), len(manifest))
	return nil
}

// writeFileAtomic 先写临时文件再 rename：读者只会看到旧版或新版，
// 半截签名文件永远不会被上传。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Chmod(perm); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}
