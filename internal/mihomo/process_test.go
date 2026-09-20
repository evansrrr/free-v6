package mihomo

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestEnsurePIDAvailableRemovesStalePID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo.pid")
	if err := os.WriteFile(path, []byte("1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensurePIDAvailable(path, func(int) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected stale pid file removed, got %v", err)
	}
}

func TestEnsurePIDAvailableRejectsRunningPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensurePIDAvailable(path, func(pid int) bool { return pid == os.Getpid() }); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("expected ErrAlreadyRunning, got %v", err)
	}
}

func TestEnsurePIDAvailableRemovesInvalidPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo.pid")
	if err := os.WriteFile(path, []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensurePIDAvailable(path, func(int) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected invalid pid file removed, got %v", err)
	}
}

func TestStatusCleansInvalidPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo.pid")
	if err := os.WriteFile(path, []byte("invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pid, running, err := Status(path)
	if err != nil || pid != 0 || running {
		t.Fatalf("expected stopped status, got pid=%d running=%t err=%v", pid, running, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected invalid pid file removed, got %v", err)
	}
}
