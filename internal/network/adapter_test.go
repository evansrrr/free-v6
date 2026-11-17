package network

import (
	"context"
	"testing"
	"time"
)

// 不存在的适配器必须立刻判定为"已消失"：实机上的 TUN 已经没了还在等，
// 只会让每次停止白等两秒。
func TestWaitAdapterGoneReturnsForAbsentAdapter(t *testing.T) {
	const name = "FreeV6TUN-not-present-in-this-test"
	if AdapterPresent(name) {
		t.Skipf("环境里真的存在 %s，跳过", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	if !WaitAdapterGone(ctx, name, 10*time.Millisecond) {
		t.Fatal("适配器不在却报告还在")
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Errorf("耗时 %v，第一次轮询就该返回", elapsed)
	}
}

// 名字对不上就什么都不删：残留清理只允许动 FreeV6 自己的 TUN，绝不能碰
// 别的接口上的默认路由。
func TestClearTunRoutesIgnoresUnknownAdapter(t *testing.T) {
	if removed := ClearTunRoutes(context.Background(), "FreeV6TUN-not-present-in-this-test"); removed != 0 {
		t.Errorf("删掉了 %d 条不该动的路由", removed)
	}
}
