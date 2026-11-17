package main

// 启动免流前的 IPv6 出口探测。
//
// 前端原先直接在 WebView 里 fetch api-ipv6.ip.sb：单一地址抖一下（超时、限流、
// 5xx）就让一个本来能用的校园 IPv6 被判成「不支持免流」，日志里只剩
// "signal is aborted without reason"。探测搬到 helper 有两个原因：
//  1. 没有 CORS 与混合内容限制——高校 speedtest 后端（getIP.php）普遍不带
//     Access-Control-Allow-Origin，WebView 里根本读不到响应；
//  2. 强制 tcp6 拨号，这正是要验证的东西：双栈主机不会因为走了 IPv4 而回一个
//     IPv4 地址，把「有 IPv6 出口」误判成「没有」。
//
// 多个地址并行竞速，先给出合法 IPv6 的胜出，其余立即取消。
//
// 另有一个坑：刚停完免流的几秒里，Windows 还在拆 FreeV6TUN、默认路由也没收敛，
// 这时任何 IPv6 出站都会失败。只问一次，就会把「等两秒就好」判成「本机没有出口」
// ——所以一轮全败且本机确实配了 IPv6 时按预算重试（见 probeIPv6Sources）。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/yourname/freev6/internal/mihomo"
	"github.com/yourname/freev6/internal/network"
)

const (
	// 单轮竞速：几个地址并行，正常情况下 <300ms 出结果。
	ipv6ProbeRoundTimeout = 4 * time.Second
	// 含重试的总预算。停止免流后 Windows 要拆掉 TUN 适配器、重新收敛默认路由，
	// 这几秒里 IPv6 出站全废——只问一次必然把「刚停完免流」误判成「没有出口」。
	ipv6ProbeBudget   = 12 * time.Second
	ipv6ProbeRetryGap = 1200 * time.Millisecond
)

// 检测地址。三所高校的 speedtest 后端在 CERNET2 内往返通常远小于 200ms，
// ipv6.testipv6.cn 是校外的兜底（JSONP，必须带 callback 才会回正文）。
var ipv6EchoSources = []string{
	"https://speedtest6.bupt.edu.cn/getIP.php",
	"https://test6.ustc.edu.cn/backend/getIP.php",
	"https://ipv6.testipv6.cn/ip/?callback=_freev6&testdomain=testipv6.cn",
	"https://fs.nju.edu.cn/speed/getIP.php",
}

// 探测专用客户端：只走 IPv6，且不读系统代理——免流运行时系统代理指向 mihomo，
// 走代理只会拿到代理出口的地址，测不出本机 IPv6。
var ipv6ProbeClient = func() *http.Client {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	return &http.Client{
		Timeout: ipv6ProbeRoundTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, "tcp6", addr)
			},
			TLSHandshakeTimeout:   3 * time.Second,
			ResponseHeaderTimeout: 4 * time.Second,
			DisableKeepAlives:     true,
		},
	}
}()

type ipv6ProbeResult struct {
	IP     string
	Source string
}

// 响应正文里第一个全球单播 IPv6。地址在正文里总是原样出现，无论是纯文本
// （ip.sb）、librespeed 的 getIP.php JSON（processedString: "2408:… - ISP"）
// 还是 JSONP 包装，所以扫原文比拼每家的字段名更稳。
var ipv6Candidate = regexp.MustCompile(`(?i)[0-9a-f]{0,4}(?::[0-9a-f]{0,4}){2,7}`)

func extractIPv6(body string) string {
	for _, candidate := range ipv6Candidate.FindAllString(body, -1) {
		// 句末/分隔符留下的尾冒号会破坏解析，'::' 结尾除外
		for strings.HasSuffix(candidate, ":") && !strings.HasSuffix(candidate, "::") {
			candidate = strings.TrimSuffix(candidate, ":")
		}
		ip := net.ParseIP(candidate)
		// IsGlobalUnicast 不排除 ULA（fc00::/7），IsPrivate 补上；只要全球单播，
		// 排掉 ::1 / fe80:: / fd00::
		if ip == nil || ip.To4() != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
			continue
		}
		return ip.String()
	}
	return ""
}

// 探测失败的原因。noPath 表示「连都连不上」：本机很可能根本没有 IPv6 出口。
// 这是最常见的失败场景（宿舍关掉 v6、用手机热点），值得一句直白的结论，而不是
// 把五个地址的技术细节原样丢到用户日志里。
type probeError struct {
	host   string
	reason string
	noPath bool
}

func (e *probeError) Error() string { return e.host + " " + e.reason }

func fetchIPv6(ctx context.Context, client *http.Client, source string) (ipv6ProbeResult, *probeError) {
	host := source
	if parsed, err := url.Parse(source); err == nil {
		host = parsed.Host
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return ipv6ProbeResult{}, &probeError{host: host, reason: err.Error()}
	}
	response, err := client.Do(request)
	if err != nil {
		reason := "连接失败"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			reason = "超时"
		}
		return ipv6ProbeResult{}, &probeError{host: host, reason: reason, noPath: true}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ipv6ProbeResult{}, &probeError{host: host, reason: fmt.Sprintf("HTTP %d", response.StatusCode)}
	}
	// 少量读取即可：这些接口只回一个地址，限长防止被重定向到别的大页面拖住
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return ipv6ProbeResult{}, &probeError{host: host, reason: "读取失败"}
	}
	// 反爬校验页（Anubis 之类）会回一整页 HTML，里面可能带着本机地址或站点
	// 自己的地址；当成回显会得出错误结论，一律判为失败。
	if strings.HasPrefix(strings.TrimSpace(string(body)), "<") {
		return ipv6ProbeResult{}, &probeError{host: host, reason: "疑似反爬校验页"}
	}
	ip := extractIPv6(string(body))
	if ip == "" {
		return ipv6ProbeResult{}, &probeError{host: host, reason: "响应中没有 IPv6"}
	}
	return ipv6ProbeResult{IP: ip, Source: host}, nil
}

// 本机是否配了全球单播 IPv6（排除 ULA/链路本地）。用来区分两件事：「本来就
// 没有 v6 出口」要立刻给结论，而「刚停完免流、路由还在收敛」值得重试。
func hasLocalIPv6() bool {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, address := range addresses {
		ipnet, ok := address.(*net.IPNet)
		if !ok {
			continue
		}
		if ipnet.IP.To4() == nil && ipnet.IP.IsGlobalUnicast() && !ipnet.IP.IsPrivate() {
			return true
		}
	}
	return false
}

// probeFailure 汇总一轮竞速的失败原因。
type probeFailure struct {
	failures []*probeError
	total    int
}

// transient：整轮都停在「连不上/超时」这一层，没有拿到任何 HTTP 响应或正文，
// 说明是链路/路由层面的过渡态（TUN 正在拆除、默认路由还没收敛），而不是某个
// 站点挂了——这种失败会自愈，值得重试。HTTP 错误、正文里没有地址则不会。
func (f *probeFailure) transient() bool {
	for _, failure := range f.failures {
		if !failure.noPath {
			return false
		}
	}
	return true
}

func (f *probeFailure) error() error {
	if len(f.failures) == 0 {
		return fmt.Errorf("%d 个检测地址均无响应（超时）", f.total)
	}
	if f.transient() && len(f.failures) == f.total {
		return fmt.Errorf("%d 个检测地址均无法访问，本机可能没有 IPv6 出口（如 %s）", f.total, f.failures[0].Error())
	}
	shown := f.failures
	if len(shown) > 2 {
		shown = shown[:2]
	}
	reasons := make([]string, 0, len(shown))
	for _, failure := range shown {
		reasons = append(reasons, failure.Error())
	}
	message := fmt.Sprintf("全部 %d 个检测地址失败：%s", f.total, strings.Join(reasons, "、"))
	if len(f.failures) > len(shown) {
		message += " 等"
	}
	return errors.New(message)
}

// 一轮竞速：所有地址并行发出，第一个成功的结果胜出，其余立即取消。
func probeIPv6Once(ctx context.Context, client *http.Client, sources []string) (ipv6ProbeResult, *probeFailure) {
	ctx, cancel := context.WithTimeout(ctx, ipv6ProbeRoundTimeout)
	defer cancel()

	type outcome struct {
		result ipv6ProbeResult
		err    *probeError
	}
	results := make(chan outcome, len(sources))
	for _, source := range sources {
		go func(source string) {
			result, err := fetchIPv6(ctx, client, source)
			select {
			case results <- outcome{result, err}:
			case <-ctx.Done():
			}
		}(source)
	}

	failures := make([]*probeError, 0, len(sources))
	for range sources {
		select {
		case out := <-results:
			if out.err == nil {
				cancel() // 中止仍在途的请求
				return out.result, nil
			}
			failures = append(failures, out.err)
		case <-ctx.Done():
			return ipv6ProbeResult{}, &probeFailure{failures: failures, total: len(sources)}
		}
	}
	return ipv6ProbeResult{}, &probeFailure{failures: failures, total: len(sources)}
}

// 探到出结果为止：过渡态失败在预算内反复重试，其余立刻给结论。
// 返回的 attempts 让前端能把「等了 6 秒才成功」说清楚。
func probeIPv6Sources(ctx context.Context, client *http.Client, sources []string) (ipv6ProbeResult, int, error) {
	budget, cancel := context.WithTimeout(ctx, ipv6ProbeBudget)
	defer cancel()

	attempts := 0
	for {
		attempts++
		result, failure := probeIPv6Once(budget, client, sources)
		if failure == nil {
			return result, attempts, nil
		}
		// 真没有 v6 出口时重试只会让用户干等（校外、手机热点走这条快速失败）
		if !failure.transient() || !hasLocalIPv6() {
			return ipv6ProbeResult{}, attempts, failure.error()
		}
		select {
		case <-time.After(ipv6ProbeRetryGap):
		case <-budget.Done():
			// 本机有地址却始终打不出去：多半卡在停止免流的网络回收上
			if attempts > 1 {
				// 不嵌套括号：前端会把它包进「检测失败: …」
				return ipv6ProbeResult{}, attempts, errors.New(
					"本机有 IPv6 地址但出站不通，可能刚停完免流或校园网 v6 不通，请稍后重试")
			}
			return ipv6ProbeResult{}, attempts, failure.error()
		}
	}
}

// GET /api/v1/ipv6/probe 探测本机公网 IPv6，回答启动免流的网络前置条件。
// 探测失败仍是 200 + 空 ip（附 error）——这是「网络不具备条件」，不是接口错误。
func (h *helper) ipv6Probe(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	// 上一次停止如果被强杀在"关 TUN 的中途"，会留下一个没有会话的 FreeV6TUN：
	// 会话没了，auto-route 写进去的半段默认路由还在，出站全被它吸走。这种残留
	// 不会自愈，探测只会全部超时，然后把责任推给"校园网 v6 不通"。免流没在跑
	// 就不该留着它——先清掉残留路由再测，机器能立刻恢复。
	pidPath := filepath.Join(h.root, "state", "mihomo.pid")
	if _, running, statusErr := mihomo.Status(pidPath); statusErr == nil && !running &&
		network.AdapterPresent(network.TunDeviceName) {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		if removed := network.ClearTunRoutes(cleanupCtx, network.TunDeviceName); removed > 0 {
			fmt.Fprintf(os.Stderr, "helper: ipv6 probe: cleared %d leftover %s route(s)\n", removed, network.TunDeviceName)
		}
		cleanupCancel()
	}
	result, attempts, err := probeIPv6Sources(request.Context(), ipv6ProbeClient, ipv6EchoSources)
	if err != nil {
		// 失败原因也落一行 helper.log：前端只显示一句结论，排障要看每个源的
		// 具体表现（超时 / HTTP 错误 / 反爬页），否则只能猜。
		fmt.Fprintf(os.Stderr, "helper: ipv6 probe failed (%d attempt(s)): %v\n", attempts, err)
		writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "ip": "", "error": err.Error(), "attempts": attempts})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "ip": result.IP, "source": result.Source, "attempts": attempts})
}
