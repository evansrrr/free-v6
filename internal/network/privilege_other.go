//go:build !windows

package network

import "context"

func IsAdministrator(context.Context) (bool, error) {
	return false, nil
}
