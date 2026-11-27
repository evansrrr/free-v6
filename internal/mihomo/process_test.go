package mihomo

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestEnsurePIDAvailableRemovesStalePID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo.pid")
	if err := os.WriteFile(path, []byte("1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensurePIDAvailable(path, func(int) bool { return false }, func(int) bool { return true }); err != nil {
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
	if err := ensurePIDAvailable(path, func(pid int) bool { return pid == os.Getpid() }, func(int) bool { return true }); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("expected ErrAlreadyRunning, got %v", err)
	}
}

func TestEnsurePIDAvailableClearsLiveUnrelatedPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo.pid")
	// A live pid that is NOT one of our cores must be treated as stale, not as
	// "already running" — otherwise a reused pid blocks start forever.
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensurePIDAvailable(path, func(pid int) bool { return pid == os.Getpid() }, func(int) bool { return false }); err != nil {
		t.Fatalf("expected unrelated live pid to be cleared, got %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected unrelated pid file removed, got %v", err)
	}
}

func TestEnsurePIDAvailableRemovesInvalidPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo.pid")
	if err := os.WriteFile(path, []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensurePIDAvailable(path, func(int) bool { return true }, func(int) bool { return true }); err != nil {
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

// TestStopRefusesUnrelatedPID proves Stop never TerminateProcess a pid that
// is not a FreeV6 core (the pid file can point at a reused pid after a crash).
func TestStopRefusesUnrelatedPID(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("pid identity check is windows-only")
	}
	child := exec.Command("cmd.exe", "/c", "ping", "-n", "10", "127.0.0.1")
	if err := child.Start(); err != nil {
		t.Skipf("cannot spawn child: %v", err)
	}
	defer func() { _ = child.Process.Kill(); _, _ = child.Process.Wait() }()
	path := filepath.Join(t.TempDir(), "mihomo.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Stop(path); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("expected refusal for unrelated pid, got %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("pid file must be left in place on refusal, got %v", statErr)
	}
	time.Sleep(200 * time.Millisecond)
	if !IsRunning(child.Process.Pid) {
		t.Fatal("unrelated child was killed")
	}
}

// Stub binaries via .bat: TestConfig only needs exit code + combined output
// passthrough, no real mihomo required at unit level (real -t is E2E'd).
func TestConfigPassAndFail(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("bat stubs are windows-only")
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("mixed-port: 7890\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	pass := filepath.Join(dir, "pass.bat")
	if err := os.WriteFile(pass, []byte("@rem ok\r\n@exit /b 0\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := TestConfig(pass, configPath, dir); err != nil {
		t.Fatalf("passing stub must yield nil, got %v", err)
	}

	fail := filepath.Join(dir, "fail.bat")
	if err := os.WriteFile(fail, []byte("@echo level=error msg=proxy [x] dialer-proxy [y] not found\r\n@exit /b 1\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := TestConfig(fail, configPath, dir)
	if err == nil || !strings.Contains(err.Error(), "dialer-proxy") {
		t.Fatalf("failing stub must surface its output, got %v", err)
	}
}

func TestConfigRejectsEmptyBinary(t *testing.T) {
	if err := TestConfig("  ", "config.yaml", "."); err == nil {
		t.Fatal("expected empty-path error")
	}
}

func TestCoreMetaRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo.pid")
	writeCoreMeta(path, 12345)
	identity, ok := readCoreMeta(path)
	if !ok || identity.PID != 12345 {
		t.Fatalf("meta round-trip failed: %+v ok=%v", identity, ok)
	}
	if !coreIdentityMatches(path, 12345) {
		t.Fatal("a recorded pid whose start time cannot be re-read must match (legacy fallback)")
	}
	removeCoreMeta(path)
	if _, ok := readCoreMeta(path); ok {
		t.Fatal("meta must be gone after removeCoreMeta")
	}
}
