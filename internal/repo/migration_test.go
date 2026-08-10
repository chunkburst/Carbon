package repo

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeLegacyFile(t *testing.T, root, name string, data []byte) {
	t.Helper()
	path := filepath.Join(root, LegacyCairnDirName, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureCarbonStoreImportsLegacyTreeByteForByte(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{
		"config.yaml":                   []byte("prefix: OLD\ncounter: 8\n"),
		"tasks/OLD-1.md":                []byte("---\nid: OLD-1\n---\nbody\n"),
		"sessions/session-1.yaml":       []byte("id: session-1\n"),
		"runs/OLD-1/check.json":         []byte("{\"ok\":true}\n"),
		"views/board.json":              []byte("{\"layout\":\"board\"}\n"),
		"worklogs/private/worker.jsonl": []byte("private log\n"),
		"provenance/OLD-1.json":         []byte("[{\"who\":\"agent:old\"}]\n"),
		"binary/artifact.bin":           {0x00, 0x01, 0xff, 0x10},
	}
	for name, data := range files {
		writeLegacyFile(t, root, name, data)
	}
	writeLegacyFile(t, root, "live/OLD-1.json", []byte("stale heartbeat"))
	writeLegacyFile(t, root, "write.lock", []byte("stale lock"))
	before, err := snapshotTree(filepath.Join(root, LegacyCairnDirName))
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureCarbonStore(root); err != nil {
		t.Fatalf("EnsureCarbonStore: %v", err)
	}
	after, err := snapshotTree(filepath.Join(root, CarbonDirName))
	if err != nil {
		t.Fatal(err)
	}
	if !sameSnapshot(before, after) {
		t.Fatalf("migrated snapshot = %+v, want %+v", after, before)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(root, CarbonDirName, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("migrated %s = %q, %v; want %q", name, got, err, want)
		}
		legacy, err := os.ReadFile(filepath.Join(root, LegacyCairnDirName, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(legacy, want) {
			t.Fatalf("legacy %s changed = %q, %v", name, legacy, err)
		}
	}
	for _, ephemeral := range []string{"live", "write.lock"} {
		if _, err := os.Stat(filepath.Join(root, CarbonDirName, ephemeral)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ephemeral legacy entry %s was imported: %v", ephemeral, err)
		}
	}
	receipt, exists, err := ReadMigrationReceipt(root)
	if err != nil || !exists || !receiptMatches(receipt, before) {
		t.Fatalf("receipt = %+v exists=%v err=%v", receipt, exists, err)
	}
	if err := EnsureCarbonStore(root); err != nil {
		t.Fatalf("second EnsureCarbonStore: %v", err)
	}
}

func TestEnsureCarbonStorePrefersCanonicalWhenBothExist(t *testing.T) {
	root := t.TempDir()
	writeLegacyFile(t, root, "tasks/OLD-1.md", []byte("legacy"))
	canonical := filepath.Join(root, CarbonDirName, "tasks")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canonical, "NEW-1.md"), []byte("canonical"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCarbonStore(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(canonical, "OLD-1.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy content was merged into canonical: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(canonical, "NEW-1.md")); err != nil || string(got) != "canonical" {
		t.Fatalf("canonical content changed = %q, %v", got, err)
	}
}

func TestEnsureCarbonStoreRecoversVerifiedInterruptedStage(t *testing.T) {
	root := t.TempDir()
	writeLegacyFile(t, root, "tasks/OLD-2.md", []byte("keep me"))
	legacy := filepath.Join(root, LegacyCairnDirName)
	source, err := snapshotTree(legacy)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := os.MkdirTemp(root, migrationStagePrefix)
	if err != nil {
		t.Fatal(err)
	}
	if err := copyTree(legacy, stage); err != nil {
		t.Fatal(err)
	}
	if err := writeStageReceipt(stage, receiptFor(source)); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCarbonStore(root); err != nil {
		t.Fatalf("recover interrupted migration: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, CarbonDirName, "tasks", "OLD-2.md")); err != nil || string(got) != "keep me" {
		t.Fatalf("recovered data = %q, %v", got, err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging directory remains after recovery: %v", err)
	}
}

func TestEnsureCarbonStoreRejectsLegacyReparseAndTraversal(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, LegacyCairnDirName)
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	repoSymlinkOrSkip(t, external, filepath.Join(legacy, "escaped"))
	if err := EnsureCarbonStore(root); !errors.Is(err, ErrUnsafeLegacyMigration) {
		t.Fatalf("EnsureCarbonStore through nested symlink = %v, want unsafe migration", err)
	}
	if _, err := os.Stat(filepath.Join(root, CarbonDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe migration published canonical directory: %v", err)
	}
}

func TestEnsureCarbonStoreRejectsLegacyRootReparse(t *testing.T) {
	root := t.TempDir()
	repoSymlinkOrSkip(t, t.TempDir(), filepath.Join(root, LegacyCairnDirName))
	if err := EnsureCarbonStore(root); !errors.Is(err, ErrUnsafeLegacyMigration) {
		t.Fatalf("EnsureCarbonStore through legacy root symlink = %v, want unsafe migration", err)
	}
}

func TestValidateCairnPathMapsLegacySuffixToCanonical(t *testing.T) {
	root := t.TempDir()
	writeLegacyFile(t, root, "tasks/OLD-3.md", []byte("task"))
	path, err := ValidateCairnPath(root, filepath.Join(root, LegacyCairnDirName, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, CarbonDirName, "tasks")
	if filepath.Clean(path) != filepath.Clean(want) {
		t.Fatalf("compat path = %q, want %q", path, want)
	}
}

func TestValidateCairnPathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside")
	if _, err := ValidateCairnPath(root, outside); !errors.Is(err, ErrCarbonPathOutsideRoot) {
		t.Fatalf("ValidateCairnPath traversal = %v, want ErrCarbonPathOutsideRoot", err)
	}
}
