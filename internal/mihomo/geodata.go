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

// GeoSite.dat 是启动的硬依赖：渲染的配置在 nameserver-policy 里引用了
// geosite 分类，缺文件时 mihomo 会在「Start initial configuration」阶段
// 自己去 github 直连下载 —— 大陆网络下挂起，controller 永远起不来，
// 表现为 wait for mihomo controller 超时（新机器首启的经典故障）。
// EnsureGeodata 在拉起核心前用可直连的镜像把文件准备好，失败则快速
// 报出可读错误，而不是让 15s 就绪超时把真相盖住。
const GeositeFileName = "GeoSite.dat"

// geositeDownloadURLs 按优先级排列：gh-proxy 是本项目核心/更新下载
// 使用的大陆加速通道；testingcf.jsdelivr 是 mihomo 官方文档给出的
// 镜像；github 直连兜底（校园网 IPv6 国际可达时可用）。
// 变量形式便于测试注入。
var geositeDownloadURLs = []string{
	"https://gh-proxy.com/https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geosite.dat",
	"https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/geosite.dat",
	"https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geosite.dat",
}

const (
	// GeodataDownloadTimeout 是首启预置 GeoSite.dat 的总预算：三个镜像
	// 各自 20s，正好 60s；单个镜像挂死只烧掉自己的份额，不会饿死后备。
	GeodataDownloadTimeout = 60 * time.Second
	// 真实 geosite.dat 约 4MB；低于该值视为损坏/占位文件。
	minGeositeSize = 64 << 10
	maxGeositeSize = 32 << 20
)

// geodataPerMirrorTimeout 是单个镜像的独立超时（变量便于测试压缩时长）。
// 故障案例：某镜像在部分机器上完全挂住，共享的 60s 预算被第一个镜像
// 吃光，jsdelivr/github 兑底根本没机会跑（"已尝试 1 个镜像"）。
var geodataPerMirrorTimeout = 20 * time.Second

// GeodataValid 判断磁盘上的 geodata 是否可直接使用：体积下限兜底 +
// 拒绝 HTML 错误页（镜像故障时常回一页 <html>）。
func GeodataValid(path string) bool {
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
func EnsureGeodata(ctx context.Context, homeDir string) (created bool, err error) {
	path := filepath.Join(homeDir, GeositeFileName)
	if GeodataValid(path) {
		return false, nil
	}
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		return false, fmt.Errorf("create geodata directory: %w", err)
	}
	// 1) 安装包内置副本：零网络。homeDir 是 state/，它的上级就是安装根
	// 目录（bundle.resources 把 GeoSite.dat 放在那里）；runtime/ 兼容手动
	// 放置的旧办法。开发机仓库里 state/ 自带有效文件时同样走不到网络。
	if seed := findSeedGeodata(homeDir); seed != "" {
		if copyErr := copyGeodata(seed, path); copyErr == nil && GeodataValid(path) {
			return true, nil
		}
		_ = os.Remove(path) // 种子损坏/半截：清掉继续走镜像
	}
	// 2) 镜像链：每个镜像独立超时，挂死的镜像不会占用后备镜像的预算。
	client := &http.Client{}
	var failures []string
	skipped := 0
	for index, source := range geositeDownloadURLs {
		if ctx.Err() != nil {
			skipped = len(geositeDownloadURLs) - index
			break
		}
		mirrorCtx, cancel := context.WithTimeout(ctx, geodataPerMirrorTimeout)
		downloadErr := downloadGeodata(mirrorCtx, client, source, path)
		cancel()
		if downloadErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", hostOf(source), downloadErr))
			continue
		}
		return true, nil
	}
	if skipped > 0 {
		failures = append(failures, fmt.Sprintf("剩余 %d 个镜像因总超时未尝试", skipped))
	}
	if len(failures) == 0 && ctx.Err() != nil {
		return false, fmt.Errorf("下载 %s 超时: %w", GeositeFileName, ctx.Err())
	}
	return false, fmt.Errorf("下载 %s 失败（已尝试 %d/%d 个镜像: %s），请检查网络后重试",
		GeositeFileName, len(failures), len(geositeDownloadURLs), strings.Join(failures, "; "))
}

// findSeedGeodata 查找随安装包分发/手动放置的副本，返回首个有效的路径。
func findSeedGeodata(homeDir string) string {
	root := filepath.Dir(homeDir) // state/ 的上级 = 安装根目录/仓库根
	candidates := []string{
		filepath.Join(root, GeositeFileName),
		filepath.Join(root, "runtime", GeositeFileName),
	}
	for _, candidate := range candidates {
		if GeodataValid(candidate) {
			return candidate
		}
	}
	return ""
}

// copyGeodata 流式拷贝种子文件到目标路径。
func copyGeodata(source, dest string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, io.LimitReader(input, maxGeositeSize))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
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
	if !GeodataValid(temporaryPath) {
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
