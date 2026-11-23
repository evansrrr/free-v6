package network

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// RestoreFunc 是网络还原入口，形状与 RestoreSnapshot / RestoreHotspot 一致。
// 用参数传入而不是直接调用，是为了让自愈流程可以在不碰真实网络的前提下测试。
type RestoreFunc func(context.Context, Snapshot) error

// HealResult 是启动前自愈的结果。
type HealResult struct {
	// Preserved 非 nil：上次会话的基线还在，且这次没能还原干净。调用方必须
	// 用它（而不是刚抓到的现状）作为本次会话的基线 —— 见 AdoptBaseline。
	Preserved *Snapshot
	// Warning 非空：需要告知用户的自愈结论（快照过期被跳过 / 还原失败）。
	Warning string
	// Failed 为真表示 Warning 是一个失败，界面按错误样式记录。
	Failed bool
}

// HealLeftoverSnapshot 按上次会话残留的快照还原，处理完删除文件。
//
// 读不出来的快照（0 字节/截断/被外部改坏、权限异常）只告警不阻断：曾有线上
// 问题是一个损坏的 network-snapshot.json 把启动和停止同时永久卡死。
//
// 三种「不完整还原」必须区分开：
//   - 快照过期（> SnapshotMaxAge）：网卡 DNS/网关属于另一个网络，套上去就是
//     一次人为解析故障，跳过；但 Sharing/Hotspot 是我们自己切出来的状态，
//     与网络身份无关，继续单独还原（见 healStale）。
//   - 还原失败：基线本身可能还是对的（外部改掉热点/共享配置导致的永久失败），
//     原样留着，交回调用方继续当基线。
//   - 基线缺失/损坏：没有可还原的东西，返回空结果。
func HealLeftoverSnapshot(ctx context.Context, path string, restore RestoreFunc) HealResult {
	old, err := LoadSnapshot(path)
	switch {
	case err == nil:
		now := time.Now()
		if old.StaleAt(now) {
			return healStale(ctx, path, old, now, restore)
		}
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

// healStale 处理过期快照：DNS/网关只属于它被采集时的那个网络，不能套用；
// Sharing/Hotspot 是本程序自己切出来的状态，跟换没换网络无关 —— 上次会话
// 如果是开着热点共享被强杀，ICS 可能仍指向已经消失的 FreeV6TUN，必须继续
// 按基线还原，否则热点共享会一直坏下去。Interfaces 清空后同一个还原入口
// 自然只会走热点那一段，不会碰过期 DNS。
//
// 还原失败时把「只剩 Sharing/Hotspot」的基线交回调用方（Interfaces 已清空，
// 任何后续还原都不会再碰过期 DNS）。
func healStale(ctx context.Context, path string, old Snapshot, now time.Time, restore RestoreFunc) HealResult {
	residual := old
	residual.Interfaces = nil
	notice := staleNotice(old, now)
	if restore != nil {
		if err := restore(ctx, residual); err != nil {
			return HealResult{
				Preserved: &residual,
				Failed:    true,
				Warning:   fmt.Sprintf("上次会话的热点共享还原失败（快照已过期，网卡 DNS 不再还原）：%v", err),
			}
		}
	}
	_ = os.Remove(path)
	return HealResult{Warning: notice}
}

func staleNotice(snapshot Snapshot, now time.Time) string {
	const tail = "已跳过网卡 DNS 还原；若无法上网请重新连接一次网络"
	switch {
	case snapshot.CapturedAt.IsZero():
		return "上次会话的网络快照没有时间戳（来路不明），" + tail
	case snapshot.CapturedAt.After(now.Add(snapshotClockSkew)):
		// 时间戳在「未来」：不是真的过期，是系统时钟被调整过，年龄不可信。
		return fmt.Sprintf("上次会话的网络快照记录时间晚于当前系统时间（%s，系统时钟被调整过），%s",
			snapshot.CapturedAt.Local().Format("2006-01-02 15:04"), tail)
	default:
		return fmt.Sprintf("上次会话的网络快照记录于 %s（已超过 %d 小时），%s",
			snapshot.CapturedAt.Local().Format("2006-01-02 15:04"), int(SnapshotMaxAge.Hours()), tail)
	}
}

// AdoptBaseline 返回本次会话要落盘的基线：正常就是刚抓到的 fresh；自愈没能
// 完整还原时以 preserved 为准，并用 fresh 补齐它缺的那一维（过期快照的
// Interfaces 已被清空、旧基线没有 ICS 信息时用本次的）。
//
// 这是「自愈失败禁止覆盖基线」的落点：把还原失败的现状重新抓成基线，等于让
// 残留的切换状态从此变成正式配置，那之后再没有干净基线可回。
func AdoptBaseline(fresh Snapshot, preserved *Snapshot) Snapshot {
	if preserved == nil {
		return fresh
	}
	baseline := *preserved
	if len(baseline.Interfaces) == 0 {
		baseline.Interfaces = fresh.Interfaces
		// 接口基线换成了本次现状，时间戳必须跟着走：留着旧时间会让这份刚
		// 抓的基线在下一次启动就被判成过期快照，等于白抓一次。
		baseline.CapturedAt = fresh.CapturedAt
	}
	if len(baseline.Sharing) == 0 {
		baseline.Sharing = fresh.Sharing
	}
	return baseline
}
