//go:build windows

package network

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows"
)

// ResetDNSToDHCP 把「自动获取 IP 却被写死了静态 DNS」的网卡交还给 DHCP，返回
// 改动的接口名与失败详情。用来迁移被旧版本固化过 DNS 的机器：一次正常的
// 启动→停止就把 DHCP 下发的地址写成了静态，当下无感，换网络后静态地址仍指向
// 旧网段，表现是「Wi-Fi 已连接但打不开网页」，重启网卡也不恢复。
//
// 只动 dnsResetCandidate 认定的网卡（见 dnsreset.go），且必须由用户显式触发：
// 它确实会清掉「DHCP 网卡上手动设置的 DNS」，所以不在启动流程里自动跑。
func ResetDNSToDHCP(ctx context.Context) (changed []string, failures []string) {
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
		reset := false
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
			reset = true
		}
		if reset {
			changed = append(changed, alias)
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
