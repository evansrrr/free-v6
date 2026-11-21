package network

import (
	"encoding/json"
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

// 还原命令必须按快照记录的来源生成：静态按值写回，DHCP 交还给 DHCP，
// 来源未知（旧快照）一律不动。回归：无条件 source=static 会把 DHCP 拿到的
// 地址固化进网卡，换网络后静态 DNS 指向旧网段，表现为「Wi-Fi 已连接但
// 打不开网页」。
func TestBuildDNSRestorePlans(t *testing.T) {
	plans := buildDNSRestorePlans(Snapshot{Interfaces: []InterfaceSnapshot{
		{
			Alias: "Wi-Fi",
			// 静态：set + add，保持多地址顺序
			IPv4DNSServers: []string{"192.0.2.53"}, IPv4DNSSource: DNSSourceStatic,
			IPv6DNSServers: []string{"2001:db8::1", "2001:db8::2"}, IPv6DNSSource: DNSSourceStatic,
		},
		{Alias: "Ethernet"}, // 无 DNS → 跳过，不生成 dhcp 还原命令
		// DHCP：只有一条 source=dhcp，绝不带 address=
		{Alias: "Wired", IPv4DNSServers: []string{"198.51.100.1"}, IPv4DNSSource: DNSSourceDHCP},
		// 旧快照没有来源字段 → 不动 DNS
		{Alias: "Legacy", IPv4DNSServers: []string{"203.0.113.53"}},
	}})
	// 三个接口：Ethernet 无 DNS、Legacy 来源未知，都被跳过。
	if len(plans) != 2 {
		t.Fatalf("expected 2 plans (Ethernet/Legacy skipped), got %d", len(plans))
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
		t.Fatalf("expected 1 Wired command (DHCP), got %d", len(wired))
	}
	want := []string{"interface", "ipv4", "set", "dnsservers", "name=Wired", "source=dhcp"}
	if !reflect.DeepEqual(wired[0].Args, want) {
		t.Fatalf("DHCP restore must only hand the interface back to DHCP: got %+v want %+v", wired[0].Args, want)
	}
}

// 旧版本写下的快照（没有 ipv4DnsSource / ipv6DnsSource）必须能正常读出，
// 且绝不能按 static 还原 —— 那正是「一次正常的启动→停止把 DHCP DNS 固化成
// 静态」的路径。兼容旧文件优先于「尽量还原」。
func TestLegacySnapshotWithoutDNSSourceRestoresNothing(t *testing.T) {
	const legacy = `{
  "capturedAt": "2026-11-22T00:00:00Z",
  "interfaces": [{"alias": "Wi-Fi", "index": 12, "ipv4DnsServers": ["192.0.2.53"], "ipv6DnsServers": ["2001:db8::1"]}]
}`
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(legacy), &snapshot); err != nil {
		t.Fatalf("legacy snapshot must stay readable: %v", err)
	}
	if len(snapshot.Interfaces) != 1 || snapshot.Interfaces[0].IPv4DNSSource != DNSSourceUnknown {
		t.Fatalf("legacy snapshot must decode with an unknown DNS source, got %+v", snapshot.Interfaces)
	}
	if plans := buildDNSRestorePlans(snapshot); len(plans) != 0 {
		t.Fatalf("a snapshot without a DNS source must not touch DNS, got %+v", plans)
	}
}

// 过期快照不能再当基线：它的 DNS/网关属于当时的网络，套到今天就是一次
// 人为解析故障。无时间戳（手改/极旧文件）同样不算可信基线；时间戳跑到
// 「未来」说明时钟被回拨过，年龄算不出来，也不能当新鲜基线用。
func TestSnapshotStaleAt(t *testing.T) {
	now := time.Date(2026, 11, 22, 1, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		snapshot Snapshot
		want     bool
	}{
		"one hour old":   {Snapshot{CapturedAt: now.Add(-time.Hour)}, false},
		"just under":     {Snapshot{CapturedAt: now.Add(-SnapshotMaxAge + time.Minute)}, false},
		"just over":      {Snapshot{CapturedAt: now.Add(-SnapshotMaxAge - time.Minute)}, true},
		"three days old": {Snapshot{CapturedAt: now.Add(-72 * time.Hour)}, true},
		"no timestamp":   {Snapshot{}, true},
		"small skew":     {Snapshot{CapturedAt: now.Add(time.Minute)}, false},
		"clock rolled back": {
			Snapshot{CapturedAt: now.Add(time.Hour)}, true,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := c.snapshot.StaleAt(now); got != c.want {
				t.Fatalf("StaleAt = %v, want %v", got, c.want)
			}
		})
	}
}

// DHCP 还原是「撤掉静态配置」的破坏性动作：只有当前值和快照一致（这期间没人
// 动过 DNS）才做；值变了就跳过，交给用户自己。静态还原不受影响 —— 写回的
// 本来就是用户自己的配置。
func TestPruneChangedDHCPDNS(t *testing.T) {
	snapshot := Snapshot{Interfaces: []InterfaceSnapshot{
		{Alias: "Wi-Fi", IPv4DNSServers: []string{"192.0.2.53", "192.0.2.54"}, IPv4DNSSource: DNSSourceDHCP},
		{Alias: "Ethernet", IPv4DNSServers: []string{"198.51.100.1"}, IPv4DNSSource: DNSSourceStatic},
		{Alias: "Wired", IPv6DNSServers: []string{"2001:db8::1"}, IPv6DNSSource: DNSSourceDHCP},
	}}
	current := func(alias string, ipv6 bool) ([]string, bool) {
		switch {
		case alias == "Wi-Fi" && !ipv6:
			// 顺序不同、内容相同 → 视为没动过
			return []string{"192.0.2.54", "192.0.2.53"}, true
		case alias == "Ethernet" && !ipv6:
			return []string{"203.0.113.9"}, true // 用户改了静态 DNS
		case alias == "Wired" && ipv6:
			return []string{"2001:db8::99"}, true // DHCP 换过地址
		}
		return nil, false
	}
	pruned := pruneChangedDHCPDNS(snapshot, current)
	if len(pruned.Interfaces[0].IPv4DNSServers) != 2 {
		t.Fatalf("unchanged DHCP DNS must keep its restore plan: %+v", pruned.Interfaces[0])
	}
	if len(pruned.Interfaces[1].IPv4DNSServers) != 1 || pruned.Interfaces[1].IPv4DNSSource != DNSSourceStatic {
		t.Fatalf("static restore must not be pruned by the current value: %+v", pruned.Interfaces[1])
	}
	if len(pruned.Interfaces[2].IPv6DNSServers) != 0 {
		t.Fatalf("changed DHCP DNS must be left to the user: %+v", pruned.Interfaces[2])
	}
	// 原快照不能被就地改动。
	if len(snapshot.Interfaces[2].IPv6DNSServers) != 1 {
		t.Fatalf("prune must not mutate the input snapshot: %+v", snapshot.Interfaces[2])
	}
	// 查不到接口（改名/刚删除）时保持原计划，让 netsh 自己报错进日志。
	if got := pruneChangedDHCPDNS(snapshot, func(string, bool) ([]string, bool) { return nil, false }); len(got.Interfaces[2].IPv6DNSServers) != 1 {
		t.Fatalf("unknown interface must keep the restore plan: %+v", got.Interfaces[2])
	}
	// 读不到现状（枚举失败）时整份保持不变。
	if got := pruneChangedDHCPDNS(snapshot, nil); len(got.Interfaces[2].IPv6DNSServers) != 1 {
		t.Fatalf("a nil lookup must leave the snapshot untouched: %+v", got.Interfaces[2])
	}
}

// 修复网卡 DNS 之后，残留基线里对应接口的 DNS 还原点必须一起清掉，否则下次
// 启动的自愈会把静态地址写回去；Sharing/Hotspot 仍要保留给热点还原用。
func TestDropDNSServers(t *testing.T) {
	snapshot := Snapshot{
		Interfaces: []InterfaceSnapshot{
			{Alias: "WiFi", IPv4DNSServers: []string{"202.204.52.89"}, IPv4DNSSource: DNSSourceStatic},
			{Alias: "Ethernet", IPv4DNSServers: []string{"198.51.100.1"}, IPv4DNSSource: DNSSourceStatic, IPv6DNSServers: []string{"2001:db8::1"}, IPv6DNSSource: DNSSourceStatic},
		},
		Sharing: []SharingSnapshot{{Name: "WiFi", Enabled: true, Kind: "public"}},
		Hotspot: &HotspotMeta{Applied: true, Path: "ics"},
	}
	snapshot.DropDNSServers([]string{"wifi"})
	if len(snapshot.Interfaces[0].IPv4DNSServers) != 0 || snapshot.Interfaces[0].IPv4DNSSource != DNSSourceUnknown {
		t.Fatalf("target interface DNS must be dropped: %+v", snapshot.Interfaces[0])
	}
	if len(snapshot.Interfaces[1].IPv4DNSServers) != 1 || len(snapshot.Interfaces[1].IPv6DNSServers) != 1 {
		t.Fatalf("other interfaces must keep their restore points: %+v", snapshot.Interfaces[1])
	}
	if len(snapshot.Sharing) != 1 || snapshot.Hotspot == nil {
		t.Fatalf("sharing/hotspot baseline must survive: %+v", snapshot)
	}
}
