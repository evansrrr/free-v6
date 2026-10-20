package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const chainTestYAML = `proxies:
  - name: chain-node-1
    type: socks5
    server: a.example.com
    port: 1080
  - name: chain-node-2
    type: ss
    server: b.example.com
    port: 443
    cipher: aes-256-gcm
    password: pw
`

func chainRequest(t *testing.T, h *helper, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, "/api/v1/chain-proxy", reader)
	recorder := httptest.NewRecorder()
	h.chainProxy(recorder, request)
	return recorder
}

func TestChainImportFileAndStatus(t *testing.T) {
	h := &helper{root: t.TempDir()}

	put := chainRequest(t, h, http.MethodPut, `{"content":"`+jsonEscape(chainTestYAML)+`"}`)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", put.Code, put.Body.String())
	}
	var imported struct {
		Count      int      `json:"count"`
		Names      []string `json:"names"`
		SourceType string   `json:"sourceType"`
	}
	if err := json.Unmarshal(put.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Count != 2 || imported.SourceType != "file" {
		t.Fatalf("unexpected import result: %+v", imported)
	}
	if imported.Names[0] != "chain-node-1" {
		t.Fatalf("unexpected names: %v", imported.Names)
	}

	raw, err := os.ReadFile(filepath.Join(h.root, "config", "chain-proxy.yaml"))
	if err != nil {
		t.Fatalf("snapshot must be persisted: %v", err)
	}
	if string(raw) != chainTestYAML {
		t.Fatal("persisted snapshot differs from imported content")
	}

	get := chainRequest(t, h, http.MethodGet, "")
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d", get.Code)
	}
	var status struct {
		Enabled    bool     `json:"enabled"`
		SourceType string   `json:"sourceType"`
		Count      int      `json:"count"`
		Names      []string `json:"names"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || status.Count != 2 || status.SourceType != "file" || len(status.Names) != 2 {
		t.Fatalf("unexpected status: %+v", status)
	}
	// Names only — credential fields must never appear in API responses.
	if strings.Contains(get.Body.String(), "password") {
		t.Fatalf("status leaked node credentials: %s", get.Body.String())
	}
}

func TestChainImportRejectsBadContent(t *testing.T) {
	h := &helper{root: t.TempDir()}

	for name, body := range map[string]string{
		"no proxies": `{"content":"rules:\n  - MATCH,DIRECT\n"}`,
		"bad yaml":   `{"content":"::::::\n  ["}`,
		"empty":      `{}`,
		"bad scheme": `{"url":"ftp://example.com/sub"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := chainRequest(t, h, http.MethodPut, body)
			if rec.Code < 400 || rec.Code >= 500 {
				t.Fatalf("status = %d, want 4xx (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
	if _, err := os.Stat(filepath.Join(h.root, "config", "chain-proxy.yaml")); !os.IsNotExist(err) {
		t.Fatal("failed import must not write a snapshot")
	}
	status := chainRequest(t, h, http.MethodGet, "")
	if !bytes.Contains(status.Body.Bytes(), []byte(`"enabled":false`)) {
		t.Fatalf("failed import must not enable the chain: %s", status.Body.String())
	}
}

func TestChainImportRejectsOversizeBody(t *testing.T) {
	h := &helper{root: t.TempDir()}
	huge := strings.Repeat("a", maxChainProxyBytes+8)
	rec := chainRequest(t, h, http.MethodPut, `{"content":"`+huge+`"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "2MB") {
		t.Fatalf("status = %d body = %s, want 400 mentioning 2MB", rec.Code, rec.Body.String())
	}
}

func TestChainImportFromURLAndRefreshFailureKeepsSnapshot(t *testing.T) {
	h := &helper{root: t.TempDir()}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(chainTestYAML))
	}))
	defer server.Close()

	put := chainRequest(t, h, http.MethodPut, `{"url":"`+server.URL+`/sub"}`)
	if put.Code != http.StatusOK {
		t.Fatalf("URL import status = %d, body = %s", put.Code, put.Body.String())
	}
	var imported struct {
		SourceType string `json:"sourceType"`
		SourceURL  string `json:"sourceUrl"`
	}
	if err := json.Unmarshal(put.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.SourceType != "url" || imported.SourceURL != server.URL+"/sub" {
		t.Fatalf("unexpected source: %+v", imported)
	}

	// Refresh against a dead endpoint: old snapshot AND source fields stay.
	dead := chainRequest(t, h, http.MethodPut, `{"url":"http://127.0.0.1:1/sub"}`)
	if dead.Code < 400 {
		t.Fatalf("dead refresh must fail, got %d", dead.Code)
	}
	status := chainRequest(t, h, http.MethodGet, "")
	var after struct {
		Enabled    bool   `json:"enabled"`
		SourceType string `json:"sourceType"`
		Count      int    `json:"count"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if !after.Enabled || after.Count != 2 || after.SourceType != "url" || after.Error != "" {
		t.Fatalf("failed refresh must keep the last good snapshot: %+v", after)
	}
}

func TestChainClearRemovesEverything(t *testing.T) {
	h := &helper{root: t.TempDir()}
	if rec := chainRequest(t, h, http.MethodPut, `{"content":"`+jsonEscape(chainTestYAML)+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("setup import failed: %s", rec.Body.String())
	}
	if rec := chainRequest(t, h, http.MethodDelete, ""); rec.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(h.root, "config", "chain-proxy.yaml")); !os.IsNotExist(err) {
		t.Fatal("snapshot file must be gone")
	}
	status := chainRequest(t, h, http.MethodGet, "")
	for _, expected := range []string{`"enabled":false`, `"count":0`, `"sourceType":""`} {
		if !bytes.Contains(status.Body.Bytes(), []byte(expected)) {
			t.Fatalf("status after clear missing %s: %s", expected, status.Body.String())
		}
	}
}

// The GUI's settings PUT never carries the chain keys — omitting them must
// preserve the stored import state, or every settings save would silently
// disable the chain and flip the exit IP back to bare WARP.
func TestSettingsPUTPreservesChainFields(t *testing.T) {
	h := &helper{root: t.TempDir()}
	if rec := chainRequest(t, h, http.MethodPut, `{"content":"`+jsonEscape(chainTestYAML)+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("setup import failed: %s", rec.Body.String())
	}

	put := httptest.NewRequest(http.MethodPut, "/api/v1/settings",
		strings.NewReader(`{"mode":"global","campusCidrs":["edu.cn"]}`))
	rec := httptest.NewRecorder()
	h.settings(rec, put)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings PUT status = %d, body = %s", rec.Code, rec.Body.String())
	}

	get := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	getRec := httptest.NewRecorder()
	h.settings(getRec, get)
	body := getRec.Body.String()
	if !strings.Contains(body, `"chainEnabled":true`) || !strings.Contains(body, `"chainSourceType":"file"`) {
		t.Fatalf("settings PUT wiped chain fields: %s", body)
	}
}

func jsonEscape(value string) string {
	encoded, _ := json.Marshal(value)
	return strings.TrimSuffix(strings.TrimPrefix(string(encoded), `"`), `"`)
}

// startProxy 的链式闸门放在端口/权限检查之前（纯读输入校验），所以非提权
// 环境也能直接单测三条契约：启用却缺内容 → 400 明确报错；内容损坏 → 400；
// 未启用 → 根本不读链式文件。
func TestStartProxyChainGates(t *testing.T) {
	startBody := `{"mode":"rule","campusCidrs":["edu.cn"],"devMode":false}`
	runStart := func(h *helper) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/proxy/start", strings.NewReader(startBody))
		recorder := httptest.NewRecorder()
		h.startProxy(recorder, request)
		return recorder
	}
	chainErrMarkers := []string{"链式代理已启用但导入内容缺失", "解析链式代理失败"}

	t.Run("enabled but snapshot missing", func(t *testing.T) {
		h := &helper{root: t.TempDir()}
		if rec := chainRequest(t, h, http.MethodPut, `{"content":"`+jsonEscape(chainTestYAML)+`"}`); rec.Code != http.StatusOK {
			t.Fatalf("setup import failed: %s", rec.Body.String())
		}
		if err := os.Remove(h.chainPath()); err != nil {
			t.Fatal(err)
		}
		rec := runStart(h)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "重新导入") {
			t.Fatalf("status = %d body = %s, want 400 about re-import", rec.Code, rec.Body.String())
		}
	})

	t.Run("enabled with corrupt snapshot", func(t *testing.T) {
		h := &helper{root: t.TempDir()}
		if rec := chainRequest(t, h, http.MethodPut, `{"content":"`+jsonEscape(chainTestYAML)+`"}`); rec.Code != http.StatusOK {
			t.Fatalf("setup import failed: %s", rec.Body.String())
		}
		if err := os.WriteFile(h.chainPath(), []byte("proxies: [definitely not yaml"), 0o600); err != nil {
			t.Fatal(err)
		}
		rec := runStart(h)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "解析链式代理失败") {
			t.Fatalf("status = %d body = %s, want 400 parse failure", rec.Code, rec.Body.String())
		}
	})

	t.Run("disabled ignores chain file", func(t *testing.T) {
		h := &helper{root: t.TempDir()}
		if err := os.MkdirAll(filepath.Dir(h.chainPath()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(h.chainPath(), []byte("garbage: ["), 0o600); err != nil {
			t.Fatal(err)
		}
		rec := runStart(h)
		for _, marker := range chainErrMarkers {
			if strings.Contains(rec.Body.String(), marker) {
				t.Fatalf("disabled chain must skip parsing, got: %s", rec.Body.String())
			}
		}
	})

	t.Run("enabled valid passes the gate", func(t *testing.T) {
		h := &helper{root: t.TempDir()}
		if rec := chainRequest(t, h, http.MethodPut, `{"content":"`+jsonEscape(chainTestYAML)+`"}`); rec.Code != http.StatusOK {
			t.Fatalf("setup import failed: %s", rec.Body.String())
		}
		rec := runStart(h)
		for _, marker := range chainErrMarkers {
			if strings.Contains(rec.Body.String(), marker) {
				t.Fatalf("valid chain must pass the gate, got: %s", rec.Body.String())
			}
		}
	})
}
