package network

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 自愈必须永不阻断启动：缺失→无操作；损坏→删除重建；新鲜→还原后删除。
// 回归：一个空的 network-snapshot.json 曾让每次启动都 500（decode network
// snapshot: unexpected end of JSON input），用户只能手动删文件自救。
func TestHealLeftoverSnapshotNeverBlocksStart(t *testing.T) {
	restoreOK := func(context.Context, Snapshot) error { return nil }

	t.Run("missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state", "network-snapshot.json")
		got := HealLeftoverSnapshot(context.Background(), path, restoreOK)
		if got.Preserved != nil || got.Warning != "" || got.Failed {
			t.Fatalf("missing snapshot must be a no-op, got %+v", got)
		}
	})

	t.Run("corrupt is deleted", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state", "network-snapshot.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		got := HealLeftoverSnapshot(context.Background(), path, restoreOK)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("corrupt snapshot must be removed, stat err = %v", err)
		}
		if got.Warning == "" || got.Failed {
			t.Fatalf("corrupt snapshot must be reported without failing the start, got %+v", got)
		}
	})

	t.Run("valid is restored and removed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state", "network-snapshot.json")
		if err := SaveSnapshot(path, Snapshot{CapturedAt: time.Now().Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
		called := false
		HealLeftoverSnapshot(context.Background(), path, func(context.Context, Snapshot) error {
			called = true
			return nil
		})
		if !called {
			t.Fatal("a fresh snapshot must be restored before it is dropped")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("healed snapshot must be removed, stat err = %v", err)
		}
	})

	t.Run("failed restore preserves the baseline", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state", "network-snapshot.json")
		want := Snapshot{
			CapturedAt: time.Now().Add(-time.Hour),
			Sharing:    []SharingSnapshot{{Name: "Wi-Fi", Guid: "{guid}", Enabled: true, Kind: "public"}},
			Hotspot:    &HotspotMeta{Applied: true, Path: "ics"},
		}
		if err := SaveSnapshot(path, want); err != nil {
			t.Fatal(err)
		}
		got := HealLeftoverSnapshot(context.Background(), path, func(context.Context, Snapshot) error {
			return errors.New("ics restore failed")
		})
		if got.Preserved == nil {
			t.Fatal("a failed restore must hand the old baseline back to the caller")
		}
		if !got.Preserved.CapturedAt.Equal(want.CapturedAt) || len(got.Preserved.Sharing) != 1 || got.Preserved.Hotspot == nil {
			t.Fatalf("preserved baseline changed: %+v", got.Preserved)
		}
		if got.Warning == "" || !got.Failed {
			t.Fatalf("failed restore must be reported as a failure, got %+v", got)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("failed restore must keep the baseline file: %v", err)
		}
	})
}

// 基线选择：自愈没能完整还原时以旧基线为准，缺的维度用本次现状补齐。
// 关键是「本次现状不能覆盖旧基线」——否则没还原掉的切换状态会被固化成正式配置。
func TestAdoptBaseline(t *testing.T) {
	fresh := Snapshot{
		Interfaces: []InterfaceSnapshot{{Alias: "Wi-Fi"}},
		Sharing:    []SharingSnapshot{{Name: "Wi-Fi", Enabled: true, Kind: "public"}},
	}

	if got := AdoptBaseline(fresh, nil); len(got.Interfaces) != 1 || len(got.Sharing) != 1 {
		t.Fatalf("without a preserved baseline the fresh capture must win: %+v", got)
	}

	old := Snapshot{Hotspot: &HotspotMeta{Applied: true, Path: "winrt", OriginalProfile: "USTB-Student"}}
	got := AdoptBaseline(fresh, &old)
	if got.Hotspot == nil || got.Hotspot.OriginalProfile != "USTB-Student" {
		t.Fatalf("a preserved baseline must not be overwritten by the live state: %+v", got)
	}
	if len(got.Interfaces) != 1 || len(got.Sharing) != 1 {
		t.Fatalf("missing dimensions must be filled from the fresh capture: %+v", got)
	}
}
