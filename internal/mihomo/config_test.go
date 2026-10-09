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
		"log-level: error", "find-process-mode: 'always'", "mode: rule", "tun:", "enable: true",
		// 固定设备名：ICS 热点共享按 FreeV6TUN 识别 TUN 适配器
		"device: FreeV6TUN",
		"♻️ 自动选择", "🔄 故障转移", "rule-providers:", "RULE-SET,rule00,🚀 节点选择", "rules:",
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
	if count := strings.Count(config, "    type: http"); count != 10 {
		t.Fatalf("expected 10 HTTP rule providers, got %d", count)
	}
	if !strings.Contains(config, "external-controller: 127.0.0.1:9090") {
		t.Fatal("renderer must expose only the loopback mihomo controller")
	}
	// 地理数据下载地址必须走大陆可达镜像（新机器缺 GeoSite.dat 时的兜底）
	if !strings.Contains(config, "geox-url:") || !strings.Contains(config, "gh-proxy.com/https://github.com/MetaCubeX/meta-rules-dat") {
		t.Fatal("config must point geox-url at the reachable mirror")
	}
	if strings.Contains(config, "GEOIP,CN") || strings.Contains(config, "🎯 全球直连") {
		t.Fatal("external traffic must not have a direct route")
	}
	if strings.Count(config, "DIRECT") != 1 {
		t.Fatalf("expected only the LAN direct route, got %d DIRECT entries", strings.Count(config, "DIRECT"))
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
