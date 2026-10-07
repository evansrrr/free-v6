package network

import (
	"strings"
	"testing"
)

// ICSApplied 是脚本自报与独立复核共用的判据：TUN=public 且热点私有侧=private。
func TestICSApplied(t *testing.T) {
	base := []SharingSnapshot{
		{Name: TunDeviceName, Enabled: true, Kind: "public"},
		{Name: "Local Area Connection* 3", Enabled: true, Kind: "private"},
		{Name: "WLAN", Enabled: false, Kind: "public"},
	}
	if !ICSApplied(base, "Local Area Connection* 3") {
		t.Fatal("switched state must report applied")
	}
	// TUN 未共享 → 未生效
	off := []SharingSnapshot{
		{Name: TunDeviceName, Enabled: false, Kind: "public"},
		{Name: "Local Area Connection* 3", Enabled: true, Kind: "private"},
	}
	if ICSApplied(off, "Local Area Connection* 3") {
		t.Fatal("tun not shared must not report applied")
	}
	// 私有侧还是 public（切换半途）→ 未生效
	half := []SharingSnapshot{
		{Name: TunDeviceName, Enabled: true, Kind: "public"},
		{Name: "Local Area Connection* 3", Enabled: true, Kind: "public"},
	}
	if ICSApplied(half, "Local Area Connection* 3") {
		t.Fatal("private side still public must not report applied")
	}
	// 空别名（无独立私有侧网卡）→ 只看 TUN
	if !ICSApplied(base[:1], "") {
		t.Fatal("empty alias must only require tun public")
	}
	if ICSApplied(off[:1], "") {
		t.Fatal("empty alias with tun off must not report applied")
	}
}

// 结构化切换结果的解析（容错 PowerShell 噪声输出）。
func TestParseICSSwitchResult(t *testing.T) {
	var res ICSSwitchResult
	if err := jsonUnmarshalFlexible([]byte("WARNING: x\n{\"ok\":true,\"tunPublic\":true,\"privPrivate\":true,\"error\":\"\"}"), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Ok || !res.TunPublic || !res.PrivPrivate {
		t.Fatalf("unexpected: %+v", res)
	}
	res = ICSSwitchResult{}
	if err := jsonUnmarshalFlexible([]byte(`{"ok":false,"tunPublic":false,"privPrivate":false,"error":"TUN: An event was unable to invoke any of the subscribers"}`), &res); err != nil {
		t.Fatal(err)
	}
	if res.Ok || !strings.Contains(res.Error, "unable to invoke") {
		t.Fatalf("unexpected: %+v", res)
	}
	// WinRT 切换结果
	var wres WinRTSwitchResult
	if err := jsonUnmarshalFlexible([]byte(`{"originalProfile":"WLAN","started":true}`), &wres); err != nil {
		t.Fatal(err)
	}
	if !wres.Started || wres.OriginalProfile != "WLAN" {
		t.Fatalf("unexpected: %+v", wres)
	}
}

// 探测脚本必须输出 tunBound / physProfile（TunBound 快速路径与 winrt 还原依赖）。
func TestDetectCommandOutputsTunBinding(t *testing.T) {
	cmd := BuildHotspotDetectCommand()
	for _, expected := range []string{
		"tunBound=$tunBound",
		"physProfile=$phys",
		"'" + DefaultHotspotGateway + "'",
		"'" + TunDeviceName + "'",
	} {
		if !strings.Contains(cmd, expected) {
			t.Errorf("detect command missing %q", expected)
		}
	}
	// tunBound 判定必须保守：TUN 报 On 且另有 profile 报 Off（防全局语义误判）
	if !strings.Contains(cmd, "$states[$tunName] -eq 'On'") {
		t.Error("tunBound must require TUN state On")
	}
	status, err := ParseHotspotStatus([]byte(`{"active":true,"privateAlias":"x","privateIP":"192.168.137.1","tethering":true,"tunBound":true,"physProfile":"WLAN"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !status.TunBound || status.PhysicalProfile != "WLAN" {
		t.Fatalf("unexpected: %+v", status)
	}
}

// 还原脚本停止阶段也改为状态轮询（同一切换脚本的假失败回归）。
func TestWinRTRestoreUsesStatePolling(t *testing.T) {
	script := BuildWinRTRestoreScript("WLAN")
	if strings.Contains(script, "Wait-Op") || strings.Contains(script, "$op2.Status") {
		t.Fatal("restore must poll TetheringState, not async op.Status")
	}
	for _, expected := range []string{"function Wait-State", "stop-timeout", "TetheringState="} {
		if !strings.Contains(script, expected) {
			t.Errorf("restore script missing %q", expected)
		}
	}
}

// WinRT 切换脚本的三条安全约束（真机回归：停了热点没重启 → 断连不恢复）：
//  1. 停止确认失败不中止（不得出现“停不了就 throw”的硬门槛）；
//  2. 定位当前上游跳过 TUN，避免停掉目标本身；
//  3. 启动失败且停过热点 → 必须先回滚重启原上游再抛错，且回滚结果入错误消息。
func TestWinRTSwitchScriptSafetyInvariants(t *testing.T) {
	script := BuildWinRTSwitchScript()
	// 约束1：旧版回归点 —— 停止超时直接 throw，导致热点死掉
	if strings.Contains(script, "移动热点未处于运行状态或停止超时") {
		t.Fatal("stop-confirmation failure must not abort (it strands the hotspot off)")
	}
	// 约束2：定位上游时跳过 TUN 连接
	if !strings.Contains(script, "if ([string]$p.ProfileName -eq $tunName) { continue }") {
		t.Fatal("must skip TUN profile when locating the current upstream")
	}
	// 约束3：失败路径必须回滚，且回滚结果写进错误消息
	for _, expected := range []string{
		"已恢复原上游",                          // 回滚成功的错误消息
		"未能恢复原上游",                          // 回滚失败的错误消息
		"未停过热点，原上游未受影响",                  // didStop=false 的分支
		"note='already-on'",                    // 已绑 TUN 的免断开快速路径
	} {
		if !strings.Contains(script, expected) {
			t.Errorf("winrt switch missing safety branch %q", expected)
		}
	}
	// 成功判定必须含 proof（stopOK/othersOff/未停过 三者之一），不允许无条件成功
	if !strings.Contains(script, "$proof") {
		t.Fatal("success must be gated on a proof condition")
	}
}
