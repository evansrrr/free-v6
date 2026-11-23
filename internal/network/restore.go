package network

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrCorruptSnapshot marks a snapshot file that exists but cannot be decoded
// (0 bytes, truncated, hand-edited garbage). Callers must treat it as "no
// usable baseline": delete/self-heal instead of failing every proxy start
// forever — an unreadable snapshot file once pinned start AND stop on user
// machines until the file was removed by hand.
var ErrCorruptSnapshot = errors.New("network snapshot corrupt")

type RestoreCommand struct {
	Executable string
	Args       []string
}

func SaveSnapshot(path string, snapshot Snapshot) error {
	if path == "" {
		return fmt.Errorf("network snapshot path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create network snapshot directory: %w", err)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode network snapshot: %w", err)
	}
	// Atomic replace: a plain os.WriteFile truncates first, so a kill mid-write
	// (update child.kill, orphan taskkill, power loss) or a full disk leaves a
	// 0-byte/partial network-snapshot.json behind. Write a temp file, flush it
	// to disk, then rename over the target — the visible file is either the
	// old snapshot or the new one, never a half-written one.
	tmpPath := path + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write network snapshot: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write network snapshot: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync network snapshot: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close network snapshot: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace network snapshot: %w", err)
	}
	return nil
}

func LoadSnapshot(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read network snapshot: %w", err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("%w: %w", ErrCorruptSnapshot, err)
	}
	return snapshot, nil
}

// buildDNSRestorePlans 按接口分组构建 DNS 还原命令：每个接口内部的命令序列
// （set dnsservers 在前、add dnsservers 在后）有顺序依赖，但接口之间互不相干，
// 可并行执行。停止免流时用它把串行的 netsh 还原并行化，缩短停止耗时。
//
// 只为「快照里有地址、且来源已判定」的协议生成还原命令：mihomo 的 TUN +
// dns-hijack 不改系统 DNS 配置，dhcp/无 DNS 的接口（虚拟网卡、Loopback、
// 蓝牙等）没有还原点，跳过 —— 否则每个接口都跑一条 netsh（几十条串行/
// 并发 spawn），是停止免流「还要三秒」的主因。
func buildDNSRestorePlans(snapshot Snapshot) [][]RestoreCommand {
	plans := make([][]RestoreCommand, 0, len(snapshot.Interfaces))
	for _, interfaceSnapshot := range snapshot.Interfaces {
		if interfaceSnapshot.Alias == "" {
			continue
		}
		var commands []RestoreCommand
		if len(interfaceSnapshot.IPv4DNSServers) > 0 {
			commands = append(commands, buildDNSRestoreCommand(protocolIPv4, interfaceSnapshot.Alias,
				interfaceSnapshot.IPv4DNSServers, interfaceSnapshot.IPv4DNSSource)...)
		}
		if len(interfaceSnapshot.IPv6DNSServers) > 0 {
			commands = append(commands, buildDNSRestoreCommand(protocolIPv6, interfaceSnapshot.Alias,
				interfaceSnapshot.IPv6DNSServers, interfaceSnapshot.IPv6DNSSource)...)
		}
		if len(commands) > 0 {
			plans = append(plans, commands)
		}
	}
	return plans
}

// DNSCurrentLookup 按接口别名查「此刻生效的 DNS」，ok=false 表示查不到该接口。
type DNSCurrentLookup func(alias string, ipv6 bool) ([]string, bool)

// pruneChangedDHCPDNS 丢掉当前值和快照不一致的 DHCP 还原点。
//
// DHCP 还原的动作是「撤掉静态配置」，属于破坏性操作：用户在免流期间手动改过
// DNS、或中途换过网络让 DHCP 重新下发，快照里的地址就已经不是现状了，此时再
// 执行 source=dhcp 会把用户的设置一起抹掉。比对的是「值」而不是来源 ——
// 值相同时还原等于空操作（顺带修掉旧版本留下的同值静态配置），值不同就说明
// 这期间有人动过，交给用户自己。静态还原不走这个判断：它写回的就是用户自己
// 的配置，与现状无关。
func pruneChangedDHCPDNS(snapshot Snapshot, current DNSCurrentLookup) Snapshot {
	if current == nil {
		return snapshot
	}
	pruned := snapshot
	pruned.Interfaces = make([]InterfaceSnapshot, len(snapshot.Interfaces))
	for index, interfaceSnapshot := range snapshot.Interfaces {
		if interfaceSnapshot.IPv4DNSSource == DNSSourceDHCP &&
			!dnsStillMatches(current, interfaceSnapshot.Alias, false, interfaceSnapshot.IPv4DNSServers) {
			interfaceSnapshot.IPv4DNSServers = nil
			interfaceSnapshot.IPv4DNSSource = DNSSourceUnknown
		}
		if interfaceSnapshot.IPv6DNSSource == DNSSourceDHCP &&
			!dnsStillMatches(current, interfaceSnapshot.Alias, true, interfaceSnapshot.IPv6DNSServers) {
			interfaceSnapshot.IPv6DNSServers = nil
			interfaceSnapshot.IPv6DNSSource = DNSSourceUnknown
		}
		pruned.Interfaces[index] = interfaceSnapshot
	}
	return pruned
}

// dnsStillMatches 报告接口当前生效的 DNS 是否仍等于快照记录的值。查不到接口
// （改名/刚被删除）时保持原计划：让 netsh 自己报错进日志，比静默跳过更容易
// 排查，这也是还原失败一直以来的非致命处理方式。
func dnsStillMatches(current DNSCurrentLookup, alias string, ipv6 bool, want []string) bool {
	got, known := current(alias, ipv6)
	if !known {
		return true
	}
	return sameServerSet(got, want)
}

// sameServerSet 比较两组 DNS 地址是否相同（忽略顺序与大小写）。
func sameServerSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[strings.ToLower(value)]++
	}
	for _, value := range right {
		key := strings.ToLower(value)
		if counts[key] == 0 {
			return false
		}
		counts[key]--
	}
	return true
}

// needsSharingOnlyRestore 报告快照是否需要「只按角色基线」补一次 ICS 还原：
// 有 Sharing、却没有 Hotspot 元数据。这是旧版本在「ICS 已经在脚本里切完、
// 元数据还没写回」时被强杀留下的形状 —— 不补这一步，那段共享角色会一直
// 停在切换后的状态，指向已经消失的 FreeV6TUN。
func needsSharingOnlyRestore(snapshot Snapshot) bool {
	return snapshot.Hotspot == nil && len(snapshot.Sharing) > 0
}

// buildDNSRestoreCommand 按快照记录的 DNS 来源生成还原命令：
//   - 静态：set source=static + 依次 add，保持原来的多地址顺序；
//   - DHCP：一条 set source=dhcp，把接口交还给 DHCP（地址由租约提供）；
//   - 未知：不生成命令。旧版本快照没有来源字段，在那种快照上按 static
//     还原正是「把 DHCP DNS 固化成静态」的成因，宁可不动。
func buildDNSRestoreCommand(protocol, alias string, servers []string, source string) []RestoreCommand {
	if len(servers) == 0 {
		return nil
	}
	switch source {
	case DNSSourceStatic:
		// 落到下面的 set source=static + 逐个 add。
	case DNSSourceDHCP:
		return []RestoreCommand{{Executable: "netsh", Args: []string{
			"interface", protocol, "set", "dnsservers", "name=" + alias, "source=dhcp",
		}}}
	default:
		return nil
	}
	args := []string{"interface", protocol, "set", "dnsservers", "name=" + alias, "source=static", "address=" + servers[0], "validate=no"}
	commands := []RestoreCommand{{Executable: "netsh", Args: args}}
	for index, server := range servers[1:] {
		commands = append(commands, RestoreCommand{
			Executable: "netsh",
			Args:       []string{"interface", protocol, "add", "dnsservers", "name=" + alias, "address=" + server, "index=" + fmt.Sprint(index+2), "validate=no"},
		})
	}
	return commands
}
