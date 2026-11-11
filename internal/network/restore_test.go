package network

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestSnapshotPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "network-snapshot.json")
	want := Snapshot{
		CapturedAt: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC),
		Interfaces: []InterfaceSnapshot{{Alias: "Wi-Fi", Index: 12, IPv6DNSServers: []string{"2001:db8::1"}}},
	}
	if err := SaveSnapshot(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
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
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot mismatch: got %+v want %+v", got, want)
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp file left behind: %v", err)
	}
}

func TestBuildDNSRestorePlans(t *testing.T) {
	plans := buildDNSRestorePlans(Snapshot{Interfaces: []InterfaceSnapshot{
		{Alias: "Wi-Fi", IPv4DNSServers: []string{"192.0.2.53"}, IPv6DNSServers: []string{"2001:db8::1", "2001:db8::2"}},
		{Alias: "Ethernet"}, // 无静态 DNS → 跳过，不生成 dhcp 还原命令
		{Alias: "Wired", IPv4DNSServers: []string{"198.51.100.1"}}, // 只有 IPv4 → 只还原 IPv4
	}})
	// 三个接口：Ethernet 无 DNS 被跳过，Wi-Fi 与 Wired 各一个 plan。
	if len(plans) != 2 {
		t.Fatalf("expected 2 plans (Ethernet skipped), got %d", len(plans))
	}
	wifi := plans[0]
	if len(wifi) != 3 {
		t.Fatalf("expected 3 Wi-Fi commands, got %d", len(wifi))
	}
	if wifi[0].Executable != "netsh" || wifi[0].Args[1] != "ipv4" || wifi[0].Args[4] != "name=Wi-Fi" || wifi[0].Args[6] != "address=192.0.2.53" {
		t.Fatalf("unexpected primary DNS restore command: %+v", wifi[0])
	}
	if wifi[1].Args[1] != "ipv6" || wifi[1].Args[2] != "set" || wifi[1].Args[6] != "address=2001:db8::1" {
		t.Fatalf("unexpected IPv6 primary DNS restore command: %+v", wifi[1])
	}
	if wifi[2].Args[2] != "add" || wifi[2].Args[5] != "address=2001:db8::2" {
		t.Fatalf("unexpected secondary DNS restore command: %+v", wifi[2])
	}
	wired := plans[1]
	if len(wired) != 1 {
		t.Fatalf("expected 1 Wired command (IPv4 only), got %d", len(wired))
	}
	if wired[0].Args[1] != "ipv4" || wired[0].Args[4] != "name=Wired" || wired[0].Args[6] != "address=198.51.100.1" {
		t.Fatalf("unexpected Wired restore command: %+v", wired[0])
	}
}
