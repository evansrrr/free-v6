//go:build !windows

package mihomo

import (
	"os"
	"path/filepath"
	"syscall"
)

// withStartLock serializes Start across processes using flock on a shared lock
// file (the Windows side uses a named mutex). flock releases on process exit,
// so no stale-lock cleanup is needed.
func withStartLock(fn func() error) error {
	lockPath := filepath.Join(os.TempDir(), "freev6-mihomo-start.lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }()
	return fn()
}
