package mihomo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GeodataDownloadTimeout 是首启预置 GeoSite.dat 的总超时：gitproxy 加速
// 下 4MB 通常几秒，校园网慢链路给足一分钟；到点后 EnsureGeodata 返回
// 可读错误，不再让前端挂住。
const GeodataDownloadTimeout = 60 * time.Second

// GeoSite.dat 是启动的硬依赖：渲染的配置在 nameserver-policy 里引用了
// geosite 分类，缺文件时 mihomo 会在「Start initial configuration」阶段
// 自己去 github 直连下载 —— 大陆网络下挂起，controller 永远起不来，
// 表现为 wait for mihomo controller 超时（新机器首启的经典故障）。
// EnsureGeodata 在拉起核心前用可直连的镜像把文件准备好，失败则快速
// 报出可读错误，而不是让 15s 就绪超时把真相盖住。
const GeositeFileName = "GeoSite.dat"

// geositeDownloadURLs 按优先级排列：gitproxy 是本项目核心/更新下载
// 已验证的大陆加速通道；testingcf.jsdelivr 是 mihomo 官方文档给出的
// 镜像；github 直连兜底（校园网 IPv6 国际可达时可用）。
// 变量形式便于测试注入。
var geositeDownloadURLs = []string{
	"https://api.gitproxy.dev/github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geosite.dat",
	"https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/geosite.dat",
	"https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geosite.dat",
}

const (
	// 真实 geosite.dat 约 4MB；低于该值视为损坏/占位文件。
	minGeositeSize = 64 << 10
	maxGeositeSize = 32 << 20
)

// geodataValid 判断磁盘上的 geodata 是否可直接使用：体积下限兜底 +
// 拒绝 HTML 错误页（镜像故障时常回一页 <html>）。
func geodataValid(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.Size() < minGeositeSize || info.Size() > maxGeositeSize {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	head := make([]byte, 64)
	n, _ := file.Read(head)
	trimmed := bytes.TrimLeft(head[:n], " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] != '<'
}

// EnsureGeodata 确保 homeDir/GeoSite.dat 存在且有效。已有效 → 直接返回；
// 否则按镜像顺序下载（ctx 控制总超时），临时文件 + rename 落盘，任何一半
// 的下载都不会被 mihomo 读到。返回 downloaded 表示本次是否真的下载了。
func EnsureGeodata(ctx context.Context, homeDir string) (downloaded bool, err error) {
	path := filepath.Join(homeDir, GeositeFileName)
	if geodataValid(path) {
		return false, nil
	}
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		return false, fmt.Errorf("create geodata directory: %w", err)
	}
	client := &http.Client{}
	var failures []string
	for _, source := range geositeDownloadURLs {
		if ctx.Err() != nil {
			break
		}
		if downloadErr := downloadGeodata(ctx, client, source, path); downloadErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", hostOf(source), downloadErr))
			continue
		}
		return true, nil
	}
	if len(failures) == 0 && ctx.Err() != nil {
		return false, fmt.Errorf("下载 %s 超时: %w", GeositeFileName, ctx.Err())
	}
	return false, fmt.Errorf("下载 %s 失败（已尝试 %d 个镜像: %s），请检查网络后重试",
		GeositeFileName, len(failures), strings.Join(failures, "; "))
}

// downloadGeodata 从单个镜像拉取并原子替换 path。
func downloadGeodata(ctx context.Context, client *http.Client, source, path string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "freev6")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", response.Status)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "geosite-*.dat")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath) // 成功时已被 rename 覆盖，删除是 no-op
	}()
	written, err := io.Copy(temporary, io.LimitReader(response.Body, maxGeositeSize))
	if err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if written < minGeositeSize {
		return fmt.Errorf("文件过小 (%d bytes)", written)
	}
	// HTML 错误页通常状态码也是 200（镜像回退页），按内容再校验一次。
	if !geodataValid(temporaryPath) {
		return fmt.Errorf("内容不是有效的 geodata（疑似错误页）")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

// hostOf 提取 URL 的 host 部分用于错误汇总（避免错误信息里刷完整链接）。
func hostOf(rawURL string) string {
	index := strings.Index(rawURL, "://")
	if index < 0 {
		return rawURL
	}
	rest := rawURL[index+3:]
	if slash := strings.Index(rest, "/"); slash >= 0 {
		rest = rest[:slash]
	}
	return rest
}
