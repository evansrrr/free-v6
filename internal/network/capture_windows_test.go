//go:build windows

package network

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 真实抓取一轮：所有网卡别名与 ICS 共享名必须是有效 UTF-8（无 U+FFFD 替换
// 字符）。原生路径从 GetAdaptersAddresses 读 UTF-16 友好名、HNetShare 枚举
// 走 PowerShell（其输出已切 UTF-8），任何一环出乱码这里都会抓到。
// 只读操作，对系统无副作用。includeSharing=true 以覆盖共享基线路径。
func TestCaptureSnapshotAliasesAreValidUTF8(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	snapshot, err := CaptureSnapshot(ctx, true)
	if err != nil {
		t.Fatalf("capture snapshot: %v", err)
	}
	if len(snapshot.Interfaces) == 0 {
		t.Fatal("expected at least one interface")
	}
	for _, iface := range snapshot.Interfaces {
		if strings.ContainsRune(iface.Alias, '\uFFFD') {
			t.Fatalf("interface alias got mojibake (non-UTF-8 output): %q", iface.Alias)
		}
	}
	for _, entry := range snapshot.Sharing {
		if strings.ContainsRune(entry.Name, '\uFFFD') {
			t.Fatalf("ICS sharing name got mojibake: %q", entry.Name)
		}
	}
}
