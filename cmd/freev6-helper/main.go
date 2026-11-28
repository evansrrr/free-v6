package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yourname/freev6/internal/mihomo"
	"github.com/yourname/freev6/internal/network"
	"github.com/yourname/freev6/internal/warp"
)

const defaultListenAddress = "127.0.0.1:13335"

// listenAddress honours FREEV6_API_PORT — the desktop shell reserves a free
// port per launch and passes it down, so a leftover helper (or any other
// program) can never make the new helper fail to bind. Without the variable
// (manual `go run`, older shell) it falls back to the historical fixed port.
func listenAddress() string {
	raw := strings.TrimSpace(os.Getenv("FREEV6_API_PORT"))
	if raw == "" {
		return defaultListenAddress
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 || port > 65535 {
		fmt.Fprintf(os.Stderr, "helper: ignoring invalid FREEV6_API_PORT %q\n", raw)
		return defaultListenAddress
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

// icsOpTimeout 覆盖 WinRT tethering 异步操作的最坏情况（Stop/Start 各
// 最多 Wait-Op 60s），正常几百毫秒完成；探测单独给短超时。
const (
	icsOpTimeout     = 120 * time.Second
	icsDetectTimeout = 30 * time.Second
	// icsRecoverTimeout 是「切换失败后把热点拉回原上游」以及「失败回滚」的
	// 独立预算。这两步都发生在切换之后，绝不能复用 icsOpTimeout 那个 ctx：
	// 切换失败往往就是它已经超时，复用等于立刻放弃、什么都不做。
	// 脚本最坏情况 = 停等 8s + 起等 15s + PowerShell 启动，60s 足够。
	icsRecoverTimeout = 60 * time.Second

	// tunCleanupTimeout 是删除残留 TUN 默认路由的预算。实测本机四条 netsh
	// 合计约 0.4s；3s 足够，又不会让停止/启动多等太久。
	tunCleanupTimeout = 3 * time.Second
)

type helper struct {
	root string
	mu   sync.Mutex
	// server 是本地 API 服务器，shutdown 端点用它做有界 Shutdown（不能从
	// handler 里同步调用，所以 handler 只负责调度，见 exitAfterShutdown）。
	server *http.Server
	// settingsMu serializes settings.json read-modify-write transactions across
	// the handlers that write it (settings PUT, chain/cloud import, proxy
	// start, reset). Reads stay lock-free: the atomic write guarantees a reader
	// never observes a partial file.
	settingsMu sync.Mutex
	// lastStop 是最近一次停止免流完成的时刻（UnixNano）。IPv6 前置检查用它
	// 判断要不要重试：TUN 拆除后的几秒里出站必然失败，其余时候一轮定论。
	lastStop atomic.Int64
}

type settings struct {
	Mode        string   `json:"mode"`
	CampusCIDRs []string `json:"campusCidrs"`
	Blacklist   []string `json:"blacklist"`
	DevMode     bool     `json:"devMode"`
	AutoStart   bool     `json:"autoStart"`
	SilentStart bool     `json:"silentStart"`
	// 自动运行免流（设置 → 通用）：打开软件后由前端自动尝试启动免流，
	// 开机自启动时等到网络恢复等合适时机再试；默认关闭。
	AutoRunProxy bool `json:"autoRunProxy"`
	// 快捷键（设置 → 通用）：启用后按 F6 由桌面壳弹出主窗口，即使窗口
	// 处于托盘驻留/静默启动状态；默认关闭。
	HotkeyEnabled bool `json:"hotkeyEnabled"`
	// 共享移动热点（设置 → 通用）：启动免流时把 Windows 移动热点的上游
	// 切到 FreeV6 TUN（ICS 拓扑切换），热点设备走电脑的代理出口，
	// 停止免流自动还原；默认关闭。
	HotspotShare bool `json:"hotspotShare"`
	// 链式代理（设置 → 核心）：来源与开关由 /api/v1/chain-proxy 端点维护。
	// GUI 的 settings PUT 不携带这些键 —— 必须「省略即保留」，否则每次
	// 保存设置都会悄悄关掉链式、把出口切回裸 WARP。
	ChainEnabled    bool   `json:"chainEnabled"`
	ChainSourceType string `json:"chainSourceType"`
	ChainSourceURL  string `json:"chainSourceUrl"`
	// 备用配置（设置 → 核心）：来源与更新时间由 /api/v1/cloud-config 端点
	// 维护，开关由 GUI 主 PUT 携带。与链式代理同理「省略即保留」，否则
	// 一次普通保存就会把学生刚取到的配置悄悄关掉、退回裸 WARP 出口。
	CloudEnabled   bool   `json:"cloudEnabled"`
	CloudSourceURL string `json:"cloudSourceUrl"`
	CloudUpdatedAt string `json:"cloudUpdatedAt"`
	// PortalHost 是校园网登录页地址（学生只输一次，之后用来派生入口）。
	// 存的是归一化后的裸 host，不是派生的完整入口。
	PortalHost string `json:"portalHost"`
}

type proxyRequest struct {
	Mode        string   `json:"mode"`
	CampusCIDRs []string `json:"campusCidrs"`
	DevMode     bool     `json:"devMode"`
}

type registerRequest struct {
	Name string `json:"name"`
}

func defaultCampusCIDRs() []string {
	return []string{"edu.cn"}
}

func defaultSettings() settings {
	return settings{Mode: mihomo.ModeRule, CampusCIDRs: defaultCampusCIDRs(), Blacklist: []string{}}
}

func main() {
	// Route-cleanup failures land in helper.log (this process's stderr) instead
	// of being swallowed.
	network.RouteCleanupLog = os.Stderr
	// Task Manager's Startup tab runs the HKCU Run marker (helper --autostart)
	// at logon; the real launch is the "FreeV6 Autostart" logon task. Exit
	// before binding the API port so the marker never collides with the helper
	// the desktop app spawns.
	if len(os.Args) > 1 && os.Args[1] == "--autostart" {
		return
	}
	root := executableRoot()
	// 进程树诊断进 helper.log（与 RouteCleanupLog 同一套约定）。
	mihomo.ProcessLog = os.Stderr
	// Job 必须在任何子进程之前创建：helper 自加入后，之后派生的核心、
	// powershell 等自动成为成员，helper 消失时一起退出。
	if err := mihomo.InitProcessJob(); err != nil {
		fmt.Fprintf(os.Stderr, "helper: process job unavailable: %v\n", err)
	}
	h := &helper{root: root}
	// 旧版本预置的地理数据（geosite/geoip/mmdb，合计约 27MB）已经没有引用，
	// 静默清掉；删不掉的（被占用）下次启动再试。
	if removed := mihomo.RemoveLegacyGeodata(root); len(removed) > 0 {
		fmt.Fprintf(os.Stderr, "helper: removed legacy geodata: %v\n", removed)
	}
	// 规则集缓存：列表换过之后旧文件不会自己消失，且"序号复用"会让它被
	// 误当成新列表加载。现在 provider 用语义名做文件名，不认识的都清掉。
	if removed := mihomo.RemoveStaleRuleSetFiles(root); len(removed) > 0 {
		fmt.Fprintf(os.Stderr, "helper: removed stale rule sets: %v\n", removed)
	}
	// 上一轮已经实测跑通过的内核不需要再留回滚位：磁盘收敛到一份 38MB。
	// 这里只是提前回收——即使不做，下一次启动免流成功时也会收敛。
	if store := mihomo.NewCoreStore(root); store.Verified() {
		if removed := store.Prune(); len(removed) > 0 {
			fmt.Fprintf(os.Stderr, "helper: pruned stale cores: %v\n", removed)
		}
	}
	// Re-apply the autostart pieces while the switch is on: refresh the task's
	// exe path after updates move the install directory and rewrite the Task
	// Manager marker entry (migrating old builds' direct-exe form). Task
	// Manager's own enable/disable state is left untouched.
	if current, err := h.loadSettings(); err == nil && current.AutoStart {
		if syncErr := applyAutoStart(root); syncErr != nil {
			fmt.Fprintf(os.Stderr, "autostart re-apply failed: %v\n", syncErr)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", h.status)
	mux.HandleFunc("/api/v1/runtime", h.runtime)
	mux.HandleFunc("/api/v1/runtime/check", h.runtimeCheck)
	mux.HandleFunc("/api/v1/appearance", h.appearance)
	mux.HandleFunc("/api/v1/runtime/download", h.downloadRuntime)
	mux.HandleFunc("/api/v1/runtime/download/progress", h.runtimeDownloadProgress)
	mux.HandleFunc("/api/v1/settings", h.settings)
	mux.HandleFunc("/api/v1/settings/reset", h.resetSettings)
	mux.HandleFunc("/api/v1/proxy/start", h.startProxy)
	mux.HandleFunc("/api/v1/proxy/stop", h.stopProxy)
	mux.HandleFunc("/api/v1/shutdown", h.shutdown)
	mux.HandleFunc("/api/v1/warp/register", h.registerWarp)
	mux.HandleFunc("/api/v1/update/download", h.updateDownload)
	mux.HandleFunc("/api/v1/update/progress", h.updateProgress)
	mux.HandleFunc("/api/v1/update/latest", h.updateLatest)
	mux.HandleFunc("/api/v1/notice", h.notice)
	mux.HandleFunc("/api/v1/ipv6/probe", h.ipv6Probe)
	mux.HandleFunc("/api/v1/chain-proxy", h.chainProxy)
	mux.HandleFunc("/api/v1/cloud-config", h.cloudConfig)
	// Per-launch bearer token minted by the desktop shell: without it the
	// helper must not serve an unauthenticated loopback API (any browser
	// page could drive proxy start/stop and settings otherwise).
	apiToken := os.Getenv("FREEV6_API_TOKEN")
	if apiToken == "" {
		fmt.Fprintln(os.Stderr, "helper: FREEV6_API_TOKEN is not set; all API requests will be rejected")
	}
	addr := listenAddress()
	server := &http.Server{
		Addr:              addr,
		Handler:           withCORS(guardLocalOnly(requireAuth(apiToken, mux))),
		ReadHeaderTimeout: 5 * time.Second,
		// ReadTimeout bounds how long a request may dribble its body, so a slow
		// client cannot pin a handler goroutine (or the proxy start/stop lock)
		// indefinitely. WriteTimeout stays 0: proxy start/stop legitimately runs
		// for minutes. IdleTimeout only reaps idle keep-alive connections.
		ReadTimeout: 30 * time.Second,
		IdleTimeout: 120 * time.Second,
	}
	// Bind with orphan takeover: killing the desktop via Task Manager leaves
	// its helper child alive (Windows doesn't cascade-kill), and that orphan
	// otherwise keeps our address busy forever — every relaunch died on the
	// bind error. The shell now hands out a free port per launch, so this is
	// the second line of defence rather than the only one.
	h.server = server
	// 桌面壳被强杀/崩溃时，helper 自己把会话停干净再退出（旧壳不传这个 env，
	// 监视不启用，行为与之前一致）。
	watchParent(h.shutdownFromParent)
	listener, err := listenOrTakeOver(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: %v\n", err)
		os.Exit(1)
	}
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		panic(err)
	}
}

func (h *helper) status(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	pidPath := filepath.Join(h.root, "state", "mihomo.pid")
	pid, running, statusErr := mihomo.Status(pidPath)
	admin := false
	adminErr := ""
	ctx, cancel := context.WithTimeout(request.Context(), 3*time.Second)
	defer cancel()
	if runtime.GOOS == "windows" {
		admin, statusErr = network.IsAdministrator(ctx)
		if statusErr != nil {
			adminErr = statusErr.Error()
		}
	}
	currentSettings, settingsErr := h.loadSettings()
	settingsError := ""
	if settingsErr != nil {
		if !errors.Is(settingsErr, os.ErrNotExist) {
			settingsError = settingsErr.Error()
		}
		currentSettings = defaultSettings()
	}
	_, warpErr := os.Stat(filepath.Join(h.root, "state", "warp.json"))
	// 外部控制器 secret（只读探测）：存在即回传给前端用于 9090 直连；
	// 读失败按空串降级——运行中的面板只是暂时取不到数据，不影响免流。
	controllerSecret, secretErr := mihomo.ReadControllerSecret(filepath.Join(h.root, "state"))
	if secretErr != nil {
		controllerSecret = ""
	}
	// 热点共享状态：仅免流运行中且本会话已切换时有意义。active 用本机
	// 地址探测（无子进程，3s 轮询无压力）；前端观察 shared→断开 的下降沿
	// 弹“下次启动生效”提示。
	var hotspot any
	if running {
		if snap, snapErr := network.LoadSnapshot(filepath.Join(h.root, "state", "network-snapshot.json")); snapErr == nil && snap.Hotspot != nil && snap.Hotspot.Applied {
			hotspot = map[string]any{"shared": true, "active": network.HotspotActive(snap.Hotspot), "path": snap.Hotspot.Path}
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok":               true,
		"root":             h.root,
		"admin":            admin,
		"adminError":       adminErr,
		"proxy":            map[string]any{"running": running, "pid": pid, "error": errorText(statusErr)},
		"warp":             map[string]any{"registered": warpErr == nil},
		"settings":         currentSettings,
		"settingsError":    settingsError,
		"hotspot":          hotspot,
		"controllerSecret": controllerSecret,
	})
}

func (h *helper) runtime(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	core, err := mihomo.NewCoreStore(h.root).Active()
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok":             true,
		"present":        err == nil,
		"path":           core.Path,
		"version":        core.Version,
		"versionDisplay": core.Parsed().Display(),
		"managed":        core.Managed,
		// legacy=true 表示当前跑的还是老版本遗留的上游 mihomo 内核，
		// 更新检查会把它当成"该升级"，UI 用它解释为什么要更新。
		"legacy":       err == nil && !core.Managed,
		"error":        errorText(err),
		"platform":     runtime.GOOS,
		"architecture": runtime.GOARCH,
	})
}

// runtimeCheck 比对发布清单，回答"有没有新内核"。带 ?force=1 时绕过缓存。
// 网络失败不是致命错误：前端据此决定是否提示，不阻塞启动。
func (h *helper) runtimeCheck(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	check, err := mihomo.CheckCoreUpdate(h.root, request.URL.Query().Get("force") == "1")
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok":         err == nil,
		"installed":  check.Installed,
		"latest":     check.Latest,
		"available":  check.Available,
		"legacy":     check.Legacy,
		"notes":      check.Notes,
		"minApp":     check.MinApp,
		"maxApp":     check.MaxApp,
		"downloadMB": check.DownloadMB,
		"error":      errorText(err),
	})
}

// runtimeDownloadRun 由后台下载 goroutine 写入、GET /runtime/download/progress
// 读取。done/total 是字节计数器，切换镜像时归零重算（进度条可能回跳，与
// 更新下载同约定）。state/path/version/sha256/err/busy 受 mu 保护。
type runtimeDownloadRunState struct {
	mu       sync.Mutex
	state    string // idle | downloading | done | error
	path     string
	version  string
	previous string
	warning  string
	sha256   string
	err      string
	busy     bool
	done     atomic.Int64
	total    atomic.Int64
}

var runtimeDownloadRun = runtimeDownloadRunState{state: "idle"}

// downloadRuntime 触发后台下载并立即返回；进度由前端轮询
// GET /api/v1/runtime/download/progress 获取（与更新下载同一套模式）。
func (h *helper) downloadRuntime(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	// body 可选：{"force": true} 表示重装当前版本（修复损坏的内核）。
	var input struct {
		Force bool `json:"force"`
	}
	if request.Body != nil {
		_ = json.NewDecoder(io.LimitReader(request.Body, 4<<10)).Decode(&input)
	}
	force := input.Force
	// 重装当前版本会覆盖正被进程映射的 exe，Windows 下 rename 必然失败，
	// 先给一句人话而不是让用户看到 "Access is denied"。
	if force {
		if _, running, _ := mihomo.Status(filepath.Join(h.root, "state", "mihomo.pid")); running {
			writeJSON(writer, http.StatusConflict, map[string]string{"error": "免流运行中无法重装内核，请先停止"})
			return
		}
	}

	runtimeDownloadRun.mu.Lock()
	if runtimeDownloadRun.busy {
		runtimeDownloadRun.mu.Unlock()
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "核心下载已在进行"})
		return
	}
	runtimeDownloadRun.busy = true
	runtimeDownloadRun.state = "downloading"
	runtimeDownloadRun.path = ""
	runtimeDownloadRun.version = ""
	runtimeDownloadRun.previous = ""
	runtimeDownloadRun.warning = ""
	runtimeDownloadRun.sha256 = ""
	runtimeDownloadRun.err = ""
	runtimeDownloadRun.done.Store(0)
	runtimeDownloadRun.total.Store(-1)
	runtimeDownloadRun.mu.Unlock()

	go func() {
		// 请求体立即返回，下载生命周期与请求解耦：用独立 context 而非
		// request.Context()。清单 + 内核包共用这份总预算，每条镜像另外
		// 有自己的停滞/硬限预算。
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()

		finish := func(state string, apply func()) {
			runtimeDownloadRun.mu.Lock()
			runtimeDownloadRun.state = state
			runtimeDownloadRun.busy = false
			if apply != nil {
				apply()
			}
			runtimeDownloadRun.mu.Unlock()
		}

		// 清单永远强制刷新：用户点"下载/更新"就是要拿最新的，缓存留给
		// 后台静默检查。
		manifest, err := mihomo.FetchCoreManifest(h.root, true)
		if err != nil {
			finish("error", func() { runtimeDownloadRun.err = err.Error() })
			return
		}
		store := mihomo.NewCoreStore(h.root)
		result, err := store.ApplyUpdate(ctx, manifest, force, func(received, total int64) {
			runtimeDownloadRun.done.Store(received)
			runtimeDownloadRun.total.Store(total)
		})
		if err != nil {
			finish("error", func() { runtimeDownloadRun.err = err.Error() })
			return
		}
		finish("done", func() {
			runtimeDownloadRun.path = result.Path
			runtimeDownloadRun.version = result.Version
			runtimeDownloadRun.previous = result.Previous
			runtimeDownloadRun.warning = result.Warning
			runtimeDownloadRun.sha256 = result.SHA256
		})
	}()

	writeJSON(writer, http.StatusOK, map[string]any{"ok": true})
}

// runtimeDownloadProgress 供前端在下载期间每 300ms 轮询一次。
func (h *helper) runtimeDownloadProgress(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	runtimeDownloadRun.mu.Lock()
	state, path, version, previous, warning := runtimeDownloadRun.state, runtimeDownloadRun.path, runtimeDownloadRun.version, runtimeDownloadRun.previous, runtimeDownloadRun.warning
	sha256Hex, errMsg := runtimeDownloadRun.sha256, runtimeDownloadRun.err
	runtimeDownloadRun.mu.Unlock()
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok":       true,
		"state":    state,
		"done":     runtimeDownloadRun.done.Load(),
		"total":    runtimeDownloadRun.total.Load(),
		"path":     path,
		"version":  version,
		"previous": previous,
		"warning":  warning,
		"sha256":   sha256Hex,
		"error":    errMsg,
	})
}

// resetSettings wipes the user's stored settings back to defaults and removes
// any imported chain proxy. It is a recovery action, not a routine toggle:
// autostart is torn down if it was on, the chain-proxy file is deleted, and
// settings.json is overwritten with defaultSettings(). The running mihomo
// (if any) is untouched — the frontend warns the user to stop first.
func (h *helper) resetSettings(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	// 开机自启开着时撤销任务调度：先做有副作用的操作，失败则 500，
	// 否则文件写了但自启任务还挂着。
	if current, err := h.loadSettings(); err == nil && current.AutoStart {
		if syncErr := removeAutoStart(); syncErr != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": syncErr.Error()})
			return
		}
	}
	// 清除已导入的链式代理与备用配置（文件不存在不算错误）
	if err := os.Remove(h.chainPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := os.Remove(h.cloudProfilePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defaults := defaultSettings()
	if err := h.updateSettings(func(s *settings) error {
		*s = defaults
		return nil
	}); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "settings": defaults})
}

func (h *helper) settings(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		current, err := h.loadSettings()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if err != nil {
			// First launch (no file yet): return full defaults, consistent with
			// /status — a zero-value settings would expose a nil blacklist.
			current = defaultSettings()
		}
		writeJSON(writer, http.StatusOK, current)
	case http.MethodPut:
		body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 256<<10))
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeJSON(writer, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
				return
			}
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		var current settings
		if err := json.Unmarshal(body, &current); err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		if current.Mode == "" {
			current.Mode = mihomo.ModeRule
		}
		if current.Mode != mihomo.ModeRule && current.Mode != mihomo.ModeGlobal && current.Mode != mihomo.ModeDirect {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "mode must be rule, global or direct"})
			return
		}
		if current.CampusCIDRs == nil {
			current.CampusCIDRs = defaultCampusCIDRs()
		}
		entries := make([]string, 0, len(current.CampusCIDRs))
		for _, entry := range current.CampusCIDRs {
			if err := mihomo.ValidateCampusTarget(entry); err != nil {
				writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			entries = append(entries, strings.ToLower(strings.TrimSpace(entry)))
		}
		current.CampusCIDRs = entries
		persisted, loadErr := h.loadSettings()
		if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "settings.json 无法解析，请在设置中重置: " + loadErr.Error(), "code": "settings-corrupt"})
			return
		}
		if loadErr != nil {
			persisted = defaultSettings()
		}
		// Merge the omitted fields from the stored settings, then validate the
		// (possibly hand-edited) blacklist. The authoritative merge + persist
		// happens under the settings write lock below.
		mergeSettingsOnto(&current, persisted, body)
		blacklistEntries := make([]string, 0, len(current.Blacklist))
		for _, entry := range current.Blacklist {
			if err := mihomo.ValidateDomain(entry); err != nil {
				writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			blacklistEntries = append(blacklistEntries, strings.ToLower(strings.TrimSpace(entry)))
		}
		current.Blacklist = blacklistEntries
		// Re-merge against the freshest settings and persist atomically under
		// the write lock, so concurrent writers cannot clobber each other. The
		// autostart side effect must run before the file is written (a failure
		// returns 500 so the GUI reverts the toggle).
		if err := h.updateSettings(func(s *settings) error {
			mergeSettingsOnto(&current, *s, body)
			if s.AutoStart != current.AutoStart {
				var syncErr error
				if current.AutoStart {
					syncErr = applyAutoStart(h.root)
				} else {
					syncErr = removeAutoStart()
				}
				if syncErr != nil {
					return syncErr
				}
			}
			*s = current
			return nil
		}); err != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(writer, http.StatusOK, current)
	default:
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// describeDNSChanges 把自动修复的改动排成一行日志：WiFi (ipv4/ipv6), Ethernet (ipv4)。
func describeDNSChanges(changes []network.DNSResetChange) string {
	parts := make([]string, 0, len(changes))
	for _, change := range changes {
		var protocols []string
		if change.IPv4 {
			protocols = append(protocols, "ipv4")
		}
		if change.IPv6 {
			protocols = append(protocols, "ipv6")
		}
		parts = append(parts, change.Alias+" ("+strings.Join(protocols, "/")+")")
	}
	return strings.Join(parts, ", ")
}

// clearLeftoverTunRoutes 清掉「内核没在跑、FreeV6TUN 却还在」留下的 auto-route
// 残留（判定与执行见 network.ClearLeftoverTunRoutes）。
//
// 启动流程与 IPv6 探测共用：前者覆盖开发者模式等跳过探测的路径，后者在
// 停止后立刻点检测时先恢复网络。返回清掉的路由条数。
func (h *helper) clearLeftoverTunRoutes(reason string) int {
	pidPath := filepath.Join(h.root, "state", "mihomo.pid")
	_, running, statusErr := mihomo.Status(pidPath)
	ctx, cancel := context.WithTimeout(context.Background(), tunCleanupTimeout)
	defer cancel()
	removed := network.ClearLeftoverTunRoutes(ctx, running, statusErr)
	if removed > 0 {
		fmt.Fprintf(os.Stderr, "helper: %s: cleared %d leftover %s route(s)\n", reason, removed, network.TunDeviceName)
	}
	return removed
}

func (h *helper) startProxy(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	// Decode and normalize before taking the proxy lock: a slow or oversized
	// body must never hold the start/stop mutex (start/stop takes minutes).
	var input proxyRequest
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 16<<10)).Decode(&input); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if input.Mode == "" {
		input.Mode = mihomo.ModeRule
	}
	if input.CampusCIDRs == nil {
		input.CampusCIDRs = defaultCampusCIDRs()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// 链式代理：启用时先解析导入内容——纯读、无副作用，放在端口/权限检查
	// 之前让坏输入最快失败。启用却缺内容必须明确报错，绝不静默降级成裸
	// WARP 出口（用户以为出口是自有代理，实际不是，是语义欺诈）。
	storedForChain, chainSettingsErr := h.loadSettings()
	if chainSettingsErr != nil && !errors.Is(chainSettingsErr, os.ErrNotExist) {
		// A corrupt settings file must abort, never silently fall back to a
		// bare-WARP render while the UI still shows chain/cloud as enabled.
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "settings.json 无法解析，请在设置中重置: " + chainSettingsErr.Error(), "code": "settings-corrupt"})
		return
	}
	if chainSettingsErr != nil {
		storedForChain = defaultSettings()
	}
	// 公告总开关：维护者关掉备用配置后，即使本地还留着上次的节点也不许用。
	// 放在端口/权限/网络快照之前——纯读校验，失败不产生任何副作用。
	if storedForChain.CloudEnabled && !h.cloudProfileUnlocked(time.Now()) {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "备用配置当前未开放（公告中已关闭或未获取到公告），请在设置中改用其他出口", "code": "cloud-locked"})
		return
	}
	var chainProxies []mihomo.ChainProxy
	if storedForChain.ChainEnabled {
		chainData, readErr := os.ReadFile(h.chainPath())
		if readErr != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "链式代理已启用但导入内容缺失，请在设置中重新导入"})
			return
		}
		parsed, parseErr := mihomo.ParseChainProxies(chainData)
		if parseErr != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "解析链式代理失败: " + parseErr.Error()})
			return
		}
		chainProxies = parsed
	}
	// 备用配置：节点即出口，启用时不套 WARP、也就不需要 WARP 凭据。与链式
	// 代理互斥——两者改的是同一个出口，同时开会变成"到底走的哪条"不可解释。
	var cloudNodes []mihomo.ChainProxy
	if storedForChain.CloudEnabled {
		if storedForChain.ChainEnabled {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "备用配置与链式代理不能同时启用，请在设置中关闭其中一个"})
			return
		}
		cloudData, readErr := os.ReadFile(h.cloudProfilePath())
		if readErr != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "备用配置已启用但本地内容缺失，请在设置中重新获取"})
			return
		}
		profile, parseErr := mihomo.ParseCloudProfile(cloudData)
		if parseErr != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "解析备用配置失败: " + parseErr.Error()})
			return
		}
		if len(profile.Nodes) == 0 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "备用配置里没有可用节点，请重新获取"})
			return
		}
		cloudNodes = profile.Nodes
	}
	if err := mihomo.CheckListenPorts(); err != nil {
		// code 供前端精准提示（toast），error 文本保留给日志排障。
		writeJSON(writer, http.StatusConflict, map[string]string{"error": err.Error() + "; please close other proxy/DNS software first", "code": "port-conflict"})
		return
	}
	if runtime.GOOS == "windows" {
		adminCtx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
		defer cancel()
		admin, err := network.IsAdministrator(adminCtx)
		if err != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if !admin {
			writeJSON(writer, http.StatusForbidden, map[string]string{"error": "administrator privileges are required to start mihomo TUN"})
			return
		}
	}
	// 上次停止被强杀在「关 TUN 的中途」时，会留下没有会话的 FreeV6TUN 与半段
	// 默认路由：出站全被它吸走，表现是「网连着但什么都打不开」。内核没在跑就
	// 不该留着它——在抓快照/还原热点之前先清掉，用户不必等到 IPv6 探测那一步
	// （开发者模式会跳过探测）。只删路由，保留适配器供 ICS 角色还原使用。
	h.clearLeftoverTunRoutes("start")
	// 校园模式下凭据完全用不上（网关由云端节点自带），此时不能因为没注册
	// WARP 就拦人启动——那会让"导入配置即用"的学生永远启不了免流。
	var device warp.Device
	if len(cloudNodes) == 0 {
		storedDevice, readErr := readDevice(filepath.Join(h.root, "state", "warp.json"))
		if readErr != nil {
			// code 供前端弹「重试注册」引导（凭据缺失/损坏时重新注册即可覆盖重建）
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("read WARP state: %v", readErr), "code": "warp-missing"})
			return
		}
		device = storedDevice
	}
	// Default blacklist is compiled into the binary (blacklist.txt embedded
	// at build time); a non-empty settings.json blacklist overrides it.
	blacklist, blErr := mihomo.DefaultBlacklist()
	if blErr != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": blErr.Error()})
		return
	}
	// Reuse the settings already loaded above (storedForChain) instead of a
	// second read that silently swaps in the built-in blacklist on any error.
	if len(storedForChain.Blacklist) > 0 {
		blacklist = storedForChain.Blacklist
	}
	stateDir := filepath.Join(h.root, "state")
	// Controller secret: load-or-create BEFORE any network change so a disk
	// failure aborts cleanly, and so the render below and the readiness check
	// after mihomo starts both use the same value.
	controllerSecret, secretErr := mihomo.LoadOrCreateSecret(stateDir)
	if secretErr != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": secretErr.Error()})
		return
	}
	config, err := mihomo.RenderWithOptions(device, mihomo.RenderOptions{
		Mode:             input.Mode,
		CampusCIDRs:      input.CampusCIDRs,
		Blacklist:        blacklist,
		DevMode:          input.DevMode,
		ControllerSecret: controllerSecret,
		ChainProxies:     chainProxies,
		CloudNodes:       cloudNodes,
	})
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	configPath := filepath.Join(stateDir, "mihomo.yaml")
	pidPath := filepath.Join(stateDir, "mihomo.pid")
	logPath := filepath.Join(stateDir, "mihomo.log")
	snapshotPath := filepath.Join(stateDir, "network-snapshot.json")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := writeFileAtomic(configPath, []byte(config), 0o600); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("write mihomo config: %v", err)})
		return
	}
	// 链式启用时用真实 -t 校验合并产物：未知 type/非法字段等只有 mihomo 能
	// 发现的问题，必须在残留自愈与网络快照（接下来的副作用）之前暴露。
	if len(chainProxies) > 0 || len(cloudNodes) > 0 {
		chainBinary, discoverErr := mihomo.DiscoverBinary(h.root)
		if discoverErr != nil {
			writeJSON(writer, http.StatusNotFound, map[string]string{"error": discoverErr.Error()})
			return
		}
		if testErr := mihomo.TestConfig(chainBinary, configPath, stateDir); testErr != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": testErr.Error()})
			return
		}
	}
	// 残留自愈：走到这里说明端口检查已通过（mihomo 必未在跑）。
	healCtx, healCancel := context.WithTimeout(context.Background(), icsOpTimeout)
	leftover := network.HealLeftoverSnapshot(healCtx, snapshotPath, network.RestoreLeftoverSnapshot)
	healCancel()
	if leftover.Warning != "" {
		fmt.Fprintf(os.Stderr, "helper: %s\n", leftover.Warning)
	}
	// 默认防护：旧版本停止免流时把当时生效的 DNS（即 DHCP 下发的那些地址）
	// 原样写成了静态。值相同所以当场无感，换网络后静态地址仍指向旧网段，
	// 表现是「Wi-Fi 已连接但打不开网页」，重启网卡也不恢复。
	//
	// 在启动时修的是「机器当前状态」：新快照已经不再记录 DNS，也就不存在
	// 「停止时写回去」这一步；但污染还留在注册表里，不清掉用户这次会话换个
	// 网络照样断解析。只处理「静态值 == DHCP 下发的值」的网卡（见
	// dnsResetCandidate），用户自己设的 DNS 一定不相等，不会被碰。失败不阻断
	// 启动 —— 修不了只是回到修复前的行为，免流本身照常。
	dnsCtx, dnsCancel := context.WithTimeout(context.Background(), 30*time.Second)
	frozenDNS, frozenDNSFailures := network.ResetDNSToDHCP(dnsCtx)
	dnsCancel()
	for _, failure := range frozenDNSFailures {
		fmt.Fprintf(os.Stderr, "helper: restore frozen DNS: %s\n", failure)
	}
	if len(frozenDNS) > 0 {
		fmt.Fprintf(os.Stderr, "helper: restored DNS to DHCP on %s\n", describeDNSChanges(frozenDNS))
	}
	snapshotCtx, snapshotCancel := context.WithTimeout(request.Context(), 10*time.Second)
	// ICS 共享基线只在热点共享开启时需要：跳过它的 COM 枚举能省掉一次
	// powershell.exe 冷启动（默认关闭热点共享时，启动免流只做一次 GetAdaptersAddresses
	// 系统调用，不再为用不上的基线 spawn 子进程）。
	snapshot, err := network.CaptureSnapshot(snapshotCtx, storedForChain.HotspotShare)
	snapshotCancel()
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// 基线：正常情况下就是刚抓到的这份。上次自愈没能还原干净时沿用旧基线 ——
	// 把「还原失败的现状」重新抓成基线，等于让残留的切换状态从此变成正式
	// 配置，那之后再没有干净基线可回。
	baseline := network.AdoptBaseline(snapshot, leftover.Preserved)
	if err := network.SaveSnapshot(snapshotPath, baseline); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	store := mihomo.NewCoreStore(h.root)
	core, err := store.Active()
	if err != nil {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	// 起内核：Start → 等 /version 就绪 → 控制器鉴权。三步合成一次尝试，
	// 好让"新内核起不来就回滚重试"只写一遍。
	startCore := func(binaryPath string) (int, error) {
		startedPID, startErr := mihomo.Start(binaryPath, configPath, stateDir, pidPath, logPath)
		if startErr != nil {
			return http.StatusConflict, startErr
		}
		// The core is already spawned, so the readiness wait must finish (and,
		// on failure, the rollback/MarkBad below must stay meaningful) even if
		// the client disconnects. Detach from the request context and rely on
		// our own budget: DeadlineExceeded is still a real failure; a client
		// cancel must not punish a healthy core with a permanent MarkBad.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 15*time.Second)
		defer cancel()
		if readyErr := mihomo.WaitReady(ctx, "127.0.0.1:9090", 100*time.Millisecond); readyErr != nil {
			_ = mihomo.Stop(pidPath)
			if errors.Is(readyErr, context.Canceled) {
				return http.StatusBadGateway, fmt.Errorf("start canceled before ready: %w", readyErr)
			}
			return http.StatusBadGateway, errors.New(diagnosticError(readyErr, logPath))
		}
		if _, checkErr := mihomo.CheckController(ctx, "127.0.0.1:9090", controllerSecret); checkErr != nil {
			_ = mihomo.Stop(pidPath)
			return http.StatusBadGateway, errors.New(diagnosticError(checkErr, logPath))
		}
		// A stale instance can answer 9090 with the same per-install secret
		// while our freshly spawned core already exited: confirm OUR pid is the
		// one still running before reporting success.
		if !mihomo.IsRunning(startedPID) {
			_ = mihomo.Stop(pidPath)
			return http.StatusBadGateway, errors.New("core exited before becoming ready")
		}
		return http.StatusOK, nil
	}

	statusCode, startErr := startCore(core.Path)
	if startErr != nil {
		// 自动回滚：新内核起不来时退回上一版再试一次，并把它记进 bad 列表，
		// 后续自动更新不再推这个版本。
		if rolled, rollErr := store.Rollback(); rollErr == nil && rolled.Path != core.Path {
			if !errors.Is(startErr, context.Canceled) {
				store.MarkBad(core.Version)
			}
			fmt.Fprintf(os.Stderr, "helper: core %s failed to start (%v), rolled back to %s\n", core.Version, startErr, rolled.Version)
			if retryStatus, retryErr := startCore(rolled.Path); retryErr == nil {
				statusCode, startErr = http.StatusOK, nil
				core = rolled
			} else {
				statusCode, startErr = retryStatus, retryErr
			}
		}
	}
	if startErr != nil {
		writeJSON(writer, statusCode, map[string]string{"error": startErr.Error()})
		return
	}
	// 内核跑通并应答了控制器：这份内核从此可信，回滚位可以回收。
	// 失败路径不回收——那时回滚位正是要用的东西。
	// 只标记**本次实际启动的版本**：启动期间内核更新可能已经把 active 换掉，
	// 那时不能把没跑过的内核标成"已验证"，也不能按新 active 回收旧版本。
	if store.MarkVerified(core.Version) {
		if removed := store.Prune(); len(removed) > 0 {
			fmt.Fprintf(os.Stderr, "helper: pruned previous core: %v\n", removed)
		}
	}
	// Persist onto the FULL stored settings — saving a partial struct here
	// used to wipe blacklist/devMode on every proxy start, so the developer
	// switch reverted after a page refresh. Re-merge onto the freshest settings
	// under the write lock instead of re-reading with a defaults fallback.
	_ = h.updateSettings(func(s *settings) error {
		*s = mergeStartSettings(*s, input)
		return nil
	})
	// 热点共享（设置 → 通用，非阻断）：mihomo 就绪后才切 ICS，失败回滚
	// 到启动前基线；结果随响应带回前端（成功记日志、失败弹日志界面）。
	hotspotResult := applyHotspotShare(snapshot, baseline, snapshotPath, storedForChain.HotspotShare)
	// 自愈结果随响应回给前端（快照损坏 / 上次基线还原失败）：都只影响
	// 历史残留，本次免流照常，因此前端只记日志、不弹窗。
	//
	// DNS 自动修复失败只进 helper.log，不在这里提示：它多半是没提权（免流
	// 本身这时也起不来）或系统状态异常，用户无从操作，每次启动飘一条红字
	// 只是噪音。诊断信息在 helper.log 里。
	var networkNotice any
	if leftover.Warning != "" {
		networkNotice = map[string]any{"warning": leftover.Warning, "failed": leftover.Failed}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"running": true, "hotspot": hotspotResult, "controllerSecret": controllerSecret, "core": core.Version, "network": networkNotice})
}

// applyHotspotShare 执行移动热点上游切换（ICS 拓扑方案）。开关关 → disabled；
// 热点未运行 → not-running；切换失败 → 回滚基线并返回 failed。成功时把
// Hotspot 元数据写回快照，供 /status 探测与停止免流时还原。任何失败都只
// 体现在返回值里，不影响免流本身（非阻断决策）。
//
// snapshot 是本次会话刚抓的现状（判断/切换都用它）；baseline 是要落盘的
// 基线，正常与 snapshot 相同，上次自愈失败时是保留下来的旧基线。
func applyHotspotShare(snapshot network.Snapshot, baseline network.Snapshot, snapshotPath string, enabled bool) map[string]any {
	if !enabled {
		return map[string]any{"applied": false, "reason": "disabled"}
	}
	if len(snapshot.Sharing) == 0 {
		return map[string]any{"applied": false, "reason": "failed", "error": "未取到 ICS 共享基线，无法安全切换"}
	}
	// 探测/切换用独立超时（Background 而非请求上下文）：客户端断开也不能
	// 把系统留在半切换状态。
	detectCtx, detectCancel := context.WithTimeout(context.Background(), icsDetectTimeout)
	status, err := network.DetectHotspot(detectCtx)
	detectCancel()
	if err != nil {
		return map[string]any{"applied": false, "reason": "failed", "error": err.Error()}
	}
	if !status.Active {
		return map[string]any{"applied": false, "reason": "not-running"}
	}
	switchCtx, switchCancel := context.WithTimeout(context.Background(), icsOpTimeout)
	defer switchCancel()
	// 先落「准备切换」的占位元数据，再动 ICS：角色是在 PowerShell 脚本里改的，
	// 脚本执行中途被强杀时进程没有任何机会写回元数据，快照就成了「有 Sharing、
	// 没有 Hotspot」—— 那种形状要么被自愈当成无事发生（热点共享一直坏下去），
	// 要么只能靠事后猜。留痕这一步把窗口封死。
	baseline.Hotspot = &network.HotspotMeta{Applied: true, Path: network.HotspotPathPending}
	if err := network.SaveSnapshot(snapshotPath, baseline); err != nil {
		return map[string]any{"applied": false, "reason": "failed", "error": "保存热点共享状态: " + err.Error()}
	}
	meta, switchErr := switchHotspotTopology(switchCtx, status, snapshot, func(pending network.HotspotMeta) error {
		baseline.Hotspot = &pending
		return network.SaveSnapshot(snapshotPath, baseline)
	})
	if switchErr != nil {
		// 切换没成：把共享角色还原。热点本身有没有被拉回来由
		// switchHotspotTopology 负责（WinRT 失败时它会用探测到的原上游当场
		// 拉起）；只有「热点还关着」时它才会带回一份 winrt 记录 —— 把它留在
		// 基线里，下次启动的自愈才有机会再拉一次。热点没被碰过、或已经恢复时
		// 必须清掉标记，否则停止时会白跑一次还原。
		baseline.Hotspot = nil
		if meta != nil && meta.OriginalProfile != "" {
			baseline.Hotspot = meta
		}
		_ = network.SaveSnapshot(snapshotPath, baseline)
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), icsRecoverTimeout)
		rollbackErr := network.RestoreICSSharing(rollbackCtx, snapshot)
		rollbackCancel()
		if rollbackErr != nil {
			return map[string]any{"applied": false, "reason": "failed",
				"error": fmt.Sprintf("%v；回滚失败: %v", switchErr, rollbackErr)}
		}
		return map[string]any{"applied": false, "reason": "failed", "error": switchErr.Error()}
	}
	baseline.Hotspot = meta
	if err := network.SaveSnapshot(snapshotPath, baseline); err != nil {
		// 元数据落不了盘 → 停止时无法还原 → 也算失败并回滚。同样换新 ctx：
		// 切换可能已经把 switchCtx 的预算用光。
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), icsRecoverTimeout)
		defer rollbackCancel()
		var rollbackErrs []string
		if meta.Path == "winrt" {
			// 用恢复版而不是还原版：切换成功后热点一定在跑，跑不起来的状态
			// 恰恰是最需要拉起来的。
			if recoverErr := network.RecoverWinRT(rollbackCtx, meta.OriginalProfile); recoverErr != nil {
				rollbackErrs = append(rollbackErrs, recoverErr.Error())
			}
		}
		if icsErr := network.RestoreICSSharing(rollbackCtx, snapshot); icsErr != nil {
			rollbackErrs = append(rollbackErrs, icsErr.Error())
		}
		message := "保存热点共享状态: " + err.Error()
		if len(rollbackErrs) > 0 {
			message += "；回滚失败: " + strings.Join(rollbackErrs, "; ")
		}
		return map[string]any{"applied": false, "reason": "failed", "error": message}
	}
	// restarted=true 表示本路径重启过移动热点（WinRT 路径必然 stop→start），
	// 前端据此把“热点短暂断开”作为预期事件记入日志而不是当成故障。
	return map[string]any{"applied": true, "path": meta.Path, "restarted": meta.Path == "winrt"}
}

// switchHotspotTopology 路径选择：识别到热点私有侧网卡 → 经典 ICS 切换；
// 否则（新 WDI 驱动无独立虚拟适配器）或经典路径失败 → WinRT 重绑兜底。
//
// persistPending 在停掉热点之前被调用，用来把「原上游」写进快照：tethering 的
// 停与重开在同一个脚本里，进程被杀在中间时热点会停在关闭状态，而脚本的输出
// 要跑完才有 —— 不先落盘，下次启动就没有东西可还原。
//
// WinRT 路径的全部前提是「知道原上游」：探测不到就不切换。那个脚本会先停
// 热点、再绑 TUN，没有可恢复的上游等于做一件无法撤销的事 —— 失败时用户的
// 热点停着、其他设备断网，而程序没有任何办法把它拉回来。
func switchHotspotTopology(ctx context.Context, status network.HotspotStatus, snapshot network.Snapshot,
	persistPending func(network.HotspotMeta) error) (*network.HotspotMeta, error) {
	var icsErr error
	if status.PrivateAlias != "" {
		if _, err := network.SwitchICS(ctx, status.PrivateAlias); err == nil {
			return &network.HotspotMeta{Applied: true, Path: "ics", PrivateIP: status.PrivateIP}, nil
		} else {
			icsErr = err
			// 经典路径可能已改动角色：先回滚再走兜底，避免两套状态叠加。
			if rollbackErr := network.RestoreICSSharing(ctx, snapshot); rollbackErr != nil {
				return nil, fmt.Errorf("ics: %v（回滚失败: %v）", icsErr, rollbackErr)
			}
		}
	}
	probedProfile := network.ProbeWinRTOriginalProfile(ctx)
	if probedProfile == "" {
		return nil, fmt.Errorf("未能识别移动热点当前的上游连接，已跳过切换（热点保持原样）")
	}
	if persistPending != nil {
		if err := persistPending(network.HotspotMeta{
			Applied: true, Path: "winrt", PrivateIP: status.PrivateIP, OriginalProfile: probedProfile,
		}); err != nil {
			return nil, fmt.Errorf("保存热点共享原上游: %w", err)
		}
	}
	result, err := network.SwitchWinRT(ctx, probedProfile)
	if err != nil {
		// 脚本里的停与启不是原子的：TUN 绑定失败、或者脚本被超时/强杀打断，
		// 热点都可能停在关闭状态（脚本自己的回滚未必跑到）。
		//
		// 只在「热点可能还停着」时才去恢复：准备阶段的失败（TUN/原上游连接
		// 不存在、热点本来就没开）热点分毫未动，脚本自己回滚成功时也已经在
		// 原上游上跑着了 —— 这两种情况再去「恢复」会白白把它停一次，甚至因为
		// 同一个原因再也拉不起来。恢复必须用新 ctx：切换失败往往就是超时，
		// 复用那个 ctx 等于立刻放弃。
		var down *network.HotspotMeta
		switch {
		case result.Stage == "recovered":
			// 脚本自己已经把热点拉回原上游，错误消息里也写明了，不再重复一次。
			err = fmt.Errorf("winrt: %w", err)
		case result.NeedsHotspotRecovery():
			if recoverErr := recoverWinRTHotspot(probedProfile); recoverErr != nil {
				err = fmt.Errorf("winrt: %w；热点未能自动恢复到 %s（%v），请手动打开移动热点", err, probedProfile, recoverErr)
				// 热点还关着：把 winrt 记录留在基线里，下次启动的自愈走的是
				// 「热点关着也拉起」的恢复路径，还能再试一次 —— 清掉标记就等于
				// 连重试的机会都没有了（正常停止会删快照，所以这条只对「本次
				// 会话没有正常停止」的情况生效）。
				down = &network.HotspotMeta{Applied: true, Path: "winrt", PrivateIP: status.PrivateIP, OriginalProfile: probedProfile}
			} else {
				err = fmt.Errorf("winrt: %w；热点已恢复到原上游 %s", err, probedProfile)
			}
		default:
			err = fmt.Errorf("winrt: %w（热点未改动）", err)
		}
		if icsErr != nil {
			return down, fmt.Errorf("ics: %v; %v", icsErr, err)
		}
		return down, err
	}
	originalProfile := result.OriginalProfile
	if originalProfile == "" {
		originalProfile = probedProfile
	}
	return &network.HotspotMeta{Applied: true, Path: "winrt", PrivateIP: status.PrivateIP, OriginalProfile: originalProfile}, nil
}

// recoverWinRTHotspot 在 WinRT 切换失败后把热点拉回原上游。它必须用新的 ctx：
// 切换失败往往就是超时，复用那个 ctx 会立刻失败、等于没试。
func recoverWinRTHotspot(originalProfile string) error {
	recoverCtx, cancel := context.WithTimeout(context.Background(), icsRecoverTimeout)
	defer cancel()
	return network.RecoverWinRT(recoverCtx, originalProfile)
}

func (h *helper) stopProxy(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	// 顺序与失败策略都收在 internal/lifecycle：还原热点 → 关 TUN → 停核 →
	// 重读 → 清路由 → 删快照。用 Background 而不是请求上下文：客户端断开
	// 不能把一次热点还原砍在半截。
	result, stopErr := runStopSession(context.Background(), h.root)
	h.logStopResult(result)
	if stopErr != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": stopErr.Error()})
		return
	}
	// 记下停止完成的时刻：紧接着的 IPv6 前置检查据此判断"还在过渡态"。
	h.markStopped()
	// 不论热点还原成败都回 200：否则前端会把"已停止"读成"还在运行"。失败
	// 通过结构化字段回传，快照保留给下次启动自愈。
	response := map[string]any{"running": false}
	if result.RestoreErr != nil {
		response["restoreError"] = result.RestoreErr.Error()
	}
	if result.RestoreSkipped != nil {
		response["restoreSkipped"] = result.RestoreSkipped.Error()
	}
	writeJSON(writer, http.StatusOK, response)
}

func (h *helper) registerWarp(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var input registerRequest
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 4<<10)).Decode(&input); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if strings.TrimSpace(input.Name) == "" {
		input.Name = "freev6-windows"
	}
	device, err := warp.NewClient(nil).Register(request.Context(), input.Name)
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if err := writePrivateJSON(filepath.Join(h.root, "state", "warp.json"), device); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"registered": true, "deviceId": device.DeviceID})
}

func (h *helper) loadSettings() (settings, error) {
	var current settings
	data, err := os.ReadFile(filepath.Join(h.root, "config", "settings.json"))
	if err != nil {
		return current, err
	}
	if err := json.Unmarshal(data, &current); err != nil {
		return current, err
	}
	if current.CampusCIDRs == nil {
		current.CampusCIDRs = defaultCampusCIDRs()
	}
	if current.Blacklist == nil {
		current.Blacklist = []string{}
	}
	return current, nil
}

// jsonKeyPresent reports whether the top level of a JSON object body carries
// the given key. Used by the settings PUT so an omitted field means "leave the
// stored value alone" rather than "reset to false" (bool zero value).
func jsonKeyPresent(body []byte, key string) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	_, ok := probe[key]
	return ok
}

// mergeStartSettings overlays a proxy-start request onto the stored settings:
// mode/campus CIDRs/devMode reflect what actually started, while fields the
// request never carries (the manually edited blacklist) are preserved.
func mergeStartSettings(stored settings, input proxyRequest) settings {
	stored.Mode = input.Mode
	stored.CampusCIDRs = input.CampusCIDRs
	stored.DevMode = input.DevMode
	return stored
}

func (h *helper) saveSettings(current settings) error {
	path := filepath.Join(h.root, "config", "settings.json")
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o600)
}

// updateSettings runs fn under the settings write lock: it re-reads the stored
// settings, lets fn mutate them in place, then persists atomically. Every
// handler that performs a settings read-modify-write must go through here so
// concurrent writers cannot clobber each other. Side effects (network fetch,
// schtasks, chain parsing) should be completed before this call. A corrupt
// settings file is surfaced as an error instead of being silently overwritten.
func (h *helper) updateSettings(fn func(*settings) error) error {
	h.settingsMu.Lock()
	defer h.settingsMu.Unlock()
	current, err := h.loadSettings()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("settings.json 无法解析，请在设置中重置: %w", err)
	}
	if err != nil {
		current = defaultSettings()
	}
	if fnErr := fn(&current); fnErr != nil {
		return fnErr
	}
	return h.saveSettings(current)
}

// mergeSettingsOnto copies the "omitted means keep stored value" fields from
// src into dst, so a partial PUT never silently resets chain/cloud/hotspot/etc.
// to their zero values.
func mergeSettingsOnto(dst *settings, src settings, body []byte) {
	if dst.Blacklist == nil {
		dst.Blacklist = src.Blacklist
	}
	if !jsonKeyPresent(body, "autoRunProxy") {
		dst.AutoRunProxy = src.AutoRunProxy
	}
	if !jsonKeyPresent(body, "hotkeyEnabled") {
		dst.HotkeyEnabled = src.HotkeyEnabled
	}
	if !jsonKeyPresent(body, "hotspotShare") {
		dst.HotspotShare = src.HotspotShare
	}
	if !jsonKeyPresent(body, "chainEnabled") {
		dst.ChainEnabled = src.ChainEnabled
	}
	if !jsonKeyPresent(body, "chainSourceType") {
		dst.ChainSourceType = src.ChainSourceType
	}
	if !jsonKeyPresent(body, "chainSourceUrl") {
		dst.ChainSourceURL = src.ChainSourceURL
	}
	if !jsonKeyPresent(body, "cloudEnabled") {
		dst.CloudEnabled = src.CloudEnabled
	}
	if !jsonKeyPresent(body, "cloudSourceUrl") {
		dst.CloudSourceURL = src.CloudSourceURL
	}
	if !jsonKeyPresent(body, "cloudUpdatedAt") {
		dst.CloudUpdatedAt = src.CloudUpdatedAt
	}
	if !jsonKeyPresent(body, "portalHost") {
		dst.PortalHost = src.PortalHost
	}
}

func readDevice(path string) (warp.Device, error) {
	var device warp.Device
	data, err := os.ReadFile(path)
	if err != nil {
		return device, err
	}
	if err := json.Unmarshal(data, &device); err != nil {
		return device, err
	}
	return device, nil
}

func writePrivateJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o600)
}

// writeFileAtomic writes data to path through a unique temp file, fsync and an
// atomic rename, so a crash or a concurrent reader never observes a truncated
// settings/state file. The rename is retried briefly for Windows sharing
// violations (AV/backup software holding a transient handle).
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := os.Rename(tmpName, path); err == nil {
			return nil
		} else {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
		}
	}
	return lastErr
}

func executableRoot() string {
	if configuredRoot := strings.TrimSpace(os.Getenv("FREEV6_ROOT")); configuredRoot != "" {
		return configuredRoot
	}
	executable, err := os.Executable()
	if err != nil {
		return "."
	}
	root := filepath.Dir(executable)
	if strings.EqualFold(filepath.Base(root), "state") {
		return filepath.Dir(root)
	}
	return root
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// Echo the Origin only when it is allow-listed; a foreign origin gets
		// no Access-Control-Allow-Origin, so the browser never exposes the
		// response to it. OPTIONS is answered further down the chain (after the
		// Host/Origin guard, before the token check).
		if origin := request.Header.Get("Origin"); origin != "" && allowedOrigin(origin) {
			writer.Header().Set("Access-Control-Allow-Origin", origin)
			writer.Header().Add("Vary", "Origin")
		}
		// Authorization must be allow-listed or the browser preflight rejects
		// the bearer token before the request ever reaches requireAuth. POST
		// and DELETE were both missing from this list even though action
		// endpoints use them — a rejected preflight surfaces in the UI as an
		// opaque "Failed to fetch" (chain-clear's DELETE hit exactly this).
		writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		writer.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, OPTIONS")
		next.ServeHTTP(writer, request)
	})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func diagnosticError(err error, logPath string) string {
	message := err.Error()
	tail, tailErr := readFileTail(logPath, 4000)
	if tailErr != nil || tail == "" {
		return message
	}
	return message + "; mihomo log: " + tail
}

// readFileTail returns the last maxBytes of path without slurping the whole
// file — mihomo.log grows to tens of MB over a long session and this runs
// while holding the proxy start/stop lock.
func readFileTail(path string, maxBytes int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	offset := int64(0)
	if info.Size() > maxBytes {
		offset = info.Size() - maxBytes
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
