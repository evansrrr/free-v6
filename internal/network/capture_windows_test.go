//go:build windows

package network

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 真实抓取一轮：所有网卡别名必须是有效 UTF-8（无 U+FFFD 替换字符）。
// 在代码页 936（GBK）的中文 Windows 上，若 PowerShellSnapshotCommand
// 忘了先切 UTF-8 输出，这里就会抓到乱码 —— 即本次线上故障的回归守护。
// 只读操作（Get-NetIPConfiguration + HNetShare 枚举），对系统无副作用。
func TestCaptureSnapshotAliasesAreValidUTF8(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	snapshot, err := CaptureSnapshot(ctx)
	if err != nil {
		t.Fatalf("capture snapshot: %v", err)
	}
	if len(snapshot.Interfaces) == 0 {
		t.Fatal("expected at least one interface")
	}
	for _, iface := range snapshot.Interfaces {
		if strings.ContainsRune(iface.Alias, '\uFFFD') {
			t.Fatalf("interface alias got mojibake (non-UTF-8 PowerShell output): %q", iface.Alias)
		}
	}
	for _, entry := range snapshot.Sharing {
		if strings.ContainsRune(entry.Name, '\uFFFD') {
			t.Fatalf("ICS sharing name got mojibake: %q", entry.Name)
		}
	}
}
