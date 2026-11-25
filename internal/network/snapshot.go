package network

import (
	"strings"
	"time"
)

// Snapshot 只记录「本程序可能改过的系统状态」：ICS 共享角色与热点上游。
//
// 这里没有网卡 DNS，因为免流全程不写系统 DNS：mihomo 的 dns-hijack 在包层
// 拦截解析请求，auto-route 只改路由表。既然没改过，就没有可还原的 DNS 基线
// —— 旧版本「把 DHCP 下发的地址写成静态」的存量污染，由启动时的
// ResetDNSToDHCP 迁移修复，不需要在停止时再写一次 DNS。
type Snapshot struct {
	// CapturedAt 仅用于排障（确认残留基线来自哪次会话），不参与任何还原
	// 决策：热点/共享是本程序自己的开关，与采集时间无关。
	CapturedAt time.Time `json:"capturedAt"`
	// Interfaces 仅用于排障日志（各网卡的默认网关），不会被还原。
	Interfaces []InterfaceSnapshot `json:"interfaces,omitempty"`
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
}

// firstString 从解析出的 JSON 值里取字符串字段（容错：非字符串返回空）。
func firstString(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}
