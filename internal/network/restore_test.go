package network

import (
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

func TestBuildRestoreCommands(t *testing.T) {
	commands := BuildRestoreCommands(Snapshot{Interfaces: []InterfaceSnapshot{
		{Alias: "Wi-Fi", IPv4DNSServers: []string{"192.0.2.53"}, IPv6DNSServers: []string{"2001:db8::1", "2001:db8::2"}},
		{Alias: "Ethernet"},
	}})
	if len(commands) != 5 {
		t.Fatalf("expected 5 restore commands, got %d", len(commands))
	}
	if commands[0].Executable != "netsh" || commands[0].Args[1] != "ipv4" || commands[0].Args[4] != "name=Wi-Fi" || commands[0].Args[6] != "address=192.0.2.53" {
		t.Fatalf("unexpected primary DNS restore command: %+v", commands[0])
	}
	if commands[1].Args[1] != "ipv6" || commands[1].Args[2] != "set" || commands[1].Args[6] != "address=2001:db8::1" {
		t.Fatalf("unexpected IPv6 primary DNS restore command: %+v", commands[1])
	}
	if commands[2].Args[2] != "add" || commands[2].Args[5] != "address=2001:db8::2" {
		t.Fatalf("unexpected secondary DNS restore command: %+v", commands[1])
	}
	if commands[3].Args[5] != "source=dhcp" || commands[4].Args[5] != "source=dhcp" {
		t.Fatalf("expected DHCP restore commands: %+v", commands)
	}
}
