package mihomo

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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
//
// Creation is atomic: a unique temp file is fsynced and hard-linked into place
// so a concurrent creator (helper + CLI racing on first boot) can never make a
// reader observe a half-written file, and both end up with the same value.
func LoadOrCreateSecret(stateDir string) (string, error) {
	secret, err := ReadControllerSecret(stateDir)
	if err != nil {
		return "", err
	}
	if validSecret(secret) {
		return secret, nil
	}
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate controller secret: %w", err)
	}
	fresh := hex.EncodeToString(bytes[:])
	path := filepath.Join(stateDir, controllerSecretFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	// publishSecret handles "already exists" (adopt the winner) and a
	// pre-existing corrupt/empty file (clear + re-link) atomically — do NOT
	// remove the file here: a concurrent creator may already have published a
	// valid secret, and a blind Remove would clobber it (observed as a secret
	// mismatch under -race).
	if err := publishSecret(stateDir, path, fresh); err != nil {
		return "", err
	}
	// A concurrent creator may have won the race: prefer the stored value so
	// every caller ends up with the same secret.
	stored, rerr := ReadControllerSecret(stateDir)
	if rerr == nil && validSecret(stored) {
		return stored, nil
	}
	return fresh, nil
}

// validSecret reports whether s is a well-formed 64-hex controller secret. A
// truncated file would otherwise be adopted as-is and silently 401 every local
// API call.
func validSecret(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// publishSecret atomically installs secret at path. It writes a unique temp
// file, fsyncs it, then hard-links it into place; on filesystems without hard
// links (or when a concurrent creator already won), it falls back to an
// exclusive create with read-back retries.
func publishSecret(stateDir, path, secret string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".secret-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(secret + "\n"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpName, path); err == nil {
		return nil
	}
	// Already exists: either a valid winner (adopt it — first-writer-wins keeps
	// every caller on the same secret) or a corrupt/empty file left by a
	// pre-atomic build (clear it and re-link). Read-back before clearing so a
	// valid secret is never removed.
	for attempt := 0; attempt < 5; attempt++ {
		if stored, rerr := ReadControllerSecret(stateDir); rerr == nil && validSecret(stored) {
			return nil
		}
		_ = os.Remove(path)
		if err := os.Link(tmpName, path); err == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("controller secret already exists with an unusable value")
}
