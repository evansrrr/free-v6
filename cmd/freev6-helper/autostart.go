package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
)

// Autostart is two cooperating pieces:
//
//  1. A Task Scheduler logon task ("FreeV6 Autostart", ONLOGON + HIGHEST)
//     really launches the desktop exe — elevated, so the helper can manage
//     the network; the task's embedded requireAdministrator manifest never
//     raises a UAC prompt at logon, and whether the main window shows is
//     decided solely by the 静默启动 setting at launch time. (A Run
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
// desktop app), which registering a HighestAvailable task requires.
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
	if err := createLogonTask(exe); err != nil {
		return err
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

// createLogonTask registers the ONLOGON task from Task Scheduler's own XML.
//
// `/TR ... /SC ONLOGON /RL HIGHEST` cannot express the settings that decide
// whether the task actually fires on the *next* logon, and its defaults are
// harmful in exactly the reported case — 免流 still running when the machine
// goes down:
//
//   - MultipleInstancesPolicy defaults to IgnoreNew. Shutting down with the
//     app alive (免流 running → 直接关机, not a tray 退出) kills the instance
//     without Task Scheduler recording the stop — Fast Startup makes it worse
//     by hibernating the Scheduler service instead of ending the instance.
//     The stale "instance already running" record then swallows the logon
//     trigger (0x41301, event 322 NEW_INSTANCE_IGNORED) and the app never
//     starts. Parallel starts a new instance regardless of the stale record;
//     the desktop shell's single-instance mutex already makes a real
//     duplicate a silent no-op.
//   - DisallowStartIfOnBatteries / StopIfGoingOnBatteries default to true, so
//     a laptop task is skipped at logon while unplugged.
//   - StartWhenAvailable defaults to false, which drops a logon start that
//     was delayed instead of running it late.
//   - ExecutionTimeLimit defaults to 72h — wrong for a tray-resident app that
//     is expected to live until the next shutdown.
//
// If the XML form is rejected (unresolvable user name, unusual policy), fall
// back to the /TR form so the switch still works and only the hardening is
// lost.
func createLogonTask(exe string) error {
	if user := currentWindowsUser(); user != "" {
		if path, writeErr := writeTaskXML(autostartTaskXML(exe, user)); writeErr != nil {
			fmt.Fprintf(os.Stderr, "helper: cannot stage autostart task XML: %v\n", writeErr)
		} else {
			output, createErr := exec.Command(
				"schtasks", "/Create", "/F", "/TN", autostartTaskName, "/XML", path,
			).CombinedOutput()
			_ = os.Remove(path)
			if createErr == nil {
				return nil
			}
			fmt.Fprintf(os.Stderr, "helper: autostart task XML rejected (%s); falling back to /TR\n",
				strings.TrimSpace(string(output)))
		}
	}
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
	return nil
}

// autostartTaskXML renders the task definition. The principal is the current
// interactive user (LogonType InteractiveToken), so the ONLOGON trigger fires
// only for the account that owns the install and the task can use the user's
// own token — no stored password, no UAC prompt.
func autostartTaskXML(exe, user string) string {
	escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	// Element order follows the Task Scheduler schema (Settings is a strict
	// sequence, not a set) and only the values that differ from the defaults
	// are spelled out — a rejected document would silently fall back to /TR.
	// The task URI is left out on purpose: /TN already fixes the path, and a
	// mismatching URI is one more way for schtasks to reject the document.
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Launches the freev6 shell at logon (elevated, no UAC prompt).</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>%s</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>Parallel</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>--minimized --autostart</Arguments>
    </Exec>
  </Actions>
</Task>
`, escape.Replace(user), escape.Replace(user), escape.Replace(exe))
}

// writeTaskXML stages the definition in %TEMP%: schtasks /XML only accepts
// Unicode input, so the file is UTF-16LE with a BOM regardless of the API's
// UTF-8 strings. The caller removes it.
func writeTaskXML(document string) (string, error) {
	file, err := os.CreateTemp("", "freev6-autostart-*.xml")
	if err != nil {
		return "", err
	}
	units := utf16.Encode([]rune(document))
	data := make([]byte, 2+len(units)*2)
	data[0], data[1] = 0xFF, 0xFE
	for i, unit := range units {
		binary.LittleEndian.PutUint16(data[2+i*2:], unit)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}

// currentWindowsUser returns DOMAIN\user for the elevated helper. Empty when
// the environment does not carry it (service-like contexts), which makes the
// caller use the /TR fallback.
func currentWindowsUser() string {
	user := os.Getenv("USERNAME")
	domain := os.Getenv("USERDOMAIN")
	switch {
	case user == "":
		return ""
	case domain == "":
		return user
	default:
		return domain + `\` + user
	}
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
