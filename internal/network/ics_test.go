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

// 经典切换脚本：TUN 设 PUBLIC、热点私有侧设 PRIVATE，参数正确引用。
func TestBuildICSSwitchScript(t *testing.T) {
	script := BuildICSSwitchScript("Local Area Connection* 3")
	for _, expected := range []string{
		"New-Object -ComObject HNetCfg.HNetShare",
		"$tc.EnableSharing(0)",
		"$pc.EnableSharing(1)",
		"'Local Area Connection* 3'",
		"'" + TunDeviceName + "'",
		"$ErrorActionPreference='Stop'",
	} {
		if !strings.Contains(script, expected) {
			t.Errorf("switch script missing %q:\n%s", expected, script)
		}
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
		"'{AAA}'", // guid 条目
		"e=$true", // 原 PUBLIC
		"t=0",
		"'{CCC}'",
		"e=$false", // 原未共享
		"'" + TunDeviceName + "'",
		"$s.DisableSharing()",
		"$errors +=", // 单连接失败要汇总
	} {
		if !strings.Contains(script, expected) {
			t.Errorf("restore script missing %q:\n%s", expected, script)
		}
	}
}

// WinRT 切换脚本：改绑目标必须是 TunDeviceName 字面量（回归：字符串拼接
// 曾把 Go 表达式原样写进 PowerShell）；成败以 TetheringState 轮询为准，
// 绝不读异步 op 的 .Status/GetResults（部分机器上该属性为空，曾把进行中
// 的启动误判为“启动热点未完成”的假失败）；定位失败必须发生在停止之前。
func TestBuildWinRTSwitchScript(t *testing.T) {
	script := BuildWinRTSwitchScript("USTB-Student 2")
	if !strings.Contains(script, "'"+TunDeviceName+"'") {
		t.Fatalf("winrt switch must reference tun device literal:\n%s", script)
	}
	if strings.Contains(script, "+ TunDeviceName +") || strings.Contains(script, "psQuote") {
		t.Fatalf("Go expression leaked into PowerShell script:\n%s", script)
	}
	// 原上游由调用方给定，脚本不再自己按网卡名猜：那个猜测在「网卡显示名
	// != 连接配置名」的机器上恒为空，等于让失败回滚永远空转。
	if !strings.Contains(script, "'USTB-Student 2'") {
		t.Fatal("switch script must embed the upstream given by the caller")
	}
	if strings.Contains(script, "$p.ProfileName -eq $wlan.Name") {
		t.Fatal("switch script must not re-derive the upstream from the adapter name")
	}
	// 参数为空、上游已消失都必须先于任何停止动作失败：热点一旦停了，这两
	// 种情况都没有东西可以拉回来。它们也必须落在 preflight 阶段里 —— 调用方
	// 靠 stage 判断「热点分毫未动」，才不会去重启一个好好的热点。
	stoppedStage := strings.Index(script, "$stage = 'stopping'")
	if stoppedStage < 0 {
		t.Fatal("switch script must mark the stop phase")
	}
	for _, expected := range []string{"未提供热点原上游", "原上游连接配置", "'preflight'"} {
		idx := strings.Index(script, expected)
		stop := strings.Index(script, "StopTetheringAsync")
		if idx < 0 || stop < 0 || idx > stop {
			t.Fatalf("upstream preflight %q (idx %d) must precede any stop (idx %d)", expected, idx, stop)
		}
		if expected != "'preflight'" && idx > stoppedStage {
			t.Fatalf("upstream preflight %q must stay inside the preflight stage", expected)
		}
	}
	// 失败也要输出结构化结果：靠退出码无法区分「停在准备阶段」与「已经停过
	// 热点」，而这两种情况需要完全不同的善后。
	if !strings.Contains(script, "stage=$stage") || !strings.Contains(script, "ok=$done") {
		t.Fatal("switch script must always emit {ok, stage, originalProfile, error}")
	}
	// 脚本自己回滚成功时要标出来：否则调用方会再「恢复」一次，把这个已经
	// 回到原上游的热点又停一遍。
	if !strings.Contains(script, "$stage = 'recovered'") {
		t.Fatal("switch script must mark a successful in-script rollback")
	}
	for _, expected := range []string{"StartTetheringAsync", "StopTetheringAsync", "originalProfile", "TetheringState", "Wait-Heat", DefaultHotspotGateway, "IsNullOrWhiteSpace"} {
		if !strings.Contains(script, expected) {
			t.Errorf("winrt switch missing %q", expected)
		}
	}
	// 双层判定：状态投影读空的机器必须能退回网关 IP 探测（Get-TetherState
	// 时代第一个空读数就返回，三段轮询全部超时仍报假失败）。
	if !strings.Contains(script, "Test-HeatUp") || !strings.Contains(script, "Get-NetIPAddress") {
		t.Fatal("switch script must fall back to gateway IP probing when state reads are empty")
	}
	if strings.Contains(script, "Wait-Op") || strings.Contains(script, "op2.Status") || strings.Contains(script, ".GetResults") {
		t.Fatal("switch script must judge by TetheringState polling, never by async op .Status/GetResults")
	}
	// 先定位 tunP（含 not-found 抛错），再发生任何停止动作：否则热点
	// 停了之后才发现目标不存在，就没人把它拉起来。
	locate := strings.Index(script, "connection profile not found")
	stop := strings.Index(script, "StopTetheringAsync")
	if locate < 0 || stop < 0 || locate > stop {
		t.Fatalf("tunP lookup (idx %d) must precede any stop (idx %d)", locate, stop)
	}
	// 回滚失败必须把结果写进错误消息，便于区分“已恢复/未恢复”
	if !strings.Contains(script, "已恢复原上游") || !strings.Contains(script, "未能恢复原上游") {
		t.Fatal("switch failure must report rollback outcome in the message")
	}
	// 回滚直接用停止前解析好的 $origP：再查一次就可能查不到，等于把热点
	// 留在关闭状态。
	if !strings.Contains(script, "CreateFromConnectionProfile($origP).StartTetheringAsync()") {
		t.Fatal("switch rollback must reuse the resolved upstream profile object")
	}
}

// 切换失败后要不要「把热点拉回来」，取决于脚本走到了哪一步：preflight
// 阶段的失败没有动过热点，脚本自己回滚成功的也已经在原上游上跑着 —— 这两种
// 情况再恢复只会白停一次，甚至因为同一个原因再也拉不起来。空 Stage（脚本被
// 杀/输出不可解析）按最坏情况处理。
func TestWinRTSwitchResultNeedsHotspotRecovery(t *testing.T) {
	cases := map[string]struct {
		stage string
		want  bool
	}{
		"preflight": {stage: "preflight", want: false},
		"recovered": {stage: "recovered", want: false},
		"done":      {stage: "done", want: false},
		"stopping":  {stage: "stopping", want: true},
		"starting":  {stage: "starting", want: true},
		"no output": {stage: "", want: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := (WinRTSwitchResult{Stage: c.stage}).NeedsHotspotRecovery(); got != c.want {
				t.Fatalf("NeedsHotspotRecovery(%q) = %v, want %v", c.stage, got, c.want)
			}
		})
	}
}

// WinRT 还原脚本：回绑到记录的原上游；输出 restarted/reason；同样只看
// 真实状态轮询，不读异步 op 属性。
func TestBuildWinRTRestoreScript(t *testing.T) {
	script := BuildWinRTRestoreScript("WLAN")
	if !strings.Contains(script, "'WLAN'") {
		t.Fatalf("restore must embed original profile:\n%s", script)
	}
	for _, expected := range []string{"not-running", "restarted", "StartTetheringAsync", "TetheringState", "Wait-Heat", DefaultHotspotGateway, "IsNullOrWhiteSpace", "Get-NetIPAddress"} {
		if !strings.Contains(script, expected) {
			t.Errorf("winrt restore missing %q", expected)
		}
	}
	if strings.Contains(script, "Wait-Op") || strings.Contains(script, "op2.Status") || strings.Contains(script, ".GetResults") {
		t.Fatal("restore script must judge by TetheringState polling, never by async op .Status/GetResults")
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

// WinRT 原上游探测脚本必须只读：它跑在「停热点」之前，任何 Stop/Start 调用
// 都会把「抢在停机前留痕」变成一次真实故障。
//
// 它同时是切换脚本的唯一上游来源，所以「取哪条链路」必须取真正承载流量的
// 那条：默认路由所在的物理网卡。按「第一个 Up 的物理网卡」猜在有线+无线
// 同时在线时会指错，而指错的上游在失败回滚里同样拉不起热点。
func TestBuildWinRTProfileProbeScript(t *testing.T) {
	script := BuildWinRTProfileProbeScript()
	for _, forbidden := range []string{"StopTetheringAsync", "StartTetheringAsync", "EnableSharing", "DisableSharing", "GetConnectionProfiles"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("probe script must stay read-only and cheap, found %q", forbidden)
		}
	}
	for _, expected := range []string{"originalProfile", "Get-NetConnectionProfile", "Get-NetAdapter", "Get-NetRoute", "$phys.Name -contains $_.InterfaceAlias"} {
		if !strings.Contains(script, expected) {
			t.Errorf("probe script missing %q", expected)
		}
	}
	// 只在物理网卡里选默认路由：TUN 在免流运行时 metric 最低，不限定就会
	// 把 TUN 自己当成热点上游。
	if !strings.Contains(script, "-Physical") {
		t.Fatal("probe must restrict route lookup to physical adapters")
	}
}

// 切换失败后的恢复脚本与还原脚本只差一点：热点关着时也要拉起。这是唯一会
// 主动打开热点的路径 —— 它只跑在「刚刚因为切换失败把热点关掉」的现场。
func TestBuildWinRTRecoverScript(t *testing.T) {
	recoverScript := BuildWinRTRecoverScript("USTB-Student 2")
	if !strings.Contains(recoverScript, "'USTB-Student 2'") {
		t.Fatal("recover script must embed the original upstream")
	}
	if !strings.Contains(recoverScript, "StartTetheringAsync") || !strings.Contains(recoverScript, "restarted") {
		t.Fatal("recover script must start tethering and report the outcome")
	}
	if strings.Contains(recoverScript, "'not-running'") {
		t.Fatal("recover script must start the hotspot even when it is currently off")
	}
	// 与还原脚本共用骨架：状态判定必须一致，否则「还原成功」和「恢复成功」
	// 会各说一套。
	restoreScript := BuildWinRTRestoreScript("USTB-Student 2")
	if !strings.Contains(restoreScript, "'not-running'") {
		t.Fatal("restore script must keep skipping a hotspot the user turned off")
	}
	for _, shared := range []string{"Get-HeatState", "Wait-Heat", "original-profile-missing", "start-failed"} {
		if !strings.Contains(recoverScript, shared) || !strings.Contains(restoreScript, shared) {
			t.Fatalf("recover and restore must share %q", shared)
		}
	}
}
