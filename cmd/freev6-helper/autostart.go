package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Per-user autostart runs the desktop exe at logon through a Task Scheduler
// task, NOT an HKCU Run entry. freev6-desktop.exe is manifested
// requireAdministrator, so a Run entry asks for a UAC consent prompt at every
// logon — and when that logon-time consent never shows, nothing starts at all
// while Task Manager still reports the entry "enabled". A logon task created
// with /RL HIGHEST is launched by the Task Scheduler service itself:
// elevated, silent, no prompt. Omitting /RU keeps it on an interactive token
// (no stored password), so it fires for this user's logon — the --minimized
// flag then shows just the tray icon. It requires an elevated caller (the
// helper always is: sidecar of the requireAdministrator desktop app).
const (
	autostartTaskName = "FreeV6 Autostart"
	// Legacy HKCU Run entry written by older builds; both apply and remove
	// drop it so the two mechanisms can never double-launch the app.
	legacyRunKey   = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	legacyRunValue = "FreeV6"
)

// desktopExePath is where freev6-desktop.exe lives: FREEV6_ROOT is always the
// desktop exe's own directory (main.rs passes current_exe().parent()).
func desktopExePath(root string) string {
	return filepath.Join(root, "freev6-desktop.exe")
}

// applyAutoStart registers the logon task for root's exe. The value is a full
// command line passed to schtasks as a single argument (no shell), so the
// quoted exe path survives for install dirs containing spaces.
func applyAutoStart(root string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("autostart is only supported on windows")
	}
	exe := desktopExePath(root)
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("desktop executable not found: %s", exe)
	}
	cmdLine := fmt.Sprintf(`"%s" --minimized`, exe)
	// /F replaces an existing task (also refreshes the path after updates
	// move the install dir). A failure surfaces to the PUT handler so the GUI
	// switch reverts instead of claiming a state that never took effect.
	output, err := exec.Command(
		"schtasks", "/Create", "/F",
		"/TN", autostartTaskName,
		"/TR", cmdLine,
		"/SC", "ONLOGON",
		"/RL", "HIGHEST",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks create failed: %s", strings.TrimSpace(string(output)))
	}
	dropLegacyRunEntry() // migrate away from old builds' Run entry
	return nil
}

// removeAutoStart deletes the logon task; removing an absent task is a no-op.
func removeAutoStart() error {
	if runtime.GOOS != "windows" {
		return nil
	}
	dropLegacyRunEntry() // old builds registered a Run entry instead
	if !autoStartEnabled() {
		return nil
	}
	output, err := exec.Command(
		"schtasks", "/Delete", "/TN", autostartTaskName, "/F",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks delete failed: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

// autoStartEnabled reports whether the logon task exists. schtasks exits
// non-zero when it doesn't — locale-independent, no message parsing.
func autoStartEnabled() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	return exec.Command("schtasks", "/Query", "/TN", autostartTaskName).Run() == nil
}

// dropLegacyRunEntry best-effort removes the HKCU Run entry older builds
// wrote; a missing value just makes reg delete fail, which is fine here.
func dropLegacyRunEntry() {
	_ = exec.Command("reg", "delete", legacyRunKey, "/v", legacyRunValue, "/f").Run()
}
