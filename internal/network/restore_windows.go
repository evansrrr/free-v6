//go:build windows

package network

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func RestoreSnapshot(ctx context.Context, snapshot Snapshot) error {
	// 热点已切换过：先还原 tethering/ICS 角色（此时 TUN 仍在，删除前还原
	// 才能命中 FreeV6TUN 连接），再还原 DNS。ICS 部分按 GUID 匹配，失败
	// 返回错误（状态风险，调用方保留快照）；DNS 还原失败只记警告不阻断：
	// 网卡改名/删除、旧版 GBK 乱码快照等都会让 netsh 找不到名字，但本应用
	// 不改 DNS，失败无实质影响 —— 若阻断，故障机的损坏快照会在每次启动
	// 的自愈里永久卡死（已发生过的线上问题）。
	var issues []string
	if snapshot.Hotspot != nil && snapshot.Hotspot.Applied {
		if snapshot.Hotspot.Path == "winrt" {
			if err := RestoreWinRT(ctx, snapshot.Hotspot.OriginalProfile); err != nil {
				issues = append(issues, err.Error())
			}
		}
		if err := RestoreICSSharing(ctx, snapshot); err != nil {
			issues = append(issues, err.Error())
		}
	}
	var dnsFailures []string
	for _, restoreCommand := range BuildRestoreCommands(snapshot) {
		if output, err := exec.CommandContext(ctx, restoreCommand.Executable, restoreCommand.Args...).CombinedOutput(); err != nil {
			dnsFailures = append(dnsFailures, fmt.Sprintf("%q: %v (%s)", restoreCommand.Args, err, strings.TrimSpace(string(output))))
		}
	}
	if len(dnsFailures) > 0 {
		fmt.Fprintf(os.Stderr, "freev6: DNS restore warning (non-fatal): %s\n", strings.Join(dnsFailures, "; "))
	}
	if len(issues) > 0 {
		return fmt.Errorf("%s", strings.Join(issues, "; "))
	}
	return nil
}
