package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// 共享移动热点 defaults off: 打开软件绝不能擅自改系统的 ICS 共享状态。
func TestSettingsDefaultHotspotShareOff(t *testing.T) {
	if (settings{}).HotspotShare {
		t.Fatal("hotspotShare must default to false")
	}
	if defaultSettings().HotspotShare {
		t.Fatal("defaultSettings must keep hotspotShare false")
	}
}

// PUT 必须持久化 hotspotShare，GET 原样返回 —— GUI 开关的读写契约。
func TestSettingsHandlerPersistsHotspotShare(t *testing.T) {
	root := t.TempDir()
	h := &helper{root: root}

	put := httptest.NewRequest(http.MethodPut, "/api/v1/settings",
		bytes.NewReader([]byte(`{"mode":"rule","campusCidrs":["edu.cn"],"hotspotShare":true}`)))
	putRec := httptest.NewRecorder()
	h.settings(putRec, put)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", putRec.Code, putRec.Body.String())
	}
	raw, err := os.ReadFile(filepath.Join(root, "config", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"hotspotShare": true`)) {
		t.Fatalf("settings.json must store hotspotShare, got:\n%s", raw)
	}

	get := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	getRec := httptest.NewRecorder()
	h.settings(getRec, get)
	if !bytes.Contains(getRec.Body.Bytes(), []byte(`"hotspotShare":true`)) {
		t.Fatalf("GET must return hotspotShare, got: %s", getRec.Body.String())
	}
}

// 省略 hotspotShare 的 PUT（旧版 GUI / 局部更新）必须保留存储值，
// 否则 bool 零值会悄悄关掉已开启的热点共享。
func TestSettingsPutOmittedHotspotShareKeepsStored(t *testing.T) {
	root := t.TempDir()
	h := &helper{root: root}

	first := httptest.NewRequest(http.MethodPut, "/api/v1/settings",
		bytes.NewReader([]byte(`{"mode":"rule","campusCidrs":["edu.cn"],"hotspotShare":true}`)))
	firstRec := httptest.NewRecorder()
	h.settings(firstRec, first)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first PUT status = %d", firstRec.Code)
	}

	second := httptest.NewRequest(http.MethodPut, "/api/v1/settings",
		bytes.NewReader([]byte(`{"mode":"global","campusCidrs":["edu.cn"]}`)))
	secondRec := httptest.NewRecorder()
	h.settings(secondRec, second)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("second PUT status = %d", secondRec.Code)
	}
	if !bytes.Contains(secondRec.Body.Bytes(), []byte(`"hotspotShare":true`)) {
		t.Fatalf("omitted hotspotShare must keep stored true, got: %s", secondRec.Body.String())
	}
}

// 免流启动的合并逻辑不得触碰 hotspotShare —— start 请求从不携带它。
func TestMergeStartSettingsPreservesHotspotShare(t *testing.T) {
	stored := settings{Mode: "rule", CampusCIDRs: []string{"edu.cn"}, HotspotShare: true}
	got := mergeStartSettings(stored, proxyRequest{Mode: "global", CampusCIDRs: []string{"edu.cn"}})
	if !got.HotspotShare {
		t.Fatal("hotspotShare must survive a proxy start")
	}
}
