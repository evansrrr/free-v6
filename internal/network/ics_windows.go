//go:build windows

package network

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// runPowerShell 执行一段 PowerShell 脚本并返回合并输出。
// 脚本以非零退出码或异常结束时错误里带上输出，便于日志直接展示。
func runPowerShell(ctx context.Context, script string) ([]byte, error) {
	command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

// EnsureHotspot5GHz 探测活动 Wi-Fi 网卡的频段偏好，存在 5GHz 选项时幂等地
// 设为「优先 5GHz」（持久化到网卡驱动配置）。不支持/无网卡时返回
// Supported=false 或 error，调用方据此降级，不阻断热点共享本身。
func EnsureHotspot5GHz(ctx context.Context) (HotspotBandResult, error) {
	output, err := runPowerShell(ctx, PowerShellEnsure5GHzCommand)
	if err != nil {
		return HotspotBandResult{}, fmt.Errorf("ensure 5GHz hotspot: %w", err)
	}
	var result HotspotBandResult
	if err := jsonUnmarshalFlexible(output, &result); err != nil {
		return HotspotBandResult{}, fmt.Errorf("decode 5GHz hotspot result: %w", err)
	}
	return result, nil
}

// SwitchWinRT 新 WDI 兜底路径：把移动热点上游改绑到 FreeV6TUN。
// 返回切换前的原上游连接配置名（供停止时还原）。
func SwitchWinRT(ctx context.Context) (string, error) {
	output, err := runPowerShell(ctx, BuildWinRTSwitchScript())
	if err != nil {
		if strings.Contains(string(output), "移动热点未处于运行状态") {
			return "", ErrHotspotNotRunning
		}
		return "", fmt.Errorf("bind hotspot to TUN via WinRT: %w", err)
	}
	var result struct {
		OriginalProfile string `json:"originalProfile"`
	}
	if err := jsonUnmarshalFlexible(output, &result); err != nil {
		return "", fmt.Errorf("decode WinRT switch result: %w", err)
	}
	return result.OriginalProfile, nil
}

// RestoreWinRT 停掉绑在 TUN 上的 tethering，并按记录的原上游重新拉起热点。
// reason == "not-running" 表示热点本就关闭，不算错误。
func RestoreWinRT(ctx context.Context, originalProfile string) error {
	output, err := runPowerShell(ctx, BuildWinRTRestoreScript(originalProfile))
	if err != nil {
		return fmt.Errorf("restore hotspot tethering: %w", err)
	}
	var result WinRTRestoreResult
	if err := jsonUnmarshalFlexible(output, &result); err != nil {
		return fmt.Errorf("decode WinRT restore result: %w", err)
	}
	if result.Reason == "not-running" || result.Restarted {
		return nil
	}
	return fmt.Errorf("restore hotspot tethering: %s", result.Reason)
}
