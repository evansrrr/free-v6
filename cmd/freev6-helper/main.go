package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type helper struct {
	root string
	mu   sync.Mutex
}

type settings struct {
	Mode        string   `json:"mode"`
	CampusCIDRs []string `json:"campusCidrs"`
	Blacklist   []string `json:"blacklist"`
	DevMode     bool     `json:"devMode"`
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
	root := executableRoot()
	h := &helper{root: root}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", h.status)
	mux.HandleFunc("/api/v1/runtime", h.runtime)
	mux.HandleFunc("/api/v1/runtime/download", h.downloadRuntime)
	mux.HandleFunc("/api/v1/settings", h.settings)
	mux.HandleFunc("/api/v1/proxy/start", h.startProxy)
	mux.HandleFunc("/api/v1/proxy/stop", h.stopProxy)
	mux.HandleFunc("/api/v1/warp/register", h.registerWarp)
	server := &http.Server{Addr: listenAddress, Handler: withCORS(mux), ReadHeaderTimeout: 5 * time.Second}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok":         true,
		"root":       h.root,
		"admin":      admin,
		"adminError": adminErr,
		"proxy":      map[string]any{"running": running, "pid": pid, "error": errorText(statusErr)},
		"warp":       map[string]any{"registered": warpErr == nil},
		"settings":   currentSettings,
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
		var current settings
		if err := json.NewDecoder(request.Body).Decode(&current); err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		if current.Mode == "" {
			current.Mode = mihomo.ModeRule
		}
		if current.Mode != mihomo.ModeRule && current.Mode != mihomo.ModeGlobal {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "mode must be rule or global"})
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
		// The GUI no longer sends the blacklist — when the client omits the
		// field, keep the value from settings.json (edit it there manually).
		if current.Blacklist == nil {
			if persisted, loadErr := h.loadSettings(); loadErr == nil {
				current.Blacklist = persisted.Blacklist
			}
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
	// The blacklist lives only in settings.json (no GUI editor) — load it
	// fresh so manual file edits apply the next time the proxy starts.
	blacklist := []string{}
	if stored, storeErr := h.loadSettings(); storeErr == nil {
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
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("write mihomo config: %v", err)})
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
	if _, err := mihomo.Start(binaryPath, configPath, pidPath, logPath); err != nil {
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
	_ = h.saveSettings(settings{Mode: input.Mode, CampusCIDRs: input.CampusCIDRs})
	writeJSON(writer, http.StatusOK, map[string]any{"running": true})
}

func (h *helper) stopProxy(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	pidPath := filepath.Join(h.root, "state", "mihomo.pid")
	if err := mihomo.Stop(pidPath); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	snapshotPath := filepath.Join(h.root, "state", "network-snapshot.json")
	snapshot, err := network.LoadSnapshot(snapshotPath)
	if err == nil {
		ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
		restoreErr := network.RestoreSnapshot(ctx, snapshot)
		cancel()
		if restoreErr != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": restoreErr.Error()})
			return
		}
		_ = os.Remove(snapshotPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
