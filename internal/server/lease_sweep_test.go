package server

import (
	"context"
	"slices"
	"testing"
	"time"

	"carbon/internal/home"
	"carbon/internal/mcp"
	"carbon/internal/repo"
	"carbon/internal/store"
	"carbon/internal/task"
)

func TestLeaseSweepReleasesExpiredOwnership(t *testing.T) {
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "SWP"); err != nil {
		t.Fatal(err)
	}
	st := store.New(root)
	svc := mcp.NewService(st, "human:test", nil)
	doc, err := svc.Create(store.Draft{Title: "expired lease"})
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.SetAssignee("agent:stale"); err != nil {
		t.Fatal(err)
	}
	if err := doc.SetLease(&task.Lease{
		ID: "lease_expired", Holder: "agent:stale", AcquiredAt: time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339), ExpiresAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(doc); err != nil {
		t.Fatal(err)
	}

	s := New(root, "human:test")
	s.sweepLeases(context.Background())
	reloaded, err := st.Get(doc.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Task.Lease != nil || reloaded.Task.Assignee != "" {
		t.Fatalf("expired lease survived sweep: %+v", reloaded.Task)
	}
}

func TestLeaseSweepCoversStandaloneProjectRoot(t *testing.T) {
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	project, err := home.AddStandaloneProject(homeRoot, home.AddProjectRequest{Name: "Private", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	root, err := home.ProjectDataRoot(homeRoot, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(root)
	doc, err := st.Create(store.Draft{Title: "expired standalone lease", ProjectID: project.ID, ProjectIDSet: true}, "human:test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.SetAssignee("agent:stale"); err != nil {
		t.Fatal(err)
	}
	if err := doc.SetLease(&task.Lease{
		ID: "lease_standalone_expired", Holder: "agent:stale", AcquiredAt: time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339), ExpiresAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(doc); err != nil {
		t.Fatal(err)
	}

	s := NewWithScope("human:test", ScopeDefaults{Home: homeRoot, ProjectID: project.ID, HomeByDefault: true})
	if roots := s.leaseSweepRoots(); !slices.Contains(roots, root) || len(roots) != 1 {
		t.Fatalf("standalone lease roots = %v, want only %s", roots, root)
	}
	s.sweepLeases(context.Background())
	reloaded, err := st.Get(doc.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Task.Lease != nil || reloaded.Task.Assignee != "" {
		t.Fatalf("standalone expired lease survived sweep: %+v", reloaded.Task)
	}
}
