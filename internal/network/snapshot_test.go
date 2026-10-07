package network

import (
	"strings"
	"testing"
)

func TestParsePowerShellSnapshotArray(t *testing.T) {
	data := `[
	  {
	    "InterfaceAlias": "Wi-Fi",
	    "InterfaceIndex": 12,
	    "IPv4DefaultGateway": [{"NextHop": "192.0.2.1"}],
	    "IPv6DefaultGateway": [{"NextHop": "fe80::1"}],
	    "DNSServer": {"ServerAddresses": ["2400:3200::1", "2400:3200::1", "192.0.2.53"]}
	  },
	  {
	    "InterfaceAlias": "vEthernet (Default Switch)",
	    "InterfaceIndex": 42,
	    "IPv4DefaultGateway": null,
	    "IPv6DefaultGateway": null,
	    "DNSServer": null
	  }
	]`

	snapshot, err := ParsePowerShellSnapshot([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Interfaces) != 2 {
		t.Fatalf("expected 2 interfaces, got %d", len(snapshot.Interfaces))
	}
	wifi := snapshot.Interfaces[0]
	if wifi.Alias != "Wi-Fi" || wifi.Index != 12 {
		t.Fatalf("unexpected interface identity: %+v", wifi)
	}
	if strings.Join(wifi.IPv4DefaultGateways, ",") != "192.0.2.1" {
		t.Fatalf("unexpected IPv4 gateways: %+v", wifi.IPv4DefaultGateways)
	}
	if strings.Join(wifi.IPv6DefaultGateways, ",") != "fe80::1" {
		t.Fatalf("unexpected IPv6 gateways: %+v", wifi.IPv6DefaultGateways)
	}
	if strings.Join(wifi.IPv4DNSServers, ",") != "192.0.2.53" {
		t.Fatalf("unexpected IPv4 DNS servers: %+v", wifi.IPv4DNSServers)
	}
	if strings.Join(wifi.IPv6DNSServers, ",") != "2400:3200::1" {
		t.Fatalf("unexpected IPv6 DNS servers: %+v", wifi.IPv6DNSServers)
	}
}

func TestParsePowerShellSnapshotSingleObject(t *testing.T) {
	data := `{"InterfaceAlias":"Ethernet","InterfaceIndex":"7","DNSServer":{"ServerAddresses":["2001:4860:4860::8888"]}}`
	snapshot, err := ParsePowerShellSnapshot([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Interfaces) != 1 || snapshot.Interfaces[0].Index != 7 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

// 快照命令必须先切 UTF-8 输出：中文 Windows（GBK 代码页）上省略前缀会让
// 中文网卡名在管道里变 GBK 字节，JSON 解析成 U+FFFD 乱码，netsh 还原时
// 找不到网卡（线上故障：restore leftover network snapshot 卡死启动）。
func TestSnapshotCommandForcesUTF8Output(t *testing.T) {
	if !strings.HasPrefix(PowerShellSnapshotCommand, "[Console]::OutputEncoding=[System.Text.Encoding]::UTF8;") {
		t.Fatalf("snapshot command must force UTF-8 console output, got: %.80s", PowerShellSnapshotCommand)
	}
}

func TestParsePowerShellSnapshotRejectsInvalidJSON(t *testing.T) {
	if _, err := ParsePowerShellSnapshot([]byte("not-json")); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}
