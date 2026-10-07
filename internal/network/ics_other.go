//go:build !windows

package network

import "context"

// 非 Windows 平台：ICS/热点操作不可用，统一返回 ErrWindowsOnly，
// 调用方（helper）会把它记为热点共享失败并保持免流照常运行。

func CaptureSharing(context.Context) ([]SharingSnapshot, error) {
	return nil, ErrWindowsOnly
}

func DetectHotspot(context.Context) (HotspotStatus, error) {
	return HotspotStatus{}, ErrWindowsOnly
}

func SwitchICS(context.Context, string) (ICSSwitchResult, error) {
	return ICSSwitchResult{}, ErrWindowsOnly
}

func SwitchWinRT(context.Context) (WinRTSwitchResult, error) {
	return WinRTSwitchResult{}, ErrWindowsOnly
}

func VerifyICSApplied(context.Context, string) (bool, error) {
	return false, ErrWindowsOnly
}

func RestoreICSSharing(context.Context, Snapshot) error {
	return ErrWindowsOnly
}

func RestoreWinRT(context.Context, string) error {
	return ErrWindowsOnly
}
