package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yourname/freev6/internal/mihomo"
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
	config, err := mihomo.Render(device)
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
	binaryPath := flags.String("binary", "mihomo.exe", "mihomo executable path")
	statePath := flags.String("state", filepath.FromSlash("state/warp.json"), "private state file")
	configPath := flags.String("config", filepath.FromSlash("state/mihomo.yaml"), "mihomo YAML path")
	pidPath := flags.String("pid-file", filepath.FromSlash("state/mihomo.pid"), "mihomo PID file")
	logPath := flags.String("log", filepath.FromSlash("state/mihomo.log"), "mihomo log file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	device, err := readDevice(*statePath)
	if err != nil {
		return err
	}
	config, err := mihomo.Render(device)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*configPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(*configPath, []byte(config), 0o600); err != nil {
		return fmt.Errorf("write mihomo config: %w", err)
	}
	pid, err := mihomo.Start(*binaryPath, *configPath, *pidPath, *logPath)
	if err != nil {
		return err
	}
	fmt.Printf("mihomo started: pid %d\n", pid)
	return nil
}

func stopMihomo(args []string) error {
	flags := flag.NewFlagSet("stop", flag.ContinueOnError)
	pidPath := flags.String("pid-file", filepath.FromSlash("state/mihomo.pid"), "mihomo PID file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := mihomo.Stop(*pidPath); err != nil {
		return err
	}
	fmt.Println("mihomo stopped")
	return nil
}

func statusMihomo(args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	pidPath := flags.String("pid-file", filepath.FromSlash("state/mihomo.pid"), "mihomo PID file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	pid, running, err := mihomo.Status(*pidPath)
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

func printUsage() {
	fmt.Println("freev6 register [-name name] [-state path]")
	fmt.Println("freev6 render-config [-state path] [-out path]")
	fmt.Println("freev6 start [-binary mihomo.exe] [-state path]")
	fmt.Println("freev6 stop [-pid-file path]")
	fmt.Println("freev6 status [-pid-file path]")
	fmt.Println("freev6 version")
}
