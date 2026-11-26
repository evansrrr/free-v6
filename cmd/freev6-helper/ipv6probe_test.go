package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 探测日志是给 helper.log 的，用例里只会把 CI 输出刷满。
func TestMain(m *testing.M) {
	probeLogf = func(string, ...any) {}
	os.Exit(m.Run())
}

// 只有"能出网"的全球单播地址才该被当成 IPv6 出口；链路本地、ULA 与废弃的
// site-local 留着只会把「没有出口」掩盖成「有地址」。
func TestGlobalIPv6FiltersNonRoutable(t *testing.T) {
	cases := map[string]struct {
		addr net.IP
		want string
	}{
		"校园地址":     {net.ParseIP("2001:da8:208:38::1"), "2001:da8:208:38::1"},
		"压缩写法统一":   {net.ParseIP("2001:0DB8:0000:0000:0000:0000:0000:0001"), "2001:db8::1"},
		"链路本地排除":   {net.ParseIP("fe80::1"), ""},
		"ULA 排除":   {net.ParseIP("fd00::1"), ""},
		"站点本地排除":   {net.ParseIP("fec0::1"), ""},
		"回环排除":     {net.ParseIP("::1"), ""},
		"IPv4 排除":  {net.ParseIP("10.0.0.1"), ""},
		"公网 v4 排除": {net.ParseIP("223.5.5.5"), ""},
	}
	for name, testCase := range cases {
		addr := &net.IPNet{IP: testCase.addr, Mask: net.CIDRMask(64, 128)}
		if got := globalIPv6(addr); got != testCase.want {
			t.Errorf("%s: globalIPv6(%v) = %q, want %q", name, testCase.addr, got, testCase.want)
		}
	}
}

// 非 IPNet 类型的 Addr（例如测试替身、伪接口）不该 panic，也不该被当成地址。
func TestGlobalIPv6IgnoresForeignAddr(t *testing.T) {
	if got := globalIPv6(&net.IPAddr{IP: net.ParseIP("2001:da8::1")}); got != "" {
		t.Errorf("IPAddr 不该被当成接口地址: %q", got)
	}
}

// 绑源地址必须真的生效：多网卡下不绑，测的就不是校园那个地址。用 Dialer 的
// LocalAddr 直接断言，不真的连一次——CI 的 runner 没有 IPv6，连不出结果。
func TestIPv6DialerBindsSourceAddress(t *testing.T) {
	local := net.ParseIP("2001:da8:208:38::1")
	dialer := ipv6Dialer(local, 900*time.Millisecond)
	tcpAddr, ok := dialer.LocalAddr.(*net.TCPAddr)
	if !ok {
		t.Fatalf("LocalAddr = %#v，没有绑定源地址", dialer.LocalAddr)
	}
	if !tcpAddr.IP.Equal(local) {
		t.Errorf("LocalAddr.IP = %v, want %v", tcpAddr.IP, local)
	}
	if dialer.Timeout != 900*time.Millisecond {
		t.Errorf("Timeout = %v", dialer.Timeout)
	}
}

// 不绑源地址时交给系统选：LocalAddr 必须留空，否则等于替系统做了决定。
func TestIPv6DialerLeavesLocalAddrEmpty(t *testing.T) {
	if got := ipv6Dialer(nil, time.Second).LocalAddr; got != nil {
		t.Errorf("LocalAddr = %v，应该为空", got)
	}
}

// bind 失败和"连不通"要分开：前者说明这台机器不支持绑源地址（应退回系统选源），
// 后者才是出站有问题。传一个伪造的错误即可，不依赖网络。
func TestIsBindFailure(t *testing.T) {
	bindErr := &net.OpError{Op: "dial", Net: "tcp6", Err: &os.SyscallError{Syscall: "bind", Err: errors.New("invalid argument")}}
	if !isBindFailure(bindErr) {
		t.Error("bind 阶段失败没被识别出来")
	}
	connectErr := &net.OpError{Op: "dial", Net: "tcp6", Err: &os.SyscallError{Syscall: "connect", Err: errors.New("network is unreachable")}}
	if isBindFailure(connectErr) {
		t.Error("connect 失败被误判成 bind 失败")
	}
	if isBindFailure(errors.New("plain error")) {
		t.Error("普通错误被误判成 bind 失败")
	}
}

func probeOne(addr ipv6Address, dial dialFunc) addressProbe {
	return probeAddressEgress(context.Background(), addr, dial)
}

// 竞速：任一目标握手成功就算这个地址可用，并带回目标类别（校园/校外）。
func TestCheckAddressEgressFirstSuccessWins(t *testing.T) {
	probe := probeOne(ipv6Address{IP: "2001:da8:208:38::1", Alias: "WiFi"},
		func(_ context.Context, local net.IP, target string, _ time.Duration) error {
			if local == nil || local.String() != "2001:da8:208:38::1" {
				return errors.New("unexpected source")
			}
			if target == ipv6EgressTargets[1].Addr {
				return nil
			}
			return errors.New("unreachable")
		})
	if !probe.Egress {
		t.Fatalf("应判定可用: %+v", probe)
	}
	if probe.Target != targetHost(ipv6EgressTargets[1].Addr) {
		t.Errorf("Target = %q", probe.Target)
	}
	if probe.Scope != scopePublic {
		t.Errorf("Scope = %q, want %q", probe.Scope, scopePublic)
	}
	if probe.Attempts != 1 {
		t.Errorf("Attempts = %d，一次就该成功", probe.Attempts)
	}
}

// 只连得上校园内目标：判定可用，但 scope 要标成 campus，前端才能提示
// "校外路径未验证"。
func TestCheckAddressEgressReportsCampusOnlyScope(t *testing.T) {
	probe := probeOne(ipv6Address{IP: "2001:da8:208:38::1"},
		func(_ context.Context, _ net.IP, target string, _ time.Duration) error {
			if target == ipv6EgressTargets[0].Addr {
				return nil
			}
			return errors.New("unreachable")
		})
	if !probe.Egress || probe.Scope != scopeCampus {
		t.Fatalf("probe = %+v", probe)
	}
}

// 竞速里校内目标先回、校外也能通：scope 必须是 public，不能因为"赢家在校内"
// 就说"只到得了校内"——那会让每次启动都误报一句提示。
func TestResolveScopeChecksPublicTarget(t *testing.T) {
	probe := probeOne(ipv6Address{IP: "2001:da8:208:38::1"},
		func(_ context.Context, _ net.IP, target string, _ time.Duration) error {
			if target == ipv6EgressTargets[0].Addr {
				return nil // 校内 2ms 先赢
			}
			return nil
		})
	if probe.Scope != scopePublic {
		t.Errorf("Scope = %q，校外可达时应标 public", probe.Scope)
	}
}

// 全败一次后重试：停止免流拆 TUN 的那几秒正是这个样子，不能一次失败就定论。
func TestCheckAddressEgressRetriesTransientFailure(t *testing.T) {
	var calls int32
	dial := func(context.Context, net.IP, string, time.Duration) error {
		if atomic.AddInt32(&calls, 1) <= int32(len(ipv6EgressTargets)) {
			return &net.OpError{Op: "dial", Err: errors.New("network is unreachable")}
		}
		return nil
	}

	started := time.Now()
	probe := probeOne(ipv6Address{IP: "2001:da8:208:38::1"}, dial)
	if !probe.Egress {
		t.Fatalf("重试后应判定可用: %+v", probe)
	}
	if probe.Attempts < 2 {
		t.Errorf("Attempts = %d，应该重试过", probe.Attempts)
	}
	if elapsed := time.Since(started); elapsed < ipv6EgressGap {
		t.Errorf("耗时 %v，没有等到重试间隔", elapsed)
	}
}

// 全部不可达：结果里要带上目标名，别只丢一句"失败"。
func TestCheckAddressEgressReportsTargets(t *testing.T) {
	probe := probeOne(ipv6Address{IP: "2001:da8:208:38::1"},
		func(context.Context, net.IP, string, time.Duration) error {
			return &net.OpError{Op: "dial", Err: errors.New("network is unreachable")}
		})
	if probe.Egress {
		t.Fatal("全部不可达却判定可用")
	}
	if probe.Attempts != ipv6EgressAttempts {
		t.Errorf("Attempts = %d, want %d", probe.Attempts, ipv6EgressAttempts)
	}
	for _, target := range ipv6EgressTargets {
		if !strings.Contains(probe.Detail, targetHost(target.Addr)) {
			t.Errorf("Detail 缺少目标 %s: %s", targetHost(target.Addr), probe.Detail)
		}
	}
}

// 这台机器不支持绑源地址（bind 阶段就失败）：不能因此判"出站不通"，应退回系统
// 选源验证，并把结论标成未验证。
func TestCheckAddressEgressFallsBackWhenBindUnsupported(t *testing.T) {
	probe := probeOne(ipv6Address{IP: "2001:da8:208:38::1", Alias: "WiFi"},
		func(_ context.Context, local net.IP, _ string, _ time.Duration) error {
			if local != nil {
				return &net.OpError{Op: "dial", Err: &os.SyscallError{Syscall: "bind", Err: errors.New("invalid argument")}}
			}
			return nil
		})
	if !probe.Egress || !probe.Unverified {
		t.Fatalf("应退回系统选源并标记未验证: %+v", probe)
	}
}

// 文档里那条 P1 场景：校园地址出不去，另一条链路能出去。逐地址结论必须区分
// 开——总体 egress=true 不等于校园地址可用。
func TestIPv6ProbeSeparatesPerAddressEgress(t *testing.T) {
	restoreList, restoreDial := stubProbe([]ipv6Address{
		{IP: "2001:da8:208:38::1", Alias: "WiFi"},
		{IP: "2408:8207:1234::2", Alias: "Ethernet"},
	}, func(_ context.Context, local net.IP, _ string, _ time.Duration) error {
		if local != nil && local.String() == "2001:da8:208:38::1" {
			return &net.OpError{Op: "dial", Err: errors.New("network is unreachable")}
		}
		return nil
	})
	defer restoreList()
	defer restoreDial()

	body := runProbe(t)
	if len(body.Addresses) != 2 {
		t.Fatalf("addresses = %+v", body.Addresses)
	}
	if body.Addresses[0].Egress {
		t.Errorf("校园地址出不去却报成可用: %+v", body.Addresses[0])
	}
	if !body.Addresses[1].Egress {
		t.Errorf("另一条链路可用却没报出来: %+v", body.Addresses[1])
	}
	if !body.Egress {
		t.Error("总览应表示「有地址能出站」")
	}
	// ip 指向可用地址，别让旧消费者拿到一条废链路
	if body.IP != "2408:8207:1234::2" {
		t.Errorf("ip = %q，应指向能出站的地址", body.IP)
	}
}

// 没有全局 IPv6：直接给结论，不发任何连接（不能靠"连不上"去推断）。
func TestIPv6ProbeWithoutAddressSkipsEgress(t *testing.T) {
	restoreList, restoreDial := stubProbe(nil, nil)
	defer restoreList()
	defer restoreDial()

	var dialed int32
	probeDialIPv6 = func(context.Context, net.IP, string, time.Duration) error {
		atomic.AddInt32(&dialed, 1)
		return nil
	}

	body := runProbe(t)
	if dialed != 0 {
		t.Errorf("没有地址却发起了 %d 次连接", dialed)
	}
	if len(body.Addresses) != 0 {
		t.Errorf("addresses = %v", body.Addresses)
	}
	if body.Egress {
		t.Error("没有地址却报出站可用")
	}
	if !strings.Contains(body.Error, "IPv6") {
		t.Errorf("error 应说明没有 IPv6: %q", body.Error)
	}
}

// 有地址但出不去：文案要说清是出站问题，不能再让人以为是"没有 IPv6"。
func TestIPv6ProbeReportsEgressFailure(t *testing.T) {
	restoreList, restoreDial := stubProbe([]ipv6Address{
		{IP: "2001:da8:208:38::1", Alias: "WiFi"},
	}, func(context.Context, net.IP, string, time.Duration) error {
		return &net.OpError{Op: "dial", Err: errors.New("network is unreachable")}
	})
	defer restoreList()
	defer restoreDial()

	body := runProbe(t)
	if body.Egress {
		t.Fatal("全部连不上却报出站可用")
	}
	if !strings.Contains(body.Error, "出站不通") {
		t.Errorf("error = %q", body.Error)
	}
	if body.Attempts != ipv6EgressAttempts {
		t.Errorf("attempts = %d", body.Attempts)
	}
}

type probeBody struct {
	OK        bool           `json:"ok"`
	IP        string         `json:"ip"`
	Addresses []addressProbe `json:"addresses"`
	Source    string         `json:"source"`
	Egress    bool           `json:"egress"`
	Attempts  int            `json:"attempts"`
	Error     string         `json:"error"`
}

func runProbe(t *testing.T) probeBody {
	t.Helper()
	instance := &helper{root: t.TempDir()}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/ipv6/probe", nil)
	instance.ipv6Probe(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", recorder.Code)
	}
	var body probeBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON: %v (%s)", err, recorder.Body.String())
	}
	return body
}

func stubProbe(addresses []ipv6Address, dial func(context.Context, net.IP, string, time.Duration) error) (func(), func()) {
	originalList, originalDial := listLocalIPv6, probeDialIPv6
	listLocalIPv6 = func() []ipv6Address { return addresses }
	if dial != nil {
		probeDialIPv6 = dial
	}
	return func() { listLocalIPv6 = originalList },
		func() { probeDialIPv6 = originalDial }
}
