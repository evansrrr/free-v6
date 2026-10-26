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
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// mihomoGitHubZipURL 是被镜像前缀拼接的发布资产本体；版本与下方
// expectedRuntimeSHA256 必须一起升级。
const mihomoGitHubZipURL = "https://github.com/MetaCubeX/mihomo/releases/download/v1.19.31/mihomo-windows-amd64-v3-v1.19.31.zip"

// expectedRuntimeSHA256 是 GitHub release API 对该资产公布的 digest。
// 多镜像意味着多个不受控分发方，下载的又是会被执行的文件——哈希必须
// 与固定值比对，不匹配按该线路失败处理（投毒/截断/换包都到此为止）。
// var 形式便于测试注入本地构造的载荷摘要。
var expectedRuntimeSHA256 = "f1a81678517dee871b51858f2a62d190d014b389057c67db575d1ddba892d19b"

// runtimeDownloadURLs 按优先级排列：gh-proxy 家族是本项目核心/更新下载
// 的大陆加速通道（geodata.go 同源），按 2026-10 校园网实测 1MB 首段延迟
// 排序（v6/v4 最快，现役 gh-proxy.com 保留前三）；github 直连兜底给
// 国际可达的网络。变量形式便于测试注入。
var runtimeDownloadURLs = []string{
	"https://v6.gh-proxy.org/" + mihomoGitHubZipURL,
	"https://v4.gh-proxy.org/" + mihomoGitHubZipURL,
	"https://gh-proxy.com/" + mihomoGitHubZipURL,
	"https://axisnow.gh-proxy.org/" + mihomoGitHubZipURL,
	"https://cdn.gh-proxy.org/" + mihomoGitHubZipURL,
	mihomoGitHubZipURL,
}

// 单镜像预算：停滞 10s 内无字节进展立即切线路（挂死的 gh-proxy 只烧掉
// 10 秒，慢而有进展的下载不被误杀）；硬上限兜住持续蠕动的镜像。总预算
// 仍由调用方 context 控制（helper 3 分钟），镜像间互不共享份额——这正是
// geodata 镜像链踩过的坑（共享预算被首镜像吃光）。
var (
	runtimeStallTimeout      = 10 * time.Second
	runtimeMirrorHardTimeout = 60 * time.Second
	// 巡检粒度；测试压缩时长时一并调小。
	runtimeStallCheckInterval = time.Second
)

const maxRuntimeDownload = 150 << 20

type DownloadResult struct {
	Version string
	Path    string
	SHA256  string
}

func DownloadLatest(ctx context.Context, root string) (DownloadResult, error) {
	client := &http.Client{}
	var failures []string
	skipped := 0
	for index, source := range runtimeDownloadURLs {
		if ctx.Err() != nil {
			skipped = len(runtimeDownloadURLs) - index
			break
		}
		digest, temporaryPath, downloadErr := downloadRuntimeMirror(ctx, client, source)
		if downloadErr == nil {
			runtimeDir := filepath.Join(root, "runtime")
			if mkdirErr := os.MkdirAll(runtimeDir, 0o700); mkdirErr != nil {
				_ = os.Remove(temporaryPath)
				return DownloadResult{}, mkdirErr
			}
			outputPath, extractErr := extractRuntime(temporaryPath, runtimeDir)
			_ = os.Remove(temporaryPath)
			if extractErr == nil {
				return DownloadResult{Version: "v1.19.31", Path: outputPath, SHA256: digest}, nil
			}
			// 包体已通过 SHA256 校验仍解不开 → 环境问题而非线路问题，
			// 但换线路重试无害且可能掩盖不了磁盘错误；按线路失败记录。
			downloadErr = extractErr
		} else {
			_ = os.Remove(temporaryPath)
		}
		failures = append(failures, fmt.Sprintf("%s: %v", hostOf(source), downloadErr))
	}
	if skipped > 0 {
		failures = append(failures, fmt.Sprintf("剩余 %d 个镜像因总超时未尝试", skipped))
	}
	if len(failures) == 0 && ctx.Err() != nil {
		return DownloadResult{}, fmt.Errorf("下载 mihomo runtime 超时: %w", ctx.Err())
	}
	return DownloadResult{}, fmt.Errorf("下载 mihomo runtime 失败（已尝试 %d/%d 个镜像: %s），请检查网络后重试",
		len(failures), len(runtimeDownloadURLs), strings.Join(failures, "; "))
}

// downloadRuntimeMirror 拉取单个镜像到临时文件并校验固定 SHA256。返回
// 摘要与临时文件路径（调用方负责删除）。停滞 watchdog 与硬上限都通过
// 取消 mirrorCtx 中断在途读取：连接挂死、首字节不来、传输中断流都会在
// runtimeStallTimeout 内让位给下一条线路。
func downloadRuntimeMirror(ctx context.Context, client *http.Client, source string) (digest string, temporaryPath string, err error) {
	mirrorCtx, cancel := context.WithTimeout(ctx, runtimeMirrorHardTimeout)
	defer cancel()

	// watchdog 必须在 client.Do 之前启动：连接挂死/响应头不来的镜像会把
	// Do 本身阻塞住，等返回再巡检就只能靠硬上限兜底了。
	var received atomic.Int64
	stopWatchdog := startStallWatchdog(mirrorCtx, cancel, &received)
	defer stopWatchdog()

	request, requestErr := http.NewRequestWithContext(mirrorCtx, http.MethodGet, source, nil)
	if requestErr != nil {
		return "", "", requestErr
	}
	request.Header.Set("User-Agent", "freev6")

	response, doErr := client.Do(request)
	if doErr != nil {
		return "", "", fmt.Errorf("download mihomo runtime: %w", doErr)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("mihomo runtime download returned HTTP %s", response.Status)
	}

	temporary, createErr := os.CreateTemp("", "freev6-mihomo-*.zip")
	if createErr != nil {
		return "", "", createErr
	}
	// 捕获到局部变量：失败路径的显式 return 会把 named return 清空，
	// defer 若引用 named path 就删不掉临时文件。
	temporaryPath = temporary.Name()
	cleanupPath := temporaryPath
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(cleanupPath)
		}
	}()

	body := &countingReader{reader: response.Body, counter: &received}
	hash := sha256.New()
	limited := io.LimitReader(io.TeeReader(body, hash), maxRuntimeDownload)
	if _, copyErr := io.Copy(temporary, limited); copyErr != nil {
		return "", "", fmt.Errorf("save mihomo runtime download: %w", copyErr)
	}
	if closeErr := temporary.Close(); closeErr != nil {
		return "", "", closeErr
	}
	digest = hex.EncodeToString(hash.Sum(nil))
	if digest != expectedRuntimeSHA256 {
		return "", "", fmt.Errorf("sha256 与固定值不符（got %s…），镜像内容不可信", digest[:16])
	}
	keep = true
	return digest, temporaryPath, nil
}

// startStallWatchdog 在 received 连续 runtimeStallTimeout 内零增长时取消
// mirrorCtx（含尚无首字节的连接挂死阶段：lastChange 从尝试开始计时）。
// 返回的停止函数幂等，成功/失败路径都应调用以回收 goroutine。
func startStallWatchdog(mirrorCtx context.Context, cancel context.CancelFunc, received *atomic.Int64) func() {
	done := make(chan struct{})
	var stopped atomic.Bool
	go func() {
		lastBytes := int64(0)
		lastChange := time.Now()
		for {
			select {
			case <-mirrorCtx.Done():
				return
			case <-done:
				return
			case <-time.After(runtimeStallCheckInterval):
				current := received.Load()
				if current != lastBytes {
					lastBytes = current
					lastChange = time.Now()
					continue
				}
				if time.Since(lastChange) >= runtimeStallTimeout {
					cancel() // 零进展超时：中断在途读取，让调用方切线路
					return
				}
			}
		}
	}()
	return func() {
		if stopped.CompareAndSwap(false, true) {
			close(done)
		}
	}
}

// countingReader 给 watchdog 喂进度字节数（只统计真正从网络读到的量）。
type countingReader struct {
	reader  io.Reader
	counter *atomic.Int64
}

func (c *countingReader) Read(buffer []byte) (int, error) {
	read, err := c.reader.Read(buffer)
	if read > 0 {
		c.counter.Add(int64(read))
	}
	return read, err
}

func extractRuntime(archivePath, runtimeDir string) (string, error) {
	outputPath := filepath.Join(runtimeDir, "mihomo-windows-amd64-v3.exe")
	name := strings.ToLower(filepath.Base(archivePath))

	if strings.HasSuffix(name, ".zip") {
		archive, err := zip.OpenReader(archivePath)
		if err != nil {
			return "", fmt.Errorf("open mihomo archive: %w", err)
		}
		defer archive.Close()
		for _, file := range archive.File {
			lower := strings.ToLower(file.Name)
			if strings.HasSuffix(lower, ".exe") {
				input, err := file.Open()
				if err != nil {
					return "", err
				}
				output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
				if err == nil {
					_, err = io.Copy(output, io.LimitReader(input, maxRuntimeDownload))
				}
				_ = input.Close()
				_ = output.Close()
				if err != nil {
					return "", fmt.Errorf("extract mihomo runtime: %w", err)
				}
				return outputPath, nil
			}
		}
		return "", fmt.Errorf("mihomo zip contains no executable")
	}

	input, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer input.Close()
	reader, err := gzip.NewReader(input)
	if err != nil {
		return "", fmt.Errorf("open mihomo gzip: %w", err)
	}
	defer reader.Close()
	output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(output, io.LimitReader(reader, maxRuntimeDownload))
	closeErr := output.Close()
	if copyErr != nil {
		return "", fmt.Errorf("extract mihomo gzip: %w", copyErr)
	}
	if closeErr != nil {
		return "", closeErr
	}
	return outputPath, nil
}