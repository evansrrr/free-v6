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

// RestoreLeftoverSnapshot 是启动前残留自愈用的还原入口：先做完整还原
// （RestoreSnapshot = 热点 + DNS），再补一种历史形状（见 needsSharingOnlyRestore）
// —— 上次进程死在「ICS 已切完、元数据还没写回」的窗口里时，快照只有 Sharing，
// 没有 Hotspot，RestoreHotspot 会直接跳过，角色就再没人还原。
//
// 只给自愈用，不给停止用：正常停止时没有元数据就说明这次会话没切过，不该为
// 一个空操作多起一个进程（那是停止时延的主因之一）。
func RestoreLeftoverSnapshot(ctx context.Context, snapshot Snapshot) error {
	err := RestoreSnapshot(ctx, snapshot)
	if !needsSharingOnlyRestore(snapshot) {
		return err
	}
	// 这个形状下 RestoreHotspot 本就空转（Hotspot 为 nil 直接返回），ICS 角色
	// 只会在这里还原一次。
	icsErr := RestoreICSSharing(ctx, snapshot)
	switch {
	case err == nil:
		return icsErr
	case icsErr == nil:
		return err
	default:
		return fmt.Errorf("%v; %w", err, icsErr)
	}
}

// restoreDNSCommand 执行一条 netsh 还原命令。抽成变量是为了让用例能在不碰
// 真实网卡的前提下断言「还原计划真的被执行了」—— 只断言「没返回错误」查不出
// 「一条命令都没跑」这种回归。
var restoreDNSCommand = func(ctx context.Context, executable string, args []string) ([]byte, error) {
	return exec.CommandContext(ctx, executable, args...).CombinedOutput()
}

// RestoreDNS 还原各接口 DNS。不依赖 TUN 适配器，放在 mihomo 退出之后执行
// 也安全；接口间并行、接口内保序（set dnsservers → add dnsservers）。失败
// 只记警告不阻断：网卡改名/删除、旧版 GBK 乱码快照都会让 netsh 找不到
// 名字，若当致命错误，故障机上损坏的旧快照会在每次启动自愈里永久卡死。
//
// 还原前先对一次现状：DHCP 还原会撤掉静态配置，只在这期间没人动过 DNS 时才做
// （见 pruneChangedDHCPDNS）。比对读不到现状时退回原计划。
func RestoreDNS(ctx context.Context, snapshot Snapshot) {
	plans := buildDNSRestorePlans(pruneChangedDHCPDNS(snapshot, currentDNSLookup()))
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
				if output, err := restoreDNSCommand(ctx, restoreCommand.Executable, restoreCommand.Args); err != nil {
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
