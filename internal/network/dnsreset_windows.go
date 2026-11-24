//go:build windows

package network

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows"
)

// ResetDNSToDHCP 把「自动获取 IP 却被旧版本写死了静态 DNS」的网卡交还给
// DHCP，返回改动的接口（按协议记录）与失败详情。
//
// 这是默认防护的一部分，不是用户选项：旧版本每次「启动 → 停止」都会把当时
// 生效的 DNS（即 DHCP 下发的值）写成静态，值相同所以当场无感，换网络后静态
// 地址仍指向旧网段，表现是「Wi-Fi 已连接但打不开网页」，重启网卡也不恢复。
// 启动流程在抓基线之前调用它，把存量污染清掉，顺带让新基线记住 DHCP 来源。
//
// 判定见 dnsResetCandidate：只认「静态值 == DHCP 下发的值」。用户自己设的
// DNS 一定不相等，不会被改。
func ResetDNSToDHCP(ctx context.Context) (changed []DNSResetChange, failures []string) {
	adapters, err := getAdaptersAddresses()
	if err != nil {
		return nil, []string{err.Error()}
	}
	for adapter := adapters; adapter != nil; adapter = adapter.Next {
		alias := windows.UTF16PtrToString(adapter.FriendlyName)
		adapterGUID := windows.BytePtrToString(adapter.AdapterName)
		if alias == "" || adapterGUID == "" {
			continue
		}
		var change DNSResetChange
		change.Alias = alias
		for _, protocol := range []string{protocolIPv4, protocolIPv6} {
			if !adapterDNSFrozenByOldBug(adapterGUID, protocol) {
				continue
			}
			args := []string{"interface", protocol, "set", "dnsservers", "name=" + alias, "source=dhcp"}
			if output, runErr := exec.CommandContext(ctx, "netsh", args...).CombinedOutput(); runErr != nil {
				failures = append(failures, fmt.Sprintf("%s (%s): %v %s",
					alias, protocol, runErr, strings.TrimSpace(string(output))))
				continue
			}
			if protocol == protocolIPv6 {
				change.IPv6 = true
			} else {
				change.IPv4 = true
			}
		}
		if change.IPv4 || change.IPv6 {
			changed = append(changed, change)
		}
	}
	return changed, failures
}

// adapterDNSFrozenByOldBug 读取网卡的 DHCP/静态 DNS 注册表状态，交给
// dnsResetCandidate 判定（判定规则本身不依赖平台，可在任意系统上测试）。
func adapterDNSFrozenByOldBug(adapterGUID, protocol string) bool {
	key, err := openAdapterInterfaceKey(adapterGUID, protocol)
	if err != nil {
		return false
	}
	defer func() { _ = key.Close() }()
	enableDHCP, _, err := key.GetIntegerValue("EnableDHCP")
	if err != nil {
		return false
	}
	// 值缺失/类型异常时按空串处理：判定函数要求两者都非空，读不到就不会误伤。
	nameServer, _, _ := key.GetStringValue("NameServer")
	dhcpNameServer, _, _ := key.GetStringValue("DhcpNameServer")
	return dnsResetCandidate(int(enableDHCP), nameServer, dhcpNameServer)
}
