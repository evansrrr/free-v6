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
		"log-level: error", "find-process-mode: 'always'", "sniffer:", "enhanced-mode: fake-ip",
		"♻️ 自动选择", "🔄 故障转移", "rule-providers:", "RULE-SET,rule00,🎯 全球直连", "rules:",
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
	if strings.Contains(config, "external-controller:") {
		t.Fatal("renderer should follow gen_masque.py settings and omit external controller")
	}
}

func TestRenderRejectsIncompleteDevice(t *testing.T) {
	if _, err := Render(warp.Device{}); err == nil {
		t.Fatal("expected missing device data error")
	}
}
