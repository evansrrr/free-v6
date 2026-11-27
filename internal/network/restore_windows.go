//go:build windows

package network

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// RestoreHotspot 还原热点上游（WinRT/ICS 角色）。必须在 TUN 适配器仍在时
// 完成：FreeV6TUN 连接存在才能显式禁用其共享角色。失败返回错误，调用方
// 保留快照、下次启动免流前自愈还原。
//
// 停止流程只用这个入口：网卡 DNS 全程没被改过，没有需要还原的东西。
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

// RestoreLeftoverSnapshot 是启动前残留自愈用的还原入口：先按 Hotspot 元数据
// 还原，再补一种历史形状（见 needsSharingOnlyRestore）—— 上次进程死在
// 「ICS 已切完、元数据还没写回」的窗口里时，快照只有 Sharing，没有 Hotspot，
// RestoreHotspot 会直接跳过，角色就再没人还原。
//
// 与停止路径的唯一区别：winrt 记录在案而热点现在关着时，会按记录的原上游把
// 它重新拉起来。自愈面对的是「上次进程死在停与启之间」留下的现场 —— 这次切换
// 是我们发起的，热点本不该是关的；而停止免流必须尊重「用户自己关了热点」。
//
// 只给自愈用，不给停止用：正常停止时没有元数据就说明这次会话没切过，不该为
// 一个空操作多起一个进程（那是停止时延的主因之一）。
func RestoreLeftoverSnapshot(ctx context.Context, snapshot Snapshot) error {
	err := restoreLeftoverHotspot(ctx, snapshot)
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

// 自愈专用的两个可替换入口，便于用例在不碰真实热点/COM 的前提下断言「自愈
// 走的是会拉起关闭热点的恢复路径，而不是停止路径的严格还原」。
var (
	winRTRecover      = RecoverWinRT
	restoreICSSharing = RestoreICSSharing
)

// restoreLeftoverHotspot 与 RestoreHotspot 同构，只有 winrt 那一步换成
// RecoverWinRT（热点关着也拉起）。存在意义见 RestoreLeftoverSnapshot 的注释。
func restoreLeftoverHotspot(ctx context.Context, snapshot Snapshot) error {
	if snapshot.Hotspot == nil || !snapshot.Hotspot.Applied {
		return nil
	}
	var issues []string
	if snapshot.Hotspot.Path == "winrt" && shouldRecoverTethering(snapshot.Hotspot, snapshot.SavedAt, time.Now()) {
		if err := winRTRecover(ctx, snapshot.Hotspot.OriginalProfile); err != nil {
			issues = append(issues, err.Error())
		}
	}
	if err := restoreICSSharing(ctx, snapshot); err != nil {
		issues = append(issues, err.Error())
	}
	if len(issues) > 0 {
		return fmt.Errorf("%s", strings.Join(issues, "; "))
	}
	return nil
}
