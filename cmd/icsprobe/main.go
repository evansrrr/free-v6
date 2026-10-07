//go:build ignore

// 只读探测探针：go run cmd/icsprobe/main.go（构建标签 ignore 使常规 build/test 不受影响）。
// 只执行 DetectHotspot / CaptureSharing —— 不修改任何 ICS 或 tethering 状态。
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/yourname/freev6/internal/network"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := network.DetectHotspot(ctx)
	fmt.Printf("detect: %+v\n  err=%v\n", st, err)
	sharing, err := network.CaptureSharing(ctx)
	if err != nil {
		fmt.Printf("capture err=%v\n", err)
		return
	}
	for _, s := range sharing {
		fmt.Printf("  %-45s enabled=%v kind=%s device=%s\n", s.Name, s.Enabled, s.Kind, s.Device)
	}
	fmt.Printf("ICSApplied(tun+privateAlias)=%v\n", network.ICSApplied(sharing, st.PrivateAlias))
}
