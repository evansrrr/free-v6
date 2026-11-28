package lifecycle

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/yourname/freev6/internal/network"
)

// recorder 记录步骤顺序。disableTun 在 goroutine 里调 DisableTun，同一时刻
// 主 goroutine 在调 WaitAdapterGone，两者都会 add —— 不加锁时 `go test -race`
// 会直接判失败（CI 的 go-race job 就是这么抓到的）。
type recorder struct {
	mu    sync.Mutex
	steps []string
}

func (r *recorder) add(step string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, step)
}

func (r *recorder) index(step string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, value := range r.steps {
		if value == step {
			return i
		}
	}
	return -1
}

// fake 用记录器替换每一步，用来断言顺序与失败路径。
type fake struct {
	rec *recorder

	restoreErr  error
	snapshotErr error
	stopErr     error
	lockErr     error
	adapterGone bool

	coreRunningBeforeStop bool
	coreRunningAfterStop  bool
	statusCalls           int
}

func (f *fake) options() Options {
	f.rec = &recorder{}
	return Options{
		PidPath:      "state/mihomo.pid",
		SnapshotPath: "state/network-snapshot.json",
		LoadSnapshot: func(string) (network.Snapshot, error) {
			f.rec.add("load-snapshot")
			return network.Snapshot{}, f.snapshotErr
		},
		RestoreHotspot: func(context.Context, network.Snapshot) error {
			f.rec.add("restore")
			return f.restoreErr
		},
		DisableTun: func(context.Context, string, string) error {
			f.rec.add("tun")
			return nil
		},
		WaitAdapterGone: func(context.Context, string, time.Duration) bool {
			f.rec.add("adapter")
			return f.adapterGone
		},
		StopCore: func(string, time.Duration) (time.Duration, bool, error) {
			f.rec.add("core")
			if f.stopErr != nil {
				return 0, false, f.stopErr
			}
			return 10 * time.Millisecond, true, nil
		},
		ClearRoutes: func(_ context.Context, running bool, _ error) int {
			f.rec.add("routes")
			if running {
				return 0
			}
			return 2
		},
		Status: func(string) (int, bool, error) {
			f.rec.add("status")
			f.statusCalls++
			if f.statusCalls == 1 {
				return 100, f.coreRunningBeforeStop, nil
			}
			return 100, f.coreRunningAfterStop, nil
		},
		RemoveFile: func(string) error {
			f.rec.add("remove")
			return nil
		},
		Lock: func(fn func() error) error {
			f.rec.add("lock")
			if f.lockErr != nil {
				return f.lockErr
			}
			return fn()
		},
		HotspotBudget: time.Second,
		TunBudget:     time.Second,
		CoreBudget:    time.Second,
		RouteBudget:   time.Second,
	}
}

// 顺序是这条路径的全部价值：还原必须在 TUN 还在时、关 TUN 必须在停核之前、
// 清路由必须在核确认退出之后、删快照必须最后。
func TestStopSessionOrdersSteps(t *testing.T) {
	// adapterGone=false 让实现必须等 PATCH 返回，"tun" 才会在 "core" 之前落定。
	// 适配器先消失时 PATCH 结果不再被等待（这是刻意的延迟优化），此时 tun 与
	// core 的先后不做断言。
	f := &fake{adapterGone: false, coreRunningBeforeStop: true}
	opt := f.options()

	result, err := StopSession(context.Background(), opt)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !result.CoreExited || !result.SnapshotRemoved || result.ClearedRoutes != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !result.TunAttempted {
		t.Fatalf("TUN should have been disabled: %+v", result)
	}
	if result.AdapterGone {
		t.Fatalf("adapter should still be present in this scenario: %+v", result)
	}

	restore := f.rec.index("restore")
	lock := f.rec.index("lock")
	tun := f.rec.index("tun")
	core := f.rec.index("core")
	routes := f.rec.index("routes")
	remove := f.rec.index("remove")
	for name, index := range map[string]int{"restore": restore, "lock": lock, "tun": tun, "core": core, "routes": routes, "remove": remove} {
		if index < 0 {
			t.Fatalf("%s was never called: %v", name, f.rec.steps)
		}
	}
	if !(restore < lock && lock < tun && tun < core && core < routes && routes < remove) {
		t.Fatalf("wrong order: %v", f.rec.steps)
	}
}

// 热点还原失败不能阻断停止；快照必须保留（下次启动自愈）。
func TestStopSessionKeepsSnapshotWhenRestoreFails(t *testing.T) {
	f := &fake{adapterGone: true, coreRunningBeforeStop: true, restoreErr: errors.New("ics busy")}
	result, err := StopSession(context.Background(), f.options())
	if err != nil {
		t.Fatalf("restore failure must not fail the stop: %v", err)
	}
	if result.RestoreErr == nil {
		t.Fatal("restore error should be reported")
	}
	if f.rec.index("core") < 0 {
		t.Fatal("core must still be stopped")
	}
	if result.SnapshotRemoved || f.rec.index("remove") >= 0 {
		t.Fatal("snapshot must be kept for the next start")
	}
}

// 停核失败是致命错误：此时清路由等于跟还在收尾的内核抢。
func TestStopSessionDoesNotClearRoutesWhenStopFails(t *testing.T) {
	f := &fake{adapterGone: true, coreRunningBeforeStop: true, stopErr: errors.New("refusing to stop")}
	_, err := StopSession(context.Background(), f.options())
	if err == nil {
		t.Fatal("stop failure must be returned")
	}
	if f.rec.index("routes") >= 0 {
		t.Fatal("routes must not be cleared while the core may still be running")
	}
	if f.rec.index("remove") >= 0 {
		t.Fatal("snapshot must be kept")
	}
}

// 停止窗口里新会话接管：跳过清路由与删快照，快照属于新会话。
func TestStopSessionSkipsCleanupForNewSession(t *testing.T) {
	f := &fake{adapterGone: true, coreRunningBeforeStop: true, coreRunningAfterStop: true}
	result, err := StopSession(context.Background(), f.options())
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !result.SkippedCleanup {
		t.Fatal("a takeover must be reported")
	}
	if result.SnapshotRemoved || f.rec.index("remove") >= 0 {
		t.Fatal("snapshot belongs to the new session")
	}
}

// 适配器没消失也要继续停核：卡在关 TUN 的中途更不能留着进程。
func TestStopSessionStopsCoreEvenIfAdapterStays(t *testing.T) {
	f := &fake{adapterGone: false, coreRunningBeforeStop: true}
	result, err := StopSession(context.Background(), f.options())
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if result.AdapterGone {
		t.Fatal("adapter should be reported as still present")
	}
	if f.rec.index("core") < 0 {
		t.Fatal("core must still be stopped")
	}
}

// 核心本来就没跑：不碰 TUN，但停核与清理照旧（幂等）。
func TestStopSessionSkipsTunWhenCoreNotRunning(t *testing.T) {
	f := &fake{coreRunningBeforeStop: false}
	result, err := StopSession(context.Background(), f.options())
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if result.TunAttempted {
		t.Fatal("must not PATCH a core that is not running")
	}
	if f.rec.index("tun") >= 0 || f.rec.index("adapter") >= 0 {
		t.Fatalf("no TUN work expected: %v", f.rec.steps)
	}
	if f.rec.index("core") < 0 {
		t.Fatal("StopCore is still the idempotent path")
	}
}

// 快照不存在是正常情况（从没成功启动过会话）：不能当成"还原已跳过"报给前端，
// 否则每次停止都多一条红色提示。停止照常继续。
func TestStopSessionIgnoresMissingSnapshot(t *testing.T) {
	f := &fake{adapterGone: true, coreRunningBeforeStop: true, snapshotErr: os.ErrNotExist}
	result, err := StopSession(context.Background(), f.options())
	if err != nil {
		t.Fatalf("missing snapshot must not fail the stop: %v", err)
	}
	if result.RestoreSkipped != nil {
		t.Fatalf("a missing snapshot is not an anomaly: %v", result.RestoreSkipped)
	}
	if f.rec.index("restore") >= 0 {
		t.Fatalf("nothing to restore: %v", f.rec.steps)
	}
	if f.rec.index("core") < 0 {
		t.Fatal("stop must continue after a missing snapshot")
	}
}

// 快照存在但读不出来（损坏/权限）仍然要提示：那是需要人看一眼的异常。
func TestStopSessionReportsUnreadableSnapshot(t *testing.T) {
	f := &fake{adapterGone: true, snapshotErr: errors.New("snapshot: invalid character '}'")}
	result, err := StopSession(context.Background(), f.options())
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if result.RestoreSkipped == nil {
		t.Fatal("an unreadable snapshot must be reported")
	}
}

// 拿不到会话锁：直接返回错误，一个系统状态都不动。
func TestStopSessionReturnsLockError(t *testing.T) {
	f := &fake{adapterGone: true, coreRunningBeforeStop: true, lockErr: errors.New("session lock busy")}
	_, err := StopSession(context.Background(), f.options())
	if err == nil {
		t.Fatal("lock failure must be returned")
	}
	if f.rec.index("tun") >= 0 || f.rec.index("core") >= 0 {
		t.Fatalf("nothing may run without the lock: %v", f.rec.steps)
	}
}
