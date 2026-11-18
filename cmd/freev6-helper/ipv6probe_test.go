package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExtractIPv6(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"纯文本 ip.sb":    {"2408:8207:1234::1\n", "2408:8207:1234::1"},
		"getIP.php 新版": {`{"processedString":"2408:8207:abcd::5 - China Mobile","rawIspInfo":""}`, "2408:8207:abcd::5"},
		"getIP.php 旧版": {`{"ip":"2001:da8:1::2","country":"CN"}`, "2001:da8:1::2"},
		"JSONP 包装":     {`freev6probe({"ip":"2408:8000:1::9","type":"ipv6"});`, "2408:8000:1::9"},
		"只回 IPv4":      {`{"processedString":"1.2.3.4 - China"}`, ""},
		"没有地址的 HTML":   {`<html><body>14:30:05</body></html>`, ""},
		"链路本地/ULA 忽略":  {`fe80::1 fd00:1234::5`, ""},
		"句末尾冒号":        {`addr: 2001:250:1006::1: ok`, "2001:250:1006::1"},
		"压缩形式统一为规范写法":  {`2001:0DB8:0000:0000:0000:0000:0000:0001`, "2001:db8::1"},
		"私网前缀不误判为端口":   {`connect 127.0.0.1:8080 via ::1`, ""},
	}
	for name, testCase := range cases {
		if got := extractIPv6(testCase.body); got != testCase.want {
			t.Errorf("%s: extractIPv6(%q) = %q, want %q", name, testCase.body, got, testCase.want)
		}
	}
}

// 反爬校验页里也带着本机地址（Anubis 会把 X-Real-IP 塞进调试 JSON），
// 绝不能当成回显得出结论。
func TestFetchIPv6RejectsBotWallHTML(t *testing.T) {
	page := `<!doctype html><html><head><title>Making sure you&#39;re not a bot!</title></head>
<body><script type="application/json">{"headers":{"X-Real-IP":"2001:da8:208:38::1"}}</script></body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte(page))
	}))
	defer server.Close()

	_, err := fetchIPv6(context.Background(), server.Client(), server.URL)
	if err == nil {
		t.Fatal("校验页被当成了回显")
	}
	if !strings.Contains(err.Error(), "反爬校验页") {
		t.Errorf("错误信息应指出是校验页: %v", err)
	}
}

// 竞速：慢的那个即使先返回也不能挡住快的；胜出结果带上来源主机。
func TestProbeIPv6SourcesPicksFastest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/slow":
			time.Sleep(300 * time.Millisecond)
			_, _ = writer.Write([]byte("2408:8207:aaaa::1"))
		case "/broken":
			writer.WriteHeader(http.StatusInternalServerError)
		case "/empty":
			_, _ = writer.Write([]byte("no address here"))
		case "/fast":
			_, _ = writer.Write([]byte(`{"processedString":"2001:250:1006::2 - CERNET"}`))
		}
	}))
	defer server.Close()

	started := time.Now()
	result, _, err := probeIPv6Sources(context.Background(), server.Client(), []string{
		server.URL + "/slow",
		server.URL + "/broken",
		server.URL + "/empty",
		server.URL + "/fast",
	})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("probeIPv6Sources 出错: %v", err)
	}
	if result.IP != "2001:250:1006::2" {
		t.Errorf("IP = %q, want 2001:250:1006::2", result.IP)
	}
	if !strings.HasPrefix(result.Source, "127.0.0.1:") {
		t.Errorf("Source = %q, 应为 httptest 主机", result.Source)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("耗时 %v：没有在最快的结果上返回，慢点挡住了竞速", elapsed)
	}
}

// 全部失败要返回带原因的错误，而不是一句裸的超时。
func TestProbeIPv6SourcesReportsFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	_, _, err := probeIPv6Sources(context.Background(), server.Client(), []string{
		server.URL + "/a",
		server.URL + "/b",
	})
	if err == nil {
		t.Fatal("全部失败却返回成功")
	}
	if !strings.Contains(err.Error(), "全部 2 个检测地址失败") || !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("错误信息不够具体: %v", err)
	}
}

// 全都连不上 = 本机没有 IPv6 出口：给一句直白结论，不要空转。
// 上下文短于预算时按上下文收尾，避免本机恰好有 v6 时重试到预算用完。
func TestProbeIPv6SourcesReportsMissingPath(t *testing.T) {
	closed := "http://127.0.0.1:1/probe"
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err := probeIPv6Sources(ctx, http.DefaultClient, []string{closed, closed + "2"})
	if err == nil {
		t.Fatal("全部连不上却返回成功")
	}
	if !strings.Contains(err.Error(), "IPv6") {
		t.Errorf("错误信息应提到 IPv6: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("耗时 %v，超出短预算", elapsed)
	}
}

// 过渡态失败（拨号层报错）要重试：停止免流后 TUN 拆除的那几秒正是这个样子。
type flakyTransport struct {
	calls     int32
	failTimes int32
	body      string
}

func (f *flakyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	if atomic.AddInt32(&f.calls, 1) <= f.failTimes {
		return nil, &net.OpError{Op: "dial", Err: errors.New("network is unreachable")}
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(f.body)),
	}, nil
}

func TestProbeIPv6SourcesRetriesTransientFailure(t *testing.T) {
	// CI 的 Windows runner 没有全球 IPv6 地址：真去问本机会走进"没有出口"的
	// 快速失败分支，重试逻辑根本跑不到。这里把判定固定成 true。
	localIPv6Present = func() bool { return true }
	t.Cleanup(func() { localIPv6Present = hasLocalIPv6 })

	transport := &flakyTransport{failTimes: 1, body: "2408:8207:1234::1"}
	started := time.Now()
	result, _, err := probeIPv6Sources(context.Background(), &http.Client{Transport: transport}, []string{"http://probe.invalid/ip"})
	if err != nil {
		t.Fatalf("重试后应拿到结果: %v", err)
	}
	if result.IP != "2408:8207:1234::1" {
		t.Errorf("IP = %q", result.IP)
	}
	if calls := atomic.LoadInt32(&transport.calls); calls < 2 {
		t.Errorf("只请求了 %d 次，没有重试", calls)
	}
	if elapsed := time.Since(started); elapsed < ipv6ProbeRetryGap {
		t.Errorf("耗时 %v，没有等到重试间隔", elapsed)
	}
}

// 全部挂住则按整体超时收尾，错误里明确是超时。
func TestProbeIPv6SourcesTimesOut(t *testing.T) {
	blocked := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case <-blocked:
		case <-request.Context().Done():
		}
	}))
	defer server.Close()
	defer close(blocked)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err := probeIPv6Sources(ctx, server.Client(), []string{server.URL + "/hang"})
	if err == nil {
		t.Fatal("全部挂住却返回成功")
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Errorf("错误信息应说明超时: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("耗时 %v，未按请求上下文及时收尾", elapsed)
	}
}

// 处理函数：探测失败是「网络条件不具备」，返回 200 + 空 ip + error。
func TestIPv6ProbeHandler(t *testing.T) {
	original := ipv6EchoSources
	originalClient := ipv6ProbeClient
	defer func() { ipv6EchoSources = original; ipv6ProbeClient = originalClient }()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("2408:8207:1234::1"))
	}))
	defer server.Close()
	ipv6EchoSources = []string{server.URL}
	ipv6ProbeClient = server.Client()

	handler := &helper{}
	recorder := httptest.NewRecorder()
	handler.ipv6Probe(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/ipv6/probe", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"ip":"2408:8207:1234::1"`) || !strings.Contains(body, `"source"`) {
		t.Errorf("响应缺少 ip/source: %s", body)
	}

	// 非 GET 一律 405
	recorder = httptest.NewRecorder()
	handler.ipv6Probe(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/ipv6/probe", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST 状态码 = %d, want 405", recorder.Code)
	}

	// 探测失败：200 + 空 ip + error
	failing := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer failing.Close()
	ipv6EchoSources = []string{failing.URL}
	ipv6ProbeClient = failing.Client()
	recorder = httptest.NewRecorder()
	handler.ipv6Probe(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/ipv6/probe", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("失败路径状态码 = %d, want 200", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"ip":""`) || !strings.Contains(body, `"error"`) {
		t.Errorf("失败响应应带空 ip 与 error: %s", body)
	}
}

// 生产客户端必须只走 IPv6：拿到的是 IPv4 字面量就说明约束失效了。
func TestProductionProbeClientIsIPv6Only(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {}))
	defer server.Close()
	_, err := fetchIPv6(context.Background(), ipv6ProbeClient, server.URL)
	if err == nil {
		t.Fatal("IPv4 地址被 tcp6 客户端连上了，约束没有生效")
	}
	if !strings.Contains(err.Error(), "连接失败") && !strings.Contains(err.Error(), "超时") {
		t.Logf("失败原因（可接受）: %v", fmt.Sprint(err))
	}
}

// 字面量地址不该经过解析器：否则每次探测都白搭一次 DNS 查询。
func TestDialIPv6SkipsResolverForLiteral(t *testing.T) {
	started := time.Now()
	_, err := dialIPv6(context.Background(), &net.Dialer{Timeout: 300 * time.Millisecond}, "[::1]:9")
	if err == nil {
		t.Fatal("::1:9 竟然连上了")
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		t.Errorf("字面量地址被当成了域名解析失败: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("耗时 %v，字面量不该有解析开销", elapsed)
	}
}

// Windows DNS 客户端在网络变更后会熔断几秒，这时所有查询都超时。必须能认出
// 「是解析挂了」而不是「出站不通」，两者给用户的建议完全不同。
type dnsFailTransport struct{}

func (dnsFailTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, &net.DNSError{Err: "server misbehaving", Name: "probe.invalid", IsTemporary: true}
}

func TestFetchIPv6MarksDNSFailure(t *testing.T) {
	_, err := fetchIPv6(context.Background(),
		&http.Client{Transport: dnsFailTransport{}}, "http://probe.invalid/ip")
	if err == nil {
		t.Fatal("解析失败被当成成功")
	}
	if !err.dns {
		t.Errorf("应标记为 DNS 失败: %v", err)
	}
	if err.reason != "域名解析失败" {
		t.Errorf("reason = %q", err.reason)
	}
}

// 探测日志是给 helper.log 的，用例里只会把 CI 输出刷满。
func TestMain(m *testing.M) {
	probeLogf = func(string, ...any) {}
	os.Exit(m.Run())
}
