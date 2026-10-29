package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// resetSettings 必须把 settings.json 恢复成默认值（所有开关 false、
// mode=rule、campusCidrs=["edu.cn"]）并删除已导入的链式代理文件。
func TestResetSettingsRestoresDefaults(t *testing.T) {
	root := t.TempDir()
	h := &helper{root: root}

	// 写入一份非默认的 settings.json + 一个 chain-proxy.yaml
	custom := settings{
		Mode:        "global",
		CampusCIDRs: []string{"10.0.0.0/8"},
		Blacklist:   []string{"ads.example.com"},
		DevMode:     true,
		AutoStart:   false, // 测试环境无法操作 Task Scheduler，保持 false
		SilentStart: true,
		AutoRunProxy: true,
		HotkeyEnabled: true,
		HotspotShare: true,
		ChainEnabled: true,
		ChainSourceType: "url",
		ChainSourceURL:  "https://example.com/sub.yaml",
	}
	if err := h.saveSettings(custom); err != nil {
		t.Fatal(err)
	}
	chainDir := filepath.Join(root, "config")
	if err := os.MkdirAll(chainDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.chainPath(), []byte("proxies:\n  - name: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.resetSettings(rec, httptest.NewRequest(http.MethodPost, "/api/v1/settings/reset", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK       bool     `json:"ok"`
		Settings settings `json:"settings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatal("ok must be true")
	}
	if resp.Settings.Mode != "rule" || resp.Settings.DevMode || resp.Settings.AutoRunProxy ||
		resp.Settings.SilentStart || resp.Settings.HotkeyEnabled || resp.Settings.HotspotShare ||
		resp.Settings.ChainEnabled {
		t.Fatalf("response settings not defaults: %+v", resp.Settings)
	}
	if len(resp.Settings.CampusCIDRs) != 1 || resp.Settings.CampusCIDRs[0] != "edu.cn" {
		t.Fatalf("campusCIDRs not reset: %v", resp.Settings.CampusCIDRs)
	}

	// 持久化的文件必须也回到默认值
	persisted, err := h.loadSettings()
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	if persisted.Mode != "rule" || persisted.DevMode || persisted.ChainEnabled {
		t.Fatalf("persisted settings not defaults: %+v", persisted)
	}

	// 链式代理文件必须被删除
	if _, err := os.Stat(h.chainPath()); !os.IsNotExist(err) {
		t.Fatalf("chain-proxy.yaml must be removed, stat err = %v", err)
	}
}

// 没有 settings.json 也能重置：直接写默认值，不报错。
func TestResetSettingsFromEmpty(t *testing.T) {
	root := t.TempDir()
	h := &helper{root: root}

	rec := httptest.NewRecorder()
	h.resetSettings(rec, httptest.NewRequest(http.MethodPost, "/api/v1/settings/reset", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(root, "config", "settings.json")); err != nil {
		t.Fatalf("settings.json must exist after reset: %v", err)
	}
}

func TestResetSettingsRejectsNonPOST(t *testing.T) {
	h := &helper{root: t.TempDir()}
	rec := httptest.NewRecorder()
	h.resetSettings(rec, httptest.NewRequest(http.MethodGet, "/api/v1/settings/reset", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
