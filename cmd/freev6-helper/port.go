package main

import (
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// listenOrTakeOver binds the helper API port, reclaiming it from an orphaned
// previous helper: killing the desktop via Task Manager leaves its helper
// child alive (Windows doesn't cascade-kill processes), and that orphan keeps
// holding 127.0.0.1:13335 — every relaunch then died on the bind error.
// A holder whose desktop parent is still alive means another running
// instance: leave it alone and surface the original error (the caller exits,
// exactly like the pre-takeover behavior).
func listenOrTakeOver(addr string) (net.Listener, error) {
	listener, err := net.Listen("tcp", addr)
	if err == nil {
		return listener, nil
	}
	if runtime.GOOS != "windows" {
		return nil, err
	}
	owner := listeningPID(netstatOutput(), addr)
	if owner == 0 || owner == os.Getpid() || !isHelperPID(owner) || !isOrphaned(owner) {
		return nil, err
	}
	// The holder is an orphaned helper — kill it and poll for the port.
	_ = killProcess(owner)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
		if listener, retryErr := net.Listen("tcp", addr); retryErr == nil {
			return listener, nil
		}
	}
	return nil, err
}

func netstatOutput() string {
	out, err := exec.Command("netstat", "-ano").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// listeningPID finds the PID listening on addr (e.g. "127.0.0.1:13335") in
// `netstat -ano` output. The column layout (proto, local, foreign, state,
// pid) is stable across locales; only TCP LISTENING rows count, so the
// server-side of an accepted connection on the same port is never picked.
func listeningPID(output, addr string) int {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") || fields[1] != addr {
			continue
		}
		if !strings.EqualFold(fields[3], "LISTENING") {
			continue
		}
		if pid, err := strconv.Atoi(fields[4]); err == nil && pid > 0 {
			return pid
		}
	}
	return 0
}

// isHelperPID / isOrphaned / killProcess are platform hooks implemented in
// port_windows.go (Win32) and port_other.go (stubs). They used to live here as
// `powershell Get-CimInstance` + `tasklist` calls, which failed closed — any
// policy, trimmed Windows image or security product that blocks those two
// binaries turned the takeover off, and the stale helper kept :13335 while the
// UI 401'd against it with a token from the previous launch.
