//go:build !windows

package mihomo

import (
	"os"
	"syscall"
)

func processRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// processIsOurs cannot be determined cheaply on non-Windows without parsing
// /proc; keep the pre-existing kill behaviour there (the pid-reuse mis-kill
// is a Windows elevated-process concern). Always true preserves semantics.
func processIsOurs(int) bool { return true }

// processStartTime is not used for identity on non-Windows (no pid-reuse
// mis-kill concern); returning 0 keeps the meta-based check a no-op there.
func processStartTime(int) (int64, error) { return 0, nil }
