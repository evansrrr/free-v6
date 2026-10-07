//go:build windows

package network

import (
	"context"
	"testing"
	"time"
)

// DNS 还原失败必须是警告而非错误：网卡改名/删除、旧版 GBK 乱码快照都会
// 让 netsh 找不到名字。若把它当致命错误，故障机上已损坏的旧快照会在每次
// 启动的自愈里 500 卡死（线上故障：restore leftover network snapshot）。
// 用必然不存在的别名触发 netsh 失败来守护该行为（netsh 只报错不改状态）。
func TestRestoreSnapshotDNSFailureIsNonFatal(t *testing.T) {
	snapshot := Snapshot{Interfaces: []InterfaceSnapshot{
		{Alias: "\uFFFD\uFFFDfreev6-nonexistent-if", IPv4DNSServers: nil, IPv6DNSServers: []string{"2001:db8::1"}},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := RestoreSnapshot(ctx, snapshot); err != nil {
		t.Fatalf("DNS restore failure must not block the caller: %v", err)
	}
}
