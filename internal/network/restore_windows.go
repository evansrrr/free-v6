//go:build windows

package network

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func RestoreSnapshot(ctx context.Context, snapshot Snapshot) error {
	// 还原顺序：WinRT tethering → ICS 角色 → DNS（TUN 还在时才能命中
	// FreeV6TUN 连接）。ICS 还原只要基线非空就执行（Hotspot==nil 也跑）：
	// 切换失败可能留下脏 ICS 而元数据没记，只按 Hotspot 门控会漏还原；
	// 已在基线的连接 Set-Share 首查即过，代价仅一次 PowerShell 启动。
	// 所有步骤都尝试执行，错误汇总返回，避免半还原被当成整体失败而卡死停止。
	var issues []string
	if snapshot.Hotspot != nil && snapshot.Hotspot.Applied && snapshot.Hotspot.Path == "winrt" {
		if err := RestoreWinRT(ctx, snapshot.Hotspot.OriginalProfile); err != nil {
			issues = append(issues, err.Error())
		}
	}
	if len(snapshot.Sharing) > 0 {
		if err := RestoreICSSharing(ctx, snapshot); err != nil {
			issues = append(issues, err.Error())
		}
	}
	for _, restoreCommand := range BuildRestoreCommands(snapshot) {
		if output, err := exec.CommandContext(ctx, restoreCommand.Executable, restoreCommand.Args...).CombinedOutput(); err != nil {
			issues = append(issues, fmt.Sprintf("restore Windows DNS with %q: %v (%s)", restoreCommand.Args, err, output))
			break
		}
	}
	if len(issues) > 0 {
		return fmt.Errorf("%s", strings.Join(issues, "; "))
	}
	return nil
}
