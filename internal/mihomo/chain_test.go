package mihomo

import (
	"strings"
	"testing"

	"github.com/yourname/freev6/internal/warp"
	"gopkg.in/yaml.v3"
)

const sampleSubscription = `
# Clash subscription sample
port: 7890
proxy-groups: []
rules:
  - MATCH,DIRECT
proxies:
  - name: 机场A-香港01
    type: ss
    server: hk1.example.com
    port: 443
    cipher: aes-256-gcm
    password: secret
    dialer-proxy: some-other-hop
  - name: "机场B : 东京"
    type: vmess
    server: jp1.example.com
    port: 8443
    uuid: 11111111-2222-3333-4444-555555555555
    alterId: 0
    tls: true
`

func TestParseChainProxiesInjectsDialerAndReemits(t *testing.T) {
	chains, err := ParseChainProxies([]byte(sampleSubscription))
	if err != nil {
		t.Fatal(err)
	}
	if len(chains) != 2 {
		t.Fatalf("expected 2 proxies, got %d", len(chains))
	}
	if chains[0].Name != "机场A-香港01" {
		t.Fatalf("unexpected name %q", chains[0].Name)
	}
	for _, chain := range chains {
		if !strings.HasPrefix(chain.YAML, "- ") {
			t.Fatalf("block for %q is not a YAML list item:\n%s", chain.Name, chain.YAML)
		}
		// Overwrite, not merge: a user-set dialer must not stay in place.
		if !strings.Contains(chain.YAML, "dialer-proxy:") || strings.Contains(chain.YAML, "some-other-hop") {
			t.Fatalf("dialer-proxy not forced for %q:\n%s", chain.Name, chain.YAML)
		}
		// The group tail must appear; yaml.v3 may \U-escape the emoji itself
		// ("\U0001F680 节点选择") which parses back to the same string.
		if !strings.Contains(chain.YAML, "节点选择") {
			t.Fatalf("dialer-proxy must point at the MASQUE group for %q:\n%s", chain.Name, chain.YAML)
		}
	}
	// Credentials survive the round-trip (we only rewrite dialer-proxy).
	if !strings.Contains(chains[0].YAML, "password: secret") {
		t.Fatalf("password lost in re-emit:\n%s", chains[0].YAML)
	}
	// The subscription's own rules/groups never leak into our config —
	// ParseChainProxies only returns proxy entries.
	for _, chain := range chains {
		if strings.Contains(chain.YAML, "MATCH,") {
			t.Fatalf("rules leaked into chain block:\n%s", chain.YAML)
		}
	}
}

func TestParseChainProxiesRejectsBadInput(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{"not yaml", "::::::\n  - x: [", "解析 yaml 失败"},
		{"no proxies", "rules:\n  - MATCH,DIRECT\n", "没有 proxies"},
		{"missing type", "proxies:\n  - name: node1\n", "name 或 type"},
		{"missing name", "proxies:\n  - type: ss\n", "name 或 type"},
		{"duplicate name", "proxies:\n  - {name: a, type: ss}\n  - {name: a, type: ss}\n", "重复"},
		{"reserved builtin", "proxies:\n  - {name: DIRECT, type: ss}\n", "冲突"},
		{"reserved group", "proxies:\n  - {name: \"🚀 节点选择\", type: ss}\n", "冲突"},
		{"reserved chain group", "proxies:\n  - {name: \"🔗 链式代理\", type: ss}\n", "冲突"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ParseChainProxies([]byte(testCase.content))
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, testCase.wantErr)
			}
		})
	}
}

func TestParseChainProxiesRejectsReservedAccessPointName(t *testing.T) {
	// MASQUE node names share the namespace; simulate one from the endpoints table.
	reserved := endpointName(endpoints[0].Server, endpoints[0].Port)
	content := "proxies:\n  - {name: \"" + reserved + "\", type: ss}\n"
	if _, err := ParseChainProxies([]byte(content)); err == nil || !strings.Contains(err.Error(), "冲突") {
		t.Fatalf("expected reserved-name conflict, got %v", err)
	}
}

func chainTestDevice() warp.Device {
	return warp.Device{PrivateKey: "private", PeerPublicKey: "peer", IPv4: "172.16.0.2", IPv6: "2606:4700::2"}
}

func TestRenderWithChainProxy(t *testing.T) {
	chains, err := ParseChainProxies([]byte(sampleSubscription))
	if err != nil {
		t.Fatal(err)
	}
	config, err := RenderWithOptions(chainTestDevice(), RenderOptions{
		Mode:         ModeRule,
		CampusCIDRs:  []string{"edu.cn"},
		ChainProxies: chains,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"name: 机场A-香港01", // imported entry present (yaml.v3 sorts keys,
		// so the block starts with dialer-proxy, not with the name)
		"dialer-proxy:",               // underlay injected
		"节点选择",                        // ...pointing at the MASQUE group (emoji may be \U-escaped)
		"name: 🔗 链式代理",                // chain group
		"name: '机场B : 东京'",            // special-char name safely quoted
		"MATCH,🔗 链式代理",                // MATCH routed through the chain
		"type: masque",                // masque nodes untouched
		"DOMAIN-SUFFIX,edu.cn,DIRECT", // whitelist still first
		"password: secret",            // credentials survive re-emit
	} {
		if !strings.Contains(config, expected) {
			t.Errorf("chain config missing %q", expected)
		}
	}
	if strings.Contains(config, "MATCH,🚀 节点选择") {
		t.Fatal("chain build must not route MATCH straight to the MASQUE group")
	}
}

// 回读解析：子串断言证明不了 YAML 合法（链式块曾因混用顶格/缩进序列而
// 让真实 mihomo -t 直接解析失败）。渲染产物必须能被严格解析回结构。
func TestRenderWithChainProxyParsesBack(t *testing.T) {
	chains, err := ParseChainProxies([]byte(sampleSubscription))
	if err != nil {
		t.Fatal(err)
	}
	config, err := RenderWithOptions(chainTestDevice(), RenderOptions{
		Mode:         ModeRule,
		CampusCIDRs:  []string{"edu.cn"},
		ChainProxies: chains,
	})
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal([]byte(config), &document); err != nil {
		t.Fatalf("rendered chain config is not valid YAML: %v", err)
	}
	proxies, ok := document["proxies"].([]any)
	if !ok || len(proxies) != len(endpoints)+len(chains) {
		t.Fatalf("proxies section must hold %d masque + %d chain entries, got %d",
			len(endpoints), len(chains), len(proxies))
	}
	last := proxies[len(proxies)-1].(map[string]any)
	if last["name"] != "机场B : 东京" || last["dialer-proxy"] != "🚀 节点选择" {
		t.Fatalf("chain entry not parsed back correctly: %+v", last)
	}
}

func TestRenderWithoutChainUnchanged(t *testing.T) {
	config, err := RenderWithOptions(chainTestDevice(), RenderOptions{Mode: ModeRule})
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{ChainGroup, "dialer-proxy", "机场A-香港01"} {
		if strings.Contains(config, unwanted) {
			t.Errorf("non-chain config must not contain %q", unwanted)
		}
	}
	if !strings.Contains(config, "MATCH,🚀 节点选择") {
		t.Fatal("non-chain MATCH target changed")
	}
}

func TestParseChainProxiesRejectsLeakyTypes(t *testing.T) {
	cases := map[string]string{
		"direct": "proxies:\n  - {name: leaky, type: direct}\n",
		"dns":    "proxies:\n  - {name: weird, type: dns}\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseChainProxies([]byte(content))
			if err == nil || !strings.Contains(err.Error(), "不允许导入") {
				t.Fatalf("err = %v, want rejection for type %s", err, name)
			}
		})
	}
}

// 链式启用 = 全流量经自有代理：不得再出现指向接入点的 RULE-SET 分流
// 与 rule-providers（它们造成“国内域名走 WARP 出口”的分裂，并在后台
// 拉取时绕链外联）。未启用时两者必须原样保留。
func TestRenderChainDropsRuleSetSplitEgress(t *testing.T) {
	chains, err := ParseChainProxies([]byte(sampleSubscription))
	if err != nil {
		t.Fatal(err)
	}
	withChain, err := RenderWithOptions(chainTestDevice(), RenderOptions{
		Mode:         ModeRule,
		CampusCIDRs:  []string{"edu.cn"},
		ChainProxies: chains,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"rule-providers:", "RULE-SET,"} {
		if strings.Contains(withChain, unwanted) {
			t.Errorf("chain config must not contain %q (split-egress path)", unwanted)
		}
	}
	if !strings.Contains(withChain, "MATCH,🔗 链式代理") || !strings.Contains(withChain, "DOMAIN-SUFFIX,edu.cn,DIRECT") {
		t.Fatal("chain config must keep whitelist rules and chain MATCH target")
	}

	withoutChain, err := RenderWithOptions(chainTestDevice(), RenderOptions{Mode: ModeRule, CampusCIDRs: []string{"edu.cn"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withoutChain, "rule-providers:") || !strings.Contains(withoutChain, "RULE-SET,rule00") {
		t.Fatal("non-chain config must keep rule-providers and RULE-SET lines unchanged")
	}
}

// UDP 泄露兜底：REJECT 规则必须在 MATCH 之后（依赖 mihomo match() 的
// skip-continue——节点不支持 UDP 时 MATCH 被跳过才会落到它）；
// 未启用链式时不得出现该规则。
func TestRenderChainAppendsUdpCatchAfterMatch(t *testing.T) {
	chains, err := ParseChainProxies([]byte(sampleSubscription))
	if err != nil {
		t.Fatal(err)
	}
	withChain, err := RenderWithOptions(chainTestDevice(), RenderOptions{
		Mode: ModeRule, CampusCIDRs: []string{"edu.cn"}, ChainProxies: chains,
	})
	if err != nil {
		t.Fatal(err)
	}
	matchIdx := strings.Index(withChain, "MATCH,🔗 链式代理")
	catchIdx := strings.Index(withChain, "NETWORK,UDP,REJECT")
	if matchIdx < 0 || catchIdx < 0 {
		t.Fatalf("chain config missing MATCH or UDP catch rule (match=%d catch=%d)", matchIdx, catchIdx)
	}
	if catchIdx < matchIdx {
		t.Fatal("UDP REJECT catch must come AFTER MATCH so capable nodes still chain UDP")
	}

	withoutChain, err := RenderWithOptions(chainTestDevice(), RenderOptions{Mode: ModeRule, CampusCIDRs: []string{"edu.cn"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(withoutChain, "NETWORK,UDP,REJECT") {
		t.Fatal("non-chain config must not contain the UDP catch rule")
	}
}
