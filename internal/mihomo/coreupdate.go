package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// coreManifestRepoSlug 是内核发布仓库；清单随每个 release 一起发布，
	// 与 App 的 version.json 同一套思路（不打 GitHub API，不吃匿名配额）。
	coreManifestRepoSlug = "evansrrr/f6-core"
	// coreManifestCacheName 是清单缓存：拉取失败时回退，避免离线时反复重试。
	coreManifestCacheName = "core-manifest.json"
	coreManifestMaxBytes  = 64 << 10
	coreManifestTTL       = 6 * time.Hour
	coreManifestTimeout   = 6 * time.Second
)

// coreMirrorPrefixes 与内核下载、App 更新共用同一批加速线路。变量便于测试注入。
var coreMirrorPrefixes = []string{
	"https://v6.gh-proxy.org/",
	"https://v4.gh-proxy.org/",
	"https://gh-proxy.com/",
	"https://axisnow.gh-proxy.org/",
	"https://cdn.gh-proxy.org/",
}

// coreManifestFallbackTag 是引导用的固定 tag。
//
// releases/latest/download/core.json 指向"最新正式版"，而预发布版不计入
// latest——在内核还没有任何正式版之前，latest 会 404，首次安装和更新检查
// 都会失效。所以先兜底到一个确定存在的 tag，等 core-v0.2.0 这类正式版
// 发出去之后 latest 自动接管，这段兜底就只剩"多一条候选"的作用。
const coreManifestFallbackTag = "core-v0.2.0"

// coreManifestURLs 是清单的候选地址：latest 组（镜像 + 直连）→ 兜底 tag 组。
// 404 由镜像快速返回，不会把整条链拖成几十秒。
var coreManifestURLs = func() []string {
	targets := []string{
		fmt.Sprintf("https://github.com/%s/releases/latest/download/core.json", coreManifestRepoSlug),
		fmt.Sprintf("https://github.com/%s/releases/download/%s/core.json", coreManifestRepoSlug, coreManifestFallbackTag),
	}
	urls := make([]string, 0, len(targets)*(len(coreMirrorPrefixes)+1))
	for _, target := range targets {
		for _, prefix := range coreMirrorPrefixes {
			urls = append(urls, prefix+target)
		}
		urls = append(urls, target)
	}
	return urls
}

// CoreAsset 是某个平台上的一份内核发布包。
type CoreAsset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// CoreManifest 是 f6-core 发布页上的 core.json。
type CoreManifest struct {
	Version   string               `json:"version"`
	Channel   string               `json:"channel"`
	MinApp    string               `json:"minApp"`
	MaxApp    string               `json:"maxApp"`
	Notes     string               `json:"notes"`
	Platforms map[string]CoreAsset `json:"platforms"`

	// FetchedAt 只在缓存里有意义（写入时间），不参与 JSON 往返。
	FetchedAt time.Time `json:"-"`
}

// Asset 返回当前平台对应的发布包。
func (m CoreManifest) Asset() (CoreAsset, bool) {
	asset, ok := m.Platforms[CorePlatformKey()]
	if !ok || strings.TrimSpace(asset.URL) == "" || strings.TrimSpace(asset.SHA256) == "" {
		return CoreAsset{}, false
	}
	if !validCoreAssetURL(asset.URL) {
		return CoreAsset{}, false
	}
	return asset, true
}

// validCoreAssetURL pins a core.json asset URL to https + the GitHub hosts the
// release pipeline publishes to. Anything else (plain http, an arbitrary host,
// loopback) is rejected before it is ever fetched — the SHA is delivered by
// the same manifest, so the URL is the only transport anchor left. A var so
// download-flow tests can point it at a local server.
var validCoreAssetURL = func(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if parsed.Scheme != "https" {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com":
		return true
	}
	return false
}

type coreManifestCacheFile struct {
	FetchedAt time.Time    `json:"fetchedAt"`
	Manifest  CoreManifest `json:"manifest"`
}

func coreManifestCachePath(root string) string {
	return filepath.Join(root, "state", coreManifestCacheName)
}

// FetchCoreManifest 取发布清单。有新鲜缓存就直接用；否则逐镜像拉取，
// 全线路失败时回退到过期缓存（离线也能知道"当前有没有更新"这件事发生过），
// 连缓存都没有才报错。
func FetchCoreManifest(root string, force bool) (CoreManifest, error) {
	cached, cachedAt, hasCache := readCoreManifestCache(root)
	if hasCache && !force && time.Since(cachedAt) < coreManifestTTL {
		cached.FetchedAt = cachedAt
		return cached, nil
	}

	manifest, err := downloadCoreManifest()
	if err != nil {
		if hasCache {
			cached.FetchedAt = cachedAt
			return cached, nil
		}
		return CoreManifest{}, err
	}
	manifest.FetchedAt = time.Now().UTC()
	writeCoreManifestCache(root, manifest)
	return manifest, nil
}

func downloadCoreManifest() (CoreManifest, error) {
	client := &http.Client{Timeout: coreManifestTimeout}
	// 清单只有几十 KB，不值得让十几条候选各耗 6 秒：整段限死在 20 秒内，
	// 超时就当"没查到"，交给缓存或 UI 提示。
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var lastErr error
	for _, source := range coreManifestURLs() {
		if ctx.Err() != nil {
			break
		}
		// Cache-bust：镜像的边缘缓存可能把旧的 latest 钉住。
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			lastErr = err
			continue
		}
		request.Header.Set("User-Agent", "freev6")
		request.Header.Set("Cache-Control", "no-cache")
		response, err := client.Do(request)
		if err != nil {
			lastErr = err
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, coreManifestMaxBytes))
		_ = response.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if response.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("%s: HTTP %s", hostOf(source), response.Status)
			continue
		}
		manifest, parseErr := parseCoreManifest(data)
		if parseErr != nil {
			lastErr = fmt.Errorf("%s: %w", hostOf(source), parseErr)
			continue
		}
		return manifest, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的镜像地址")
	}
	return CoreManifest{}, fmt.Errorf("获取内核清单失败: %w", lastErr)
}

// parseCoreManifest 只做必要校验：版本可解析、有一个带 URL+SHA256 的平台包。
// 其余字段（notes/minApp/channel）留给上层按需使用。
func parseCoreManifest(data []byte) (CoreManifest, error) {
	var manifest CoreManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return CoreManifest{}, fmt.Errorf("invalid core.json: %w", err)
	}
	if !ParseCoreVersion(manifest.Version).Valid() || !coreVersionPattern.MatchString(manifest.Version) {
		return CoreManifest{}, fmt.Errorf("core.json 版本非法: %q", manifest.Version)
	}
	if len(manifest.Platforms) == 0 {
		return CoreManifest{}, fmt.Errorf("core.json 缺少 platforms")
	}
	return manifest, nil
}

// coreVersionPattern pins managed core versions to the exact tag shape the
// release pipeline emits (core-v0.1.0-rc.2, core-v0.3.0-beta.1, core-v0.2.0).
// The version is later interpolated into a file name under cores/, so anything
// outside this shape (e.g. core-v0.1.0-../../../x) is rejected at parse time
// before it can escape the directory.
var coreVersionPattern = regexp.MustCompile(`^core-v\d+\.\d+\.\d+(-[0-9A-Za-z.\-]{1,32})?(\+[0-9A-Za-z.\-]+)?$`)

func readCoreManifestCache(root string) (CoreManifest, time.Time, bool) {
	data, err := os.ReadFile(coreManifestCachePath(root))
	if err != nil {
		return CoreManifest{}, time.Time{}, false
	}
	var cache coreManifestCacheFile
	if err := json.Unmarshal(data, &cache); err != nil {
		return CoreManifest{}, time.Time{}, false
	}
	if !ParseCoreVersion(cache.Manifest.Version).Valid() {
		return CoreManifest{}, time.Time{}, false
	}
	return cache.Manifest, cache.FetchedAt, true
}

func writeCoreManifestCache(root string, manifest CoreManifest) {
	path := coreManifestCachePath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	data, err := json.MarshalIndent(coreManifestCacheFile{FetchedAt: time.Now().UTC(), Manifest: manifest}, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, append(data, '\n'), 0o600)
}

// UpdateCheck 是一次"有没有新内核"的判定结果。
type UpdateCheck struct {
	Installed string
	Latest    string
	Available bool
	Notes     string
	// MinApp / MaxApp 是这版内核兼容的客户端区间：内核可能要求更新的客户端
	// （砍了协议、换了 TUN 栈），也可能只验证到某个客户端版本为止。区间外
	// 一律不提示更新——装上就是"能启动但没网"。
	MinApp     string
	MaxApp     string
	FetchedAt  time.Time
	DownloadMB int64
	// Legacy 表示当前可用内核是老版本遗留的上游 mihomo（尚未换成 f6-core）。
	Legacy bool
}

// CheckCoreUpdate 比对本机内核与发布清单。网络问题不算致命：调用方把 Error
// 交给 UI 决定是否提示，不阻塞启动。
func CheckCoreUpdate(root string, force bool) (UpdateCheck, error) {
	store := NewCoreStore(root)
	check := UpdateCheck{}
	if core, err := store.Active(); err == nil {
		check.Installed = core.Version
		check.Legacy = !core.Managed
	}
	manifest, err := FetchCoreManifest(root, force)
	if err != nil {
		return check, err
	}
	check.Latest = manifest.Version
	check.Notes = manifest.Notes
	check.MinApp = manifest.MinApp
	check.MaxApp = manifest.MaxApp
	check.FetchedAt = manifest.FetchedAt
	if asset, ok := manifest.Asset(); ok && asset.Size > 0 {
		check.DownloadMB = asset.Size >> 20
	}
	installed := ParseCoreVersion(check.Installed)
	latest := ParseCoreVersion(manifest.Version)
	check.Available = latest.Valid() && installed.Compare(latest) < 0 && !store.IsBad(manifest.Version)
	return check, nil
}

// UpdateResult 是一次更新的产物。
type UpdateResult struct {
	Version  string
	Path     string
	SHA256   string
	Previous string
	// Warning 记录非致命的检查结果（例如 `-t` 配置自检没过，仍然安装）。
	Warning string
	Removed []string
}

// ErrCoreUpToDate 表示清单版本不比本机新（用户主动点"更新"时用 force 绕过）。
var ErrCoreUpToDate = errors.New("已是最新内核")

// verifyCore 是安装前的自检钩子。测试里替换成不做真实进程调用的实现，
// 生产路径永远是 VerifyCore。
var verifyCore = VerifyCore

// ApplyUpdate 执行完整更新流水线：
//
//	下载 → SHA256 → 解压 → `-v` 自检 → `-t` 配置自检（尽力）→ 切指针 → 删旧
//
// 任何一步失败都不会动现有内核：指针只在自检通过之后才切换，旧版本退为
// previous 保留，等新版本真的跑起来（helper 的健康检查）之后才被 GC。
func (s *CoreStore) ApplyUpdate(ctx context.Context, manifest CoreManifest, force bool, progress progressSink) (UpdateResult, error) {
	version := manifest.Version
	if !ParseCoreVersion(version).Valid() {
		return UpdateResult{}, fmt.Errorf("清单版本非法: %q", version)
	}
	asset, ok := manifest.Asset()
	if !ok {
		return UpdateResult{}, fmt.Errorf("清单里没有 %s 的内核包", CorePlatformKey())
	}

	current, currentErr := s.Active()
	if !force && currentErr == nil {
		if ParseCoreVersion(version).Compare(current.Parsed()) <= 0 {
			return UpdateResult{}, ErrCoreUpToDate
		}
	}
	if !force && s.IsBad(version) {
		return UpdateResult{}, fmt.Errorf("内核 %s 此前自检失败过，已跳过（可手动强制更新）", version)
	}

	urls := coreAssetURLs(asset.URL)
	archivePath, digest, err := downloadAsset(ctx, urls, asset.SHA256, progress)
	if err != nil {
		return UpdateResult{}, err
	}
	defer os.Remove(archivePath)

	// 解压 + 自检都放在 cores/ 里做，而不是 %TEMP%：一是免去一次 38MB 的
	// 跨目录拷贝，二是"从临时目录跑起来的可执行文件"本身就是杀软启发式的
	// 高危特征，装在程序自己的目录下能少踩一次误杀。
	if err := os.MkdirAll(s.CoresDir(), 0o700); err != nil {
		return UpdateResult{}, err
	}
	executable, err := os.CreateTemp(s.CoresDir(), ".incoming-*.exe")
	if err != nil {
		return UpdateResult{}, err
	}
	executablePath := executable.Name()
	_ = executable.Close()
	defer os.Remove(executablePath)
	if err := extractArchive(archivePath, executablePath); err != nil {
		return UpdateResult{}, err
	}
	// 自检一：二进制能跑，且自报版本与清单一致（防错包/截断/挂羊头）。
	if err := verifyCore(executablePath, version); err != nil {
		return UpdateResult{}, err
	}
	result := UpdateResult{Version: version, SHA256: digest}
	// 自检二：用户的真实配置能被新内核接受（尽力而为，不阻断安装——
	// 配置每次启动都会重新渲染，且启动失败还有自动回滚兜底）。
	if warning := s.checkConfig(executablePath); warning != "" {
		result.Warning = warning
	}

	// 只有受管内核才有回滚位；老用户的上游内核马上会被删掉，报成 previous
	// 只会让 UI 显示一个已经不存在的版本。
	if currentErr == nil && current.Managed {
		result.Previous = current.Version
	}
	core, err := s.Install(executablePath, version)
	if err != nil {
		return UpdateResult{}, err
	}
	result.Path = core.Path
	// 新内核已经落盘并切了指针，此时才轮到清理：
	// 遗留的 58MB 上游内核 + 比 previous 更老的版本。
	result.Removed = append(result.Removed, s.DeleteLegacy()...)
	result.Removed = append(result.Removed, s.GC()...)
	return result, nil
}

// coreConfigTestBudget 是配置自检的采纳窗口。
//
// `-t` 要解析整份渲染配置（含 rule-provider、geosite），离线时会被超时拖满。
// 3 秒内跑不完就当"没有结论"，既不阻断也不记警告：更新本身已经有 `-v`
// 自检把关，配置问题还有启动失败回滚兜底。
var coreConfigTestBudget = 3 * time.Second

// checkConfig 用状态目录里现有的渲染配置跑一次 `-t`。没有配置（首次安装）
// 就跳过；3 秒内没跑完同样跳过；跑完但失败只作为警告返回，不阻断更新。
func (s *CoreStore) checkConfig(binaryPath string) string {
	stateDir := filepath.Join(s.root, "state")
	configPath := filepath.Join(stateDir, "mihomo.yaml")
	if !isRegularFile(configPath) {
		return ""
	}
	err := TestConfigWithin(binaryPath, configPath, stateDir, coreConfigTestBudget)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrConfigTestTimeout):
		return ""
	default:
		return fmt.Sprintf("配置自检未通过（仍已安装，启动失败会自动回滚）: %v", err)
	}
}

// coreAssetURLs 给清单里的直连 URL 套上镜像前缀（与 App 更新同一套改写规则）。
// 已经是镜像地址的不重复套。
func coreAssetURLs(rawURL string) []string {
	if !strings.Contains(rawURL, "://github.com/") {
		return []string{rawURL}
	}
	urls := make([]string, 0, len(coreMirrorPrefixes)+1)
	for _, prefix := range coreMirrorPrefixes {
		urls = append(urls, prefix+rawURL)
	}
	return append(urls, rawURL)
}
