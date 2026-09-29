package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// A proxy start must not drop settings fields the start request never carries.
// Regression: startProxy saved settings{Mode, CampusCIDRs} — a partial struct
// whose zero values wiped blacklist (null) and devMode (false) on every start,
// so the developer-mode switch reverted after a page refresh.
func TestMergeStartSettingsPreservesUnsentFields(t *testing.T) {
	stored := settings{
		Mode:        "rule",
		CampusCIDRs: []string{"edu.cn"},
		Blacklist:   []string{"ads.example.com"},
		DevMode:     true,
	}
	got := mergeStartSettings(stored, proxyRequest{
		Mode:        "global",
		CampusCIDRs: []string{"202.204.0.0/12", "edu.cn"},
		DevMode:     true,
	})
	if len(got.Blacklist) != 1 || got.Blacklist[0] != "ads.example.com" {
		t.Fatalf("blacklist must survive a proxy start, got %v", got.Blacklist)
	}
	if !got.DevMode {
		t.Fatal("devMode must be persisted from the start request")
	}
	if got.Mode != "global" || len(got.CampusCIDRs) != 2 {
		t.Fatalf("start request fields must be applied, got %+v", got)
	}
}

// 静默启动 defaults off: normal manual launches must show the window.
func TestSettingsDefaultSilentStartOff(t *testing.T) {
	if (settings{}).SilentStart {
		t.Fatal("silentStart must default to false")
	}
	if defaultSettings().SilentStart {
		t.Fatal("defaultSettings must keep silentStart false")
	}
}

// 自动运行免流 defaults off: opening the app must not start 免流 unasked.
func TestSettingsDefaultAutoRunProxyOff(t *testing.T) {
	if (settings{}).AutoRunProxy {
		t.Fatal("autoRunProxy must default to false")
	}
	if defaultSettings().AutoRunProxy {
		t.Fatal("defaultSettings must keep autoRunProxy false")
	}
}

// 快捷键 defaults off: the app must not grab F6 unasked.
func TestSettingsDefaultHotkeyEnabledOff(t *testing.T) {
	if (settings{}).HotkeyEnabled {
		t.Fatal("hotkeyEnabled must default to false")
	}
	if defaultSettings().HotkeyEnabled {
		t.Fatal("defaultSettings must keep hotkeyEnabled false")
	}
}

// PUT must persist hotkeyEnabled — main.rs reads it from settings.json at
// startup to register F6 before the webview exists.
func TestSettingsHandlerPersistsHotkeyEnabled(t *testing.T) {
	root := t.TempDir()
	h := &helper{root: root}

	put := httptest.NewRequest(http.MethodPut, "/api/v1/settings",
		bytes.NewReader([]byte(`{"mode":"rule","campusCidrs":["edu.cn"],"hotkeyEnabled":true}`)))
	putRec := httptest.NewRecorder()
	h.settings(putRec, put)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", putRec.Code, putRec.Body.String())
	}
	raw, err := os.ReadFile(filepath.Join(root, "config", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"hotkeyEnabled": true`)) {
		t.Fatalf("settings.json must store hotkeyEnabled, got:\n%s", raw)
	}

	// A PUT that omits the key (older GUI) must keep the stored value.
	second := httptest.NewRequest(http.MethodPut, "/api/v1/settings",
		bytes.NewReader([]byte(`{"mode":"global","campusCidrs":["edu.cn"]}`)))
	secondRec := httptest.NewRecorder()
	h.settings(secondRec, second)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("second PUT status = %d, body = %s", secondRec.Code, secondRec.Body.String())
	}
	if !bytes.Contains(secondRec.Body.Bytes(), []byte(`"hotkeyEnabled":true`)) {
		t.Fatalf("omitted hotkeyEnabled must keep stored true, got: %s", secondRec.Body.String())
	}
}

// A proxy start must not wipe the autoRun switch: mergeStartSettings only
// overlays mode/cidrs/devMode onto the stored settings.
func TestMergeStartSettingsPreservesAutoRunProxy(t *testing.T) {
	stored := settings{
		Mode:         "rule",
		CampusCIDRs:  []string{"edu.cn"},
		AutoRunProxy: true,
	}
	got := mergeStartSettings(stored, proxyRequest{Mode: "global", CampusCIDRs: []string{"edu.cn"}})
	if !got.AutoRunProxy {
		t.Fatal("autoRunProxy must survive a proxy start")
	}
}

// PUT must persist autoRunProxy and GET must hand it back — the GUI switch
// reverts itself whenever the helper drops the field on save.
func TestSettingsHandlerPersistsAutoRunProxy(t *testing.T) {
	root := t.TempDir()
	h := &helper{root: root}

	put := httptest.NewRequest(http.MethodPut, "/api/v1/settings",
		bytes.NewReader([]byte(`{"mode":"rule","campusCidrs":["edu.cn"],"autoRunProxy":true}`)))
	putRec := httptest.NewRecorder()
	h.settings(putRec, put)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", putRec.Code, putRec.Body.String())
	}

	// The file must contain the flag for main.rs / other readers too.
	raw, err := os.ReadFile(filepath.Join(root, "config", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"autoRunProxy": true`)) {
		t.Fatalf("settings.json must store autoRunProxy, got:\n%s", raw)
	}

	get := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	getRec := httptest.NewRecorder()
	h.settings(getRec, get)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", getRec.Code)
	}
	if !bytes.Contains(getRec.Body.Bytes(), []byte(`"autoRunProxy":true`)) {
		t.Fatalf("GET must return autoRunProxy, got: %s", getRec.Body.String())
	}
}

// A PUT that omits autoRunProxy (older GUI, partial update) must keep the
// stored value — a bool zero value would silently disable 自动运行免流.
func TestSettingsPutOmittedAutoRunProxyKeepsStored(t *testing.T) {
	root := t.TempDir()
	h := &helper{root: root}

	first := httptest.NewRequest(http.MethodPut, "/api/v1/settings",
		bytes.NewReader([]byte(`{"mode":"rule","campusCidrs":["edu.cn"],"autoRunProxy":true}`)))
	firstRec := httptest.NewRecorder()
	h.settings(firstRec, first)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first PUT status = %d, body = %s", firstRec.Code, firstRec.Body.String())
	}

	second := httptest.NewRequest(http.MethodPut, "/api/v1/settings",
		bytes.NewReader([]byte(`{"mode":"global","campusCidrs":["edu.cn"]}`)))
	secondRec := httptest.NewRecorder()
	h.settings(secondRec, second)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("second PUT status = %d, body = %s", secondRec.Code, secondRec.Body.String())
	}
	if !bytes.Contains(secondRec.Body.Bytes(), []byte(`"autoRunProxy":true`)) {
		t.Fatalf("omitted autoRunProxy must keep stored true, got: %s", secondRec.Body.String())
	}

	// Explicit false still turns it off.
	third := httptest.NewRequest(http.MethodPut, "/api/v1/settings",
		bytes.NewReader([]byte(`{"mode":"rule","campusCidrs":["edu.cn"],"autoRunProxy":false}`)))
	thirdRec := httptest.NewRecorder()
	h.settings(thirdRec, third)
	if !bytes.Contains(thirdRec.Body.Bytes(), []byte(`"autoRunProxy":false`)) {
		t.Fatalf("explicit false must persist, got: %s", thirdRec.Body.String())
	}
}
