package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yourname/freev6/internal/appupdate"
)

func TestHostAllowed(t *testing.T) {
	allowed := []string{"github.com", "objects.githubusercontent.com", "gh-proxy.com",
		"v6.gh-proxy.org", "v4.gh-proxy.org", "cdn.gh-proxy.org", "axisnow.gh-proxy.org"}
	cases := map[string]bool{
		"github.com":                    true,
		"objects.githubusercontent.com": true,
		"GITHUB.COM":                    true, // case-insensitive
		"gh-proxy.com":                  true, // 大陆加速代理
		"v6.gh-proxy.org":               true, // 新增镜像族
		"v4.gh-proxy.org":               true,
		"cdn.gh-proxy.org":              true,
		"axisnow.gh-proxy.org":          true,
		"evil-github.com":               false,
		"github.com.evil.net":           false,
		"evilgh-proxy.com":              false,
		"gh-proxy.org.evil.net":         false,
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
	// Keep the download hermetic: the handler writes to updateSetupDest() by
	// default (the real per-user cache dir), which tests must not touch.
	originalDest := updateSetupDest
	updateSetupDest = func() string { return filepath.Join(t.TempDir(), "setup.exe") }
	t.Cleanup(func() { updateSetupDest = originalDest })

	body, _ := json.Marshal(updateDownloadRequest{URL: server.URL + "/setup.exe", SHA256: sha256Hex(payload)})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/update/download", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h := &helper{root: t.TempDir()}
	// The real source check would hit GitHub; stub it for the handler flow.
	originalVerify := verifyUpdateSource
	verifyUpdateSource = func(string, string) error { return nil }
	t.Cleanup(func() { verifyUpdateSource = originalVerify })
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

func TestFetchBody(t *testing.T) {
	payload := []byte(`{"version":"9.9.9","download":"https://x/y"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	ctx := context.Background()
	got, err := fetchBody(ctx, server.Client(), server.URL+"/version.json", []string{"127.0.0.1"}, updateManifestMaxBytes)
	if err != nil {
		t.Fatalf("fetchBody: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("body mismatch: %q", got)
	}
	// host guard rejects before any network I/O
	if _, err := fetchBody(ctx, server.Client(), "https://evil.example/version.json", []string{"github.com"}, updateManifestMaxBytes); err == nil {
		t.Fatal("expected host rejection")
	}
	// 上限由读取端裁掉，不是错误：签名文件用 4 KiB 的上限走同一条路径。
	truncated, err := fetchBody(ctx, server.Client(), server.URL+"/version.json", []string{"127.0.0.1"}, 4)
	if err != nil || len(truncated) != 4 {
		t.Fatalf("max bytes must truncate: len=%d err=%v", len(truncated), err)
	}
}

// Handler flow: proxy→direct URL list is overridable; success maps the JSON,
// an unreachable list degrades to {update:false} instead of an error.
func TestUpdateLatestHandlerFlow(t *testing.T) {
	allowLoopback(t)
	sign := installTestSigner(t)
	resetCacheForTest(t)
	orig := updateLatestURLs
	t.Cleanup(func() { updateLatestURLs = orig })

	payload := []byte(`{"version":"0.3.0","tag":"v0.3.0","notes":"## [0.3.0]","setup":"FreeV6_0.3.0_x64-setup.exe","download":"https://github.com/o/r/releases/download/v0.3.0/FreeV6_0.3.0_x64-setup.exe","sha256":"` + strings.Repeat("cd", 32) + `"}`)
	server, _ := signedManifestServer(t, payload, sign)
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
	resetManifestCache()
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

// withUpdateDownloadTest 注入镜像前缀并压缩停滞/硬限（测试专用）。
func withUpdateDownloadTest(t *testing.T, prefixes ...string) {
	t.Helper()
	origPrefixes := updateMirrorPrefixes
	origStall := updateStallTimeout
	origHard := updateMirrorHardTimeout
	origInterval := updateStallCheckInterval
	updateMirrorPrefixes = prefixes
	updateStallTimeout = 300 * time.Millisecond
	updateMirrorHardTimeout = 3 * time.Second
	updateStallCheckInterval = 50 * time.Millisecond
	t.Cleanup(func() {
		updateMirrorPrefixes = origPrefixes
		updateStallTimeout = origStall
		updateMirrorHardTimeout = origHard
		updateStallCheckInterval = origInterval
	})
}

func TestUpdateDownloadCandidatesRewritesGitHubURL(t *testing.T) {
	orig := updateMirrorPrefixes
	updateMirrorPrefixes = []string{"https://a.example/", "https://b.example/"}
	t.Cleanup(func() { updateMirrorPrefixes = orig })

	githubURL := "https://github.com/evansrrr/free-v6/releases/download/v9.9.9/setup.exe"
	got := updateDownloadCandidates(githubURL)
	want := []string{"https://a.example/" + githubURL, "https://b.example/" + githubURL, githubURL}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// 非 github.com 的已允许主机（CDN/镜像本体）不改写。
	cdnURL := "https://objects.githubusercontent.com/x/setup.exe"
	if got := updateDownloadCandidates(cdnURL); len(got) != 1 || got[0] != cdnURL {
		t.Fatalf("cdn url must stay single, got %v", got)
	}
}

// 首条线路 500 → 换线成功；github.com 形态的 URL 经前缀改写命中本地镜像，
// 直连候选因白名单不含 github.com 被本地拒绝（不触真实网络）。
func TestDownloadUpdateFileFallsBackToNextMirror(t *testing.T) {
	payload := []byte("installer payload across mirrors")
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer good.Close()
	withUpdateDownloadTest(t, bad.URL+"/", good.URL+"/")

	githubURL := "https://github.com/evansrrr/free-v6/releases/download/v9.9.9/setup.exe"
	dest := filepath.Join(t.TempDir(), "setup.exe")
	_, err := downloadUpdateFile(http.DefaultClient, githubURL, sha256Hex(payload), dest, []string{"127.0.0.1"}, &updateProgress{})
	if err != nil {
		t.Fatalf("fallback must succeed: %v", err)
	}
	if got, readErr := os.ReadFile(dest); readErr != nil || len(got) != len(payload) {
		t.Fatalf("dest wrong: %v", readErr)
	}
}

// 首条线路内容与 sha 不符 → 按线路失败继续换线。
func TestDownloadUpdateFileDigestMismatchFallsBack(t *testing.T) {
	payload := []byte("honest installer bytes")
	tampered := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tampered mirror junk"))
	}))
	defer tampered.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer good.Close()
	withUpdateDownloadTest(t, tampered.URL+"/", good.URL+"/")

	githubURL := "https://github.com/evansrrr/free-v6/releases/download/v9.9.9/setup.exe"
	dest := filepath.Join(t.TempDir(), "setup.exe")
	if _, err := downloadUpdateFile(http.DefaultClient, githubURL, sha256Hex(payload), dest, []string{"127.0.0.1"}, &updateProgress{}); err != nil {
		t.Fatalf("mismatch must fall back: %v", err)
	}
	if _, statErr := os.Stat(dest); statErr != nil {
		t.Fatalf("dest must exist after good mirror: %v", statErr)
	}
}

// 挂死的线路在停滞窗口内被切掉——进度条最坏冻结 ~300ms（生产 10s）。
func TestDownloadUpdateFileStallFailover(t *testing.T) {
	payload := []byte("second mirror wins")
	hung := make(chan struct{})
	hungServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hung
	}))
	defer hungServer.Close()
	defer close(hung)
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer good.Close()
	withUpdateDownloadTest(t, hungServer.URL+"/", good.URL+"/")

	githubURL := "https://github.com/evansrrr/free-v6/releases/download/v9.9.9/setup.exe"
	dest := filepath.Join(t.TempDir(), "setup.exe")
	start := time.Now()
	if _, err := downloadUpdateFile(http.DefaultClient, githubURL, sha256Hex(payload), dest, []string{"127.0.0.1"}, &updateProgress{}); err != nil {
		t.Fatalf("fallback must win: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("hung mirror must be cut by stall watchdog, took %v", elapsed)
	}
}

func TestUpdateLatestURLsDefaultIncludesMirrors(t *testing.T) {
	urls := updateLatestURLs()
	if len(urls) != len(updateMirrorPrefixes)+1 {
		t.Fatalf("expected %d urls (mirrors + direct), got %d", len(updateMirrorPrefixes)+1, len(urls))
	}
	if !strings.HasPrefix(urls[0], "https://v6.gh-proxy.org/") {
		t.Fatalf("first url should be the fastest mirror, got %s", urls[0])
	}
	if urls[len(urls)-1] != "https://github.com/"+updateRepoSlug+"/releases/latest/download/version.json" {
		t.Fatalf("last url must be the direct fallback, got %s", urls[len(urls)-1])
	}
}

// ── 签名相关的测试夹具 ────────────────────────────────────────────

// allowLoopback 把 127.0.0.1 临时加进更新白名单（生产只有 GitHub 与镜像族）。
func allowLoopback(t *testing.T) {
	t.Helper()
	original := append([]string(nil), updateAllowedHosts...)
	updateAllowedHosts = append(updateAllowedHosts, "127.0.0.1")
	t.Cleanup(func() { updateAllowedHosts = original })
}

// installTestSigner 生成一次性密钥并替换验签入口，返回"对字节签名"的函数。
// 用它签出的信封只被本次测试接受：客户端内置公钥仍然为空（或生产密钥）。
func installTestSigner(t *testing.T) func(body []byte) []byte {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keySet := map[string]ed25519.PublicKey{"test": pub}
	original := verifyManifestSignature
	verifyManifestSignature = func(manifest, envelope []byte) error {
		return appupdate.VerifyWith(keySet, manifest, envelope)
	}
	t.Cleanup(func() { verifyManifestSignature = original })
	return func(body []byte) []byte {
		envelope, err := appupdate.Sign(body, priv, "test")
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return envelope
	}
}

// signedManifestServer 同时提供 version.json 与 version.json.sig，
// 返回服务与"命中次数"计数器（用于断言缓存命中后不再发请求）。
func signedManifestServer(t *testing.T, body []byte, sign func([]byte) []byte) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if strings.HasSuffix(r.URL.Path, ".sig") {
			_, _ = w.Write(sign(body))
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func resetCacheForTest(t *testing.T) {
	t.Helper()
	resetManifestCache()
	t.Cleanup(resetManifestCache)
}

func manifestBody(sum string) []byte {
	return []byte(fmt.Sprintf(`{"version":"0.5.7","tag":"v0.5.7","setup":"FreeV6_0.5.7_x64-setup.exe","download":"https://github.com/evansrrr/free-v6/releases/download/v0.5.7/FreeV6_0.5.7_x64-setup.exe","sha256":"%s"}`, sum))
}

// ── 源校验 ────────────────────────────────────────────────────────

func TestVerifyUpdateSource(t *testing.T) {
	resetCacheForTest(t)
	allowLoopback(t)
	sign := installTestSigner(t)

	sum := strings.Repeat("ab", 32)
	server, _ := signedManifestServer(t, manifestBody(sum), sign)

	originalStable := updateLatestURLs
	originalBeta := updateBetaLatestURLs
	updateLatestURLs = func() []string { return []string{server.URL + "/stable/version.json"} }
	updateBetaLatestURLs = func() []string { return []string{server.URL + "/beta/version.json"} }
	t.Cleanup(func() { updateLatestURLs = originalStable; updateBetaLatestURLs = originalBeta })

	if err := verifyUpdateSource("https://github.com/x/setup.exe", sum); err != nil {
		t.Fatalf("matching sha must verify, got %v", err)
	}
	if err := verifyUpdateSource("https://github.com/x/setup.exe", strings.Repeat("0", 64)); err == nil {
		t.Fatal("non-matching sha must be rejected")
	}
	if err := verifyUpdateSource("https://github.com/x/setup.exe", "short"); err == nil {
		t.Fatal("malformed sha must be rejected")
	}
}

// 冷缓存时给的是**一份**总预算：两路都挂死也不能等成两份预算之和。
func TestVerifyUpdateSourceColdCacheBudget(t *testing.T) {
	resetCacheForTest(t)
	allowLoopback(t)

	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(hang.Close)

	originalStable, originalBeta := updateLatestURLs, updateBetaLatestURLs
	updateLatestURLs = func() []string { return []string{hang.URL + "/stable/version.json"} }
	updateBetaLatestURLs = func() []string { return []string{hang.URL + "/beta/version.json"} }
	t.Cleanup(func() { updateLatestURLs = originalStable; updateBetaLatestURLs = originalBeta })

	originalBudget := updateSourceBudget
	updateSourceBudget = 300 * time.Millisecond
	t.Cleanup(func() { updateSourceBudget = originalBudget })

	start := time.Now()
	err := verifyUpdateSource("https://github.com/x/setup.exe", strings.Repeat("ab", 32))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("unreachable manifests must not verify")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("cold-cache source check must respect one shared budget, took %v", elapsed)
	}
}

// ── 清单拉取：并发、预算、成对、失败关闭 ──────────────────────────

func TestUpdateLatestBudgetWithHungMirror(t *testing.T) {
	allowLoopback(t)
	sign := installTestSigner(t)
	resetCacheForTest(t)

	good, _ := signedManifestServer(t, manifestBody(strings.Repeat("cd", 32)), sign)
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(hang.Close)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	original := updateLatestURLs
	updateLatestURLs = func() []string {
		return []string{
			deadURL + "/a/version.json",
			hang.URL + "/b/version.json", // 挂死：串行实现会在这里等满单请求超时
			good.URL + "/c/version.json",
		}
	}
	t.Cleanup(func() { updateLatestURLs = original })

	originalBudget := updateCheckBudget
	updateCheckBudget = 800 * time.Millisecond
	t.Cleanup(func() { updateCheckBudget = originalBudget })

	start := time.Now()
	rec := httptest.NewRecorder()
	h := &helper{root: t.TempDir()}
	h.updateLatest(rec, httptest.NewRequest(http.MethodGet, "/api/v1/update/latest", nil))
	elapsed := time.Since(start)

	var payload struct {
		Update  bool   `json:"update"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Update || payload.Version != "0.5.7" {
		t.Fatalf("healthy candidate must win: %s", rec.Body.String())
	}
	if elapsed > 3*time.Second {
		t.Fatalf("hung mirror must not dominate the wait, took %v", elapsed)
	}

	// 全部候选都挂死：由**预算**兜底，而不是"每条线路各等一个请求超时"。
	// 串行实现这里是 3 × 8s；带预算的并发实现应该在一个预算附近返回。
	resetManifestCache()
	updateLatestURLs = func() []string {
		return []string{
			hang.URL + "/x/version.json",
			hang.URL + "/y/version.json",
			hang.URL + "/z/version.json",
		}
	}
	start = time.Now()
	rec = httptest.NewRecorder()
	h.updateLatest(rec, httptest.NewRequest(http.MethodGet, "/api/v1/update/latest", nil))
	elapsed = time.Since(start)
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Update {
		t.Fatalf("all-hung candidates must yield update:false: %s", rec.Body.String())
	}
	if elapsed > 3*time.Second {
		t.Fatalf("all-hung check must be bounded by the budget, took %v", elapsed)
	}
}

// 两通道并发：各挂 200ms 时总耗时必须明显低于串行的 400ms。
func TestUpdateLatestFetchesChannelsConcurrently(t *testing.T) {
	allowLoopback(t)
	sign := installTestSigner(t)
	resetCacheForTest(t)

	stableBody := manifestBody(strings.Repeat("11", 32))
	betaBody := manifestBody(strings.Repeat("22", 32))
	delayed := func(body []byte) http.HandlerFunc {
		signature := sign(body)
		return func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
			if strings.HasSuffix(r.URL.Path, ".sig") {
				_, _ = w.Write(signature)
				return
			}
			_, _ = w.Write(body)
		}
	}
	stable := httptest.NewServer(delayed(stableBody))
	t.Cleanup(stable.Close)
	beta := httptest.NewServer(delayed(betaBody))
	t.Cleanup(beta.Close)

	originalStable, originalBeta := updateLatestURLs, updateBetaLatestURLs
	updateLatestURLs = func() []string { return []string{stable.URL + "/version.json"} }
	updateBetaLatestURLs = func() []string { return []string{beta.URL + "/version.json"} }
	t.Cleanup(func() { updateLatestURLs = originalStable; updateBetaLatestURLs = originalBeta })

	start := time.Now()
	rec := httptest.NewRecorder()
	h := &helper{root: t.TempDir()}
	h.updateLatest(rec, httptest.NewRequest(http.MethodGet, "/api/v1/update/latest?channel=beta", nil))
	elapsed := time.Since(start)

	var payload struct {
		Update bool `json:"update"`
		Beta   struct {
			Version string `json:"version"`
		} `json:"beta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Update || payload.Beta.Version != "0.5.7" {
		t.Fatalf("beta channel must be reported: %s", rec.Body.String())
	}
	if elapsed > 380*time.Millisecond {
		t.Fatalf("channels must be fetched concurrently, took %v", elapsed)
	}
}

// 检查更新后必须把验签通过的清单写进缓存，点"立即更新"时不再发清单请求。
func TestUpdateLatestPopulatesVerifiedCache(t *testing.T) {
	allowLoopback(t)
	sign := installTestSigner(t)
	resetCacheForTest(t)

	sum := strings.Repeat("ef", 32)
	server, hits := signedManifestServer(t, manifestBody(sum), sign)
	original := updateLatestURLs
	updateLatestURLs = func() []string { return []string{server.URL + "/version.json"} }
	t.Cleanup(func() { updateLatestURLs = original })

	h := &helper{root: t.TempDir()}
	rec := httptest.NewRecorder()
	h.updateLatest(rec, httptest.NewRequest(http.MethodGet, "/api/v1/update/latest", nil))
	afterCheck := atomic.LoadInt32(hits)
	if afterCheck == 0 {
		t.Fatal("update check must hit the network")
	}

	if err := verifyUpdateSource("https://github.com/x/setup.exe", sum); err != nil {
		t.Fatalf("cached manifest must verify the published sha: %v", err)
	}
	if got := atomic.LoadInt32(hits); got != afterCheck {
		t.Fatalf("source check must reuse the cache: hits %d -> %d", afterCheck, got)
	}
}

func TestFetchVerifiedManifestRejectsPairMismatch(t *testing.T) {
	allowLoopback(t)
	sign := installTestSigner(t)

	good := manifestBody(strings.Repeat("33", 32))
	other := manifestBody(strings.Repeat("44", 32))
	// 清单来自 good、签名却覆盖 other：这正是"清单 A + 签名 B"的投毒形状。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sig") {
			_, _ = w.Write(sign(other))
			return
		}
		_, _ = w.Write(good)
	}))
	t.Cleanup(server.Close)

	if _, err := fetchVerifiedManifest(server.Client(), []string{server.URL + "/version.json"}, 2*time.Second); err == nil {
		t.Fatal("manifest signed by another candidate must be rejected")
	}
}

func TestFetchVerifiedManifestRejectsUnsigned(t *testing.T) {
	allowLoopback(t)
	installTestSigner(t)

	body := manifestBody(strings.Repeat("55", 32))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sig") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	_, err := fetchVerifiedManifest(server.Client(), []string{server.URL + "/version.json"}, 2*time.Second)
	if err == nil {
		t.Fatal("unsigned manifest must be rejected")
	}
	if !strings.Contains(err.Error(), "missing signature") {
		t.Fatalf("error should name the missing signature, got %v", err)
	}
}

// ── 下载入口的同步校验 ────────────────────────────────────────────

func TestUpdateDownloadRejectsMissingSHA(t *testing.T) {
	resetUpdateRun()
	t.Cleanup(resetUpdateRun)
	allowLoopback(t)

	h := &helper{root: t.TempDir()}
	body, _ := json.Marshal(updateDownloadRequest{URL: "http://127.0.0.1:1/setup.exe", SHA256: ""})
	rec := httptest.NewRecorder()
	h.updateDownload(rec, httptest.NewRequest(http.MethodPut, "/api/v1/update/download", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing sha must be rejected synchronously, got HTTP %d %s", rec.Code, rec.Body.String())
	}
	if updateRun.state != "idle" {
		t.Fatalf("no download may start, state=%s", updateRun.state)
	}

	// 短摘要同样同步拒绝。
	body, _ = json.Marshal(updateDownloadRequest{URL: "http://127.0.0.1:1/setup.exe", SHA256: "abcd"})
	rec = httptest.NewRecorder()
	h.updateDownload(rec, httptest.NewRequest(http.MethodPut, "/api/v1/update/download", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed sha must be rejected synchronously, got HTTP %d", rec.Code)
	}

	// 白名单外的地址同样同步拒绝（不会起 goroutine 去连它）。
	body, _ = json.Marshal(updateDownloadRequest{URL: "https://evil.example/setup.exe", SHA256: strings.Repeat("ab", 32)})
	rec = httptest.NewRecorder()
	h.updateDownload(rec, httptest.NewRequest(http.MethodPut, "/api/v1/update/download", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("disallowed host must be rejected synchronously, got HTTP %d", rec.Code)
	}
}

// 重定向必须仍然落在白名单里：初始地址 127.0.0.1 合法，302 到 localhost 就不合法。
// 没有这道守卫时内容会被成功读取，"只访问白名单主机"的传输约束形同虚设。
func TestFetchBodyRejectsRedirectOutsideAllowlist(t *testing.T) {
	var port string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version.json" {
			http.Redirect(w, r, "http://localhost:"+port+"/secret", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"version":"9.9.9","download":"https://x/y"}`))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port = parsed.Port()

	_, err = fetchBody(context.Background(), server.Client(), server.URL+"/version.json", []string{"127.0.0.1"}, updateManifestMaxBytes)
	if err == nil {
		t.Fatal("redirect to a non-allowlisted host must be rejected")
	}
	if !strings.Contains(err.Error(), "host not allowed") {
		t.Fatalf("error should name the host guard, got %v", err)
	}
}

// 白名单内的跳转必须继续可用：GitHub 的 release 资产本来就会 302 到自家 CDN。
func TestFetchBodyFollowsAllowlistedRedirect(t *testing.T) {
	payload := []byte(`{"version":"9.9.9","download":"https://x/y"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version.json" {
			http.Redirect(w, r, "/real.json", http.StatusFound)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	got, err := fetchBody(context.Background(), server.Client(), server.URL+"/version.json", []string{"127.0.0.1"}, updateManifestMaxBytes)
	if err != nil {
		t.Fatalf("allowlisted redirect must be followed: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("body mismatch: %q", got)
	}
}
