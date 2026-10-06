//go:build windows

package network

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func RestoreSnapshot(ctx context.Context, snapshot Snapshot) error {
	// 热点已切换过：先还原 tethering/ICS 角色（此时 TUN 仍在，删除前还原
	// 才能命中 FreeV6TUN 连接），再还原 DNS。所有步骤都尝试执行，
	// 错误汇总返回，避免半还原状态被当成整体失败而无法推进。
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
