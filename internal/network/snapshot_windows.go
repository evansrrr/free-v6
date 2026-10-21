//go:build windows

package network

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func CaptureSnapshot(ctx context.Context) (Snapshot, error) {
	// Get-NetIPConfiguration 偶发对状态抖动/半移除网卡抛终止性错误，
	// PowerShell 直接 exit 1（重启免流时旧 FreeV6TUN 正在消失、热点/虚拟
	// 网卡热插拔都会触发）。带退避重试一次吸收这类瞬态；stderr 并入错误
	// 信息，否则 exit status 1 完全无从诊断。
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Snapshot{}, fmt.Errorf("capture Windows network snapshot: %w", ctx.Err())
			case <-time.After(400 * time.Millisecond):
			}
		}
		snapshot, err := captureSnapshotOnce(ctx)
		if err == nil {
			return snapshot, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return Snapshot{}, lastErr
}

func captureSnapshotOnce(ctx context.Context) (Snapshot, error) {
	command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", PowerShellSnapshotCommand)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		details := strings.TrimSpace(stderr.String())
		if details == "" {
			details = err.Error()
		} else if len(details) > 400 {
			details = details[:400]
		}
		return Snapshot{}, fmt.Errorf("capture Windows network snapshot: %v (powershell: %s)", err, details)
	}
	snapshot, err := ParsePowerShellSnapshot(stdout.Bytes())
	if err != nil {
		return Snapshot{}, err
	}
	// ICS 共享基线：非致命（COM 偶发失败不阻断启动）；热点切换前会再校验非空。
	if sharing, shareErr := CaptureSharing(ctx); shareErr == nil {
		snapshot.Sharing = sharing
	}
	return snapshot, nil
}
