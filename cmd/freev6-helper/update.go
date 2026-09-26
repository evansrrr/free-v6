package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// In-app updater download support: the frontend resolves the release asset
// through the GitHub API and PUTs {url, sha256} to /api/v1/update/download;
// the helper streams it to %TEMP% with progress counters while the frontend
// polls GET /api/v1/update/progress, verifying SHA256 before Rust launches
// the installer.
//
// The local API is CORS-open, so this endpoint must not become an
// arbitrary-file fetcher: only https GitHub release hosts plus the gitproxy
// acceleration mirror are accepted (loopback http is allowed for tests
// only).

var updateAllowedHosts = []string{
	"github.com",
	"objects.githubusercontent.com",
	"release-assets.githubusercontent.com",
	"api.gitproxy.dev", // 大陆访问加速代理（直通 github release 资产）
}

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

// downloadUpdateFile streams rawURL into dest and verifies sha256Hex.
// Redirects are followed by the client (github.com release assets redirect
// to GitHub's own CDN — trusted once github.com was validated).
func downloadUpdateFile(client *http.Client, rawURL, sha256Hex, dest string, allowed []string, prog *updateProgress) (int64, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, fmt.Errorf("invalid url")
	}
	loopback := hostAllowed(u.Hostname(), []string{"127.0.0.1", "localhost"})
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return 0, fmt.Errorf("unsupported url scheme: %s", u.Scheme)
	}
	if !hostAllowed(u.Hostname(), allowed) {
		return 0, fmt.Errorf("host not allowed: %s", u.Hostname())
	}
	if strings.TrimSpace(sha256Hex) == "" {
		return 0, fmt.Errorf("missing sha256")
	}
	resp, err := client.Get(rawURL)
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
	written, copyErr := io.Copy(counter, resp.Body)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(dest)
		return 0, fmt.Errorf("download interrupted: %w", copyErr)
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
	dest := filepath.Join(os.TempDir(), "freev6-update-setup.exe")

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
		// Generous timeout: big setup.exe on a slow link; the frontend poll
		// loop has its own cap for the UI side.
		client := &http.Client{Timeout: 30 * time.Minute}
		_, err := downloadUpdateFile(client, input.URL, input.SHA256, dest, updateAllowedHosts, &updateRun.prog)
		updateRun.mu.Lock()
		if err != nil {
			updateRun.state = "error"
			updateRun.err = err.Error()
			updateRun.busy = false
		} else {
			updateRun.state = "done"
			updateRun.path = dest
		}
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
