// Package lifecycle 把"停止一次免流会话"收成唯一一条路径。
//
// 改动前 GUI、CLI、退出流程各写一遍顺序，而且已经不一致：CLI 完全不关 TUN，
// 所以"CLI 停完网络恢复更慢"。这里把顺序固定下来（严格按 GUI 现有顺序，因为
// 还原 ICS 共享角色要求 FreeV6TUN 仍然存在）：
//
//	还原热点/ICS → 关 TUN 并等适配器消失 → 停核并等退出 → 重读 pid → 清残留路由 → 删快照
//
// 第 2–6 步在跨进程会话锁内完成（调用方通过 Options.Lock 注入），热点还原在
// 锁外：它可能长达 120s，不能阻塞 CLI 或另一实例的启停。
package lifecycle

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/yourname/freev6/internal/mihomo"
	"github.com/yourname/freev6/internal/network"
)

// 默认预算。它们原先散在 helper 的三个常量里，收敛到这里后 GUI/CLI/退出共用
// 一套；测试可以压缩。
const (
	// DefaultHotspotBudget 覆盖 WinRT tethering 异步操作的最坏情况。
	DefaultHotspotBudget = 120 * time.Second
	// DefaultTunBudget 是"让内核关 TUN 并等适配器从系统消失"的预算。内核同步
	// 关闭 wintun 会话、实测 ~1s 才回响应，偶尔更久；把"关到一半"当成失败去
	// 硬杀，才是留下幽灵适配器、网络长时间不恢复的根因。
	DefaultTunBudget = 4 * time.Second
	// DefaultCoreBudget 等内核进程真正退出（9090 端口释放）。不等它，紧接着的
	// 启动会在端口自检上撞出"端口被占用"。
	DefaultCoreBudget = 2 * time.Second
	// DefaultRouteBudget 是删除残留 TUN 默认路由的预算：实测四条 netsh 合计
	// 约 0.4s，3s 足够。
	DefaultRouteBudget = 3 * time.Second
)

// Options 是一次停止所需的输入与可替换实现。
//
// 字段留空即取默认实现（生产路径）；测试注入假实现来断言顺序与失败路径。
// Lock 是唯一必须由调用方显式提供的语义：它必须包住"关 TUN → 停核 → 清路由
// → 删快照"，且实现内部不得再调用 mihomo.Start（会自死锁，见 WithSessionLock）。
type Options struct {
	PidPath          string
	SnapshotPath     string
	ControllerSecret string

	LoadSnapshot    func(string) (network.Snapshot, error)
	RestoreHotspot  func(context.Context, network.Snapshot) error
	DisableTun      func(context.Context, string, string) error
	WaitAdapterGone func(context.Context, string, time.Duration) bool
	StopCore        func(string, time.Duration) (time.Duration, bool, error)
	ClearRoutes     func(context.Context, bool, error) int
	Status          func(string) (int, bool, error)
	RemoveFile      func(string) error
	Lock            func(func() error) error
	Logf            func(string, ...any)

	HotspotBudget time.Duration
	TunBudget     time.Duration
	CoreBudget    time.Duration
	RouteBudget   time.Duration
}

// Result 记录每个阶段的结果。调用方据此决定响应体与日志，不需要自己再判断。
type Result struct {
	RestoreErr     error // 热点还原失败（快照保留，下次启动自愈）
	RestoreSkipped error // 快照损坏或不存在，没有可还原的基线

	TunAttempted bool // 停之前核心在运行，尝试过关 TUN
	AdapterGone  bool
	TunErr       error
	// TunStage 是"发 PATCH → 适配器消失/拿到 PATCH 结果"的耗时，用于日志排障。
	TunStage time.Duration

	CoreWait   time.Duration
	CoreExited bool

	ClearedRoutes   int
	SnapshotRemoved bool
	// SkippedCleanup 表示停止窗口里已经有新会话起来：清路由与删快照被跳过。
	SkippedCleanup bool

	Elapsed time.Duration
}

// StopSession 执行一次完整停止。返回 error 只代表"核心没停下来"这类致命失败；
// 热点还原失败等非致命结果放在 Result 里，由调用方决定如何提示。
func StopSession(ctx context.Context, opt Options) (Result, error) {
	opt.applyDefaults()
	started := time.Now()
	result := Result{}

	// 1) 热点还原：必须在 TUN 适配器仍在时完成，且放在锁外（可能长达 120s）。
	//    失败不阻断停止：快照保留，下次启动自愈。
	snapshot, err := opt.LoadSnapshot(opt.SnapshotPath)
	if err != nil {
		// 坏文件/不存在都只记录：没有可还原的基线，但停止必须继续。
		result.RestoreSkipped = err
	} else {
		restoreCtx, cancel := context.WithTimeout(ctx, opt.HotspotBudget)
		result.RestoreErr = opt.RestoreHotspot(restoreCtx, snapshot)
		cancel()
	}

	// 2) 临界区：关 TUN → 停核 → 重读 → 清路由 → 删快照。
	lockErr := opt.Lock(func() error {
		opt.disableTun(ctx, &result)
		if stopErr := opt.stopCore(&result); stopErr != nil {
			return stopErr
		}
		opt.cleanup(ctx, &result)
		return nil
	})
	result.Elapsed = time.Since(started)
	return result, lockErr
}

func (o *Options) applyDefaults() {
	if o.LoadSnapshot == nil {
		o.LoadSnapshot = network.LoadSnapshot
	}
	if o.RestoreHotspot == nil {
		o.RestoreHotspot = network.RestoreHotspot
	}
	if o.DisableTun == nil {
		o.DisableTun = mihomo.DisableTun
	}
	if o.WaitAdapterGone == nil {
		o.WaitAdapterGone = network.WaitAdapterGone
	}
	if o.StopCore == nil {
		o.StopCore = mihomo.StopAndWait
	}
	if o.ClearRoutes == nil {
		o.ClearRoutes = network.ClearLeftoverTunRoutes
	}
	if o.Status == nil {
		o.Status = mihomo.Status
	}
	if o.RemoveFile == nil {
		o.RemoveFile = os.Remove
	}
	if o.Lock == nil {
		o.Lock = mihomo.WithSessionLock
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.HotspotBudget <= 0 {
		o.HotspotBudget = DefaultHotspotBudget
	}
	if o.TunBudget <= 0 {
		o.TunBudget = DefaultTunBudget
	}
	if o.CoreBudget <= 0 {
		o.CoreBudget = DefaultCoreBudget
	}
	if o.RouteBudget <= 0 {
		o.RouteBudget = DefaultRouteBudget
	}
}

// disableTun 让内核自己关掉 TUN，并等适配器真的消失。
//
// PATCH 与"等适配器消失"并发：内核回这个请求要 ~1s，串行只是白等。不管 PATCH
// 成没成都要等适配器消失——PATCH 超时往往不是"没生效"，而是内核正关到一半，
// 这时 TerminateProcess 会把它停在半路，留下幽灵适配器和半段默认路由。
func (o *Options) disableTun(ctx context.Context, result *Result) {
	_, running, statusErr := o.Status(o.PidPath)
	if statusErr != nil || !running {
		return
	}
	result.TunAttempted = true

	stageStarted := time.Now()
	stageCtx, cancel := context.WithTimeout(ctx, o.TunBudget)
	defer cancel()
	patchDone := make(chan error, 1)
	go func() {
		patchDone <- o.DisableTun(stageCtx, mihomo.ControllerAddress, o.ControllerSecret)
	}()

	result.AdapterGone = o.WaitAdapterGone(stageCtx, network.TunDeviceName, 10*time.Millisecond)
	result.TunStage = time.Since(stageStarted)
	if result.AdapterGone {
		// 适配器已经消失就不用等 PATCH 了；如果它恰好已经返回，顺手记下结果。
		select {
		case err := <-patchDone:
			result.TunErr = err
		default:
		}
		return
	}
	select {
	case err := <-patchDone:
		result.TunErr = err
	case <-stageCtx.Done():
		result.TunErr = stageCtx.Err()
	}
}

// stopCore 停核并等退出。失败即返回错误：进程还活着会继续往路由表里写，
// 此刻清路由是在跟内核收尾抢。
func (o *Options) stopCore(result *Result) error {
	waited, exited, err := o.StopCore(o.PidPath, o.CoreBudget)
	result.CoreWait, result.CoreExited = waited, exited
	return err
}

// cleanup 清理残留路由并按条件删除快照。
func (o *Options) cleanup(ctx context.Context, result *Result) {
	if result.CoreExited {
		// Stop 会先删 pid 文件再等进程退出，所以这里必须重读一次：若有另一个
		// 入口（CLI、第二个 helper）在这个窗口里刚把新内核拉起来，删路由就等于
		// 删掉新会话的出站路径。
		_, coreRunning, statusErr := o.Status(o.PidPath)
		result.SkippedCleanup = coreRunning
		cleanupCtx, cancel := context.WithTimeout(ctx, o.RouteBudget)
		result.ClearedRoutes = o.ClearRoutes(cleanupCtx, coreRunning, statusErr)
		cancel()
	}
	if result.RestoreErr != nil || result.SkippedCleanup {
		return
	}
	// 只有"还原没失败、且没有新会话接盘"才删快照：否则下次启动就没有自愈基线了。
	if err := o.RemoveFile(o.SnapshotPath); err == nil || errors.Is(err, os.ErrNotExist) {
		result.SnapshotRemoved = true
	}
}
