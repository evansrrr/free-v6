package network

import "testing"

// 判定的是「这段静态配置是不是旧版本写的」，不是「有没有静态 DNS」。
// 只有「静态值 == DHCP 下发的值」才是旧版本留下的指纹；用户自己设的 DNS
// 一定与 DHCP 下发值不同，不能被碰。
func TestDNSResetCandidate(t *testing.T) {
	cases := map[string]struct {
		enableDHCP     int
		nameServer     string
		dhcpNameServer string
		want           bool
	}{
		// 真实形态：名 Server 与 DhcpNameServer 分隔符不同，值集合相同。
		"frozen by the old bug":       {1, "202.204.52.89,202.204.52.94", "202.204.52.89 202.204.52.94", true},
		"single server":               {1, "202.204.52.89", "202.204.52.89", true},
		"order and spaces ignored":    {1, "202.204.52.94, 202.204.52.89", "202.204.52.89  202.204.52.94", true},
		"ipv6 compressed vs expanded": {1, "2001:db8::1", "2001:0db8:0000:0000:0000:0000:0000:0001", true},
		"user set their own dns":      {1, "1.1.1.1", "202.204.52.89 202.204.52.94", false},
		"user set one of two":         {1, "202.204.52.89", "202.204.52.89 202.204.52.94", false},
		"still dhcp":                  {1, "", "192.0.2.1", false},
		"static ip, static dns":       {0, "192.0.2.53", "192.0.2.53", false},
		"dhcp without dhcp dns":       {1, "192.0.2.53", "", false},
		"blank static":                {1, "   ", "192.0.2.1", false},
		"no dhcp key (zero value)":    {0, "192.0.2.53", "192.0.2.1", false},
		"unparseable values":          {1, "dns.example", "dns.example", true},
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

// 集合解析必须与分隔符、顺序、IPv6 写法无关：注册表里 NameServer 用逗号、
// DhcpNameServer 用空格，字符串比较永远不相等。
func TestParseDNSServers(t *testing.T) {
	got := parseDNSServers("202.204.52.89, 202.204.52.94;2001:0db8::1\t2001:db8::2")
	want := []string{"202.204.52.89", "202.204.52.94", "2001:db8::1", "2001:db8::2"}
	if len(got) != len(want) {
		t.Fatalf("parseDNSServers = %v, want %d entries", got, len(want))
	}
	for _, server := range want {
		if _, ok := got[server]; !ok {
			t.Fatalf("parseDNSServers missing %q: %v", server, got)
		}
	}
	if len(parseDNSServers("   ")) != 0 {
		t.Fatal("blank list must parse to an empty set")
	}
}
