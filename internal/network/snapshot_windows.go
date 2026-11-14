//go:build windows

package network

import (
	"context"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// CaptureSnapshot builds the network baseline (per-interface DNS servers and
// default gateways) that stop/rollback restores afterwards.
//
// includeSharing controls whether the ICS sharing baseline (HNetShare COM
// enumeration) is also captured. It is only consumed by hotspot sharing, and
// enumerating it costs a powershell.exe round-trip, so callers that will not
// switch the hotspot pass false to skip it. The interface baseline itself is
// read in-process via GetAdaptersAddresses — no subprocess.
//
// Three attempts with backoff absorb the transient adapter churn seen on cold
// boot (old FreeV6TUN disappearing, virtual/hotspot adapters hot-plugging), the
// same resilience the PowerShell capture had; each attempt is now a ~ms syscall
// instead of a ~1s process spawn.
func CaptureSnapshot(ctx context.Context, includeSharing bool) (Snapshot, error) {
	const snapshotAttempts = 3
	backoffs := [...]time.Duration{0, 400 * time.Millisecond, 1000 * time.Millisecond}
	var lastErr error
	for attempt := 0; attempt < snapshotAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Snapshot{}, fmt.Errorf("capture Windows network snapshot: %w", ctx.Err())
			case <-time.After(backoffs[attempt]):
			}
		}
		snapshot, err := captureSnapshotOnce(ctx, includeSharing)
		if err == nil {
			return snapshot, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return Snapshot{}, lastErr
}

func captureSnapshotOnce(ctx context.Context, includeSharing bool) (Snapshot, error) {
	adapters, err := getAdaptersAddresses()
	if err != nil {
		return Snapshot{}, fmt.Errorf("capture Windows network snapshot: %w", err)
	}
	snapshot := buildSnapshot(adapters)
	// 空快照绝不能落盘：后续恢复会变成无操作，等于把改过的网络状态
	// 永久留在切换态。空结果视为本次抓取失败（可重试）。
	if len(snapshot.Interfaces) == 0 {
		return Snapshot{}, fmt.Errorf("network snapshot is empty")
	}
	// ICS 共享基线：非致命（COM 偶发失败不阻断启动）；热点切换前会再校验非空。
	if includeSharing {
		if sharing, shareErr := CaptureSharing(ctx); shareErr == nil {
			snapshot.Sharing = sharing
		}
	}
	return snapshot, nil
}

// getAdaptersAddresses enumerates adapters with the two-call pattern: grow the
// buffer until GetAdaptersAddresses stops returning ERROR_BUFFER_OVERFLOW.
// GAA_FLAG_INCLUDE_GATEWAYS makes each adapter carry its default gateway list,
// so a single syscall replaces the old Get-NetIPConfiguration PowerShell run.
func getAdaptersAddresses() (*windows.IpAdapterAddresses, error) {
	const flags = windows.GAA_FLAG_INCLUDE_GATEWAYS |
		windows.GAA_FLAG_SKIP_ANYCAST |
		windows.GAA_FLAG_SKIP_MULTICAST
	var size uint32 = 15 << 10
	for {
		buffer := make([]byte, size)
		adapters := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buffer[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, adapters, &size)
		switch err {
		case nil:
			return adapters, nil
		case windows.ERROR_BUFFER_OVERFLOW:
			continue
		case windows.ERROR_NO_DATA:
			// No adapters at all — surface as an empty snapshot upstream.
			return nil, nil
		default:
			return nil, err
		}
	}
}

// buildSnapshot walks the IpAdapterAddresses linked list into a Snapshot.
// Pure (no syscalls, no context) so it is unit-testable against a hand-built
// adapter list.
func buildSnapshot(adapters *windows.IpAdapterAddresses) Snapshot {
	snapshot := Snapshot{CapturedAt: time.Now()}
	for adapter := adapters; adapter != nil; adapter = adapter.Next {
		alias := windows.UTF16PtrToString(adapter.FriendlyName)
		if alias == "" {
			continue
		}
		iface := InterfaceSnapshot{
			Alias: alias,
			Index: int(adapter.IfIndex),
		}
		for dns := adapter.FirstDnsServerAddress; dns != nil; dns = dns.Next {
			ip := dns.Address.IP()
			if ip == nil {
				continue
			}
			if ipv4 := ip.To4(); ipv4 != nil {
				iface.IPv4DNSServers = append(iface.IPv4DNSServers, ipv4.String())
			} else {
				iface.IPv6DNSServers = append(iface.IPv6DNSServers, ip.String())
			}
		}
		for gateway := adapter.FirstGatewayAddress; gateway != nil; gateway = gateway.Next {
			ip := gateway.Address.IP()
			if ip == nil {
				continue
			}
			if ipv4 := ip.To4(); ipv4 != nil {
				iface.IPv4DefaultGateways = append(iface.IPv4DefaultGateways, ipv4.String())
			} else {
				iface.IPv6DefaultGateways = append(iface.IPv6DefaultGateways, ip.String())
			}
		}
		// Windows 可能重复列出同一服务器；保留顺序、去重，与旧 PowerShell
		// 解析的 uniqueStrings 语义一致，避免还原时 netsh 重复添加。
		iface.IPv4DNSServers = dedupe(iface.IPv4DNSServers)
		iface.IPv6DNSServers = dedupe(iface.IPv6DNSServers)
		iface.IPv4DefaultGateways = dedupe(iface.IPv4DefaultGateways)
		iface.IPv6DefaultGateways = dedupe(iface.IPv6DefaultGateways)
		snapshot.Interfaces = append(snapshot.Interfaces, iface)
	}
	return snapshot
}

// dedupe removes consecutive-duplicate-free duplicates while preserving order
// (first occurrence wins), matching the old uniqueStrings behavior.
func dedupe(values []string) []string {
	if len(values) < 2 {
		return values
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
