//go:build windows

package network

import (
	"context"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IsAdministrator reports whether the current process runs elevated (UAC).
// It reads TokenElevation from the process token in-process via
// GetTokenInformation — no powershell.exe subprocess, so it costs microseconds
// instead of the ~1s cold start the old PowerShell check added to every
// /proxy/start and /status call. Fail-closed: a token read error returns false
// so TUN startup is refused rather than attempted without privileges.
func IsAdministrator(_ context.Context) (bool, error) {
	token := windows.GetCurrentProcessToken()
	var elevated uint32
	var returned uint32
	err := windows.GetTokenInformation(
		token,
		windows.TokenElevation,
		(*byte)(unsafe.Pointer(&elevated)),
		uint32(unsafe.Sizeof(elevated)),
		&returned,
	)
	if err != nil {
		return false, err
	}
	return returned == uint32(unsafe.Sizeof(elevated)) && elevated != 0, nil
}
