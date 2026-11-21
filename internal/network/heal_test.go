package network

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 自愈必须永不阻断启动：缺失→无操作；损坏→删除重建；新鲜→还原后删除。
// 回归：一个空的 network-snapshot.json 曾让每次启动都 500（decode network
// snapshot: unexpected end of JSON input），用户只能手动删文件自救。
func TestHealLeftoverSnapshotNeverBlocksStart(t *testing.T) {
	restoreOK := func(context.Context, Snapshot) error { return nil }
	hotspotOK := func(context.Context, Snapshot) error { return nil }

	t.Run("missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state", "network-snapshot.json")
		got := HealLeftoverSnapshot(context.Background(), path, restoreOK, hotspotOK)
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
		got := HealLeftoverSnapshot(context.Background(), path, restoreOK, hotspotOK)
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
		restore := func(context.Context, Snapshot) error {
			called = true
			return nil
		}
		HealLeftoverSnapshot(context.Background(), path, restore, hotspotOK)
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
			Interfaces: []InterfaceSnapshot{{Alias: "Wi-Fi", IPv4DNSServers: []string{"192.0.2.53"}, IPv4DNSSource: DNSSourceDHCP}},
		}
		if err := SaveSnapshot(path, want); err != nil {
			t.Fatal(err)
		}
		got := HealLeftoverSnapshot(context.Background(), path, func(context.Context, Snapshot) error {
			return errors.New("ics restore failed")
		}, hotspotOK)
		if got.Preserved == nil {
			t.Fatal("a failed restore must hand the old baseline back to the caller")
		}
		if !got.Preserved.CapturedAt.Equal(want.CapturedAt) || len(got.Preserved.Interfaces) != 1 {
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

// 过期快照只丢掉网卡 DNS/网关，绝不能连 Sharing/Hotspot 一起丢：上次若是开着
// 热点共享被强杀，ICS 可能仍指向已经消失的 FreeV6TUN，必须继续按基线还原，
// 否则热点共享会一直坏下去。还原失败时保留的就只剩这两块（不带过期 DNS）。
func TestHealStaleSnapshotKeepsHotspotBaseline(t *testing.T) {
	sharing := []SharingSnapshot{{Name: "Wi-Fi", Guid: "{guid}", Enabled: true, Kind: "public"}}
	meta := &HotspotMeta{Applied: true, Path: "ics"}
	stale := Snapshot{
		CapturedAt: time.Now().Add(-SnapshotMaxAge - time.Hour),
		Interfaces: []InterfaceSnapshot{{Alias: "Wi-Fi", IPv4DNSServers: []string{"202.204.52.89"}, IPv4DNSSource: DNSSourceStatic}},
		Sharing:    sharing,
		Hotspot:    meta,
	}

	writeStale := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "state", "network-snapshot.json")
		if err := SaveSnapshot(path, stale); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("dns skipped, hotspot restored", func(t *testing.T) {
		path := writeStale(t)
		restored := false
		got := HealLeftoverSnapshot(context.Background(), path,
			func(context.Context, Snapshot) error {
				t.Fatal("a stale baseline must never be applied to interfaces")
				return nil
			},
			func(_ context.Context, snapshot Snapshot) error {
				restored = true
				if len(snapshot.Interfaces) != 0 {
					t.Fatalf("stale DNS must be stripped before the hotspot restore, got %+v", snapshot.Interfaces)
				}
				if len(snapshot.Sharing) != 1 || snapshot.Hotspot == nil {
					t.Fatalf("sharing/hotspot baseline must survive: %+v", snapshot)
				}
				return nil
			})
		if !restored {
			t.Fatal("hotspot state from the stale snapshot must still be restored")
		}
		if got.Preserved != nil || got.Failed || got.Warning == "" {
			t.Fatalf("stale snapshot must be a non-fatal notice, got %+v", got)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("fully healed stale snapshot must be removed, stat err = %v", err)
		}
	})

	t.Run("hotspot restore failure preserves residual baseline", func(t *testing.T) {
		path := writeStale(t)
		got := HealLeftoverSnapshot(context.Background(), path,
			func(context.Context, Snapshot) error { return nil },
			func(context.Context, Snapshot) error { return errors.New("ics restore failed") })
		if got.Preserved == nil || !got.Failed {
			t.Fatalf("failed hotspot restore must preserve the residual baseline, got %+v", got)
		}
		if len(got.Preserved.Interfaces) != 0 {
			t.Fatalf("residual baseline must not carry stale DNS, got %+v", got.Preserved.Interfaces)
		}
		if len(got.Preserved.Sharing) != 1 || got.Preserved.Hotspot == nil {
			t.Fatalf("residual baseline must keep sharing/hotspot: %+v", got.Preserved)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("failed hotspot restore must keep the baseline file: %v", err)
		}
	})
}

// 无时间戳的快照按来路不明处理，提示里不能出现 0001-01-01。
func TestHealSnapshotWithoutTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "network-snapshot.json")
	if err := SaveSnapshot(path, Snapshot{Interfaces: []InterfaceSnapshot{{Alias: "Wi-Fi", IPv4DNSServers: []string{"192.0.2.53"}, IPv4DNSSource: DNSSourceDHCP}}}); err != nil {
		t.Fatal(err)
	}
	got := HealLeftoverSnapshot(context.Background(), path,
		func(context.Context, Snapshot) error {
			t.Fatal("a snapshot without a timestamp must not be applied")
			return nil
		},
		func(context.Context, Snapshot) error { return nil })
	if got.Warning == "" || got.Failed {
		t.Fatalf("missing timestamp must be reported as a notice, got %+v", got)
	}
	if !strings.Contains(got.Warning, "没有时间戳") {
		t.Fatalf("warning %q must say the snapshot has no timestamp, not print a zero time", got.Warning)
	}
}

// 基线选择：自愈没能完整还原时以旧基线为准，缺的那一维用本次现状补齐。
func TestAdoptBaseline(t *testing.T) {
	fresh := Snapshot{
		CapturedAt: time.Now(),
		Interfaces: []InterfaceSnapshot{{Alias: "Wi-Fi"}},
		Sharing:    []SharingSnapshot{{Name: "Wi-Fi", Enabled: true, Kind: "public"}},
	}

	if got := AdoptBaseline(fresh, nil); !got.CapturedAt.Equal(fresh.CapturedAt) || len(got.Interfaces) != 1 {
		t.Fatalf("without a preserved baseline the fresh capture must win: %+v", got)
	}

	old := Snapshot{
		CapturedAt: time.Now().Add(-2 * time.Hour),
		Interfaces: []InterfaceSnapshot{{Alias: "Wi-Fi", IPv4DNSServers: []string{"192.0.2.53"}, IPv4DNSSource: DNSSourceStatic}},
	}
	got := AdoptBaseline(fresh, &old)
	if !got.CapturedAt.Equal(old.CapturedAt) || len(got.Interfaces) != 1 || got.Interfaces[0].IPv4DNSSource != DNSSourceStatic {
		t.Fatalf("a preserved baseline must not be overwritten by the live state: %+v", got)
	}
	if len(got.Sharing) != 1 {
		t.Fatalf("missing dimensions must be filled from the fresh capture: %+v", got)
	}

	// 过期快照的 Interfaces 已被清空：DNS 用本次现状。
	residual := Snapshot{CapturedAt: time.Now().Add(-48 * time.Hour), Sharing: old.Sharing}
	got = AdoptBaseline(fresh, &residual)
	if len(got.Interfaces) != 1 || got.Interfaces[0].Alias != "Wi-Fi" {
		t.Fatalf("a residual baseline must take interfaces from the fresh capture: %+v", got)
	}
	if len(got.Sharing) != 1 {
		t.Fatalf("sharing must be kept from the residual baseline: %+v", got)
	}
}
