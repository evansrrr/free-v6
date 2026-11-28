//go:build !windows

package main

// watchParent 在非 Windows 平台不启用：桌面壳只在 Windows 上运行，CLI 也不需要
// 父进程监视（它自己就是前台进程）。
func watchParent(func()) {}
