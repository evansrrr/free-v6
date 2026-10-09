//go:build windows

package network

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// 手写的多行 PowerShell 脚本（切换/还原）必须能通过 PS 解析器 —— 语法
// 错误只会在真机执行到那一刻才暴露（热点共享现场故障代价高），这里提前拦。
// 只解析不执行：真实执行会重启系统热点，测试绝不允许。
//
// ⚠️ 写 .ps1 必须带 UTF-8 BOM：Windows PowerShell 5.1 的 ParseFile 对
// 无 BOM 文件按系统 ANSI 代码页解码 —— 中文开发机(936/65001)能过，GitHub
// CI 英文 runner(1252) 会把中文注释/字符串解成乱码并连锁报引号错误
// （CI 实测：Unexpected token '¹ç½‘å…³...'）。BOM 让任何代码页都按 UTF-8。
// 真实运行不受影响：helper 走 -Command 命令行参数，无文件解码环节。
func TestWinRTPowerShellScriptsParse(t *testing.T) {
	const utf8BOM = "\xef\xbb\xbf"
	scripts := map[string]string{
		"switch":  BuildWinRTSwitchScript(),
		"restore": BuildWinRTRestoreScript("WLAN"),
	}
	for name, script := range scripts {
		file := filepath.Join(t.TempDir(), name+".ps1")
		if err := os.WriteFile(file, append([]byte(utf8BOM), script...), 0o600); err != nil {
			t.Fatal(err)
		}
		check := fmt.Sprintf(
			"$errs=$null; [void][System.Management.Automation.Language.Parser]::ParseFile('%s',[ref]$null,[ref]$errs); if ($errs.Count -gt 0) { $errs | ForEach-Object { $_.Message }; exit 1 }",
			file)
		output, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", check).CombinedOutput()
		if err != nil {
			t.Fatalf("%s script has PowerShell syntax errors: %v\n%s\n--- script ---\n%s", name, err, output, script)
		}
	}
}
