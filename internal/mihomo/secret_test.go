package mihomo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateSecret(t *testing.T) {
	stateDir := t.TempDir()

	// Nothing stored yet: a secret is created and persisted.
	first, err := LoadOrCreateSecret(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 64 || strings.Trim(first, "0123456789abcdef") != "" {
		t.Fatalf("secret %q is not 64 hex chars", first)
	}

	// Second call must reuse the stored value — a rotating secret would
	// desync the helper's status reads from the running mihomo config.
	second, err := LoadOrCreateSecret(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("secret rotated unexpectedly: %q != %q", second, first)
	}

	stored, err := os.ReadFile(filepath.Join(stateDir, controllerSecretFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(stored)) != first {
		t.Fatal("persisted secret differs from returned value")
	}
}

func TestReadControllerSecretMissingFile(t *testing.T) {
	secret, err := ReadControllerSecret(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		t.Fatalf("expected empty secret before creation, got %q", secret)
	}
}

func TestLoadOrCreateSecretReplacesEmptyFile(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, controllerSecretFileName), []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret, err := LoadOrCreateSecret(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 64 {
		t.Fatalf("expected fresh 64-hex secret, got %q", secret)
	}
}
