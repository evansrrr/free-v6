//go:build windows

package network

import (
	"context"
	"fmt"
	"os/exec"
)

func CaptureSnapshot(ctx context.Context) (Snapshot, error) {
	command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", PowerShellSnapshotCommand)
	output, err := command.Output()
	if err != nil {
		return Snapshot{}, fmt.Errorf("capture Windows network snapshot: %w", err)
	}
	snapshot, err := ParsePowerShellSnapshot(output)
	if err != nil {
		return Snapshot{}, err
	}
	// ICS 共享基线：非致命（COM 偶发失败不阻断启动）；热点切换前会再校验非空。
	if sharing, shareErr := CaptureSharing(ctx); shareErr == nil {
		snapshot.Sharing = sharing
	}
	return snapshot, nil
}
