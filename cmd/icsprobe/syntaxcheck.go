//go:build ignore

// 脚本语法校验器：go run cmd/icsprobe/syntaxcheck.go
// 用 PowerShell 语言解析器（不执行）验证所有生成脚本可解析，
// 拦截“Go 表达式泄漏进 PowerShell”一类的拼接错误。
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/yourname/freev6/internal/network"
)

func main() {
	sharing := []network.SharingSnapshot{
		{Name: "WLAN", Guid: "{AAA-111}", Enabled: true, Kind: "public"},
		{Name: "Local Area Connection* 3", Guid: "{BBB-222}", Enabled: true, Kind: "private"},
		{Name: "Ethernet", Guid: "{CCC-333}", Enabled: false, Kind: "public"},
	}
	scripts := map[string]string{
		"detect":      network.BuildHotspotDetectCommand(),
		"ics-switch":  network.BuildICSSwitchScript("Local Area Connection* 3"),
		"ics-restore": network.BuildICSRestoreScript(sharing, network.TunDeviceName),
		"winrt-switch":  network.BuildWinRTSwitchScript(),
		"winrt-restore": network.BuildWinRTRestoreScript("WLAN"),
		"sharing-capture": network.PowerShellSharingCaptureCommand,
	}
	dir, err := os.MkdirTemp("", "ics-syntax")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	failed := false
	for name, script := range scripts {
		path := filepath.Join(dir, name+".ps1")
		if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
			panic(err)
		}
		// 解析但不执行
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
			fmt.Sprintf("$e=$null;$t=$null;[System.Management.Automation.Language.Parser]::ParseFile('%s',[ref]$t,[ref]$e)|Out-Null; if($e.Count -gt 0){$e|%%{$_.Message}; exit 1}", path))
		out, err := cmd.CombinedOutput()
		if err != nil {
			failed = true
			fmt.Printf("FAIL %s:\n%s\n", name, out)
		} else {
			fmt.Printf("OK   %s\n", name)
		}
	}
	if failed {
		os.Exit(1)
	}
	fmt.Println("all scripts parse cleanly")
}
