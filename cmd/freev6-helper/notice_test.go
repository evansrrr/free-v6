package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 固定「现在」，让过期/未过期的断言与真实时钟无关。
var noticeTestNow = time.Date(2026, 10, 29, 21, 0, 0, 0, time.UTC)

func TestParseNoticePayload(t *testing.T) {
	body := `{
	  "_comment": "ignored",
	  "updated": "2026-10-29T20:00:00+08:00",
	  "items": [
	    {"id":"a","title":"维护通知","body":"第一行\n第二行","published":"2026-10-29T19:00:00+08:00"},
	    {"id":"b","title":"已过期","published":"2026-10-28T19:00:00+08:00","expires":"2026-10-29T08:00:00+08:00"},
	    {"id":"","title":"没有 id","published":"2026-10-29T19:00:00+08:00"},
	    {"id":"c","title":"","published":"2026-10-29T19:00:00+08:00"},
	    {"id":"d","title":"没有时间","published":""},
	    {"id":"e","title":"时间坏了","published":"昨天"},
	    {"id":"f","title":"级别未知","level":"WHATEVER","published":"2026-10-29T19:00:00+08:00"},
	    {"id":"g","title":"危险链接","published":"2026-10-29T19:00:00+08:00","link":"javascript:alert(1)","linkText":"点我"},
	    {"id":"h","title":"正常链接","published":"2026-10-29T19:00:00+08:00","link":"https://freev6.cc.cd/guide/introduction"},
	    {"id":"a","title":"重复 id","published":"2026-10-29T19:00:00+08:00"}
	  ]
	}`
	payload, err := parseNoticePayload([]byte(body), noticeTestNow)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if payload.Updated != "2026-10-29T20:00:00+08:00" {
		t.Fatalf("updated = %q", payload.Updated)
	}
	var ids []string
	for _, item := range payload.Items {
		ids = append(ids, item.ID)
	}
	// 过期、缺字段、时间非法的条目都不该出现；重复 id 只保留第一条。
	want := []string{"a", "f", "g", "h"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	if payload.Items[0].Body != "第一行\n第二行" {
		t.Fatalf("body newlines must survive: %q", payload.Items[0].Body)
	}
	if payload.Items[1].Level != "info" {
		t.Fatalf("unknown level must fall back to info, got %q", payload.Items[1].Level)
	}
	if payload.Items[2].Link != "" || payload.Items[2].LinkText != "" {
		t.Fatalf("non-https link must be dropped: %+v", payload.Items[2])
	}
	if payload.Items[3].Link != "https://freev6.cc.cd/guide/introduction" {
		t.Fatalf("https link must survive: %+v", payload.Items[3])
	}
	if payload.Items[3].LinkText != "查看详情" {
		t.Fatalf("empty linkText must default, got %q", payload.Items[3].LinkText)
	}
}

// 维护者手滑写成顶层数组时也不能把公告板清空。
func TestParseNoticePayloadBareArray(t *testing.T) {
	body := `[{"id":"x","title":"数组写法","published":"2026-10-29T19:00:00+08:00"}]`
	payload, err := parseNoticePayload([]byte(body), noticeTestNow)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].ID != "x" {
		t.Fatalf("bare array not accepted: %+v", payload.Items)
	}
}

func TestParseNoticePayloadRejectsGarbage(t *testing.T) {
	for _, body := range []string{"", "not json", "<html>404</html>", `{"items":"nope"}`} {
		if _, err := parseNoticePayload([]byte(body), noticeTestNow); err == nil {
			t.Errorf("expected error for %q", body)
		}
	}
}

func TestParseNoticePayloadCapsAndTruncates(t *testing.T) {
	item := func(i int) map[string]any {
		return map[string]any{
			"id":        "id-" + strings.Repeat("x", i%3) + string(rune('a'+i%26)),
			"title":     strings.Repeat("标", noticeMaxTitle+50),
			"body":      strings.Repeat("文", noticeMaxBody+50),
			"published": "2026-10-29T19:00:00+08:00",
		}
	}
	items := make([]map[string]any, 0, noticeMaxItems+5)
	for i := 0; i < noticeMaxItems+5; i++ {
		entry := item(i)
		entry["id"] = "id-" + strings.Repeat("0", i%2) + string(rune('a'+i))
		items = append(items, entry)
	}
	data, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := parseNoticePayload(data, noticeTestNow)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(payload.Items) != noticeMaxItems {
		t.Fatalf("items = %d, want cap %d", len(payload.Items), noticeMaxItems)
	}
	first := payload.Items[0]
	if got := len([]rune(first.Title)); got != noticeMaxTitle+1 { // +1 = 省略号
		t.Fatalf("title runes = %d, want %d", got, noticeMaxTitle+1)
	}
	if got := len([]rune(first.Body)); got != noticeMaxBody+1 {
		t.Fatalf("body runes = %d, want %d", got, noticeMaxBody+1)
	}
}

// 拉取成功 → 200 + notices；随后线路全挂 → 回退上一次成功的缓存并标记 stale。
func TestNoticeHandlerFallsBackToCache(t *testing.T) {
	restore := withNoticeTestURLs(t)
	defer restore()

	payload := []byte(`{"updated":"2026-10-29T20:00:00+08:00","items":[
	  {"id":"live","title":"在线公告","body":"hi","level":"warning","published":"2026-10-29T19:00:00+08:00"}
	]}`)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.Contains(request.URL.RawQuery, "ts=") {
			t.Errorf("cache-buster missing: %s", request.URL.RawQuery)
		}
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	noticeURLs = func() []string { return []string{server.URL + "/notice.json"} }

	h := &helper{root: t.TempDir()}
	first := decodeNoticeResponse(t, h)
	if first.Stale || len(first.Notices) != 1 || first.Notices[0].ID != "live" {
		t.Fatalf("unexpected first response: %+v", first)
	}
	if _, err := os.Stat(filepath.Join(h.root, "state", "notice-cache.json")); err != nil {
		t.Fatalf("cache must be written on success: %v", err)
	}

	// 线路全挂：回退缓存，stale 必须为 true
	dead := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	noticeURLs = func() []string { return []string{deadURL + "/notice.json"} }

	second := decodeNoticeResponse(t, h)
	if !second.Stale {
		t.Fatalf("cache fallback must flag stale: %+v", second)
	}
	if len(second.Notices) != 1 || second.Notices[0].ID != "live" {
		t.Fatalf("cached notices lost: %+v", second)
	}
	if second.Error == "" {
		t.Fatal("fallback should carry the underlying error text")
	}

	// 无缓存 + 全挂：仍是 200 + 空列表，绝不报错阻塞启动
	empty := decodeNoticeResponse(t, &helper{root: t.TempDir()})
	if empty.Stale || len(empty.Notices) != 0 || empty.Error == "" {
		t.Fatalf("unexpected offline response: %+v", empty)
	}
}

func TestNoticeHandlerRejectsNonGET(t *testing.T) {
	h := &helper{root: t.TempDir()}
	rec := httptest.NewRecorder()
	h.notice(rec, httptest.NewRequest(http.MethodPost, "/api/v1/notice", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// 缓存过旧或条目已过期时不得再展示：宁可空着。
func TestLoadNoticeCacheExpiry(t *testing.T) {
	h := &helper{root: t.TempDir()}
	now := noticeTestNow
	write := func(cache noticeCache) {
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

	write(noticeCache{FetchedAt: now.Add(-8 * 24 * time.Hour), Items: []noticeItem{{ID: "old", Title: "旧缓存"}}})
	if _, ok := h.loadNoticeCache(now); ok {
		t.Fatal("cache older than 7 days must be ignored")
	}

	write(noticeCache{
		FetchedAt: now.Add(-time.Hour),
		Items: []noticeItem{
			{ID: "gone", Title: "过期", Expires: now.Add(-time.Minute).Format(time.RFC3339)},
			{ID: "alive", Title: "有效"},
		},
	})
	cache, ok := h.loadNoticeCache(now)
	if !ok {
		t.Fatal("fresh cache must be used")
	}
	if len(cache.Items) != 1 || cache.Items[0].ID != "alive" {
		t.Fatalf("expired cached items must be filtered: %+v", cache.Items)
	}

	if err := os.WriteFile(h.noticeCachePath(), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.loadNoticeCache(now); ok {
		t.Fatal("corrupt cache must be ignored")
	}
}

type noticeResponse struct {
	OK        bool         `json:"ok"`
	Notices   []noticeItem `json:"notices"`
	Updated   string       `json:"updated"`
	FetchedAt string       `json:"fetchedAt"`
	Stale     bool         `json:"stale"`
	Error     string       `json:"error"`
}

func decodeNoticeResponse(t *testing.T, h *helper) noticeResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	h.notice(rec, httptest.NewRequest(http.MethodGet, "/api/v1/notice", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp noticeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if !resp.OK {
		t.Fatalf("ok must always be true: %s", rec.Body.String())
	}
	return resp
}

// withNoticeTestURLs 让测试能用本机 http 服务；清理时恢复原状。
func withNoticeTestURLs(t *testing.T) func() {
	t.Helper()
	origURLs := noticeURLs
	origHosts := append([]string(nil), updateAllowedHosts...)
	updateAllowedHosts = append(updateAllowedHosts, "127.0.0.1")
	return func() {
		noticeURLs = origURLs
		updateAllowedHosts = origHosts
	}
}
