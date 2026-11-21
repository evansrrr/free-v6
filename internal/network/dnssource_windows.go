//go:build windows

package network

import (
	"errors"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// adapterDNSSource 读出某个网卡某个协议的 DNS 来源。
//
// GetAdaptersAddresses 只给「当前生效的地址」，不区分是 DHCP 下发的还是
// 用户在网卡属性里手填的，而这两者的还原方式相反：静态要按值写回，DHCP
// 必须交还给 DHCP。真正的来源在注册表里 —— NameServer 存静态配置（手填 /
// 组策略），DHCP 下发的地址只落在 DhcpNameServer，所以 NameServer 非空
// 即静态、空或不存在即 DHCP。
//
// 读不到注册表（键缺失、类型异常、权限）时返回 DNSSourceUnknown：调用方
// 据此放弃还原该协议，不猜。adapterGUID 为空（测试里的假适配器）同样视为
// 未知，这样纯函数式的快照构建不必碰真实注册表。
func adapterDNSSource(adapterGUID, protocol string) string {
	if adapterGUID == "" {
		return DNSSourceUnknown
	}
	key, err := openAdapterInterfaceKey(adapterGUID, protocol)
	if err != nil {
		return DNSSourceUnknown
	}
	defer func() { _ = key.Close() }()
	value, _, err := key.GetStringValue("NameServer")
	switch {
	case err == nil:
	case errors.Is(err, registry.ErrNotExist):
		return DNSSourceDHCP
	default:
		// ErrUnexpectedType 等：这个值不是我们认识的静态 DNS 列表，别动它。
		return DNSSourceUnknown
	}
	if strings.TrimSpace(value) != "" {
		return DNSSourceStatic
	}
	return DNSSourceDHCP
}

// openAdapterInterfaceKey 打开网卡在 Tcpip（IPv4）/ Tcpip6（IPv6）下的接口键，
// DNS 配置的静态值与 DHCP 下发值都在这里。
func openAdapterInterfaceKey(adapterGUID, protocol string) (registry.Key, error) {
	service := "Tcpip"
	if protocol == protocolIPv6 {
		service = "Tcpip6"
	}
	return registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Services\`+service+`\Parameters\Interfaces\`+adapterGUID,
		registry.QUERY_VALUE)
}
