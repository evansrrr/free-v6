package mihomo

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func coreZipBytes(t *testing.T, payload string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create(CoreFileName("core-v0.2.0"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func manifestJSON(t *testing.T, version, assetURL, sha string) []byte {
	t.Helper()
	manifest := map[string]any{
		"version": version,
		"channel": "stable",
		"notes":   "test",
		"platforms": map[string]any{
			CorePlatformKey(): map[string]any{"url": assetURL, "sha256": sha, "size": 1024},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseCoreManifest(t *testing.T) {
	good := manifestJSON(t, "core-v0.2.0", "https://github.com/evansrrr/f6-core/releases/download/core-v0.2.0/core.zip", strings.Repeat("a", 64))
	manifest, err := parseCoreManifest(good)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "core-v0.2.0" {
		t.Fatalf("version = %q", manifest.Version)
	}
	if _, ok := manifest.Asset(); !ok {
		t.Fatal("asset lookup failed")
	}

	bad := [][]byte{
		[]byte(`{"platforms":{}}`),
		[]byte(`{"version":"core-v0.2.0"}`),
		[]byte(`not json`),
		// Path-traversal versions must be rejected before they reach a file name.
		[]byte(`{"version":"core-v0.1.0-../../../x","platforms":{"windows-amd64-v3":{"url":"https://example.com/c.zip","sha256":"` + strings.Repeat("a", 64) + `"}}}`),
	}
	for _, data := range bad {
		if _, err := parseCoreManifest(data); err == nil {
			t.Fatalf("expected error for %s", data)
		}
	}
}

// 清单拉到一次之后就缓存；镜像全挂时用过期缓存，离线也能判断"有没有更新"。
func TestFetchCoreManifestPrefersCacheOnFailure(t *testing.T) {
	root := t.TempDir()
	body := manifestJSON(t, "core-v0.2.0", "https://example.com/core.zip", strings.Repeat("b", 64))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	original := coreManifestURLs
	coreManifestURLs = func() []string { return []string{server.URL} }
	t.Cleanup(func() { coreManifestURLs = original })

	first, err := FetchCoreManifest(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != "core-v0.2.0" {
		t.Fatalf("version = %q", first.Version)
	}
	server.Close()

	second, err := FetchCoreManifest(root, true)
	if err != nil {
		t.Fatalf("expected cached fallback, got %v", err)
	}
	if second.Version != first.Version {
		t.Fatalf("cached version = %q", second.Version)
	}
}

func TestCheckCoreUpdateComparesVersions(t *testing.T) {
	root := t.TempDir()
	store := NewCoreStore(root)
	if _, err := store.Install(writeTempExe(t, "old"), "core-v0.1.0"); err != nil {
		t.Fatal(err)
	}
	body := manifestJSON(t, "core-v0.2.0", "https://example.com/core.zip", strings.Repeat("c", 64))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()
	original := coreManifestURLs
	coreManifestURLs = func() []string { return []string{server.URL} }
	t.Cleanup(func() { coreManifestURLs = original })

	check, err := CheckCoreUpdate(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if !check.Available || check.Latest != "core-v0.2.0" || check.Installed != "core-v0.1.0" {
		t.Fatalf("unexpected check: %+v", check)
	}
	if check.Legacy {
		t.Fatal("managed core must not be flagged legacy")
	}
}

// 老用户机上的 58MB 上游内核：版本比较天然判定为"该更新"，不需要额外状态位。
func TestCheckCoreUpdateSeesLegacyCoreAsOutdated(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "runtime", "mihomo-"+platformSuffix()+"-v3.exe"), "legacy")
	body := manifestJSON(t, "core-v0.2.0", "https://example.com/core.zip", strings.Repeat("d", 64))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()
	original := coreManifestURLs
	coreManifestURLs = func() []string { return []string{server.URL} }
	t.Cleanup(func() { coreManifestURLs = original })

	check, err := CheckCoreUpdate(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if !check.Available || !check.Legacy {
		t.Fatalf("legacy core should be seen as updatable: %+v", check)
	}
}

func TestApplyUpdateInstallsAndDropsLegacy(t *testing.T) {
	root := t.TempDir()
	store := NewCoreStore(root)
	legacy := filepath.Join(root, "runtime", "mihomo-"+platformSuffix()+"-v3.exe")
	writeFile(t, legacy, "legacy upstream core")

	archive := coreZipBytes(t, "new core")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer server.Close()
	manifest, err := parseCoreManifest(manifestJSON(t, "core-v0.2.0", server.URL, sha256Hex(archive)))
	if err != nil {
		t.Fatal(err)
	}

	// 自检会 exec 内核真实二进制；测试里换成空实现，只验证流水线本身。
	originalVerify := verifyCore
	verifyCore = func(path, version string) error { return nil }
	t.Cleanup(func() { verifyCore = originalVerify })

	// The production asset URL is pinned to GitHub hosts; allow the local test
	// server for this hermetic download.
	originalValidate := validCoreAssetURL
	validCoreAssetURL = func(string) bool { return true }
	t.Cleanup(func() { validCoreAssetURL = originalValidate })

	result, err := store.ApplyUpdate(context.Background(), manifest, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != "core-v0.2.0" {
		t.Fatalf("version = %q", result.Version)
	}
	if _, statErr := os.Stat(result.Path); statErr != nil {
		t.Fatalf("installed core missing: %v", statErr)
	}
	if _, statErr := os.Stat(legacy); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("legacy core should be removed after a successful install")
	}
	active, err := store.Active()
	if err != nil || active.Version != "core-v0.2.0" || !active.Managed {
		t.Fatalf("unexpected active core: %+v %v", active, err)
	}
}

func TestApplyUpdateRejectsOlderManifest(t *testing.T) {
	root := t.TempDir()
	store := NewCoreStore(root)
	if _, err := store.Install(writeTempExe(t, "new"), "core-v0.2.0"); err != nil {
		t.Fatal(err)
	}
	manifest, err := parseCoreManifest(manifestJSON(t, "core-v0.1.0", "https://github.com/evansrrr/f6-core/releases/download/core-v0.1.0/core.zip", strings.Repeat("e", 64)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyUpdate(context.Background(), manifest, false, nil); !errors.Is(err, ErrCoreUpToDate) {
		t.Fatalf("expected ErrCoreUpToDate, got %v", err)
	}
}

func TestApplyUpdateHonoursBadVersionList(t *testing.T) {
	root := t.TempDir()
	store := NewCoreStore(root)
	store.MarkBad("core-v0.9.0")
	manifest, err := parseCoreManifest(manifestJSON(t, "core-v0.9.0", "https://github.com/evansrrr/f6-core/releases/download/core-v0.9.0/core.zip", strings.Repeat("f", 64)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyUpdate(context.Background(), manifest, false, nil); err == nil {
		t.Fatal("expected bad-version guard to reject the update")
	}
}

// platformSuffix 拼出 legacy 内核名里的 <os>-<arch> 段。
func platformSuffix() string {
	return CorePlatformKey()[:len(CorePlatformKey())-len("-v3")]
}

func TestValidCoreAssetURL(t *testing.T) {
	cases := map[string]bool{
		"https://github.com/evansrrr/f6-core/releases/download/core-v0.3.0/core.zip": true,
		"https://objects.githubusercontent.com/x/y":                                  true,
		"https://release-assets.githubusercontent.com/x/y":                           true,
		"http://github.com/x":               false, // plain http
		"https://evil.example/x.zip":        false, // arbitrary host
		"https://127.0.0.1/x.zip":           false, // loopback
		"https://github.com.evil.net/x.zip": false, // suffix squat
	}
	for rawURL, want := range cases {
		if got := validCoreAssetURL(rawURL); got != want {
			t.Fatalf("validCoreAssetURL(%q) = %v, want %v", rawURL, got, want)
		}
	}
}
