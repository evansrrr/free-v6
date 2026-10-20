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
	if err := os.MkdirAll(filepath.Dir(h.chainPath()), 0o700); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := os.WriteFile(h.chainPath(), content, 0o600); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	stored, loadErr := h.loadSettings()
	if loadErr != nil {
		stored = defaultSettings()
	}
	stored.ChainEnabled = true
	stored.ChainSourceType = sourceType
	stored.ChainSourceURL = sourceURL
	if err := h.saveSettings(stored); err != nil {
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
	if err := os.Remove(h.chainPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	stored, loadErr := h.loadSettings()
	if loadErr != nil {
		stored = defaultSettings()
	}
	stored.ChainEnabled = false
	stored.ChainSourceType = ""
	stored.ChainSourceURL = ""
	if err := h.saveSettings(stored); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
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
