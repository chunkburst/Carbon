package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreOpenMigratesLegacyAndSubsequentWritesUseCarbon(t *testing.T) {
	root := t.TempDir()
	legacyTasks := filepath.Join(root, ".cairn", "tasks")
	if err := os.MkdirAll(legacyTasks, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyConfig := []byte("prefix: PROJ\ncounter: 2\nstates: [backlog, in_progress, in_review, done, canceled]\nclosed: [done, canceled]\ninitial: backlog\ncheck_timeout_default: 120\n")
	if err := os.WriteFile(filepath.Join(root, ".cairn", "config.yaml"), legacyConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyTasks, "PROJ-001.md"), []byte(minimalTask), 0o600); err != nil {
		t.Fatal(err)
	}

	s := New(root)
	if _, err := s.Get("PROJ-001"); err != nil {
		t.Fatalf("Get legacy task through Store = %v", err)
	}
	carbonConfig := filepath.Join(root, ".carbon", "config.yaml")
	if _, err := os.Stat(carbonConfig); err != nil {
		t.Fatalf("canonical config missing after open: %v", err)
	}
	cfg, err := s.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Counter = 9
	if err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	legacyAfter, err := os.ReadFile(filepath.Join(root, ".cairn", "config.yaml"))
	if err != nil || string(legacyAfter) != string(legacyConfig) {
		t.Fatalf("legacy config was changed = %q err=%v", legacyAfter, err)
	}
	canonicalAfter, err := os.ReadFile(carbonConfig)
	if err != nil || string(canonicalAfter) == string(legacyConfig) {
		t.Fatalf("canonical config was not updated = %q err=%v", canonicalAfter, err)
	}
}
