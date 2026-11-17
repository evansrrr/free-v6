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

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const ipv6ProbeTimeout = 5 * time.Second

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
		Timeout: ipv6ProbeTimeout,
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

// 并行竞速所有地址，第一个成功的结果胜出。全失败才返回错误，并把各地址的
// 失败原因带上——前端把它原样写进日志，用户能看出是哪个地址出了问题。
func probeIPv6Sources(ctx context.Context, client *http.Client, sources []string) (ipv6ProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, ipv6ProbeTimeout)
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
			return ipv6ProbeResult{}, probeFailedError(failures, len(sources))
		}
	}
	return ipv6ProbeResult{}, probeFailedError(failures, len(sources))
}

func probeFailedError(failures []*probeError, total int) error {
	noPath := len(failures) == total
	for _, failure := range failures {
		if !failure.noPath {
			noPath = false
			break
		}
	}
	if noPath {
		return fmt.Errorf("%d 个检测地址均无法访问，本机可能没有 IPv6 出口（如 %s）", total, failures[0].Error())
	}
	if len(failures) == 0 {
		return fmt.Errorf("%d 个检测地址均无响应（超时）", total)
	}
	shown := failures
	if len(shown) > 2 {
		shown = shown[:2]
	}
	reasons := make([]string, 0, len(shown))
	for _, failure := range shown {
		reasons = append(reasons, failure.Error())
	}
	message := fmt.Sprintf("全部 %d 个检测地址失败：%s", total, strings.Join(reasons, "、"))
	if len(failures) > len(shown) {
		message += " 等"
	}
	return errors.New(message)
}

// GET /api/v1/ipv6/probe 探测本机公网 IPv6，回答启动免流的网络前置条件。
// 探测失败仍是 200 + 空 ip（附 error）——这是「网络不具备条件」，不是接口错误。
func (h *helper) ipv6Probe(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	result, err := probeIPv6Sources(request.Context(), ipv6ProbeClient, ipv6EchoSources)
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "ip": "", "error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "ip": result.IP, "source": result.Source})
}
