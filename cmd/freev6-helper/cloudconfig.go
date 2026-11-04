package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yourname/freev6/internal/mihomo"
)

// 备用配置（cloud profile）：校内服务端下发的出口节点。
//
// 与链式代理共用同一套"拉取 → 校验 → 落盘 → 启动时合并"管线，区别只有两点：
//  1. 校园节点是出口本身，不套 WARP 接入点，因此启动时也不再要求 WARP 凭据；
//  2. 入口地址可以是内置常量，学生零输入（见下方门禁说明）。
const (
	// maxCloudProfileBytes 与订阅导入同档：正常节点表 <100KB，2MB 挡住
	// 失控下载与内存空转。
	maxCloudProfileBytes = 2 << 20
	cloudFetchTimeout    = 20 * time.Second
)

// 入口地址由「校园网登录页地址」派生（见 internal/mihomo/endpoint.go），
// 仓库里不出现维护者域名的完整入口，避免被爬虫扫到、以及 fork 之后自动
// 继承一个能用的地址。
//
// 但派生不是门禁：本项目是 MIT 开源，规则与输入（README 里都写着）都是
// 公开的，输出天然可复现。判断权必须留在服务端——Worker 按请求源 IP 段
// 决定是否下发（详见 docs/cloud-profile.md）。客户端只负责"问"，问到就
// 存，问不到就明确告诉用户"请连校园网"。

func (h *helper) cloudProfilePath() string {
	return filepath.Join(h.root, "config", "cloud-profile.yaml")
}

// cloudProfileUnlocked 读取「公告里维护者开关」的最近一次结果。
//
// 语义与 notice.json 的 cloudProfile 一致：显式 true 才开放；公告拉不到、
// 没写这个字段、缓存超过 7 天，一律锁定（fail-closed）。
//
// 读缓存而不是当场联网：启动免流不该再多一次网络往返，且离线时缓存仍能
// 反映维护者最近一次的决定。公告本身由前端启动时的 /api/v1/notice 拉取
// 并写入同一个缓存文件。
func (h *helper) cloudProfileUnlocked(now time.Time) bool {
	cache, ok := h.loadNoticeCache(now)
	if !ok {
		return false
	}
	return cloudProfileAllowed(cache.CloudProfile)
}

// cloudConfig serves GET/PUT/DELETE /api/v1/cloud-config. Every method sits
// behind withCORS(guardLocalOnly(requireAuth(...))) registered in main().
func (h *helper) cloudConfig(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		h.cloudStatus(writer)
	case http.MethodPut:
		h.cloudImport(writer, request)
	case http.MethodDelete:
		h.cloudClear(writer)
	default:
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *helper) cloudStatus(writer http.ResponseWriter) {
	current, err := h.loadSettings()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	response := map[string]any{
		"enabled":    current.CloudEnabled,
		"count":      0,
		"names":      []string{},
		"sourceUrl":  current.CloudSourceURL,
		"updatedAt":  current.CloudUpdatedAt,
		"ignored":    []string{},
		"portalHost": current.PortalHost,
		// 公告开关：前端据此禁用「获取」与开关，而不是点下去才报错。
		"locked": !h.cloudProfileUnlocked(time.Now()),
	}
	// 只回显打码后的入口：完整地址一旦出现在界面上，截图/日志就会把它
	// 带出去，白白抵消派生的意义。
	if current.PortalHost != "" {
		if derived, deriveErr := mihomo.CloudProfileURL(current.PortalHost); deriveErr == nil {
			response["entryMasked"] = maskCloudEntry(derived)
		}
	}
	data, readErr := os.ReadFile(h.cloudProfilePath())
	switch {
	case readErr == nil:
		profile, parseErr := mihomo.ParseCloudProfile(data)
		if parseErr != nil {
			// 如实暴露坏内容（文件被手改/截断），不假装 0 个节点。
			response["error"] = parseErr.Error()
			break
		}
		names := make([]string, 0, len(profile.Nodes))
		for _, node := range profile.Nodes {
			names = append(names, node.Name)
		}
		response["count"] = len(profile.Nodes)
		response["names"] = names
		response["ignored"] = profile.Ignored
	case errors.Is(readErr, os.ErrNotExist):
		// 尚未获取：计数保持 0。
	default:
		response["error"] = readErr.Error()
	}
	writeJSON(writer, http.StatusOK, response)
}

func (h *helper) cloudImport(writer http.ResponseWriter, request *http.Request) {
	// 总开关：维护者在公告里关闭 / 公告没拿到 → 连拉取都不允许，
	// 免得学生反复点「获取」只看到网络错误。
	if !h.cloudProfileUnlocked(time.Now()) {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "备用配置当前未开放（维护者已在公告中关闭或未获取到公告）", "code": "cloud-locked"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxCloudProfileBytes+1))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if len(body) > maxCloudProfileBytes {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "内容超过 2MB 上限"})
		return
	}
	var input struct {
		Content    string `json:"content"`
		URL        string `json:"url"`
		PortalHost string `json:"portalHost"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	stored, loadErr := h.loadSettings()
	if loadErr != nil {
		stored = defaultSettings()
	}
	// 门户地址：本次请求填了就用新的，没填就沿用上次的（刷新不必重输）。
	portalHost := strings.TrimSpace(input.PortalHost)
	if portalHost == "" {
		portalHost = strings.TrimSpace(stored.PortalHost)
	}
	if portalHost != "" {
		normalized, normErr := mihomo.NormalizePortalHost(portalHost)
		if normErr != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": normErr.Error()})
			return
		}
		portalHost = normalized
	}
	var content []byte
	sourceType := ""
	sourceURL := ""
	switch {
	case strings.TrimSpace(input.Content) != "":
		content = []byte(input.Content)
		sourceType = "file"
	default:
		rawURL := strings.TrimSpace(input.URL)
		if rawURL == "" {
			if portalHost == "" {
				writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "请先填写校园网登录页地址"})
				return
			}
			derived, deriveErr := mihomo.CloudProfileURL(portalHost)
			if deriveErr != nil {
				writeJSON(writer, http.StatusBadRequest, map[string]string{"error": deriveErr.Error()})
				return
			}
			rawURL = derived
		}
		parsed, parseErr := url.Parse(rawURL)
		if parseErr != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "入口地址必须是 http(s) URL"})
			return
		}
		// 先拉取后落盘：失败的刷新绝不能覆盖上一份可用快照（服务端维护
		// 中/网络抖动时，已缓存配置仍要能启动）。
		fetched, fetchErr := fetchCloudProfile(parsed.String())
		if fetchErr != nil {
			writeJSON(writer, http.StatusBadGateway, map[string]string{"error": fetchErr.Error(), "code": cloudErrorCode(fetchErr)})
			return
		}
		content = fetched
		sourceType = "url"
		// 派生出的地址不落盘：需要时由门户地址重新派生即可，settings.json
		// 里少一处完整入口，就少一处被顺手贴出去的地方。只有用户显式
		// 指定了自定义地址时才记录。
		if strings.TrimSpace(input.URL) != "" {
			sourceURL = parsed.String()
		}
	}
	// Tolerate a UTF-8 BOM pasted from Windows editors.
	content = bytes.TrimPrefix(content, []byte("\xEF\xBB\xBF"))
	profile, parseErr := mihomo.ParseCloudProfile(content)
	if parseErr != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": parseErr.Error()})
		return
	}
	if stored.ChainEnabled {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "链式代理已启用，两种模式改的是同一个出口，请先关闭链式代理"})
		return
	}
	if err := os.MkdirAll(filepath.Dir(h.cloudProfilePath()), 0o700); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := os.WriteFile(h.cloudProfilePath(), content, 0o600); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	stored.CloudEnabled = true
	if portalHost != "" {
		stored.PortalHost = portalHost
	}
	if sourceURL != "" {
		stored.CloudSourceURL = sourceURL
	}
	stored.CloudUpdatedAt = time.Now().Format(time.RFC3339)
	if err := h.saveSettings(stored); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	names := make([]string, 0, len(profile.Nodes))
	for _, node := range profile.Nodes {
		names = append(names, node.Name)
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok":         true,
		"count":      len(profile.Nodes),
		"names":      names,
		"ignored":    profile.Ignored,
		"sourceType": sourceType,
		"sourceUrl":  sourceURL,
		"updatedAt":  stored.CloudUpdatedAt,
	})
}

func (h *helper) cloudClear(writer http.ResponseWriter) {
	if err := os.Remove(h.cloudProfilePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	stored, loadErr := h.loadSettings()
	if loadErr != nil {
		stored = defaultSettings()
	}
	stored.CloudEnabled = false
	stored.CloudSourceURL = ""
	stored.CloudUpdatedAt = ""
	if err := h.saveSettings(stored); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
}

// cloudGateError 是服务端门禁的拒绝（HTTP 403）。它和学生能自愈的情况
// （没连校园网）是同一件事，所以单独给一个 code 让 UI 说人话，而不是把
// "HTTP 403" 直接甩到 toast 上。
type cloudGateError struct{ status string }

func (e *cloudGateError) Error() string {
	return "备用配置仅对校园网开放（" + e.status + "），请连接校园网后重试"
}

// cloudErrorCode 把错误映射成前端可判别的 code。
func cloudErrorCode(err error) string {
	var gate *cloudGateError
	if errors.As(err, &gate) {
		return "campus-only"
	}
	return "fetch-failed"
}

// fetchCloudProfile downloads the backup profile. Only plain http(s) reaches
// here; the response is size-capped like a file import.
//
// 错误文案一律不带入口地址：这些字符串会原样出现在 toast / 弹窗里，而
// 弹窗是"会被截图发群里"的东西。完整 err（含 URL）只写 stderr 留作排障。
func fetchCloudProfile(rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cloudFetchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-config: bad request: %v\n", err)
		return nil, &cloudFetchError{reason: "入口地址无效"}
	}
	request.Header.Set("User-Agent", "FreeV6")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-config: fetch failed: %v\n", err)
		return nil, &cloudFetchError{reason: describeFetchFailure(err)}
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusForbidden, response.StatusCode == http.StatusUnauthorized:
		return nil, &cloudGateError{status: response.Status}
	case response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices:
		return nil, fmt.Errorf("获取备用配置失败：服务端返回 HTTP %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxCloudProfileBytes+1))
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-config: read body failed: %v\n", err)
		return nil, &cloudFetchError{reason: "响应读取中断"}
	}
	if len(data) > maxCloudProfileBytes {
		return nil, fmt.Errorf("备用配置超过 2MB 上限")
	}
	return data, nil
}

// cloudFetchError 是"网络层失败"的脱敏错误：只给原因类别，不给地址。
type cloudFetchError struct{ reason string }

func (e *cloudFetchError) Error() string {
	return "获取备用配置失败：" + e.reason
}

// describeFetchFailure 把 net/http 的错误归成人类可读的类别。
// 绝不能直接 %w 透出：Go 的错误里带着完整 URL（Get "https://<slug>.…"）。
func describeFetchFailure(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "请求超时，请稍后重试"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "请求超时，请稍后重试"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "域名解析失败，请检查网络或 DNS"
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "connection refused"):
		return "连接被拒绝"
	case strings.Contains(text, "certificate"):
		return "证书校验失败"
	case strings.Contains(text, "no route to host"), strings.Contains(text, "network is unreachable"):
		return "网络不可达"
	}
	return "网络请求失败，请稍后重试"
}

// maskCloudEntry 只保留子域名前 4 位用于界面回显：完整入口不该出现在
// 能被截图/贴群的地方。
func maskCloudEntry(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return ""
	}
	labels := strings.SplitN(parsed.Host, ".", 2)
	if len(labels) != 2 {
		return ""
	}
	slug := labels[0]
	if len(slug) > 4 {
		slug = slug[:4]
	}
	return slug + "…" + labels[1] + parsed.Path
}
