package mihomo

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yourname/freev6/internal/stallhttp"
)

// 单镜像预算：停滞 10s 内无字节进展立即切线路（挂死的 gh-proxy 只烧掉
// 10 秒，慢而有进展的下载不被误杀）；硬上限兜住持续蠕动的镜像。总预算
// 仍由调用方 context 控制，镜像间互不共享份额——这正是 geodata 镜像链
// 踩过的坑（共享预算被首镜像吃光）。
var (
	runtimeStallTimeout      = 10 * time.Second
	runtimeMirrorHardTimeout = 60 * time.Second
	// 巡检粒度；测试压缩时长时一并调小。
	runtimeStallCheckInterval = time.Second
)

// maxRuntimeDownload 是内核包/可执行文件的大小上限，同时兜住恶意镜像的
// 无限流。38MB 的内核留足余量。
const maxRuntimeDownload = 150 << 20

// progressSink 接收下载进度：received 为当前镜像已传输字节数，total 为响应
// Content-Length（未知时为 -1）。回调在下载 goroutine 内随数据流入触发，
// 实现必须线程安全且廉价（helper 用原子计数器承载）。切换镜像时 received
// 会归零重算，前端进度条可能回跳，属预期行为。
type progressSink func(received, total int64)

// downloadAsset 按顺序尝试每条镜像，返回临时文件路径与它的 SHA256。
// 调用方负责删除临时文件。
//
// 三类失败一律换线：网络/HTTP 错误、SHA256 与期望不符、超时。多镜像意味着
// 多个不受控分发方，而下载的又是会被执行的文件——哈希不符必须当成"这条
// 线路的内容不可信"，而不是整体失败（换一条线常常就好了）。
func downloadAsset(ctx context.Context, urls []string, expectedSHA string, progress progressSink) (string, string, error) {
	client := &http.Client{}
	var failures []string
	skipped := 0
	for index, source := range urls {
		if ctx.Err() != nil {
			skipped = len(urls) - index
			break
		}
		path, digest, err := downloadMirror(ctx, client, source, expectedSHA, progress)
		if err == nil {
			return path, digest, nil
		}
		_ = os.Remove(path)
		failures = append(failures, fmt.Sprintf("%s: %v", hostOf(source), err))
	}
	if skipped > 0 {
		failures = append(failures, fmt.Sprintf("剩余 %d 个镜像因总超时未尝试", skipped))
	}
	if len(failures) == 0 && ctx.Err() != nil {
		return "", "", fmt.Errorf("下载超时: %w", ctx.Err())
	}
	return "", "", fmt.Errorf("下载失败（已尝试 %d/%d 个镜像: %s），请检查网络后重试",
		len(failures), len(urls), strings.Join(failures, "; "))
}

// downloadMirror 拉取单个镜像到临时文件并校验摘要。返回的路径即使出错也
// 可能是非空的临时文件（供调用方清理）。
func downloadMirror(ctx context.Context, client *http.Client, source, expectedSHA string, progress progressSink) (string, string, error) {
	mirrorCtx, cancel := context.WithTimeout(ctx, runtimeMirrorHardTimeout)
	defer cancel()

	// watchdog 必须在 client.Do 之前启动：连接挂死/响应头不来的镜像会把
	// Do 本身阻塞住，等返回再巡检就只能靠硬上限兜底了。
	var received atomic.Int64
	stopWatchdog := stallhttp.StartWatchdog(mirrorCtx, cancel, &received, runtimeStallTimeout, runtimeStallCheckInterval)
	defer stopWatchdog()

	request, err := http.NewRequestWithContext(mirrorCtx, http.MethodGet, source, nil)
	if err != nil {
		return "", "", err
	}
	request.Header.Set("User-Agent", "freev6")

	response, err := client.Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP %s", response.Status)
	}

	temporary, err := os.CreateTemp("", "freev6-core-*")
	if err != nil {
		return "", "", err
	}
	path := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()

	body := &stallhttp.CountingReader{Reader: response.Body, Counter: &received}
	hash := sha256.New()
	reader := io.Reader(io.TeeReader(body, hash))
	if progress != nil {
		reader = &progressReportingReader{r: reader, received: &received, total: response.ContentLength, onProgress: progress}
	}
	limited := io.LimitReader(reader, maxRuntimeDownload)
	if _, err := io.Copy(temporary, limited); err != nil {
		return path, "", err
	}
	if err := temporary.Close(); err != nil {
		return path, "", err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if expectedSHA != "" && !strings.EqualFold(digest, expectedSHA) {
		return path, digest, fmt.Errorf("sha256 不符（got %s…）", digest[:16])
	}
	keep = true
	return path, digest, nil
}

// progressReportingReader 在每次 Read 后上报当前字节进度。received 复用
// stall 计数器（CountingReader 已统计真正从网络读到的字节量），避免二次
// 计数与 watchdog 看到的字节数不一致。
type progressReportingReader struct {
	r          io.Reader
	received   *atomic.Int64
	total      int64
	onProgress progressSink
}

func (p *progressReportingReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if p.onProgress != nil && n > 0 {
		p.onProgress(p.received.Load(), p.total)
	}
	return n, err
}

// extractArchive 从 zip 或 gzip 里取出内核可执行文件写到 destPath。
//
// 按内容判断而不是扩展名：下载落在临时文件里，没有后缀可用。zip 只认第一个
// .exe 条目——f6-core 的发布包正好只有一个根级 exe。
func extractArchive(archivePath, destPath string) error {
	archive, zipErr := zip.OpenReader(archivePath)
	if zipErr == nil {
		defer archive.Close()
		for _, file := range archive.File {
			if !strings.HasSuffix(strings.ToLower(file.Name), ".exe") {
				continue
			}
			input, err := file.Open()
			if err != nil {
				return err
			}
			if err := writeLimited(input, destPath); err != nil {
				_ = input.Close()
				return err
			}
			return input.Close()
		}
		return fmt.Errorf("core zip contains no executable")
	}

	input, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer input.Close()
	reader, err := gzip.NewReader(input)
	if err != nil {
		return fmt.Errorf("不认识的发布包格式（zip: %v, gzip: %w）", zipErr, err)
	}
	defer reader.Close()
	return writeLimited(reader, destPath)
}

func writeLimited(source io.Reader, destPath string) error {
	output, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, io.LimitReader(source, maxRuntimeDownload)); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}
