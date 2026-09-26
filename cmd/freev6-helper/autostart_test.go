package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// requireElevated skips when the test shell lacks admin: creating a
// /RL HIGHEST task is denied otherwise. The helper itself always runs
// elevated (sidecar of the requireAdministrator desktop app).
func requireElevated(t *testing.T) {
	t.Helper()
	if exec.Command("net", "session").Run() != nil {
		t.Skip("autostart task creation requires an elevated shell")
	}
}

// Round-trips the real logon task: apply → query → assert command line →
// remove. Refuses to run if a task already exists so a developer's real
// autostart is never clobbered.
func TestAutoStartRoundTrip(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("autostart is windows-only")
	}
	requireElevated(t)
	if autoStartEnabled() {
		t.Skip("FreeV6 autostart task already present; refusing to overwrite")
	}

	root := t.TempDir()
	exe := filepath.Join(root, "freev6-desktop.exe")
	if err := os.WriteFile(exe, []byte("dummy"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = removeAutoStart() })

	if err := applyAutoStart(root); err != nil {
		t.Fatalf("applyAutoStart: %v", err)
	}
	if !autoStartEnabled() {
		t.Fatal("logon task missing after apply")
	}

	output, _ := exec.Command("schtasks", "/Query", "/TN", autostartTaskName, "/V", "/FO", "LIST").CombinedOutput()
	text := string(output)
	// Headers are localized; assert on our own values only. The exe path must
	// appear whole (schtasks stores the quoted /TR string as one element, so
	// spaced install dirs don't break at the space) plus the tray-only flag.
	if !strings.Contains(text, "--minimized") {
		t.Fatalf("task must launch tray-only (--minimized): %s", text)
	}
	if !strings.Contains(text, exe) {
		t.Fatalf("task must contain the exe path, got: %s", text)
	}

	if err := removeAutoStart(); err != nil {
		t.Fatalf("removeAutoStart: %v", err)
	}
	if autoStartEnabled() {
		t.Fatal("logon task still present after remove")
	}
}

// Removing when nothing exists must stay a silent no-op (idempotent disable).
func TestRemoveAutoStartIdempotent(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("autostart is windows-only")
	}
	if autoStartEnabled() {
		t.Skip("FreeV6 autostart task present; refusing to remove a real entry")
	}
	if err := removeAutoStart(); err != nil {
		t.Fatalf("remove on absent task should be nil, got: %v", err)
	}
}

// Settings default: autostart off (requirement: 开关默认关闭).
func TestSettingsDefaultAutoStartOff(t *testing.T) {
	if (settings{}).AutoStart {
		t.Fatal("autoStart must default to false")
	}
	if defaultSettings().AutoStart {
		t.Fatal("defaultSettings must keep autoStart false")
	}
}
