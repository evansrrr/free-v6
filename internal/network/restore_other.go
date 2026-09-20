//go:build !windows

package network

import "context"

func RestoreSnapshot(context.Context, Snapshot) error {
	return ErrWindowsOnly
}
