//go:build windows

package network

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// RestoreSnapshot 完整还原网络基线（热点上游 + 各接口 DNS），保持旧语义：
// 热点还原失败返回错误（调用方保留快照），DNS 还原失败只记警告不阻断。
// 需要把 mihomo 停止插在热点与 DNS 之间的调用方（stopProxy）用
// RestoreHotspot / RestoreDNS 单独控制阶段。
func RestoreSnapshot(ctx context.Context, snapshot Snapshot) error {
	err := RestoreHotspot(ctx, snapshot)
	RestoreDNS(ctx, snapshot)
	return err
}

// RestoreHotspot 还原热点上游（WinRT/ICS 角色）。必须在 TUN 适配器仍在时
// 完成：FreeV6TUN 连接存在才能显式禁用其共享角色。失败返回错误，调用方
// 保留快照、下次启动免流前自愈还原。
func RestoreHotspot(ctx context.Context, snapshot Snapshot) error {
	if snapshot.Hotspot == nil || !snapshot.Hotspot.Applied {
		return nil
	}
	var issues []string
	if snapshot.Hotspot.Path == "winrt" {
		if err := RestoreWinRT(ctx, snapshot.Hotspot.OriginalProfile); err != nil {
			issues = append(issues, err.Error())
		}
	}
	if err := RestoreICSSharing(ctx, snapshot); err != nil {
		issues = append(issues, err.Error())
	}
	if len(issues) > 0 {
		return fmt.Errorf("%s", strings.Join(issues, "; "))
	}
	return nil
}

// RestoreDNS 还原各接口 DNS。不依赖 TUN 适配器，放在 mihomo 退出之后执行
// 也安全；接口间并行、接口内保序（set dnsservers → add dnsservers）。失败
// 只记警告不阻断：网卡改名/删除、旧版 GBK 乱码快照都会让 netsh 找不到
// 名字，若当致命错误，故障机上损坏的旧快照会在每次启动自愈里永久卡死。
func RestoreDNS(ctx context.Context, snapshot Snapshot) {
	plans := buildDNSRestorePlans(snapshot)
	if len(plans) == 0 {
		return
	}
	// 有限并发：接口多时一次 spawn 二三十个 netsh 会互相抢占、也容易触发
	// netsh 内部的并发限制；6 路足够把串行 N×~150ms 压到 ~300ms 量级。
	const maxConcurrent = 6
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []string
	for _, plan := range plans {
		wg.Add(1)
		go func(commands []RestoreCommand) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			for _, restoreCommand := range commands {
				if ctx.Err() != nil {
					return
				}
				if output, err := exec.CommandContext(ctx, restoreCommand.Executable, restoreCommand.Args...).CombinedOutput(); err != nil {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("%q: %v (%s)", restoreCommand.Args, err, strings.TrimSpace(string(output))))
					mu.Unlock()
				}
			}
		}(plan)
	}
	wg.Wait()
	if len(failures) > 0 {
		fmt.Fprintf(os.Stderr, "freev6: DNS restore warning (non-fatal): %s\n", strings.Join(failures, "; "))
	}
}
