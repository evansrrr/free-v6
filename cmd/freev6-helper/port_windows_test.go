//go:build windows

package main

import (
	"os"
	"testing"
)

// A live process must never be judged orphaned — this is the branch that used
// to mis-fire (powershell blocked → "unknown" → no takeover; recycled parent
// PID → "live" → no takeover). Both now resolve through Win32, so a plain
// self-check is enough to catch a regression in the plumbing.
func TestIsOrphanedFalseForLiveProcess(t *testing.T) {
	if isOrphaned(os.Getpid()) {
		t.Fatal("a running process must not be reported as orphaned")
	}
}

func TestProcessImagePathResolvesSelf(t *testing.T) {
	path, err := processImagePath(uint32(os.Getpid()))
	if err != nil {
		t.Fatalf("processImagePath(self): %v", err)
	}
	if path == "" {
		t.Fatal("processImagePath(self) returned an empty path")
	}
}
