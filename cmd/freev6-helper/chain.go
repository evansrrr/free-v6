package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yourname/freev6/internal/mihomo"
)

// 订阅/文件内容硬上限：机场订阅通常 <100KB，2MB 足够覆盖超长列表，
// 同时挡住失控的下载与内存空转。
const maxChainProxyBytes = 2 << 20

// 订阅拉取超时（仅导入/手动刷新触发，启动免流不联网拉订阅）。
const chainFetchTimeout = 30 * time.Second

func (h *helper) chainPath() string {
	return filepath.Join(h.root, "config", "chain-proxy.yaml")
}

// chainProxy serves GET/PUT/DELETE /api/v1/chain-proxy: status listing
// (names/count only — never credential fields), import from a file body or
// a server-side subscription fetch, and removal. Every method sits behind
// withCORS(guardLocalOnly(requireAuth(...))) registered in main().
func (h *helper) chainProxy(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		h.chainStatus(writer, request)
	case http.MethodPut:
		h.chainImport(writer, request)
	case http.MethodDelete:
		h.chainClear(writer, request)
	default:
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *helper) chainStatus(writer http.ResponseWriter, request *http.Request) {
	current, err := h.loadSettings()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	response := map[string]any{
		"enabled":    current.ChainEnabled,
		"sourceType": current.ChainSourceType,
		"sourceUrl":  current.ChainSourceURL,
		"count":      0,
		"names":      []string{},
	}
	data, readErr := os.ReadFile(h.chainPath())
	switch {
	case readErr == nil:
		chains, parseErr := mihomo.ParseChainProxies(data)
		if parseErr != nil {
			// 保留 enabled 如实反映设置，但让 UI 看见内容坏了（文件被
			// 手改/截断），而不是假装 0 个节点。
			response["error"] = parseErr.Error()
		} else {
			names := make([]string, 0, len(chains))
			for _, chain := range chains {
				names = append(names, chain.Name)
			}
			response["count"] = len(chains)
			response["names"] = names
		}
	case errors.Is(readErr, os.ErrNotExist):
		// 尚未导入：计数保持 0。
	default:
		response["error"] = readErr.Error()
	}
	writeJSON(writer, http.StatusOK, response)
}

func (h *helper) chainImport(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(io.LimitReader(request.Body, maxChainProxyBytes+1))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if len(body) > maxChainProxyBytes {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "导入内容超过 2MB 上限"})
		return
	}
	var input struct {
		Content string `json:"content"`
		URL     string `json:"url"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	var content []byte
	sourceType := ""
	sourceURL := ""
	switch {
	case strings.TrimSpace(input.Content) != "":
		content = []byte(input.Content)
		sourceType = "file"
	case strings.TrimSpace(input.URL) != "":
		rawURL := strings.TrimSpace(input.URL)
		parsed, parseErr := url.Parse(rawURL)
		if parseErr != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "订阅地址必须是 http(s) URL"})
			return
		}
		// Fetch BEFORE touching disk: a failed refresh must never clobber
		// the last good snapshot (订机场故障时已有缓存仍可用).
		fetched, fetchErr := fetchChainSubscription(parsed.String())
		if fetchErr != nil {
			writeJSON(writer, http.StatusBadGateway, map[string]string{"error": "拉取订阅失败: " + fetchErr.Error()})
			return
		}
		content = fetched
		sourceType = "url"
		sourceURL = parsed.String()
	default:
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "需要 content 或 url"})
		return
	}
	// Tolerate a UTF-8 BOM pasted from Windows editors.
	content = bytes.TrimPrefix(content, []byte("\xEF\xBB\xBF"))
	chains, parseErr := mihomo.ParseChainProxies(content)
	if parseErr != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": parseErr.Error()})
		return
	}
	// Parse passed → persist content, then flip the settings fields.
	//
	// 互斥检查、写文件、翻开关必须在同一把锁里：两个并发导入各自读到"对方没
	// 开"，最后会两个都开，启动时才以 400 暴露——用户看到的是"导入一次就打
	// 不开了"。锁序见 helper.configMu 的注释。
	h.configMu.Lock()
	defer h.configMu.Unlock()
	if stored, loadErr := h.loadSettings(); loadErr == nil && stored.CloudEnabled {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": errOutletConflict})
		return
	}
	// 原子写：崩溃留下的半个 YAML 会让下次启动报"解析失败"，而这里完全能避免。
	if err := writeFileAtomic(h.chainPath(), content, 0o600); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := h.updateSettings(func(s *settings) error {
		s.ChainEnabled = true
		s.ChainSourceType = sourceType
		s.ChainSourceURL = sourceURL
		return nil
	}); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	names := make([]string, 0, len(chains))
	for _, chain := range chains {
		names = append(names, chain.Name)
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok":         true,
		"count":      len(chains),
		"names":      names,
		"sourceType": sourceType,
		"sourceUrl":  sourceURL,
	})
}

func (h *helper) chainClear(writer http.ResponseWriter, request *http.Request) {
	// 与导入同一把锁：clear 与 import 交错会留下"内容是新导入的、开关是旧的"
	// 或"内容已删、开关还开着"——后者下次启动直接报错。
	h.configMu.Lock()
	defer h.configMu.Unlock()
	// 先关开关再删内容。反过来的话，进程在两步之间退出会留下"开关仍为开、
	// 内容已删"，下次启动直接报"已启用但导入内容缺失"。删除失败只记日志：
	// 开关已关，残留文件不再被任何路径引用（与 reset 一致）。
	if err := h.updateSettings(func(s *settings) error {
		s.ChainEnabled = false
		s.ChainSourceType = ""
		s.ChainSourceURL = ""
		return nil
	}); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := os.Remove(h.chainPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "chain clear: remove %s: %v\n", h.chainPath(), err)
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
}

// fetchChainSubscription downloads a Clash-format subscription. The URL is
// user-supplied through the token-gated API; only plain http(s) is accepted
// by the caller and the response is size-capped like a file import.
func fetchChainSubscription(rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), chainFetchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	// Some subscription hosts reject the default Go UA.
	request.Header.Set("User-Agent", "FreeV6")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("HTTP %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxChainProxyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxChainProxyBytes {
		return nil, fmt.Errorf("订阅内容超过 2MB 上限")
	}
	return data, nil
}
