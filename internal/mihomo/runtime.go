package mihomo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrBinaryNotFound = errors.New("mihomo runtime binary not found")

// DiscoverBinary 返回当前该用的内核路径。解析顺序由 CoreStore.Active 决定：
// 指针清单 → cores/ 里版本最高的 → 老用户遗留的上游内核。
func DiscoverBinary(root string) (string, error) {
	core, err := NewCoreStore(root).Active()
	if err != nil {
		return "", err
	}
	return core.Path, nil
}

// ResolveBinary 优先用显式路径（CLI --binary / 调试覆盖），否则走发现逻辑。
func ResolveBinary(explicit, root string) (string, error) {
	if explicit != "" {
		info, err := os.Stat(explicit)
		if err != nil {
			return "", fmt.Errorf("inspect mihomo binary %q: %w", explicit, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("mihomo binary path %q is a directory", explicit)
		}
		return explicit, nil
	}
	return DiscoverBinary(root)
}

// RuntimeDir 返回内核目录（安装布局）。
func RuntimeDir(root string) string {
	if root == "" {
		root = "."
	}
	return filepath.Join(root, "runtime")
}
