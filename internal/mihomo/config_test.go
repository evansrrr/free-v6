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

func TestRenderRejectsIncompleteDevice(t *testing.T) {
	if _, err := Render(warp.Device{}); err == nil {
		t.Fatal("expected missing device data error")
	}
}
