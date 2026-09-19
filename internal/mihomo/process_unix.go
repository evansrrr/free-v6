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
