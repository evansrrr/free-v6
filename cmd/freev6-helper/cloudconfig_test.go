package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const cloudTestYAML = `port: 7890
rules:
  - MATCH,DIRECT
proxies:
  - name: campus-1
    type: ss
    server: n1.example.edu.cn
    port: 8388
    cipher: aes-128-gcm
    password: pw
`

func cloudRequest(t *testing.T, h *helper, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/api/v1/cloud-config", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	h.cloudConfig(recorder, request)
	return recorder
}

// seedNoticeGate 写入一份「本机公告缓存」，模拟启动时检查过 notice.json。
// nil 表示公告里没有 cloudProfile 字段。
func seedNoticeGate(t *testing.T, h *helper, allowed *bool) {
	t.Helper()
	stateDir := filepath.Join(h.root, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cache := map[string]any{"fetchedAt": time.Now().Format(time.RFC3339)}
	if allowed != nil {
		cache["cloudProfile"] = *allowed
	}
	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "notice-cache.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func boolPtr(value bool) *bool { return &value }

func TestCloudImportFromContent(t *testing.T) {
	h := &helper{root: t.TempDir()}
	seedNoticeGate(t, h, boolPtr(true))
	put := cloudRequest(t, h, http.MethodPut, `{"content":`+"\""+jsonEscape(cloudTestYAML)+"\""+`}`)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", put.Code, put.Body.String())
	}
	var result struct {
		Count   int      `json:"count"`
		Names   []string `json:"names"`
		Ignored []string `json:"ignored"`
	}
	if err := json.Unmarshal(put.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || result.Names[0] != "campus-1" {
		t.Fatalf("unexpected import result: %+v", result)
	}
	if strings.Join(result.Ignored, ",") != "port,rules" {
		t.Fatalf("ignored = %v, want [port rules]", result.Ignored)
	}
	raw, err := os.ReadFile(filepath.Join(h.root, "config", "cloud-profile.yaml"))
	if err != nil {
		t.Fatalf("snapshot must be persisted: %v", err)
	}
	if string(raw) != cloudTestYAML {
		t.Fatal("persisted snapshot differs from imported content")
	}
	// 导入成功即启用：否则学生还要多点一次开关，且容易以为没生效。
	stored, err := h.loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !stored.CloudEnabled {
		t.Fatal("cloudEnabled should be true after import")
	}
	// 清除后必须同时关掉开关，否则下次启动会因"启用但内容缺失"报错。
	if del := cloudRequest(t, h, http.MethodDelete, ""); del.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d", del.Code)
	}
	stored, _ = h.loadSettings()
	if stored.CloudEnabled {
		t.Fatal("cloudEnabled should be false after clear")
	}
}

// 不带 url 也不带门户地址：必须明确报错，不能静默使用某个内置地址。
func TestCloudImportRequiresEntry(t *testing.T) {
	h := &helper{root: t.TempDir()}
	seedNoticeGate(t, h, boolPtr(true))
	put := cloudRequest(t, h, http.MethodPut, `{}`)
	if put.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", put.Code)
	}
	if !strings.Contains(put.Body.String(), "登录页") {
		t.Fatalf("body = %s", put.Body.String())
	}
}

// 门户地址非法时必须在发请求之前失败（不产生无意义的 DNS 查询）。
func TestCloudImportRejectsBadPortalHost(t *testing.T) {
	h := &helper{root: t.TempDir()}
	seedNoticeGate(t, h, boolPtr(true))
	put := cloudRequest(t, h, http.MethodPut, `{"portalHost":"不是地址"}`)
	if put.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", put.Code, put.Body.String())
	}
}

// 服务端门禁（403）要映射成 campus-only，让前端说人话而不是甩 HTTP 状态码。
func TestFetchCloudProfileMapsGateToCampusOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	h := &helper{root: t.TempDir()}
	_, err := h.fetchCloudProfile(server.URL)
	if err == nil {
		t.Fatal("expected gate error")
	}
	if cloudErrorCode(err) != "campus-only" {
		t.Fatalf("code = %q, err = %v", cloudErrorCode(err), err)
	}
	if !strings.Contains(err.Error(), "校园网") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchCloudProfileReadsBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(cloudTestYAML))
	}))
	defer server.Close()
	h := &helper{root: t.TempDir()}
	data, err := h.fetchCloudProfile(server.URL)
	if err != nil {
		t.Fatalf("fetchCloudProfile: %v", err)
	}
	if string(data) != cloudTestYAML {
		t.Fatal("body mismatch")
	}
}

func TestMaskCloudEntry(t *testing.T) {
	masked := maskCloudEntry("https://0f6d31cf1f09ea10.freev6.cc.cd/p")
	if masked != "0f6d…freev6.cc.cd/p" {
		t.Fatalf("masked = %q", masked)
	}
	if strings.Contains(masked, "31cf1f09ea10") {
		t.Fatal("mask must not leak the full slug")
	}
}

// 公告总开关：显式 true 才开放，缺失 / false / 拿不到公告 一律锁定。
func TestCloudProfileAllowedParsing(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		payload string
		want    bool
	}{
		{"enabled", `{"cloudProfile":true,"items":[]}`, true},
		{"disabled", `{"cloudProfile":false,"items":[]}`, false},
		{"absent", `{"items":[]}`, false},
		{"empty doc", `{}`, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			payload, err := parseNoticePayload([]byte(testCase.payload), now)
			if err != nil {
				t.Fatalf("parseNoticePayload: %v", err)
			}
			if got := cloudProfileAllowed(payload.CloudProfile); got != testCase.want {
				t.Fatalf("cloudProfileAllowed = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestCloudImportBlockedWithoutNotice(t *testing.T) {
	h := &helper{root: t.TempDir()}
	// 没有任何公告缓存：不知道维护者是否开放 → 锁定
	put := cloudRequest(t, h, http.MethodPut, `{"content":`+"\""+jsonEscape(cloudTestYAML)+"\""+`}`)
	if put.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", put.Code, put.Body.String())
	}
	if !strings.Contains(put.Body.String(), "cloud-locked") {
		t.Fatalf("body = %s", put.Body.String())
	}
	if _, err := os.Stat(h.cloudProfilePath()); err == nil {
		t.Fatal("locked import must not write a snapshot")
	}
}

func TestCloudImportBlockedWhenNoticeSaysFalse(t *testing.T) {
	h := &helper{root: t.TempDir()}
	seedNoticeGate(t, h, boolPtr(false))
	put := cloudRequest(t, h, http.MethodPut, `{"content":`+"\""+jsonEscape(cloudTestYAML)+"\""+`}`)
	if put.Code != http.StatusConflict || !strings.Contains(put.Body.String(), "cloud-locked") {
		t.Fatalf("status = %d body = %s, want 409 cloud-locked", put.Code, put.Body.String())
	}
}

// 已启用备用配置但公告关闭时，启动必须被拦在端口/权限检查之前。
func TestStartProxyBlockedWhenCloudLocked(t *testing.T) {
	h := &helper{root: t.TempDir()}
	stored := defaultSettings()
	stored.CloudEnabled = true
	if err := h.saveSettings(stored); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(h.cloudProfilePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.cloudProfilePath(), []byte(cloudTestYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	runStart := func() *httptest.ResponseRecorder {
		body := `{"mode":"rule","campusCidrs":["edu.cn"],"devMode":false}`
		request := httptest.NewRequest(http.MethodPost, "/api/v1/proxy/start", strings.NewReader(body))
		recorder := httptest.NewRecorder()
		h.startProxy(recorder, request)
		return recorder
	}

	locked := runStart()
	if locked.Code != http.StatusBadRequest || !strings.Contains(locked.Body.String(), "cloud-locked") {
		t.Fatalf("locked start = %d %s, want 400 cloud-locked", locked.Code, locked.Body.String())
	}

	// 解锁后应越过这道闸门（后面可能因端口/权限失败，但不再是 cloud-locked）
	seedNoticeGate(t, h, boolPtr(true))
	unlocked := runStart()
	if strings.Contains(unlocked.Body.String(), "cloud-locked") {
		t.Fatalf("unlocked start still locked: %s", unlocked.Body.String())
	}
}

// 失败分类与"是否值得重试"是弹窗文案与重试策略的唯一依据，单测锁住。
func TestFetchFailureClassification(t *testing.T) {
	refused := errors.New("dial tcp 1.2.3.4:443: connect: connection refused")
	reset := errors.New("read tcp 1.2.3.4:443: connection reset by peer")
	dns := &net.DNSError{Err: "no such host", Name: "x.freev6.cc.cd"}

	cases := []struct {
		name   string
		err    error
		reason string
		retry  bool
	}{
		{"deadline", context.DeadlineExceeded, "请求超时，请稍后重试", true},
		{"dns", dns, "域名解析失败，请检查网络或 DNS", false},
		{"refused", refused, "连接被拒绝", false},
		{"reset", reset, "网络请求失败，请稍后重试", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := describeFetchFailure(testCase.err); got != testCase.reason {
				t.Fatalf("reason = %q, want %q", got, testCase.reason)
			}
			if got := isRetryableFetch(testCase.err); got != testCase.retry {
				t.Fatalf("retryable = %v, want %v", got, testCase.retry)
			}
		})
	}
}

// 失败日志落 state/cloud-config.log：弹窗不给地址，排障要能查。
func TestLogCloudFetchWritesFile(t *testing.T) {
	h := &helper{root: t.TempDir()}
	h.logCloudFetch("do: dial failed")
	data, err := os.ReadFile(filepath.Join(h.root, "state", "cloud-config.log"))
	if err != nil {
		t.Fatalf("log file missing: %v", err)
	}
	if !strings.Contains(string(data), "do: dial failed") {
		t.Fatalf("log content = %q", string(data))
	}
}
