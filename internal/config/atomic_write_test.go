package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSaveDurablyReplacesWithPrivatePermissions(t *testing.T) {
	path := writeConfig(t, sample)
	value, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	value.Counter = 77
	if err := Save(path, value); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Counter != 77 {
		t.Fatalf("saved counter = %d, want 77", got.Counter)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := info.Mode().Perm(), os.FileMode(configWriteFileMode); got != want {
			t.Fatalf("config permissions = %04o, want %04o", got, want)
		}
	}
	assertNoConfigAtomicTemps(t, filepath.Dir(path))
}

func TestSaveRejectsNonRegularTargetWithoutTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, Default("PROJ")); !errors.Is(err, ErrUnsafeConfigPath) {
		t.Fatalf("Save directory = %v, want ErrUnsafeConfigPath", err)
	}
	assertNoConfigAtomicTemps(t, dir)
}

func TestAtomicWriteReplaceFailurePreservesOldConfigAndCleansTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	const original = "prefix: OLD\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("simulated final replace failure")
	err := atomicWriteWithReplace(path, []byte("prefix: NEW\n"), func(_, _ string) error {
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("atomic config replace failure = %v, want injected failure", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Fatalf("old config changed after failed replace: %q", got)
	}
	assertNoConfigAtomicTemps(t, dir)
}

func TestAtomicWriteReportsPublishedWhenParentSyncFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("prefix: OLD\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	syncFailure := errors.New("simulated parent sync failure")
	err := atomicWriteWithDurability(path, []byte("prefix: NEW\n"), atomicReplace, func(string) error {
		return syncFailure
	}, nil)
	if !errors.Is(err, ErrConfigWritePublished) || !errors.Is(err, syncFailure) {
		t.Fatalf("atomic config parent sync failure = %v, want published + sync failure", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "prefix: NEW\n" {
		t.Fatalf("published config = %q", got)
	}
	assertNoConfigAtomicTemps(t, dir)
}

func TestAtomicWriteRevalidatesPathImmediatelyBeforePublication(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	const original = "prefix: OLD\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	unsafeAfterStaging := errors.New("simulated path swap detected before publish")
	validateCalls := 0
	replaceCalled := false
	err := atomicWriteWithDurability(path, []byte("prefix: NEW\n"), func(_, _ string) error {
		replaceCalled = true
		return nil
	}, syncAtomicParent, func(string) error {
		validateCalls++
		if validateCalls == 2 {
			return unsafeAfterStaging
		}
		return nil
	})
	if !errors.Is(err, unsafeAfterStaging) {
		t.Fatalf("atomic config revalidation = %v, want injected path failure", err)
	}
	if validateCalls != 2 {
		t.Fatalf("config path validations = %d, want initial + pre-publication validation", validateCalls)
	}
	if replaceCalled {
		t.Fatal("replacement ran after pre-publication path validation failed")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Fatalf("old config changed after failed revalidation: %q", got)
	}
	assertNoConfigAtomicTemps(t, dir)
}

func TestAtomicTempIdentityRejectsRegularFileSubstitution(t *testing.T) {
	dir := t.TempDir()
	tmp, err := os.CreateTemp(dir, "carbon-config-*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := secureAtomicTempFile(tmp); err != nil {
		t.Fatal(err)
	}
	if _, err := tmp.WriteString("original staged bytes\n"); err != nil {
		t.Fatal(err)
	}
	if err := tmp.Sync(); err != nil {
		t.Fatal(err)
	}
	identity, err := captureAtomicTempIdentity(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "replacement.tmp")
	defer os.Remove(replacement)
	if err := os.WriteFile(replacement, []byte("substituted bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(tmpName); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, tmpName); err != nil {
		t.Fatal(err)
	}
	if err := validateAtomicTempFile(tmpName, identity); err == nil {
		t.Fatal("substituted regular temp file passed identity validation")
	}
	got, err := os.ReadFile(tmpName)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "substituted bytes\n" {
		t.Fatalf("substituted temp content = %q", got)
	}
}

func TestAtomicWriteFailureCleanupPreservesSubstitutedTempEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	const original = "prefix: OLD\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	var substituted string
	validateCalls := 0
	err := atomicWriteWithDurability(path, []byte("prefix: NEW\n"), atomicReplace, syncAtomicParent, func(string) error {
		validateCalls++
		if validateCalls != 2 {
			return nil
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "carbon-config-") && strings.HasSuffix(entry.Name(), ".tmp") {
				substituted = filepath.Join(dir, entry.Name())
				break
			}
		}
		if substituted == "" {
			return errors.New("staged config temp file not found")
		}
		if err := os.Remove(substituted); err != nil {
			return err
		}
		return os.WriteFile(substituted, []byte("substituted entry\n"), 0o600)
	})
	if err == nil {
		t.Fatal("config atomic write succeeded after its temp name was substituted")
	}
	defer os.Remove(substituted)
	got, readErr := os.ReadFile(substituted)
	if readErr != nil {
		t.Fatalf("failure cleanup removed substituted temp entry: %v", readErr)
	}
	if string(got) != "substituted entry\n" {
		t.Fatalf("substituted entry = %q", got)
	}
	final, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(final) != original {
		t.Fatalf("final config changed after temp substitution: %q", final)
	}
}

func TestSaveRejectsSymlinkTargetWithoutTouchingExternalFile(t *testing.T) {
	dir := t.TempDir()
	external := filepath.Join(t.TempDir(), "external.yaml")
	const original = "external: must-not-change\n"
	if err := os.WriteFile(external, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(external, path); err != nil {
		t.Skipf("symlink unavailable (for example, Windows without Developer Mode): %v", err)
	}

	if err := Save(path, Default("PROJ")); !errors.Is(err, ErrUnsafeConfigPath) {
		t.Fatalf("Save symlink = %v, want ErrUnsafeConfigPath", err)
	}
	got, err := os.ReadFile(external)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("external config changed: %q", got)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("unsafe final entry was replaced instead of rejected: %s", path)
	}
	assertNoConfigAtomicTemps(t, dir)
}

func TestSaveRejectsSymlinkedParentComponent(t *testing.T) {
	dir := t.TempDir()
	realParent := filepath.Join(dir, "real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	linkParent := filepath.Join(dir, "link")
	if err := os.Symlink(realParent, linkParent); err != nil {
		t.Skipf("symlink unavailable (for example, Windows without Developer Mode): %v", err)
	}
	path := filepath.Join(linkParent, "config.yaml")
	if err := Save(path, Default("PROJ")); !errors.Is(err, ErrUnsafeConfigPath) {
		t.Fatalf("Save through symlinked parent = %v, want ErrUnsafeConfigPath", err)
	}
	entries, err := os.ReadDir(realParent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("config write followed symlinked parent: %+v", entries)
	}
}

func assertNoConfigAtomicTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "carbon-config-") && strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("config atomic temporary file left behind: %s", entry.Name())
		}
	}
}
