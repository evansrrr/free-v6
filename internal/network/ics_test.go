package network

import (
	"strings"
	"testing"

	"github.com/yourname/freev6/internal/mihomo"
	"github.com/yourname/freev6/internal/warp"
)

// TunDeviceName 必须与 mihomo 渲染出的 tun.device 一致，否则 ICS 切换
// 脚本按名找不到 TUN 连接。
func TestTunDeviceNameMatchesMihomoConfig(t *testing.T) {
	device := mihomoTunDevice(t)
	if device != TunDeviceName {
		t.Fatalf("tun device mismatch: config renders %q, ics expects %q", device, TunDeviceName)
	}
}

// mihomoTunDevice 渲染真实配置并抽取 tun.device 的值。
func mihomoTunDevice(t *testing.T) string {
	t.Helper()
	config, err := mihomo.Render(warp.Device{
		PrivateKey: "private", PeerPublicKey: "peer",
		IPv4: "172.16.0.2", IPv6: "2606:4700::2",
	})
	if err != nil {
		t.Fatal(err)
	}
	const marker = "device: "
	idx := strings.Index(config, marker)
	if idx < 0 {
		t.Fatalf("rendered config has no tun device:\n%.400s", config)
	}
	rest := config[idx+len(marker):]
	if end := strings.IndexAny(rest, "\r\n"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

// 解析共享状态数组（正常形态）。
func TestParsePowerShellSharingArray(t *testing.T) {
	data := `[
	  {"name":"WLAN","guid":"{AAA}","device":"Intel Wi-Fi","enabled":true,"kind":"public"},
	  {"name":"Local Area Connection* 3","guid":"{BBB}","device":"Microsoft Wi-Fi Direct Virtual Adapter","enabled":true,"kind":"private"},
	  {"name":"Ethernet","guid":"{CCC}","device":"Realtek","enabled":false,"kind":"public"}
	]`
	sharing, err := ParsePowerShellSharing([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(sharing) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(sharing))
	}
	if sharing[0].Name != "WLAN" || !sharing[0].Enabled || sharing[0].Kind != "public" {
		t.Fatalf("unexpected first entry: %+v", sharing[0])
	}
	if sharing[1].Kind != "private" || sharing[1].Guid != "{BBB}" {
		t.Fatalf("unexpected second entry: %+v", sharing[1])
	}
}

// PowerShell 对单元素数组输出对象 —— 单对象形态也要能解析。
func TestParsePowerShellSharingSingleObject(t *testing.T) {
	sharing, err := ParsePowerShellSharing([]byte(`{"name":"WLAN","guid":"{AAA}","enabled":true,"kind":"public"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(sharing) != 1 || sharing[0].Name != "WLAN" {
		t.Fatalf("unexpected result: %+v", sharing)
	}
}

// 空输出（无共享连接）→ 空列表而不是错误。
func TestParsePowerShellSharingEmpty(t *testing.T) {
	sharing, err := ParsePowerShellSharing([]byte("[]"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sharing) != 0 {
		t.Fatalf("expected empty, got %+v", sharing)
	}
	// kind 非法的条目被丢弃，不让坏数据进入还原脚本
	sharing, err = ParsePowerShellSharing([]byte(`{"name":"X","guid":"{X}","enabled":true,"kind":"weird"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(sharing) != 0 {
		t.Fatalf("entry with invalid kind must be dropped, got %+v", sharing)
	}
}

// 探测脚本要带上网关地址参数，输出解析要覆盖两种形态。
func TestBuildHotspotDetectCommandAndParse(t *testing.T) {
	cmd := BuildHotspotDetectCommand()
	if !strings.Contains(cmd, "'"+DefaultHotspotGateway+"'") {
		t.Fatalf("detect command must embed gateway literal:\n%s", cmd)
	}
	status, err := ParseHotspotStatus([]byte(`{"active":true,"privateAlias":"Local Area Connection* 3","privateIP":"192.168.137.1","tethering":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if !status.Active || status.PrivateAlias == "" || status.PrivateIP != DefaultHotspotGateway {
		t.Fatalf("unexpected status: %+v", status)
	}
	if _, err := ParseHotspotStatus([]byte("")); err == nil {
		t.Fatal("empty detect output must be an error")
	}
	if _, err := ParseHotspotStatus([]byte("not json")); err == nil {
		t.Fatal("garbage detect output must be an error")
	}
}

// 经典切换脚本：TUN 设 PUBLIC(0)、热点私有侧设 PRIVATE(1)，经 Set-Share
// 重试核验（容忍 0x80040201 瞬态），输出结构化 JSON 而非直接抛错。
func TestBuildICSSwitchScript(t *testing.T) {
	script := BuildICSSwitchScript("Local Area Connection* 3")
	for _, expected := range []string{
		"New-Object -ComObject HNetCfg.HNetShare",
		"function Set-Share",                          // 重试核验函数
		"function Test-Share",
		"Start-Sleep -Milliseconds 900",               // 瞬态错误退避
		"$e = Set-Share $m $tun $true 0",              // TUN → public
		"$e = Set-Share $m $priv $true 1",             // 热点私有侧 → private
		"'Local Area Connection* 3'",
		"'" + TunDeviceName + "'",
		"ok=($tunOK -and $privOK)",                    // 结构化判定输出
		"ConvertTo-Json",
	} {
		if !strings.Contains(script, expected) {
			t.Errorf("switch script missing %q:\n%s", expected, script)
		}
	}
	// 不再直接抛错（业务失败走 JSON ok/error，避免半套状态）
	if strings.Contains(script, "throw ") {
		t.Fatalf("switch script must report via JSON, not throw:\n%s", script)
	}
	// 单引号注入防护：别名里的单引号被翻倍
	script = BuildICSSwitchScript("it's-a-conn")
	if !strings.Contains(script, "'it''s-a-conn'") {
		t.Fatalf("single quote must be escaped:\n%s", script)
	}
}

// 还原脚本：每个基线条目按 guid 恢复角色；FreeV6TUN 不在基线里 → 禁用共享。
func TestBuildICSRestoreScript(t *testing.T) {
	sharing := []SharingSnapshot{
		{Name: "WLAN", Guid: "{AAA}", Enabled: true, Kind: "public"},
		{Name: "Ethernet", Guid: "{CCC}", Enabled: false, Kind: "public"},
	}
	script := BuildICSRestoreScript(sharing, TunDeviceName)
	for _, expected := range []string{
		"'{AAA}'",         // guid 条目
		"e=$true",         // 原 PUBLIC
		"t=0",
		"'{CCC}'",
		"e=$false",        // 原未共享
		"'" + TunDeviceName + "'",
		"$s.DisableSharing()",
		"$errors +=",      // 单连接失败要汇总
	} {
		if !strings.Contains(script, expected) {
			t.Errorf("restore script missing %q:\n%s", expected, script)
		}
	}
}

// WinRT 切换脚本：改绑目标必须是 TunDeviceName 字面量（回归：字符串拼接
// 曾把 Go 表达式原样写进 PowerShell）；成功判定基于 TetheringState 轮询
// 而非 async op.Status（回归：Status 空串导致“热点实际已切换却报失败”）。
func TestBuildWinRTSwitchScript(t *testing.T) {
	script := BuildWinRTSwitchScript()
	if !strings.Contains(script, "'"+TunDeviceName+"'") {
		t.Fatalf("winrt switch must reference tun device literal:\n%s", script)
	}
	if strings.Contains(script, "+ TunDeviceName +") || strings.Contains(script, "psQuote") {
		t.Fatalf("Go expression leaked into PowerShell script:\n%s", script)
	}
	for _, expected := range []string{
		"StartTetheringAsync", "StopTetheringAsync", "originalProfile",
		"function Wait-State", "TetheringState -eq 'On'",   // 状态轮询判定
		"started=$true",
	} {
		if !strings.Contains(script, expected) {
			t.Errorf("winrt switch missing %q", expected)
		}
	}
	// 回归：不再信任 async op.Status（曾为空串导致假失败）
	for _, banned := range []string{"Wait-Op", "$op2.Status", "GetResults()"} {
		if strings.Contains(script, banned) {
			t.Errorf("winrt switch must not trust async op field %q (use TetheringState polling)", banned)
		}
	}
}

// WinRT 还原脚本：回绑到记录的原上游；输出 restarted/reason。
func TestBuildWinRTRestoreScript(t *testing.T) {
	script := BuildWinRTRestoreScript("WLAN")
	if !strings.Contains(script, "'WLAN'") {
		t.Fatalf("restore must embed original profile:\n%s", script)
	}
	for _, expected := range []string{"not-running", "restarted", "StartTetheringAsync"} {
		if !strings.Contains(script, expected) {
			t.Errorf("winrt restore missing %q", expected)
		}
	}
	// 单引号注入防护
	script = BuildWinRTRestoreScript("it's wlan")
	if !strings.Contains(script, "'it''s wlan'") {
		t.Fatalf("single quote must be escaped:\n%s", script)
	}
}

// HotspotActive：切换时记录的私网地址出现在本机网卡上 → 在线；
// 消失 → 断开；未记录地址 → 保守视为在线（避免误报）。
func TestHotspotActive(t *testing.T) {
	orig := listInterfaceIPs
	defer func() { listInterfaceIPs = orig }()

	listInterfaceIPs = func() []string { return []string{"192.168.137.1", "10.0.0.5"} }
	if !HotspotActive(&HotspotMeta{Applied: true, PrivateIP: "192.168.137.1"}) {
		t.Fatal("gateway present must report active")
	}
	listInterfaceIPs = func() []string { return []string{"10.0.0.5"} }
	if HotspotActive(&HotspotMeta{Applied: true, PrivateIP: "192.168.137.1"}) {
		t.Fatal("gateway gone must report disconnected")
	}
	listInterfaceIPs = func() []string { return nil }
	if !HotspotActive(&HotspotMeta{Applied: true}) {
		t.Fatal("no recorded address must stay conservative-active")
	}
	if !HotspotActive(nil) || !HotspotActive(&HotspotMeta{Applied: false}) {
		t.Fatal("nil/not-applied must not report disconnected")
	}
}

// jsonUnmarshalFlexible：正常 JSON 直接解析；混入噪声行时截取最后一段。
func TestJSONUnmarshalFlexible(t *testing.T) {
	var out struct {
		Errors []string `json:"errors"`
	}
	if err := jsonUnmarshalFlexible([]byte(`{"errors":[]}`+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	if err := jsonUnmarshalFlexible([]byte("WARNING: something\n{\"errors\":[\"a\"]}"), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Errors) != 1 || out.Errors[0] != "a" {
		t.Fatalf("unexpected: %+v", out)
	}
	if err := jsonUnmarshalFlexible([]byte("no json at all"), &out); err == nil {
		t.Fatal("non-JSON output must error")
	}
	if err := jsonUnmarshalFlexible([]byte("  "), &out); err == nil {
		t.Fatal("empty output must error")
	}
}
