package mihomo

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var ErrAlreadyRunning = errors.New("mihomo is already running")

func Start(binaryPath, configPath, pidPath, logPath string) (int, error) {
	if strings.TrimSpace(binaryPath) == "" {
		return 0, errors.New("mihomo binary path is empty")
	}
	if pid, err := readPID(pidPath); err == nil && IsRunning(pid) {
		return 0, ErrAlreadyRunning
	}
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o700); err != nil {
		return 0, fmt.Errorf("create runtime directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, fmt.Errorf("create log directory: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open mihomo log: %w", err)
	}
	cmd := exec.Command(binaryPath, "-f", configPath)
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
		if errors.Is(err, os.ErrNotExist) {
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

func Status(pidPath string) (pid int, running bool, err error) {
	pid, err = readPID(pidPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
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
