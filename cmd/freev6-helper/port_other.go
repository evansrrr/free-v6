//go:build !windows

package main

import "errors"

// Stubs for non-Windows builds: listenOrTakeOver bails out on runtime.GOOS
// before reaching any of these, but the file still has to compile so that
// `go build ./...` and the linux CI job keep working.

func isHelperPID(int) bool { return false }

func isOrphaned(int) bool { return false }

func killProcess(int) error { return errors.New("port takeover is windows-only") }
