package network

import "strings"

// dnsResetCandidate 判断一条网卡注册表状态是否属于「自动获取 IP 却被写死了
// DNS」——旧版本固化的指纹：网卡仍在自动获取 IP（EnableDHCP=1），DHCP 也曾
// 经下发过 DNS（DhcpNameServer 非空），但当前生效的是静态配置（NameServer
// 非空）。三个条件缺一不可：静态 IP 的网卡、从没拿到过 DHCP DNS 的网卡都
// 不该被这个修复碰到。
func dnsResetCandidate(enableDHCP int, nameServer, dhcpNameServer string) bool {
	if enableDHCP != 1 {
		return false
	}
	if strings.TrimSpace(nameServer) == "" {
		return false
	}
	return strings.TrimSpace(dhcpNameServer) != ""
}
