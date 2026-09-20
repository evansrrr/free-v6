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

func TestParsePowerShellSnapshotRejectsInvalidJSON(t *testing.T) {
	if _, err := ParsePowerShellSnapshot([]byte("not-json")); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}
