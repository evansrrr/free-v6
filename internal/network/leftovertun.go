package network

import (
	"context"
	"io"
)

// RouteCleanupLog is where route-cleanup diagnostics go. The helper and the CLI
// point it at stderr (helper.log) at startup so a failed netsh delete — e.g.
// running without elevation — leaves a trace instead of silently stranding the
// user with "an address but no egress". Tests leave it at io.Discard.
var RouteCleanupLog io.Writer = io.Discard

// shouldClearLeftoverTunRoutes 决定是否清理残留 TUN 路由。内核在跑时绝不动：
// 那些路由正是它写进去的；状态读不到时也不动，宁可留给下一处再试。
func shouldClearLeftoverTunRoutes(running bool, statusErr error, adapterPresent bool) bool {
	return statusErr == nil && !running && adapterPresent
}

// ClearLeftoverTunRoutes 清掉「内核没在跑、FreeV6TUN 却还在」留下的 auto-route
// 残留：停止被强杀在关 TUN 的中途时，适配器还在、默认路由仍指向它，但没有
// 会话接收流量，出站会一直超时（重启网卡也不恢复）。只删路由、不删适配器：
// 残留热点共享的还原需要 FreeV6TUN 连接仍然存在。返回清掉的条数。
//
// 内核状态由调用方读取后传入（running/statusErr）：network 包刻意不依赖
// mihomo，只负责「该不该动系统状态」这一层判断。helper 的启动与 IPv6 探测、
// 开发用 CLI 的启动都走这里，避免三处各写一份判定。
func ClearLeftoverTunRoutes(ctx context.Context, running bool, statusErr error) int {
	if !shouldClearLeftoverTunRoutes(running, statusErr, AdapterPresent(TunDeviceName)) {
		return 0
	}
	return ClearTunRoutes(ctx, TunDeviceName)
}
