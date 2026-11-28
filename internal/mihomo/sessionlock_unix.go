//go:build !windows

package mihomo

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	sessionLockTimeout      = 60 * time.Second
	sessionLockPollInterval = 50 * time.Millisecond
)

// WithSessionLock 在持有跨进程会话锁的情况下执行 fn（Windows 侧用命名互斥量，
// 这里用共享锁文件上的 flock；flock 在进程退出时自动释放，无需清理残留锁）。
//
// 与 Windows 版同样的约束：锁只覆盖 Start 与停止的临界区，fn 内部不得再取锁
// 或调用 Start。
func WithSessionLock(fn func() error) error {
	lockPath := filepath.Join(os.TempDir(), "freev6-mihomo-start.lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	deadline := time.Now().Add(sessionLockTimeout)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK || time.Now().After(deadline) {
			return fmt.Errorf("session lock busy (waited %v): %w", sessionLockTimeout, err)
		}
		time.Sleep(sessionLockPollInterval)
	}
	defer func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }()
	return fn()
}
