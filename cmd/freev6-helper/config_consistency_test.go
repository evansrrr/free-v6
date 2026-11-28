package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// G1：settings.json 损坏时 reset 必须能成功。
//
// 那条 500 的文案就是"请在设置中重置"，如果 reset 自己也走 updateSettings，
// 它会以同一句错误拒绝覆盖坏文件——恢复入口闭环，用户只能手动删文件。
func TestResetRecoversFromCorruptSettings(t *testing.T) {
	root := t.TempDir()
	h := &helper{root: root}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "settings.json"), []byte("{ broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.resetSettings(rec, httptest.NewRequest(http.MethodPost, "/api/v1/settings/reset", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reset must recover a corrupt settings.json, got %d: %s", rec.Code, rec.Body.String())
	}
	loaded, err := h.loadSettings()
	if err != nil {
		t.Fatalf("settings must parse after reset: %v", err)
	}
	if loaded.Mode != "rule" || loaded.ChainEnabled || loaded.CloudEnabled {
		t.Fatalf("reset must leave defaults, got %+v", loaded)
	}
}

// C2：删内容文件失败不能让 reset 变成 500。
//
// 设置已经写成默认值，残留文件不再被任何人引用（开关已关）；返回 500 只会让
// 用户以为没重置。用一个非空目录模拟"删不掉的文件"。
func TestResetStaysSuccessfulWhenContentCannotBeRemoved(t *testing.T) {
	root := t.TempDir()
	h := &helper{root: root}
	blocked := filepath.Join(root, "config", "chain-proxy.yaml")
	if err := os.MkdirAll(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	custom := defaultSettings()
	custom.ChainEnabled = true
	if err := h.saveSettings(custom); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.resetSettings(rec, httptest.NewRequest(http.MethodPost, "/api/v1/settings/reset", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reset must stay successful, got %d: %s", rec.Code, rec.Body.String())
	}
	loaded, err := h.loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ChainEnabled {
		t.Fatal("the switch must be off even when the file cannot be removed")
	}
	if _, statErr := os.Stat(blocked); statErr != nil {
		t.Fatal("the fixture should still exist; the test is not exercising the failure path")
	}
}

// G2：备用配置开着时导入链式代理必须被拒绝，且不留下内容文件（否则下次启动
// 会带着一个"已启用但互相冲突"的组合起来）。
func TestChainImportRejectsEnabledCloudProfile(t *testing.T) {
	h := &helper{root: t.TempDir()}
	base := defaultSettings()
	base.CloudEnabled = true
	if err := h.saveSettings(base); err != nil {
		t.Fatal(err)
	}

	rec := chainRequest(t, h, http.MethodPut, `{"content":"`+jsonEscape(chainTestYAML)+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(h.chainPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a rejected import must not write the content file")
	}
}

// 反向同样要被拒绝。
func TestCloudImportRejectsEnabledChain(t *testing.T) {
	h := &helper{root: t.TempDir()}
	base := defaultSettings()
	base.ChainEnabled = true
	if err := h.saveSettings(base); err != nil {
		t.Fatal(err)
	}
	writeNoticeCache(t, h, noticeCache{FetchedAt: time.Now(), CloudProfile: boolPtr(true)})

	rec := cloudRequest(t, h, http.MethodPut, `{"content":"`+jsonEscape(cloudTestYAML)+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(h.cloudProfilePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a rejected import must not write the content file")
	}
}

// G2：并发导入。没有 configMu 时两个 handler 都会读到"对方没开"，最后两个开关
// 同时为 true——启动时才以 400 暴露。
func TestConcurrentImportsKeepOneOutlet(t *testing.T) {
	h := &helper{root: t.TempDir()}
	writeNoticeCache(t, h, noticeCache{FetchedAt: time.Now(), CloudProfile: boolPtr(true)})

	var wg sync.WaitGroup
	var chainCode, cloudCode int
	wg.Add(2)
	go func() {
		defer wg.Done()
		chainCode = chainRequest(t, h, http.MethodPut, `{"content":"`+jsonEscape(chainTestYAML)+`"}`).Code
	}()
	go func() {
		defer wg.Done()
		cloudCode = cloudRequest(t, h, http.MethodPut, `{"content":"`+jsonEscape(cloudTestYAML)+`"}`).Code
	}()
	wg.Wait()

	loaded, err := h.loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ChainEnabled && loaded.CloudEnabled {
		t.Fatalf("both outlets ended up enabled (chain=%d cloud=%d)", chainCode, cloudCode)
	}
	if chainCode == http.StatusOK && cloudCode == http.StatusOK {
		t.Fatalf("both imports reported success (chain=%d cloud=%d)", chainCode, cloudCode)
	}
	if chainCode != http.StatusOK && cloudCode != http.StatusOK {
		t.Fatalf("exactly one import should win, got chain=%d cloud=%d", chainCode, cloudCode)
	}
}

// F1：公告缓存过期只影响展示，不改变"最后一次成功结论"。
func TestCloudGateKeepsConclusionAfterCacheExpiry(t *testing.T) {
	stale := time.Now().Add(-30 * 24 * time.Hour)

	open := &helper{root: t.TempDir()}
	writeNoticeCache(t, open, noticeCache{FetchedAt: stale, CloudProfile: boolPtr(true)})
	if !open.cloudProfileUnlocked() {
		t.Fatal("a stale but positive conclusion must stay unlocked")
	}

	closed := &helper{root: t.TempDir()}
	writeNoticeCache(t, closed, noticeCache{FetchedAt: stale, CloudProfile: boolPtr(false)})
	if closed.cloudProfileUnlocked() {
		t.Fatal("a stale negative conclusion must stay locked")
	}

	// 从没成功拉过公告：仍然 fail-closed。
	never := &helper{root: t.TempDir()}
	if never.cloudProfileUnlocked() {
		t.Fatal("no cache at all must stay locked")
	}
}

// 展示语义不变：过期的缓存不能继续出现在公告栏里。
func TestExpiredCacheStillHiddenFromNotices(t *testing.T) {
	h := &helper{root: t.TempDir()}
	writeNoticeCache(t, h, noticeCache{
		FetchedAt:    time.Now().Add(-8 * 24 * time.Hour),
		CloudProfile: boolPtr(true),
		Items:        []noticeItem{{ID: "old", Title: "旧公告"}},
	})
	if _, ok := h.loadNoticeCache(time.Now()); ok {
		t.Fatal("the notice list must still expire after 7 days")
	}
}

func writeNoticeCache(t *testing.T, h *helper, cache noticeCache) {
	t.Helper()
	path := h.noticeCachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
