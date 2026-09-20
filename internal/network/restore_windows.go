//go:build windows

package network

import (
	"context"
	"fmt"
	"os/exec"
)

func RestoreSnapshot(ctx context.Context, snapshot Snapshot) error {
	for _, restoreCommand := range BuildRestoreCommands(snapshot) {
		if output, err := exec.CommandContext(ctx, restoreCommand.Executable, restoreCommand.Args...).CombinedOutput(); err != nil {
			return fmt.Errorf("restore Windows DNS with %q: %w (%s)", restoreCommand.Args, err, output)
		}
	}
	return nil
}
