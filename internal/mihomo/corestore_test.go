package mihomo

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestActivePrefersIndexedCore(t *testing.T) {
	store := NewCoreStore(t.TempDir())
	core, err := store.Install(writeTempExe(t, "v1"), "core-v0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Active()
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != core.Path || got.Version != "core-v0.1.0" || !got.Managed {
		t.Fatalf("unexpected active core: %+v", got)
	}
}

func TestActiveFallsBackToHighestInstalledWhenIndexMissing(t *testing.T) {
	root := t.TempDir()
	store := NewCoreStore(root)
	for _, version := range []string{"core-v0.1.0", "core-v0.2.0", "core-v0.1.9"} {
		if _, err := store.Install(writeTempExe(t, version), version); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(store.IndexPath()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Active()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "core-v0.2.0" {
		t.Fatalf("expected highest installed version, got %q", got.Version)
	}
}

// 老用户机器上那个 58MB 上游内核必须仍然可用：迁移不是"删了重下"。
func TestActiveAdoptsLegacyCore(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "runtime", "mihomo-"+runtime.GOOS+"-"+runtime.GOARCH+"-v3.exe")
	writeFile(t, legacy, "legacy upstream core")

	core, err := NewCoreStore(root).Active()
	if err != nil {
		t.Fatal(err)
	}
	if core.Managed {
		t.Fatal("legacy core must not be reported as managed")
	}
	if core.Path != legacy {
		t.Fatalf("expected legacy path %q, got %q", legacy, core.Path)
	}
}

func TestActiveWithoutAnyCoreReportsNotFound(t *testing.T) {
	_, err := NewCoreStore(t.TempDir()).Active()
	if !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("expected ErrBinaryNotFound, got %v", err)
	}
}

func TestInstallKeepsPreviousAndRollbackSwaps(t *testing.T) {
	store := NewCoreStore(t.TempDir())
	first, err := store.Install(writeTempExe(t, "v1"), "core-v0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Install(writeTempExe(t, "v2"), "core-v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	index := store.Index()
	if index.Active != filepath.Base(second.Path) || index.Previous != filepath.Base(first.Path) {
		t.Fatalf("unexpected index after install: %+v", index)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("previous core must stay on disk for rollback: %v", err)
	}

	rolled, err := store.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if rolled.Path != first.Path {
		t.Fatalf("rollback should restore %q, got %q", first.Path, rolled.Path)
	}
	if store.Index().Active != filepath.Base(first.Path) {
		t.Fatalf("index not updated by rollback: %+v", store.Index())
	}
}

func TestRollbackWithoutPreviousFails(t *testing.T) {
	store := NewCoreStore(t.TempDir())
	if _, err := store.Install(writeTempExe(t, "v1"), "core-v0.1.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Rollback(); !errors.Is(err, ErrNoRollback) {
		t.Fatalf("expected ErrNoRollback, got %v", err)
	}
}

func TestReinstallSameVersionKeepsPrevious(t *testing.T) {
	store := NewCoreStore(t.TempDir())
	if _, err := store.Install(writeTempExe(t, "v1"), "core-v0.1.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(writeTempExe(t, "v2"), "core-v0.2.0"); err != nil {
		t.Fatal(err)
	}
	// 重装修复当前版本（force）不应把回滚位顶掉。
	if _, err := store.Install(writeTempExe(t, "v2 again"), "core-v0.2.0"); err != nil {
		t.Fatal(err)
	}
	if previous := store.Index().Previous; previous != CoreFileName("core-v0.1.0") {
		t.Fatalf("rollback slot lost on reinstall: %q", previous)
	}
}

func TestGCKeepsActiveAndPrevious(t *testing.T) {
	store := NewCoreStore(t.TempDir())
	for _, version := range []string{"core-v0.0.1", "core-v0.1.0", "core-v0.2.0"} {
		if _, err := store.Install(writeTempExe(t, version), version); err != nil {
			t.Fatal(err)
		}
	}
	store.GC()
	for _, version := range []string{"core-v0.2.0", "core-v0.1.0"} {
		if _, ok := store.Has(version); !ok {
			t.Fatalf("%s should survive GC", version)
		}
	}
	if _, ok := store.Has("core-v0.0.1"); ok {
		t.Fatal("版本比 previous 更老的应该被回收")
	}
}

func TestDeleteLegacyRemovesOnlyLegacyFiles(t *testing.T) {
	root := t.TempDir()
	store := NewCoreStore(root)
	legacyRuntime := filepath.Join(root, "runtime", "mihomo-"+runtime.GOOS+"-"+runtime.GOARCH+"-v3.exe")
	legacyState := filepath.Join(root, "state", "mihomo.exe")
	writeFile(t, legacyRuntime, "old")
	writeFile(t, legacyState, "old")
	core, err := store.Install(writeTempExe(t, "new"), "core-v0.2.0")
	if err != nil {
		t.Fatal(err)
	}

	removed := store.DeleteLegacy()
	if len(removed) != 2 {
		t.Fatalf("expected 2 legacy files removed, got %v", removed)
	}
	if _, err := os.Stat(core.Path); err != nil {
		t.Fatalf("managed core must survive legacy cleanup: %v", err)
	}
}

func TestCorruptIndexFallsBackToDirectoryScan(t *testing.T) {
	root := t.TempDir()
	store := NewCoreStore(root)
	core, err := store.Install(writeTempExe(t, "v1"), "core-v0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, store.IndexPath(), "{not json")

	got, err := store.Active()
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != core.Path {
		t.Fatalf("expected directory scan to recover %q, got %q", core.Path, got.Path)
	}
}

func TestMarkBadIsIdempotent(t *testing.T) {
	store := NewCoreStore(t.TempDir())
	store.MarkBad("core-v0.3.0")
	store.MarkBad("core-v0.3.0")
	if !store.IsBad("core-v0.3.0") {
		t.Fatal("version should be marked bad")
	}
	if len(store.Index().Bad) != 1 {
		t.Fatalf("bad list should not duplicate: %v", store.Index().Bad)
	}
}

// writeTempExe 造一个占位"内核"文件。真实可执行性由 VerifyCore 负责，
// CoreStore 只关心落盘与指针。
func writeTempExe(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "core.exe")
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// 内核证明能跑之后，回滚位就该回收：磁盘上只留一份 38MB。
func TestPruneKeepsOnlyActiveCore(t *testing.T) {
	root := t.TempDir()
	store := NewCoreStore(root)
	legacy := filepath.Join(root, "runtime", "mihomo-"+runtime.GOOS+"-"+runtime.GOARCH+"-v3.exe")
	writeFile(t, legacy, "legacy core")
	if _, err := store.Install(writeTempExe(t, "v1"), "core-v0.1.0"); err != nil {
		t.Fatal(err)
	}
	active, err := store.Install(writeTempExe(t, "v2"), "core-v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	store.MarkVerified()

	removed := store.Prune()
	if len(removed) == 0 {
		t.Fatal("expected the rollback slot and the legacy core to be pruned")
	}
	installed := store.Installed()
	if len(installed) != 1 || installed[0].Path != active.Path {
		t.Fatalf("expected only the active core to remain, got %+v", installed)
	}
	if index := store.Index(); index.Previous != "" {
		t.Fatalf("previous pointer must be cleared after pruning: %+v", index)
	}
	if _, statErr := os.Stat(legacy); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("legacy core should be pruned together with the rollback slot")
	}
	if _, err := store.Active(); err != nil {
		t.Fatalf("active core must stay usable: %v", err)
	}
}

func TestVerifiedResetsOnEveryInstallAndRollback(t *testing.T) {
	store := NewCoreStore(t.TempDir())
	if _, err := store.Install(writeTempExe(t, "v1"), "core-v0.1.0"); err != nil {
		t.Fatal(err)
	}
	if store.Verified() {
		t.Fatal("a freshly installed core is not verified yet")
	}
	store.MarkVerified()
	if !store.Verified() {
		t.Fatal("MarkVerified should mark the active core as verified")
	}

	if _, err := store.Install(writeTempExe(t, "v2"), "core-v0.2.0"); err != nil {
		t.Fatal(err)
	}
	if store.Verified() {
		t.Fatal("installing a new core must clear the verified flag")
	}

	store.MarkVerified()
	if _, err := store.Rollback(); err != nil {
		t.Fatal(err)
	}
	if store.Verified() {
		t.Fatal("rolling back must clear the verified flag")
	}
}
