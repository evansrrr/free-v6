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
	Path            string `json:"path"` // "ics" | "winrt"
	PrivateIP       string `json:"privateIP,omitempty"`
	OriginalProfile string `json:"originalProfile,omitempty"` // winrt 路径：切换前的上游连接配置
}

// HotspotStatus 是 DetectHotspot 的探测结果。
type HotspotStatus struct {
	Active       bool   `json:"active"`
	PrivateAlias string `json:"privateAlias"` // 持有 ICS 网关地址的网卡显示名（经典路径）
	PrivateIP    string `json:"privateIP"`
	Tethering    bool   `json:"tethering"`    // WinRT tethering 处于 On
	TunBound     bool   `json:"tunBound"`     // tethering 已绑在 FreeV6TUN 上（残留/重复启动）
	PhysicalProfile string `json:"physProfile"` // 物理 WLAN 连接配置名（winrt 还原的原上游提示）
}

// ICSSwitchResult 是经典路径切换脚本的结构化输出。
type ICSSwitchResult struct {
	Ok          bool   `json:"ok"`
	TunPublic   bool   `json:"tunPublic"`
	PrivPrivate bool   `json:"privPrivate"`
	Error       string `json:"error"`
}

// WinRTSwitchResult 是 WinRT 切换脚本的结构化输出。
type WinRTSwitchResult struct {
	OriginalProfile string `json:"originalProfile"`
	Started         bool   `json:"started"`
}

// ICSApplied 纯函数复核：共享状态快照里 TUN 是否已是 public 且热点私有侧
// 是否已是 private。脚本自报与独立复核共用这一判据。
func ICSApplied(sharing []SharingSnapshot, privateAlias string) bool {
	tunPublic := false
	privPrivate := privateAlias == "" // 无私有侧网卡时只看 TUN
	for _, entry := range sharing {
		if entry.Name == TunDeviceName && entry.Enabled && entry.Kind == "public" {
			tunPublic = true
		}
		if privateAlias != "" && entry.Name == privateAlias && entry.Enabled && entry.Kind == "private" {
			privPrivate = true
		}
	}
	return tunPublic && privPrivate
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

// psSetShareFunc 是 switch/restore 脚本共用的共享设置函数：每次尝试前重新
// 读取真实状态，带退避重试并核验目标状态 —— 0x80040201（“An event was
// unable to invoke any of the subscribers”）这类 ICS 瞬态事件错误（状态可能
// 已变、只是事件广播失败）在重试 + 状态核验下不再造成假失败。
const psSetShareFunc = `
function Test-Share($m, $c, [bool]$wantEnabled, [int]$wantType) {
  try {
    $s = $m.INetSharingConfigurationForINetConnection($c)
    if (-not $wantEnabled) { return (-not [bool]$s.SharingEnabled) }
    return ([bool]$s.SharingEnabled -and [int]$s.SharingType -eq $wantType)
  } catch { return $false }
}
function Set-Share($m, $c, [bool]$wantEnabled, [int]$wantType, [string]$label) {
  $last = ''
  for ($i = 0; $i -lt 4; $i++) {
    if (Test-Share $m $c $wantEnabled $wantType) { return $null }
    try {
      $s = $m.INetSharingConfigurationForINetConnection($c)
      if ([bool]$s.SharingEnabled -and -not ($wantEnabled -and [int]$s.SharingType -eq $wantType)) {
        $s.DisableSharing()
        Start-Sleep -Milliseconds 500
      }
      $s2 = $m.INetSharingConfigurationForINetConnection($c)
      if ($wantEnabled) { $s2.EnableSharing($wantType) } else { $s2.DisableSharing() }
      $last = ''
    } catch { $last = $_.Exception.Message }
    Start-Sleep -Milliseconds 900
  }
  if (Test-Share $m $c $wantEnabled $wantType) { return $null }
  if ($last -ne '') { return ($label + ': ' + $last) }
  return ($label + ': 状态未达到目标')
}
`

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
// 也会输出对象，两种形状都要接受（与 ParsePowerShellSnapshot 同一约定）。
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
$active=$false; $alias=''; $ip=''; $tether=$false; $tunBound=$false; $phys=''
$a = Get-NetIPAddress -AddressFamily IPv4 | Where-Object { $_.IPAddress -eq %s } | Select-Object -First 1
if ($null -ne $a) { $active=$true; $alias=[string]$a.InterfaceAlias; $ip=[string]$a.IPAddress }
try {
  $ni=[Windows.Networking.Connectivity.NetworkInformation,Windows.Networking.Connectivity,ContentType=WindowsRuntime]
  $tm=[Windows.Networking.NetworkOperators.NetworkOperatorTetheringManager,Windows.Networking.NetworkOperators,ContentType=WindowsRuntime]
  $tunName = %s
  $states = @{}
  foreach ($p in $ni::GetConnectionProfiles()) {
    try { $states[[string]$p.ProfileName] = [string]($tm::CreateFromConnectionProfile($p)).TetheringState } catch {}
  }
  foreach ($k in $states.Keys) { if ($states[$k] -eq 'On') { $tether=$true; $active=$true } }
  # tunBound = TUN 报 On 且另有 profile 报 Off：全局语义下两者同步（恒 false，保守不误判），
  # 按 profile 语义下准确反映“上游已绑在 TUN”。
  if ($states[$tunName] -eq 'On') {
    foreach ($k in $states.Keys) { if ($k -ne $tunName -and $states[$k] -eq 'Off') { $tunBound=$true; break } }
  }
  $wlan = Get-NetAdapter -Physical -ErrorAction SilentlyContinue | Where-Object { $_.Status -eq 'Up' -and $_.InterfaceDescription -notmatch 'Wi-Fi Direct|Virtual|Loopback' } | Select-Object -First 1
  if ($null -ne $wlan) { foreach ($p in $ni::GetConnectionProfiles()) { if ([string]$p.ProfileName -eq $wlan.Name) { $phys=[string]$p.ProfileName; break } } }
} catch {}
ConvertTo-Json -InputObject ([pscustomobject]@{active=$active; privateAlias=$alias; privateIP=$ip; tethering=$tether; tunBound=$tunBound; physProfile=$phys}) -Compress`

// BuildHotspotDetectCommand 返回带网关地址与 TUN 名参数的探测脚本。
func BuildHotspotDetectCommand() string {
	return fmt.Sprintf(PowerShellHotspotDetectCommand, psQuote(DefaultHotspotGateway), psQuote(TunDeviceName))
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
// 输出结构化 JSON {ok,tunPublic,privPrivate,error}：所有状态变化都经
// Set-Share 重试核验（容忍 0x80040201 瞬态错误），脚本本身尽量不抛错，
// 由调用方按 ok/error 决策。
func BuildICSSwitchScript(privateAlias string) string {
	var b strings.Builder
	b.WriteString(psHeader)
	b.WriteString("\n$ErrorActionPreference='SilentlyContinue'\n")
	b.WriteString("$m = New-Object -ComObject HNetCfg.HNetShare\n")
	b.WriteString(psSetShareFunc)
	b.WriteString("function Find([string]$name) { foreach ($c in $m.EnumEveryConnection) { if ($m.NetConnectionProps($c).Name -eq $name) { return $c } }; return $null }\n")
	fmt.Fprintf(&b, "$tun = Find %s\n", psQuote(TunDeviceName))
	fmt.Fprintf(&b, "$priv = Find %s\n", psQuote(privateAlias))
	b.WriteString("$errs = @()\n")
	fmt.Fprintf(&b, "if ($null -eq $tun) { $errs += %s }\n", psQuote("TUN connection "+TunDeviceName+" not found"))
	fmt.Fprintf(&b, "if ($null -eq $priv) { $errs += %s }\n", psQuote("hotspot connection '"+privateAlias+"' not found"))
	b.WriteString("if ($null -ne $tun) { $e = Set-Share $m $tun $true 0 'TUN'; if ($e) { $errs += $e } }\n")
	b.WriteString("if ($null -ne $priv) { $e = Set-Share $m $priv $true 1 'private'; if ($e) { $errs += $e } }\n")
	b.WriteString("$tunOK = $false; $privOK = $false\n")
	b.WriteString("if ($null -ne $tun) { $tunOK = Test-Share $m $tun $true 0 }\n")
	b.WriteString("if ($null -ne $priv) { $privOK = Test-Share $m $priv $true 1 }\n")
	b.WriteString("ConvertTo-Json -InputObject ([pscustomobject]@{ok=($tunOK -and $privOK); tunPublic=$tunOK; privPrivate=$privOK; error=($errs -join '; ')}) -Compress\n")
	return b.String()
}

// BuildICSRestoreScript 构造还原脚本：按快照把每个连接恢复到原角色（带
// 重试核验）；快照里没有的连接只有 FreeV6TUN 会被禁用共享。
// 输出 {errors:[...]}；调用方按 errors 是否为空判定成败。
func BuildICSRestoreScript(sharing []SharingSnapshot, tunName string) string {
	var b strings.Builder
	b.WriteString(psHeader)
	b.WriteString("\n$ErrorActionPreference='SilentlyContinue'\n")
	b.WriteString("$m = New-Object -ComObject HNetCfg.HNetShare\n")
	b.WriteString(psSetShareFunc)
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
	b.WriteString("  $match = $null\n")
	b.WriteString("  foreach ($w in $want) { if ($w.guid -eq $p.Guid) { $match = $w; break } }\n")
	b.WriteString("  if ($null -ne $match) {\n")
	b.WriteString("    $e = Set-Share $m $c ([bool]$match.e) ([int]$match.t) $p.Name\n")
	b.WriteString("    if ($e) { $errors += $e }\n")
	b.WriteString("  } elseif ($p.Name -eq " + psQuote(tunName) + ") {\n")
	b.WriteString("    $e = Set-Share $m $c $false 0 $p.Name\n")
	b.WriteString("    if ($e) { $errors += $e }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	b.WriteString("ConvertTo-Json -InputObject ([pscustomobject]@{errors=$errors}) -Compress\n")
	return b.String()
}

// BuildWinRTSwitchScript 构造新 WDI 兜底路径：停掉现有 tethering，
// 改绑到 FreeV6TUN 连接配置重新启动热点。切换前记录原上游。
// 成功判定完全基于 TetheringState 状态轮询（不信任 async op.Status —— 实测
// 曾出现 Status 为空串导致假失败，而 tethering 实际已启动）；启动失败时先
// 核验真实状态再回滚重启原上游，避免误杀已成功的切换。
func BuildWinRTSwitchScript() string {
	return psHeader + `
$ErrorActionPreference='Stop'
$ni=[Windows.Networking.Connectivity.NetworkInformation,Windows.Networking.Connectivity,ContentType=WindowsRuntime]
$tm=[Windows.Networking.NetworkOperators.NetworkOperatorTetheringManager,Windows.Networking.NetworkOperators,ContentType=WindowsRuntime]
$profiles=@($ni::GetConnectionProfiles())
function Wait-State($mgr, [string]$want, [int]$seconds) {
  $deadline = (Get-Date).AddSeconds($seconds)
  while ((Get-Date) -lt $deadline) {
    if ([string]$mgr.TetheringState -eq $want) { return $true }
    Start-Sleep -Milliseconds 500
  }
  return ([string]$mgr.TetheringState -eq $want)
}
$wlan = Get-NetAdapter -Physical -ErrorAction SilentlyContinue | Where-Object { $_.Status -eq 'Up' -and $_.InterfaceDescription -notmatch 'Wi-Fi Direct|Virtual|Loopback' } | Select-Object -First 1
$orig = ''
if ($null -ne $wlan) { foreach ($p in $profiles) { if ([string]$p.ProfileName -eq $wlan.Name) { $orig = [string]$p.ProfileName; break } } }
$stopped = $false
foreach ($p in $profiles) {
  $mgr = $null
  try { $mgr = $tm::CreateFromConnectionProfile($p) } catch { continue }
  if ([string]$mgr.TetheringState -ne 'Off') {
    $null = $mgr.StopTetheringAsync()
    if (Wait-State $mgr 'Off' 30) { $stopped = $true }
    break
  }
}
if (-not $stopped) { throw '移动热点未处于运行状态或停止超时' }
$tunName = ` + psQuote(TunDeviceName) + `
$tunP = $null
foreach ($p in $profiles) { if ([string]$p.ProfileName -eq $tunName) { $tunP = $p; break } }
if ($null -eq $tunP) { throw ($tunName + ' connection profile not found') }
$mgr2 = $tm::CreateFromConnectionProfile($tunP)
$startErr = ''
try { $null = $mgr2.StartTetheringAsync() } catch { $startErr = $_.Exception.Message }
if (-not (Wait-State $mgr2 'On' 60)) {
  # 状态语义兜底：扫描所有 profile，任一报 On 即认为 tethering 已起来
  $anyOn = $false
  foreach ($p in $profiles) {
    try { $mm = $tm::CreateFromConnectionProfile($p); if ([string]$mm.TetheringState -eq 'On') { $anyOn = $true; break } } catch {}
  }
  if (-not $anyOn) {
    $state = [string]$mgr2.TetheringState
    if ($orig -ne '') {
      foreach ($p in $profiles) { if ([string]$p.ProfileName -eq $orig) { try { $om = $tm::CreateFromConnectionProfile($p); $null = $om.StartTetheringAsync(); $null = Wait-State $om 'On' 30 } catch {}; break } }
    }
    throw ('启动热点失败: TetheringState=' + $state + ' ' + $startErr)
  }
}
ConvertTo-Json -InputObject ([pscustomobject]@{originalProfile=$orig; started=$true}) -Compress
`
}

// BuildWinRTRestoreScript 构造 winrt 路径的还原：停掉绑在 TUN 上的
// tethering，再用切换前记录的上游连接配置重新拉起热点。
// 输出 {restarted, reason}；reason 为 not-running 表示热点本就关闭（不算警告）。
func BuildWinRTRestoreScript(originalProfile string) string {
	var b strings.Builder
	b.WriteString(psHeader)
	b.WriteString(`
$ErrorActionPreference='Stop'
$ni=[Windows.Networking.Connectivity.NetworkInformation,Windows.Networking.Connectivity,ContentType=WindowsRuntime]
$tm=[Windows.Networking.NetworkOperators.NetworkOperatorTetheringManager,Windows.Networking.NetworkOperators,ContentType=WindowsRuntime]
$profiles=@($ni::GetConnectionProfiles())
function Wait-State($mgr, [string]$want, [int]$seconds) {
  $deadline = (Get-Date).AddSeconds($seconds)
  while ((Get-Date) -lt $deadline) {
    if ([string]$mgr.TetheringState -eq $want) { return $true }
    Start-Sleep -Milliseconds 500
  }
  return ([string]$mgr.TetheringState -eq $want)
}
$running = $false
$activeMgr = $null
foreach ($p in $profiles) {
  try {
    $mgr = $tm::CreateFromConnectionProfile($p)
    if ([string]$mgr.TetheringState -ne 'Off') { $running = $true; $activeMgr = $mgr; break }
  } catch {}
}
$restarted = $false
$reason = ''
if (-not $running) { $reason = 'not-running' } else {
  # 停止也按状态轮询核验；停不下来则如实报告，不能假装成功
  $null = $activeMgr.StopTetheringAsync()
  if (-not (Wait-State $activeMgr 'Off' 30)) { $reason = 'stop-timeout' } else {
  $orig = `)
	b.WriteString(psQuote(originalProfile))
	b.WriteString(`
  if ($orig -eq '') { $reason = 'no-original-profile' } else {
    $target = $null
    foreach ($p in $profiles) { if ([string]$p.ProfileName -eq $orig) { $target = $p; break } }
    if ($null -eq $target) { $reason = 'original-profile-missing' } else {
      $mgr2 = $tm::CreateFromConnectionProfile($target)
      $startErr = ''
      try { $null = $mgr2.StartTetheringAsync() } catch { $startErr = $_.Exception.Message }
      if (Wait-State $mgr2 'On' 45) { $restarted = $true } else { $reason = 'start-failed: TetheringState=' + [string]$mgr2.TetheringState + ' ' + $startErr }
    }
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
