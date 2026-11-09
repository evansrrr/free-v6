//go:build windows

package main

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// All process introspection here goes through Win32 directly. The previous
// implementation shelled out to `powershell -Command Get-CimInstance` and
// `tasklist`, and treated any failure of those two as "can't tell → don't touch
// the port". On images where PowerShell is blocked (AppLocker/ASR policy,
// 精简版 Windows, security software that kills console children) the takeover
// silently never ran: the orphaned helper from a previous launch kept
// 127.0.0.1:13335, the new helper exited on the bind error, and the UI kept
// calling the *old* helper with the *new* token — the "组件异常" dialog plus a
// log full of `helper 不可用: unauthorized` pairing that users reported.

const stillActive = 259 // STILL_ACTIVE from winbase.h

// killProcess terminates the holder. Processes are only ever killed after
// isHelperPID + isOrphaned both agree, so this can't reach an innocent process.
func killProcess(pid int) error {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	return windows.TerminateProcess(handle, 1)
}

// isHelperPID confirms the port holder is one of ours before killing it — never
// terminate an unrelated process squatting the port. The image path comes from
// the kernel, so unlike `tasklist` there is no localized column layout and no
// stdout that another tool could poison. The substring covers the installed
// (freev6-helper.exe) and dev sidecar (freev6-helper-<triple>.exe) names.
func isHelperPID(pid int) bool {
	path, err := processImagePath(uint32(pid))
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(path), "freev6-helper")
}

// isOrphaned reports whether the holder's desktop parent is gone (the Task
// Manager kill / crash scenario). A live parent means another running
// instance — the new helper must not steal its port. "Unknown" still means
// "don't kill".
//
// The parent PID alone is not proof of life: Windows recycles PIDs, so the
// dead desktop's id may now belong to an unrelated process. A real parent
// always starts before its child, so a "parent" whose creation time is later
// than the holder's is a reused id — i.e. the holder is an orphan.
func isOrphaned(pid int) bool {
	parent, err := parentPIDOf(uint32(pid))
	if err != nil || parent == 0 {
		return false
	}
	alive, err := processAlive(parent)
	if err != nil {
		return false // 读不到父进程状态 → 保守，不动端口
	}
	if !alive {
		return true // 父进程已退出 → 残留进程，可以接管
	}
	parentStart, err := processStartTime(parent)
	if err != nil {
		return false
	}
	holderStart, err := processStartTime(uint32(pid))
	if err != nil {
		return false
	}
	return parentStart > holderStart
}

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

// processAlive reports whether pid is still running. Access denied is treated
// as "alive but unreadable" so a locked-down parent never causes a kill.
func processAlive(pid uint32) (bool, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		if err == windows.ERROR_ACCESS_DENIED {
			return true, nil
		}
		return false, nil // 进程已退出
	}
	defer windows.CloseHandle(handle)
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return true, err
	}
	return code == stillActive, nil
}

// processStartTime returns the creation time as a comparable FILETIME value
// (both sides use the same 1601 epoch, so a raw compare is enough).
func processStartTime(pid uint32) (int64, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(handle)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	return creation.Nanoseconds(), nil
}

func parentPIDOf(pid uint32) (uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return 0, err
	}
	for {
		if entry.ProcessID == pid {
			return entry.ParentProcessID, nil
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			return 0, err
		}
	}
}
