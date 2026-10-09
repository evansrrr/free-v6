package mihomo

import (
	"fmt"
	"net"
	"strings"

	"github.com/yourname/freev6/internal/warp"
)

var endpoints = []struct {
	Server string
	Port   int
}{
	{"2606:4700:103::1", 443}, {"2606:4700:103::1", 500},
	{"2606:4700:103::1", 1701}, {"2606:4700:103::1", 4500},
	{"2606:4700:103::1", 4443}, {"2606:4700:103::1", 8443}, {"2606:4700:103::1", 8095},
	{"2606:4700:103::2", 443}, {"2606:4700:103::2", 500},
	{"2606:4700:103::2", 1701}, {"2606:4700:103::2", 4500},
	{"2606:4700:103::2", 4443}, {"2606:4700:103::2", 8443}, {"2606:4700:103::2", 8095},
	{"2606:4700:104::1", 443}, {"2606:4700:104::1", 500},
	{"2606:4700:104::1", 1701}, {"2606:4700:104::1", 4500},
	{"2606:4700:104::1", 4443}, {"2606:4700:104::1", 8443}, {"2606:4700:104::1", 8095},
	{"2606:4700:104::2", 443}, {"2606:4700:104::2", 500},
	{"2606:4700:104::2", 1701}, {"2606:4700:104::2", 4500},
	{"2606:4700:104::2", 4443}, {"2606:4700:104::2", 8443}, {"2606:4700:104::2", 8095},
}

var ruleSets = []struct {
	Group string
	URL   string
}{
	{"🚀 节点选择", "https://raw.githubusercontent.com/cmliu/ACL4SSR/refs/heads/main/Clash/CFnat.list"},
	{"🚀 节点选择", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/LocalAreaNetwork.list"},
	{"🚀 节点选择", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/UnBan.list"},
	{"🛑 全球拦截", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/BanAD.list"},
	{"🚀 节点选择", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/GoogleCN.list"},
	{"🚀 节点选择", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/Ruleset/SteamCN.list"},
	{"🚀 节点选择", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/ProxyLite.list"},
	{"🚀 节点选择", "https://raw.githubusercontent.com/cmliu/ACL4SSR/main/Clash/CMBlog.list"},
	{"🚀 节点选择", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/ChinaDomain.list"},
	{"🚀 节点选择", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/ChinaCompanyIp.list"},
}

const (
	ModeRule   = "rule"
	ModeGlobal = "global"
	// ModeDirect = mihomo "Global direct connection": every connection goes
	// DIRECT, no rules matched. Developer-mode-only option in the GUI.
	ModeDirect = "direct"
)

type RenderOptions struct {
	Mode        string
	CampusCIDRs []string
	Blacklist   []string
	DevMode     bool
}

const configHeader = `# Cloudflare WARP over MASQUE - mihomo config
# needs mihomo Alpha
#
# contains 28 nodes
# take care of your private-key

mixed-port: 7890
allow-lan: false
log-level: error
external-controller: 127.0.0.1:9090
ipv6: true
unified-delay: true
tcp-concurrent: true
find-process-mode: 'always'

# 地理数据下载地址走大陆可达镜像（与 internal/mihomo/geodata.go 的
# EnsureGeodata 镜像链一致）；正常情况下启动前文件已预置，这里只在
# 文件被删除/geo-auto-update 开启时生效。
geox-url:
  geoip: "https://gh-proxy.com/https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.dat"
  geosite: "https://gh-proxy.com/https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geosite.dat"
  mmdb: "https://gh-proxy.com/https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/country.mmdb"

profile:
  store-selected: true
  store-fake-ip: true

sniffer:
  enable: true
  sniff:
    HTTP:
      ports: [80, 8080-8880]
      override-destination: true
    TLS:
      ports: [443, 8443]
    QUIC:
      ports: [443, 8443]
  skip-domain:
    - '+.push.apple.com'
    - '+.apple.com'

dns:
  enable: true
  listen: 0.0.0.0:1053
  ipv6: true
  enhanced-mode: fake-ip
  fake-ip-range: 198.18.0.1/16
  fake-ip-filter:
    - '+.lan'
    - '+.local'
    - '*.msftconnecttest.com'
    - '*.msftncsi.com'
  default-nameserver:
    - 223.5.5.5
    - 119.29.29.29
    - 2606:4700:4700::1111
    - 2606:4700:4700::1001
    - 2400:3200::1
    - 2400:3200:baba::1
  nameserver:
    - https://223.5.5.5/dns-query
    - https://1.12.12.12/dns-query
    - 2606:4700:4700::1111
    - 2606:4700:4700::1001
    - 2400:3200::1
    - 2400:3200:baba::1
  proxy-server-nameserver:
    - https://223.5.5.5/dns-query
    - 2606:4700:4700::1111
    - 2400:3200::1
  nameserver-policy:
    'geosite:cn,private':
      - https://223.5.5.5/dns-query
      - https://1.12.12.12/dns-query
      - 2400:3200::1
      - 2400:3200:baba::1
    'geosite:geolocation-!cn':
      - https://1.1.1.1/dns-query
      - https://8.8.8.8/dns-query
      - 2606:4700:4700::1111
      - 2606:4700:4700::1001

`

func Render(w warp.Device) (string, error) {
	return RenderWithOptions(w, RenderOptions{Mode: ModeRule})
}

func RenderWithOptions(w warp.Device, options RenderOptions) (string, error) {
	for name, value := range map[string]string{"private key": w.PrivateKey, "peer public key": w.PeerPublicKey, "IPv4": w.IPv4, "IPv6": w.IPv6} {
		if strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("missing %s", name)
		}
	}
	if options.Mode == "" {
		options.Mode = ModeRule
	}
	if options.Mode != ModeRule && options.Mode != ModeGlobal && options.Mode != ModeDirect {
		return "", fmt.Errorf("unsupported mihomo mode %q", options.Mode)
	}
	campusTargets, err := normalizeCampusTargets(options.CampusCIDRs)
	if err != nil {
		return "", err
	}
	blacklist, err := normalizeBlacklist(options.Blacklist)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(configHeader)
	// device: 固定 TUN 适配器显示名，供 ICS 热点共享（internal/network/ics）
	// 按名识别 FreeV6 的虚拟网卡，不依赖 mihomo 默认名。
	fmt.Fprintf(&b, "mode: %s\n\ntun:\n  enable: true\n  device: FreeV6TUN\n  stack: mixed\n  auto-route: true\n  auto-detect-interface: true\n  strict-route: true\n  dns-hijack:\n    - any:53\n    - tcp://any:53\n\nproxies:\n", options.Mode)
	var names []string
	for _, endpoint := range endpoints {
		name := endpointName(endpoint.Server, endpoint.Port)
		names = append(names, name)
		fmt.Fprintf(&b, "  - name: %s\n    type: masque\n    server: \"%s\"\n    port: %d\n    private-key: %s\n    public-key: %s\n    ip: %s\n    ipv6: %s\n    mtu: 1280\n    udp: true\n    remote-dns-resolve: true\n    dns: [1.1.1.1, 2606:4700:4700::1111]\n", name, endpoint.Server, endpoint.Port, w.PrivateKey, w.PeerPublicKey, w.IPv4, w.IPv6)
	}

	b.WriteString("\nproxy-groups:\n")
	b.WriteString("  - name: 🚀 节点选择\n    type: select\n    proxies:\n      - ♻️ 自动选择\n      - 🔄 故障转移\n      - ☑️ 手动切换\n\n")
	b.WriteString("  - name: GLOBAL\n    type: select\n    proxies:\n      - ♻️ 自动选择\n      - 🔄 故障转移\n      - ☑️ 手动切换\n\n")
	writeProxyGroup(&b, "☑️ 手动切换", "select", "", names)
	writeProxyGroup(&b, "♻️ 自动选择", "url-test", "    url: http://www.gstatic.com/generate_204\n    interval: 300\n    tolerance: 50\n    lazy: false\n", names)
	writeProxyGroup(&b, "🔄 故障转移", "fallback", "    url: http://www.gstatic.com/generate_204\n    interval: 180\n", names)
	b.WriteString("  - name: 🛑 全球拦截\n    type: select\n    proxies:\n      - REJECT\n\n")
	b.WriteString("  - name: 🐟 漏网之鱼\n    type: select\n    proxies:\n      - 🚀 节点选择\n      - ♻️ 自动选择\n\n")

	b.WriteString("rule-providers:\n")
	for i, ruleSet := range ruleSets {
		fmt.Fprintf(&b, "  rule%02d:\n    type: http\n    behavior: classical\n    format: text\n    interval: 86400\n    url: %s\n    path: ./ruleset/rule%02d.list\n", i, ruleSet.URL, i)
	}
	b.WriteString("\nrules:\n")
	if !options.DevMode {
		// Blacklist wins: block external domains before any bypass/proxy rules
		for _, domain := range blacklist {
			fmt.Fprintf(&b, "  - DOMAIN-SUFFIX,%s,REJECT\n", domain)
		}
	}
	for _, target := range campusTargets {
		if !strings.Contains(target, "/") {
			// Domain entry: bypass WARP for the domain and all of its subdomains
			fmt.Fprintf(&b, "  - DOMAIN-SUFFIX,%s,DIRECT\n", target)
			continue
		}
		kind := "IP-CIDR"
		if strings.Contains(target, ":") {
			kind = "IP-CIDR6"
		}
		fmt.Fprintf(&b, "  - %s,%s,DIRECT,no-resolve\n", kind, target)
	}
	for i, ruleSet := range ruleSets {
		fmt.Fprintf(&b, "  - RULE-SET,rule%02d,%s\n", i, ruleSet.Group)
	}
	b.WriteString("  - GEOIP,LAN,DIRECT,no-resolve\n  - MATCH,🚀 节点选择\n")
	return b.String(), nil
}

// ValidateCampusTarget accepts either an IP CIDR (10.0.0.0/8, 2001:db8::/48)
// or a bare domain (pku.edu.cn) whose subdomains are handled together with it.
func ValidateCampusTarget(value string) error {
	target := strings.TrimSpace(strings.ToLower(value))
	if target == "" {
		return fmt.Errorf("empty campus entry")
	}
	if strings.Contains(target, "/") {
		if _, _, err := net.ParseCIDR(target); err != nil {
			return fmt.Errorf("invalid campus CIDR %q: %w", target, err)
		}
		return nil
	}
	if !validDomain(target) {
		return fmt.Errorf("invalid campus domain %q", target)
	}
	return nil
}

func validDomain(domain string) bool {
	if len(domain) > 253 || !strings.Contains(domain, ".") ||
		strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			char := label[i]
			if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
				continue
			}
			if char == '-' && i > 0 && i < len(label)-1 {
				continue
			}
			return false
		}
	}
	return true
}

func normalizeCampusTargets(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		target := strings.TrimSpace(strings.ToLower(value))
		if target == "" {
			continue
		}
		if err := ValidateCampusTarget(target); err != nil {
			return nil, err
		}
		if seen[target] {
			continue
		}
		seen[target] = true
		result = append(result, target)
	}
	return result, nil
}

// ValidateDomain reports whether value is a bare domain such as pku.edu.cn.
// Blacklist entries are domain-only: each blocks the domain and its subdomains.
func ValidateDomain(value string) error {
	target := strings.TrimSpace(strings.ToLower(value))
	if target == "" {
		return fmt.Errorf("empty domain")
	}
	if !validDomain(target) {
		return fmt.Errorf("invalid domain %q", target)
	}
	return nil
}

func normalizeBlacklist(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		target := strings.TrimSpace(strings.ToLower(value))
		if target == "" {
			continue
		}
		if err := ValidateDomain(target); err != nil {
			return nil, err
		}
		if seen[target] {
			continue
		}
		seen[target] = true
		result = append(result, target)
	}
	return result, nil
}

func writeProxyGroup(b *strings.Builder, name, groupType, options string, names []string) {
	fmt.Fprintf(b, "  - name: %s\n    type: %s\n", name, groupType)
	b.WriteString(options)
	b.WriteString("    proxies:\n")
	for _, proxyName := range names {
		fmt.Fprintf(b, "      - %s\n", proxyName)
	}
	b.WriteString("\n")
}

func endpointName(server string, port int) string {
	parts := strings.Split(server, ":")
	segment := parts[2]
	tail := parts[len(parts)-1]
	return fmt.Sprintf("%s-%s-%d", segment, tail, port)
}
