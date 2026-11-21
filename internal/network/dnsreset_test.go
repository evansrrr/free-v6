package network

import "testing"

// 只有「自动获取 IP + DHCP 曾下发过 DNS + 当前是静态 DNS」三条同时成立才算
// 旧版本固化的候选：少任何一条都可能误伤静态 IP 的网卡或用户故意设置的 DNS。
func TestDNSResetCandidate(t *testing.T) {
	cases := map[string]struct {
		enableDHCP     int
		nameServer     string
		dhcpNameServer string
		want           bool
	}{
		"frozen by the old bug":    {1, "202.204.52.89,202.204.52.94", "202.204.52.89 202.204.52.94", true},
		"still dhcp":               {1, "", "192.0.2.1", false},
		"static ip, static dns":    {0, "192.0.2.53", "192.0.2.53", false},
		"dhcp without dhcp dns":    {1, "192.0.2.53", "", false},
		"blank static":             {1, "   ", "192.0.2.1", false},
		"no dhcp key (zero value)": {0, "192.0.2.53", "192.0.2.1", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := dnsResetCandidate(c.enableDHCP, c.nameServer, c.dhcpNameServer); got != c.want {
				t.Fatalf("dnsResetCandidate(%d, %q, %q) = %v, want %v",
					c.enableDHCP, c.nameServer, c.dhcpNameServer, got, c.want)
			}
		})
	}
}
