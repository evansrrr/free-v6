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

	"github.com/yourname/freev6/internal/appupdate"
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
	"raw.githubusercontent.com",
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

// updateMaxBytes caps the installer download so a hostile/broken mirror cannot
// fill the disk within the per-mirror hard budget.
const updateMaxBytes = 300 << 20

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

// validSHA256 报告字符串是否是 64 位十六进制摘要（大小写均可）。
func validSHA256(value string) bool {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) != 64 {
		return false
	}
	_, err := hex.DecodeString(trimmed)
	return err == nil
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
	written, copyErr := io.Copy(counter, io.LimitReader(resp.Body, updateMaxBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(dest)
		return 0, fmt.Errorf("download interrupted: %w", copyErr)
	}
	if written > updateMaxBytes {
		_ = os.Remove(dest)
		return 0, fmt.Errorf("download exceeds %d bytes", updateMaxBytes)
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
	// 入参本身能判的错误同步返回。旧实现先回 200、再在后台 goroutine 里失败，
	// 前端只能靠轮询 progress 发现——用户先看到进度条，再看到红字。
	if !validSHA256(input.SHA256) {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "缺少有效的 sha256，已拒绝下载"})
		return
	}
	if err := validateUpdateURL(input.URL, updateAllowedHosts); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "下载地址不在允许范围: " + err.Error()})
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
		// The URL and SHA come from the caller; before downloading an
		// executable that will be run with admin rights, require the SHA to
		// match what the official release channel actually published. The host
		// allowlist alone only pins the host, not the artifact.
		if err := verifyUpdateSource(input.URL, input.SHA256); err != nil {
			updateRun.mu.Lock()
			updateRun.state = "error"
			updateRun.err = err.Error()
			updateRun.busy = false
			updateRun.mu.Unlock()
			return
		}
		// 无全局 client 超时：每次线路尝试有自己的硬限/停滞预算
		// （downloadUpdateFileOnce），总时长被线路数天然封顶；前端轮询
		// 自带 UI 侧上限。重定向必须仍落在白名单主机上，否则一次 302 就能
		// 绕过 https + host 约束。
		client := &http.Client{
			CheckRedirect: redirectGuard(updateAllowedHosts),
		}
		_, err := downloadUpdateFile(client, input.URL, input.SHA256, dest, updateAllowedHosts, &updateRun.prog)
		updateRun.mu.Lock()
		if err != nil {
			updateRun.state = "error"
			updateRun.err = err.Error()
		} else {
			updateRun.state = "done"
			updateRun.path = dest
		}
		updateRun.busy = false
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

// 更新检查的耗时预算。
//
// 旧实现按"候选串行 + 每条 8s"跑：6 条线路全挂时检查更新要等 48s，beta 客户端
// 两通道再翻倍到 96s，期间 UI 的更新徽标一直不出现。改成候选并发后，最坏等待
// 只由这两个预算决定，与线路数量无关；变量便于测试压缩时长。
var (
	updateCheckBudget  = 15 * time.Second // "检查更新"单通道总预算
	updateSourceBudget = 10 * time.Second // 下载前源校验（冷缓存）总预算
)

const (
	// updateManifestFetchTimeout 是单个候选请求（清单或签名）的硬上限。
	updateManifestFetchTimeout = 8 * time.Second
	// updateManifestMaxBytes 挡住恶意镜像用超大响应拖死解析。
	updateManifestMaxBytes = 1 << 20
	// updateSigMaxBytes 是签名信封的上限：v1.<id>.<88 base64> 量级，4 KiB 足够。
	updateSigMaxBytes = 4 << 10
)

// verifyManifestSignature 是验签入口，变量便于测试注入临时密钥。
// 生产路径永远是 appupdate.Verify（内置公钥 + 失败关闭）。
var verifyManifestSignature = appupdate.Verify

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

// updateBetaLatestURLs points at the rolling "beta-channel" release: a
// permanent prerelease whose version.json asset is replaced on every beta
// publish. GitHub's /releases/latest ignores prereleases, so this is the only
// API-free way for a beta client to discover a newer beta -- the API itself is
// 403 through the mirrors and rate-limited anyway.
var updateBetaLatestURLs = func() []string {
	direct := fmt.Sprintf("https://github.com/%s/releases/download/beta-channel/version.json", updateRepoSlug)
	urls := make([]string, 0, len(updateMirrorPrefixes)+1)
	for _, prefix := range updateMirrorPrefixes {
		urls = append(urls, prefix+direct)
	}
	return append(urls, direct)
}

// stableVersionPattern accepts a plain x.y.z base and an optional prerelease
// suffix: the channel decision (beta builds may see betas, stable builds may
// not) is made by the client, which knows which side of the fence it is on.
var stableVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`)

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

// maxUpdateRedirects 是清单/签名/安装包允许的最大跳数。GitHub 的 release
// 资产会跳到 objects.githubusercontent.com，两跳足够；给 5 是留余量。
const maxUpdateRedirects = 5

// redirectGuard 把"https + 白名单主机"这条传输约束延伸到整条重定向链。
//
// 没有它，一次 302 就能把请求引向任意主机（包括 http 降级），而 helper 是以
// 管理员身份运行的：清单本身有签名，但"只允许访问白名单"是独立的一层约束，
// 不能因为内容可信就放弃。下载路径与清单路径共用这一个守卫，避免两处漂移。
func redirectGuard(allowed []string) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, via []*http.Request) error {
		if len(via) >= maxUpdateRedirects {
			return fmt.Errorf("too many redirects")
		}
		return validateUpdateURL(request.URL.String(), allowed)
	}
}

// fetchBody 是清单与签名共用的取字节入口：先过 scheme/host 白名单，
// 再带着 ctx 发请求，并把响应体限制在 max 字节以内。
func fetchBody(ctx context.Context, client *http.Client, rawURL string, allowed []string, max int64) ([]byte, error) {
	if err := validateUpdateURL(rawURL, allowed); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "freev6")
	// 复制 client 而不是改它：调用方的 client 在多个候选之间共享，测试也会复用。
	// 只覆盖 CheckRedirect，Transport/Timeout 原样继承。
	guarded := *client
	guarded.CheckRedirect = redirectGuard(allowed)
	response, err := guarded.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, max))
}

// manifestAndSignatureURLs 从同一条候选地址派生出"清单 + 签名"这一对 URL。
//
// 成对是硬要求：清单来自镜像 A、签名来自镜像 B 时必须失败，否则被攻破的
// 镜像可以拿官方签名去背书自己的伪造清单。两者共享同一个缓存穿透参数。
func manifestAndSignatureURLs(rawURL string) (manifestURL, signatureURL string, err error) {
	parsed, parseErr := url.Parse(rawURL)
	if parseErr != nil || parsed.Host == "" {
		return "", "", fmt.Errorf("invalid update manifest url")
	}
	stamp := strconv.FormatInt(time.Now().UnixNano(), 10)

	manifest := *parsed
	query := manifest.Query()
	query.Set("ts", stamp)
	manifest.RawQuery = query.Encode()

	signature := *parsed
	signature.Path += ".sig"
	signedQuery := signature.Query()
	signedQuery.Set("ts", stamp)
	signature.RawQuery = signedQuery.Encode()

	return manifest.String(), signature.String(), nil
}

// fetchOneVerifiedManifest 是单个候选的完整判定：成对取清单与签名（并发），
// 先验签、再解析。任何一步失败都算这个候选不可用。
func fetchOneVerifiedManifest(ctx context.Context, client *http.Client, rawURL string) (*latestVersionInfo, error) {
	manifestURL, signatureURL, err := manifestAndSignatureURLs(rawURL)
	if err != nil {
		return nil, err
	}
	var (
		body, envelope []byte
		bodyErr        error
		signatureErr   error
		wg             sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		body, bodyErr = fetchBody(ctx, client, manifestURL, updateAllowedHosts, updateManifestMaxBytes)
	}()
	go func() {
		defer wg.Done()
		envelope, signatureErr = fetchBody(ctx, client, signatureURL, updateAllowedHosts, updateSigMaxBytes)
	}()
	wg.Wait()

	if bodyErr != nil {
		return nil, bodyErr
	}
	if signatureErr != nil {
		// 缺签名不是"网络问题"而是"这条候选不可信"，但仍然让其他候选继续。
		return nil, fmt.Errorf("missing signature: %w", signatureErr)
	}
	if err := verifyManifestSignature(body, envelope); err != nil {
		return nil, err
	}
	return parseVersionJSON(body)
}

// fetchVerifiedManifest 并发尝试所有候选，返回首个"验签通过且可解析"的清单。
//
// 并发是这次的性能修复本体：串行版最坏 = 候选数 × 单请求超时（6 × 8s = 48s，
// beta 两通道 96s），单条线路挂死就会拖住整次检查。并发后最坏等待收敛到
// budget，与候选数量无关；首个成功立即取消其余请求。
func fetchVerifiedManifest(client *http.Client, urls []string, budget time.Duration) (*latestVersionInfo, error) {
	if len(urls) == 0 {
		return nil, fmt.Errorf("no update source configured")
	}
	if budget <= 0 {
		return nil, context.DeadlineExceeded
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	type candidateResult struct {
		info *latestVersionInfo
		err  error
	}
	results := make(chan candidateResult, len(urls))
	for _, rawURL := range urls {
		go func(u string) {
			info, err := fetchOneVerifiedManifest(ctx, client, u)
			results <- candidateResult{info: info, err: err}
		}(rawURL)
	}

	var lastErr error
	for range urls {
		select {
		case <-ctx.Done():
			if lastErr == nil {
				lastErr = ctx.Err()
			}
			return nil, lastErr
		case result := <-results:
			if result.err == nil {
				cancel() // 其余候选的请求随 ctx 一起放弃
				return result.info, nil
			}
			lastErr = result.err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no candidate returned a usable manifest")
	}
	return nil, lastErr
}

// updateManifestCache 缓存**验签通过**的官方清单，供下载前的源校验复用。
//
// 只有 appupdate.Verify 通过的清单才会写进来，所以 at 本身就是"验证时间"；
// 未验证的内容永远不会进入缓存（这就是为什么不需要额外的 verified 标记）。
var updateManifestCache = struct {
	sync.Mutex
	stable *latestVersionInfo
	beta   *latestVersionInfo
	at     time.Time
}{}

const updateManifestCacheTTL = 60 * time.Second

func cachedManifests() (stable, beta *latestVersionInfo, ok bool) {
	updateManifestCache.Lock()
	defer updateManifestCache.Unlock()
	if updateManifestCache.at.IsZero() || time.Since(updateManifestCache.at) > updateManifestCacheTTL {
		return nil, nil, false
	}
	stable, beta = updateManifestCache.stable, updateManifestCache.beta
	return stable, beta, stable != nil || beta != nil
}

// storeManifests 只接受验签通过的结果；nil 表示该通道这次不可用，保留旧值。
func storeManifests(stable, beta *latestVersionInfo) {
	if stable == nil && beta == nil {
		return
	}
	updateManifestCache.Lock()
	defer updateManifestCache.Unlock()
	if stable != nil {
		updateManifestCache.stable = stable
	}
	if beta != nil {
		updateManifestCache.beta = beta
	}
	updateManifestCache.at = time.Now()
}

// resetManifestCache 只给测试用（模拟 helper 刚启动、缓存为空）。
func resetManifestCache() {
	updateManifestCache.Lock()
	defer updateManifestCache.Unlock()
	updateManifestCache.stable, updateManifestCache.beta, updateManifestCache.at = nil, nil, time.Time{}
}

func manifestHasDigest(info *latestVersionInfo, sum string) bool {
	return info != nil && strings.EqualFold(info.SHA256, sum)
}

// verifyUpdateSource requires the caller's SHA256 to match the official
// stable or beta manifest. The frontend may rewrite the download URL through a
// mirror prefix, so only the digest (and not the raw URL) is compared — a
// matching SHA is the integrity anchor, while the host allowlist already pins
// the transport. Rejects with a clear message when the manifests are
// unreachable or nothing matches. A var so tests can stub it out.
var verifyUpdateSource = func(rawURL, sha string) error {
	sum := strings.ToLower(strings.TrimSpace(sha))
	if len(sum) != 64 {
		return fmt.Errorf("更新源校验失败：sha256 无效")
	}
	stable, beta, cached := cachedManifests()
	if !cached {
		// 冷缓存（helper 刚重启，或用户从旧页面直接点更新）：只给一份总预算。
		// stable 命中就不再请求 beta —— 旧实现无条件串行拉两路，beta 客户端
		// 冷缓存时最坏要等 96s 才开始下载。
		deadline := time.Now().Add(updateSourceBudget)
		client := &http.Client{Timeout: updateManifestFetchTimeout}
		stable, _ = fetchVerifiedManifest(client, updateLatestURLs(), time.Until(deadline))
		if !manifestHasDigest(stable, sum) {
			beta, _ = fetchVerifiedManifest(client, updateBetaLatestURLs(), time.Until(deadline))
		}
		storeManifests(stable, beta)
	}
	for _, info := range []*latestVersionInfo{stable, beta} {
		if manifestHasDigest(info, sum) {
			return nil
		}
	}
	return fmt.Errorf("更新地址与官方清单不匹配，请重新检查更新")
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
	client := &http.Client{Timeout: updateManifestFetchTimeout}
	// 测试版客户端要多查一路：stable 通道永远看不到预发布，beta-channel 是唯一
	// 入口。两路都返回，由前端按"自己是哪个通道"挑（Go 侧不做版本比较）。
	// 两路**并发**：串行会让 beta 客户端的最坏等待翻倍。
	wantBeta := request.URL.Query().Get("channel") == "beta"
	type channelResult struct {
		beta bool
		info *latestVersionInfo
		err  error
	}
	channels := 1
	if wantBeta {
		channels = 2
	}
	results := make(chan channelResult, channels)
	go func() {
		info, err := fetchVerifiedManifest(client, updateLatestURLs(), updateCheckBudget)
		results <- channelResult{info: info, err: err}
	}()
	if wantBeta {
		go func() {
			info, err := fetchVerifiedManifest(client, updateBetaLatestURLs(), updateCheckBudget)
			results <- channelResult{beta: true, info: info, err: err}
		}()
	}
	var info, betaInfo *latestVersionInfo
	var stableErr, betaErr error
	for i := 0; i < channels; i++ {
		result := <-results
		if result.beta {
			betaInfo, betaErr = result.info, result.err
		} else {
			info, stableErr = result.info, result.err
		}
	}
	// 验签通过的清单缓存起来：用户随后点"立即更新"时，源校验直接命中缓存，
	// 不再重拉两路（旧实现每次都重拉，冷缓存最坏 96s 才开始下载）。
	storeManifests(info, betaInfo)
	if info == nil && betaInfo == nil {
		writeJSON(writer, http.StatusOK, map[string]any{
			"ok": true, "update": false, "error": errorText(stableErr),
		})
		return
	}
	if info == nil {
		// stable 拉不到（离线/镜像挂）但 beta 通道通了：仍然回一个空 stable，
		// 让前端按 beta 判断，而不是整体判成"没有更新"。
		info = &latestVersionInfo{}
	}
	payload := map[string]any{
		"ok":       true,
		"update":   true,
		"version":  info.Version,
		"tag":      info.Tag,
		"notes":    info.Notes,
		"sha256":   info.SHA256,
		"download": info.Download,
	}
	// beta 通道候选：只对测试版客户端返回，稳定版客户端连这次请求都不会发。
	// 两路都给出去，由前端用完整的 semver 比较决定装哪个。
	if betaInfo != nil {
		payload["beta"] = map[string]any{
			"version":  betaInfo.Version,
			"tag":      betaInfo.Tag,
			"notes":    betaInfo.Notes,
			"sha256":   betaInfo.SHA256,
			"download": betaInfo.Download,
		}
	} else if betaErr != nil {
		payload["betaError"] = betaErr.Error()
	}
	writeJSON(writer, http.StatusOK, payload)
}
