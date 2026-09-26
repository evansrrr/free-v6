package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Autostart is two cooperating pieces:
//
//  1. A Task Scheduler logon task ("FreeV6 Autostart", ONLOGON + HIGHEST)
//     really launches the desktop exe — silently and elevated, so the
//     requireAdministrator manifest never raises a UAC prompt at logon. (A Run
//     entry pointing at the exe does prompt; when the logon-time consent
//     never shows, nothing starts while Task Manager still reports the entry
//     "enabled".)
//  2. An HKCU Run entry kept present for Task Manager's "Startup apps" tab —
//     where users view, disable and enable this autostart. The entry runs
//     helper --autostart, an asInvoker marker that exits immediately. The
//     real gate is StartupApproved\Run\FreeV6, which Task Manager's toggle
//     writes and which the task-launched desktop exe (--autostart) checks,
//     exiting when disabled. The task fires via its own logon trigger — no
//     requester, no UAC, no on-demand run permissions.
//
// The helper always runs elevated (sidecar of the requireAdministrator
// desktop app), which creating the /RL HIGHEST task requires.
const (
	autostartTaskName  = "FreeV6 Autostart"
	autostartRunKey    = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	autostartValueName = "FreeV6"
	// StartupApproved is written by Task Manager's enable/disable toggle; the
	// desktop exe reads it in task_manager_allows_autostart (main.rs).
	startupApprovedKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
)

// desktopExePath is where freev6-desktop.exe lives: FREEV6_ROOT is always the
// desktop exe's own directory (main.rs passes current_exe().parent()).
func desktopExePath(root string) string {
	return filepath.Join(root, "freev6-desktop.exe")
}

// applyAutoStart registers the logon task and rewrites the Task Manager
// marker entry. The value name is the one older builds used for a direct-exe
// entry, so upgrades migrate in place and the two mechanisms can never
// double-launch. Both command lines are passed as single arguments (no
// shell), so quoted exe paths survive install dirs containing spaces.
func applyAutoStart(root string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("autostart is only supported on windows")
	}
	exe := desktopExePath(root)
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("desktop executable not found: %s", exe)
	}
	helper := filepath.Join(root, "freev6-helper.exe")
	if _, err := os.Stat(helper); err != nil {
		return fmt.Errorf("helper executable not found: %s", helper)
	}
	// /F replaces an existing task (also refreshes the path after updates
	// move the install dir). A failure surfaces to the PUT handler so the GUI
	// switch reverts instead of claiming a state that never took effect.
	output, err := exec.Command(
		"schtasks", "/Create", "/F",
		"/TN", autostartTaskName,
		"/TR", fmt.Sprintf(`"%s" --minimized --autostart`, exe),
		"/SC", "ONLOGON",
		"/RL", "HIGHEST",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks create failed: %s", strings.TrimSpace(string(output)))
	}
	regOutput, regErr := exec.Command(
		"reg", "add", autostartRunKey,
		"/v", autostartValueName,
		"/t", "REG_SZ",
		"/d", fmt.Sprintf(`"%s" --autostart`, helper),
		"/f",
	).CombinedOutput()
	if regErr != nil {
		// Compensate for the partial state: without the marker entry there
		// would be nothing for Task Manager to show.
		_ = exec.Command("schtasks", "/Delete", "/TN", autostartTaskName, "/F").Run()
		return fmt.Errorf("reg add failed: %s", strings.TrimSpace(string(regOutput)))
	}
	return nil
}

// removeAutoStart deletes the logon task, the Task Manager marker entry and
// any leftover enable/disable state; missing pieces are no-ops.
func removeAutoStart() error {
	if runtime.GOOS != "windows" {
		return nil
	}
	if autoStartEnabled() {
		output, err := exec.Command(
			"schtasks", "/Delete", "/TN", autostartTaskName, "/F",
		).CombinedOutput()
		if err != nil {
			return fmt.Errorf("schtasks delete failed: %s", strings.TrimSpace(string(output)))
		}
	}
	// reg delete fails when the value is already gone — that is a no-op.
	_ = exec.Command("reg", "delete", autostartRunKey, "/v", autostartValueName, "/f").Run()
	// Drop the stale StartupApproved toggle so a later re-enable starts from
	// the default (enabled) state instead of inheriting an old disable.
	_ = exec.Command("reg", "delete", startupApprovedKey, "/v", autostartValueName, "/f").Run()
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
