package network

import (
	"strings"
	"time"
)

// DNS 来源标记。快照必须记住「这组 DNS 是用户写死的，还是 DHCP 下发的」：
// 还原时无条件 source=static 会把 DHCP 拿到的地址固化进网卡（值相同，当下
// 无感），换网络后静态 DNS 仍指向旧网段，表现是「Wi-Fi 已连接但打不开网页」，
// 而且重启网卡也不会恢复 —— 静态配置会一直留着。
const (
	// DNSSourceStatic 用户在网卡属性 / 组策略里写死的静态 DNS。
	DNSSourceStatic = "static"
	// DNSSourceDHCP DNS 由 DHCP（IPv6 为 RA）下发，快照里只是当前生效值。
	DNSSourceDHCP = "dhcp"
	// DNSSourceUnknown 来源未知：旧版本快照没有这两个字段，或注册表读不到。
	// 还原时一律不动 DNS —— 宁可少做一步，也不能把别人的 DHCP 配置写成静态。
	DNSSourceUnknown = ""
)

// netsh interface <protocol> ... 用的协议名。
const (
	protocolIPv4 = "ipv4"
	protocolIPv6 = "ipv6"
)

// SnapshotMaxAge 是残留快照还能被自动还原的最大年龄。超过它的基线不再代表
// 「当前网络」：三天前的校园网 DNS 套到宿舍/热点上，等于人为制造一次解析
// 故障（与把 DHCP DNS 固化成静态是同一类事故，只是方向相反）。
const SnapshotMaxAge = 24 * time.Hour

// snapshotClockSkew 是允许的系统时钟误差。比「现在」还晚这么多的快照说明
// 机器时钟被回拨过，年龄算不出来 —— 按来路不明处理，别当新鲜基线用。
const snapshotClockSkew = 5 * time.Minute

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
	// IPv4DNSSource / IPv6DNSSource 记录上面两组地址的来源（DNSSourceStatic /
	// DNSSourceDHCP / DNSSourceUnknown）。旧快照没有这两个字段 → 解出空值 =
	// 未知，还原时跳过 DNS，向后兼容。
	IPv4DNSSource string `json:"ipv4DnsSource,omitempty"`
	IPv6DNSSource string `json:"ipv6DnsSource,omitempty"`
}

// StaleAt 报告快照是否老到不能再当作当前基线自动还原。没有时间戳的快照
// （手改、极旧版本）同样算过期：来路不明的基线不值得信任。
func (snapshot Snapshot) StaleAt(now time.Time) bool {
	if snapshot.CapturedAt.IsZero() {
		return true
	}
	// 时钟被回拨过时 now.Sub 会是负数，快照会显得「刚从未来抓的」。这种
	// 年龄不可信，按过期处理（成功还原一次就会删除快照，影响本来有界，
	// 但不能让它顶着错的时间戳反复生效）。
	if snapshot.CapturedAt.After(now.Add(snapshotClockSkew)) {
		return true
	}
	return now.Sub(snapshot.CapturedAt) > SnapshotMaxAge
}

// DropDNSServers 清掉指定接口（按协议）的 DNS 还原点，Sharing/Hotspot 等其他
// 基线保持不变。用于「修复网卡 DNS 为自动」之后：残留基线里那几条静态地址
// 正是用户刚清掉的，留着会在下次启动的自愈里被写回去。
func (snapshot *Snapshot) DropDNSServers(changes []DNSResetChange) {
	for _, change := range changes {
		for index := range snapshot.Interfaces {
			if !strings.EqualFold(snapshot.Interfaces[index].Alias, change.Alias) {
				continue
			}
			if change.IPv4 {
				snapshot.Interfaces[index].IPv4DNSServers = nil
				snapshot.Interfaces[index].IPv4DNSSource = DNSSourceUnknown
			}
			if change.IPv6 {
				snapshot.Interfaces[index].IPv6DNSServers = nil
				snapshot.Interfaces[index].IPv6DNSSource = DNSSourceUnknown
			}
		}
	}
}

// firstString 从解析出的 JSON 值里取字符串字段（容错：非字符串返回空）。
func firstString(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}
