package main

import (
	"fmt"
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

// Round-trips the real autostart pieces: apply → assert the logon task plus
// the Task Manager marker entry (including migration of a legacy direct-exe
// entry) → remove. Refuses to run when a task already exists so a
// developer's real autostart is never clobbered.
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
	helper := filepath.Join(root, "freev6-helper.exe")
	for _, file := range []string{exe, helper} {
		if err := os.WriteFile(file, []byte("dummy"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = removeAutoStart() })

	// Seed the legacy direct-exe Run entry — apply must migrate it in place.
	legacy := fmt.Sprintf(`"%s" --minimized`, exe)
	if out, err := exec.Command("reg", "add", autostartRunKey, "/v", autostartValueName,
		"/t", "REG_SZ", "/d", legacy, "/f").CombinedOutput(); err != nil {
		t.Fatalf("seed legacy Run entry: %v: %s", err, out)
	}

	if err := applyAutoStart(root); err != nil {
		t.Fatalf("applyAutoStart: %v", err)
	}
	if !autoStartEnabled() {
		t.Fatal("logon task missing after apply")
	}

	query, _ := exec.Command("reg", "query", autostartRunKey, "/v", autostartValueName).CombinedOutput()
	text := string(query)
	// The marker must target the asInvoker helper (pointing at the desktop
	// exe would raise UAC at logon) and pass the no-op --autostart flag.
	if !strings.Contains(text, `"`+helper+`"`) {
		t.Fatalf("Run marker must contain the quoted helper path, got: %s", text)
	}
	if !strings.Contains(text, "--autostart") {
		t.Fatalf("Run marker must pass --autostart, got: %s", text)
	}
	if strings.Contains(text, "freev6-desktop.exe") {
		t.Fatalf("Run entry must not point at the desktop exe (UAC at logon): %s", text)
	}

	taskQuery, _ := exec.Command("schtasks", "/Query", "/TN", autostartTaskName, "/V", "/FO", "LIST").CombinedOutput()
	taskText := string(taskQuery)
	// Headers are localized; assert on our own values only. The exe path must
	// appear whole (schtasks stores the quoted /TR string as one element, so
	// spaced install dirs don't break at the space).
	if !strings.Contains(taskText, "--minimized") || !strings.Contains(taskText, "--autostart") {
		t.Fatalf("task must launch tray-only desktop (--minimized --autostart): %s", taskText)
	}
	if !strings.Contains(taskText, exe) {
		t.Fatalf("task must contain the exe path, got: %s", taskText)
	}

	if err := removeAutoStart(); err != nil {
		t.Fatalf("removeAutoStart: %v", err)
	}
	if autoStartEnabled() {
		t.Fatal("logon task still present after remove")
	}
	if out, err := exec.Command("reg", "query", autostartRunKey, "/v", autostartValueName).CombinedOutput(); err == nil {
		t.Fatalf("Run marker still present after remove: %s", out)
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
