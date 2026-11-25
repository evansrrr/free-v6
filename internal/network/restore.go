package network

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrCorruptSnapshot marks a snapshot file that exists but cannot be decoded
// (0 bytes, truncated, hand-edited garbage). Callers must treat it as "no
// usable baseline": delete/self-heal instead of failing every proxy start
// forever — an unreadable snapshot file once pinned start AND stop on user
// machines until the file was removed by hand.
var ErrCorruptSnapshot = errors.New("network snapshot corrupt")

func SaveSnapshot(path string, snapshot Snapshot) error {
	if path == "" {
		return fmt.Errorf("network snapshot path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create network snapshot directory: %w", err)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode network snapshot: %w", err)
	}
	// Atomic replace: a plain os.WriteFile truncates first, so a kill mid-write
	// (update child.kill, orphan taskkill, power loss) or a full disk leaves a
	// 0-byte/partial network-snapshot.json behind. Write a temp file, flush it
	// to disk, then rename over the target — the visible file is either the
	// old snapshot or the new one, never a half-written one.
	tmpPath := path + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write network snapshot: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write network snapshot: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync network snapshot: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close network snapshot: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace network snapshot: %w", err)
	}
	return nil
}

func LoadSnapshot(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read network snapshot: %w", err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("%w: %w", ErrCorruptSnapshot, err)
	}
	return snapshot, nil
}

// needsSharingOnlyRestore 报告快照是否需要「只按角色基线」补一次 ICS 还原：
// 有 Sharing、却没有 Hotspot 元数据。这是旧版本在「ICS 已经在脚本里切完、
// 元数据还没写回」时被强杀留下的形状 —— 不补这一步，那段共享角色会一直
// 停在切换后的状态，指向已经消失的 FreeV6TUN。
func needsSharingOnlyRestore(snapshot Snapshot) bool {
	return snapshot.Hotspot == nil && len(snapshot.Sharing) > 0
}
