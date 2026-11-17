package mihomo

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withDownloadTest 压缩镜像预算，让"挂死的镜像"在毫秒级被判死。
func withDownloadTest(t *testing.T) {
	t.Helper()
	stall, hard, interval := runtimeStallTimeout, runtimeMirrorHardTimeout, runtimeStallCheckInterval
	runtimeStallTimeout = 200 * time.Millisecond
	runtimeMirrorHardTimeout = 2 * time.Second
	runtimeStallCheckInterval = 20 * time.Millisecond
	t.Cleanup(func() {
		runtimeStallTimeout, runtimeMirrorHardTimeout, runtimeStallCheckInterval = stall, hard, interval
	})
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestDownloadAssetFallsBackToNextMirror(t *testing.T) {
	withDownloadTest(t)
	payload := []byte("core archive bytes")
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer broken.Close()
	working := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer working.Close()

	path, digest, err := downloadAsset(context.Background(), []string{broken.URL, working.URL}, sha256Hex(payload), nil)
	if err != nil {
		t.Fatalf("expected fallback to succeed, got %v", err)
	}
	defer os.Remove(path)
	if digest != sha256Hex(payload) {
		t.Fatalf("digest mismatch: %s", digest)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("downloaded payload mismatch: %v", err)
	}
}

// 摘要不符必须换线，而不是直接失败：多镜像里只有一条被投毒是常态。
func TestDownloadAssetSkipsMirrorWithWrongDigest(t *testing.T) {
	withDownloadTest(t)
	payload := []byte("honest bytes")
	poisoned := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tampered bytes"))
	}))
	defer poisoned.Close()
	honest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer honest.Close()

	path, _, err := downloadAsset(context.Background(), []string{poisoned.URL, honest.URL}, sha256Hex(payload), nil)
	if err != nil {
		t.Fatalf("expected the honest mirror to win, got %v", err)
	}
	defer os.Remove(path)
}

func TestDownloadAssetErrorsWhenAllMirrorsFail(t *testing.T) {
	withDownloadTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer server.Close()

	if _, _, err := downloadAsset(context.Background(), []string{server.URL}, "", nil); err == nil {
		t.Fatal("expected error")
	} else if !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("error should mention the HTTP status: %v", err)
	}
}

// 停滞的镜像必须在 stall 预算内被换掉，否则首启会卡死在一条挂掉的线上。
func TestDownloadAssetSwitchesAwayFromStalledMirror(t *testing.T) {
	withDownloadTest(t)
	payload := []byte("slow but alive")
	release := make(chan struct{})
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		_, _ = w.Write([]byte("x")) // 吐一个字节后挂住
		w.(http.Flusher).Flush()
		<-release
	}))
	defer stalled.Close()
	defer close(release)
	working := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer working.Close()

	start := time.Now()
	path, _, err := downloadAsset(context.Background(), []string{stalled.URL, working.URL}, sha256Hex(payload), nil)
	if err != nil {
		t.Fatalf("expected stall failover, got %v", err)
	}
	defer os.Remove(path)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("stall failover took too long: %s", elapsed)
	}
}

func TestExtractArchiveZipPicksExecutable(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("f6-core-windows-amd64-v3.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("exe payload")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "core.zip")
	if err := os.WriteFile(archivePath, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "core.exe")
	if err := extractArchive(archivePath, dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "exe payload" {
		t.Fatalf("extracted payload mismatch: %q %v", data, err)
	}
}

func TestExtractArchiveGzip(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "core.gz")
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write([]byte("gzip payload")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "core")
	if err := extractArchive(archivePath, dest); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != "gzip payload" {
		t.Fatalf("extracted payload mismatch: %q %v", data, err)
	}
}

func TestExtractArchiveZipWithoutExecutable(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	if _, err := writer.Create("README.md"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "core.zip")
	if err := os.WriteFile(archivePath, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(archivePath, filepath.Join(dir, "core.exe")); err == nil {
		t.Fatal("expected an error for an archive without an executable")
	}
}
