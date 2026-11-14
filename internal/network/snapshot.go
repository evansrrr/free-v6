package network

import (
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

// firstString 从解析出的 JSON 值里取字符串字段（容错：非字符串返回空）。
func firstString(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}
