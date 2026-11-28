//go:build windows

package mihomo

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Job 是一个"最后一个句柄关闭时杀掉所有成员"的 Windows Job Object。
//
// 存在意义：helper 以管理员身份运行，核心是它的子进程。用任务管理器结束
// helper 时，核心会变成无主进程——网络还在被一个没有任何 UI 能控制的提权
// 进程接管。挂进 KILL_ON_JOB_CLOSE 的 Job 之后，helper 的任何消失方式
// （正常退出、被杀、崩溃）都会让核心一起退出。
type Job struct {
	handle windows.Handle
}

// NewKillOnCloseJob 创建一个 kill-on-close 的匿名 Job。
func NewKillOnCloseJob() (*Job, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		handle,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	return &Job{handle: handle}, nil
}

// InitProcessJob 创建 Job 并尝试把 helper 自己加进去。
//
// 自加入在 Win8+ 是合法的嵌套 Job；如果宿主 Job 不允许嵌套会失败，此时不
// 阻断启动，退化为"启动核心时逐个挂接"（见 assignCoreToJob）。失败必须记
// 日志：否则"杀 helper 会带走核心"这条保证会静默消失，验收时也看不出来。
func InitProcessJob() error {
	job, err := NewKillOnCloseJob()
	if err != nil {
		return err
	}
	processJob = job
	if err := job.AssignProcess(os.Getpid()); err != nil {
		fmt.Fprintf(ProcessLog, "mihomo: job self-assign failed: %v; falling back to per-process assignment\n", err)
	}
	return nil
}

// AssignProcess 把已存在的进程加入 Job。
func (j *Job) AssignProcess(pid int) error {
	if pid <= 0 {
		return errors.New("invalid pid")
	}
	// AssignProcessToJobObject 要求 PROCESS_SET_QUOTA | PROCESS_TERMINATE。
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(j.handle, process); err != nil {
		return fmt.Errorf("AssignProcessToJobObject(%d): %w", pid, err)
	}
	return nil
}

// Close 关闭 Job：所有成员进程会被立刻终止。
func (j *Job) Close() error {
	if j == nil || j.handle == 0 {
		return nil
	}
	handle := j.handle
	j.handle = 0
	return windows.CloseHandle(handle)
}
