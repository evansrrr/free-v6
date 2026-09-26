package main

import "testing"

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
