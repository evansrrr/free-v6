package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Per-user autostart: an HKCU Run entry launches the desktop exe with
// --minimized, so a login start shows only the tray icon (main.rs skips the
// window.show() for that flag). HKCU needs no admin rights.
const (
	autostartRunKey    = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	autostartValueName = "FreeV6"
)

// desktopExePath is where freev6-desktop.exe lives: FREEV6_ROOT is always the
// desktop exe's own directory (main.rs passes current_exe().parent()).
func desktopExePath(root string) string {
	return filepath.Join(root, "freev6-desktop.exe")
}

// applyAutoStart writes the Run entry. The value data is a full command line;
// Go's exec escaping produces the documented reg.exe form "\"exe" --flag".
func applyAutoStart(root string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("autostart is only supported on windows")
	}
	exe := desktopExePath(root)
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("desktop executable not found: %s", exe)
	}
	data := fmt.Sprintf(`"%s" --minimized`, exe)
	output, err := exec.Command(
		"reg", "add", autostartRunKey,
		"/v", autostartValueName,
		"/t", "REG_SZ",
		"/d", data,
		"/f",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("reg add failed: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

// removeAutoStart deletes the Run entry; removing an absent entry is a no-op.
func removeAutoStart() error {
	if runtime.GOOS != "windows" {
		return nil
	}
	if !autoStartEnabled() {
		return nil
	}
	output, err := exec.Command(
		"reg", "delete", autostartRunKey,
		"/v", autostartValueName,
		"/f",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("reg delete failed: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

// autoStartEnabled reports whether the Run entry exists. reg query exits
// non-zero when the value is missing — locale-independent, no message parsing.
func autoStartEnabled() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	return exec.Command("reg", "query", autostartRunKey, "/v", autostartValueName).Run() == nil
}
