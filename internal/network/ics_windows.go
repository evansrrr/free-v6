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

// CaptureSharing 抓取当前 ICS 共享状态基线（切换前的原状）。
func CaptureSharing(ctx context.Context) ([]SharingSnapshot, error) {
	output, err := runPowerShell(ctx, PowerShellSharingCaptureCommand)
	if err != nil {
		return nil, fmt.Errorf("capture ICS sharing state: %w", err)
	}
	return ParsePowerShellSharing(output)
}

// DetectHotspot 探测移动热点是否在运行，并返回经典路径所需的私有侧网卡名。
func DetectHotspot(ctx context.Context) (HotspotStatus, error) {
	output, err := runPowerShell(ctx, BuildHotspotDetectCommand())
	if err != nil {
		return HotspotStatus{}, fmt.Errorf("detect hotspot: %w", err)
	}
	return ParseHotspotStatus(output)
}

// SwitchICS 经典路径：FreeV6TUN=PUBLIC、热点私有侧=PRIVATE。
// 脚本内部已完成重试与状态核验，返回结构化结果；仅在执行/解析层面出错时
// 返回 error，业务失败在 result.Ok/result.Error 里。
func SwitchICS(ctx context.Context, privateAlias string) (ICSSwitchResult, error) {
	var result ICSSwitchResult
	output, err := runPowerShell(ctx, BuildICSSwitchScript(privateAlias))
	if err != nil {
		return result, fmt.Errorf("switch ICS topology: %w", err)
	}
	if err := jsonUnmarshalFlexible(output, &result); err != nil {
		return result, fmt.Errorf("decode ICS switch result: %w", err)
	}
	return result, nil
}

// SwitchWinRT 新 WDI 兜底路径：把移动热点上游改绑到 FreeV6TUN。
// 脚本按 TetheringState 轮询判定成功；返回原上游连接配置名。
func SwitchWinRT(ctx context.Context) (WinRTSwitchResult, error) {
	var result WinRTSwitchResult
	output, err := runPowerShell(ctx, BuildWinRTSwitchScript())
	if err != nil {
		return result, fmt.Errorf("bind hotspot to TUN via WinRT: %w", err)
	}
	if err := jsonUnmarshalFlexible(output, &result); err != nil {
		return result, fmt.Errorf("decode WinRT switch result: %w", err)
	}
	return result, nil
}

// VerifyICSApplied 独立复核经典拓扑是否真的生效（不信任切换脚本的自报）：
// 重新抓共享状态，检查 TUN=public 且热点私有侧=private。
// 用于脚本报错后的假阴性挽救 —— 实测出现过脚本抛错而状态已切换成功。
func VerifyICSApplied(ctx context.Context, privateAlias string) (bool, error) {
	sharing, err := CaptureSharing(ctx)
	if err != nil {
		return false, err
	}
	return ICSApplied(sharing, privateAlias), nil
}

// RestoreICSSharing 把 ICS 共享状态还原到基线；单连接失败会汇总在错误里。
func RestoreICSSharing(ctx context.Context, snapshot Snapshot) error {
	script := BuildICSRestoreScript(snapshot.Sharing, TunDeviceName)
	output, err := runPowerShell(ctx, script)
	if err != nil {
		return fmt.Errorf("restore ICS sharing: %w", err)
	}
	var result struct {
		Errors []string `json:"errors"`
	}
	if err := jsonUnmarshalFlexible(output, &result); err != nil {
		return fmt.Errorf("decode ICS restore result: %w", err)
	}
	if len(result.Errors) > 0 {
		return fmt.Errorf("restore ICS sharing: %s", strings.Join(result.Errors, "; "))
	}
	return nil
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
