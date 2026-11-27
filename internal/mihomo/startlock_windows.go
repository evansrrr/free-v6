//go:build windows

package mihomo

import (
	"golang.org/x/sys/windows"
)

// withStartLock runs fn while holding the named start mutex so only one
// process (helper or CLI) can run Start's check-spawn-write-pid sequence. A
// kernel object releases automatically when the owning process dies, so a
// crashed holder cannot leave a stale lock behind.
func withStartLock(fn func() error) error {
	name, err := windows.UTF16PtrFromString(`Local\FreeV6-MihomoStart`)
	if err != nil {
		return err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if _, err := windows.WaitForSingleObject(handle, windows.INFINITE); err != nil {
		return err
	}
	defer windows.ReleaseMutex(handle)
	return fn()
}
