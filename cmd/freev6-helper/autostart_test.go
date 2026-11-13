package main

import (
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"
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
	// The task must be registered from the hardened XML: IgnoreNew (the /TR
	// default) is what swallowed the logon trigger after a shutdown that
	// killed the still-running instance.
	definition := readRegisteredTask(t)
	for _, want := range []string{
		"<MultipleInstancesPolicy>Parallel</MultipleInstancesPolicy>",
		"<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>",
		"<StartWhenAvailable>true</StartWhenAvailable>",
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
		"<RunLevel>HighestAvailable</RunLevel>",
	} {
		if !strings.Contains(definition, want) {
			t.Fatalf("task definition missing %q (fell back to /TR?):\n%s", want, definition)
		}
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

// The rendered document must stay schema-shaped: schtasks rejects a malformed
// definition and createLogonTask then falls back to /TR, which silently drops
// the whole "still runs after an unclean shutdown" fix. Covers escaping for
// install dirs that contain spaces and ampersands.
func TestAutostartTaskXML(t *testing.T) {
	exe := `C:\Program Files\A&B\freev6-desktop.exe`
	document := autostartTaskXML(exe, `DESKTOP\student`)

	decoder := xml.NewDecoder(strings.NewReader(document))
	// The document is staged as UTF-16 on disk; here the Go string is already
	// decoded, so the declared charset needs no converter — only structure.
	decoder.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	for {
		if _, err := decoder.Token(); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("task XML is not well formed: %v", err)
		}
	}
	for _, want := range []string{
		"<MultipleInstancesPolicy>Parallel</MultipleInstancesPolicy>",
		"<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>",
		"<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>",
		"<StartWhenAvailable>true</StartWhenAvailable>",
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
		"<RunLevel>HighestAvailable</RunLevel>",
		"<LogonType>InteractiveToken</LogonType>",
		"<Command>C:\\Program Files\\A&amp;B\\freev6-desktop.exe</Command>",
		"<Arguments>--minimized --autostart</Arguments>",
	} {
		if !strings.Contains(document, want) {
			t.Fatalf("task XML missing %q:\n%s", want, document)
		}
	}
	// Both the trigger and the principal must be scoped to the owning user;
	// an empty UserId would turn the trigger into "any user who logs on".
	if got := strings.Count(document, `<UserId>DESKTOP\student</UserId>`); got != 2 {
		t.Fatalf("expected trigger and principal to both name the user, got %d in:\n%s", got, document)
	}
	// Settings is a strict sequence in the schema, not a set.
	if strings.Index(document, "<Principals>") > strings.Index(document, "<Settings>") ||
		strings.Index(document, "<Settings>") > strings.Index(document, "<Actions") {
		t.Fatal("task XML sections out of schema order")
	}
}

// schtasks /XML only reads Unicode files: the staging file must be UTF-16LE
// with a BOM no matter what the Go strings are.
func TestWriteTaskXMLIsUnicode(t *testing.T) {
	path, err := writeTaskXML(autostartTaskXML(`C:\freev6\freev6-desktop.exe`, `DESKTOP\student`))
	if err != nil {
		t.Fatalf("writeTaskXML: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged XML: %v", err)
	}
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xFE {
		t.Fatalf("staged XML must be UTF-16LE with a BOM, got %x", data[:min(len(data), 4)])
	}
	text, ok := decodeUTF16LE(data)
	if !ok {
		t.Fatal("staged XML did not decode as UTF-16LE")
	}
	if !strings.Contains(text, "--minimized --autostart") {
		t.Fatalf("round-trip lost the arguments:\n%s", text)
	}
}

// readRegisteredTask returns the task definition Task Scheduler stored, as
// UTF-8 text. The file under System32\Tasks is the authoritative copy; the
// schtasks dump is the fallback for locked-down task folders.
func readRegisteredTask(t *testing.T) string {
	t.Helper()
	path := filepath.Join(os.Getenv("SystemRoot"), "System32", "Tasks", autostartTaskName)
	if data, err := os.ReadFile(path); err == nil {
		if text, ok := decodeUTF16LE(data); ok {
			return text
		}
		return string(data)
	}
	out, err := exec.Command("schtasks", "/Query", "/TN", autostartTaskName, "/XML").Output()
	if err != nil {
		t.Fatalf("read registered task: %v", err)
	}
	if text, ok := decodeUTF16LE(out); ok {
		return text
	}
	return string(out)
}

func decodeUTF16LE(data []byte) (string, bool) {
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xFE {
		return "", false
	}
	units := make([]uint16, 0, (len(data)-2)/2)
	for i := 2; i+1 < len(data); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(data[i:]))
	}
	return string(utf16.Decode(units)), true
}
