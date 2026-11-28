package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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

	h := &helper{root: t.TempDir()}
	h.mu.Lock()
	defer h.mu.Unlock()

	rec := httptest.NewRecorder()
	h.shutdown(rec, httptest.NewRequest(http.MethodPost, "/api/v1/shutdown", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("busy helper must answer 409, got %d: %s", rec.Code, rec.Body.String())
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
}
