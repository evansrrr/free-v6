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
// is mapped into the diagnostic part of a Snapshot: friendly name, index and
// the v4/v6 split of default gateways, skipping an adapter with no friendly
// name. DNS is deliberately absent — the proxy never writes system DNS.
func TestBuildSnapshotMapsAdapters(t *testing.T) {
	gw4 := fakeSockaddr4("192.0.2.1")
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
		IfIndex:             12,
		FriendlyName:        utf16Ptr("Wi-Fi"),
		FirstGatewayAddress: &windows.IpAdapterGatewayAddress{Address: gw4},
		Next:                nil,
	}
	// Second gateway (IPv6) chained after the first.
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
	if len(lb.IPv4DefaultGateways) != 0 || len(lb.IPv6DefaultGateways) != 0 {
		t.Fatalf("loopback should have no gateway, got %+v", lb)
	}

	w := snapshot.Interfaces[1]
	if w.Alias != "Wi-Fi" || w.Index != 12 {
		t.Fatalf("unexpected wifi identity: %+v", w)
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
