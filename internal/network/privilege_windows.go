//go:build windows

package network

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

const administratorCheckCommand = `$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent()); if ($principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { 'true' } else { 'false' }`

func IsAdministrator(ctx context.Context) (bool, error) {
	command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", administratorCheckCommand)
	output, err := command.Output()
	if err != nil {
		return false, fmt.Errorf("check Windows administrator privilege: %w", err)
	}
	return strings.EqualFold(strings.TrimSpace(string(output)), "true"), nil
}
