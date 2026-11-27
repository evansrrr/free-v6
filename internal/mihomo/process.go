package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var ErrAlreadyRunning = errors.New("mihomo is already running")

// TestConfig 跑真实 `mihomo -t` 校验渲染产物：链式合并可能引入 mihomo 自己
// 才能发现的问题（未知 type、非法字段值…），必须在网络快照之前暴露。
// 仅链式路径调用，经典路径不承担这份耗时与失败面。
func TestConfig(binaryPath, configPath, homeDir string) error {
	return TestConfigWithin(binaryPath, configPath, homeDir, 15*time.Second)
}

// ErrConfigTestTimeout 表示 `-t` 在预算内没跑完（典型场景：离线，
// rule-provider 拉取把时间耗光）。调用方据此判断"这条结论不作数"。
var ErrConfigTestTimeout = errors.New("mihomo -t 超时")

// TestConfigWithin 与 TestConfig 相同，但允许指定预算。内核安装自检用
// 3 秒的小预算：跑不完就当没结论，不拿它当放行/拦截的依据。
func TestConfigWithin(binaryPath, configPath, homeDir string, budget time.Duration) error {
	if strings.TrimSpace(binaryPath) == "" {
		return errors.New("mihomo binary path is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	output, err := exec.CommandContext(ctx, binaryPath, "-t", "-d", homeDir, "-f", configPath).CombinedOutput()
	if err == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrConfigTestTimeout
	}
	details := strings.TrimSpace(string(output))
	if details == "" {
		details = err.Error()
	}
	return fmt.Errorf("mihomo -t: %s", details)
}

// Start 以 -d homeDir 显式固定 mihomo 的工作目录（cache.db、ruleset 缓存、
// 日志都落在这里）：不传时它取进程 cwd/可执行文件目录，不同机器上位置飘忽，
// 缓存就会散落到随机目录。homeDir 由调用方统一传 state 目录。
func Start(binaryPath, configPath, homeDir, pidPath, logPath string) (int, error) {
	if strings.TrimSpace(binaryPath) == "" {
		return 0, errors.New("mihomo binary path is empty")
	}
	// Serialize the whole check-spawn-write-pid sequence across processes: the
	// helper and the CLI can both call Start, and a second instance's pid write
	// would otherwise orphan the first core (which then can no longer be stopped).
	pid := 0
	err := withStartLock(func() error {
		if err := ensurePIDAvailable(pidPath, IsRunning, processIsOurs); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(pidPath), 0o700); err != nil {
			return fmt.Errorf("create runtime directory: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
			return fmt.Errorf("create log directory: %w", err)
		}
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return fmt.Errorf("open mihomo log: %w", err)
		}
		cmd := exec.Command(binaryPath, "-d", homeDir, "-f", configPath)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		if err := cmd.Start(); err != nil {
			_ = logFile.Close()
			return fmt.Errorf("start mihomo: %w", err)
		}
		// Reap the child: without Wait the process handle leaks on Windows and
		// the child becomes a zombie on Unix (where kill(pid,0) then always
		// reports "running" and every stop waits out its full timeout).
		// Stdout/Stderr are *os.File, not pipes, so no copy goroutines needed.
		go func() { _ = cmd.Wait() }()
		_ = logFile.Close()
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600); err != nil {
			_ = cmd.Process.Kill()
			return fmt.Errorf("write mihomo pid: %w", err)
		}
		writeCoreMeta(pidPath, cmd.Process.Pid)
		pid = cmd.Process.Pid
		return nil
	})
	if err != nil {
		return 0, err
	}
	return pid, nil
}

// coreIdentity is the stable identity of a spawned core, persisted alongside
// the pid file so a reused pid (after a hard crash) can be told apart from the
// core we actually started. The pid file itself stays single-line numeric for
// the Rust shell and older CLI/helper builds that read it directly.
type coreIdentity struct {
	PID       int   `json:"pid"`
	StartTime int64 `json:"startTime"`
}

const coreMetaSuffix = ".meta"

func coreMetaPath(pidPath string) string { return pidPath + coreMetaSuffix }

// writeCoreMeta persists {pid, startTime} atomically. A failure is non-fatal:
// the caller falls back to the image-path check (legacy behaviour).
func writeCoreMeta(pidPath string, pid int) {
	identity := coreIdentity{PID: pid}
	if start, err := processStartTime(pid); err == nil {
		identity.StartTime = start
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return
	}
	path := coreMetaPath(pidPath)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// readCoreMeta returns the stored identity, or ok=false when absent/invalid.
func readCoreMeta(pidPath string) (coreIdentity, bool) {
	data, err := os.ReadFile(coreMetaPath(pidPath))
	if err != nil {
		return coreIdentity{}, false
	}
	var identity coreIdentity
	if err := json.Unmarshal(data, &identity); err != nil {
		return coreIdentity{}, false
	}
	return identity, true
}

// coreIdentityMatches reports whether the live pid is the same process we
// recorded at start time. A missing meta file is treated as "match" (legacy:
// the image-path check upstream is the only gate then).
func coreIdentityMatches(pidPath string, pid int) bool {
	identity, ok := readCoreMeta(pidPath)
	if !ok || identity.PID != pid {
		return true
	}
	live, err := processStartTime(pid)
	if err != nil {
		// Cannot read the start time; the image check already passed.
		return true
	}
	return identity.StartTime == live
}

func removeCoreMeta(pidPath string) { _ = os.Remove(coreMetaPath(pidPath)) }

func Stop(pidPath string) error {
	pid, err := readPID(pidPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "invalid pid file") {
			_ = os.Remove(pidPath)
			removeCoreMeta(pidPath)
			return nil
		}
		return err
	}
	process, err := os.FindProcess(pid)
	if err == nil && IsRunning(pid) {
		if !processIsOurs(pid) {
			// The pid was reused by an unrelated program after a crash: never
			// TerminateProcess an innocent process (the helper runs elevated).
			return fmt.Errorf("refusing to stop pid %d: not a FreeV6 core process", pid)
		}
		if !coreIdentityMatches(pidPath, pid) {
			// The pid is another core process that reused our pid — also refuse.
			return fmt.Errorf("refusing to stop pid %d: process identity changed", pid)
		}
		if err := process.Kill(); err != nil {
			return fmt.Errorf("stop mihomo process %d: %w", pid, err)
		}
	}
	removeCoreMeta(pidPath)
	return os.Remove(pidPath)
}

// ensurePIDAvailable clears a stale pid file, or fails when the pid belongs to
// a live FreeV6 core. running reports liveness; ours reports whether the pid
// actually is one of our cores (see processIsOurs) — a live-but-unrelated pid
// is treated as stale and removed rather than blocking start forever.
func ensurePIDAvailable(path string, running, ours func(int) bool) error {
	pid, err := readPID(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("remove invalid mihomo pid file: %w", removeErr)
		}
		return nil
	}
	if running(pid) && ours(pid) {
		return ErrAlreadyRunning
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale mihomo pid file: %w", err)
	}
	return nil
}

func Status(pidPath string) (pid int, running bool, err error) {
	pid, err = readPID(pidPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "invalid pid file") {
			_ = os.Remove(pidPath)
			return 0, false, nil
		}
		return 0, false, err
	}
	return pid, IsRunning(pid), nil
}

func IsRunning(pid int) bool {
	return pid > 0 && processRunning(pid)
}

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid pid file %q", path)
	}
	return pid, nil
}
