package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostAllowed(t *testing.T) {
	allowed := []string{"github.com", "objects.githubusercontent.com", "api.gitproxy.dev"}
	cases := map[string]bool{
		"github.com":                    true,
		"objects.githubusercontent.com": true,
		"GITHUB.COM":                    true, // case-insensitive
		"api.gitproxy.dev":              true, // 大陆加速代理
		"evil-github.com":               false,
		"github.com.evil.net":           false,
		"evilgitproxy.dev":              false,
		"example.com":                   false,
	}
	for host, want := range cases {
		if got := hostAllowed(host, allowed); got != want {
			t.Errorf("hostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// downloadUpdateFile end-to-end over a local TLS server: verifies the
// content, the progress counters and the loopback-host exemption used by
// tests (production hosts are https-only GitHub domains).
func TestDownloadUpdateFile(t *testing.T) {
	payload := make([]byte, 64*1024)
	for i := range payload {
		payload[i] = byte(i)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "65536")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "setup.exe")
	prog := &updateProgress{}
	n, err := downloadUpdateFile(server.Client(), server.URL, sha256Hex(payload), dest, []string{"127.0.0.1"}, prog)
	if err != nil {
		t.Fatalf("downloadUpdateFile: %v", err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("wrote %d bytes, want %d", n, len(payload))
	}
	got, err := os.ReadFile(dest)
	if err != nil || len(got) != len(payload) {
		t.Fatalf("dest file wrong: %v (len=%d)", err, len(got))
	}
	if prog.done.Load() != int64(len(payload)) || prog.total.Load() != int64(len(payload)) {
		t.Fatalf("progress done=%d total=%d, want %d", prog.done.Load(), prog.total.Load(), len(payload))
	}
}

func TestDownloadSha256MismatchRemovesFile(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tampered"))
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "setup.exe")
	_, err := downloadUpdateFile(server.Client(), server.URL, sha256Hex([]byte("expected")), dest, []string{"127.0.0.1"}, &updateProgress{})
	if err == nil {
		t.Fatal("expected sha256 mismatch error")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatal("mismatched download must be deleted")
	}
}

func TestDownloadRejectsBadTargets(t *testing.T) {
	cases := []struct{ name, url string }{
		{"http scheme on non-loopback", "http://github.com/x/setup.exe"},
		{"unknown host", "https://evil.example/setup.exe"},
		{"invalid url", "://nope"},
	}
	for _, tc := range cases {
		if _, err := downloadUpdateFile(http.DefaultClient, tc.url, "00", filepath.Join(t.TempDir(), "x.exe"), []string{"github.com"}, &updateProgress{}); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
}

// Handler-level flow: PUT download → poll GET progress → done with a
// verified file on disk (async goroutine path).
func TestUpdateDownloadHandlerFlow(t *testing.T) {
	resetUpdateRun()
	t.Cleanup(resetUpdateRun)

	payload := []byte("freev6 update payload")
	// Plain-http loopback: allowed by the scheme rule for 127.0.0.1 and no
	// test CA needed for the production http.Client used by the handler.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	// Production allowlist only holds GitHub hosts; extend it for this test
	// and restore the original slice afterwards.
	originalHosts := append([]string(nil), updateAllowedHosts...)
	updateAllowedHosts = append(updateAllowedHosts, "127.0.0.1")
	t.Cleanup(func() { updateAllowedHosts = originalHosts })

	body, _ := json.Marshal(updateDownloadRequest{URL: server.URL + "/setup.exe", SHA256: sha256Hex(payload)})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/update/download", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h := &helper{root: t.TempDir()}
	h.updateDownload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("download accepted: HTTP %d %s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	var state, path string
	for time.Now().Before(deadline) {
		rec2 := httptest.NewRecorder()
		h.updateProgress(rec2, httptest.NewRequest(http.MethodGet, "/api/v1/update/progress", nil))
		var out struct {
			State string `json:"state"`
			Path  string `json:"path"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec2.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		state, path = out.State, out.Path
		if state == "done" || state == "error" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if state != "done" {
		t.Fatalf("download did not finish: state=%s", state)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != string(payload) {
		t.Fatalf("bad downloaded file: %v", err)
	}
}

// resetUpdateRun restarts the package-level tracker without copying its
// atomics (assignment would trip copylocks).
func resetUpdateRun() {
	updateRun.mu.Lock()
	defer updateRun.mu.Unlock()
	updateRun.state = "idle"
	updateRun.path = ""
	updateRun.err = ""
	updateRun.busy = false
	updateRun.prog.done.Store(0)
	updateRun.prog.total.Store(0)
}

func TestParseVersionJSON(t *testing.T) {
	valid := `{"version":"0.2.9","tag":"v0.2.9","notes":"## [0.2.9] - 2026-09-27\n- stuff","setup":"FreeV6_0.2.9_x64-setup.exe","download":"https://github.com/o/r/releases/download/v0.2.9/FreeV6_0.2.9_x64-setup.exe","sha256":"` + strings.Repeat("ab", 32) + `"}`
	info, err := parseVersionJSON([]byte(valid))
	if err != nil {
		t.Fatalf("valid version.json rejected: %v", err)
	}
	if info.Version != "0.2.9" || !strings.HasSuffix(info.Download, "FreeV6_0.2.9_x64-setup.exe") {
		t.Fatalf("parsed wrong: %+v", info)
	}
	if !strings.Contains(info.Notes, "## [0.2.9]") || info.SHA256 != strings.Repeat("ab", 32) {
		t.Fatalf("notes/sha lost: %+v", info)
	}

	invalid := []string{
		`{`,                                   // truncated
		`{"version":"latest","download":"x"}`, // not numeric
		`{"version":"v0.2.9","download":"x"}`, // v-prefix
		`{"version":"0.2.9"}`,                 // no download url
		``,                                    // empty
	}
	for _, body := range invalid {
		if _, err := parseVersionJSON([]byte(body)); err == nil {
			t.Errorf("expected rejection for %q", body)
		}
	}
}

func TestFetchVersionJSON(t *testing.T) {
	payload := []byte(`{"version":"9.9.9","download":"https://x/y"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	got, err := fetchVersionJSON(server.Client(), server.URL+"/version.json", []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("fetchVersionJSON: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("body mismatch: %q", got)
	}
	// host guard rejects before any network I/O
	if _, err := fetchVersionJSON(server.Client(), "https://evil.example/version.json", []string{"github.com"}); err == nil {
		t.Fatal("expected host rejection")
	}
}

// Handler flow: proxy→direct URL list is overridable; success maps the JSON,
// an unreachable list degrades to {update:false} instead of an error.
func TestUpdateLatestHandlerFlow(t *testing.T) {
	orig := updateLatestURLs
	t.Cleanup(func() { updateLatestURLs = orig })
	origHosts := append([]string(nil), updateAllowedHosts...)
	updateAllowedHosts = append(updateAllowedHosts, "127.0.0.1")
	t.Cleanup(func() { updateAllowedHosts = origHosts })

	payload := []byte(`{"version":"0.3.0","tag":"v0.3.0","notes":"## [0.3.0]","setup":"FreeV6_0.3.0_x64-setup.exe","download":"https://github.com/o/r/releases/download/v0.3.0/FreeV6_0.3.0_x64-setup.exe","sha256":"` + strings.Repeat("cd", 32) + `"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	updateLatestURLs = func() []string { return []string{server.URL + "/version.json"} }

	h := &helper{root: t.TempDir()}
	rec := httptest.NewRecorder()
	h.updateLatest(rec, httptest.NewRequest(http.MethodGet, "/api/v1/update/latest", nil))
	var okResp struct {
		Update   bool   `json:"update"`
		Version  string `json:"version"`
		Download string `json:"download"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &okResp); err != nil {
		t.Fatal(err)
	}
	if !okResp.Update || okResp.Version != "0.3.0" || !strings.HasSuffix(okResp.Download, "FreeV6_0.3.0_x64-setup.exe") {
		t.Fatalf("unexpected success response: %s", rec.Body.String())
	}

	// unreachable list (closed loopback server) → silent no-update
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	updateLatestURLs = func() []string { return []string{deadURL + "/version.json"} }
	rec2 := httptest.NewRecorder()
	h.updateLatest(rec2, httptest.NewRequest(http.MethodGet, "/api/v1/update/latest", nil))
	var noResp struct {
		Update bool `json:"update"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &noResp); err != nil {
		t.Fatal(err)
	}
	if noResp.Update {
		t.Fatalf("unreachable must yield update:false: %s", rec2.Body.String())
	}
}
