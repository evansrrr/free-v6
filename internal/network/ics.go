package network

import (
	"encoding/json"
	"errors"
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
	Path            string `json:"path"` // "ics" | "winrt"
	PrivateIP       string `json:"privateIP,omitempty"`
	OriginalProfile string `json:"originalProfile,omitempty"` // winrt 路径：切换前的上游连接配置
}

// ErrHotspotNotRunning 表示移动热点当前未运行。WinRT 切换脚本在热点关闭时
// 直接抛错；SwitchWinRT 把该情形归一化为本哨兵，供调用方映射为软性的
// "not-running" 结果（区别于真正的失败）。
var ErrHotspotNotRunning = errors.New("mobile hotspot not running")

// FindHotspotPrivate 返回共享基线里「热点私有侧」连接的显示名（ICS 的
// private 角色且处于共享中）。它直接复用 CaptureSharing 抓到的
// NetConnectionProps.Name，与 SwitchICS 脚本按名匹配的目标一致，因此比旧的
// Get-NetIPAddress InterfaceAlias 探测更准确，也省掉一次 PowerShell 冷启动。
// 找不到（热点未开、或新 WDI 驱动无独立虚拟适配器）时返回空串。
func FindHotspotPrivate(sharing []SharingSnapshot) string {
	for _, entry := range sharing {
		if entry.Kind == "private" && entry.Enabled && entry.Name != "" {
			return entry.Name
		}
	}
	return ""
}

// HotspotBandResult 是优先 5GHz 探测/设置的结果。
type HotspotBandResult struct {
	Supported bool   `json:"supported"`         // 网卡存在可写的 5GHz 频段偏好属性
	Applied   bool   `json:"applied"`           // 本次把频段偏好改成了 5GHz
	Already   bool   `json:"already"`           // 已是 5GHz，无需改动
	Reason    string `json:"reason,omitempty"`  // 不支持时的原因
	Value     string `json:"value,omitempty"`   // 设置的 5GHz 显示值
	Adapter   string `json:"adapter,omitempty"` // Wi-Fi 网卡名
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

// firstString 从解析出的 JSON 值里取字符串字段（容错：非字符串返回空）。
func firstString(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

// PowerShellSharingCaptureCommand 枚举全部 HNetShare 连接的共享状态，
// 输出 UTF-8 JSON 数组（中文网卡名依赖首行的编码设置）。它是 CaptureSharing
// 的兜底：COM 直调（hnshare_windows.go）失败或返回空时用它恢复基线抓取。
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

// hnshareConn 是一次 HNetShare 连接枚举读到的共享状态（纯数据，与 COM 解耦，
// 便于还原计划计算的单测）。
type hnshareConn struct {
	name    string
	guid    string
	device  string
	enabled bool
	kind    string // "public" | "private"
}

// restoreAction 描述对一个连接要执行的一次共享操作。
type restoreAction int

const (
	restoreNone    restoreAction = iota
	restoreDisable               // DisableSharing
	restoreEnablePublic          // EnableSharing(0)
	restoreEnablePrivate         // EnableSharing(1)
)

// kindToEnable 把共享角色映射成对应的 EnableSharing 动作。
func kindToEnable(kind string) restoreAction {
	if kind == "private" {
		return restoreEnablePrivate
	}
	return restoreEnablePublic
}

// restoreActions 计算一个当前连接要执行的还原动作序列（可能为空）。
// 规则与原 PowerShell 还原脚本一致：
//   · baseline 里有该 guid → 恢复到基线状态（enabled + kind）；
//   · baseline 里没有、且是 TUN（按名匹配）→ 若当前共享则禁用；
//   · 其它 → 不动（会话中途新建的其他共享不碰）。
func restoreActions(c hnshareConn, baseline []SharingSnapshot, tunName string) []restoreAction {
	var target *SharingSnapshot
	for i := range baseline {
		if baseline[i].Guid != "" && baseline[i].Guid == c.guid {
			target = &baseline[i]
			break
		}
	}
	if target != nil {
		if target.Enabled {
			if !c.enabled {
				return []restoreAction{kindToEnable(target.Kind)}
			}
			if c.kind != target.Kind {
				return []restoreAction{restoreDisable, kindToEnable(target.Kind)}
			}
			return nil
		}
		if c.enabled {
			return []restoreAction{restoreDisable}
		}
		return nil
	}
	if c.name == tunName && c.enabled {
		return []restoreAction{restoreDisable}
	}
	return nil
}

// PowerShellEnsure5GHzCommand 探测活动 Wi-Fi 网卡的频段偏好高级属性，并在
// 存在 5GHz 选项时幂等地把它设为「优先 5GHz」。属性名/显示值因厂商而异
// （Intel/部分 Realtek 叫 "Preferred Band"，值形如 "3. Prefer 5GHz band"），
// 因此按 ValidDisplayValues 是否含 5GHz 动态定位，而非硬编码关键字。
// 输出 UTF-8 JSON。此脚本是一次性/幂等设置，不是每轮询热路径。
const PowerShellEnsure5GHzCommand = psHeader + `
$ErrorActionPreference='Stop'
$wifi = Get-NetAdapter -Physical -ErrorAction SilentlyContinue | Where-Object { $_.Status -eq 'Up' -and ($_.MediaType -match '802.11' -or $_.InterfaceDescription -match 'Wi-Fi|Wireless|802.11') } | Select-Object -First 1
if ($null -eq $wifi) { throw 'no active Wi-Fi adapter' }
$band = $null; $five = $null
foreach ($p in (Get-NetAdapterAdvancedProperty -Name $wifi.Name -ErrorAction SilentlyContinue)) {
  $v = @($p.ValidDisplayValues) | Where-Object { $_ -match '5' -and $_ -match 'GHz' } | Select-Object -First 1
  if ($null -ne $v) { $band = $p; $five = [string]$v; break }
}
if ($null -eq $band) {
  ConvertTo-Json -InputObject ([pscustomobject]@{supported=$false; reason='no 5GHz band option'; adapter=[string]$wifi.Name}) -Compress
  exit 0
}
$already = ([string]$band.DisplayValue -eq $five)
if (-not $already) { Set-NetAdapterAdvancedProperty -Name $wifi.Name -DisplayName $band.DisplayName -DisplayValue $five }
ConvertTo-Json -InputObject ([pscustomobject]@{supported=$true; applied=(-not $already); already=$already; value=$five; adapter=[string]$wifi.Name}) -Compress
`

// BuildICSSwitchScript 构造经典路径（HNetShare COM）的拓扑切换脚本：
// FreeV6TUN → PUBLIC（上游，自动取消原 PUBLIC），热点私有侧网卡 → PRIVATE。
// 任何一步失败都以非零退出码抛错，由调用方回滚。它是 SwitchICS 的兜底，
// 当 COM 直调失败时用脚本重试（脚本内部幂等：已处于目标角色则跳过）。
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
// 单连接失败收集后以非零退出码退出，输出还原结果 JSON。它是 RestoreICSSharing
// 的兜底，COM 直调失败时用它重试。
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

// BuildWinRTSwitchScript 构造新 WDI 兜底路径：停掉现有 tethering，
// 改绑到 FreeV6TUN 连接配置重新启动热点。切换前记录原上游（物理 WLAN），
// 新上游启动失败时在脚本内回滚重启原上游，回滚结果写进错误消息。
//
// 成败判定是双层的，任何一层可用即可：优先 TetheringState（读到非空值），
// 投影读空的机器退回热点网关 IP 探测（与 ICS 网关地址 192.168.137.1 同一
// 信号，已在故障机验证可用）。绝不读异步 op 的 .Status/GetResults —— 部分
// 机器上该属性为空，曾把进行中的启动误判为“未完成”的假失败。定位 tunP/原
// 上游必须在任何停止动作之前完成。
func BuildWinRTSwitchScript() string {
	return psHeader + `
$ErrorActionPreference='Stop'
$ni=[Windows.Networking.Connectivity.NetworkInformation,Windows.Networking.Connectivity,ContentType=WindowsRuntime]
$tm=[Windows.Networking.NetworkOperators.NetworkOperatorTetheringManager,Windows.Networking.NetworkOperators,ContentType=WindowsRuntime]
$profiles=@($ni::GetConnectionProfiles())
$tunName = ` + psQuote(TunDeviceName) + `
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
# 1) 任何停止动作之前先定位原上游与 TUN 连接：找不到就原地失败，热点不动。
$wlan = Get-NetAdapter -Physical -ErrorAction SilentlyContinue | Where-Object { $_.Status -eq 'Up' -and $_.InterfaceDescription -notmatch 'Wi-Fi Direct|Virtual|Loopback' } | Select-Object -First 1
$orig = ''
if ($null -ne $wlan) { foreach ($p in $profiles) { if ($p.ProfileName -eq $wlan.Name) { $orig = $p.ProfileName; break } } }
$tunP = $null
foreach ($p in $profiles) { if ($p.ProfileName -eq $tunName) { $tunP = $p; break } }
if ($null -eq $tunP) { throw ($tunName + ' connection profile not found') }
if (-not (Test-HeatUp)) { throw '移动热点未处于运行状态（tethering 未启动）' }
# 2) 重启热点：先停（双层判定确认；超时确认不了也继续，绝不因此中止 ——
#    半途而废会把热点丢在关闭状态）。
foreach ($p in $profiles) { try { $null = $tm::CreateFromConnectionProfile($p).StopTetheringAsync(); break } catch {} }
$null = Wait-Heat 'Down' 16
# 3) 绑定 TUN 重新启动：双层判定（最多 20s）。
try { $null = $tm::CreateFromConnectionProfile($tunP).StartTetheringAsync() } catch {}
if (-not (Wait-Heat 'Up' 40)) {
  $recovered = $false
  if ($orig -ne '') {
    foreach ($p in $profiles) {
      if ($p.ProfileName -eq $orig) {
        try { $null = $tm::CreateFromConnectionProfile($p).StartTetheringAsync() } catch {}
        break
      }
    }
    $recovered = Wait-Heat 'Up' 20
  }
  $final = Get-HeatLabel
  if ($recovered) { throw ('启动热点未成功（最终状态: ' + $final + '）；已恢复原上游') }
  throw ('启动热点未成功（最终状态: ' + $final + '）；未能恢复原上游，请手动重开热点')
}
ConvertTo-Json -InputObject ([pscustomobject]@{originalProfile=$orig}) -Compress
`
}

// BuildWinRTRestoreScript 构造 winrt 路径的还原：停掉绑在 TUN 上的
// tethering，再用切换前记录的上游连接配置重新拉起热点。
// 输出 {restarted, reason}；reason 为 not-running 表示热点本就关闭（不算警告）。
// 与切换脚本同理：双层判定（TetheringState 优先，读空退回网关 IP 探测），
// 不读异步 op 的 .Status/GetResults。
func BuildWinRTRestoreScript(originalProfile string) string {
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
# 绑在 TUN 上的热点必须尝试解开：判定失败（两层都读不到）时也按在跑处理。
if (-not (Test-HeatUp)) { $reason = 'not-running' } else {
  foreach ($p in $profiles) { try { $null = $tm::CreateFromConnectionProfile($p).StopTetheringAsync(); break } catch {} }
  $null = Wait-Heat 'Down' 16
  $orig = `)
	b.WriteString(psQuote(originalProfile))
	b.WriteString(`
  if ($orig -eq '') { $reason = 'no-original-profile' } else {
    $target = $null
    foreach ($p in $profiles) { if ($p.ProfileName -eq $orig) { $target = $p; break } }
    if ($null -eq $target) { $reason = 'original-profile-missing' } else {
      try { $null = $tm::CreateFromConnectionProfile($target).StartTetheringAsync() } catch {}
      if (Wait-Heat 'Up' 30) { $restarted = $true } else { $reason = 'start-failed (final: ' + (Get-HeatState) + '/' + (Test-HeatUp) + ')' }
    }
  }
}
ConvertTo-Json -InputObject ([pscustomobject]@{restarted=$restarted; reason=$reason}) -Compress
`)
	return b.String()
}

// WinRTRestoreResult 是 BuildWinRTRestoreScript 输出的解析结果。
type WinRTRestoreResult struct {
	Restarted bool   `json:"restarted"`
	Reason    string `json:"reason"`
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
