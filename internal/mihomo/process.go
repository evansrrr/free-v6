package mihomo

import (
	"context"
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
	if strings.TrimSpace(binaryPath) == "" {
		return errors.New("mihomo binary path is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binaryPath, "-t", "-d", homeDir, "-f", configPath).CombinedOutput()
	if err == nil {
		return nil
	}
	details := strings.TrimSpace(string(output))
	if details == "" {
		details = err.Error()
	}
	return fmt.Errorf("mihomo -t: %s", details)
}

// Start 以 -d homeDir 显式固定 mihomo 的工作目录（GeoSite.dat、cache.db、
// ruleset 缓存都落在这里）：不传时它取进程 cwd/可执行文件目录，不同机器
// 上位置飘忽导致 geodata “明明有也找不到”。homeDir 由调用方统一传 state
// 目录，与 EnsureGeodata 的写入位置保持一致。
func Start(binaryPath, configPath, homeDir, pidPath, logPath string) (int, error) {
	if strings.TrimSpace(binaryPath) == "" {
		return 0, errors.New("mihomo binary path is empty")
	}
	if err := ensurePIDAvailable(pidPath, IsRunning); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o700); err != nil {
		return 0, fmt.Errorf("create runtime directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, fmt.Errorf("create log directory: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open mihomo log: %w", err)
	}
	cmd := exec.Command(binaryPath, "-d", homeDir, "-f", configPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, fmt.Errorf("start mihomo: %w", err)
	}
	_ = logFile.Close()
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600); err != nil {
		_ = cmd.Process.Kill()
		return 0, fmt.Errorf("write mihomo pid: %w", err)
	}
	return cmd.Process.Pid, nil
}

func Stop(pidPath string) error {
	pid, err := readPID(pidPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "invalid pid file") {
			_ = os.Remove(pidPath)
			return nil
		}
		return err
	}
	process, err := os.FindProcess(pid)
	if err == nil && IsRunning(pid) {
		if err := process.Kill(); err != nil {
			return fmt.Errorf("stop mihomo process %d: %w", pid, err)
		}
	}
	return os.Remove(pidPath)
}

func ensurePIDAvailable(path string, running func(int) bool) error {
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
	if running(pid) {
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
