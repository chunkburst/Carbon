package cluster

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func writeLegacyManifest(t *testing.T, root string, manifest Manifest) []byte {
	t.Helper()
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(root, LegacyManifestFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestReadImportsLegacyManifestAndWritesCanonicalName(t *testing.T) {
	useTestLockCache(t)
	root := t.TempDir()
	legacy := Manifest{Version: Version, Name: "Old cluster", Projects: []Project{}}
	data := writeLegacyManifest(t, root, legacy)

	got, exists, err := Read(root)
	if err != nil || !exists || got.Name != legacy.Name {
		t.Fatalf("Read legacy manifest = %+v exists=%v err=%v", got, exists, err)
	}
	canonical, err := os.ReadFile(filepath.Join(root, ManifestFilename))
	if err != nil || !bytes.Equal(canonical, data) {
		t.Fatalf("canonical imported manifest = %q err=%v, want %q", canonical, err, data)
	}
	stillLegacy, err := os.ReadFile(filepath.Join(root, LegacyManifestFilename))
	if err != nil || !bytes.Equal(stillLegacy, data) {
		t.Fatalf("legacy manifest changed = %q err=%v", stillLegacy, err)
	}
	if _, err := os.Stat(filepath.Join(root, migrationReceiptName)); err != nil {
		t.Fatalf("migration receipt missing: %v", err)
	}
}

func TestReadPrefersCanonicalManifestWhenBothNamesExist(t *testing.T) {
	useTestLockCache(t)
	root := t.TempDir()
	writeLegacyManifest(t, root, Manifest{Version: Version, Name: "Old", Projects: []Project{}})
	if err := writeManifest(root, Manifest{Version: Version, Name: "Canonical", Projects: []Project{}}); err != nil {
		t.Fatal(err)
	}
	got, exists, err := Read(root)
	if err != nil || !exists || got.Name != "Canonical" {
		t.Fatalf("canonical precedence = %+v exists=%v err=%v", got, exists, err)
	}
}

func TestEnsureAndAddProjectUseCanonicalPaths(t *testing.T) {
	useTestLockCache(t)
	root := t.TempDir()
	project := t.TempDir()

	manifest, err := Ensure(root, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != Version || manifest.Name == "" || len(manifest.Projects) != 0 {
		t.Fatalf("new manifest = %+v", manifest)
	}

	manifest, added, err := AddProject(root, filepath.Join(project, "."), "")
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := ResolveRoot(project)
	if err != nil {
		t.Fatal(err)
	}
	if added.Path != canonical || len(manifest.Projects) != 1 {
		t.Fatalf("added project = %+v, manifest = %+v", added, manifest)
	}
	if _, _, err := AddProject(root, project, "again"); !errors.Is(err, ErrDuplicateProject) {
		t.Fatalf("duplicate canonical project = %v, want ErrDuplicateProject", err)
	}

	data, err := os.ReadFile(filepath.Join(root, ManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("manifest is not complete JSON with trailing newline: %q", data)
	}
}

func TestReadKeepsOfflineProjectRegistered(t *testing.T) {
	useTestLockCache(t)
	root := t.TempDir()
	project := filepath.Join(t.TempDir(), "offline-project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(root, "cluster", false); err != nil {
		t.Fatal(err)
	}
	_, added, err := AddProject(root, project, "offline")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(project); err != nil {
		t.Fatal(err)
	}

	manifest, exists, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || len(manifest.Projects) != 1 || manifest.Projects[0].ID != added.ID {
		t.Fatalf("offline manifest = %+v, exists=%v", manifest, exists)
	}
}

func TestEnsureDoesNotLeaveClusterRootLock(t *testing.T) {
	useTestLockCache(t)
	root := t.TempDir()

	if _, err := Ensure(root, "cluster", false); err != nil {
		t.Fatal(err)
	}
	assertNoClusterRootLock(t, root)
}

func TestWithLockCleansUpClusterRootArtifactAfterCallbackError(t *testing.T) {
	useTestLockCache(t)
	root := t.TempDir()
	want := errors.New("callback failed")

	err := withLock(root, func() error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("withLock error = %v, want callback error", err)
	}
	assertNoClusterRootLock(t, root)
}

func TestConcurrentProjectRegistrationsRemainSerializedWithoutRootLock(t *testing.T) {
	useTestLockCache(t)
	root := t.TempDir()
	if _, err := Ensure(root, "cluster", false); err != nil {
		t.Fatal(err)
	}

	const projectCount = 24
	projects := make([]string, projectCount)
	for i := range projects {
		projects[i] = t.TempDir()
	}

	start := make(chan struct{})
	errs := make(chan error, projectCount)
	var wg sync.WaitGroup
	for i, project := range projects {
		wg.Add(1)
		go func(i int, project string) {
			defer wg.Done()
			<-start
			_, _, err := AddProject(root, project, fmt.Sprintf("project %d", i))
			errs <- err
		}(i, project)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	manifest, exists, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || len(manifest.Projects) != projectCount {
		t.Fatalf("registered projects = %d, exists=%v, want %d", len(manifest.Projects), exists, projectCount)
	}
	assertNoClusterRootLock(t, root)
}

func TestWithLockRejectsSymlinkedCacheNamespace(t *testing.T) {
	cacheRoot := useTestLockCache(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(cacheRoot, lockCacheNamespace)); err != nil {
		t.Skipf("symlink unavailable on this host: %v", err)
	}

	err := withLock(t.TempDir(), func() error {
		t.Fatal("callback must not run with an unsafe cache lock path")
		return nil
	})
	if !errors.Is(err, ErrUnsafeManifest) {
		t.Fatalf("withLock error = %v, want ErrUnsafeManifest", err)
	}
}

func useTestLockCache(t *testing.T) string {
	t.Helper()
	cacheRoot := t.TempDir()
	previous := userCacheDir
	userCacheDir = func() (string, error) { return cacheRoot, nil }
	t.Cleanup(func() { userCacheDir = previous })
	return cacheRoot
}

func assertNoClusterRootLock(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(root, manifestLockName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cluster root lock artifact error = %v, want absent", err)
	}
}
