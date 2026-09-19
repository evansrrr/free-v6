//go:build windows

package mihomo

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func processRunning(pid int) bool {
	output, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		listedPID, err := strconv.Atoi(fields[1])
		if err == nil && listedPID == pid {
			return true
		}
	}
	return false
}
