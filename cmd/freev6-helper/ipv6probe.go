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
// 两个实测过的坑决定了这里的形状：
//  1. 停止免流拆掉 TUN 会触发 Windows DNS 客户端熔断——`ipconfig /flushdns`
//     之后前两次查询各超时 3s 才恢复。用系统解析器时，四个地址会一起停在
//     "域名解析失败"，然后被报成"本机有 IPv6 地址但出站不通"。所以解析走
//     纯 Go 解析器（probeResolver），只查 AAAA（dialIPv6）。
//  2. 链路层面仍会有过渡态（路由收敛、上游抖动），一轮全败且本机确实配了
//     IPv6 时按预算重试；DNS 之外的失败立刻定论，不让用户干等。

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
	// 含重试的总预算。链路过渡态（路由收敛、上游抖动）在几秒内自愈，
	// 只问一次会把"刚停完免流"误判成"没有出口"。DNS 熔断已由 probeResolver
	// 绕开，这里覆盖的是剩下的链路层抖动。
	ipv6ProbeBudget   = 12 * time.Second
	ipv6ProbeRetryGap = 1200 * time.Millisecond
)

// 检测地址。四家分散在不同网络里，任一给出合法 IPv6 就够：
//   - speedtest6.bupt.edu.cn、speed6.ujs.edu.cn：校园 speedtest 后端，
//     CERNET2 内往返通常几十毫秒（实测 23ms / 75ms）。
//   - ipv6.testipv6.cn：校外兜底，JSONP，**必须带 callback** 才会回正文。
//   - api-ipv6.ip.sb：纯文本，走 Cloudflare，慢一些（0.2–4s）但目前可用。
//
// test6.ustc.edu.cn、fs.nju.edu.cn、test6.nju.edu.cn 已移出：前两个在用户
// 这条出口上稳定 3s 超时，后一个被反爬挡在校验页后面。
var ipv6EchoSources = []string{
	"https://speedtest6.bupt.edu.cn/getIP.php",
	"http://speed6.ujs.edu.cn/backend/getIP.php",
	"https://ipv6.testipv6.cn/ip/?callback=_freev6&testdomain=testipv6.cn",
	"https://api-ipv6.ip.sb/ip",
}

// 探测专用解析器：强制使用 Go 自带的纯 Go 解析器，直接向 DNS 服务器发查询。
//
// Windows 的 DNS 客户端在网卡增删之后（停止免流拆掉 TUN 正是这种事件）会进入
// 数秒的"服务器未响应"熔断：实测 `ipconfig /flushdns` 之后前两次查询各超时
// 3 秒，第三次才通（23ms）。这几秒正好盖住整个探测窗口——四个地址全部停在
// "域名解析失败"，最后被当成"出站不通"报给用户。同一个服务器、同一时刻，
// 纯 Go 解析器 25ms 就能拿到结果，因为它不经过那层熔断。
var probeResolver = &net.Resolver{PreferGo: true}

// dialIPv6 先只查 AAAA，再连那个地址。
//   - 只查 AAAA：探测的目的就是验证 IPv6 出口，混进 A 记录会连回 IPv4，
//     拿到一个 IPv4 地址，结论完全相反；
//   - 自己解析再拨号：绕开系统解析器（见 probeResolver）。
//
// 解析器读不到系统 DNS 配置时退回"直接拨域名"的旧行为，宁可慢也别直接失败。
func dialIPv6(ctx context.Context, dialer *net.Dialer, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		return dialer.DialContext(ctx, "tcp6", addr)
	}
	addresses, lookupErr := probeResolver.LookupIP(ctx, "ip6", host)
	if lookupErr != nil || len(addresses) == 0 {
		if conn, fallbackErr := dialer.DialContext(ctx, "tcp6", addr); fallbackErr == nil {
			return conn, nil
		}
		if lookupErr != nil {
			return nil, lookupErr
		}
		return nil, &net.DNSError{Err: "no AAAA record", Name: host, IsNotFound: true}
	}
	var lastErr error
	for _, address := range addresses {
		conn, dialErr := dialer.DialContext(ctx, "tcp6", net.JoinHostPort(address.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

// probeLogf 是探测的日志出口。用例里换成 no-op——这些行是给 helper.log 看的，
// 测试输出里只会变成噪音。
var probeLogf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format, args...) }

// localIPv6Present 报告本机是否配了全球单播 IPv6。做成变量是为了让"过渡态
// 重试"这条分支在**没有 IPv6 的机器**上也能被验证：CI 的 Windows runner 没有
// 全球 v6 地址，写死调用 hasLocalIPv6 会让重试用例直接走快速失败分支。
var localIPv6Present = hasLocalIPv6

// 探测专用客户端：只走 IPv6，且不读系统代理——免流运行时系统代理指向 mihomo，
// 走代理只会拿到代理出口的地址，测不出本机 IPv6。
var ipv6ProbeClient = func() *http.Client {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	return &http.Client{
		Timeout: ipv6ProbeRoundTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialIPv6(ctx, dialer, addr)
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
	// dns 标记"名字解析不了"这一类失败：它和出站不通是两种病，汇总文案要分开。
	dns bool
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
		var dnsErr *net.DNSError
		switch {
		case errors.As(err, &dnsErr):
			// 解析失败和"连不上"是两件事：前者说明 DNS 坏了（停止免流后确实
			// 出现过这种窗口），后者才是出站不通。混成一句会让排障走弯路。
			return ipv6ProbeResult{}, &probeError{host: host, reason: "域名解析失败", noPath: true, dns: true}
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
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

// dnsOnly：这一轮所有源都栽在"名字解析不了"上，一个都没能发起连接。
// 这种失败的方向完全不同——网络可能是通的，坏的是 DNS，修路由没用。
func (f *probeFailure) dnsOnly() bool {
	if len(f.failures) == 0 {
		return false
	}
	for _, failure := range f.failures {
		if !failure.dns {
			return false
		}
	}
	return true
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
		// 每轮全败都落一行：前端只显示一句汇总，"到底每个源是超时、解析失败
		// 还是没地址"只有这里看得到。没有这一行，排障就只剩猜。
		probeLogf("helper: ipv6 probe round %d: %s\n", attempts, failure.describe())
		// 真没有 v6 出口时重试只会让用户干等（校外、手机热点走这条快速失败）
		if !failure.transient() || !localIPv6Present() {
			return ipv6ProbeResult{}, attempts, failure.error()
		}
		select {
		case <-time.After(ipv6ProbeRetryGap):
		case <-budget.Done():
			// 本机有地址却始终打不出去：多半卡在停止免流的网络回收上
			if attempts > 1 {
				logProbeNetworkState()
				if failure.dnsOnly() {
					// 名字根本解析不出来：问题在 DNS 这一层，别说成"出站不通"
					return ipv6ProbeResult{}, attempts, errors.New(
						"DNS 暂时解析不了检测地址（刚停完免流时常见），请稍后重试")
				}
				// 不嵌套括号：前端会把它包进「检测失败: …」
				return ipv6ProbeResult{}, attempts, errors.New(
					"本机有 IPv6 地址但出站不通，可能刚停完免流或校园网 v6 不通，请稍后重试")
			}
			return ipv6ProbeResult{}, attempts, failure.error()
		}
	}
}

// describe 把一轮里每个源的失败原因拼成一行，供 helper.log 用。
func (f *probeFailure) describe() string {
	if len(f.failures) == 0 {
		return fmt.Sprintf("%d 个地址全部超时", f.total)
	}
	parts := make([]string, 0, len(f.failures))
	for _, failure := range f.failures {
		parts = append(parts, failure.Error())
	}
	if len(parts) < f.total {
		parts = append(parts, fmt.Sprintf("另有 %d 个未响应", f.total-len(parts)))
	}
	return strings.Join(parts, "；")
}

// logProbeNetworkState 记录"此刻各接口还有没有默认网关"。
//
// 这是判断"有 IPv6 地址但出站不通"的最后一道分界线：网卡上没有 v6 默认网关，
// 说明是路由被拆掉（停止免流残留 / 路由器通告还没回来）；网关都在，问题就在
// DNS 或上游，别再往路由上找。
func logProbeNetworkState() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snapshot, err := network.CaptureSnapshot(ctx, false)
	if err != nil {
		probeLogf("helper: ipv6 probe net: snapshot failed: %v\n", err)
		return
	}
	parts := make([]string, 0, len(snapshot.Interfaces))
	for _, iface := range snapshot.Interfaces {
		if len(iface.IPv6DefaultGateways) == 0 && len(iface.IPv4DefaultGateways) == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s v6gw=%s v4gw=%s", iface.Alias,
			strings.Join(iface.IPv6DefaultGateways, "/"),
			strings.Join(iface.IPv4DefaultGateways, "/")))
	}
	if len(parts) == 0 {
		parts = append(parts, "没有任何接口报告默认网关")
	}
	probeLogf("helper: ipv6 probe net: %s；%s=%v\n", strings.Join(parts, "；"),
		network.TunDeviceName, network.AdapterPresent(network.TunDeviceName))
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
			probeLogf("helper: ipv6 probe: cleared %d leftover %s route(s)\n", removed, network.TunDeviceName)
		}
		cleanupCancel()
	}
	result, attempts, err := probeIPv6Sources(request.Context(), ipv6ProbeClient, ipv6EchoSources)
	if err != nil {
		// 失败原因也落一行 helper.log：前端只显示一句结论，排障要看每个源的
		// 具体表现（超时 / HTTP 错误 / 反爬页），否则只能猜。
		probeLogf("helper: ipv6 probe failed (%d attempt(s)): %v\n", attempts, err)
		writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "ip": "", "error": err.Error(), "attempts": attempts})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "ip": result.IP, "source": result.Source, "attempts": attempts})
}
