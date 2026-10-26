// Package stallhttp 提供「零进展即取消」的下载停滞检测：watchdog 监视
// 字节计数器，连续 stall 时长无增长就 cancel 掉镜像 context，中断在途
// 读取让调用方切换下一条线路。mihomo 运行时下载与 helper 应用更新共用
// 这套逻辑——微妙的并发代码只存在一份。
package stallhttp

import (
	"context"
	"io"
	"sync/atomic"
	"time"
)

// StartWatchdog 在 counter 连续 stall 时长零增长时取消 ctx（含尚无首字节
// 的连接挂死阶段：lastChange 从调用时刻开始计时）。interval 是巡检粒度。
// 返回的停止函数幂等，成功/失败路径都应调用以回收 goroutine。
func StartWatchdog(ctx context.Context, cancel context.CancelFunc, counter *atomic.Int64, stall, interval time.Duration) func() {
	done := make(chan struct{})
	var stopped atomic.Bool
	go func() {
		lastBytes := int64(0)
		lastChange := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-time.After(interval):
				current := counter.Load()
				if current != lastBytes {
					lastBytes = current
					lastChange = time.Now()
					continue
				}
				if time.Since(lastChange) >= stall {
					cancel() // 零进展超时：中断在途读取
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

// CountingReader 给 watchdog 喂进度字节数（只统计真正从网络读到的量）。
type CountingReader struct {
	Reader  io.Reader
	Counter *atomic.Int64
}

func (c *CountingReader) Read(buffer []byte) (int, error) {
	read, err := c.Reader.Read(buffer)
	if read > 0 {
		c.Counter.Add(int64(read))
	}
	return read, err
}
