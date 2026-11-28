// Command boot-measure 测 helper「进程创建 → GET /api/v1/status 首次 200」的耗时。
//
// 为什么不用"启动桌面壳再掐表"：壳自身启动要几百毫秒，会把 helper 的 30ms
// 量级启动开销完全淹没，量出来的数字和改动无关。这里直接 spawn helper，
// 每 5ms 轮询一次，跑 n 次取分位数——批次 1/2 的"+<10ms"预算就用这把尺子量。
//
// 用法：
//
//	go run ./scripts/boot-measure -bin ./freev6-helper.exe -root /tmp/fv6-root
//
// 注意：-root 指向一个**临时空目录**，不要指向真实安装目录（helper 会往里写
// state/config）。-port 是一个起始端口，每次运行 +1，避免 TIME_WAIT 干扰。
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

func main() {
	bin := flag.String("bin", "./freev6-helper.exe", "helper 可执行文件路径")
	root := flag.String("root", filepath.Join(os.TempDir(), "fv6-boot-root"), "helper 的 FREEV6_ROOT（临时目录）")
	basePort := flag.Int("port", 38200, "起始端口，每次运行 +1")
	runs := flag.Int("n", 8, "运行次数")
	timeout := flag.Duration("timeout", 30*time.Second, "单次运行的等待上限")
	flag.Parse()

	if err := os.MkdirAll(*root, 0o700); err != nil {
		fail("创建临时 root: %v", err)
	}
	absBin, err := filepath.Abs(*bin)
	if err != nil {
		fail("解析 helper 路径: %v", err)
	}
	if _, err := os.Stat(absBin); err != nil {
		fail("找不到 helper: %v", err)
	}

	samples := make([]time.Duration, 0, *runs)
	for i := 0; i < *runs; i++ {
		elapsed, stderr, helperErr := measure(absBin, *root, *basePort+i, *timeout)
		if helperErr != nil {
			fmt.Fprintf(os.Stderr, "run %d 失败: %v\n%s\n", i+1, helperErr, stderr)
			os.Exit(1)
		}
		samples = append(samples, elapsed)
		fmt.Printf("run %d: %d ms\n", i+1, elapsed.Milliseconds())
		// 给系统一点时间回收进程与端口（TIME_WAIT 不影响新端口，但进程退出要收尾）。
		time.Sleep(200 * time.Millisecond)
	}

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	var sum time.Duration
	for _, s := range samples {
		sum += s
	}
	fmt.Printf("min=%dms p50=%dms mean=%dms max=%dms\n",
		samples[0].Milliseconds(),
		samples[len(samples)/2].Milliseconds(),
		(sum / time.Duration(len(samples))).Milliseconds(),
		samples[len(samples)-1].Milliseconds())
}

// measure 起一次 helper，返回从 exec.Start 到首个 200 的耗时。
// 结束时无论成败都杀掉子进程并回收句柄。
func measure(bin, root string, port int, timeout time.Duration) (time.Duration, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"FREEV6_ROOT="+root,
		// 固定 token：本工具只读 /status，不承担鉴权测试。
		"FREEV6_API_TOKEN=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		fmt.Sprintf("FREEV6_API_PORT=%d", port),
	)
	var stderr bytes.Buffer
	cmd.Stdout = nil
	cmd.Stderr = &stderr

	started := time.Now()
	if err := cmd.Start(); err != nil {
		return 0, "", err
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d/api/v1/status", port)
	client := &http.Client{Timeout: 300 * time.Millisecond}
	for {
		select {
		case <-ctx.Done():
			return 0, stderr.String(), fmt.Errorf("helper 未在 %v 内就绪", timeout)
		default:
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return 0, stderr.String(), err
		}
		request.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return time.Since(started), "", nil
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "boot-measure: "+format+"\n", args...)
	os.Exit(1)
}
