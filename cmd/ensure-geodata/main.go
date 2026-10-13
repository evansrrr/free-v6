// 在 tauri 构建前确保仓库根目录有 GeoSite.dat，它会被打包进安装包，首次启动直接使用
// fallback 从镜像下载，全失败则以非零 exit code 中止构建
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
		return nil // 已有文件
	}
	// 开发机 state/ 有现成副本
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
