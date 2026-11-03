package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestCloudImportFromContent(t *testing.T) {
	h := &helper{root: t.TempDir()}
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
	_, err := fetchCloudProfile(server.URL)
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
	data, err := fetchCloudProfile(server.URL)
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
