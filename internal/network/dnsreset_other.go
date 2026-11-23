//go:build !windows

package network

import "context"

// ResetDNSToDHCP 只在 Windows 上有意义（其他平台没有 netsh / 网卡注册表）。
func ResetDNSToDHCP(context.Context) (changed []DNSResetChange, failures []string) {
	return nil, []string{ErrWindowsOnly.Error()}
}
