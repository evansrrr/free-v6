package network

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// RestoreFunc 是网络还原入口，形状与 RestoreLeftoverSnapshot 一致。用参数
// 传入而不是直接调用，是为了让自愈流程可以在不碰真实热点的前提下测试。
type RestoreFunc func(context.Context, Snapshot) error

// HealResult 是启动前自愈的结果。
type HealResult struct {
	// Preserved 非 nil：上次会话的基线还在，且这次没能还原干净。调用方必须
	// 用它（而不是刚抓到的现状）作为本次会话的基线 —— 见 AdoptBaseline。
	Preserved *Snapshot
	// Warning 非空：需要告知用户的自愈结论（快照损坏 / 还原失败）。
	Warning string
	// Failed 为真表示 Warning 是一个失败，界面按错误样式记录。
	Failed bool
}

// HealLeftoverSnapshot 按上次会话残留的快照还原，处理完删除文件。
//
// 读不出来的快照（0 字节/截断/被外部改坏、权限异常）只告警不阻断：曾有线上
// 问题是一个损坏的 network-snapshot.json 把启动和停止同时永久卡死。
//
// 还原失败时把旧基线原样交回调用方：基线本身可能还是对的（外部改掉热点/共享
// 配置导致的永久失败），留着下次启动再试；用本次现状覆盖它，等于把没还原掉的
// 切换状态固化成正式配置，那之后就再没有干净基线可回。
func HealLeftoverSnapshot(ctx context.Context, path string, restore RestoreFunc) HealResult {
	old, err := LoadSnapshot(path)
	switch {
	case err == nil:
		if err := restore(ctx, old); err != nil {
			return HealResult{
				Preserved: &old,
				Failed:    true,
				Warning:   fmt.Sprintf("上次会话的网络基线还原失败（已保留该基线）：%v", err),
			}
		}
		_ = os.Remove(path)
		return HealResult{}
	case errors.Is(err, ErrCorruptSnapshot):
		_ = os.Remove(path)
		return HealResult{Warning: "上次会话的网络快照已损坏，已删除并重新采集基线"}
	case errors.Is(err, os.ErrNotExist):
		return HealResult{}
	default:
		return HealResult{Warning: fmt.Sprintf("上次会话的网络快照无法读取，已跳过还原：%v", err)}
	}
}

// AdoptBaseline 返回本次会话要落盘的基线：正常就是刚抓到的 fresh；自愈没能
// 完整还原时以 preserved 为准，并用 fresh 补齐它缺的维度（网关排障信息、
// ICS 共享基线）。
//
// 这是「自愈失败禁止覆盖基线」的落点：把还原失败的现状重新抓成基线，等于让
// 残留的切换状态从此变成正式配置，那之后再没有干净基线可回。
func AdoptBaseline(fresh Snapshot, preserved *Snapshot) Snapshot {
	if preserved == nil {
		return fresh
	}
	baseline := *preserved
	// Interfaces 只用于排障日志，缺了就补一份本次的，避免日志里看不到网关。
	if len(baseline.Interfaces) == 0 {
		baseline.Interfaces = fresh.Interfaces
	}
	if len(baseline.Sharing) == 0 {
		baseline.Sharing = fresh.Sharing
	}
	return baseline
}
