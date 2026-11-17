package mihomo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveStaleRuleSetFilesKeepsReferencedOnes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state", "ruleset")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := RuleSetFileNames()[0]
	for _, name := range []string{keep, "rule00.list", "rule04.list", "note.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	removed := RemoveStaleRuleSetFiles(root)
	if len(removed) != 3 {
		t.Fatalf("expected 3 stale files removed, got %v", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
		t.Fatalf("referenced rule set must survive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "rule00.list")); !os.IsNotExist(err) {
		t.Fatal("old numbered cache must be removed")
	}
}

func TestRemoveStaleRuleSetFilesToleratesMissingDir(t *testing.T) {
	if removed := RemoveStaleRuleSetFiles(t.TempDir()); len(removed) != 0 {
		t.Fatalf("expected no-op, got %v", removed)
	}
}
