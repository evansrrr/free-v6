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
//
// geoip.dat / country.mmdb 被 geox-url 引用（GEOIP 规则用）：mihomo 的
// geox-url 每个文件只能配一条 URL，无法多线路——预置链把它们的下载从
// 「mihomo 内部单线路」换成「多镜像抢先下载」，失败仅告警不阻塞启动
// （mihomo 仍可经 geox-url 兜底或降级运行）。
const (
	GeositeFileName = "GeoSite.dat"
	GeoipFileName   = "geoip.dat"
	MmdbFileName    = "country.mmdb"
)

// geositeDownloadURLs 按优先级排列：v6/v4.gh-proxy 是核心/更新下载同族
// 的加速入口（2026-10 校园网实测最快）；gh-proxy 是现役通道；testingcf
// jsdelivr 是 mihomo 官方文档给出的镜像；github 直连兜底（校园网 IPv6
// 国际可达时可用）。变量形式便于测试注入。
var geositeDownloadURLs = []string{
	"https://v6.gh-proxy.org/https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geosite.dat",
	"https://gh-proxy.com/https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geosite.dat",
	"https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/geosite.dat",
	"https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geosite.dat",
}

// geoip / mmdb 与 geosite 同仓库发布，走同一套 gh-proxy 前缀镜像 + 直连。
var geoipDownloadURLs = []string{
	"https://v6.gh-proxy.org/https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.dat",
	"https://v4.gh-proxy.org/https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.dat",
	"https://gh-proxy.com/https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.dat",
	"https://axisnow.gh-proxy.org/https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.dat",
	"https://cdn.gh-proxy.org/https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.dat",
	"https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.dat",
}

var mmdbDownloadURLs = []string{
	"https://v6.gh-proxy.org/https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/country.mmdb",
	"https://v4.gh-proxy.org/https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/country.mmdb",
	"https://gh-proxy.com/https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/country.mmdb",
	"https://axisnow.gh-proxy.org/https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/country.mmdb",
	"https://cdn.gh-proxy.org/https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/country.mmdb",
	"https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/country.mmdb",
}

const (
	// GeodataDownloadTimeout 是首启预置 geo 数据的总预算：三个资产共用，
	// 每个镜像各自 20s 独立超时。120s 覆盖 4+6+6 条镜像链在慢网下的组合，
	// 正常情况（前两条镜像就命中）三个文件 ~20s 内完成。
	GeodataDownloadTimeout = 120 * time.Second
	// 真实体积：geosite ~4MB、geoip ~11MB、mmdb ~6MB；低于下限视为
	// 损坏/占位文件，镜像回的 HTML 错误页同理（体积+首字节双重校验）。
	minGeositeSize = 64 << 10
	minGeoipSize   = 1 << 20
	minMmdbSize    = 256 << 10
	maxGeositeSize = 32 << 20
)

// geodataPerMirrorTimeout 是单个镜像的独立超时（变量便于测试压缩时长）。
// 故障案例：某镜像在部分机器上完全挂住，共享的总预算被第一个镜像吃光，
// 后备镜像根本没机会跑（"已尝试 1 个镜像"）。
var geodataPerMirrorTimeout = 20 * time.Second

// geoAsset 描述一个需要预置的 geo 数据文件。hard=true 的失败阻塞免流
// 启动；best-effort 资产只告警——mihomo 仍可能经 geox-url 兜底或降级，
// 预置的意义是把「mihomo 内部单线路下载」换成「多镜像抢先下载」。
type geoAsset struct {
	name    string
	urls    []string
	minSize int64
	hard    bool
}

// geodataAssetList 在每次调用时读取当前的镜像链变量——存成包级切片会
// 在初始化时快照切片头，测试注入的替换将永远不生效（单测因此触过真实
// 网络）。
func geodataAssetList() []geoAsset {
	return []geoAsset{
		{GeositeFileName, geositeDownloadURLs, minGeositeSize, true},
		{GeoipFileName, geoipDownloadURLs, minGeoipSize, false},
		{MmdbFileName, mmdbDownloadURLs, minMmdbSize, false},
	}
}

// GeodataValid 判断磁盘上的 geosite 是否可直接使用（体积下限 + 拒绝
// HTML 错误页）。镜像故障时常回一页 <html> 且状态码 200。
func GeodataValid(path string) bool {
	return geodataValidWithMin(path, minGeositeSize)
}

func geodataValidWithMin(path string, minSize int64) bool {
	info, err := os.Stat(path)
	if err != nil || info.Size() < minSize || info.Size() > maxGeositeSize {
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

// EnsureGeodata 确保 homeDir 下的 geo 数据资产就绪。已有效 → 直接跳过；
// 否则按镜像顺序下载（ctx 控制总超时），临时文件 + rename 落盘，任何
// 一半的下载都不会被 mihomo 读到。返回 downloaded 表示本次是否真的下载
// 了任一资产。
func EnsureGeodata(ctx context.Context, homeDir string) (downloaded bool, err error) {
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		return false, fmt.Errorf("create geodata directory: %w", err)
	}
	client := &http.Client{}
	for _, asset := range geodataAssetList() {
		if ctx.Err() != nil {
			if asset.hard {
				return downloaded, fmt.Errorf("下载 %s 超时: %w", asset.name, ctx.Err())
			}
			fmt.Fprintf(os.Stderr, "freev6: skip best-effort %s: %v\n", asset.name, ctx.Err())
			continue
		}
		created, assetErr := ensureGeoAsset(ctx, client, homeDir, asset)
		downloaded = downloaded || created
		if assetErr != nil {
			if asset.hard {
				return downloaded, assetErr
			}
			fmt.Fprintf(os.Stderr, "freev6: prepare %s failed (best-effort): %v\n", asset.name, assetErr)
		}
	}
	return downloaded, nil
}

func ensureGeoAsset(ctx context.Context, client *http.Client, homeDir string, asset geoAsset) (bool, error) {
	path := filepath.Join(homeDir, asset.name)
	if geodataValidWithMin(path, asset.minSize) {
		return false, nil
	}
	// 1) 安装包内置副本：零网络。homeDir 是 state/，它的上级就是安装根
	// 目录（bundle.resources 把 GeoSite.dat 放在那里）；runtime/ 兼容手动
	// 放置的旧办法。开发机仓库里 state/ 自带有效文件时同样走不到网络。
	if seed := findSeedGeodata(homeDir, asset.name); seed != "" {
		if copyErr := copyGeodata(seed, path); copyErr == nil && geodataValidWithMin(path, asset.minSize) {
			return true, nil
		}
		_ = os.Remove(path) // 种子损坏/半截：清掉继续走镜像
	}
	// 2) 镜像链：每个镜像独立超时，挂死的镜像不会占用后备镜像的预算。
	var failures []string
	skipped := 0
	for index, source := range asset.urls {
		if ctx.Err() != nil {
			skipped = len(asset.urls) - index
			break
		}
		mirrorCtx, cancel := context.WithTimeout(ctx, geodataPerMirrorTimeout)
		downloadErr := downloadGeodata(mirrorCtx, client, source, path, asset.minSize)
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
		return false, fmt.Errorf("下载 %s 超时: %w", asset.name, ctx.Err())
	}
	return false, fmt.Errorf("下载 %s 失败（已尝试 %d/%d 个镜像: %s），请检查网络后重试",
		asset.name, len(failures), len(asset.urls), strings.Join(failures, "; "))
}

// findSeedGeodata 查找随安装包分发/手动放置的副本，返回首个有效的路径。
func findSeedGeodata(homeDir, name string) string {
	root := filepath.Dir(homeDir) // state/ 的上级 = 安装根目录/仓库根
	candidates := []string{
		filepath.Join(root, name),
		filepath.Join(root, "runtime", name),
	}
	for _, candidate := range candidates {
		if file, err := os.Open(candidate); err == nil {
			_ = file.Close()
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
func downloadGeodata(ctx context.Context, client *http.Client, source, path string, minSize int64) error {
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
	temporary, err := os.CreateTemp(filepath.Dir(path), "geodata-*.tmp")
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
	if written < minSize {
		return fmt.Errorf("文件过小 (%d bytes)", written)
	}
	// HTML 错误页通常状态码也是 200（镜像回退页），按内容再校验一次。
	if !geodataValidWithMin(temporaryPath, minSize) {
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
