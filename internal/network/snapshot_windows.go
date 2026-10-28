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
	// 网卡热插拔都会触发）。带退避重试吸收这类瞬态；stderr 并入错误
	// 信息，否则 exit status 1 完全无从诊断。
	// 首次启动（开机自启冷启动）网卡/网栈最不稳定，单次重试不够——三次
	// 退避（0/400ms/1000ms）在同一个 /proxy/start 调用内尽量吃下，避免
	// 前端把「第一次启动接口异常」透给自动运行重试循环。
	const snapshotAttempts = 3
	backoffs := [...]time.Duration{0, 400 * time.Millisecond, 1000 * time.Millisecond}
	var lastErr error
	for attempt := 0; attempt < snapshotAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Snapshot{}, fmt.Errorf("capture Windows network snapshot: %w", ctx.Err())
			case <-time.After(backoffs[attempt]):
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
