package mihomo

import (
	"fmt"
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
	{"🎯 全球直连", "https://raw.githubusercontent.com/cmliu/ACL4SSR/refs/heads/main/Clash/CFnat.list"},
	{"🎯 全球直连", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/LocalAreaNetwork.list"},
	{"🎯 全球直连", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/UnBan.list"},
	{"🛑 全球拦截", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/BanAD.list"},
	{"🎯 全球直连", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/GoogleCN.list"},
	{"🎯 全球直连", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/Ruleset/SteamCN.list"},
	{"🚀 节点选择", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/ProxyLite.list"},
	{"🚀 节点选择", "https://raw.githubusercontent.com/cmliu/ACL4SSR/main/Clash/CMBlog.list"},
	{"🎯 全球直连", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/ChinaDomain.list"},
	{"🎯 全球直连", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/ChinaCompanyIp.list"},
}

const configHeader = `# Cloudflare WARP over MASQUE - mihomo config
# needs mihomo Alpha
#
# contains 28 nodes
# take care of your private-key

mixed-port: 7890
allow-lan: false
mode: rule
log-level: error
ipv6: true
unified-delay: true
tcp-concurrent: true
find-process-mode: 'always'

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
  nameserver:
    - https://223.5.5.5/dns-query
    - https://1.12.12.12/dns-query
  proxy-server-nameserver:
    - https://223.5.5.5/dns-query
  nameserver-policy:
    'geosite:cn,private':
      - https://223.5.5.5/dns-query
      - https://1.12.12.12/dns-query
    'geosite:geolocation-!cn':
      - https://1.1.1.1/dns-query
      - https://8.8.8.8/dns-query

proxies:
`

func Render(w warp.Device) (string, error) {
	for name, value := range map[string]string{"private key": w.PrivateKey, "peer public key": w.PeerPublicKey, "IPv4": w.IPv4, "IPv6": w.IPv6} {
		if strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("missing %s", name)
		}
	}

	var b strings.Builder
	b.WriteString(configHeader)
	var names []string
	for _, endpoint := range endpoints {
		name := endpointName(endpoint.Server, endpoint.Port)
		names = append(names, name)
		fmt.Fprintf(&b, "  - name: %s\n    type: masque\n    server: \"%s\"\n    port: %d\n    private-key: %s\n    public-key: %s\n    ip: %s\n    ipv6: %s\n    mtu: 1280\n    udp: true\n    remote-dns-resolve: true\n    dns: [1.1.1.1, 2606:4700:4700::1111]\n", name, endpoint.Server, endpoint.Port, w.PrivateKey, w.PeerPublicKey, w.IPv4, w.IPv6)
	}

	b.WriteString("\nproxy-groups:\n")
	b.WriteString("  - name: 🚀 节点选择\n    type: select\n    proxies:\n      - ♻️ 自动选择\n      - 🔄 故障转移\n      - ☑️ 手动切换\n      - DIRECT\n\n")
	writeProxyGroup(&b, "☑️ 手动切换", "select", "", names)
	writeProxyGroup(&b, "♻️ 自动选择", "url-test", "    url: http://www.gstatic.com/generate_204\n    interval: 300\n    tolerance: 50\n    lazy: false\n", names)
	writeProxyGroup(&b, "🔄 故障转移", "fallback", "    url: http://www.gstatic.com/generate_204\n    interval: 180\n", names)
	b.WriteString("  - name: 🎯 全球直连\n    type: select\n    proxies:\n      - DIRECT\n      - 🚀 节点选择\n      - ♻️ 自动选择\n\n")
	b.WriteString("  - name: 🛑 全球拦截\n    type: select\n    proxies:\n      - REJECT\n      - DIRECT\n\n")
	b.WriteString("  - name: 🐟 漏网之鱼\n    type: select\n    proxies:\n      - 🚀 节点选择\n      - 🎯 全球直连\n      - ♻️ 自动选择\n\n")

	b.WriteString("rule-providers:\n")
	for i, ruleSet := range ruleSets {
		fmt.Fprintf(&b, "  rule%02d:\n    type: http\n    behavior: classical\n    format: text\n    interval: 86400\n    url: %s\n    path: ./ruleset/rule%02d.list\n", i, ruleSet.URL, i)
	}
	b.WriteString("\nrules:\n")
	for i, ruleSet := range ruleSets {
		fmt.Fprintf(&b, "  - RULE-SET,rule%02d,%s\n", i, ruleSet.Group)
	}
	b.WriteString("  - GEOIP,LAN,🎯 全球直连,no-resolve\n  - GEOIP,CN,🎯 全球直连\n  - MATCH,🐟 漏网之鱼\n")
	return b.String(), nil
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
	return fmt.Sprintf("WARP6-%s-%s-%d", segment, tail, port)
}
