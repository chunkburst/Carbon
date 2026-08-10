package store

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAtomicWriteDurablyReplacesAndUsesPrivatePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task.md")
	if err := os.WriteFile(path, []byte("old complete document\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := atomicWrite(path, []byte("new complete document\n")); err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new complete document\n" {
		t.Fatalf("published content = %q", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := info.Mode().Perm(), os.FileMode(managedWriteFileMode); got != want {
			t.Fatalf("published permissions = %04o, want %04o", got, want)
		}
	}
	assertNoStoreAtomicTemps(t, dir)
}

func TestAtomicWriteRejectsUnsafeTargetAndCleansTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	// A directory destination is a portable failure case even where symlink creation is
	// unavailable. It must be rejected before a final-file replacement is attempted.
	if err := atomicWrite(dir, []byte("must not replace a directory")); !errors.Is(err, errUnsafeAtomicWriteTarget) {
		t.Fatalf("atomicWrite directory = %v, want unsafe target", err)
	}
	assertNoStoreAtomicTemps(t, dir)
}

func TestAtomicWriteReplaceFailurePreservesOldFileAndCleansTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task.md")
	const original = "old complete document\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("simulated final replace failure")
	err := atomicWriteWithReplace(path, []byte("new document\n"), func(_, _ string) error {
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("atomicWrite replace failure = %v, want injected failure", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Fatalf("old file changed after failed replace: %q", got)
	}
	assertNoStoreAtomicTemps(t, dir)
}

func TestAtomicWriteReportsPublishedWhenParentSyncFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task.md")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	syncFailure := errors.New("simulated parent sync failure")
	err := atomicWriteWithDurability(path, []byte("new\n"), atomicReplace, func(string) error {
		return syncFailure
	}, nil)
	if !errors.Is(err, ErrAtomicWritePublished) || !errors.Is(err, syncFailure) {
		t.Fatalf("atomicWrite parent sync failure = %v, want published + sync failure", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "new\n" {
		t.Fatalf("published file = %q, want new content", got)
	}
	assertNoStoreAtomicTemps(t, dir)
}

func TestAtomicWriteRevalidatesManagedPathImmediatelyBeforePublication(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task.md")
	const original = "old complete document\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	unsafeAfterStaging := errors.New("simulated path swap detected before publish")
	validateCalls := 0
	replaceCalled := false
	err := atomicWriteWithDurability(path, []byte("new document\n"), func(_, _ string) error {
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
		t.Fatalf("atomicWrite revalidation = %v, want injected path failure", err)
	}
	if validateCalls != 2 {
		t.Fatalf("managed path validations = %d, want initial + pre-publication validation", validateCalls)
	}
	if replaceCalled {
		t.Fatal("replacement ran after pre-publication path validation failed")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Fatalf("old file changed after failed revalidation: %q", got)
	}
	assertNoStoreAtomicTemps(t, dir)
}

func TestAtomicTempIdentityRejectsRegularFileSubstitution(t *testing.T) {
	dir := t.TempDir()
	tmp, err := os.CreateTemp(dir, "carbon-write-*.tmp")
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
	path := filepath.Join(dir, "task.md")
	const original = "old complete document\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	var substituted string
	validateCalls := 0
	err := atomicWriteWithDurability(path, []byte("new document\n"), atomicReplace, syncAtomicParent, func(string) error {
		validateCalls++
		if validateCalls != 2 {
			return nil
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "carbon-write-") && strings.HasSuffix(entry.Name(), ".tmp") {
				substituted = filepath.Join(dir, entry.Name())
				break
			}
		}
		if substituted == "" {
			return errors.New("staged temp file not found")
		}
		if err := os.Remove(substituted); err != nil {
			return err
		}
		return os.WriteFile(substituted, []byte("substituted entry\n"), 0o600)
	})
	if err == nil {
		t.Fatal("atomicWrite succeeded after its temp name was substituted")
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
		t.Fatalf("final path changed after temp substitution: %q", final)
	}
}

func TestSaveIfVersionStillUsesOptimisticConcurrency(t *testing.T) {
	s := New(repo(t, map[string]string{"PROJ-001": minimalTask}))
	stale, err := s.Get("PROJ-001")
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Get("PROJ-001")
	if err != nil {
		t.Fatal(err)
	}
	if err := current.SetTitle("first durable writer"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveIfVersion(current, current.ETag()); err != nil {
		t.Fatalf("SaveIfVersion current document: %v", err)
	}

	if err := stale.SetTitle("stale writer"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveIfVersion(stale, stale.ETag()); !errors.Is(err, ErrConflict) {
		t.Fatalf("SaveIfVersion stale document = %v, want ErrConflict", err)
	}
	if err := s.SaveIfVersion(stale, `"definitely-wrong-version"`); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("SaveIfVersion wrong token = %v, want ErrVersionMismatch", err)
	}

	got, err := s.Get("PROJ-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.Task.Title != "first durable writer" {
		t.Fatalf("stale SaveIfVersion overwrote final title: %q", got.Task.Title)
	}
}

func TestAtomicWriteRejectsFinalSymlinkWithoutTouchingTarget(t *testing.T) {
	dir := t.TempDir()
	external := filepath.Join(t.TempDir(), "outside.md")
	const original = "outside data must survive\n"
	if err := os.WriteFile(external, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "task.md")
	if err := os.Symlink(external, path); err != nil {
		t.Skipf("symlink unavailable (for example, Windows without Developer Mode): %v", err)
	}

	if err := atomicWrite(path, []byte("replacement")); !errors.Is(err, errUnsafeAtomicWriteTarget) {
		t.Fatalf("atomicWrite symlink = %v, want unsafe target", err)
	}
	got, err := os.ReadFile(external)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("external target changed: %q", got)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("unsafe final entry was replaced instead of rejected: %s", path)
	}
	assertNoStoreAtomicTemps(t, dir)
}

func TestStoreWriteRejectsInternalManagedDirectorySymlink(t *testing.T) {
	s := New(repo(t, map[string]string{}))
	inside := filepath.Join(s.Root(), "inside-tasks")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.tasksDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, s.tasksDir()); err != nil {
		t.Skipf("symlink unavailable (for example, Windows without Developer Mode): %v", err)
	}
	if _, err := s.Create(Draft{Title: "must not follow an internal link"}, "agent:test", time.Now()); !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("Create through internal tasks symlink = %v, want ErrPathOutsideRoot", err)
	}
	entries, err := os.ReadDir(inside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("write escaped into internal symlink target: %+v", entries)
	}
}

func assertNoStoreAtomicTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "carbon-write-") && strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("atomic temporary file left behind: %s", entry.Name())
		}
	}
}
