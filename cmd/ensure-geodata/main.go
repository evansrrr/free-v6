// Command ensure-geodata 在 tauri 构建前确保仓库根目录有有效的 GeoSite.dat，
// 供 tauri.conf.json 的 bundle.resources 把它带进安装包 —— 用户首启时
// EnsureGeodata 直接从安装根目录抄副本，零网络、零镜像依赖。
//
// 顺序：已有有效文件 → 抄 state/GeoSite.dat（开发机自带）→ 走镜像下载。
// 任一步成功即可；全失败则以非零退出码中止构建（错误信息可读）。
//
// 挂接：beforeBuildCommand / beforeDevCommand 的 `go run ./cmd/ensure-geodata`。
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/yourname/freev6/internal/mihomo"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ensure-geodata: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dir := "."
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	target := filepath.Join(dir, mihomo.GeositeFileName)
	if mihomo.GeodataValid(target) {
		return nil // 已有有效文件（含上一次构建留下的），不动
	}
	// 开发机：仓库里 state/ 有现成的有效副本，直接抄，不走网络。
	stateCopy := filepath.Join(dir, "state", mihomo.GeositeFileName)
	if mihomo.GeodataValid(stateCopy) {
		if err := copyFile(stateCopy, target); err != nil {
			return fmt.Errorf("copy %s from state: %w", mihomo.GeositeFileName, err)
		}
		fmt.Fprintf(os.Stderr, "ensure-geodata: seeded %s from state/\n", target)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), mihomo.GeodataDownloadTimeout)
	defer cancel()
	created, err := mihomo.EnsureGeodata(ctx, dir)
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(os.Stderr, "ensure-geodata: downloaded %s\n", target)
	}
	return nil
}

func copyFile(source, dest string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
