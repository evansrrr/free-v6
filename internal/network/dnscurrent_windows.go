//go:build windows

package network

// currentDNSLookup 返回按接口别名查「此刻生效 DNS」的函数：一次
// GetAdaptersAddresses 建索引，供还原前逐个比对（接口数量少，一次枚举足够）。
//
// 枚举失败返回 nil —— 调用方据此放弃比对、按原计划还原，保持「停止时尽力
// 还原」的旧语义，不因为一次读不到现状就把还原整个跳过。
func currentDNSLookup() DNSCurrentLookup {
	adapters, err := getAdaptersAddresses()
	if err != nil {
		return nil
	}
	index := make(map[string]InterfaceSnapshot, 16)
	for _, interfaceSnapshot := range buildSnapshotWithDNSSource(adapters, nil).Interfaces {
		index[interfaceSnapshot.Alias] = interfaceSnapshot
	}
	return func(alias string, ipv6 bool) ([]string, bool) {
		interfaceSnapshot, ok := index[alias]
		if !ok {
			return nil, false
		}
		if ipv6 {
			return interfaceSnapshot.IPv6DNSServers, true
		}
		return interfaceSnapshot.IPv4DNSServers, true
	}
}
