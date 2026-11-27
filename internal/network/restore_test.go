package network

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSnapshotPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "network-snapshot.json")
	want := Snapshot{
		CapturedAt: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC),
		Interfaces: []InterfaceSnapshot{{Alias: "Wi-Fi", Index: 12, IPv6DefaultGateways: []string{"2001:db8::1"}}},
		Sharing:    []SharingSnapshot{{Name: "Wi-Fi", Guid: "{guid}", Enabled: true, Kind: "public"}},
		Hotspot:    &HotspotMeta{Applied: true, Path: "ics"},
	}
	if err := SaveSnapshot(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	got.SavedAt = time.Time{} // SavedAt is stamped by SaveSnapshot, not part of the fixture
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot mismatch: got %+v want %+v", got, want)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatalf("snapshot file is missing: %v", err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot permissions are not private: %v", info.Mode().Perm())
	}
}

// A corrupt snapshot (0 bytes, truncated, hand-edited garbage) must surface
// as ErrCorruptSnapshot so callers self-heal instead of letting the file
// block every proxy start forever.
func TestLoadSnapshotCorrupt(t *testing.T) {
	cases := map[string]string{
		"empty":      "",
		"whitespace": "   \n",
		"truncated":  `{"capturedAt": "2026-10-15T08:00:00Z"`,
		"wrong type": "[]",
		"not json":   "hello",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "network-snapshot.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadSnapshot(path)
			if !errors.Is(err, ErrCorruptSnapshot) {
				t.Fatalf("err = %v, want ErrCorruptSnapshot", err)
			}
		})
	}
	// A missing file must stay distinguishable: ErrNotExist, not corrupt.
	_, err := LoadSnapshot(filepath.Join(t.TempDir(), "absent.json"))
	if !errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrCorruptSnapshot) {
		t.Fatalf("missing file err = %v, want ErrNotExist only", err)
	}
}

// Saving must heal a previously corrupt file atomically: the result loads,
// and no temp file is left behind.
func TestSaveSnapshotHealsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "network-snapshot.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	want := Snapshot{CapturedAt: time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)}
	if err := SaveSnapshot(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSnapshot(path)
	if err != nil {
		t.Fatalf("save did not heal corrupt file: %v", err)
	}
	got.SavedAt = time.Time{} // SavedAt is stamped by SaveSnapshot, not part of the fixture
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot mismatch: got %+v want %+v", got, want)
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp file left behind: %v", err)
	}
}

// 有 Sharing、没有 Hotspot 元数据 = 上次死在「ICS 已切完、元数据没写回」的
// 窗口里。这种快照必须补一次角色还原，否则热点共享会一直指向已消失的 TUN。
// 有元数据（含 pending 占位）或根本没有 Sharing 时不该多跑一次外部进程。
func TestNeedsSharingOnlyRestore(t *testing.T) {
	sharing := []SharingSnapshot{{Name: "Wi-Fi", Enabled: true, Kind: "public"}}
	cases := map[string]struct {
		snapshot Snapshot
		want     bool
	}{
		"sharing without meta":  {Snapshot{Sharing: sharing}, true},
		"sharing with ics meta": {Snapshot{Sharing: sharing, Hotspot: &HotspotMeta{Applied: true, Path: "ics"}}, false},
		"sharing with pending":  {Snapshot{Sharing: sharing, Hotspot: &HotspotMeta{Applied: true, Path: HotspotPathPending}}, false},
		"no sharing, no meta":   {Snapshot{}, false},
		"meta without sharing":  {Snapshot{Hotspot: &HotspotMeta{Applied: true, Path: "winrt"}}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := needsSharingOnlyRestore(c.snapshot); got != c.want {
				t.Fatalf("needsSharingOnlyRestore = %v, want %v", got, c.want)
			}
		})
	}
}

// 旧版本写下的快照带着 DNS 字段：新代码必须照常读出热点/共享基线，未知的
// DNS 字段直接忽略，不能因此被判成损坏。旧文件里的 DNS 也不再有任何还原
// 效果 —— 这正是「不再写系统 DNS」的意思。
func TestLegacySnapshotWithDNSFieldsStillLoads(t *testing.T) {
	const legacy = `{
  "capturedAt": "2026-11-22T00:00:00Z",
  "interfaces": [{
    "alias": "Wi-Fi",
    "index": 12,
    "ipv4DnsServers": ["192.0.2.53"],
    "ipv4DnsSource": "static",
    "ipv6DnsServers": ["2001:db8::1"]
  }],
  "sharing": [{"name": "Wi-Fi", "guid": "{guid}", "enabled": true, "kind": "public"}],
  "hotspot": {"applied": true, "path": "winrt", "originalProfile": "USTB-Student"}
}`
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(legacy), &snapshot); err != nil {
		t.Fatalf("legacy snapshot must stay readable: %v", err)
	}
	if len(snapshot.Interfaces) != 1 || snapshot.Interfaces[0].Alias != "Wi-Fi" || snapshot.Interfaces[0].Index != 12 {
		t.Fatalf("legacy interface fields must survive: %+v", snapshot.Interfaces)
	}
	if len(snapshot.Sharing) != 1 {
		t.Fatalf("legacy sharing baseline must survive: %+v", snapshot.Sharing)
	}
	if snapshot.Hotspot == nil || snapshot.Hotspot.OriginalProfile != "USTB-Student" {
		t.Fatalf("legacy hotspot metadata must survive: %+v", snapshot.Hotspot)
	}
}

// 快照只记录本程序会改的状态：网卡 DNS 不能被写进基线 —— 一旦写进去，就
// 迟早会有人（或下一版代码）去"还原"它，而免流全程不碰系统 DNS。
func TestSnapshotDoesNotPersistDNS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "network-snapshot.json")
	snapshot := Snapshot{Interfaces: []InterfaceSnapshot{{Alias: "Wi-Fi", IPv4DefaultGateways: []string{"192.0.2.1"}}}}
	if err := SaveSnapshot(path, snapshot); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(data)), "dns") {
		t.Fatalf("network snapshot must not carry DNS state: %s", data)
	}
}

// 并发写 + 读者读到的一直是完整快照（回归：固定 .tmp 名曾被并发写互相截断，
// 读者观测到 130 次解析失败）。
func TestSaveSnapshotConcurrentReadersSeeCompleteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network-snapshot.json")
	makeSnapshot := func(seed int) Snapshot {
		snapshot := Snapshot{CapturedAt: time.Now()}
		for i := 0; i < 400; i++ {
			snapshot.Interfaces = append(snapshot.Interfaces, InterfaceSnapshot{Alias: fmt.Sprintf("a-%d-%d", seed, i), Index: i})
		}
		return snapshot
	}
	var writers sync.WaitGroup
	for w := 0; w < 8; w++ {
		payload := makeSnapshot(w)
		writers.Add(1)
		go func(s Snapshot) {
			defer writers.Done()
			for i := 0; i < 50; i++ {
				_ = SaveSnapshot(path, s)
			}
		}(payload)
	}
	stop := make(chan struct{})
	var readers sync.WaitGroup
	corrupt := 0
	var mu sync.Mutex
	for r := 0; r < 4; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := LoadSnapshot(path); errors.Is(err, ErrCorruptSnapshot) {
					// A half-written file must never be observable; a transient
					// "file in use" during the rename swap is tolerated (it is
					// not corruption and the next read succeeds).
					mu.Lock()
					corrupt++
					mu.Unlock()
				}
			}
		}()
	}
	writers.Wait()
	close(stop)
	readers.Wait()
	if corrupt != 0 {
		t.Fatalf("readers saw %d corrupt (half-written) snapshots during concurrent saves", corrupt)
	}
}

func TestShouldRecoverTethering(t *testing.T) {
	now := time.Now()
	meta := &HotspotMeta{Applied: true, Path: "winrt"}
	if !shouldRecoverTethering(meta, now.Add(-time.Minute), now) {
		t.Fatal("a fresh winrt+applied snapshot must be allowed to recover")
	}
	if shouldRecoverTethering(meta, now.Add(-2*time.Hour), now) {
		t.Fatal("a stale snapshot must not start tethering")
	}
	if shouldRecoverTethering(meta, time.Time{}, now) {
		t.Fatal("a zero SavedAt (pre-upgrade) snapshot must not start tethering")
	}
	if shouldRecoverTethering(nil, now, now) {
		t.Fatal("nil meta must not recover")
	}
	if shouldRecoverTethering(&HotspotMeta{Applied: true, Path: "ics"}, now, now) {
		t.Fatal("a non-winrt path must not recover")
	}
}
