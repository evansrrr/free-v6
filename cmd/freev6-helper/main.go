package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/yourname/freev6/internal/mihomo"
	"github.com/yourname/freev6/internal/network"
)

const listenAddress = "127.0.0.1:13335"

type helper struct {
	root string
}

type settings struct {
	Mode        string   `json:"mode"`
	CampusCIDRs []string `json:"campusCidrs"`
}

func main() {
	root := executableRoot()
	h := &helper{root: root}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", h.status)
	mux.HandleFunc("/api/v1/runtime", h.runtime)
	mux.HandleFunc("/api/v1/settings", h.settings)
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
		currentSettings = settings{Mode: mihomo.ModeRule}
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok":         true,
		"root":       h.root,
		"admin":      admin,
		"adminError": adminErr,
		"proxy":      map[string]any{"running": running, "pid": pid, "error": errorText(statusErr)},
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
		for _, cidr := range current.CampusCIDRs {
			if _, _, err := net.ParseCIDR(strings.TrimSpace(cidr)); err != nil {
				writeJSON(writer, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid campus CIDR %q", cidr)})
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

func (h *helper) loadSettings() (settings, error) {
	var current settings
	data, err := os.ReadFile(filepath.Join(h.root, "config", "settings.json"))
	if err != nil {
		return current, err
	}
	if err := json.Unmarshal(data, &current); err != nil {
		return current, err
	}
	return current, nil
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
