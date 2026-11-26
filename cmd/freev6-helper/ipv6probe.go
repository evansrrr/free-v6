package main

// 启动免流前的 IPv6 前置检查。全部在本机完成，不再请求任何回显 API。
//
// 判定分两层：
//  1. 本机有没有全局 IPv6 地址——net.InterfaceAddrs，纯本地、微秒级；
//  2. 每个地址能不能真的出网——绑定该地址做 TCP 连接（每个地址各测一轮）。
//
// 为什么不再调回显 API：那套做法要跨四家网络，受 DNS 解析、CORS、反爬校验页、
// 响应格式各自的抖动影响，任何一家慢都要靠竞速兜底；而校园网里（无 NAT66）它
// 返回的"公网地址"就是本机地址本身。真正决定免流能不能用的只有两件事：地址在
// 不在校园前缀内（前端判）、这个地址出站通不通（这里用一次 TCP 握手测）。
//
// 为什么按地址分别测：多网卡（校园 WiFi + 有线/热点/VPN）时系统会自己选源地址，
// 只测一次等于测了"某条链路能出去"，而免流要的是"校园那个地址能出去"。所以每个
// 候选地址都绑成源地址各测一轮，前端据此挑"校园前缀且出站可用"的那个。
//
// 已知边界：若出口侧做了 NAT66 / 代理，本机地址不再是公网地址，按本地地址判
// 校园前缀会误判。真遇到这种网络，再加"本地地址不在校园前缀时才查一次出口"
// 的兜底即可，但不要把它变成默认路径。

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// 单个目标的 TCP 连接预算。CERNET 内 RTT 通常 < 20ms，900ms 足够吸收抖动，
	// 又不会让"确实不通"拖住启动。
	ipv6EgressTimeout = 900 * time.Millisecond
	// 每个地址的重试轮数：停止免流拆掉 TUN 之后有一小段路由过渡态，一轮全败
	// 不代表出站真的不可用，重试比让用户再按一次更省事。
	ipv6EgressAttempts = 3
	ipv6EgressGap      = 250 * time.Millisecond
	// 给 scope 打标签时补问一次校外目标：竞速总是校内目标先回（2–6ms），
	// 不能凭"谁先赢"就说"只到得了校园"。校外可达时通常 < 120ms。
	ipv6ScopeTimeout = 600 * time.Millisecond
)

// 出站目标的口径。免流真正要连的是校外出口（WARP），所以 public 才是决定性
// 证据；只有 campus 可达时说明"包出得去但可能只在 CERNET 内"，值得提示。
const (
	scopeCampus = "campus"
	scopePublic = "public"
)

type egressTarget struct {
	Addr  string
	Scope string
}

// 连通性目标，全部是字面量地址——不发 DNS 查询。
var ipv6EgressTargets = []egressTarget{
	{"[2001:da8:215:4009::23]:443", scopeCampus}, // speedtest6.bupt.edu.cn
	{"[2400:3200::1]:443", scopePublic},          // AliDNS
	{"[2400:3200:baba::1]:443", scopePublic},     // AliDNS 备用
}

type ipv6Address struct {
	IP    string
	Alias string
}

// addressProbe 是单个地址的探测结论。Egress 只代表"这个源地址能出去"。
type addressProbe struct {
	IP         string `json:"ip"`
	Alias      string `json:"alias"`
	Egress     bool   `json:"egress"`
	RTTMs      int64  `json:"rttMs,omitempty"`
	Target     string `json:"target,omitempty"`
	Scope      string `json:"scope,omitempty"`
	Attempts   int    `json:"attempts,omitempty"`
	Unverified bool   `json:"unverified,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// 注入点：用例里固定地址、模拟连接成败，不依赖跑测试的那台机器有没有 IPv6。
var (
	listLocalIPv6 = localIPv6Addresses
	probeDialIPv6 = dialIPv6From
)

// probeLogf 是探测的日志出口；用例里换成 no-op，免得刷满 CI 输出。
var probeLogf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format, args...) }

// localIPv6Addresses 列出本机所有可用的全局单播 IPv6。
func localIPv6Addresses() []ipv6Address {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := make(map[string]bool, 8)
	addresses := make([]ipv6Address, 0, 4)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip := globalIPv6(addr)
			if ip == "" || seen[ip] {
				continue
			}
			seen[ip] = true
			addresses = append(addresses, ipv6Address{IP: ip, Alias: iface.Name})
		}
	}
	return addresses
}

// globalIPv6 取出 addr 里的全局单播 IPv6，不满足返回空串。
// 排除 IPv4、回环、链路本地（fe80::/10）、唯一本地地址（fc00::/7）与废弃的
// 站点本地地址（fec0::/10）——后三类出不了网，留着只会把"没有出口"掩盖成
// "有地址"。注意 Go 的 IsGlobalUnicast 对 fec0::/10 返回 true、IsPrivate 返回
// false，必须单独判，否则某个历史适配器分到 fec0 地址就会排到列表最前面。
func globalIPv6(addr net.Addr) string {
	ipnet, ok := addr.(*net.IPNet)
	if !ok {
		return ""
	}
	ip := ipnet.IP
	if ip.To4() != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return ""
	}
	if len(ip) == net.IPv6len && ip[0] == 0xfe && ip[1]&0xc0 == 0xc0 {
		return "" // fec0::/10 site-local
	}
	return ip.String()
}

// dialIPv6From 从指定源地址连目标；local 为 nil 时交给系统选源地址。
// 绑源地址是这套探测的关键：不绑的话多网卡下测的就不是校园那个地址。
func dialIPv6From(ctx context.Context, local net.IP, target string, timeout time.Duration) error {
	dialer := net.Dialer{Timeout: timeout}
	if local != nil {
		dialer.LocalAddr = &net.TCPAddr{IP: local}
	}
	conn, err := dialer.DialContext(ctx, "tcp6", target)
	if err != nil {
		return err
	}
	return conn.Close()
}

// ipv6Dialer 单独抽出来，是为了让"绑源地址"这件事能在没有 IPv6 的机器上被
// 断言（用例直接看 LocalAddr），而不是靠真的连一次去撞 CI 的网络条件。
func ipv6Dialer(local net.IP, timeout time.Duration) net.Dialer {
	dialer := net.Dialer{Timeout: timeout}
	if local != nil {
		dialer.LocalAddr = &net.TCPAddr{IP: local}
	}
	return dialer
}

// isBindFailure 判断失败是"源地址绑不上"而不是"连不通"。两者结论完全不同：
// 前者说明这台机器不支持绑源地址（需要退回系统选源），后者才是出站有问题。
func isBindFailure(err error) bool {
	var sysErr *os.SyscallError
	if errors.As(err, &sysErr) && strings.EqualFold(sysErr.Syscall, "bind") {
		return true
	}
	// 少数平台的错误不带 syscall 包装，直接是 Errno
	return errors.Is(err, syscall.EADDRNOTAVAIL)
}

// dialFunc 是连接函数。传参而不是直接读全局，用例才能各自持有一份，
// 不会互相干扰（全局替换时上一个用例的残留 goroutine 会污染下一个的计数）。
type dialFunc func(ctx context.Context, local net.IP, target string, timeout time.Duration) error

// checkAddressEgress 测单个源地址的出站能力：先按该地址绑源竞速三个目标，
// 全败且"绑不上"时退回系统选源再测一次（标注 Unverified，不至于误伤）。
func checkAddressEgress(ctx context.Context, addr ipv6Address) addressProbe {
	return probeAddressEgress(ctx, addr, probeDialIPv6)
}

func probeAddressEgress(ctx context.Context, addr ipv6Address, dial dialFunc) addressProbe {
	probe := addressProbe{IP: addr.IP, Alias: addr.Alias}
	local := net.ParseIP(addr.IP)
	for attempt := 1; attempt <= ipv6EgressAttempts; attempt++ {
		outcome, bindBlocked := raceEgressTargets(ctx, dial, local)
		probe.Attempts = attempt
		if outcome.OK {
			probe.Egress = true
			probe.RTTMs = outcome.RTT.Milliseconds()
			probe.Target = targetHost(outcome.Target)
			probe.Scope = resolveScope(ctx, dial, local, outcome.Scope)
			return probe
		}
		probe.Detail = outcome.Detail
		if bindBlocked {
			// 绑不上源地址不是"出站不通"的证据：退回系统选源，标为未验证。
			probe.Unverified = true
			probe.Detail = ""
			if fallback, _ := raceEgressTargets(ctx, dial, nil); fallback.OK {
				probe.Egress = true
				probe.RTTMs = fallback.RTT.Milliseconds()
				probe.Target = targetHost(fallback.Target)
				probe.Scope = resolveScope(ctx, dial, nil, fallback.Scope)
				return probe
			}
			return probe
		}
		if attempt == ipv6EgressAttempts {
			break
		}
		select {
		case <-time.After(ipv6EgressGap):
		case <-ctx.Done():
			return probe
		}
	}
	return probe
}

// resolveScope 把"竞速赢家"翻译成"能不能出 CERNET"：赢家是校内目标时再单独
// 问一次校外目标。这不是判定条件，只是给日志和用户提示一个准确的说法——把
// "先赢的那个在校内"当成"只到得了校内"，会让每次启动都误报一句提示。
func resolveScope(ctx context.Context, dial dialFunc, local net.IP, scope string) string {
	if scope != scopeCampus {
		return scope
	}
	publicTarget := ""
	for _, target := range ipv6EgressTargets {
		if target.Scope == scopePublic {
			publicTarget = target.Addr
			break
		}
	}
	if publicTarget == "" {
		return scope
	}
	if err := dial(ctx, local, publicTarget, ipv6ScopeTimeout); err == nil {
		return scopePublic
	}
	return scope
}

// raceEgressTargets 并发连所有目标，第一个成功即胜出，其余立即取消。
// bindBlocked 仅在"全部目标都败在 bind 上"时为真。
func raceEgressTargets(ctx context.Context, dial dialFunc, local net.IP) (egressResult, bool) {
	type outcome struct {
		target egressTarget
		rtt    time.Duration
		err    error
	}
	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan outcome, len(ipv6EgressTargets))
	for _, target := range ipv6EgressTargets {
		go func(target egressTarget) {
			started := time.Now()
			err := dial(raceCtx, local, target.Addr, ipv6EgressTimeout)
			select {
			case results <- outcome{target: target, rtt: time.Since(started), err: err}:
			case <-raceCtx.Done():
			}
		}(target)
	}

	failures := make([]string, 0, len(ipv6EgressTargets))
	bindFailures := 0
	for range ipv6EgressTargets {
		select {
		case out := <-results:
			if out.err == nil {
				cancel()
				return egressResult{OK: true, Target: out.target.Addr, Scope: out.target.Scope, RTT: out.rtt}, false
			}
			failures = append(failures, targetHost(out.target.Addr))
			if isBindFailure(out.err) {
				bindFailures++
			}
		case <-ctx.Done():
			return egressResult{Detail: "连接超时"}, false
		}
	}
	// 全部失败且全部是 bind 失败 → 这台机器不支持绑源地址，不是网络问题。
	bindBlocked := bindFailures > 0 && bindFailures == len(ipv6EgressTargets)
	if len(failures) == 0 {
		return egressResult{Detail: "连接超时"}, bindBlocked
	}
	return egressResult{Detail: strings.Join(failures, "、") + " 均不可达"}, bindBlocked
}

type egressResult struct {
	OK     bool
	Target string
	Scope  string
	RTT    time.Duration
	Detail string
}

func targetHost(target string) string {
	if host, _, err := net.SplitHostPort(target); err == nil {
		return "[" + host + "]"
	}
	return target
}

// GET /api/v1/ipv6/probe：本机 IPv6 前置检查。
// 条件不满足时仍回 200 + error（这是「网络不具备条件」，不是接口错误）。
func (h *helper) ipv6Probe(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	h.clearLeftoverTunRoutes("probe")

	addresses := listLocalIPv6()
	if len(addresses) == 0 {
		probeLogf("helper: ipv6 check: 本机没有全局 IPv6 地址\n")
		writeJSON(writer, http.StatusOK, map[string]any{
			"ok": true, "addresses": []addressProbe{}, "egress": false,
			"error": "本机没有全局 IPv6 地址（IPv6 未启用或未下发）",
		})
		return
	}

	// 每个地址各测一轮，互不阻塞；总耗时仍是一条 RTT 的量级。
	probes := make([]addressProbe, len(addresses))
	var wg sync.WaitGroup
	for index := range addresses {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			probes[index] = checkAddressEgress(request.Context(), addresses[index])
		}(index)
	}
	wg.Wait()

	// ip/source 给旧消费者用：优先取"能出站的那个地址"，避免多网卡时指向一条
	// 根本没用的链路。真正做判断的是 addresses 里的逐地址结论。
	primary := probes[0]
	for _, probe := range probes {
		if probe.Egress {
			primary = probe
			break
		}
	}
	response := map[string]any{
		"ok":        true,
		"ip":        primary.IP,
		"source":    primary.Alias,
		"addresses": probes,
	}
	anyEgress := false
	maxAttempts := 0
	for _, probe := range probes {
		if probe.Attempts > maxAttempts {
			maxAttempts = probe.Attempts
		}
		if !probe.Egress {
			continue
		}
		anyEgress = true
		if _, exists := response["egressTarget"]; !exists {
			response["egressTarget"] = probe.Target
			response["rttMs"] = probe.RTTMs
			response["egressScope"] = probe.Scope
		}
	}
	response["egress"] = anyEgress
	response["attempts"] = maxAttempts

	if anyEgress {
		probeLogf("helper: ipv6 check: %d 个地址，出站经 %s 用时 %dms\n",
			len(probes), response["egressTarget"], primary.RTTMs)
	} else {
		// 文案不用"本机没有 IPv6"：地址明明在，问题是出不去。
		detail := probes[0].Detail
		if detail == "" {
			detail = "全部地址均不可达"
		}
		response["error"] = "本机有 IPv6 地址但出站不通（" + detail + "）"
		probeLogf("helper: ipv6 check: %d 个地址，出站失败：%s\n", len(probes), detail)
	}
	writeJSON(writer, http.StatusOK, response)
}
