//go:build windows

package network

import (
	"context"
	"fmt"
	"os/exec"
)

func CaptureSnapshot(ctx context.Context) (Snapshot, error) {
	command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", PowerShellSnapshotCommand)
	output, err := command.Output()
	if err != nil {
		return Snapshot{}, fmt.Errorf("capture Windows network snapshot: %w", err)
	}
	snapshot, err := ParsePowerShellSnapshot(output)
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}
