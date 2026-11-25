//go:build !windows

package network

import "context"

func RestoreLeftoverSnapshot(context.Context, Snapshot) error {
	return ErrWindowsOnly
}

func RestoreHotspot(context.Context, Snapshot) error {
	return ErrWindowsOnly
}
