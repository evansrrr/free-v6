package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/yourname/freev6/internal/mihomo"
	"github.com/yourname/freev6/internal/network"
	"github.com/yourname/freev6/internal/warp"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "freev6:", err)
		os.Exit(1)
	}
}

const (
	// 与 helper 的 stopExitTimeout / tunCleanupTimeout 同值：CLI 是排障工具，
	// 停止行为要和正式路径一致，不然「CLI 停完是好的、界面停完是坏的」会把人
	// 带偏。
	stopExitTimeout   = 2 * time.Second
	tunCleanupTimeout = 3 * time.Second
)

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" {
		printUsage()
		return nil
	}
	switch args[0] {
	case "version":
		fmt.Println("freev6 0.1.0")
		return nil
	case "register":
		return register(args[1:])
	case "render-config":
		return renderConfig(args[1:])
	case "start":
		return startMihomo(args[1:])
	case "stop":
		return stopMihomo(args[1:])
	case "status":
		return statusMihomo(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func register(args []string) error {
	flags := flag.NewFlagSet("register", flag.ContinueOnError)
	name := flags.String("name", "freev6-windows", "WARP device name")
	statePath := flags.String("state", filepath.FromSlash("state/warp.json"), "private state file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	device, err := warp.NewClient(nil).Register(context.Background(), *name)
	if err != nil {
		return err
	}
	if err := writeJSON(*statePath, device); err != nil {
		return err
	}
	fmt.Println("registered device:", device.DeviceID)
	fmt.Println("state:", *statePath)
	return nil
}

func renderConfig(args []string) error {
	flags := flag.NewFlagSet("render-config", flag.ContinueOnError)
	statePath := flags.String("state", filepath.FromSlash("state/warp.json"), "private state file")
	outputPath := flags.String("out", filepath.FromSlash("state/mihomo.yaml"), "mihomo YAML output")
	mode := flags.String("mode", mihomo.ModeRule, "mihomo mode: rule or global")
	campusCIDRs := flags.String("campus-cidr", "", "comma-separated campus CIDRs allowed to bypass WARP")
	if err := flags.Parse(args); err != nil {
		return err
	}
	var device warp.Device
	data, err := os.ReadFile(*statePath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &device); err != nil {
		return err
	}
	controllerSecret, err := mihomo.LoadOrCreateSecret(filepath.Dir(*statePath))
	if err != nil {
		return err
	}
	config, err := mihomo.RenderWithOptions(device, mihomo.RenderOptions{Mode: *mode, CampusCIDRs: splitCommaList(*campusCIDRs), ControllerSecret: controllerSecret})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*outputPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(*outputPath, []byte(config), 0o600); err != nil {
		return err
	}
	fmt.Println("mihomo config:", *outputPath)
	return nil
}

func startMihomo(args []string) error {
	flags := flag.NewFlagSet("start", flag.ContinueOnError)
	binaryPath := flags.String("binary", "", "mihomo executable path override")
	rootPath := flags.String("root", ".", "installation or project root used to discover mihomo")
	statePath := flags.String("state", filepath.FromSlash("state/warp.json"), "private state file")
	configPath := flags.String("config", filepath.FromSlash("state/mihomo.yaml"), "mihomo YAML path")
	pidPath := flags.String("pid-file", filepath.FromSlash("state/mihomo.pid"), "mihomo PID file")
	logPath := flags.String("log", filepath.FromSlash("state/mihomo.log"), "mihomo log file")
	snapshotPath := flags.String("network-snapshot", filepath.FromSlash("state/network-snapshot.json"), "network snapshot path")
	mode := flags.String("mode", mihomo.ModeRule, "mihomo mode: rule or global")
	campusCIDRs := flags.String("campus-cidr", "", "comma-separated campus CIDRs allowed to bypass WARP")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		adminCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		isAdmin, err := network.IsAdministrator(adminCtx)
		if err != nil {
			return err
		}
		if !isAdmin {
			return fmt.Errorf("administrator privileges are required to start mihomo TUN")
		}
	}
	statePathValue := resolveRootPath(*rootPath, *statePath)
	configPathValue := resolveRootPath(*rootPath, *configPath)
	pidPathValue := resolveRootPath(*rootPath, *pidPath)
	logPathValue := resolveRootPath(*rootPath, *logPath)
	snapshotPathValue := resolveRootPath(*rootPath, *snapshotPath)
	binaryPathValue := resolveRootPath(*rootPath, *binaryPath)
	// 与 helper 同一条口径：上次停止被强杀在关 TUN 的中途时，FreeV6TUN 与半段
	// 默认路由会留下，出站全被它吸走（网连着却什么都连不上）。内核确认没在跑
	// 就先清掉，再往下做自愈与抓基线。
	_, coreRunning, coreStatusErr := mihomo.Status(pidPathValue)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
	removedRoutes := network.ClearLeftoverTunRoutes(cleanupCtx, coreRunning, coreStatusErr)
	cleanupCancel()
	if removedRoutes > 0 {
		fmt.Fprintf(os.Stderr, "freev6: cleared %d leftover %s route(s)\n", removedRoutes, network.TunDeviceName)
	}
	device, err := readDevice(statePathValue)
	if err != nil {
		return err
	}
	controllerSecret, err := mihomo.LoadOrCreateSecret(filepath.Dir(statePathValue))
	if err != nil {
		return err
	}
	config, err := mihomo.RenderWithOptions(device, mihomo.RenderOptions{Mode: *mode, CampusCIDRs: splitCommaList(*campusCIDRs), ControllerSecret: controllerSecret})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(configPathValue), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(configPathValue, []byte(config), 0o600); err != nil {
		return fmt.Errorf("write mihomo config: %w", err)
	}
	// 与 helper 同一套残留自愈：上次会话的快照先还原，还原失败时它必须继续
	// 当基线 —— 否则本次现状会把没还原掉的切换状态固化下来。
	healCtx, healCancel := context.WithTimeout(context.Background(), 120*time.Second)
	leftover := network.HealLeftoverSnapshot(healCtx, snapshotPathValue, network.RestoreLeftoverSnapshot)
	healCancel()
	if leftover.Warning != "" {
		fmt.Fprintln(os.Stderr, "freev6:", leftover.Warning)
	}
	// 与 helper 同一套默认防护：旧版本把 DHCP 下发的 DNS 写成了静态。
	// 基线已不再记录 DNS，这里修的是机器当前状态 —— 不清掉，用户这次会话
	// 换个网络就会断解析。
	dnsCtx, dnsCancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, dnsFailures := network.ResetDNSToDHCP(dnsCtx)
	dnsCancel()
	for _, failure := range dnsFailures {
		fmt.Fprintln(os.Stderr, "freev6: restore frozen DNS:", failure)
	}
	snapshotCtx, snapshotCancel := context.WithTimeout(context.Background(), 10*time.Second)
	// 独立 CLI 不切换热点：跳过 ICS 共享基线枚举。
	snapshot, err := network.CaptureSnapshot(snapshotCtx, false)
	snapshotCancel()
	if err != nil {
		return err
	}
	baseline := network.AdoptBaseline(snapshot, leftover.Preserved)
	if err := network.SaveSnapshot(snapshotPathValue, baseline); err != nil {
		return err
	}
	resolvedBinary, err := mihomo.ResolveBinary(binaryPathValue, *rootPath)
	if err != nil {
		return err
	}
	pid, err := mihomo.Start(resolvedBinary, configPathValue, filepath.Dir(configPathValue), pidPathValue, logPathValue)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := mihomo.WaitReady(ctx, "127.0.0.1:9090", 100*time.Millisecond); err != nil {
		_ = mihomo.Stop(pidPathValue)
		return err
	}
	if _, err := mihomo.CheckController(ctx, "127.0.0.1:9090", controllerSecret); err != nil {
		_ = mihomo.Stop(pidPathValue)
		return err
	}
	fmt.Printf("mihomo started: pid %d\n", pid)
	return nil
}

func stopMihomo(args []string) error {
	flags := flag.NewFlagSet("stop", flag.ContinueOnError)
	rootPath := flags.String("root", ".", "installation or project root")
	pidPath := flags.String("pid-file", filepath.FromSlash("state/mihomo.pid"), "mihomo PID file")
	snapshotPath := flags.String("network-snapshot", filepath.FromSlash("state/network-snapshot.json"), "network snapshot path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	pidPathValue := resolveRootPath(*rootPath, *pidPath)
	snapshotPathValue := resolveRootPath(*rootPath, *snapshotPath)
	// 与 helper 同一条口径：先等进程真的退出，再清残留路由 —— 进程还在时它会
	// 继续往路由表写，此刻删除是在跟内核收尾抢。
	_, stopExited, err := mihomo.StopAndWait(pidPathValue, stopExitTimeout)
	if err != nil {
		return err
	}
	if stopExited {
		// Stop 会先删 pid 文件再等退出，所以这里要重读一次：这段时间里若有
		// 另一个入口把新内核拉起来（running=true），删路由等于删掉新会话的
		// 出站路径。
		_, coreRunning, coreStatusErr := mihomo.Status(pidPathValue)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), tunCleanupTimeout)
		removedRoutes := network.ClearLeftoverTunRoutes(cleanupCtx, coreRunning, coreStatusErr)
		cleanupCancel()
		if removedRoutes > 0 {
			fmt.Fprintf(os.Stderr, "freev6: cleared %d leftover %s route(s)\n", removedRoutes, network.TunDeviceName)
		}
	}
	if snapshot, err := network.LoadSnapshot(snapshotPathValue); err == nil {
		restoreCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		restoreErr := network.RestoreHotspot(restoreCtx, snapshot)
		cancel()
		if restoreErr != nil {
			return restoreErr
		}
		if err := os.Remove(snapshotPathValue); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove restored network snapshot: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Println("mihomo stopped")
	return nil
}

func statusMihomo(args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	rootPath := flags.String("root", ".", "installation or project root")
	pidPath := flags.String("pid-file", filepath.FromSlash("state/mihomo.pid"), "mihomo PID file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	pid, running, err := mihomo.Status(resolveRootPath(*rootPath, *pidPath))
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("mihomo running: pid %d\n", pid)
	} else {
		fmt.Println("mihomo stopped")
	}
	return nil
}

func readDevice(path string) (warp.Device, error) {
	var device warp.Device
	data, err := os.ReadFile(path)
	if err != nil {
		return device, err
	}
	if err := json.Unmarshal(data, &device); err != nil {
		return device, fmt.Errorf("read WARP state: %w", err)
	}
	return device, nil
}

func writeJSON(path string, value any) error {
	if path == "" {
		return errors.New("state path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func splitCommaList(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func resolveRootPath(root, value string) string {
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	if root == "" {
		root = "."
	}
	return filepath.Join(root, value)
}

func printUsage() {
	fmt.Println("freev6 register [-name name] [-state path]")
	fmt.Println("freev6 render-config [-state path] [-out path] [-mode rule|global] [-campus-cidr cidr1,cidr2]")
	fmt.Println("freev6 start [-root path] [-binary path] [-state path] [-mode rule|global] [-campus-cidr cidr1,cidr2]")
	fmt.Println("freev6 stop [-root path] [-pid-file path] [-network-snapshot path]")
	fmt.Println("freev6 status [-root path] [-pid-file path]")
	fmt.Println("freev6 version")
}
