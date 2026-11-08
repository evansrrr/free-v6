//go:build windows

package network

import (
	"net"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fakeSockaddr4 builds a SocketAddress carrying an IPv4 address, laying out the
// bytes the way GetAdaptersAddresses does (RawSockaddrInet4.Addr at offset 4).
func fakeSockaddr4(ip string) windows.SocketAddress {
	parsed := net.ParseIP(ip).To4()
	raw := &syscall.RawSockaddrAny{}
	raw.Addr.Family = syscall.AF_INET
	for i, b := range parsed {
		raw.Addr.Data[2+i] = int8(b)
	}
	return windows.SocketAddress{
		Sockaddr:       raw,
		SockaddrLength: int32(unsafe.Sizeof(syscall.RawSockaddrInet4{})),
	}
}

// fakeSockaddr6 builds a SocketAddress carrying an IPv6 address
// (RawSockaddrInet6.Addr at offset 8, which spans Addr.Data[6:14] and Pad[0:8]).
func fakeSockaddr6(ip string) windows.SocketAddress {
	parsed := net.ParseIP(ip).To16()
	raw := &syscall.RawSockaddrAny{}
	raw.Addr.Family = syscall.AF_INET6
	for i, b := range parsed {
		if i < 8 {
			raw.Addr.Data[6+i] = int8(b)
		} else {
			raw.Pad[i-8] = int8(b)
		}
	}
	return windows.SocketAddress{
		Sockaddr:       raw,
		SockaddrLength: int32(unsafe.Sizeof(syscall.RawSockaddrInet6{})),
	}
}

func utf16Ptr(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		panic(err)
	}
	return p
}

// TestBuildSnapshotMapsAdapters verifies the GetAdaptersAddresses linked list
// is mapped into the Snapshot shape: friendly name, index, and the v4/v6 split
// of DNS servers and default gateways — including empty slots for adapters with
// no DNS/gateway and skipping an adapter with no friendly name.
func TestBuildSnapshotMapsAdapters(t *testing.T) {
	dns4 := fakeSockaddr4("192.0.2.53")
	gw4 := fakeSockaddr4("192.0.2.1")
	dns6 := fakeSockaddr6("2400:3200::1")
	gw6 := fakeSockaddr6("fe80::1")

	loopback := &windows.IpAdapterAddresses{
		IfIndex:      1,
		FriendlyName: utf16Ptr("Loopback"),
	}

	unnamed := &windows.IpAdapterAddresses{
		IfIndex:      5,
		FriendlyName: nil,
	}

	wifi := &windows.IpAdapterAddresses{
		IfIndex:               12,
		FriendlyName:          utf16Ptr("Wi-Fi"),
		FirstDnsServerAddress: &windows.IpAdapterDnsServerAdapter{Address: dns4},
		FirstGatewayAddress:   &windows.IpAdapterGatewayAddress{Address: gw4},
		Next:                  nil,
	}
	// Second DNS server (IPv6) chained after the first; second gateway (IPv6).
	wifi.FirstDnsServerAddress.Next = &windows.IpAdapterDnsServerAdapter{Address: dns6}
	wifi.FirstGatewayAddress.Next = &windows.IpAdapterGatewayAddress{Address: gw6}

	unnamed.Next = wifi
	loopback.Next = unnamed

	snapshot := buildSnapshot(loopback)
	if len(snapshot.Interfaces) != 2 {
		t.Fatalf("expected 2 interfaces (unnamed skipped), got %d", len(snapshot.Interfaces))
	}

	lb := snapshot.Interfaces[0]
	if lb.Alias != "Loopback" || lb.Index != 1 {
		t.Fatalf("unexpected loopback identity: %+v", lb)
	}
	if len(lb.IPv4DNSServers) != 0 || len(lb.IPv6DNSServers) != 0 {
		t.Fatalf("loopback should have no DNS, got %+v", lb)
	}

	w := snapshot.Interfaces[1]
	if w.Alias != "Wi-Fi" || w.Index != 12 {
		t.Fatalf("unexpected wifi identity: %+v", w)
	}
	if got := joinStr(w.IPv4DNSServers); got != "192.0.2.53" {
		t.Fatalf("unexpected IPv4 DNS: %q", got)
	}
	if got := joinStr(w.IPv6DNSServers); got != "2400:3200::1" {
		t.Fatalf("unexpected IPv6 DNS: %q", got)
	}
	if got := joinStr(w.IPv4DefaultGateways); got != "192.0.2.1" {
		t.Fatalf("unexpected IPv4 gateway: %q", got)
	}
	if got := joinStr(w.IPv6DefaultGateways); got != "fe80::1" {
		t.Fatalf("unexpected IPv6 gateway: %q", got)
	}
}

func joinStr(values []string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}
