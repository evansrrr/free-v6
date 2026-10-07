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
