package network

import (
	"net"
	"strings"
	"unicode"
)

// netsh interface <protocol> ... 用的协议名。
const (
	protocolIPv4 = "ipv4"
	protocolIPv6 = "ipv6"
)

// DNSResetChange 描述一次「交还给 DHCP」的改动。同一接口可能只改了 IPv4 或
// 只改了 IPv6，日志按协议区分。
type DNSResetChange struct {
	Alias string `json:"alias"`
	IPv4  bool   `json:"ipv4,omitempty"`
	IPv6  bool   `json:"ipv6,omitempty"`
}

// dnsResetCandidate 判断一条网卡注册表状态是否属于「旧版本固化下来的静态
// DNS」，也就是「这段静态配置是不是我们写的」—— 不是「有没有静态 DNS」。
//
// 旧版本停止免流时把当时生效的 DNS（对 DHCP 网卡就是 DHCP 下发的那些地址）
// 原样写成静态，所以指纹是「静态配置的地址集合 == DHCP 下发的地址集合」。
// 只判「存在静态配置」会连用户自己设的 DNS（1.1.1.1、公司内网 DNS）一起改掉，
// 那不是修 bug，是篡改用户配置。
//
// 两个值在注册表里格式不同（NameServer 用逗号分隔，DhcpNameServer 用空格
// 分隔），必须按地址集合比较，不能按字符串。
//
// 已知边界：IPv6 由 RA 下发的地址通常不会落到 DhcpNameServer（该值常为空），
// 这类被固化的 IPv6 DNS 无法与用户手写的静态值区分，不会命中，只能手动改回
// 「自动获取」。宁可不修，也不误改用户配置。
func dnsResetCandidate(enableDHCP int, nameServer, dhcpNameServer string) bool {
	if enableDHCP != 1 {
		return false
	}
	static := parseDNSServers(nameServer)
	dhcp := parseDNSServers(dhcpNameServer)
	if len(static) == 0 || len(static) != len(dhcp) {
		return false
	}
	for server := range static {
		if _, ok := dhcp[server]; !ok {
			return false
		}
	}
	return true
}

// parseDNSServers 把注册表里的 DNS 列表拆成归一化的地址集合：分隔符有逗号、
// 分号、空格三种，IPv6 还有压缩/展开等多种写法，统一交给 net.ParseIP 归一化。
// 解析不了的值原样小写保留 —— 宁可比较失败（不动手）也不要误判。
func parseDNSServers(raw string) map[string]struct{} {
	servers := make(map[string]struct{})
	for _, field := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || unicode.IsSpace(r)
	}) {
		if field == "" {
			continue
		}
		if ip := net.ParseIP(field); ip != nil {
			servers[ip.String()] = struct{}{}
			continue
		}
		servers[strings.ToLower(field)] = struct{}{}
	}
	return servers
}
