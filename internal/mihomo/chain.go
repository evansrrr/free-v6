package mihomo

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// ChainGroup is the selectable frontend group for imported nodes.
	ChainGroup = "🔗 链式代理"
	// chainDialer is the underlay every imported node dials through:
	// connecting to the user's own proxy first crosses the SELECTED MASQUE
	// access point, which is what keeps the whole chain inside the免流
	// transport (local → masque → own proxy → target).
	chainDialer = "🚀 节点选择"
)

// ChainProxy is one imported outbound re-emitted as a YAML list item
// ("- ..."), ready to append to the rendered `proxies:` section.
type ChainProxy struct {
	Name string
	YAML string
}

// ParseChainProxies extracts the `proxies` list from a Clash/mihomo yaml
// (file import or subscription snapshot), validates names, forces
// dialer-proxy onto the MASQUE group and re-emits each entry as a list
// item. The document's own rules/groups are dropped: OUR rules govern
// routing in the generated config.
func ParseChainProxies(content []byte) ([]ChainProxy, error) {
	var document struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(content, &document); err != nil {
		return nil, fmt.Errorf("解析 yaml 失败: %w", err)
	}
	if len(document.Proxies) == 0 {
		return nil, fmt.Errorf("yaml 中没有 proxies 列表（需要 Clash/mihomo 格式的订阅或配置）")
	}
	return normalizeProxyEntries(document.Proxies, reservedChainNames(), chainDialer)
}

// normalizeProxyEntries 是链式代理与备用配置共用的节点校验/重发射逻辑。
// dialer 非空时无条件写入 dialer-proxy（链式语义：所有导入节点都必须挂在
// MASQUE 接入点后面）；为空时剥掉配置自带的 dialer-proxy（备用配置：节点
// 就是出口本身，指向未知名字会让 mihomo 启动失败）。
func normalizeProxyEntries(entries []map[string]any, reserved map[string]bool, dialer string) ([]ChainProxy, error) {
	seen := make(map[string]bool, len(entries))
	result := make([]ChainProxy, 0, len(entries))
	// direct 类型 = 把流量原样从本机真实网卡送出去：混进分组后
	// “看起来在代理上、实际真实 IP 直通”，是明确的泄露向量，直接拒绝。
	// dns 类型与代理无关，同样拒绝。
	forbiddenTypes := map[string]bool{"direct": true, "dns": true}
	for index, entry := range entries {
		name, _ := entry["name"].(string)
		name = strings.TrimSpace(name)
		kind, _ := entry["type"].(string)
		kind = strings.ToLower(strings.TrimSpace(kind))
		if name == "" || kind == "" {
			return nil, fmt.Errorf("第 %d 个代理缺少 name 或 type 字段", index+1)
		}
		if len(name) > cloudMaxNameLen {
			return nil, fmt.Errorf("第 %d 个代理名称过长（上限 %d 字符）", index+1, cloudMaxNameLen)
		}
		if forbiddenTypes[kind] {
			return nil, fmt.Errorf("第 %d 个代理类型 %q 不允许导入（%s 类型会让流量直连真实出口 IP）", index+1, kind, kind)
		}
		if seen[name] {
			return nil, fmt.Errorf("代理名称重复: %q", name)
		}
		if reserved[name] {
			return nil, fmt.Errorf("代理名称与内置分组/接入点冲突: %q", name)
		}
		seen[name] = true
		if dialer != "" {
			// Overwrite unconditionally: the contract is local→masque→own proxy,
			// and a user-set dialer would silently leave the免流 path.
			entry["dialer-proxy"] = dialer
		} else {
			delete(entry, "dialer-proxy")
		}
		block, err := yaml.Marshal([]map[string]any{entry})
		if err != nil {
			return nil, fmt.Errorf("重新发射代理 %q 失败: %w", name, err)
		}
		result = append(result, ChainProxy{Name: name, YAML: string(block)})
	}
	return result, nil
}

// reservedChainNames lists every name an imported node must not take:
// built-in groups, builtin outbounds and the MASQUE access-point nodes all
// share mihomo's single proxy namespace (duplicates fail config load).
func reservedChainNames() map[string]bool {
	names := []string{
		"🚀 节点选择", "GLOBAL", "☑️ 手动切换", "♻️ 自动选择", "🔄 故障转移",
		"🛑 全球拦截", "🐟 漏网之鱼", ChainGroup,
		"DIRECT", "REJECT", "PASS", "COMPATIBLE",
	}
	for _, endpoint := range endpoints {
		names = append(names, endpointName(endpoint.Server, endpoint.Port))
	}
	reserved := make(map[string]bool, len(names))
	for _, name := range names {
		reserved[name] = true
	}
	return reserved
}

// writeChainGroup appends the selectable chain group. Member names are
// user input, so they are JSON-quoted (a JSON string is a valid YAML
// double-quoted scalar) — plain emission would misparse names like "123"
// or ones starting with YAML indicator characters.
func writeChainGroup(b *strings.Builder, chains []ChainProxy) {
	b.WriteString("  - name: " + ChainGroup + "\n    type: select\n    proxies:\n")
	for _, chain := range chains {
		quoted, _ := json.Marshal(chain.Name)
		fmt.Fprintf(b, "      - %s\n", quoted)
	}
	b.WriteString("\n")
}

// writeChainProxyBlock emits a re-emitted ("- ...") entry into our
// `proxies:` section, whose list items are indented by 2 spaces. yaml.v3
// marshals a top-level list flush left; mixing indents under one key is a
// YAML error (caught by the real `mihomo -t` E2E). Every line shifts
// equally so nested block scalars keep their relative indent.
func writeChainProxyBlock(b *strings.Builder, block string) {
	for _, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
		if line != "" {
			b.WriteString("  ")
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
}
