package mihomo

import (
	"fmt"
	"io"
)

// ProcessLog 是进程树相关诊断的输出（helper 指向 stderr，最终进 helper.log）。
// 默认丢弃，避免库自己往 stdout 写东西。
var ProcessLog io.Writer = io.Discard

// processJob 是进程级的 Job Object（仅 Windows 有实现）。
//
// 它由 helper 在启动时创建并自加入：之后 helper 派生的所有子进程（核心、
// powershell 等）自动成为成员，helper 一旦消失，它们随之退出。这样就不需要
// 桌面壳配合，混合版本也不会因为"拿不到 Job 名"而启动失败。
var processJob *Job

// JobActive 报告当前是否有可用的 Job。false 表示核心不会随 helper 退出
// （非 Windows，或自加入与兜底挂接都失败）。
func JobActive() bool { return processJob != nil }

// assignCoreToJob 把刚启动的核心挂进进程级 Job。
//
// 正常情况下 helper 已经自加入，核心作为子进程会自动成为成员，这一步是
// 重复挂接（幂等）；只有在自加入失败（宿主 Job 不允许嵌套）时才真正起作用。
// 挂接失败不构成启动失败：核心照常运行，只是失去"随 helper 一起退出"的保证。
func assignCoreToJob(pid int) {
	if processJob == nil || pid <= 0 {
		return
	}
	if err := processJob.AssignProcess(pid); err != nil {
		fmt.Fprintf(ProcessLog, "mihomo: job assign core %d failed: %v\n", pid, err)
	}
}
