//go:build windows

package network

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// tunRoutePrefixes 是 TUN 抢默认路由用的"半段前缀"：Windows 没有"替换默认
// 路由"的接口，实现上把 0.0.0.0/0 拆成两半盖在物理网卡前面。清残留也按这
// 几个前缀删，并且只删 TUN 那张网卡 ifIndex 上的条目，不碰别的接口。
var tunRoutePrefixes = []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"}

// adapterIndex 返回名字以 name 开头的适配器 ifIndex。
func adapterIndex(name string) (int, bool) {
	adapters, err := getAdaptersAddresses()
	if err != nil {
		return 0, false
	}
	for adapter := adapters; adapter != nil; adapter = adapter.Next {
		if strings.HasPrefix(windows.UTF16PtrToString(adapter.FriendlyName), name) {
			return int(adapter.IfIndex), true
		}
	}
	return 0, false
}

// ClearTunRoutes 删掉残留 TUN 适配器上的 auto-route 路由，返回删掉的条数。
//
// 内核被强杀在"关 TUN 的中途"时会留下这种适配器：会话没了、半段默认路由
// 还在，所有出站流量被吸进一个没人接收的 TUN——现象是"本机有 IPv6 地址却
// 什么都连不上"，而且不会自愈。只有把这几条路由删掉，物理网卡的默认路由
// 才会重新生效。命令失败只影响计数，不返回错误：残留清理不该成为新的失败面。
func ClearTunRoutes(ctx context.Context, name string) int {
	index, ok := adapterIndex(name)
	if !ok {
		return 0
	}
	removed := 0
	for _, prefix := range tunRoutePrefixes {
		family := "ipv4"
		if strings.Contains(prefix, ":") {
			family = "ipv6"
		}
		args := []string{
			"interface", family, "delete", "route",
			"prefix=" + prefix,
			"interface=" + strconv.Itoa(index),
			"store=active",
		}
		if err := exec.CommandContext(ctx, "netsh", args...).Run(); err == nil {
			removed++
		}
	}
	return removed
}

// AdapterPresent 报告名字以 name 开头的网络适配器是否还在系统里。
// 按前缀匹配：内核被强杀过一次之后 Windows 可能留下 "FreeV6TUN 2" 这样的
// 同名副本，两者都要算"还在"。
func AdapterPresent(name string) bool {
	adapters, err := getAdaptersAddresses()
	if err != nil {
		// 枚举失败无法判定：当作已经消失，别让停止流程卡在一个查不动的状态上。
		return false
	}
	for adapter := adapters; adapter != nil; adapter = adapter.Next {
		if strings.HasPrefix(windows.UTF16PtrToString(adapter.FriendlyName), name) {
			return true
		}
	}
	return false
}

// WaitAdapterGone 等待适配器真的从系统里消失，最多等到 ctx 结束。
//
// 关掉 TUN 之后必须确认这一点再返回：默认路由要等适配器没了才回到物理网卡，
// 提前返回就让紧接着的启动/检测又踩进那个"有地址但出不去"的黑洞。
// 纯 syscall 轮询，不起子进程。
func WaitAdapterGone(ctx context.Context, name string, poll time.Duration) bool {
	if poll <= 0 {
		poll = 10 * time.Millisecond
	}
	for {
		if !AdapterPresent(name) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(poll):
		}
	}
}
