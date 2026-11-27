//go:build windows

package mihomo

import (
	"strings"

	"golang.org/x/sys/windows"
)

// stillActive 是 Windows 的 STILL_ACTIVE 退出码：GetExitCodeProcess 对它
// 返回 259 表示进程仍在运行。
const stillActive = 259

// processRunning 用 OpenProcess + GetExitCodeProcess 判断进程是否存活。
// 不 spawn tasklist 子进程 —— 旧实现每次调用都要起一个进程（~50-100ms），
// 在停止免流路径上（IsRunning 校验）拖慢响应。纯句柄查询微秒级返回。
func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// 进程不存在，或无权限打开句柄（fail-closed 视为未运行）。
		return false
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}
	return exitCode == stillActive
}

// processIsOurs reports whether the pid actually belongs to a FreeV6-managed
// core (f6-core or legacy mihomo). Windows recycles pids quickly, so a bare
// liveness check is not enough to decide "already running" or to kill a
// process: after a hard crash the pid file may point at an unrelated program.
func processIsOurs(pid int) bool {
	if pid <= 0 {
		return false
	}
	path, err := processImagePath(uint32(pid))
	if err != nil {
		return false
	}
	name := strings.ToLower(path)
	return strings.Contains(name, "f6-core") || strings.Contains(name, "mihomo")
}

// processImagePath returns the full executable path of pid from the kernel,
// independent of localized tool output. An empty result means the process is
// gone or unreadable — callers treat that as "not ours".
func processImagePath(pid uint32) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer[:size]), nil
}
