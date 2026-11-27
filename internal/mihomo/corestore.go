package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	coresDirName  = "cores"
	coreIndexName = "core.json"
	// coreExecTimeout 是 `-v` 自检的超时：正常 50ms 内返回，5s 兜住卡死的文件。
	coreExecTimeout = 5 * time.Second
)

// ErrNoRollback 表示没有可回滚的上一版内核。
var ErrNoRollback = errors.New("no previous core to roll back to")

// Core 是一个可执行的内核实例。
type Core struct {
	// Version 是完整 tag（core-v0.1.0-rc.2）；遗留内核用它自报的版本。
	Version string
	Path    string
	// Managed 表示受 core.json 管理（位于 cores/ 且由指针引用）。
	// 遗留的 58MB 上游内核 managed=false，但仍然可用。
	Managed bool
}

// Parsed 返回解析后的版本，便于比较。
func (c Core) Parsed() CoreVersion { return ParseCoreVersion(c.Version) }

// CoreIndex 是 runtime/cores/core.json 的结构。它只有一个职责：
// 说明当前该跑哪个文件。写入即切换，因此永远不存在"半个内核"的状态。
type CoreIndex struct {
	Active      string    `json:"active"`
	Previous    string    `json:"previous"`
	Version     string    `json:"version"`
	InstalledAt time.Time `json:"installedAt"`
	// VerifiedAt 是当前内核第一次成功跑起来的时间。非零且不早于 InstalledAt
	// 表示"这份内核在本机实测可用"，此时回滚位已经没有存在意义，可以回收。
	VerifiedAt time.Time `json:"verifiedAt,omitempty"`
	// Bad 记录自检失败的版本，自动更新不再重试它们。
	Bad []string `json:"bad,omitempty"`
}

// CoreStore 管理某个安装根目录下的内核集合。
//
// 目录布局：
//
//	runtime/cores/core.json                                    指向清单
//	runtime/cores/f6-core-windows-amd64-v3-v0.1.0-rc.2.exe     当前内核
//	runtime/cores/f6-core-windows-amd64-v3-v0.2.0.exe          上一版（回滚位）
//	runtime/mihomo-windows-amd64-v3.exe                        老用户的遗留内核
type CoreStore struct {
	root string
}

func NewCoreStore(root string) *CoreStore {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	return &CoreStore{root: root}
}

// coreMu serializes the index-file read-modify-write and the cores/ sweep
// across the public mutators. NewCoreStore returns a fresh struct each call,
// so an instance-level mutex would not protect anything; a package-level lock
// is the only way to keep ApplyUpdate (download) from racing Rollback/Prune/
// MarkVerified (start) and silently reverting the active pointer or deleting a
// freshly installed core.
var coreMu sync.Mutex

// CoreFileName 是内核在 cores/ 下的文件名：版本号去掉 core- 前缀，
// 与发布资产命名保持一致，便于肉眼对账。
func CoreFileName(version string) string {
	slug := strings.TrimPrefix(strings.TrimSpace(version), "core-")
	if slug == "" {
		slug = "unknown"
	}
	return "f6-core-" + runtime.GOOS + "-" + runtime.GOARCH + "-v3-" + slug + ".exe"
}

// CorePlatformKey 是清单里 platforms 的键，与 CI 产出的 target 名一致。
// GOAMD64 只对 amd64 有意义，arm64 平台不带 -v3 后缀。
func CorePlatformKey() string {
	key := runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOARCH == "amd64" {
		key += "-v3"
	}
	return key
}

func (s *CoreStore) CoresDir() string { return filepath.Join(s.root, "runtime", coresDirName) }

func (s *CoreStore) IndexPath() string { return filepath.Join(s.CoresDir(), coreIndexName) }

// Index 读取指针清单。文件缺失或损坏都返回零值：调用方随后走"扫描目录"的
// 兜底逻辑，所以清单不是单点。
func (s *CoreStore) Index() CoreIndex {
	data, err := os.ReadFile(s.IndexPath())
	if err != nil {
		return CoreIndex{}
	}
	var index CoreIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return CoreIndex{}
	}
	return index
}

func (s *CoreStore) writeIndex(index CoreIndex) error {
	if err := os.MkdirAll(s.CoresDir(), 0o700); err != nil {
		return fmt.Errorf("create cores dir: %w", err)
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	// A unique temp name: a fixed "core.json.tmp" would be deleted by sweep()
	// (which removes *.tmp) from another goroutine while this rename is pending.
	file, err := os.CreateTemp(s.CoresDir(), ".index-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, s.IndexPath())
}

// Active 解析出当前该用的内核，顺序：指针 → 目录内最高版本 → 遗留内核。
// 每一步都校验文件真实存在，避免把一个不存在的路径交给 exec。
func (s *CoreStore) Active() (Core, error) {
	index := s.Index()
	if index.Active != "" {
		if filepath.Base(index.Active) != index.Active || strings.Contains(index.Active, "..") {
			// A tampered/corrupt pointer must never escape cores/.
			return Core{}, fmt.Errorf("invalid core pointer %q", index.Active)
		}
		path := filepath.Join(s.CoresDir(), index.Active)
		if isRegularFile(path) {
			version := index.Version
			if version == "" {
				version = versionFromCoreFileName(index.Active)
			}
			return Core{Version: version, Path: path, Managed: true}, nil
		}
	}
	if core, ok := s.highestInstalled(); ok {
		return core, nil
	}
	if core, ok := s.legacyCore(); ok {
		return core, nil
	}
	return Core{}, ErrBinaryNotFound
}

// Installed 返回 cores/ 下所有受管内核，按版本从新到旧。
func (s *CoreStore) Installed() []Core {
	entries, err := os.ReadDir(s.CoresDir())
	if err != nil {
		return nil
	}
	var cores []Core
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".exe") {
			continue
		}
		// A crash mid-extract leaves ".incoming-*.exe" (never self-tested);
		// never count it as an installed, runnable core.
		if strings.HasPrefix(entry.Name(), ".incoming-") {
			continue
		}
		cores = append(cores, Core{
			Version: versionFromCoreFileName(entry.Name()),
			Path:    filepath.Join(s.CoresDir(), entry.Name()),
			Managed: true,
		})
	}
	sort.Slice(cores, func(i, j int) bool {
		return cores[i].Parsed().Compare(cores[j].Parsed()) > 0
	})
	return cores
}

func (s *CoreStore) highestInstalled() (Core, bool) {
	cores := s.Installed()
	if len(cores) == 0 {
		return Core{}, false
	}
	return cores[0], true
}

// legacyCoreCandidates 是换成 f6-core 之前的落盘位置，按优先顺序排列。
func (s *CoreStore) legacyCoreCandidates() []string {
	names := []string{
		"mihomo-" + runtime.GOOS + "-" + runtime.GOARCH + "-v3.exe",
		"mihomo-" + runtime.GOOS + "-" + runtime.GOARCH + ".exe",
		"mihomo.exe",
	}
	var paths []string
	for _, dir := range []string{"runtime", "state"} {
		for _, name := range names {
			paths = append(paths, filepath.Join(s.root, dir, name))
		}
	}
	return paths
}

// legacyCore 探测遗留内核：文件在就拿它自报的版本当版本号，探测失败也认它
// （能跑就行），版本记为空——空版本恒低于任何 f6-core 版本，更新检查自然
// 会把它当成"该升级"。
func (s *CoreStore) legacyCore() (Core, bool) {
	for _, path := range s.legacyCoreCandidates() {
		if !isRegularFile(path) {
			continue
		}
		version := probeBinaryVersion(path)
		return Core{Version: version.raw, Path: path, Managed: false}, true
	}
	return Core{}, false
}

// Has 报告某版本是否已装且文件仍在。
func (s *CoreStore) Has(version string) (Core, bool) {
	path := filepath.Join(s.CoresDir(), CoreFileName(version))
	if !isRegularFile(path) {
		return Core{}, false
	}
	return Core{Version: version, Path: path, Managed: true}, true
}

// Install 把已下载好的内核落到 cores/ 并切换指针。调用方必须先完成
// 版本自检（能跑 + 版本对得上），这里只负责落盘与切换。
//
// 切换语义：新内核成为 active，原 active 退为 previous（回滚位）。
// 旧文件此刻不删——删旧版本发生在新版本自检通过之后（GC 负责收敛）。
func (s *CoreStore) Install(exePath, version string) (Core, error) {
	coreMu.Lock()
	defer coreMu.Unlock()
	if strings.TrimSpace(version) == "" {
		return Core{}, errors.New("core version is empty")
	}
	if err := os.MkdirAll(s.CoresDir(), 0o700); err != nil {
		return Core{}, fmt.Errorf("create cores dir: %w", err)
	}
	name := CoreFileName(version)
	if filepath.Base(name) != name || strings.Contains(name, "..") {
		return Core{}, fmt.Errorf("invalid core version %q", version)
	}
	target := filepath.Join(s.CoresDir(), name)
	if !sameFile(exePath, target) {
		if err := moveInto(exePath, target); err != nil {
			return Core{}, fmt.Errorf("install core: %w", err)
		}
	}

	index := s.Index()
	active := filepath.Base(target)
	if index.Active != active {
		// 旧 active 只在新 active 确实换掉时才降级为 previous；重装同一版本
		// 时保持 previous 不变，否则回滚位会被自己顶掉。
		if index.Active != "" && isRegularFile(filepath.Join(s.CoresDir(), index.Active)) {
			index.Previous = index.Active
		}
	}
	index.Active = active
	index.Version = version
	index.InstalledAt = time.Now().UTC()
	index.VerifiedAt = time.Time{}
	if err := s.writeIndex(index); err != nil {
		return Core{}, err
	}
	return Core{Version: version, Path: target, Managed: true}, nil
}

// Rollback 把指针退回 previous。用于新内核启动失败或起不来的场景。
func (s *CoreStore) Rollback() (Core, error) {
	coreMu.Lock()
	defer coreMu.Unlock()
	index := s.Index()
	if index.Previous == "" {
		return Core{}, ErrNoRollback
	}
	path := filepath.Join(s.CoresDir(), index.Previous)
	if !isRegularFile(path) {
		return Core{}, ErrNoRollback
	}
	version := versionFromCoreFileName(index.Previous)
	index.Active, index.Previous = index.Previous, index.Active
	index.Version = version
	index.InstalledAt = time.Now().UTC()
	index.VerifiedAt = time.Time{}
	if err := s.writeIndex(index); err != nil {
		return Core{}, err
	}
	return Core{Version: version, Path: path, Managed: true}, nil
}

// MarkVerified 记录"当前这份内核在本机确实跑起来了"。调用点只有一个：
// 免流启动并通过控制器鉴权之后。它是回滚位可以被回收的唯一凭据。
func (s *CoreStore) MarkVerified() {
	coreMu.Lock()
	defer coreMu.Unlock()
	index := s.Index()
	if index.Active == "" {
		return
	}
	index.VerifiedAt = time.Now().UTC()
	_ = s.writeIndex(index)
}

// Verified 报告当前 active 内核是否已经在本机实测跑通过。
func (s *CoreStore) Verified() bool {
	index := s.Index()
	if index.Active == "" || index.VerifiedAt.IsZero() {
		return false
	}
	return !index.VerifiedAt.Before(index.InstalledAt)
}

// MarkBad 记录一个自检失败的版本，自动更新不再尝试它。
func (s *CoreStore) MarkBad(version string) {
	coreMu.Lock()
	defer coreMu.Unlock()
	version = strings.TrimSpace(version)
	if version == "" {
		return
	}
	index := s.Index()
	for _, bad := range index.Bad {
		if bad == version {
			return
		}
	}
	index.Bad = append(index.Bad, version)
	_ = s.writeIndex(index)
}

// IsBad 报告版本是否被标记为自检失败。
func (s *CoreStore) IsBad(version string) bool {
	index := s.Index()
	for _, bad := range index.Bad {
		if bad == version {
			return true
		}
	}
	return false
}

// GC 删除既不是 active 也不是 previous 的内核，以及残留的 *.tmp。
// 只在新版本自检通过、指针切换完成之后调用：此刻旧版本已经降级为
// previous，更早的版本才是可以安全回收的。
func (s *CoreStore) GC() []string {
	coreMu.Lock()
	defer coreMu.Unlock()
	index := s.Index()
	keep := map[string]bool{}
	if index.Active != "" {
		keep[index.Active] = true
	}
	if index.Previous != "" {
		keep[index.Previous] = true
	}
	return s.sweep(keep)
}

// Prune 把磁盘收敛到只剩 active 一份内核（外加清掉遗留的上游内核）。
//
// 调用时机是"当前内核已经被证明能跑"之后——应用启动时发现上次启动免流成功
// 过、或者本次启动免流跑通。回滚位只在新内核尚未验证的窗口里有价值；窗口
// 一关，多留一份 38MB 只是占磁盘。
func (s *CoreStore) Prune() []string {
	coreMu.Lock()
	defer coreMu.Unlock()
	index := s.Index()
	if index.Active == "" {
		return nil
	}
	removed := s.sweep(map[string]bool{index.Active: true})
	if len(removed) > 0 && index.Previous != "" {
		// previous 已被删掉，指针留着只会让 Rollback 指向不存在的文件。
		index.Previous = ""
		_ = s.writeIndex(index)
	}
	return append(removed, s.DeleteLegacy()...)
}

func (s *CoreStore) sweep(keep map[string]bool) []string {
	entries, err := os.ReadDir(s.CoresDir())
	if err != nil {
		return nil
	}
	var removed []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if keep[name] {
			continue
		}
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".exe") && !strings.HasSuffix(lower, ".tmp") {
			continue
		}
		path := filepath.Join(s.CoresDir(), name)
		if err := os.Remove(path); err == nil {
			removed = append(removed, path)
		}
	}
	return removed
}

// DeleteLegacy 删除老用户遗留的上游内核（约 58MB）。只在新内核装好并
// 自检通过后调用；返回被删掉的路径。
func (s *CoreStore) DeleteLegacy() []string {
	var removed []string
	for _, path := range s.legacyCoreCandidates() {
		if !isRegularFile(path) {
			continue
		}
		if err := os.Remove(path); err == nil {
			removed = append(removed, path)
		}
	}
	return removed
}

// versionFromCoreFileName 从 f6-core-<os>-<arch>-v3-<version>.exe 里取版本。
func versionFromCoreFileName(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	marker := "-v3-"
	if cut := strings.LastIndex(base, marker); cut >= 0 {
		return "core-" + base[cut+len(marker):]
	}
	return ""
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func sameFile(a, b string) bool {
	left, err := os.Stat(a)
	if err != nil {
		return false
	}
	right, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(left, right)
}

// moveInto 优先用 rename（同目录 = 同卷，零拷贝且原子；Windows 上 rename
// 会覆盖目标）；失败再退回复制，绝不先删目标——先删再 rename 失败会留下
// 「一个内核都不剩」的空目录。
func moveInto(source, target string) error {
	if filepath.Dir(source) == filepath.Dir(target) {
		if err := os.Rename(source, target); err == nil {
			return nil
		}
	}
	return copyExecutable(source, target)
}

// copyExecutable 先写唯一临时文件再 rename，避免中途失败留下半截可执行文件。
func copyExecutable(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.CreateTemp(filepath.Dir(target), ".incoming-copy-*.tmp")
	if err != nil {
		return err
	}
	temporary := output.Name()
	defer os.Remove(temporary)
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		return err
	}
	return nil
}

// probeCache 缓存 `-v` 探测结果：/runtime 是高频接口，不能每次都起进程。
var probeCache struct {
	sync.Mutex
	entries map[string]probeEntry
}

type probeEntry struct {
	version CoreVersion
	modTime time.Time
	size    int64
}

func probeBinaryVersion(path string) CoreVersion {
	info, err := os.Stat(path)
	if err != nil {
		return CoreVersion{}
	}
	probeCache.Lock()
	if probeCache.entries == nil {
		probeCache.entries = map[string]probeEntry{}
	}
	if entry, ok := probeCache.entries[path]; ok && entry.modTime.Equal(info.ModTime()) && entry.size == info.Size() {
		probeCache.Unlock()
		return entry.version
	}
	probeCache.Unlock()

	version := runProbe(path)

	probeCache.Lock()
	probeCache.entries[path] = probeEntry{version: version, modTime: info.ModTime(), size: info.Size()}
	probeCache.Unlock()
	return version
}

func runProbe(path string) CoreVersion {
	ctx, cancel := context.WithTimeout(context.Background(), coreExecTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "-v").CombinedOutput()
	if err != nil {
		return CoreVersion{}
	}
	return ProbeVersion(string(output))
}

// VerifyCore 自检一个内核文件：能执行、`-v` 报得出的版本与期望一致。
// 这是"装完就能跑"的最低成本证明——不需要起 TUN、不需要占端口。
func VerifyCore(path, expectedVersion string) error {
	version := runProbe(path)
	if !version.Valid() {
		return fmt.Errorf("内核无法执行或版本输出不可解析: %s", path)
	}
	if expectedVersion != "" {
		want := ParseCoreVersion(expectedVersion)
		if want.Valid() && version.Compare(want) != 0 {
			return fmt.Errorf("内核版本不符: 期望 %s，实际 %s", expectedVersion, version.raw)
		}
	}
	return nil
}
