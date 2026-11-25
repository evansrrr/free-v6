//go:build windows

package network

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ipPrefixString 直接按 sockaddr 的 union 布局读地址：IPv4 在 Data[0]，
// IPv6 从 Data[1] 起（Data[0] 是 flowinfo）。这里用四个真实存在的 TUN
// 半段前缀守住偏移，写错一位就会把 8000::/1 读成别的地址，残留路由永远
// 匹配不上。
func TestIPPrefixString(t *testing.T) {
	cases := []struct {
		name   string
		family uint16
		bytes  []byte
		length uint8
		want   string
	}{
		{"ipv4 default", windows.AF_INET, []byte{0, 0, 0, 0}, 0, "0.0.0.0/0"},
		{"ipv4 low half", windows.AF_INET, []byte{0, 0, 0, 0}, 1, "0.0.0.0/1"},
		{"ipv4 high half", windows.AF_INET, []byte{128, 0, 0, 0}, 1, "128.0.0.0/1"},
		{"ipv6 default", windows.AF_INET6, make([]byte, 16), 0, "::/0"},
		{"ipv6 low half", windows.AF_INET6, make([]byte, 16), 1, "::/1"},
		{"ipv6 high half", windows.AF_INET6, append([]byte{0x80}, make([]byte, 15)...), 1, "8000::/1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prefix := windows.IpAddressPrefix{PrefixLength: c.length}
			prefix.Prefix.Family = c.family
			base := unsafe.Pointer(&prefix.Prefix.Data[0])
			if c.family == windows.AF_INET6 {
				base = unsafe.Pointer(&prefix.Prefix.Data[1])
			}
			for i, b := range c.bytes {
				*(*byte)(unsafe.Add(base, i)) = b
			}
			if got := ipPrefixString(prefix); got != c.want {
				t.Fatalf("ipPrefixString = %q, want %q", got, c.want)
			}
		})
	}
}
