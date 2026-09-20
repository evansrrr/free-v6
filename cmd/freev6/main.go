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
	config, err := mihomo.RenderWithOptions(device, mihomo.RenderOptions{Mode: *mode, CampusCIDRs: splitCommaList(*campusCIDRs)})
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
	device, err := readDevice(statePathValue)
	if err != nil {
		return err
	}
	config, err := mihomo.RenderWithOptions(device, mihomo.RenderOptions{Mode: *mode, CampusCIDRs: splitCommaList(*campusCIDRs)})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(configPathValue), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(configPathValue, []byte(config), 0o600); err != nil {
		return fmt.Errorf("write mihomo config: %w", err)
	}
	snapshotCtx, snapshotCancel := context.WithTimeout(context.Background(), 10*time.Second)
	snapshot, err := network.CaptureSnapshot(snapshotCtx)
	snapshotCancel()
	if err != nil {
		return err
	}
	if err := network.SaveSnapshot(snapshotPathValue, snapshot); err != nil {
		return err
	}
	restoreSnapshot := func() {
		restoreCtx, restoreCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer restoreCancel()
		_ = network.RestoreSnapshot(restoreCtx, snapshot)
	}
	resolvedBinary, err := mihomo.ResolveBinary(binaryPathValue, *rootPath)
	if err != nil {
		restoreSnapshot()
		return err
	}
	pid, err := mihomo.Start(resolvedBinary, configPathValue, pidPathValue, logPathValue)
	if err != nil {
		restoreSnapshot()
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := mihomo.WaitReady(ctx, "127.0.0.1:9090", 100*time.Millisecond); err != nil {
		_ = mihomo.Stop(pidPathValue)
		restoreSnapshot()
		return err
	}
	if _, err := mihomo.CheckController(ctx, "127.0.0.1:9090"); err != nil {
		_ = mihomo.Stop(pidPathValue)
		restoreSnapshot()
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
	if err := mihomo.Stop(pidPathValue); err != nil {
		return err
	}
	if snapshot, err := network.LoadSnapshot(snapshotPathValue); err == nil {
		restoreCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		restoreErr := network.RestoreSnapshot(restoreCtx, snapshot)
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
