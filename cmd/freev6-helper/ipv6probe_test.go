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

// 只有"能出网"的全球单播地址才该被当成 IPv6 出口；链路本地和 ULA 留着只会把
// 「没有出口」掩盖成「有地址」。
func TestGlobalIPv6FiltersNonRoutable(t *testing.T) {
	cases := map[string]struct {
		addr net.IP
		want string
	}{
		"校园地址":     {net.ParseIP("2001:da8:208:38::1"), "2001:da8:208:38::1"},
		"压缩写法统一":   {net.ParseIP("2001:0DB8:0000:0000:0000:0000:0000:0001"), "2001:db8::1"},
		"链路本地排除":   {net.ParseIP("fe80::1"), ""},
		"ULA 排除":   {net.ParseIP("fd00::1"), ""},
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

// 竞速：任一目标握手成功就算出站可用，并带上是哪个目标、用了多久。
func TestCheckIPv6EgressFirstSuccessWins(t *testing.T) {
	restore := stubDial(func(ctx context.Context, target string, timeout time.Duration) error {
		if target == ipv6EgressTargets[1] {
			return nil
		}
		return errors.New("unreachable")
	})
	defer restore()

	result := checkIPv6Egress(context.Background())
	if !result.OK {
		t.Fatalf("应判定可用: %+v", result)
	}
	if result.Target != ipv6EgressTargets[1] {
		t.Errorf("Target = %q", result.Target)
	}
	if result.Attempts != 1 {
		t.Errorf("Attempts = %d，一次就该成功", result.Attempts)
	}
}

// 全败一次后重试：停止免流拆 TUN 的那几秒正是这个样子，不能一次失败就定论。
func TestCheckIPv6EgressRetriesTransientFailure(t *testing.T) {
	var calls int32
	restore := stubDial(func(ctx context.Context, target string, timeout time.Duration) error {
		// 第一轮（3 个目标）全失败，之后成功
		if atomic.AddInt32(&calls, 1) <= int32(len(ipv6EgressTargets)) {
			return errors.New("network is unreachable")
		}
		return nil
	})
	defer restore()

	started := time.Now()
	result := checkIPv6Egress(context.Background())
	if !result.OK {
		t.Fatalf("重试后应判定可用: %+v", result)
	}
	if result.Attempts < 2 {
		t.Errorf("Attempts = %d，应该重试过", result.Attempts)
	}
	if elapsed := time.Since(started); elapsed < ipv6EgressGap {
		t.Errorf("耗时 %v，没有等到重试间隔", elapsed)
	}
}

// 全部不可达：给出每个目标的名字，别只丢一句"失败"。
func TestCheckIPv6EgressReportsTargets(t *testing.T) {
	restore := stubDial(func(ctx context.Context, target string, timeout time.Duration) error {
		return errors.New("network is unreachable")
	})
	defer restore()

	result := checkIPv6Egress(context.Background())
	if result.OK {
		t.Fatal("全部不可达却判定可用")
	}
	if result.Attempts != ipv6EgressAttempts {
		t.Errorf("Attempts = %d, want %d", result.Attempts, ipv6EgressAttempts)
	}
	for _, target := range ipv6EgressTargets {
		if !strings.Contains(result.Detail, targetHost(target)) {
			t.Errorf("Detail 缺少目标 %s: %s", targetHost(target), result.Detail)
		}
	}
}

// 没有全局 IPv6：直接给结论，不发任何连接（不能靠"连不上"去推断）。
func TestIPv6ProbeWithoutAddressSkipsEgress(t *testing.T) {
	restoreList, restoreDial := stubProbe(nil, nil)
	defer restoreList()
	defer restoreDial()

	var dialed int32
	probeDialIPv6 = func(context.Context, string, time.Duration) error {
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

// 正常路径：返回地址列表、校园地址、出站耗时。
func TestIPv6ProbeReportsAddressesAndEgress(t *testing.T) {
	restoreList, restoreDial := stubProbe([]ipv6Address{
		{IP: "2001:da8:208:38::1", Alias: "WiFi"},
		{IP: "2408:8207:1234::2", Alias: "Ethernet"},
	}, func(context.Context, string, time.Duration) error { return nil })
	defer restoreList()
	defer restoreDial()

	body := runProbe(t)
	if len(body.Addresses) != 2 || body.IP != "2001:da8:208:38::1" {
		t.Errorf("addresses = %v, ip = %q", body.Addresses, body.IP)
	}
	if !body.Egress {
		t.Fatalf("出站应可用: %+v", body)
	}
	if body.Source != "WiFi" {
		t.Errorf("source = %q", body.Source)
	}
	if body.Error != "" {
		t.Errorf("成功路径不该带 error: %q", body.Error)
	}
}

// 有地址但出不去：文案要说清是出站问题，不能再让人以为是"没有 IPv6"。
func TestIPv6ProbeReportsEgressFailure(t *testing.T) {
	restoreList, restoreDial := stubProbe([]ipv6Address{
		{IP: "2001:da8:208:38::1", Alias: "WiFi"},
	}, func(context.Context, string, time.Duration) error { return errors.New("network is unreachable") })
	defer restoreList()
	defer restoreDial()

	body := runProbe(t)
	if body.Egress {
		t.Fatal("全部连不上却报出站可用")
	}
	if !strings.Contains(body.Error, "出站不通") {
		t.Errorf("error = %q", body.Error)
	}
}

type probeBody struct {
	OK        bool     `json:"ok"`
	IP        string   `json:"ip"`
	Addresses []string `json:"addresses"`
	Source    string   `json:"source"`
	Egress    bool     `json:"egress"`
	Attempts  int      `json:"attempts"`
	Error     string   `json:"error"`
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

func stubProbe(addresses []ipv6Address, dial func(context.Context, string, time.Duration) error) (func(), func()) {
	originalList, originalDial := listLocalIPv6, probeDialIPv6
	listLocalIPv6 = func() []ipv6Address { return addresses }
	if dial != nil {
		probeDialIPv6 = dial
	}
	return func() { listLocalIPv6 = originalList },
		func() { probeDialIPv6 = originalDial }
}

func stubDial(dial func(context.Context, string, time.Duration) error) func() {
	original := probeDialIPv6
	probeDialIPv6 = dial
	return func() { probeDialIPv6 = original }
}
