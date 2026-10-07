package mihomo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withGeositeMirrors 临时替换镜像链（测试专用）。
func withGeositeMirrors(t *testing.T, urls ...string) {
	t.Helper()
	original := geositeDownloadURLs
	geositeDownloadURLs = urls
	t.Cleanup(func() { geositeDownloadURLs = original })
}

// 有效的 geodata：体积达标且不是 HTML。
func writeValidGeodata(t *testing.T, path string) {
	t.Helper()
	content := append([]byte("\n\x00\x01valid-geodata-"), make([]byte, minGeositeSize)...)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureGeodataSkipsWhenValid(t *testing.T) {
	home := t.TempDir()
	writeValidGeodata(t, filepath.Join(home, GeositeFileName))
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer server.Close()
	withGeositeMirrors(t, server.URL)

	downloaded, err := EnsureGeodata(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if downloaded {
		t.Fatal("valid file must not trigger a download")
	}
	if hits != 0 {
		t.Fatalf("mirror must not be hit, got %d", hits)
	}
}

func TestEnsureGeodataDownloadsFromMirror(t *testing.T) {
	home := t.TempDir()
	payload := append([]byte("\x00\x01payload-"), make([]byte, minGeositeSize)...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	withGeositeMirrors(t, server.URL)

	downloaded, err := EnsureGeodata(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if !downloaded {
		t.Fatal("missing file must trigger a download")
	}
	got, err := os.ReadFile(filepath.Join(home, GeositeFileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(payload) {
		t.Fatalf("written size %d != payload %d", len(got), len(payload))
	}
	// 二次调用不再下载
	downloaded, err = EnsureGeodata(context.Background(), home)
	if err != nil || downloaded {
		t.Fatalf("second call must be a no-op: downloaded=%v err=%v", downloaded, err)
	}
}

func TestEnsureGeodataFallsBackToNextMirror(t *testing.T) {
	home := t.TempDir()
	payload := append([]byte("\x00\x01ok-"), make([]byte, minGeositeSize)...)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer good.Close()
	withGeositeMirrors(t, bad.URL, good.URL)

	downloaded, err := EnsureGeodata(context.Background(), home)
	if err != nil || !downloaded {
		t.Fatalf("fallback must succeed: downloaded=%v err=%v", downloaded, err)
	}
}

// 镜像回 200 + HTML 错误页 → 内容校验拒绝，继续下一个镜像。
func TestEnsureGeodataRejectsHTMLErrorPage(t *testing.T) {
	home := t.TempDir()
	html := "<!DOCTYPE html><html><body>oops</body></html>" + strings.Repeat(" ", minGeositeSize)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(html))
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(append([]byte{0, 1}, make([]byte, minGeositeSize)...))
	}))
	defer good.Close()
	withGeositeMirrors(t, bad.URL, good.URL)

	if downloaded, err := EnsureGeodata(context.Background(), home); err != nil || !downloaded {
		t.Fatalf("HTML page must be rejected and fall back: downloaded=%v err=%v", downloaded, err)
	}
}

// 磁盘上已有但损坏（过小）的文件必须触发重新下载，而不是直接放行。
func TestEnsureGeodataRedownloadsCorruptFile(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, GeositeFileName)
	if err := os.WriteFile(path, []byte("tiny"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(append([]byte{0, 1}, make([]byte, minGeositeSize)...))
	}))
	defer server.Close()
	withGeositeMirrors(t, server.URL)

	if downloaded, err := EnsureGeodata(context.Background(), home); err != nil || !downloaded {
		t.Fatalf("corrupt file must be replaced: downloaded=%v err=%v", downloaded, err)
	}
	if !geodataValid(path) {
		t.Fatal("file must be valid after re-download")
	}
}

func TestEnsureGeodataAllMirrorsFail(t *testing.T) {
	home := t.TempDir()
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer failing.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer other.Close()
	withGeositeMirrors(t, failing.URL, other.URL)

	downloaded, err := EnsureGeodata(context.Background(), home)
	if err == nil || downloaded {
		t.Fatalf("expected failure, got downloaded=%v err=%v", downloaded, err)
	}
	// 错误要可读：包含文件名与镜像 host（便于用户报告问题）
	if !strings.Contains(err.Error(), GeositeFileName) || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error must name the file and mirrors: %v", err)
	}
	// 失败不能留半截文件
	if _, statErr := os.Stat(filepath.Join(home, GeositeFileName)); statErr == nil {
		t.Fatal("failed download must not leave a GeoSite.dat behind")
	}
}

// 超时的 ctx 要快速返回而不是挂住。
func TestEnsureGeodataHonorsContext(t *testing.T) {
	home := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
	}))
	defer server.Close()
	withGeositeMirrors(t, server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := EnsureGeodata(ctx, home)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("must fail fast on expired context, took %v", elapsed)
	}
}
