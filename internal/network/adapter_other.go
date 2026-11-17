//go:build !windows

package network

import (
	"context"
	"time"
)

// AdapterPresent：非 Windows 平台没有 wintun 适配器这一层，恒为不存在。
func AdapterPresent(string) bool { return false }

// WaitAdapterGone：同上，立刻返回已消失。
func WaitAdapterGone(context.Context, string, time.Duration) bool { return true }
