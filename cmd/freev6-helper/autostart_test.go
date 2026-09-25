package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Round-trips the real HKCU Run entry: apply → query → assert quoted command
// line → remove. Refuses to run if an entry already exists so a developer's
// real autostart is never clobbered.
func TestAutoStartRoundTrip(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("autostart is windows-only")
	}
	if autoStartEnabled() {
		t.Skip("FreeV6 autostart entry already present; refusing to overwrite")
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
		t.Fatal("Run entry missing after apply")
	}

	output, _ := exec.Command("reg", "query", autostartRunKey, "/v", autostartValueName).CombinedOutput()
	text := string(output)
	if !strings.Contains(text, "--minimized") {
		t.Fatalf("Run entry must launch tray-only (--minimized): %s", text)
	}
	// The path must be quoted or a spaced install dir would break at the space.
	if !strings.Contains(text, `"`+exe+`"`) {
		t.Fatalf("Run entry must contain the quoted exe path, got: %s", text)
	}

	if err := removeAutoStart(); err != nil {
		t.Fatalf("removeAutoStart: %v", err)
	}
	if autoStartEnabled() {
		t.Fatal("Run entry still present after remove")
	}
}

// Removing when nothing exists must stay a silent no-op (idempotent disable).
func TestRemoveAutoStartIdempotent(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("autostart is windows-only")
	}
	if err := removeAutoStart(); err != nil {
		t.Fatalf("remove on absent entry should be nil, got: %v", err)
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
