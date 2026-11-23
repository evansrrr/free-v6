//go:build windows

package network

import (
	"context"
	"testing"
)

// 自愈面对的是「上次进程死在停与启之间」留下的现场：热点是关的，但快照里
// 记着这次切换是我们做的。这时必须走会拉起热点的恢复路径 —— 走「热点关着
// 就什么都不做」的严格还原，用户的热点会一直关着，而他只知道设备上不了网。
// 停止路径必须保持严格（用户可能自己关了热点），两边的差别由这里守护。
func TestRestoreLeftoverSnapshotRecoversStoppedHotspot(t *testing.T) {
	origRecover, origICS := winRTRecover, restoreICSSharing
	defer func() { winRTRecover, restoreICSSharing = origRecover, origICS }()
	var recovered string
	winRTRecover = func(_ context.Context, profile string) error {
		recovered = profile
		return nil
	}
	restoreICSSharing = func(context.Context, Snapshot) error { return nil }

	snapshot := Snapshot{Hotspot: &HotspotMeta{Applied: true, Path: "winrt", OriginalProfile: "USTB-Student 2"}}
	if err := RestoreLeftoverSnapshot(context.Background(), snapshot); err != nil {
		t.Fatalf("leftover restore must succeed: %v", err)
	}
	if recovered != "USTB-Student 2" {
		t.Fatalf("leftover heal must recover the hotspot to its original upstream, got %q", recovered)
	}

	// 没有 Hotspot 元数据（没切过）或路径是 ics 时不碰 tethering。
	recovered = ""
	if err := RestoreLeftoverSnapshot(context.Background(), Snapshot{}); err != nil {
		t.Fatalf("empty snapshot must be a no-op: %v", err)
	}
	if recovered != "" {
		t.Fatalf("a snapshot without hotspot metadata must not touch tethering, got %q", recovered)
	}
}
