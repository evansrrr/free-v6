//go:build windows

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
)

// processSynchronize 只请求"可以被等待"的权限；x/sys 没有导出这个常量。
const processSynchronize = 0x00100000

// watchParent 在桌面壳退出后调用 onGone（非阻塞，内部起一个线程等待）。
//
// 语义：
//   - FREEV6_PARENT_PID 缺失 → 不启用（旧壳不传，功能不降级）；
//   - 句柄打不开 → 父已经退出，立即收尾。不能当成"不监视"——那个窗口正好
//     出现在启动竞态里，误判成"不监视"就会留下永久的孤儿 helper；
//   - 句柄一旦打开就钉住了父进程对象，不存在 PID 复用问题，直接等待退出即可。
func watchParent(onGone func()) {
	raw := strings.TrimSpace(os.Getenv("FREEV6_PARENT_PID"))
	if raw == "" {
		return
	}
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 0 {
		fmt.Fprintf(os.Stderr, "helper: invalid FREEV6_PARENT_PID %q; parent watch disabled\n", raw)
		return
	}
	handle, err := windows.OpenProcess(processSynchronize, false, uint32(pid))
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: parent process %d is already gone (%v); shutting down\n", pid, err)
		onGone()
		return
	}
	go func() {
		defer windows.CloseHandle(handle)
		_, _ = windows.WaitForSingleObject(handle, windows.INFINITE)
		fmt.Fprintf(os.Stderr, "helper: parent process %d exited; shutting down\n", pid)
		onGone()
	}()
}
