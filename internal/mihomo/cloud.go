package mihomo

import (
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// 校园配置（cloud profile）：由校内服务端下发的出口节点集合。
//
// 与「链式代理」的差别是语义而不是格式，两者解析器共用同一套校验：
//   - 链式代理 = 本机 → WARP 接入点 → 自有代理（dialer-proxy 强制指向 MASQUE）
//   - 校园配置 = 本机 → 校园节点直出（不套娃，也不再需要 WARP 凭据）
//
// 之所以只取 proxies 而不整份覆盖：TUN 命名（ICS 热点共享按名识别网卡）、
// external-controller 的 per-install secret、校园网 DIRECT 绕过、geodata
// 预置、端口冲突预检全部由本地骨架负责，云端配置一旦覆盖这些字段就会把
// 上述机制砸掉。规则同理——路由归本地，云端只提供"从哪儿出去"。
const (
	// CloudMaxNodes 挡住误传整份 ACL4SSR 千节点配置：既限制内存，也让
	// 接入点列表不至于渲染成几千行。
	CloudMaxNodes = 200
	// cloudMaxNameLen 与 mihomo 的节点名实际可用长度留余量；过长的名字在
	// 分组列表与日志里都不可读。
	cloudMaxNameLen = 64
)

// CloudProfile 是解析后的校园配置。
type CloudProfile struct {
	Nodes []ChainProxy
	// Ignored 列出被忽略的顶层字段，用于在 UI 上如实告知"你的 tun/rules
	// 没有生效"，而不是默默丢弃。
	Ignored []string
}

// cloudSkeletonKeys 是需要与本地骨架区分开、必须忽略的顶层字段。只列已知
// 项而不是"白名单之外全忽略"，是为了让未知的自定义字段不被误报成冲突。
var cloudSkeletonKeys = map[string]bool{
	"port": true, "socks-port": true, "redir-port": true, "tproxy-port": true,
	"mixed-port": true, "allow-lan": true, "bind-address": true, "mode": true,
	"log-level": true, "ipv6": true, "unified-delay": true, "tcp-concurrent": true,
	"find-process-mode": true, "external-controller": true, "external-ui": true,
	"secret": true, "tun": true, "dns": true, "sniffer": true, "profile": true,
	"hosts": true, "experimental": true, "geox-url": true, "geodata-mode": true,
	"geodata-loader": true, "rule-providers": true, "proxy-providers": true,
	"proxy-groups": true, "rules": true, "sub-rules": true,
}

// ParseCloudProfile 解析云端配置：只要 proxies，其余骨架字段忽略并回报。
func ParseCloudProfile(content []byte) (CloudProfile, error) {
	var top map[string]any
	if err := yaml.Unmarshal(content, &top); err != nil {
		return CloudProfile{}, fmt.Errorf("解析 yaml 失败: %w", err)
	}
	var document struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(content, &document); err != nil {
		return CloudProfile{}, fmt.Errorf("解析 yaml 失败: %w", err)
	}
	if len(document.Proxies) == 0 {
		return CloudProfile{}, fmt.Errorf("配置中没有 proxies 列表（需要 Clash/mihomo 格式）")
	}
	if len(document.Proxies) > CloudMaxNodes {
		return CloudProfile{}, fmt.Errorf("proxies 有 %d 个节点，超过 %d 个上限", len(document.Proxies), CloudMaxNodes)
	}
	// dialer 传空串：校园节点是直出的，不再被强制挂到 MASQUE 接入点后面；
	// 配置里自带的 dialer-proxy 一律剥掉（它可能指向不存在的节点名，会让
	// mihomo 启动失败，或者把流量绕回一个用户没预期的出口）。
	nodes, err := normalizeProxyEntries(document.Proxies, reservedChainNames(), "")
	if err != nil {
		return CloudProfile{}, err
	}
	keys := make([]string, 0, len(top))
	for key := range top {
		if cloudSkeletonKeys[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return CloudProfile{Nodes: nodes, Ignored: keys}, nil
}
