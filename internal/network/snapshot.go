package network

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

type Snapshot struct {
	CapturedAt time.Time           `json:"capturedAt"`
	Interfaces []InterfaceSnapshot `json:"interfaces"`
	// Sharing 是切换前的 ICS 共享状态基线（internal/network/ics.go）：
	// 热点共享切换失败回滚与停止免流时按它还原各连接的 PUBLIC/PRIVATE 角色。
	Sharing []SharingSnapshot `json:"sharing,omitempty"`
	// Hotspot 非 nil 表示本次会话已执行热点上游切换；停止时据此做 winrt 还原。
	Hotspot *HotspotMeta `json:"hotspot,omitempty"`
}

type InterfaceSnapshot struct {
	Alias               string   `json:"alias"`
	Index               int      `json:"index"`
	IPv4DefaultGateways []string `json:"ipv4DefaultGateways"`
	IPv6DefaultGateways []string `json:"ipv6DefaultGateways"`
	IPv4DNSServers      []string `json:"ipv4DnsServers"`
	IPv6DNSServers      []string `json:"ipv6DnsServers"`
}

// PowerShellSnapshotCommand 必须先将控制台输出编码切到 UTF-8：中文
// Windows（代码页 936/GBK）上 PowerShell 管道输出走 GBK，Go 的
// json.Unmarshal 会把无效字节替换成 U+FFFD，快照里的中文网卡名
// （如 以太网）就烂掉，netsh 还原时找不到网卡 → 自愈卡死启动。
// 与 internal/network/ics.go 的 psHeader 同一机制。
// 逐网卡 try：Get-NetIPConfiguration 对半移除/状态抖动的网卡会抛终止性
// 错误（重启免流时 FreeV6TUN 正在消失、虚拟网卡热插拔），整条管道直接
// exit 1 —— 跳过坏网卡、保住其余基线。foreach 是语句不能直接做管道左值，
// 必须包成 $(...) 子表达式。
const PowerShellSnapshotCommand = "[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; " + `$(foreach ($cfg in Get-NetIPConfiguration -ErrorAction SilentlyContinue) { try { $cfg | Select-Object InterfaceAlias,InterfaceIndex,IPv4DefaultGateway,IPv6DefaultGateway,DNSServer -ErrorAction Stop } catch { } }) | ConvertTo-Json -Depth 6 -Compress`

func ParsePowerShellSnapshot(data []byte) (Snapshot, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Snapshot{}, fmt.Errorf("empty network snapshot (PowerShell produced no output)")
	}
	var raw any
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&raw); err != nil {
		return Snapshot{}, fmt.Errorf("decode network snapshot: %w", err)
	}
	items, ok := raw.([]any)
	if !ok {
		if object, objectOK := raw.(map[string]any); objectOK {
			items = []any{object}
		} else {
			return Snapshot{}, fmt.Errorf("network snapshot must be an object or array")
		}
	}
	snapshot := Snapshot{CapturedAt: time.Now(), Interfaces: make([]InterfaceSnapshot, 0, len(items))}
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return Snapshot{}, fmt.Errorf("network snapshot item must be an object")
		}
		dnsServers := uniqueStrings(extractNetworkStrings(object["DNSServer"]))
		interfaceSnapshot := InterfaceSnapshot{
			Alias:               firstString(object["InterfaceAlias"]),
			Index:               firstInt(object["InterfaceIndex"]),
			IPv4DefaultGateways: uniqueStrings(extractNetworkStrings(object["IPv4DefaultGateway"])),
			IPv6DefaultGateways: uniqueStrings(extractNetworkStrings(object["IPv6DefaultGateway"])),
			IPv4DNSServers:      filterIPVersion(dnsServers, false),
			IPv6DNSServers:      filterIPVersion(dnsServers, true),
		}
		if interfaceSnapshot.Alias == "" && interfaceSnapshot.Index == 0 {
			continue
		}
		snapshot.Interfaces = append(snapshot.Interfaces, interfaceSnapshot)
	}
	// 空快照绝不能落盘：后续恢复会变成无操作，等于把改过的网络状态
	// 永久留在切换态。空结果视为本次抓取失败（可重试）。
	if len(snapshot.Interfaces) == 0 {
		return Snapshot{}, fmt.Errorf("network snapshot is empty")
	}
	return snapshot, nil
}

func filterIPVersion(values []string, ipv6 bool) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		address := net.ParseIP(value)
		if address == nil || (address.To4() == nil) != ipv6 {
			continue
		}
		result = append(result, value)
	}
	return result
}

func extractNetworkStrings(value any) []string {
	var result []string
	var visit func(any)
	visit = func(current any) {
		switch typed := current.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				result = append(result, strings.TrimSpace(typed))
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		case map[string]any:
			for key, child := range typed {
				if key == "NextHop" || key == "ServerAddresses" || key == "Address" {
					visit(child)
				}
			}
		}
	}
	visit(value)
	return result
}

func firstString(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func firstInt(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(typed))
		return parsed
	default:
		return 0
	}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
