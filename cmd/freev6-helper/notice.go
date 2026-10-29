package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 公告板：唯一发布入口是仓库根目录的 notice.json（见 docs/notice.md），
// 客户端不直连 GitHub——helper 承担拉取、解析、过期过滤与本机缓存，
// 与更新检查共用同一套「镜像前缀 + 直连兜底 + 静默失败」策略：
// api.github.com 有 60 次/小时/IP 的共享配额且大陆经常不可达，
// raw.githubusercontent.com 在校园网实测直连超时，只有镜像族稳定。
//
// 任何失败都不是错误响应：离线、镜像全挂、文件被删都返回 200 + 空列表
// （必要时回退本机缓存），绝不阻塞启动。

const noticeRepoSlug = "evansrrr/free-v6"

// noticeURLs 是 var 便于测试注入。github.com/raw 的 302 目标
// （raw.githubusercontent.com）在镜像前缀下由镜像直接取回，所以这里
// 只需要 github.com 一个 host，正好落在 updateAllowedHosts 白名单内。
var noticeURLs = func() []string {
	direct := fmt.Sprintf("https://github.com/%s/raw/main/notice.json", noticeRepoSlug)
	urls := make([]string, 0, len(updateMirrorPrefixes)+1)
	for _, prefix := range updateMirrorPrefixes {
		urls = append(urls, prefix+direct)
	}
	return append(urls, direct)
}

const (
	noticeMaxBytes = 256 << 10 // 公告是纯文本，256 KiB 足够且挡住恶意大包
	noticeMaxItems = 20
	noticeMaxTitle = 200
	noticeMaxBody  = 4000
	noticeLinkText = 40
	// 离线缓存最多用 7 天：更旧的公告宁可空着，也不要展示早已失效的通知。
	noticeCacheMaxAge = 7 * 24 * time.Hour
)

// noticeItem 单条公告。JSON 字段同时用于对外响应，字段裁剪在 sanitize 里做。
type noticeItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	Level     string `json:"level"`
	Published string `json:"published"`
	Expires   string `json:"expires,omitempty"`
	Link      string `json:"link,omitempty"`
	LinkText  string `json:"linkText,omitempty"`
}

// noticePayload 是 notice.json 顶层结构（未知字段一律忽略，便于以后扩展）。
type noticePayload struct {
	Updated string       `json:"updated"`
	Items   []noticeItem `json:"items"`
}

// noticeCache 落 state/notice-cache.json，只在拉取全线失败时回退使用。
type noticeCache struct {
	FetchedAt time.Time    `json:"fetchedAt"`
	Updated   string       `json:"updated"`
	Items     []noticeItem `json:"items"`
}

var noticeLevels = map[string]bool{"info": true, "warning": true, "critical": true}

// parseNoticePayload 解析并消毒公告文件：坏条目逐条丢弃（一条写错不该让
// 整个公告板变空），过期条目直接过滤，长度一律按「字符」截断。
func parseNoticePayload(data []byte, now time.Time) (noticePayload, error) {
	var payload noticePayload
	// 兼容维护者直接写成顶层数组的写法（容易手滑，但不值得为此丢公告）。
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &payload.Items); err != nil {
			return noticePayload{}, fmt.Errorf("invalid notice.json: %w", err)
		}
	} else if err := json.Unmarshal(trimmed, &payload); err != nil {
		return noticePayload{}, fmt.Errorf("invalid notice.json: %w", err)
	}
	payload.Updated = strings.TrimSpace(payload.Updated)

	items := make([]noticeItem, 0, len(payload.Items))
	seen := make(map[string]bool, len(payload.Items))
	for _, candidate := range payload.Items {
		item, ok := sanitizeNotice(candidate, now)
		if !ok || seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		items = append(items, item)
		if len(items) == noticeMaxItems {
			break
		}
	}
	payload.Items = items
	return payload, nil
}

// sanitizeNotice 校验单条公告；返回 false 表示丢弃。
func sanitizeNotice(item noticeItem, now time.Time) (noticeItem, bool) {
	item.ID = strings.TrimSpace(item.ID)
	item.Title = strings.TrimSpace(item.Title)
	item.Body = strings.TrimSpace(item.Body)
	if item.ID == "" || item.Title == "" || len(item.ID) > 120 {
		return item, false
	}
	// 发布时间是必填项：没有时间轴的公告无法判断新旧，也没有意义。
	published, err := time.Parse(time.RFC3339, strings.TrimSpace(item.Published))
	if err != nil {
		return item, false
	}
	item.Published = published.Format(time.RFC3339)

	if expires, err := time.Parse(time.RFC3339, strings.TrimSpace(item.Expires)); err == nil {
		if !now.Before(expires) {
			return item, false // 已过期：不再下发
		}
		item.Expires = expires.Format(time.RFC3339)
	} else {
		item.Expires = "" // 写错的时间按「永不过期」处理，别丢整条
	}

	item.Level = strings.ToLower(strings.TrimSpace(item.Level))
	if !noticeLevels[item.Level] {
		item.Level = "info"
	}
	item.Title = truncateRunes(item.Title, noticeMaxTitle)
	item.Body = truncateRunes(item.Body, noticeMaxBody)

	// link 只接受 https：这条链路最终会交给系统浏览器打开，
	// javascript:/file: 之类的 scheme 一律丢弃（不因此丢整条公告）。
	item.Link = validNoticeLink(item.Link)
	if item.Link == "" {
		item.LinkText = ""
	} else {
		item.LinkText = truncateRunes(strings.TrimSpace(item.LinkText), noticeLinkText)
		if item.LinkText == "" {
			item.LinkText = "查看详情"
		}
	}
	return item, true
}

// validNoticeLink 返回规范化后的 https 链接，不合规时返回空串。
func validNoticeLink(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return ""
	}
	return parsed.String()
}

// truncateRunes 按字符（而非字节）截断，避免把中文切出半个字。
func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

// fetchNoticeJSON GET 公告文件，复用更新检查的 scheme/host 白名单，
// 读取上限 256 KiB（公告是纯文本，超出即视为异常响应）。
func fetchNoticeJSON(client *http.Client, rawURL string) ([]byte, error) {
	if err := validateUpdateURL(rawURL, updateAllowedHosts); err != nil {
		return nil, err
	}
	response, err := client.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, noticeMaxBytes))
}

func (h *helper) noticeCachePath() string {
	return filepath.Join(h.root, "state", "notice-cache.json")
}

// loadNoticeCache 读取本机缓存并按当前时间重新过滤过期条目；
// 缓存过旧（> 7 天）视为不可用——宁可空着也不展示失效通知。
func (h *helper) loadNoticeCache(now time.Time) (noticeCache, bool) {
	var cache noticeCache
	data, err := os.ReadFile(h.noticeCachePath())
	if err != nil {
		return cache, false
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return cache, false
	}
	if cache.FetchedAt.IsZero() || now.Sub(cache.FetchedAt) > noticeCacheMaxAge {
		return cache, false
	}
	cache.Items = filterLiveNotices(cache.Items, now)
	return cache, true
}

// filterLiveNotices 丢掉已过期或时间戳坏掉的条目（缓存同样适用）。
func filterLiveNotices(items []noticeItem, now time.Time) []noticeItem {
	live := make([]noticeItem, 0, len(items))
	for _, item := range items {
		if item.Expires != "" {
			if expires, err := time.Parse(time.RFC3339, item.Expires); err == nil && !now.Before(expires) {
				continue
			}
		}
		live = append(live, item)
		if len(live) == noticeMaxItems {
			break
		}
	}
	return live
}

// saveNoticeCache 原子写入（临时文件 + rename），半截文件永远不会被读到。
// 写失败只影响离线回退，调用方无需处理。
func (h *helper) saveNoticeCache(payload noticePayload, now time.Time) {
	cache := noticeCache{FetchedAt: now, Updated: payload.Updated, Items: payload.Items}
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	path := h.noticeCachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o644); err != nil {
		return
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
	}
}

// notice 回答前端的公告检查：
//
//	{ok:true, notices:[...], updated, fetchedAt, stale:false} 拉取成功
//	{ok:true, notices:[...], updated, fetchedAt, stale:true}  回退本机缓存
//	{ok:true, notices:[], error:"..."}                        离线且无缓存
//
// 三种情况都是 200：公告属于「可有可无」的附加信息，任何失败都不该
// 让前端把它当成错误弹窗。
func (h *helper) notice(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	now := time.Now()
	client := &http.Client{Timeout: 8 * time.Second}
	var payload noticePayload
	var lastErr error
	fetched := false
	for _, rawURL := range noticeURLs() {
		// 缓存穿透参数：镜像边缘缓存不能钉住旧公告。
		body, err := fetchNoticeJSON(client, rawURL+"?ts="+strconv.FormatInt(now.UnixNano(), 10))
		if err != nil {
			lastErr = err
			continue
		}
		parsed, err := parseNoticePayload(body, now)
		if err != nil {
			lastErr = err
			continue
		}
		payload = parsed
		lastErr = nil
		fetched = true
		break
	}

	if !fetched {
		// 全线失败（或响应是空对象）：能回退缓存就回退，并如实标注 stale。
		if cache, ok := h.loadNoticeCache(now); ok {
			writeJSON(writer, http.StatusOK, map[string]any{
				"ok":        true,
				"notices":   cache.Items,
				"updated":   cache.Updated,
				"fetchedAt": cache.FetchedAt.Format(time.RFC3339),
				"stale":     true,
				"error":     errorText(lastErr),
			})
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{
			"ok":      true,
			"notices": []noticeItem{},
			"stale":   false,
			"error":   errorText(lastErr),
		})
		return
	}

	h.saveNoticeCache(payload, now)
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok":        true,
		"notices":   payload.Items,
		"updated":   payload.Updated,
		"fetchedAt": now.Format(time.RFC3339),
		"stale":     false,
	})
}
