package mihomo

import (
	"strings"
	"testing"
	"time"

	"github.com/yourname/freev6/internal/warp"
)

func TestRenderMasqueConfig(t *testing.T) {
	config, err := Render(warp.Device{PrivateKey: "private", PeerPublicKey: "peer", IPv4: "172.16.0.2", IPv6: "2606:4700::2", RegisteredAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"type: masque", "server: \"2606:4700:103::1\"", "private-key: private", "public-key: peer",
		"log-level: error", "mode: rule", "tun:", "enable: true",
		// 固定设备名：ICS 热点共享按 FreeV6TUN 识别 TUN 适配器
		"device: FreeV6TUN",
		"♻️ 自动选择", "🔄 故障转移", "rule-providers:", "RULE-SET,rule00,🛑 全球拦截", "rules:",
		"2606:4700:4700::1111", "2606:4700:4700::1001", "2400:3200::1", "2400:3200:baba::1",
	} {
		if !strings.Contains(config, expected) {
			t.Errorf("config missing %q", expected)
		}
	}
	if count := strings.Count(config, "    type: masque"); count != 28 {
		t.Fatalf("expected 28 MASQUE nodes, got %d", count)
	}
	if strings.Contains(config, "server: 162.") {
		t.Fatal("IPv4 access points must not be included")
	}
	// 只留真正改变行为的规则集（广告拦截）；其余列表的目标分组与 MATCH 同路，
	// 留着只会多下载 9 份列表、常驻十几万条规则。
	if count := strings.Count(config, "    type: http"); count != 1 {
		t.Fatalf("expected a single HTTP rule provider, got %d", count)
	}
	if strings.Contains(config, "find-process-mode") {
		t.Fatal("no PROCESS-* rules are rendered, so process matching must stay off")
	}
	if !strings.Contains(config, "external-controller: 127.0.0.1:9090") {
		t.Fatal("renderer must expose only the loopback mihomo controller")
	}
	// 渲染产物不能再依赖地理数据：geosite/geoip 每条都是几 MB，而它们只
	// 支撑一条私网直连规则和一段 DNS 分流。
	for _, forbidden := range []string{"geox-url:", "geosite:", "GeoIP", "GEOIP"} {
		if strings.Contains(config, forbidden) {
			t.Fatalf("config must not reference geodata anymore, found %q", forbidden)
		}
	}
	// 私网直连改成显式地址段
	for _, cidr := range lanCIDRs {
		if want := "IP-CIDR," + cidr + ",DIRECT,no-resolve"; !strings.Contains(config, want) {
			t.Fatalf("config missing private-network direct rule %q", want)
		}
	}
	for _, cidr := range lanCIDRs6 {
		if want := "IP-CIDR6," + cidr + ",DIRECT,no-resolve"; !strings.Contains(config, want) {
			t.Fatalf("config missing private-network direct rule %q", want)
		}
	}
	// fake-ip 段绝不能被当成内网直连
	if strings.Contains(config, "198.18.0.0/15") {
		t.Fatal("fake-ip range must not be routed DIRECT")
	}
	// 只有 GitHub / gh-proxy 家族走境外 DoH，其余交给默认 nameserver
	if !strings.Contains(config, "'+.github.com,+.githubusercontent.com") {
		t.Fatal("config must resolve GitHub hosts through a dedicated DoH policy")
	}
	if strings.Contains(config, "GEOIP,CN") || strings.Contains(config, "🎯 全球直连") {
		t.Fatal("external traffic must not have a direct route")
	}
	if got := strings.Count(config, ",DIRECT,no-resolve"); got != len(lanCIDRs)+len(lanCIDRs6) {
		t.Fatalf("expected %d private-network DIRECT routes, got %d", len(lanCIDRs)+len(lanCIDRs6), got)
	}
}

func TestRenderWithOptions(t *testing.T) {
	device := warp.Device{PrivateKey: "private", PeerPublicKey: "peer", IPv4: "172.16.0.2", IPv6: "2606:4700::2"}
	config, err := RenderWithOptions(device, RenderOptions{Mode: ModeGlobal, CampusCIDRs: []string{"10.20.0.0/16", "2001:db8:1234::/48", "PKU.EDU.CN"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"mode: global", "name: GLOBAL", "IP-CIDR,10.20.0.0/16,DIRECT,no-resolve",
		"IP-CIDR6,2001:db8:1234::/48,DIRECT,no-resolve",
		// Domains are lowercased and rendered as suffix rules (covers subdomains)
		"DOMAIN-SUFFIX,pku.edu.cn,DIRECT",
	} {
		if !strings.Contains(config, expected) {
			t.Errorf("config missing %q", expected)
		}
	}
	if _, err := RenderWithOptions(device, RenderOptions{Mode: "invalid"}); err == nil {
		t.Fatal("expected invalid mode error")
	}
	if _, err := RenderWithOptions(device, RenderOptions{CampusCIDRs: []string{"not-a-cidr"}}); err == nil {
		t.Fatal("expected invalid campus entry error")
	}
}

func TestRenderDirectMode(t *testing.T) {
	device := warp.Device{PrivateKey: "private", PeerPublicKey: "peer", IPv4: "172.16.0.2", IPv6: "2606:4700::2"}
	config, err := RenderWithOptions(device, RenderOptions{Mode: ModeDirect})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(config, "mode: direct") {
		t.Fatalf("expected mode: direct in config, got:\n%.200s", config)
	}
}

func TestRenderRejectsIncompleteDevice(t *testing.T) {
	if _, err := Render(warp.Device{}); err == nil {
		t.Fatal("expected missing device data error")
	}
}

func TestRenderBlacklist(t *testing.T) {
	device := warp.Device{PrivateKey: "private", PeerPublicKey: "peer", IPv4: "172.16.0.2", IPv6: "2606:4700::2"}
	blocked := "DOMAIN-SUFFIX,ads.example.com,REJECT"

	config, err := RenderWithOptions(device, RenderOptions{
		CampusCIDRs: []string{"edu.cn"},
		Blacklist:   []string{"ADS.Example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(config, blocked) {
		t.Fatalf("expected blacklist rule %q in config", blocked)
	}
	// The block must come before any campus bypass rule
	if strings.Index(config, blocked) > strings.Index(config, "DOMAIN-SUFFIX,edu.cn,DIRECT") {
		t.Fatal("blacklist REJECT must precede campus DIRECT rules")
	}

	devConfig, err := RenderWithOptions(device, RenderOptions{
		CampusCIDRs: []string{"edu.cn"},
		Blacklist:   []string{"ads.example.com"},
		DevMode:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(devConfig, blocked) {
		t.Fatal("developer mode must not emit blacklist REJECT rules")
	}

	if _, err := RenderWithOptions(device, RenderOptions{Blacklist: []string{"10.0.0.0/8"}}); err == nil {
		t.Fatal("expected CIDR blacklist entry to be rejected (domains only)")
	}
}

func TestRenderControllerSecret(t *testing.T) {
	device := warp.Device{PrivateKey: "private", PeerPublicKey: "peer", IPv4: "172.16.0.2", IPv6: "2606:4700::2"}
	without, err := RenderWithOptions(device, RenderOptions{Mode: ModeRule})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(without, "secret:") {
		t.Fatal("empty ControllerSecret must leave the config without a secret key")
	}
	with, err := RenderWithOptions(device, RenderOptions{Mode: ModeRule, ControllerSecret: "abcdef0123456789"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(with, `secret: "abcdef0123456789"`) {
		t.Fatalf("config missing injected controller secret, got:\n%.300s", with)
	}
}
