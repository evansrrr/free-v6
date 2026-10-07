package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yourname/freev6/internal/mihomo"
	"github.com/yourname/freev6/internal/network"
	"github.com/yourname/freev6/internal/warp"
)

const listenAddress = "127.0.0.1:13335"

// icsOpTimeout 覆盖 WinRT tethering 异步操作的最坏情况（Stop/Start 各
// 最多 Wait-Op 60s），正常几百毫秒完成；探测单独给短超时。
const (
	icsOpTimeout     = 120 * time.Second
	icsDetectTimeout = 30 * time.Second
)

type helper struct {
	root string
	mu   sync.Mutex
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
	// Task Manager's Startup tab runs the HKCU Run marker (helper --autostart)
	// at logon; the real launch is the "FreeV6 Autostart" logon task. Exit
	// before binding the API port so the marker never collides with the helper
	// the desktop app spawns.
	if len(os.Args) > 1 && os.Args[1] == "--autostart" {
		return
	}
	root := executableRoot()
	h := &helper{root: root}
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
	mux.HandleFunc("/api/v1/appearance", h.appearance)
	mux.HandleFunc("/api/v1/runtime/download", h.downloadRuntime)
	mux.HandleFunc("/api/v1/settings", h.settings)
	mux.HandleFunc("/api/v1/proxy/start", h.startProxy)
	mux.HandleFunc("/api/v1/proxy/stop", h.stopProxy)
	mux.HandleFunc("/api/v1/warp/register", h.registerWarp)
	mux.HandleFunc("/api/v1/update/download", h.updateDownload)
	mux.HandleFunc("/api/v1/update/progress", h.updateProgress)
	mux.HandleFunc("/api/v1/update/latest", h.updateLatest)
	server := &http.Server{Addr: listenAddress, Handler: withCORS(mux), ReadHeaderTimeout: 5 * time.Second}
	// Bind with orphan takeover: killing the desktop via Task Manager leaves
	// its helper child alive (Windows doesn't cascade-kill), and that orphan
	// otherwise holds :13335 forever — every relaunch died on the bind error.
	listener, err := listenOrTakeOver(listenAddress)
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
	if settingsErr != nil {
		currentSettings = defaultSettings()
	}
	_, warpErr := os.Stat(filepath.Join(h.root, "state", "warp.json"))
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
		"ok":         true,
		"root":       h.root,
		"admin":      admin,
		"adminError": adminErr,
		"proxy":      map[string]any{"running": running, "pid": pid, "error": errorText(statusErr)},
		"warp":       map[string]any{"registered": warpErr == nil},
		"settings":   currentSettings,
		"hotspot":    hotspot,
	})
}

func (h *helper) runtime(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	path, err := mihomo.DiscoverBinary(h.root)
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok":           true,
		"present":      err == nil,
		"path":         path,
		"error":        errorText(err),
		"platform":     runtime.GOOS,
		"architecture": runtime.GOARCH,
	})
}

func (h *helper) downloadRuntime(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 3*time.Minute)
	defer cancel()
	result, err := mihomo.DownloadLatest(ctx, h.root)
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"present": true,
		"version": result.Version,
		"path":    result.Path,
		"sha256":  result.SHA256,
	})
}

func (h *helper) settings(writer http.ResponseWriter, request *http.Request) {
	path := filepath.Join(h.root, "config", "settings.json")
	switch request.Method {
	case http.MethodGet:
		current, err := h.loadSettings()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if current.Mode == "" {
			current.Mode = mihomo.ModeRule
		}
		if current.CampusCIDRs == nil {
			current.CampusCIDRs = defaultCampusCIDRs()
		}
		writeJSON(writer, http.StatusOK, current)
	case http.MethodPut:
		body, err := io.ReadAll(request.Body)
		if err != nil {
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
		if loadErr != nil {
			persisted = defaultSettings()
		}
		// The GUI never sends the blacklist — when the client omits the field,
		// keep the value from settings.json (edit it there manually).
		if current.Blacklist == nil {
			current.Blacklist = persisted.Blacklist
		}
		// Same contract for autoRunProxy: a client that predates the switch
		// (or a partial PUT) must not silently turn 自动运行免流 off.
		if !jsonKeyPresent(body, "autoRunProxy") {
			current.AutoRunProxy = persisted.AutoRunProxy
		}
		// 同上：省略 hotkeyEnabled 时保留存储值，否则 bool 零值会悄悄解绑 F6
		if !jsonKeyPresent(body, "hotkeyEnabled") {
			current.HotkeyEnabled = persisted.HotkeyEnabled
		}
		// 同上：省略 hotspotShare 时保留存储值，否则 bool 零值会悄悄关掉热点共享
		if !jsonKeyPresent(body, "hotspotShare") {
			current.HotspotShare = persisted.HotspotShare
		}
		blacklistEntries := make([]string, 0, len(current.Blacklist))
		for _, entry := range current.Blacklist {
			if err := mihomo.ValidateDomain(entry); err != nil {
				writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			blacklistEntries = append(blacklistEntries, strings.ToLower(strings.TrimSpace(entry)))
		}
		current.Blacklist = blacklistEntries
		// Autostart toggles the Task Scheduler task plus its Task Manager marker
		// entry as a side effect, BEFORE the file is written: a failure returns
		// 500 so the GUI reverts the switch instead of claiming a state that
		// never took effect.
		if persisted.AutoStart != current.AutoStart {
			var syncErr error
			if current.AutoStart {
				syncErr = applyAutoStart(h.root)
			} else {
				syncErr = removeAutoStart()
			}
			if syncErr != nil {
				writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": syncErr.Error()})
				return
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		data, _ := json.MarshalIndent(current, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(writer, http.StatusOK, current)
	default:
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *helper) startProxy(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var input proxyRequest
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if input.Mode == "" {
		input.Mode = mihomo.ModeRule
	}
	if input.CampusCIDRs == nil {
		input.CampusCIDRs = defaultCampusCIDRs()
	}
	if err := mihomo.CheckListenPorts(); err != nil {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": err.Error() + "; please close other proxy/DNS software first"})
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
	device, err := readDevice(filepath.Join(h.root, "state", "warp.json"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("read WARP state: %v", err)})
		return
	}
	// Default blacklist is compiled into the binary (blacklist.txt embedded
	// at build time); a non-empty settings.json blacklist overrides it.
	blacklist, blErr := mihomo.DefaultBlacklist()
	if blErr != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": blErr.Error()})
		return
	}
	if stored, storeErr := h.loadSettings(); storeErr == nil && len(stored.Blacklist) > 0 {
		blacklist = stored.Blacklist
	}
	config, err := mihomo.RenderWithOptions(device, mihomo.RenderOptions{Mode: input.Mode, CampusCIDRs: input.CampusCIDRs, Blacklist: blacklist, DevMode: input.DevMode})
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	stateDir := filepath.Join(h.root, "state")
	configPath := filepath.Join(stateDir, "mihomo.yaml")
	pidPath := filepath.Join(stateDir, "mihomo.pid")
	logPath := filepath.Join(stateDir, "mihomo.log")
	snapshotPath := filepath.Join(stateDir, "network-snapshot.json")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// GeoSite.dat 硬依赖预置（新机器首启故障修复）：配置的 nameserver-policy
	// 引用 geosite，缺文件时 mihomo 启动阶段自己去 github 直连下载，大陆
	// 网络下挂起 → controller 就绪 15s 超时，真正原因（Can't find GeoSite.dat
	// start download）被盖住。这里先用镜像准备好；失败在快照/系统改动之前
	// 快速返回可读错误。
	geoCtx, geoCancel := context.WithTimeout(context.Background(), mihomo.GeodataDownloadTimeout)
	geoDownloaded, geoErr := mihomo.EnsureGeodata(geoCtx, stateDir)
	geoCancel()
	if geoErr != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("准备地理数据失败: %v", geoErr)})
		return
	}
	if geoDownloaded {
		fmt.Fprintf(os.Stderr, "helper: downloaded %s\n", mihomo.GeositeFileName)
	}
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("write mihomo config: %v", err)})
		return
	}
	// 残留自愈：走到这里说明端口检查已通过（mihomo 必未在跑）。上次会话
	// 异常退出可能留下旧快照（ICS 仍处于切换态）——先按它还原再重新抓取，
	// 否则新基线会把“已切换状态”记成原状，停止时永远还原不回去。
	if old, healErr := network.LoadSnapshot(snapshotPath); healErr == nil {
		healCtx, healCancel := context.WithTimeout(context.Background(), icsOpTimeout)
		err := network.RestoreSnapshot(healCtx, old)
		healCancel()
		if err != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("restore leftover network snapshot: %v", err)})
			return
		}
		_ = os.Remove(snapshotPath)
	} else if !errors.Is(healErr, os.ErrNotExist) {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": healErr.Error()})
		return
	}
	snapshotCtx, snapshotCancel := context.WithTimeout(request.Context(), 10*time.Second)
	snapshot, err := network.CaptureSnapshot(snapshotCtx)
	snapshotCancel()
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := network.SaveSnapshot(snapshotPath, snapshot); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	restore := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = network.RestoreSnapshot(ctx, snapshot)
	}
	binaryPath, err := mihomo.DiscoverBinary(h.root)
	if err != nil {
		restore()
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if _, err := mihomo.Start(binaryPath, configPath, stateDir, pidPath, logPath); err != nil {
		restore()
		writeJSON(writer, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	if err := mihomo.WaitReady(ctx, "127.0.0.1:9090", 100*time.Millisecond); err != nil {
		_ = mihomo.Stop(pidPath)
		restore()
		writeJSON(writer, http.StatusBadGateway, map[string]string{"error": diagnosticError(err, logPath)})
		return
	}
	if _, err := mihomo.CheckController(ctx, "127.0.0.1:9090"); err != nil {
		_ = mihomo.Stop(pidPath)
		restore()
		writeJSON(writer, http.StatusBadGateway, map[string]string{"error": diagnosticError(err, logPath)})
		return
	}
	// Persist onto the FULL stored settings — saving a partial struct here
	// used to wipe blacklist/devMode on every proxy start, so the developer
	// switch reverted after a page refresh.
	stored, storeErr := h.loadSettings()
	if storeErr != nil {
		stored = defaultSettings()
	}
	_ = h.saveSettings(mergeStartSettings(stored, input))
	// 热点共享（设置 → 通用，非阻断）：mihomo 就绪后才切 ICS，失败回滚
	// 到启动前基线；结果随响应带回前端（成功记日志、失败弹日志界面）。
	hotspotResult := applyHotspotShare(snapshot, snapshotPath, stored.HotspotShare)
	writeJSON(writer, http.StatusOK, map[string]any{"running": true, "hotspot": hotspotResult})
}

// applyHotspotShare 执行移动热点上游切换（ICS 拓扑方案）。开关关 → disabled；
// 热点未运行 → not-running；切换失败 → 回滚基线并返回 failed。成功时把
// Hotspot 元数据写回快照，供 /status 探测与停止免流时还原。任何失败都只
// 体现在返回值里，不影响免流本身（非阻断决策）。
func applyHotspotShare(snapshot network.Snapshot, snapshotPath string, enabled bool) map[string]any {
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
	meta, switchErr := switchHotspotTopology(switchCtx, status, snapshot)
	if switchErr != nil {
		if rollbackErr := network.RestoreICSSharing(switchCtx, snapshot); rollbackErr != nil {
			return map[string]any{"applied": false, "reason": "failed",
				"error": fmt.Sprintf("%v；回滚失败: %v", switchErr, rollbackErr)}
		}
		return map[string]any{"applied": false, "reason": "failed", "error": switchErr.Error()}
	}
	snapshot.Hotspot = meta
	if err := network.SaveSnapshot(snapshotPath, snapshot); err != nil {
		// 元数据落不了盘 → 停止时无法还原 → 也算失败并回滚。
		if meta.Path == "winrt" {
			_ = network.RestoreWinRT(switchCtx, meta.OriginalProfile)
		}
		_ = network.RestoreICSSharing(switchCtx, snapshot)
		return map[string]any{"applied": false, "reason": "failed", "error": "保存热点共享状态: " + err.Error()}
	}
	return map[string]any{"applied": true, "path": meta.Path}
}

// switchHotspotTopology 路径选择：识别到热点私有侧网卡 → 经典 ICS 切换；
// 否则（新 WDI 驱动无独立虚拟适配器）或经典路径失败 → WinRT 重绑兜底。
func switchHotspotTopology(ctx context.Context, status network.HotspotStatus, snapshot network.Snapshot) (*network.HotspotMeta, error) {
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
	originalProfile, err := network.SwitchWinRT(ctx)
	if err != nil {
		if icsErr != nil {
			return nil, fmt.Errorf("ics: %v; winrt: %w", icsErr, err)
		}
		return nil, err
	}
	return &network.HotspotMeta{Applied: true, Path: "winrt", PrivateIP: status.PrivateIP, OriginalProfile: originalProfile}, nil
}

func (h *helper) stopProxy(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	pidPath := filepath.Join(h.root, "state", "mihomo.pid")
	snapshotPath := filepath.Join(h.root, "state", "network-snapshot.json")
	// 先还原再停进程：ICS/WinRT 还原要在 TUN 适配器仍在时完成（FreeV6TUN
	// 连接存在才能显式禁用其共享角色）。还原失败时快照保留、mihomo 照常
	// 停止，下次启动免流前会先按它自愈还原。
	var restoreErr error
	snapshot, err := network.LoadSnapshot(snapshotPath)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), icsOpTimeout)
		restoreErr = network.RestoreSnapshot(ctx, snapshot)
		cancel()
	} else if !errors.Is(err, os.ErrNotExist) {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if restoreErr == nil {
		_ = os.Remove(snapshotPath)
	}
	if stopErr := mihomo.Stop(pidPath); stopErr != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": stopErr.Error()})
		return
	}
	if restoreErr != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": restoreErr.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"running": false})
}

func (h *helper) registerWarp(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var input registerRequest
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
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
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		writer.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		writer.Header().Set("Access-Control-Allow-Methods", "GET, PUT, OPTIONS")
		if request.Method == http.MethodOptions {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
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
	data, readErr := os.ReadFile(logPath)
	if readErr != nil {
		return message
	}
	const maxTail = 4000
	if len(data) > maxTail {
		data = data[len(data)-maxTail:]
	}
	logTail := strings.TrimSpace(string(data))
	if logTail == "" {
		return message
	}
	return message + "; mihomo log: " + logTail
}
