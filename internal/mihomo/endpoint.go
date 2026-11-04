package mihomo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

// 备用配置入口的派生。
//
// 目的只有一个：不在 MIT 开源的仓库里出现可直接复制粘贴的服务端地址，
// 免得爬虫扫 GitHub、以及 fork 之后自动继承一个能用的入口。
//
// 这不是安全机制，也不该被当成安全机制。输入（校园网登录页地址）是公开
// 常量，甚至已经写在 README 里；规则又在客户端里，所以输出天然可复现。
// 真正的门禁在服务端：按请求源 IP 段判定（见 docs/cloud-profile.md）。
//
// 因为 Worker 对任意子域名都返回同一份配置、不校验 slug，"输错门户 IP"
// 不会导致取不到配置，同时也不存在 404/200 这种可枚举的判别口。
const (
	// CloudProfileDomainSuffix 是维护者自有域名（README 里已公开）。
	CloudProfileDomainSuffix = "freev6.cc.cd"
	// cloudProfilePath Worker 上的固定路径。
	cloudProfilePath = "/p"
	// cloudSlugSalt 让派生值与本仓库其他 hash 用途隔离（防止有人拿
	// sha256("门户IP") 直接猜出来）。
	cloudSlugSalt = "freev6:campus:v1:"
	// cloudSlugOffset/Length 是从 sha256 hex 里切的位置与长度。16 位 hex
	// 足够避免碰撞，同时也是合法的 DNS label。
	cloudSlugOffset = 7
	cloudSlugLength = 16
)

// NormalizePortalHost 把用户输入折叠成裸主机名：容忍粘贴整条 URL、
// 带端口、带路径、大小写、首尾空白。
func NormalizePortalHost(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "", fmt.Errorf("校园网登录页地址为空")
	}
	for _, prefix := range []string{"https://", "http://"} {
		value = strings.TrimPrefix(value, prefix)
	}
	if cut := strings.IndexAny(value, "/?#"); cut >= 0 {
		value = value[:cut]
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.TrimSuffix(value, ".")
	if value == "" {
		return "", fmt.Errorf("校园网登录页地址无法解析")
	}
	// IPv4 字面量与域名都接受；纯 IPv6 字面量不作为门户地址（不在本场景内）。
	if net.ParseIP(value) == nil && !validDomain(value) {
		return "", fmt.Errorf("不是合法的 IP 或域名: %q", strings.TrimSpace(raw))
	}
	return value, nil
}

// CloudSlug 由门户地址派生入口子域名。
func CloudSlug(portalHost string) (string, error) {
	host, err := NormalizePortalHost(portalHost)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(cloudSlugSalt + host))
	full := hex.EncodeToString(sum[:])
	return full[cloudSlugOffset : cloudSlugOffset+cloudSlugLength], nil
}

// CloudProfileURL 返回派生出的配置入口地址。
func CloudProfileURL(portalHost string) (string, error) {
	slug, err := CloudSlug(portalHost)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://%s.%s%s", slug, CloudProfileDomainSuffix, cloudProfilePath), nil
}
