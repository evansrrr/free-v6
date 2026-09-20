package mihomo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

var ErrBinaryNotFound = errors.New("mihomo runtime binary not found")

// RuntimeCandidates returns paths in preference order for the current platform.
// The runtime directory is the installed layout; state is retained for local development.
func RuntimeCandidates(root string) []string {
	name := "mihomo-" + runtime.GOOS + "-" + runtime.GOARCH + ".exe"
	v3Name := "mihomo-" + runtime.GOOS + "-" + runtime.GOARCH + "-v3.exe"
	return []string{
		filepath.Join(root, "runtime", v3Name),
		filepath.Join(root, "runtime", name),
		filepath.Join(root, "runtime", "mihomo.exe"),
		filepath.Join(root, "state", v3Name),
		filepath.Join(root, "state", name),
		filepath.Join(root, "state", "mihomo.exe"),
	}
}

func DiscoverBinary(root string) (string, error) {
	if root == "" {
		root = "."
	}
	for _, candidate := range RuntimeCandidates(root) {
		info, err := os.Stat(candidate)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("inspect mihomo runtime %q: %w", candidate, err)
		}
		if info.IsDir() {
			continue
		}
		return candidate, nil
	}
	return "", fmt.Errorf("%w under %q", ErrBinaryNotFound, root)
}

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
