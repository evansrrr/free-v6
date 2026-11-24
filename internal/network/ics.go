package network

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

// TunDeviceName 必须与 internal/mihomo/config.go 渲染的 tun.device 一致：
// ICS 切换/还原脚本按该显示名在 HNetShare 连接列表与 WinRT 连接配置里
// 定位 FreeV6 的 TUN 虚拟网卡（不依赖本地化网卡名）。
// 一致性由 ics_test.go 的 TestTunDeviceNameMatchesMihomoConfig 守护。
const TunDeviceName = "FreeV6TUN"

// DefaultHotspotGateway 是 Windows ICS / 移动热点私有侧的默认网关地址。
// 热点开启时该地址必落在私有侧网卡上；热点关闭后消失 —— 两者都是廉价的
// 运行期探测信号（HotspotActive）。
const DefaultHotspotGateway = "192.168.137.1"

// SharingSnapshot 记录一次 ICS 共享状态（HNetShare 连接的角色），
// 作为切换失败回滚与停止免流时还原的基线。
type SharingSnapshot struct {
	Name    string `json:"name"`
	Guid    string `json:"guid"`
	Device  string `json:"device"`
	Enabled bool   `json:"enabled"`
	Kind    string `json:"kind"` // "public" | "private"
}

// HotspotMeta 是成功切换后写回网络快照的热点共享元数据：
// 停止免流时据此还原（/status 轮询也用它计算 active）。
type HotspotMeta struct {
	Applied         bool   `json:"applied"`
	Path            string `json:"path"` // "ics" | "winrt" | HotspotPathPending
	PrivateIP       string `json:"privateIP,omitempty"`
	OriginalProfile string `json:"originalProfile,omitempty"` // winrt 路径：切换前的上游连接配置
}

// HotspotPathPending 是「已经准备切换、还没落定」的占位路径。ICS 角色是在
// PowerShell 脚本里改的，进程死在中途没有任何机会写回真实元数据；快照里先
// 留下这个标记，下次启动的残留自愈才知道要按 Sharing 把角色还原回去。
const HotspotPathPending = "pending"

// HotspotStatus 是 DetectHotspot 的探测结果。
type HotspotStatus struct {
	Active       bool   `json:"active"`
	PrivateAlias string `json:"privateAlias"` // 持有 ICS 网关地址的网卡显示名（经典路径）
	PrivateIP    string `json:"privateIP"`
	Tethering    bool   `json:"tethering"` // WinRT tethering 处于 On
}

// psQuote 把字符串包成 PowerShell 单引号字面量（内部单引号翻倍）。
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// jsonUnmarshalFlexible 容错解析 PowerShell 输出：去掉首尾空白后再解码，
// 输出混入噪声行时按序尝试每个 `{`/`[` 起点，取第一个能解析成功的片段
// （正向扫描而非取最后一个 —— JSON 自身嵌套的括号会被误判成起点）。
func jsonUnmarshalFlexible(output []byte, target any) error {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return fmt.Errorf("empty PowerShell output")
	}
	if err := json.Unmarshal([]byte(trimmed), target); err == nil {
		return nil
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] != '{' && trimmed[i] != '[' {
			continue
		}
		if err := json.Unmarshal([]byte(trimmed[i:]), target); err == nil {
			return nil
		}
	}
	return fmt.Errorf("no JSON in PowerShell output: %.200s", trimmed)
}

const psHeader = "[Console]::OutputEncoding=[System.Text.Encoding]::UTF8;"

// PowerShellSharingCaptureCommand 枚举全部 HNetShare 连接的共享状态，
// 输出 UTF-8 JSON 数组（中文网卡名依赖首行的编码设置）。
const PowerShellSharingCaptureCommand = psHeader + `
$m = New-Object -ComObject HNetCfg.HNetShare
$o = @()
foreach ($c in $m.EnumEveryConnection) {
  $p = $m.NetConnectionProps($c)
  $s = $m.INetSharingConfigurationForINetConnection($c)
  $o += [pscustomobject]@{
    name = $p.Name; guid = $p.Guid; device = $p.DeviceName;
    enabled = [bool]$s.SharingEnabled;
    kind = $(if ([int]$s.SharingType -eq 0) { 'public' } else { 'private' })
  }
}
ConvertTo-Json -InputObject $o -Compress`

// ParsePowerShellSharing 解析共享状态抓取输出。PowerShell 对单元素数组
// 也会输出对象，两种形状都要接受（对象或数组都按同一逻辑展开）。
func ParsePowerShellSharing(data []byte) ([]SharingSnapshot, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, nil
	}
	var raw any
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode ICS sharing snapshot: %w", err)
	}
	items, ok := raw.([]any)
	if !ok {
		if object, objectOK := raw.(map[string]any); objectOK {
			items = []any{object}
		} else {
			return nil, fmt.Errorf("ICS sharing snapshot must be an object or array")
		}
	}
	sharing := make([]SharingSnapshot, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("ICS sharing snapshot item must be an object")
		}
		kind, _ := object["kind"].(string)
		if kind != "public" && kind != "private" {
			continue
		}
		entry := SharingSnapshot{
			Name: firstString(object["name"]),
			Guid: firstString(object["guid"]),
		}
		if entry.Name == "" && entry.Guid == "" {
			continue
		}
		entry.Device = firstString(object["device"])
		entry.Kind = kind
		entry.Enabled, _ = object["enabled"].(bool)
		sharing = append(sharing, entry)
	}
	return sharing, nil
}

// PowerShellHotspotDetectCommand 探测移动热点是否在运行：
//  1. 经典信号：ICS 网关地址（192.168.137.1）落在某块网卡上；
//  2. 新 WDI 兜底：WinRT NetworkOperatorTetheringManager.TetheringState == On。
const PowerShellHotspotDetectCommand = psHeader + `
$ErrorActionPreference='SilentlyContinue'
$active=$false; $alias=''; $ip=''; $tether=$false
$a = Get-NetIPAddress -AddressFamily IPv4 | Where-Object { $_.IPAddress -eq %s } | Select-Object -First 1
if ($null -ne $a) { $active=$true; $alias=[string]$a.InterfaceAlias; $ip=[string]$a.IPAddress }
try {
  $ni=[Windows.Networking.Connectivity.NetworkInformation,Windows.Networking.Connectivity,ContentType=WindowsRuntime]
  $tm=[Windows.Networking.NetworkOperators.NetworkOperatorTetheringManager,Windows.Networking.NetworkOperators,ContentType=WindowsRuntime]
  foreach ($p in $ni::GetConnectionProfiles()) {
    try {
      $mgr=$tm::CreateFromConnectionProfile($p)
      if ([string]$mgr.TetheringState -eq 'On') { $tether=$true; $active=$true; break }
    } catch {}
  }
} catch {}
ConvertTo-Json -InputObject ([pscustomobject]@{active=$active; privateAlias=$alias; privateIP=$ip; tethering=$tether}) -Compress`

// BuildHotspotDetectCommand 返回带网关地址参数的探测脚本。
func BuildHotspotDetectCommand() string {
	return fmt.Sprintf(PowerShellHotspotDetectCommand, psQuote(DefaultHotspotGateway))
}

// ParseHotspotStatus 解析探测输出。
func ParseHotspotStatus(data []byte) (HotspotStatus, error) {
	var status HotspotStatus
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return status, fmt.Errorf("hotspot detect produced no output")
	}
	if err := json.Unmarshal([]byte(trimmed), &status); err != nil {
		return status, fmt.Errorf("decode hotspot detect: %w", err)
	}
	return status, nil
}

// BuildICSSwitchScript 构造经典路径（HNetShare COM）的拓扑切换脚本：
// FreeV6TUN → PUBLIC（上游，自动取消原 PUBLIC），热点私有侧网卡 → PRIVATE。
// 任何一步失败都以非零退出码抛错，由调用方回滚。
func BuildICSSwitchScript(privateAlias string) string {
	var b strings.Builder
	b.WriteString(psHeader)
	b.WriteString("\n$ErrorActionPreference='Stop'\n")
	b.WriteString("$m = New-Object -ComObject HNetCfg.HNetShare\n")
	b.WriteString("function Find([string]$name) { foreach ($c in $m.EnumEveryConnection) { if ($m.NetConnectionProps($c).Name -eq $name) { return $c } }; return $null }\n")
	fmt.Fprintf(&b, "$tun = Find %s\n", psQuote(TunDeviceName))
	fmt.Fprintf(&b, "if ($null -eq $tun) { throw %s }\n", psQuote("TUN connection "+TunDeviceName+" not found"))
	b.WriteString("$tc = $m.INetSharingConfigurationForINetConnection($tun)\n")
	b.WriteString("if (-not $tc.SharingEnabled -or [int]$tc.SharingType -ne 0) { if ($tc.SharingEnabled) { $tc.DisableSharing() }; $tc.EnableSharing(0) }\n")
	fmt.Fprintf(&b, "$priv = Find %s\n", psQuote(privateAlias))
	fmt.Fprintf(&b, "if ($null -eq $priv) { throw %s }\n", psQuote("hotspot connection '"+privateAlias+"' not found"))
	b.WriteString("$pc = $m.INetSharingConfigurationForINetConnection($priv)\n")
	b.WriteString("if (-not $pc.SharingEnabled -or [int]$pc.SharingType -ne 1) { if ($pc.SharingEnabled) { $pc.DisableSharing() }; $pc.EnableSharing(1) }\n")
	b.WriteString("ConvertTo-Json -InputObject ([pscustomobject]@{path='ics'}) -Compress\n")
	return b.String()
}

// BuildICSRestoreScript 构造还原脚本：按快照把每个连接恢复到原角色；
// 快照里没有的连接只有 FreeV6TUN 会被禁用共享（会话中途新建的其他共享不碰）。
// 单连接失败收集后以非零退出码退出，输出还原结果 JSON。
func BuildICSRestoreScript(sharing []SharingSnapshot, tunName string) string {
	var b strings.Builder
	b.WriteString(psHeader)
	b.WriteString("\n$ErrorActionPreference='Stop'\n")
	b.WriteString("$m = New-Object -ComObject HNetCfg.HNetShare\n")
	b.WriteString("$errors = @()\n")
	b.WriteString("$want = @(\n")
	for _, entry := range sharing {
		fmt.Fprintf(&b, "  [pscustomobject]@{guid=%s; name=%s; e=$%s; t=%d}\n",
			psQuote(entry.Guid), psQuote(entry.Name), map[bool]string{true: "true", false: "false"}[entry.Enabled],
			map[string]int{"public": 0, "private": 1}[entry.Kind])
	}
	b.WriteString(")\n")
	b.WriteString("foreach ($c in $m.EnumEveryConnection) {\n")
	b.WriteString("  $p = $m.NetConnectionProps($c)\n")
	b.WriteString("  $s = $m.INetSharingConfigurationForINetConnection($c)\n")
	b.WriteString("  $match = $null\n")
	b.WriteString("  foreach ($w in $want) { if ($w.guid -eq $p.Guid) { $match = $w; break } }\n")
	b.WriteString("  try {\n")
	b.WriteString("    if ($null -ne $match) {\n")
	b.WriteString("      $cur = [bool]$s.SharingEnabled\n")
	b.WriteString("      $curT = $(if ($cur) { [int]$s.SharingType } else { -1 })\n")
	b.WriteString("      if ($match.e -and ((-not $cur) -or $curT -ne [int]$match.t)) {\n")
	b.WriteString("        if ($cur) { $s.DisableSharing() }\n")
	b.WriteString("        $s.EnableSharing([int]$match.t)\n")
	b.WriteString("      } elseif ((-not $match.e) -and $cur) { $s.DisableSharing() }\n")
	b.WriteString("    } elseif ($p.Name -eq " + psQuote(tunName) + " -and [bool]$s.SharingEnabled) {\n")
	b.WriteString("      $s.DisableSharing()\n")
	b.WriteString("    }\n")
	b.WriteString("  } catch { $errors += ($p.Name + ': ' + $_.Exception.Message) }\n")
	b.WriteString("}\n")
	b.WriteString("ConvertTo-Json -InputObject ([pscustomobject]@{errors=$errors}) -Compress\n")
	return b.String()
}

// winRTUpstreamAdapter 定位「tethering 实际在共享的那条链路」—— 它就是热点
// 真正的上游。优先取物理网卡中承载默认路由的那一个：有线+无线同时在线时，
// 按「第一个 Up 的物理网卡」猜会指错，而指错的上游在切换失败的回滚里同样
// 拉不起热点。取不到路由再退回第一个 Up 的物理网卡。
//
// TUN / VPN / 虚拟网卡一律排除：免流运行时 TUN 的默认路由 metric 最小，
// 不排除就会把 TUN 自己当成原上游。$tunName 由调用方在脚本里先定义好。
const winRTUpstreamAdapter = `$phys = @(Get-NetAdapter -Physical -ErrorAction SilentlyContinue | Where-Object { $_.Status -eq 'Up' -and $_.Name -ne $tunName -and $_.InterfaceDescription -notmatch 'Wi-Fi Direct|Virtual|Loopback|Wintun' })
$upIf = $null
if ($phys.Count -gt 0) {
  $route = Get-NetRoute -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue | Where-Object { $phys.Name -contains $_.InterfaceAlias } | Sort-Object { [int]$_.RouteMetric + [int]$_.InterfaceMetric } | Select-Object -First 1
  if ($null -ne $route) { $upIf = $phys | Where-Object { $_.Name -eq $route.InterfaceAlias } | Select-Object -First 1 }
  if ($null -eq $upIf) { $upIf = $phys | Select-Object -First 1 }
}
`

// BuildWinRTProfileProbeScript 只读探测：返回热点当前的上游连接名。
//
// 名字必须是 WinRT 的 ConnectionProfile.ProfileName，不是 NetConnectionProfile
// 的那个 —— 两者不是同一套命名空间：重名 SSID 重建过配置时，网络配置 API 会
// 给出 "USTB-Student 2"，而 WinRT 里同一条连接仍叫 "USTB-Student"。拿前者去
// 切换脚本里查，只会得到「连接配置不存在」，白白放弃一次本可以成功的切换
// （线上实测）。所以这里直接在 WinRT 的连接配置里按网卡 GUID + 「正在连接」
// 解析，产出的名字切换/还原脚本都能直接用。
//
// 同一块网卡上可能挂着几十条已保存的配置，只有正在连接的那条能让热点真正
// 走这条链路：WLAN 看有没有连上的 SSID，其余看连接级别。
//
// 用途有两层：抢在「停热点」之前写进快照（tethering 的停与重开在同一个脚本
// 里，进程被杀在中间时热点会停在关闭状态，而脚本的输出要跑完才有），以及作为
// 切换脚本的唯一上游来源 —— 探测为空时调用方直接放弃切换，不去做一件无法
// 撤销的事。
func BuildWinRTProfileProbeScript() string {
	return psHeader + `
$ErrorActionPreference='Stop'
$tunName = ` + psQuote(TunDeviceName) + `
` + winRTUpstreamAdapter + `$ni=[Windows.Networking.Connectivity.NetworkInformation,Windows.Networking.Connectivity,ContentType=WindowsRuntime]
$orig = ''
if ($null -ne $upIf) {
  $guid = ([string]$upIf.InterfaceGuid).Trim('{','}').ToLowerInvariant()
  $candidates = @()
  foreach ($p in $ni::GetConnectionProfiles()) {
    if ($null -eq $p.NetworkAdapter) { continue }
    if (([string]$p.NetworkAdapter.NetworkAdapterId).Trim('{','}').ToLowerInvariant() -eq $guid) { $candidates += $p }
  }
  foreach ($p in $candidates) {
    $live = $false
    if ($p.IsWlanConnectionProfile) {
      try { $live = ([string]$p.WlanConnectionProfileDetails.GetConnectedSsid()) -ne '' } catch { $live = $false }
    } else {
      try { $live = ([string]$p.GetNetworkConnectivityLevel()) -ne 'None' } catch { $live = $false }
    }
    if ($live) { $orig = [string]$p.ProfileName; break }
  }
  # 只有一条配置时不必再判「正在连接」（有线网卡就是这样）；多条却都判不出
  # 在线，说明这条链路此刻并不通，宁可不切。
  if ($orig -eq '' -and $candidates.Count -eq 1) { $orig = [string]$candidates[0].ProfileName }
}
ConvertTo-Json -InputObject ([pscustomobject]@{originalProfile=$orig}) -Compress
`
}

// BuildWinRTSwitchScript 构造新 WDI 兜底路径：停掉现有 tethering，
// 改绑到 FreeV6TUN 连接配置重新启动热点。
//
// originalProfile 由调用方（只读探测）给出，脚本不再自己猜上游：脚本里的停与
// 重开不是原子的，任何「原上游判定为空」都会把热点丢在关闭状态且没人拉起
// （历史上这里按「网卡显示名 == 连接配置名」匹配，在两者不同的机器上恒为空，
// 失败回滚也就恒为空转）。现在上游由参数给定，脚本只校验它还在不在，并且
// 必须在任何停止动作之前完成。
//
// 成败判定是双层的，任何一层可用即可：优先 TetheringState（读到非空值），
// 投影读空的机器退回热点网关 IP 探测（与 DetectHotspot 同一信号，已在
// 故障机验证可用）。绝不读异步 op 的 .Status/GetResults —— 部分机器上
// 该属性为空，曾把进行中的启动误判为“未完成”的假失败。
func BuildWinRTSwitchScript(originalProfile string) string {
	return psHeader + `
$ErrorActionPreference='Stop'
$ni=[Windows.Networking.Connectivity.NetworkInformation,Windows.Networking.Connectivity,ContentType=WindowsRuntime]
$tm=[Windows.Networking.NetworkOperators.NetworkOperatorTetheringManager,Windows.Networking.NetworkOperators,ContentType=WindowsRuntime]
$profiles=@($ni::GetConnectionProfiles())
$tunName = ` + psQuote(TunDeviceName) + `
$origName = ` + psQuote(originalProfile) + `
$gateway = ` + psQuote(DefaultHotspotGateway) + `
function Get-HeatState {
  $found = ''
  foreach ($p in $profiles) {
    try {
      $st = [string]$tm::CreateFromConnectionProfile($p).TetheringState
      if (-not [string]::IsNullOrWhiteSpace($st)) {
        if ($st -eq 'On') { return 'On' }
        if ($found -eq '') { $found = $st }
      }
    } catch {}
  }
  return $found
}
function Test-HeatUp {
  $st = Get-HeatState
  if ($st -ne '') { return ($st -eq 'On') }
  $a = Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.IPAddress -eq $gateway } | Select-Object -First 1
  return ($null -ne $a)
}
function Test-HeatDown {
  $st = Get-HeatState
  if ($st -ne '') { return ($st -eq 'Off') }
  return (-not (Test-HeatUp))
}
function Wait-Heat([string]$want, [int]$ticks) {
  for ($i = 0; $i -lt $ticks; $i++) {
    $ok = $false
    if ($want -eq 'Up') { $ok = Test-HeatUp } else { $ok = Test-HeatDown }
    if ($ok) { return $true }
    Start-Sleep -Milliseconds 500
  }
  return $false
}
function Get-HeatLabel {
  $st = Get-HeatState
  if ($st -eq '') { $st = '状态不可读' }
  $g = '热点网关不在线'
  if (Test-HeatUp) { $g = '热点网关在线' }
  return ($st + '，' + $g)
}
# 1) 任何停止动作之前先定位原上游与 TUN 连接：任一缺失就原地失败，热点不动。
$done = $false
$stage = 'preflight'
$err = ''
try {
  if ($origName -eq '') { throw '未提供热点原上游，未改动热点' }
  $tunP = $null
  foreach ($p in $profiles) { if ($p.ProfileName -eq $tunName) { $tunP = $p; break } }
  if ($null -eq $tunP) { throw ($tunName + ' connection profile not found') }
  $origP = $null
  foreach ($p in $profiles) { if ($p.ProfileName -eq $origName) { $origP = $p; break } }
  if ($null -eq $origP) { throw ('原上游连接配置 ' + $origName + ' 已不存在，未改动热点') }
  if (-not (Test-HeatUp)) { throw '移动热点未处于运行状态（tethering 未启动）' }
  # 2) 重启热点：先停（双层判定确认；超时确认不了也继续，绝不因此中止 ——
  #    半途而废会把热点丢在关闭状态）。
  $stage = 'stopping'
  foreach ($p in $profiles) { try { $null = $tm::CreateFromConnectionProfile($p).StopTetheringAsync(); break } catch {} }
  $null = Wait-Heat 'Down' 16
  # 3) 绑定 TUN 重新启动：双层判定（最多 20s）。
  $stage = 'starting'
  try { $null = $tm::CreateFromConnectionProfile($tunP).StartTetheringAsync() } catch {}
  if (-not (Wait-Heat 'Up' 40)) {
    # 就地回滚：$origP 在停止之前就解析好了，这里不再重新查找 —— 查找失败等于
    # 把一个关着的热点留给用户。
    try { $null = $tm::CreateFromConnectionProfile($origP).StartTetheringAsync() } catch {}
    $final = Get-HeatLabel
    if (Wait-Heat 'Up' 20) {
      $stage = 'recovered'
      throw ('启动热点未成功（最终状态: ' + $final + '）；已恢复原上游')
    }
    throw ('启动热点未成功（最终状态: ' + $final + '）；未能恢复原上游，请手动重开热点')
  }
  $done = $true
  $stage = 'done'
} catch {
  $err = [string]$_.Exception.Message
}
# 失败也必须输出 JSON：调用方要靠 stage 判断热点有没有被停过（preflight 失败
# 等于热点原样未动，不该再去「恢复」一个本来好好的热点）。脚本被超时杀掉时
# 没有输出，调用方按「最坏情况：可能已经停了」处理。
ConvertTo-Json -InputObject ([pscustomobject]@{ok=$done; stage=$stage; originalProfile=$origName; error=$err}) -Compress
`
}

// BuildWinRTRestoreScript 构造 winrt 路径的还原：停掉绑在 TUN 上的
// tethering，再用切换前记录的上游连接配置重新拉起热点。
// 输出 {restarted, reason}；reason 为 not-running 表示热点本就关闭（不算警告）。
// 与切换脚本同理：双层判定（TetheringState 优先，读空退回网关 IP 探测），
// 不读异步 op 的 .Status/GetResults。
//
// 热点本来就关着时不做任何事：会话期间用户可能自己把它关了，停止免流不该
// 替用户打开。
func BuildWinRTRestoreScript(originalProfile string) string {
	return buildWinRTTetherScript(originalProfile, false)
}

// BuildWinRTRecoverScript 构造「切换失败后把热点拉回原上游」的脚本。与还原
// 脚本只差一处：热点已经关着时也要拉起 —— 切换脚本里的停与启不是原子的，
// TUN 绑定失败、或进程被杀在两者之间，留下的正是「热点关着」。
//
// 这是唯一会主动打开热点的路径，只在上一步刚把它关掉的现场使用；停止免流
// 仍走 BuildWinRTRestoreScript，保留「用户自己关了热点」这一意图。
func BuildWinRTRecoverScript(originalProfile string) string {
	return buildWinRTTetherScript(originalProfile, true)
}

// buildWinRTTetherScript 是还原/失败恢复共用的脚本骨架。startWhenOff 决定
// 热点关着时是「什么都不做（reason=not-running）」还是「按原上游拉起」：
// 两者共用同一套双层状态判定，避免「还原成功」与「恢复成功」的口径漂移。
func buildWinRTTetherScript(originalProfile string, startWhenOff bool) string {
	var b strings.Builder
	b.WriteString(psHeader)
	b.WriteString(`
$ErrorActionPreference='Stop'
$ni=[Windows.Networking.Connectivity.NetworkInformation,Windows.Networking.Connectivity,ContentType=WindowsRuntime]
$tm=[Windows.Networking.NetworkOperators.NetworkOperatorTetheringManager,Windows.Networking.NetworkOperators,ContentType=WindowsRuntime]
$profiles=@($ni::GetConnectionProfiles())
$gateway = ` + psQuote(DefaultHotspotGateway) + `
function Get-HeatState {
  $found = ''
  foreach ($p in $profiles) {
    try {
      $st = [string]$tm::CreateFromConnectionProfile($p).TetheringState
      if (-not [string]::IsNullOrWhiteSpace($st)) {
        if ($st -eq 'On') { return 'On' }
        if ($found -eq '') { $found = $st }
      }
    } catch {}
  }
  return $found
}
function Test-HeatUp {
  $st = Get-HeatState
  if ($st -ne '') { return ($st -eq 'On') }
  $a = Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.IPAddress -eq $gateway } | Select-Object -First 1
  return ($null -ne $a)
}
function Test-HeatDown {
  $st = Get-HeatState
  if ($st -ne '') { return ($st -eq 'Off') }
  return (-not (Test-HeatUp))
}
function Wait-Heat([string]$want, [int]$ticks) {
  for ($i = 0; $i -lt $ticks; $i++) {
    $ok = $false
    if ($want -eq 'Up') { $ok = Test-HeatUp } else { $ok = Test-HeatDown }
    if ($ok) { return $true }
    Start-Sleep -Milliseconds 500
  }
  return $false
}
$restarted = $false
$reason = ''
$up = Test-HeatUp
`)
	if !startWhenOff {
		// 还原路径：热点本就关着就什么都不做（用户可能自己关的）。
		b.WriteString("if (-not $up) { $reason = 'not-running' }\n")
	}
	b.WriteString(`if ($reason -eq '') {
  $orig = `)
	b.WriteString(psQuote(originalProfile))
	b.WriteString(`
  if ($orig -eq '') { $reason = 'no-original-profile' } else {
    $target = $null
    foreach ($p in $profiles) { if ($p.ProfileName -eq $orig) { $target = $p; break } }
    if ($null -eq $target) { $reason = 'original-profile-missing' } else {
      # 确认目标可用之后才动热点：找不到原上游时先把热点停掉，就再也拉不
      # 起来了。$up 为真时（绑在 TUN 上）必须先解开，否则 start 会沿用旧绑定。
      if ($up) {
        foreach ($p in $profiles) { try { $null = $tm::CreateFromConnectionProfile($p).StopTetheringAsync(); break } catch {} }
        $null = Wait-Heat 'Down' 16
      }
      try { $null = $tm::CreateFromConnectionProfile($target).StartTetheringAsync() } catch {}
      if (Wait-Heat 'Up' 30) { $restarted = $true } else { $reason = 'start-failed (final: ' + (Get-HeatState) + '/' + (Test-HeatUp) + ')' }
    }
  }
}
ConvertTo-Json -InputObject ([pscustomobject]@{restarted=$restarted; reason=$reason}) -Compress
`)
	return b.String()
}

// WinRTSwitchResult 是切换脚本的输出。失败时脚本同样输出 JSON（而不是靠
// 退出码），调用方才能区分「停在准备阶段、热点分毫未动」和「已经停过热点」。
type WinRTSwitchResult struct {
	OK              bool   `json:"ok"`
	Stage           string `json:"stage"` // preflight | stopping | starting | recovered | done
	OriginalProfile string `json:"originalProfile"`
	Error           string `json:"error"`
}

// NeedsHotspotRecovery 报告失败后是否还需要调用方把热点拉回来：
//   - preflight：还没碰过热点，动它反而会制造新故障；
//   - recovered：脚本已就地回滚成功，热点已经在原上游上跑着，再恢复等于白停一次；
//   - 其余（stopping/starting/空）：可能已经停过，按最坏情况处理。
//
// 空 Stage 表示脚本没有输出（被超时杀掉/输出不可解析）。
func (result WinRTSwitchResult) NeedsHotspotRecovery() bool {
	switch result.Stage {
	case "preflight", "recovered", "done":
		return false
	}
	return true
}

// WinRTRestoreResult 是 BuildWinRTRestoreScript 输出的解析结果。
type WinRTRestoreResult struct {
	Restarted bool   `json:"restarted"`
	Reason    string `json:"reason"`
}

// WinRTProfileProbe 是 BuildWinRTProfileProbeScript 输出的解析结果。
type WinRTProfileProbe struct {
	OriginalProfile string `json:"originalProfile"`
}

// listInterfaceIPs 返回本机所有网卡上的 IPv4/IPv6 地址字符串。
// 变量形式便于单测注入。
var listInterfaceIPs = func() []string {
	ips := make([]string, 0, 8)
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err == nil {
				ips = append(ips, ip.String())
			}
		}
	}
	return ips
}

// HotspotActive 判断已切换共享的热点会话是否仍然在线：
// 以切换时记录的私网网关地址为准做本机地址探测（毫秒级、无子进程）。
// 无法探测（未记录地址）时保守返回 true，避免误报“已断开”。
func HotspotActive(meta *HotspotMeta) bool {
	if meta == nil || !meta.Applied {
		return true
	}
	if meta.PrivateIP == "" {
		return true
	}
	for _, ip := range listInterfaceIPs() {
		if ip == meta.PrivateIP {
			return true
		}
	}
	return false
}
