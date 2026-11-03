package mihomo

import (
	"strings"
	"testing"

	"github.com/yourname/freev6/internal/warp"
)

const sampleCloudProfile = `
# 校园配置：只取 proxies，骨架字段以本地为准
port: 7890
mode: rule
tun:
  enable: true
  stack: system
rules:
  - MATCH,DIRECT
proxies:
  - name: 校园节点-01
    type: vless
    server: node1.example.edu.cn
    port: 443
    uuid: 11111111-2222-3333-4444-555555555555
    tls: true
    dialer-proxy: 某个不存在的节点
  - name: 校园节点-02
    type: ss
    server: node2.example.edu.cn
    port: 8388
    cipher: aes-128-gcm
    password: secret
`

func TestParseCloudProfileTakesProxiesOnly(t *testing.T) {
	profile, err := ParseCloudProfile([]byte(sampleCloudProfile))
	if err != nil {
		t.Fatalf("ParseCloudProfile: %v", err)
	}
	if len(profile.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(profile.Nodes))
	}
	if profile.Nodes[0].Name != "校园节点-01" {
		t.Fatalf("name = %q", profile.Nodes[0].Name)
	}
	// 校园节点是出口本身：配置里自带的 dialer-proxy 必须被剥掉，否则会
	// 指向不存在的节点名导致 mihomo 启动失败。
	if strings.Contains(profile.Nodes[0].YAML, "dialer-proxy") {
		t.Fatalf("dialer-proxy should be stripped:\n%s", profile.Nodes[0].YAML)
	}
	// 骨架字段必须被忽略，但要在 Ignored 里如实回报。
	joined := strings.Join(profile.Ignored, ",")
	for _, want := range []string{"rules", "tun", "port", "mode"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("ignored = %q, want containing %q", joined, want)
		}
	}
	if strings.Contains(joined, "proxies") {
		t.Fatalf("proxies must not be reported as ignored: %q", joined)
	}
}

func TestParseCloudProfileRejects(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "no proxies",
			content: "mode: rule\nrules:\n  - MATCH,DIRECT\n",
			wantErr: "proxies",
		},
		{
			name:    "direct type",
			content: "proxies:\n  - name: 直连\n    type: direct\n",
			wantErr: "不允许导入",
		},
		{
			name: "duplicate name",
			content: "proxies:\n  - name: A\n    type: ss\n    server: a.example.cn\n    port: 1\n" +
				"  - name: A\n    type: ss\n    server: b.example.cn\n    port: 2\n",
			wantErr: "重复",
		},
		{
			name:    "reserved name",
			content: "proxies:\n  - name: 🚀 节点选择\n    type: ss\n    server: a.example.cn\n    port: 1\n",
			wantErr: "冲突",
		},
		{
			name:    "broken yaml",
			content: "proxies: [\n",
			wantErr: "解析 yaml 失败",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ParseCloudProfile([]byte(testCase.content))
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, testCase.wantErr)
			}
		})
	}
}

// 校园模式的核心契约：不需要 WARP 凭据、不渲染内置接入点、骨架照旧。
func TestRenderCloudProfileWithoutWarpDevice(t *testing.T) {
	profile, err := ParseCloudProfile([]byte(sampleCloudProfile))
	if err != nil {
		t.Fatalf("ParseCloudProfile: %v", err)
	}
	config, err := RenderWithOptions(warp.Device{}, RenderOptions{
		Mode:       ModeRule,
		CloudNodes: profile.Nodes,
	})
	if err != nil {
		t.Fatalf("RenderWithOptions: %v", err)
	}
	for _, want := range []string{
		"校园节点-01", "校园节点-02",
		"device: FreeV6TUN",
		"MATCH,🚀 节点选择",
		"external-controller: 127.0.0.1:9090",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("config missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"type: masque",     // 内置接入点不该出现
		"2606:4700:103::1", // WARP 端点地址
		"private-key: ",    // 空 Device 渲染出的残块
		"dialer-proxy:",
	} {
		if strings.Contains(config, unwanted) {
			t.Fatalf("config should not contain %q", unwanted)
		}
	}
	// 规则集仍由本地骨架渲染（路由归本地）
	if !strings.Contains(config, "RULE-SET,rule00,🚀 节点选择") {
		t.Fatalf("rule-providers missing in cloud mode")
	}
}
