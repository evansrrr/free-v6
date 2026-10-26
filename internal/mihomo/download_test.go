package mihomo

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// withRuntimeDownloadTest 注入镜像链并压缩 watchdog 时长（测试专用）。
func withRuntimeDownloadTest(t *testing.T, urls ...string) {
	t.Helper()
	originalURLs := runtimeDownloadURLs
	originalSHA := expectedRuntimeSHA256
	originalStall := runtimeStallTimeout
	originalHard := runtimeMirrorHardTimeout
	originalInterval := runtimeStallCheckInterval
	runtimeDownloadURLs = urls
	runtimeStallTimeout = 300 * time.Millisecond
	runtimeMirrorHardTimeout = 3 * time.Second
	runtimeStallCheckInterval = 50 * time.Millisecond
	t.Cleanup(func() {
		runtimeDownloadURLs = originalURLs
		expectedRuntimeSHA256 = originalSHA
		runtimeStallTimeout = originalStall
		runtimeMirrorHardTimeout = originalHard
		runtimeStallCheckInterval = originalInterval
	})
}

// makeRuntimeZip 构造一个含 .exe 的合法 zip 及其 SHA256 摘要。
func makeRuntimeZip(t *testing.T, payload string) (archive []byte, digest string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("mihomo-windows-amd64-v3.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buffer.Bytes())
	return buffer.Bytes(), hex.EncodeToString(sum[:])
}

func serveRuntimeZip(archive []byte) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(archive)
	})
}

func TestDownloadRuntimeSuccessFirstMirror(t *testing.T) {
	archive, digest := makeRuntimeZip(t, "fake mihomo executable")
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		serveRuntimeZip(archive).ServeHTTP(writer, request)
	}))
	defer server.Close()
	withRuntimeDownloadTest(t, server.URL)
	expectedRuntimeSHA256 = digest

	result, err := DownloadLatest(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("first mirror must win alone, hits = %d", hits)
	}
	if result.SHA256 != digest {
		t.Fatalf("result digest %q != %q", result.SHA256, digest)
	}
	if info, statErr := os.Stat(result.Path); statErr != nil || info.Size() == 0 {
		t.Fatalf("extracted runtime missing or empty: %v", statErr)
	}
}

// 挂死的镜像必须在停滞窗口内被 watchdog 切掉，后备镜像拿到机会——
// 回归线上故障「context deadline exceeded」（首镜像挂死吃光共享预算）。
func TestDownloadRuntimeHungMirrorCutByStallWatchdog(t *testing.T) {
	archive, digest := makeRuntimeZip(t, "fake mihomo executable")
	hung := make(chan struct{})
	hungServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-hung // 不写响应、不返回：连接挂死
	}))
	defer hungServer.Close()
	defer close(hung)
	good := httptest.NewServer(serveRuntimeZip(archive))
	defer good.Close()
	withRuntimeDownloadTest(t, hungServer.URL, good.URL)
	expectedRuntimeSHA256 = digest

	start := time.Now()
	result, err := DownloadLatest(context.Background(), t.TempDir())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("fallback mirror must win: %v", err)
	}
	if result.SHA256 != digest {
		t.Fatalf("unexpected digest %q", result.SHA256)
	}
	// 停滞 300ms + 巡检粒度：远小于硬上限 3s。断言 < 2s 证明走的是停滞
	// 快速通道而不是硬上限。
	if elapsed > 2*time.Second {
		t.Fatalf("hung mirror must be cut by stall watchdog (~300ms), took %v", elapsed)
	}
}

// 持续蠕动（慢但不停滞）的镜像由硬上限兜底切掉。
func TestDownloadRuntimeHardCapCutsTricklingMirror(t *testing.T) {
	archive, digest := makeRuntimeZip(t, "fake mihomo executable")
	trickle := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		flusher, ok := writer.(http.Flusher)
		if !ok {
			return
		}
		_, _ = writer.Write([]byte{0x50, 0x4b}) // zip magic，制造「有进展」的假象
		flusher.Flush()
		for {
			select {
			case <-request.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
				_, _ = writer.Write([]byte{0x00})
				flusher.Flush()
			}
		}
	}))
	defer trickle.Close()
	good := httptest.NewServer(serveRuntimeZip(archive))
	defer good.Close()
	withRuntimeDownloadTest(t, trickle.URL, good.URL)
	expectedRuntimeSHA256 = digest
	// 停滞 800ms > 蠕动间隔 100ms：stall 不触发，只有硬上限 1.5s 能切。
	runtimeStallTimeout = 800 * time.Millisecond
	runtimeMirrorHardTimeout = 1500 * time.Millisecond

	start := time.Now()
	result, err := DownloadLatest(context.Background(), t.TempDir())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("fallback must win after hard cap: %v", err)
	}
	if result.SHA256 != digest {
		t.Fatalf("unexpected digest %q", result.SHA256)
	}
	if elapsed < time.Second {
		t.Fatalf("trickling mirror must be cut by hard cap (~1.5s), took %v", elapsed)
	}
}

// 镜像内容与固定 SHA256 不符 → 该线路失败；换线后可用载荷仍能成功。
func TestDownloadRuntimeRejectsDigestMismatch(t *testing.T) {
	goodArchive, _ := makeRuntimeZip(t, "legit executable")
	tampered, _ := makeRuntimeZip(t, "tampered executable")
	_, goodDigest := makeRuntimeZip(t, "legit executable")
	expectedRuntimeSHA256 = goodDigest
	bad := httptest.NewServer(serveRuntimeZip(tampered))
	defer bad.Close()
	good := httptest.NewServer(serveRuntimeZip(goodArchive))
	defer good.Close()

	t.Run("mismatch alone fails with readable error", func(t *testing.T) {
		withRuntimeDownloadTest(t, bad.URL)
		expectedRuntimeSHA256 = goodDigest
		_, err := DownloadLatest(context.Background(), t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "sha256") {
			t.Fatalf("expected sha256 mismatch error, got %v", err)
		}
	})

	t.Run("mismatch falls back to the next mirror", func(t *testing.T) {
		withRuntimeDownloadTest(t, bad.URL, good.URL)
		expectedRuntimeSHA256 = goodDigest
		result, err := DownloadLatest(context.Background(), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if result.SHA256 != goodDigest {
			t.Fatalf("unexpected digest %q", result.SHA256)
		}
	})
}

func TestDownloadRuntimeAllMirrorsFail(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "boom", http.StatusInternalServerError)
	}))
	defer failing.Close()
	other := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "gone", http.StatusNotFound)
	}))
	defer other.Close()
	withRuntimeDownloadTest(t, failing.URL, other.URL)

	_, err := DownloadLatest(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("expected failure")
	}
	// 错误要可读：镜像数量 + host（便于用户报告问题）
	if !strings.Contains(err.Error(), "已尝试 2/2") || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error must aggregate mirrors: %v", err)
	}
}

// 总预算过期时快速返回，并说明剩余镜像未尝试。
func TestDownloadRuntimeHonorsExpiredContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	withRuntimeDownloadTest(t, server.URL, server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := DownloadLatest(ctx, t.TempDir())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("must fail fast on expired context, took %v", elapsed)
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("expected timeout wording, got %v", err)
	}
}
