package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yourname/freev6/internal/stallhttp"
)

// In-app updater download support: the frontend resolves the release asset
// through the GitHub API and PUTs {url, sha256} to /api/v1/update/download;
// the helper streams it to %TEMP% with progress counters while the frontend
// polls GET /api/v1/update/progress, verifying SHA256 before Rust launches
// the installer.
//
// The local API is CORS-open, so this endpoint must not become an
// arbitrary-file fetcher: only https GitHub release hosts plus the gh-proxy
// acceleration mirror family are accepted (loopback http is allowed for
// tests only). github.com asset URLs are rewritten through the mirror
// prefixes with stall-based failover, same playbook as the mihomo runtime
// download (internal/mihomo/download.go).

var updateAllowedHosts = []string{
	"github.com",
	"objects.githubusercontent.com",
	"release-assets.githubusercontent.com",
	"gh-proxy.com", // 大陆访问加速代理（直通 github release 资产）
	"v6.gh-proxy.org",
	"v4.gh-proxy.org",
	"cdn.gh-proxy.org",
	"axisnow.gh-proxy.org",
}

// updateMirrorPrefixes 把 github.com 资产 URL 改写成各加速镜像的直通
// 地址；顺序按 2026-10 校园网实测延迟。变量便于测试注入。
var updateMirrorPrefixes = []string{
	"https://v6.gh-proxy.org/",
	"https://v4.gh-proxy.org/",
	"https://gh-proxy.com/",
	"https://axisnow.gh-proxy.org/",
	"https://cdn.gh-proxy.org/",
}

// 单线路预算：停滞 10s 零进展即换线（进度条冻结最坏 10s 就切），硬上限
// 兜住持续蠕动；变量便于测试压缩时长。
var (
	updateStallTimeout       = 10 * time.Second
	updateMirrorHardTimeout  = 3 * time.Minute
	updateStallCheckInterval = time.Second
)

// updateProgress is written by the download goroutine and read by the
// progress endpoint; state/path/err/busy live behind updateRun.mu.
type updateProgress struct {
	done  atomic.Int64
	total atomic.Int64 // -1 while Content-Length is unknown
}

type updateRunState struct {
	mu    sync.Mutex
	state string // idle | downloading | done | error
	path  string
	err   string
	busy  bool
	prog  updateProgress
}

var updateRun = updateRunState{state: "idle"}

func hostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(host)
	for _, h := range allowed {
		if host == h {
			return true
		}
	}
	return false
}

// progressWriter feeds the atomic counter so /update/progress can report
// while io.Copy runs in the background.
type progressWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n.Add(int64(n))
	return n, err
}

// updateSetupDest returns where the NSIS update package is downloaded to.
// It deliberately avoids %TEMP%: dropping an .exe into the temp folder and
// then executing it is the single strongest "dropper" heuristic AV engines
// (including Defender's !ml models) key on. A per-user cache dir reads as
// routine application behaviour instead. Falls back to temp only if the OS
// refuses to report a cache dir. A var so tests can point it at t.TempDir().
var updateSetupDest = func() string {
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		return filepath.Join(dir, "FreeV6", "freev6-update-setup.exe")
	}
	return filepath.Join(os.TempDir(), "freev6-update-setup.exe")
}

// validateUpdateURL enforces https (loopback http for tests) and the host
// allowlist before any request is made.
func validateUpdateURL(rawURL string, allowed []string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url")
	}
	loopback := hostAllowed(u.Hostname(), []string{"127.0.0.1", "localhost"})
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return fmt.Errorf("unsupported url scheme: %s", u.Scheme)
	}
	if !hostAllowed(u.Hostname(), allowed) {
		return fmt.Errorf("host not allowed: %s", u.Hostname())
	}
	return nil
}

// updateDownloadCandidates 展开下载候选：github.com URL 走镜像前缀改写
// 并以直连兜底；其他已允许的主机（GitHub CDN、镜像本体）原样单条——
// 前缀拼在它们身上只会拼出无效地址。
func updateDownloadCandidates(rawURL string) []string {
	if err := validateUpdateURL(rawURL, []string{"github.com"}); err == nil {
		candidates := make([]string, 0, len(updateMirrorPrefixes)+1)
		for _, prefix := range updateMirrorPrefixes {
			candidates = append(candidates, prefix+rawURL)
		}
		return append(candidates, rawURL)
	}
	return []string{rawURL}
}

// updateHostOf 提取 URL 的 host 用于错误汇总。
func updateHostOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return parsed.Hostname()
}

// downloadUpdateFile tries every candidate mirror for rawURL; each attempt
// gets its own hard/stall budget so a hung mirror cannot freeze the progress
// bar for minutes. A SHA256 mismatch also advances to the next mirror (a
// hostile or broken edge must not block the honest one). Progress counters
// restart on each attempt — the frontend bar may jump back, by design.
func downloadUpdateFile(client *http.Client, rawURL, sha256Hex, dest string, allowed []string, prog *updateProgress) (int64, error) {
	if strings.TrimSpace(sha256Hex) == "" {
		return 0, fmt.Errorf("missing sha256")
	}
	candidates := updateDownloadCandidates(rawURL)
	var failures []string
	for _, candidate := range candidates {
		if err := validateUpdateURL(candidate, allowed); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		written, attemptErr := downloadUpdateFileOnce(client, candidate, sha256Hex, dest, prog)
		if attemptErr == nil {
			return written, nil
		}
		failures = append(failures, fmt.Sprintf("%s: %v", updateHostOf(candidate), attemptErr))
	}
	return 0, fmt.Errorf("下载更新包失败（已尝试 %d/%d 条线路: %s），请检查网络后重试",
		len(failures), len(candidates), strings.Join(failures, "; "))
}

// downloadUpdateFileOnce streams one mirror into dest and verifies sha256Hex.
// Redirects are followed by the client (github.com release assets redirect
// to GitHub's own CDN — trusted once github.com was validated; the gh-proxy
// mirrors redirect to their upstream CDN likewise).
func downloadUpdateFileOnce(client *http.Client, rawURL, sha256Hex, dest string, prog *updateProgress) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), updateMirrorHardTimeout)
	defer cancel()

	// watchdog 在 Do 之前启动：连接挂死会把 Do 本身阻塞住；计数器复用
	// 进度 done——停滞与进度条看到的是同一份字节数。
	prog.done.Store(0)
	prog.total.Store(-1)
	stopWatchdog := stallhttp.StartWatchdog(ctx, cancel, &prog.done, updateStallTimeout, updateStallCheckInterval)
	defer stopWatchdog()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("User-Agent", "freev6")
	resp, err := client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	prog.total.Store(resp.ContentLength) // -1 for chunked responses
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return 0, err
	}
	file, err := os.Create(dest)
	if err != nil {
		return 0, err
	}
	hasher := sha256.New()
	counter := &progressWriter{w: io.MultiWriter(file, hasher), n: &prog.done}
	written, copyErr := io.Copy(counter, resp.Body)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(dest)
		return 0, fmt.Errorf("download interrupted: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(dest)
		return 0, closeErr
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(sum, sha256Hex) {
		_ = os.Remove(dest)
		return 0, fmt.Errorf("sha256 mismatch (got %s)", sum)
	}
	return written, nil
}

type updateDownloadRequest struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// updateDownload starts a background download; progress is polled through
// GET /api/v1/update/progress while this request returns immediately.
func (h *helper) updateDownload(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPut {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var input updateDownloadRequest
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	dest := updateSetupDest()

	updateRun.mu.Lock()
	if updateRun.busy {
		updateRun.mu.Unlock()
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "a download is already running"})
		return
	}
	updateRun.busy = true
	updateRun.state = "downloading"
	updateRun.path = ""
	updateRun.err = ""
	updateRun.prog.done.Store(0)
	updateRun.prog.total.Store(-1)
	updateRun.mu.Unlock()

	_ = os.Remove(dest) // stale file from a previous attempt

	go func() {
		// 无全局 client 超时：每次线路尝试有自己的硬限/停滞预算
		// （downloadUpdateFileOnce），总时长被线路数天然封顶；前端轮询
		// 自带 UI 侧上限。
		client := &http.Client{}
		_, err := downloadUpdateFile(client, input.URL, input.SHA256, dest, updateAllowedHosts, &updateRun.prog)
		updateRun.mu.Lock()
		if err != nil {
			updateRun.state = "error"
			updateRun.err = err.Error()
			updateRun.busy = false
		} else {
			updateRun.state = "done"
			updateRun.path = dest
		}
		updateRun.mu.Unlock()
	}()
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true})
}

// updateProgress is polled by the frontend every 300ms during a download.
func (h *helper) updateProgress(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	updateRun.mu.Lock()
	state, path, errMsg := updateRun.state, updateRun.path, updateRun.err
	updateRun.mu.Unlock()
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok":    true,
		"state": state,
		"done":  updateRun.prog.done.Load(),
		"total": updateRun.prog.total.Load(),
		"path":  path,
		"error": errMsg,
	})
}

// ── Latest-release check (version.json — no GitHub API) ──────────────
//
// Every release publishes a version.json asset; the stable URL
// releases/latest/download/version.json redirects to the newest STABLE
// release only (prereleases excluded) and is a web/CDN endpoint, so no API
// quota is involved. The helper fetches it proxy-first (gh-proxy mirror)
// with a direct fallback and a cache-buster, so the frontend never talks to
// GitHub directly: no CORS exposure, no shared-pool403s.

const updateRepoSlug = "evansrrr/free-v6"

// updateLatestURLs is a var so tests can point it at a local server.
// 缓存穿透参数拼在每条 URL 上；镜像顺序与安装包下载一致。
var updateLatestURLs = func() []string {
	direct := fmt.Sprintf("https://github.com/%s/releases/latest/download/version.json", updateRepoSlug)
	urls := make([]string, 0, len(updateMirrorPrefixes)+1)
	for _, prefix := range updateMirrorPrefixes {
		urls = append(urls, prefix+direct)
	}
	return append(urls, direct)
}

// stableVersionPattern accepts a plain x.y.z base (any suffix allowed for
// forward compatibility; /releases/latest never returns prereleases anyway).
var stableVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+`)

type latestVersionInfo struct {
	Version  string `json:"version"`
	Tag      string `json:"tag"`
	Notes    string `json:"notes"`
	Setup    string `json:"setup"`
	Download string `json:"download"`
	SHA256   string `json:"sha256"`
}

// parseVersionJSON accepts only a well-formed artifact: numeric version +
// a download URL. Anything else (404 HTML, truncated body, mirror junk) is
// treated as "no update available".
func parseVersionJSON(data []byte) (*latestVersionInfo, error) {
	var info latestVersionInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("invalid version.json: %w", err)
	}
	info.Version = strings.TrimSpace(info.Version)
	if !stableVersionPattern.MatchString(info.Version) {
		return nil, fmt.Errorf("invalid version: %q", info.Version)
	}
	if strings.TrimSpace(info.Download) == "" {
		return nil, fmt.Errorf("version.json missing download url")
	}
	return &info, nil
}

// fetchVersionJSON GETs rawURL with the shared scheme/host guards, capped at
// 1 MiB (version.json is tiny; the cap keeps a hostile mirror honest).
func fetchVersionJSON(client *http.Client, rawURL string, allowed []string) ([]byte, error) {
	if err := validateUpdateURL(rawURL, allowed); err != nil {
		return nil, err
	}
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// updateLatest answers the frontend update check:
//
//	{ok:true, update:true, version, tag, notes, sha256, download} or
//	{ok:true, update:false} when nothing newer exists or nothing is reachable
//	(offline / private repo / mirror down — never an error, never blocking).
func (h *helper) updateLatest(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	client := &http.Client{Timeout: 8 * time.Second}
	var info *latestVersionInfo
	var lastErr error
	for _, rawURL := range updateLatestURLs() {
		// Cache-bust so the mirror's edge cache can't pin an old "latest".
		u := rawURL + "?ts=" + strconv.FormatInt(time.Now().UnixNano(), 10)
		body, err := fetchVersionJSON(client, u, updateAllowedHosts)
		if err != nil {
			lastErr = err
			continue
		}
		info, err = parseVersionJSON(body)
		if err != nil {
			lastErr = err
			continue
		}
		break
	}
	if info == nil {
		writeJSON(writer, http.StatusOK, map[string]any{
			"ok": true, "update": false, "error": errorText(lastErr),
		})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok":       true,
		"update":   true,
		"version":  info.Version,
		"tag":      info.Tag,
		"notes":    info.Notes,
		"sha256":   info.SHA256,
		"download": info.Download,
	})
}
