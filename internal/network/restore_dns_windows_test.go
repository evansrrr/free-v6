//go:build windows

package network

import (
	"context"
	"strings"
	"testing"
	"time"
)

// DNS 还原失败必须是警告而非错误：网卡改名/删除、旧版 GBK 乱码快照都会
// 让 netsh 找不到名字。若把它当致命错误，故障机上已损坏的旧快照会在每次
// 启动的自愈里 500 卡死（线上故障：restore leftover network snapshot）。
// 用必然不存在的别名触发 netsh 失败来守护该行为（netsh 只报错不改状态）。
// 静态与 DHCP 两种还原命令都要走到真实的 netsh（来源未知会跳过，测不到东西）。
func TestRestoreSnapshotDNSFailureIsNonFatal(t *testing.T) {
	snapshot := Snapshot{Interfaces: []InterfaceSnapshot{
		{Alias: "\uFFFD\uFFFDfreev6-nonexistent-static", IPv6DNSServers: []string{"2001:db8::1"}, IPv6DNSSource: DNSSourceStatic},
		{Alias: "\uFFFD\uFFFDfreev6-nonexistent-dhcp", IPv4DNSServers: []string{"192.0.2.53"}, IPv4DNSSource: DNSSourceDHCP},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := RestoreSnapshot(ctx, snapshot); err != nil {
		t.Fatalf("DNS restore failure must not block the caller: %v", err)
	}
}

// 自愈入口也必须还原 DNS：RestoreLeftoverSnapshot 是 helper 与 CLI 唯一的
// 残留还原入口，它比 RestoreSnapshot 多出来的只有 Sharing 兜底那一步 ——
// 不能因此把 DNS 段漏掉，否则快照在被删除前，DNS 基线一次都不会被应用。
// 回归：改写成「只调 RestoreHotspot」后，自愈不再还原 DNS。
func TestRestoreLeftoverSnapshotRestoresDNS(t *testing.T) {
	original := restoreDNSCommand
	defer func() { restoreDNSCommand = original }()
	var ran []string
	restoreDNSCommand = func(_ context.Context, executable string, args []string) ([]byte, error) {
		ran = append(ran, executable+" "+strings.Join(args, " "))
		return nil, nil
	}

	snapshot := Snapshot{Interfaces: []InterfaceSnapshot{
		{Alias: "freev6-test-if", IPv4DNSServers: []string{"192.0.2.53"}, IPv4DNSSource: DNSSourceStatic},
	}}
	if err := RestoreLeftoverSnapshot(context.Background(), snapshot); err != nil {
		t.Fatalf("leftover restore must not fail: %v", err)
	}
	if len(ran) != 1 || !strings.Contains(ran[0], "address=192.0.2.53") {
		t.Fatalf("leftover restore must execute the DNS plan, ran %v", ran)
	}
}
