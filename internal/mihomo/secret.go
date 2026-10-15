package mihomo

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const controllerSecretFileName = "mihomo-controller.secret"

// ReadControllerSecret returns the stored external-controller secret, or ""
// when no secret file exists yet (pre-secret install, or the file was
// removed while mihomo keeps running). Read-only: status polling must never
// create the file — only an actual proxy start does.
func ReadControllerSecret(stateDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, controllerSecretFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// LoadOrCreateSecret returns the per-install external-controller secret,
// creating a fresh 32-byte value on first use. The helper and the CLI must
// derive the SAME secret — the config one renders and the API the other
// checks against a running mihomo would otherwise disagree, and a new helper
// taking over a still-running instance has nothing else to recover it from.
func LoadOrCreateSecret(stateDir string) (string, error) {
	if secret, err := ReadControllerSecret(stateDir); err != nil {
		return "", err
	} else if secret != "" {
		return secret, nil
	}
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate controller secret: %w", err)
	}
	secret := hex.EncodeToString(bytes[:])
	path := filepath.Join(stateDir, controllerSecretFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		return "", err
	}
	return secret, nil
}
