package mihomo

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverBinaryPrefersInstalledRuntime(t *testing.T) {
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "runtime")
	stateDir := filepath.Join(root, "state")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(runtimeDir, "mihomo-windows-amd64-v3.exe")
	statePath := filepath.Join(stateDir, "mihomo-windows-amd64-v3.exe")
	if err := os.WriteFile(runtimePath, []byte("runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("state"), 0o700); err != nil {
		t.Fatal(err)
	}

	path, err := DiscoverBinary(root)
	if err != nil {
		t.Fatal(err)
	}
	if path != runtimePath {
		t.Fatalf("expected installed runtime %q, got %q", runtimePath, path)
	}
}

func TestDiscoverBinaryFallsBackToDevelopmentState(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(stateDir, "mihomo-windows-amd64-v3.exe")
	if err := os.WriteFile(want, []byte("state"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := DiscoverBinary(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("expected development runtime %q, got %q", want, got)
	}
}

func TestDiscoverBinaryReportsMissingRuntime(t *testing.T) {
	_, err := DiscoverBinary(t.TempDir())
	if !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("expected ErrBinaryNotFound, got %v", err)
	}
}

func TestResolveBinaryExplicitPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo.exe")
	if err := os.WriteFile(path, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveBinary(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("expected explicit path %q, got %q", path, got)
	}
}
