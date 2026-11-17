//go:build windows

package network

import (
	"context"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

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
