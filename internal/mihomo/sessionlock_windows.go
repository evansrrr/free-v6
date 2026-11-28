//go:build windows

package mihomo

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// sessionLockName 是跨进程会话锁的 Win32 对象名。
//
// 这是**跨版本 ABI**：新旧二进制必须互斥，名字永远不要改。改的只是 Go 侧的
// 标识符与文件名（startlock → sessionlock），因为它的作用范围已经从
// "Start 的检查→spawn→写 pid" 扩展到"停止的清理段"。
const sessionLockName = `Local\FreeV6-MihomoStart`

// sessionLockTimeout 是等待锁的上限。持有者正常时占锁是毫秒级，最坏是停止
// 序列的关 TUN + 停核 + 清路由（≈9s）；给 60s 是兜住"持有者挂死"，此时返回
// 可读错误远好过让 UI 永久卡住。
const sessionLockTimeout = 60 * time.Second

// WithSessionLock 在持有跨进程会话锁的情况下执行 fn。
//
// 锁只覆盖两个临界区：Start 的"检查→spawn→写 pid"，和停止的
// "重读→停核→清路由→删快照"。调用方**不得**在 fn 内部再次获取它，也不得在
// fn 内部调用 mihomo.Start——Win32 互斥量的重入是按线程计的，而 goroutine
// 会在 OS 线程之间迁移，第二次等待很可能落在另一个线程上，直接自死锁。
func WithSessionLock(fn func() error) error {
	name, err := windows.UTF16PtrFromString(sessionLockName)
	if err != nil {
		return err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)

	// 有界等待必须看返回码：WAIT_TIMEOUT 不是 error（x/sys 只在 WAIT_FAILED
	// 时返回 err），把它当成"拿到锁"会让两个进程同时进入临界区。
	// WAIT_ABANDONED 表示持有者已经死了、锁已转移给我们，按获取成功处理。
	event, err := windows.WaitForSingleObject(handle, uint32(sessionLockTimeout/time.Millisecond))
	if err != nil {
		return fmt.Errorf("acquire session lock: %w", err)
	}
	if event != windows.WAIT_OBJECT_0 && event != windows.WAIT_ABANDONED {
		return fmt.Errorf("session lock busy (waited %v): another start/stop is running", sessionLockTimeout)
	}
	defer windows.ReleaseMutex(handle)
	return fn()
}
