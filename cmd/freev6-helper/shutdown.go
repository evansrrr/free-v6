package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yourname/freev6/internal/lifecycle"
	"github.com/yourname/freev6/internal/mihomo"
	"github.com/yourname/freev6/internal/network"
)

// 收尾相关预算。都是变量而不是常量，测试可以压缩。
var (
	// shutdownLockBudget 是 shutdown 等 h.mu 的上限。startProxy 可能正在跑
	// （就绪等待 + 热点切换），不能在这里死等；拿不到锁就返回 409，由壳侧按
	// 自己的预算强杀——拿不到锁却硬退出，会把一次进行中的启动切成半截。
	shutdownLockBudget = 5 * time.Second
	// shutdownServiceBudget 是 http.Server.Shutdown 的上限。WriteTimeout=0，
	// 不设上限的话"优雅退出"可以永不返回。
	shutdownServiceBudget = 5 * time.Second
	// parentWatchLockBudget 是父进程消失后等 h.mu 的上限。startProxy 全程持锁，
	// 而它的最坏耗时是**多段之和**：残留自愈 120s + 热点探测 30s + 切换 120s +
	// 回滚 60s，再加就绪/快照/DNS/自检，可到 400s 量级。等锁必须覆盖它，否则
	// 父进程在长启动中消失时，helper 会在启动进行到一半时直接退出（Job 顺手
	// 杀掉核心），把热点留在半切换状态。多等几分钟换来"先收尾再退出"是划算的：
	// 这期间核心仍在跑，用户的网络没有断。
	//
	// 与收尾预算分开计时：合成一段时，一次长启动会把 120s 的热点还原预算挤到
	// 只剩十几秒，还原被截断。
	parentWatchLockBudget = 420 * time.Second
	// parentWatchBudget 是拿到锁之后的收尾预算（热点还原最长 120s）。
	parentWatchBudget = 150 * time.Second
)

// exitProcess 是退出钩子：测试替换成记录器，避免把测试进程杀掉。
var exitProcess = os.Exit

// shutdownState 保证收尾只执行一次：shutdown 端点与父进程监视可能同时触发。
//
// 它不能和 h.mu 合并成一个锁：两条路径的取锁顺序必须是 h.mu → shutdownState。
// 反过来的话，父监视持着 shutdownState 等 h.mu、HTTP 侧持着 h.mu 等
// shutdownState，直接死锁。所以这里只做"谁先到"的判定，持锁时间以微秒计。
var shutdownState struct {
	mu   sync.Mutex
	done bool
}

// beginShutdown 标记本次收尾由调用方执行；返回 false 表示已经有人在做。
func beginShutdown() bool {
	shutdownState.mu.Lock()
	defer shutdownState.mu.Unlock()
	if shutdownState.done {
		return false
	}
	shutdownState.done = true
	return true
}

// resetShutdownState 只给测试用。
func resetShutdownState() {
	shutdownState.mu.Lock()
	defer shutdownState.mu.Unlock()
	shutdownState.done = false
}

// runStopSession 在**已持有 h.mu** 的前提下执行共用停止流程。
//
// 停止按钮、shutdown 端点、父进程监视都走这里，顺序只有一份
// （见 internal/lifecycle 的包注释）。
//
// 是变量而不是方法：单测必须能把它换成 fake。默认实现会调
// ClearLeftoverTunRoutes（netsh 删路由），在开发者机器上以管理员跑测试、
// 同时真实会话在运行时，那会删掉正在使用的路由。
var runStopSession = func(ctx context.Context, root string) (lifecycle.Result, error) {
	secret, _ := mihomo.ReadControllerSecret(filepath.Join(root, "state"))
	return lifecycle.StopSession(ctx, lifecycle.Options{
		PidPath:          filepath.Join(root, "state", "mihomo.pid"),
		SnapshotPath:     filepath.Join(root, "state", "network-snapshot.json"),
		ControllerSecret: secret,
	})
}

// lockWithin 在有界时间内拿到 h.mu；false 表示超时（调用方自己决定降级）。
func (h *helper) lockWithin(budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for !h.mu.TryLock() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
	return true
}

// logStopResult 把阶段耗时写进 helper.log：teardown（关 TUN + 适配器消失）与
// exit（进程退出）分开记，才看得出时间花在哪一步。
func (h *helper) logStopResult(result lifecycle.Result) {
	if result.TunAttempted {
		fmt.Fprintf(os.Stderr, "helper: stop: tun teardown %v (adapter gone=%v, patch err=%v), exit %v (exited=%v), cleared %d route(s), total %v\n",
			result.TunStage.Round(time.Millisecond), result.AdapterGone, result.TunErr,
			result.CoreWait.Round(time.Millisecond), result.CoreExited, result.ClearedRoutes,
			result.Elapsed.Round(time.Millisecond))
		if !result.AdapterGone && !result.CoreExited {
			fmt.Fprintf(os.Stderr, "helper: stop: warning: %s 未在预算内消失且内核未退出，残留路由会在下次启动时清理\n",
				network.TunDeviceName)
		}
		return
	}
	fmt.Fprintf(os.Stderr, "helper: stop: core not running, cleared %d route(s), total %v\n",
		result.ClearedRoutes, result.Elapsed.Round(time.Millisecond))
}

// shutdown 是 POST /api/v1/shutdown：停止当前会话、回应结果、然后退出进程。
//
// 它存在的理由：桌面壳退出/更新前需要"先还原网络、再退出"，而直接
// TerminateProcess 会把一次热点还原切成半截。旧 helper 没有这个路由，壳侧
// 会回落到 /proxy/stop。
func (h *helper) shutdown(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !h.lockWithin(shutdownLockBudget) {
		writeJSON(writer, http.StatusConflict, map[string]string{
			"code":  "proxy-busy",
			"error": "a start/stop is still running",
		})
		return
	}

	var (
		result  lifecycle.Result
		stopErr error
	)
	if beginShutdown() {
		// 收尾用 WithoutCancel：壳侧只等一会儿就会断开连接，断开不能把热点
		// 还原砍掉。
		ctx := context.WithoutCancel(request.Context())
		result, stopErr = runStopSession(ctx, h.root)
		h.logStopResult(result)
		h.scheduleExit()
	}
	h.mu.Unlock()

	// 重复调用（已经有人收尾）也回 200：对壳侧来说"已经不跑了"是同一件事。
	payload := map[string]any{"ok": stopErr == nil, "running": false}
	if stopErr != nil {
		payload["error"] = stopErr.Error()
	}
	if result.RestoreErr != nil {
		payload["restoreError"] = result.RestoreErr.Error()
	}
	writeJSON(writer, http.StatusOK, payload)
}

// scheduleExit 在响应写出之后关闭监听并退出进程。
func (h *helper) scheduleExit() {
	go func() {
		// 让响应先刷出去；Shutdown 会等连接空闲（包括本请求），最后才真正退出。
		time.Sleep(50 * time.Millisecond)
		if h.server != nil {
			ctx, cancel := context.WithTimeout(context.Background(), shutdownServiceBudget)
			if err := h.server.Shutdown(ctx); err != nil {
				_ = h.server.Close()
			}
			cancel()
		}
		exitProcess(0)
	}()
}

// shutdownFromParent 是父进程监视的收尾入口：桌面壳被强杀/崩溃后，helper
// 仍然把会话停干净再退出，而不是留下一个提权孤儿。
func (h *helper) shutdownFromParent() {
	// 先有界地等锁，再开收尾预算：见 parentWatchLockBudget 的注释。
	if !h.lockWithin(parentWatchLockBudget) {
		fmt.Fprintf(os.Stderr, "helper: parent gone: session lock still held after %v, exiting without cleanup\n",
			parentWatchLockBudget)
		exitProcess(0)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), parentWatchBudget)
	defer cancel()
	if beginShutdown() {
		result, _ := runStopSession(ctx, h.root)
		h.logStopResult(result)
	}
	h.mu.Unlock()
	exitProcess(0)
}
