package home

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"carbon/internal/repo"
	"carbon/internal/store"
)

type projectDeleteFixture struct {
	homeRoot         string
	grouped          Project
	sibling          Project
	standalone       Project
	groupedSource    string
	standaloneSource string
	cluster          Cluster
	groupedStore     *store.Store
	standaloneStore  *store.Store
	groupedRoot      string
	standaloneRoot   string
}

func newProjectDeleteFixture(t *testing.T) projectDeleteFixture {
	t.Helper()
	useHomeTestCache(t)
	root := t.TempDir()
	if _, err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	cluster, err := CreateCluster(root, CreateClusterRequest{Name: "Delete group", Prefix: "DEL"})
	if err != nil {
		t.Fatal(err)
	}
	groupedSource := t.TempDir()
	standaloneSource := t.TempDir()
	grouped, err := AddProject(root, cluster.ID, AddProjectRequest{Name: "Grouped target", SourcePath: groupedSource})
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := AddProject(root, cluster.ID, AddProjectRequest{Name: "Grouped peer", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	standalone, err := AddStandaloneProject(root, AddProjectRequest{Name: "Standalone target", SourcePath: standaloneSource})
	if err != nil {
		t.Fatal(err)
	}
	groupedRoot, err := ClusterDataRoot(root, cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	standaloneRoot, err := ProjectDataRoot(root, standalone.ID)
	if err != nil {
		t.Fatal(err)
	}
	return projectDeleteFixture{
		homeRoot: root, grouped: grouped, sibling: sibling, standalone: standalone,
		groupedSource: groupedSource, standaloneSource: standaloneSource, cluster: cluster,
		groupedStore: store.New(groupedRoot), standaloneStore: store.New(standaloneRoot),
		groupedRoot: groupedRoot, standaloneRoot: standaloneRoot,
	}
}

func (f projectDeleteFixture) createTask(t *testing.T, target *store.Store, projectID, title string, deps ...string) *store.Doc {
	t.Helper()
	doc, err := target.Create(store.Draft{
		Title: title, ProjectID: projectID, ProjectIDSet: true, Deps: deps,
	}, "human:test", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func (f projectDeleteFixture) manifest(t *testing.T) Manifest {
	t.Helper()
	h, err := Open(f.homeRoot)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := h.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func hasProject(manifest Manifest, projectID string) bool {
	project, _, _, err := findProjectInManifest(&manifest, projectID)
	return err == nil && project.ID == projectID
}

func projectDeleteRequest(project Project, deleteData bool) DeleteProjectRequest {
	return DeleteProjectRequest{
		ProjectID: project.ID, ConfirmationName: project.Name, DeleteData: deleteData, Actor: "human:test",
	}
}

func TestDeleteProjectCatalogOnlyKeepsDataAndSources(t *testing.T) {
	t.Run("cluster removes catalog entry and keeps shared pool", func(t *testing.T) {
		f := newProjectDeleteFixture(t)
		owned := f.createTask(t, f.groupedStore, f.grouped.ID, "owned")
		peer := f.createTask(t, f.groupedStore, f.sibling.ID, "peer")
		sentinel := filepath.Join(f.groupedSource, "source-must-survive.txt")
		if err := os.WriteFile(sentinel, []byte("source"), 0o600); err != nil {
			t.Fatal(err)
		}

		result, err := DeleteProject(context.Background(), f.homeRoot, projectDeleteRequest(f.grouped, false))
		if err != nil {
			t.Fatal(err)
		}
		if result.DeleteData || result.Data != nil || result.ClusterID != f.cluster.ID || result.Standalone {
			t.Fatalf("catalog-only grouped result = %#v", result)
		}
		manifest := f.manifest(t)
		if hasProject(manifest, f.grouped.ID) || !hasProject(manifest, f.sibling.ID) {
			t.Fatalf("unexpected grouped manifest after catalog delete: %#v", manifest)
		}
		if _, err := f.groupedStore.Get(owned.Task.ID); err != nil {
			t.Fatalf("catalog-only delete cleared target task: %v", err)
		}
		if _, err := f.groupedStore.Get(peer.Task.ID); err != nil {
			t.Fatalf("catalog-only delete cleared peer task: %v", err)
		}
		if _, err := os.Stat(sentinel); err != nil {
			t.Fatalf("catalog-only delete removed source: %v", err)
		}
		if _, err := os.Stat(filepath.Join(f.groupedRoot, repo.CarbonDirName, "config.yaml")); err != nil {
			t.Fatalf("catalog-only delete removed shared Carbon root: %v", err)
		}
	})

	t.Run("standalone removes catalog entry and keeps private data root", func(t *testing.T) {
		f := newProjectDeleteFixture(t)
		owned := f.createTask(t, f.standaloneStore, f.standalone.ID, "owned")
		sentinel := filepath.Join(f.standaloneSource, "source-must-survive.txt")
		if err := os.WriteFile(sentinel, []byte("source"), 0o600); err != nil {
			t.Fatal(err)
		}

		result, err := DeleteProject(context.Background(), f.homeRoot, projectDeleteRequest(f.standalone, false))
		if err != nil {
			t.Fatal(err)
		}
		if result.DeleteData || result.Data != nil || !result.Standalone {
			t.Fatalf("catalog-only standalone result = %#v", result)
		}
		if hasProject(f.manifest(t), f.standalone.ID) {
			t.Fatal("standalone project remains in manifest")
		}
		if _, err := f.standaloneStore.Get(owned.Task.ID); err != nil {
			t.Fatalf("catalog-only delete cleared standalone task: %v", err)
		}
		if _, err := os.Stat(sentinel); err != nil {
			t.Fatalf("catalog-only delete removed source: %v", err)
		}
		if _, err := os.Stat(filepath.Join(f.standaloneRoot, repo.CarbonDirName, "config.yaml")); err != nil {
			t.Fatalf("catalog-only delete removed standalone Carbon root: %v", err)
		}
	})
}

func TestDeleteProjectWithDataClearsOnlyTargetAndKeepsRoots(t *testing.T) {
	t.Run("cluster clears target without touching peer or cluster-wide task", func(t *testing.T) {
		f := newProjectDeleteFixture(t)
		owned := f.createTask(t, f.groupedStore, f.grouped.ID, "owned")
		peer := f.createTask(t, f.groupedStore, f.sibling.ID, "peer")
		clusterWide := f.createTask(t, f.groupedStore, "", "cluster-wide")
		sentinel := filepath.Join(f.groupedSource, "source-must-survive.txt")
		if err := os.WriteFile(sentinel, []byte("source"), 0o600); err != nil {
			t.Fatal(err)
		}

		result, err := DeleteProject(context.Background(), f.homeRoot, projectDeleteRequest(f.grouped, true))
		if err != nil {
			t.Fatal(err)
		}
		if !result.DeleteData || result.Data == nil || result.Data.TasksDeleted != 1 || result.ClusterID != f.cluster.ID {
			t.Fatalf("grouped delete result = %#v", result)
		}
		if _, err := f.groupedStore.Get(owned.Task.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("selected grouped task remains: %v", err)
		}
		if _, err := f.groupedStore.Get(peer.Task.ID); err != nil {
			t.Fatalf("peer task was cleared: %v", err)
		}
		if _, err := f.groupedStore.Get(clusterWide.Task.ID); err != nil {
			t.Fatalf("cluster-wide task was cleared: %v", err)
		}
		manifest := f.manifest(t)
		if hasProject(manifest, f.grouped.ID) || !hasProject(manifest, f.sibling.ID) {
			t.Fatalf("unexpected grouped manifest after data delete: %#v", manifest)
		}
		if _, err := os.Stat(sentinel); err != nil {
			t.Fatalf("data delete removed source: %v", err)
		}
		if _, err := os.Stat(filepath.Join(f.groupedRoot, repo.CarbonDirName, "config.yaml")); err != nil {
			t.Fatalf("data delete removed shared Carbon root: %v", err)
		}
	})

	t.Run("standalone clears tasks but keeps source and private config", func(t *testing.T) {
		f := newProjectDeleteFixture(t)
		owned := f.createTask(t, f.standaloneStore, f.standalone.ID, "owned")
		sentinel := filepath.Join(f.standaloneSource, "source-must-survive.txt")
		if err := os.WriteFile(sentinel, []byte("source"), 0o600); err != nil {
			t.Fatal(err)
		}

		result, err := DeleteProject(context.Background(), f.homeRoot, projectDeleteRequest(f.standalone, true))
		if err != nil {
			t.Fatal(err)
		}
		if !result.DeleteData || result.Data == nil || result.Data.TasksDeleted != 1 || !result.Standalone {
			t.Fatalf("standalone delete result = %#v", result)
		}
		if _, err := f.standaloneStore.Get(owned.Task.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("selected standalone task remains: %v", err)
		}
		if hasProject(f.manifest(t), f.standalone.ID) {
			t.Fatal("standalone project remains in manifest")
		}
		if _, err := os.Stat(sentinel); err != nil {
			t.Fatalf("data delete removed source: %v", err)
		}
		if _, err := os.Stat(filepath.Join(f.standaloneRoot, repo.CarbonDirName, "config.yaml")); err != nil {
			t.Fatalf("data delete removed standalone Carbon root: %v", err)
		}
	})
}

func TestDeleteProjectRejectsNonStableIDAndExactNameMismatchWithoutMutation(t *testing.T) {
	f := newProjectDeleteFixture(t)
	owned := f.createTask(t, f.groupedStore, f.grouped.ID, "owned")
	for _, request := range []DeleteProjectRequest{
		{ProjectID: f.grouped.Name, ConfirmationName: f.grouped.Name, DeleteData: true, Actor: "human:test"},
		{ProjectID: " " + f.grouped.ID, ConfirmationName: f.grouped.Name, DeleteData: true, Actor: "human:test"},
		{ProjectID: f.grouped.ID, ConfirmationName: f.grouped.Name + " ", DeleteData: true, Actor: "human:test"},
	} {
		_, err := DeleteProject(context.Background(), f.homeRoot, request)
		if request.ProjectID == f.grouped.ID {
			if !errors.Is(err, ErrProjectDeleteNameConfirmation) {
				t.Fatalf("name mismatch = %v, want confirmation error", err)
			}
		} else if !errors.Is(err, ErrProjectNotFound) {
			t.Fatalf("non-stable id %q = %v, want not found", request.ProjectID, err)
		}
		if _, err := f.groupedStore.Get(owned.Task.ID); err != nil {
			t.Fatalf("rejected delete mutated task: %v", err)
		}
		if !hasProject(f.manifest(t), f.grouped.ID) {
			t.Fatal("rejected delete removed manifest entry")
		}
	}
}

func TestDeleteProjectRejectsSurvivingPeerReference(t *testing.T) {
	f := newProjectDeleteFixture(t)
	owned := f.createTask(t, f.groupedStore, f.grouped.ID, "owned")
	peer := f.createTask(t, f.groupedStore, f.sibling.ID, "peer depends on target", owned.Task.ID)
	_, err := DeleteProject(context.Background(), f.homeRoot, projectDeleteRequest(f.grouped, true))
	if !errors.Is(err, store.ErrProjectTaskDataReferenced) {
		t.Fatalf("delete with peer reference = %v, want %v", err, store.ErrProjectTaskDataReferenced)
	}
	if _, err := f.groupedStore.Get(owned.Task.ID); err != nil {
		t.Fatalf("reference rejection removed target task: %v", err)
	}
	if _, err := f.groupedStore.Get(peer.Task.ID); err != nil {
		t.Fatalf("reference rejection removed peer task: %v", err)
	}
	if !hasProject(f.manifest(t), f.grouped.ID) {
		t.Fatal("reference rejection removed manifest entry")
	}
}

func TestDeleteProjectManifestFailureLeavesRecoverableReceipt(t *testing.T) {
	f := newProjectDeleteFixture(t)
	owned := f.createTask(t, f.standaloneStore, f.standalone.ID, "owned")
	_, err := deleteProjectWithManifestWriter(context.Background(), f.homeRoot, projectDeleteRequest(f.standalone, true), func(string, Manifest) error {
		return errors.New("injected manifest publication failure")
	})
	if !errors.Is(err, ErrProjectDeleteRecovery) {
		t.Fatalf("manifest publication failure = %v, want recovery error", err)
	}
	if !hasProject(f.manifest(t), f.standalone.ID) {
		t.Fatal("failed publication removed manifest entry")
	}
	if _, err := f.standaloneStore.Get(owned.Task.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failure did not use compound data clear: %v", err)
	}
	h, err := Open(f.homeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if receipt, exists, err := readProjectDeleteReceipt(h.CarbonRoot, f.standalone.ID); err != nil || !exists || receipt.State != "task-data-cleared" {
		t.Fatalf("recovery receipt = %#v exists=%v err=%v", receipt, exists, err)
	}

	if _, err := DeleteProject(context.Background(), f.homeRoot, projectDeleteRequest(f.standalone, true)); err != nil {
		t.Fatalf("recovery retry = %v", err)
	}
	if hasProject(f.manifest(t), f.standalone.ID) {
		t.Fatal("recovery retry did not remove manifest entry")
	}
	if _, exists, err := readProjectDeleteReceipt(h.CarbonRoot, f.standalone.ID); err != nil || exists {
		t.Fatalf("recovery receipt remains after successful publication: exists=%v err=%v", exists, err)
	}
}

func TestDeleteProjectHoldsStoreWriteLockThroughManifestPublication(t *testing.T) {
	f := newProjectDeleteFixture(t)
	owned := f.createTask(t, f.groupedStore, f.grouped.ID, "owned")
	publishEntered := make(chan struct{})
	releasePublish := make(chan struct{})
	deleteDone := make(chan error, 1)
	released := false
	defer func() {
		if !released {
			close(releasePublish)
			<-deleteDone
		}
	}()

	go func() {
		_, err := deleteProjectWithManifestWriter(context.Background(), f.homeRoot, projectDeleteRequest(f.grouped, true), func(carbonRoot string, candidate Manifest) error {
			// This callback is the exact final manifest publication boundary. It must
			// still run under the selected Store's write lock.
			close(publishEntered)
			<-releasePublish
			return writeManifest(carbonRoot, candidate)
		})
		deleteDone <- err
	}()

	select {
	case <-publishEntered:
	case <-time.After(time.Second):
		t.Fatal("delete did not reach blocked manifest publication")
	}
	if _, err := f.groupedStore.Get(owned.Task.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("selected task was not cleared before publication: %v", err)
	}
	if !hasProject(f.manifest(t), f.grouped.ID) {
		t.Fatal("manifest changed before its blocked publication was released")
	}

	// Use a distinct Store instance to exercise the OS-level lock rather than the
	// first instance's local mutex. With the old clear-then-unlock implementation
	// this create completed here and became an orphan when publication resumed.
	writerStarted := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		close(writerStarted)
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		_, err := store.New(f.groupedRoot).CreateExplicit(ctx, "human:writer", store.ExplicitDraft{
			Title: "must not land between clear and catalog removal", ProjectID: f.grouped.ID,
			Type: "foundation", Importance: "normal",
		})
		writerDone <- err
	}()
	<-writerStarted
	select {
	case err := <-writerDone:
		if !errors.Is(err, store.ErrLockTimeout) {
			t.Fatalf("concurrent target create escaped blocked manifest publication: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent target create did not observe the Store write lock")
	}

	close(releasePublish)
	released = true
	if err := <-deleteDone; err != nil {
		t.Fatalf("delete after releasing manifest publication = %v", err)
	}
	if hasProject(f.manifest(t), f.grouped.ID) {
		t.Fatal("manifest publication did not remove target project")
	}
	if _, err := f.groupedStore.Get(owned.Task.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("target task reappeared after deletion: %v", err)
	}
}
