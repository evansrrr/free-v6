//go:build !windows

package mihomo

import "errors"

// Job 在非 Windows 平台没有等价物：进程退出后核心会成为孤儿，这是已知限制
// （见 docs/design 的批次 2 计划）。
type Job struct{}

// ErrJobUnsupported 表示当前平台没有 Job Object。
var ErrJobUnsupported = errors.New("job objects are only available on windows")

// NewKillOnCloseJob 在非 Windows 平台总是返回不支持。
func NewKillOnCloseJob() (*Job, error) { return nil, ErrJobUnsupported }

// InitProcessJob 在非 Windows 平台是空操作。
func InitProcessJob() error { return nil }

// AssignProcess 在非 Windows 平台总是返回不支持。
func (j *Job) AssignProcess(int) error { return ErrJobUnsupported }

// Close 在非 Windows 平台是空操作。
func (j *Job) Close() error { return nil }
