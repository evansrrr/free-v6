package main

// 启动免流前的 IPv6 前置检查。全部在本机完成，不再请求任何回显 API。
//
// 判定分两层：
//  1. 本机有没有全局 IPv6 地址——net.InterfaceAddrs，纯本地、微秒级；
//  2. 这些地址能不能真的出网——对几个 IPv6 字面量并发做 TCP 连接，一条 RTT。
//
// 为什么不再调回显 API：那套做法要跨四家网络，受 DNS 解析、CORS、反爬校验页、
// 响应格式各自的抖动影响，任何一家慢都要靠竞速兜底；而校园网里（无 NAT66）它
// 返回的"公网地址"就是本机地址本身。真正决定免流能不能用的只有两件事：地址在
// 不在校园前缀内（前端判）、出站通不通（这里用一次 TCP 握手测）。两件事都能在
// 本地毫秒级测完，也就没有"检测失败但网络其实是好的"这个类别了。
//
// 已知边界：若出口侧做了 NAT66 / 代理，本机地址不再是公网地址，按本地地址判
// 校园前缀会误判。真遇到这种网络，再加"本地地址不在校园前缀时才查一次出口"
// 的兜底即可，但不要把它变成默认路径。

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yourname/freev6/internal/mihomo"
	"github.com/yourname/freev6/internal/network"
)

const (
	// 单个目标的 TCP 连接预算。CERNET 内 RTT 通常 < 20ms，900ms 足够吸收抖动，
	// 又不会让"确实不通"拖住启动。
	ipv6EgressTimeout = 900 * time.Millisecond
	// 含重试的总轮数：停止免流拆掉 TUN 之后有一小段路由过渡态，一轮全败不代表
	// 出站真的不可用，重试比让用户再按一次更省事。
	ipv6EgressAttempts = 3
	ipv6EgressGap      = 250 * time.Millisecond
)

// 连通性目标，全部是字面量地址——不发 DNS 查询。
// 第一个在 CERNET 内（免流实际要连的就是校园 IPv6 出口），后两个是校外公共
// 解析器，用来区分"只到校园"和"能出 CERNET"。任一成功即视为出站可用。
var ipv6EgressTargets = []string{
	"[2001:da8:215:4009::23]:443", // speedtest6.bupt.edu.cn
	"[2400:3200::1]:443",          // AliDNS
	"[2400:3200:baba::1]:443",     // AliDNS 备用
}

type ipv6Address struct {
	IP    string
	Alias string
}

// 注入点：用例里固定地址、模拟连接成败，不依赖跑测试的那台机器有没有 IPv6。
var (
	listLocalIPv6 = localIPv6Addresses
	probeDialIPv6 = dialIPv6Once
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
// 排除 IPv4、回环、链路本地（fe80::/10）与唯一本地地址（fc00::/7）——后两类
// 出不了网，留着只会把"没有出口"掩盖成"有地址"。
func globalIPv6(addr net.Addr) string {
	ipnet, ok := addr.(*net.IPNet)
	if !ok {
		return ""
	}
	ip := ipnet.IP
	if ip.To4() != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return ""
	}
	return ip.String()
}

func dialIPv6Once(ctx context.Context, target string, timeout time.Duration) error {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp6", target)
	if err != nil {
		return err
	}
	return conn.Close()
}

type egressResult struct {
	OK       bool
	Target   string
	RTT      time.Duration
	Attempts int
	Detail   string
}

// checkIPv6Egress 用 TCP 握手判断 IPv6 出站是否可用。
//
// 只连不打字：三次握手成功就足以证明"包出得去、回得来"。不像 HTTP 回显还要
// 经历 DNS、TLS、反爬和响应体解析——那每一步都曾经把可用的网络判成故障。
func checkIPv6Egress(ctx context.Context) egressResult {
	var detail string
	for attempt := 1; attempt <= ipv6EgressAttempts; attempt++ {
		result, ok := raceEgressTargets(ctx)
		if ok {
			result.Attempts = attempt
			return result
		}
		detail = result.Detail
		if attempt == ipv6EgressAttempts {
			break
		}
		select {
		case <-time.After(ipv6EgressGap):
		case <-ctx.Done():
			return egressResult{Attempts: attempt, Detail: detail}
		}
	}
	return egressResult{Attempts: ipv6EgressAttempts, Detail: detail}
}

// raceEgressTargets 并发连所有目标，第一个成功即胜出，其余立即取消。
func raceEgressTargets(ctx context.Context) (egressResult, bool) {
	type outcome struct {
		target string
		rtt    time.Duration
		err    error
	}
	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan outcome, len(ipv6EgressTargets))
	for _, target := range ipv6EgressTargets {
		go func(target string) {
			started := time.Now()
			err := probeDialIPv6(raceCtx, target, ipv6EgressTimeout)
			select {
			case results <- outcome{target: target, rtt: time.Since(started), err: err}:
			case <-raceCtx.Done():
			}
		}(target)
	}

	failures := make([]string, 0, len(ipv6EgressTargets))
	for range ipv6EgressTargets {
		select {
		case out := <-results:
			if out.err == nil {
				cancel()
				return egressResult{OK: true, Target: out.target, RTT: out.rtt}, true
			}
			failures = append(failures, targetHost(out.target))
		case <-ctx.Done():
			return egressResult{Detail: "连接超时"}, false
		}
	}
	if len(failures) == 0 {
		return egressResult{Detail: "连接超时"}, false
	}
	return egressResult{Detail: strings.Join(failures, "、") + " 均不可达"}, false
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
	clearLeftoverTunRoutes(h.root)

	addresses := listLocalIPv6()
	if len(addresses) == 0 {
		probeLogf("helper: ipv6 check: 本机没有全局 IPv6 地址\n")
		writeJSON(writer, http.StatusOK, map[string]any{
			"ok": true, "addresses": []string{}, "egress": false,
			"error": "本机没有全局 IPv6 地址（IPv6 未启用或未下发）",
		})
		return
	}

	ips := make([]string, len(addresses))
	for index, address := range addresses {
		ips[index] = address.IP
	}
	egress := checkIPv6Egress(request.Context())
	response := map[string]any{
		"ok":        true,
		"ip":        addresses[0].IP,
		"addresses": ips,
		"source":    addresses[0].Alias,
		"egress":    egress.OK,
		"attempts":  egress.Attempts,
	}
	if egress.OK {
		response["egressTarget"] = targetHost(egress.Target)
		response["rttMs"] = egress.RTT.Milliseconds()
		probeLogf("helper: ipv6 check: %d 个地址，出站经 %s 用时 %v\n",
			len(ips), targetHost(egress.Target), egress.RTT.Round(time.Millisecond))
	} else {
		response["error"] = "本机有 IPv6 地址但出站不通（" + egress.Detail + "）"
		probeLogf("helper: ipv6 check: %d 个地址，出站失败：%s\n", len(ips), egress.Detail)
	}
	writeJSON(writer, http.StatusOK, response)
}

// clearLeftoverTunRoutes 清理上一次异常停止留下的 TUN 残留路由：没有会话的
// FreeV6TUN 会把出站全吸走，而这种残留不会自愈。免流没在跑才清。
func clearLeftoverTunRoutes(root string) {
	pidPath := filepath.Join(root, "state", "mihomo.pid")
	if _, running, err := mihomo.Status(pidPath); err != nil || running {
		return
	}
	if !network.AdapterPresent(network.TunDeviceName) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if removed := network.ClearTunRoutes(ctx, network.TunDeviceName); removed > 0 {
		probeLogf("helper: ipv6 check: 清理残留 %s 路由 %d 条\n", network.TunDeviceName, removed)
	}
}
