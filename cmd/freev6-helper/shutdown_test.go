package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yourname/freev6/internal/lifecycle"
)

// stubStopSession 把共用停止流程换成记录器。
//
// 默认实现会调 ClearLeftoverTunRoutes（netsh 删路由）：在开发者机器上以管理员
// 跑测试、同时真实免流在运行时，那会删掉正在使用的路由。测试必须隔离系统状态。
func stubStopSession(t *testing.T) *int32 {
	t.Helper()
	var calls int32
	original := runStopSession
	runStopSession = func(context.Context, string) (lifecycle.Result, error) {
		atomic.AddInt32(&calls, 1)
		return lifecycle.Result{CoreExited: true}, nil
	}
	t.Cleanup(func() { runStopSession = original })
	return &calls
}

func TestShutdownRequiresPost(t *testing.T) {
	h := &helper{root: t.TempDir()}
	rec := httptest.NewRecorder()
	h.shutdown(rec, httptest.NewRequest(http.MethodGet, "/api/v1/shutdown", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET must be rejected, got %d", rec.Code)
	}
}

// startProxy 持着 h.mu 时 shutdown 不能死等：返回 409 让壳侧按自己的预算强杀。
func TestShutdownBusyReturnsConflict(t *testing.T) {
	originalBudget := shutdownLockBudget
	shutdownLockBudget = 100 * time.Millisecond
	t.Cleanup(func() { shutdownLockBudget = originalBudget })
	calls := stubStopSession(t)

	h := &helper{root: t.TempDir()}
	h.mu.Lock()
	defer h.mu.Unlock()

	rec := httptest.NewRecorder()
	h.shutdown(rec, httptest.NewRequest(http.MethodPost, "/api/v1/shutdown", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("busy helper must answer 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("a busy helper must not start cleanup, calls=%d", got)
	}
}

// 收尾只执行一次：重复调用不再触发退出（进程可能已经在退）。
func TestShutdownRunsOnceAndExits(t *testing.T) {
	resetShutdownState()
	originalBudget := shutdownLockBudget
	shutdownLockBudget = 200 * time.Millisecond
	originalExit := exitProcess
	exits := make(chan int, 4)
	exitProcess = func(code int) { exits <- code }
	t.Cleanup(func() {
		shutdownLockBudget = originalBudget
		exitProcess = originalExit
		resetShutdownState()
	})
	calls := stubStopSession(t)

	h := &helper{root: t.TempDir()}
	rec := httptest.NewRecorder()
	h.shutdown(rec, httptest.NewRequest(http.MethodPost, "/api/v1/shutdown", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("shutdown must answer 200, got %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case <-exits:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown must request process exit")
	}

	rec2 := httptest.NewRecorder()
	h.shutdown(rec2, httptest.NewRequest(http.MethodPost, "/api/v1/shutdown", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("repeated shutdown should still answer 200, got %d", rec2.Code)
	}
	select {
	case <-exits:
		t.Fatal("exit must be requested only once")
	case <-time.After(300 * time.Millisecond):
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("cleanup must run exactly once, calls=%d", got)
	}
}

// 父进程消失后同样只收尾一次，然后退出。
func TestShutdownFromParentStopsOnce(t *testing.T) {
	resetShutdownState()
	originalExit := exitProcess
	exits := make(chan int, 4)
	exitProcess = func(code int) { exits <- code }
	t.Cleanup(func() {
		exitProcess = originalExit
		resetShutdownState()
	})
	calls := stubStopSession(t)

	h := &helper{root: t.TempDir()}
	h.shutdownFromParent()
	select {
	case <-exits:
	case <-time.After(3 * time.Second):
		t.Fatal("parent watch must exit the helper")
	}
	if beginShutdown() {
		t.Fatal("the first caller must own the shutdown")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("cleanup must run exactly once, calls=%d", got)
	}
}
