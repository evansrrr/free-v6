//go:build !windows

package network

import (
	"context"
	"errors"
)

var ErrWindowsOnly = errors.New("Windows network snapshot is only available on Windows")

func CaptureSnapshot(context.Context) (Snapshot, error) {
	return Snapshot{}, ErrWindowsOnly
}
