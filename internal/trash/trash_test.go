package trash

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"carbon/internal/config"
	"carbon/internal/store"
	"carbon/internal/task"
)

func trashStore(t *testing.T) *store.Store {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".carbon", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default("PROJ")
	cfg.TrashRetentionDays = 1
	if err := config.Save(filepath.Join(root, ".carbon", "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	return store.New(root)
}

func TestBatchTrashGraphSafetyRestoreAndNewEntryGC(t *testing.T) {
	s := trashStore(t)
	parent, err := s.Create(store.Draft{Title: "parent", ProjectID: "p1", ProjectIDSet: true, Labels: []string{"ops"}}, "human:a", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.Create(store.Draft{Title: "child", Parent: parent.Task.ID, ProjectID: "p1", ProjectIDSet: true, Labels: []string{"ops"}}, "human:a", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	m := New(s, func() time.Time { return now })
	if _, err := m.Trash(context.Background(), Input{ID: parent.Task.ID, Actor: "human:a", Reason: "archive"}); !errors.Is(err, task.ErrHasChildren) {
		t.Fatalf("single parent trash = %v", err)
	}
	entries, err := m.TrashMany(context.Background(), Input{IDs: []string{parent.Task.ID, child.Task.ID}, Actor: "human:a", Reason: "archive family"})
	if err != nil || len(entries) != 2 {
		t.Fatalf("batch trash = %+v err=%v", entries, err)
	}
	if _, err := s.Get(parent.Task.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("active parent after trash = %v", err)
	}
	parentEntry := entries[0]
	for _, entry := range entries {
		if entry.ID == parent.Task.ID {
			parentEntry = entry
			break
		}
	}
	target := "p2"
	restored, err := m.Restore(context.Background(), RestoreInput{ID: parent.Task.ID, Actor: "human:b", TargetProjectID: &target, ExpectedVersion: parentEntry.ETag})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Task.ProjectID != "p2" || restored.Task.Trash != nil {
		t.Fatalf("restore = %+v", restored.Task)
	}

	// Listing does not run GC. After retention passes, only another new trash entry causes
	// the old child entry to be collected.
	now = now.Add(48 * time.Hour)
	if listed, err := m.List(); err != nil || len(listed) != 1 {
		t.Fatalf("list unexpectedly GCd: %+v %v", listed, err)
	}
	other, err := s.Create(store.Draft{Title: "other"}, "human:a", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Trash(context.Background(), Input{ID: other.Task.ID, Actor: "human:a", Reason: "trigger gc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTrash(child.Task.ID); !errors.Is(err, store.ErrTrashNotFound) {
		t.Fatalf("expired child not GCd: %v", err)
	}
}

func TestEmptyProjectDoesNotPurgeOtherProjectsOrClusterWide(t *testing.T) {
	s := trashStore(t)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	m := New(s, func() time.Time { return now })
	p1, err := s.Create(store.Draft{Title: "p1", ProjectID: "project-1", ProjectIDSet: true}, "human:a", now)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.Create(store.Draft{Title: "p2", ProjectID: "project-2", ProjectIDSet: true}, "human:a", now)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := s.Create(store.Draft{Title: "shared", ProjectIDSet: true}, "human:a", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{p1.Task.ID, p2.Task.ID, shared.Task.ID} {
		if _, err := m.Trash(context.Background(), Input{ID: id, Actor: "human:a", Reason: "cleanup"}); err != nil {
			t.Fatal(err)
		}
	}

	purged, err := m.EmptyProject(context.Background(), "human:project-1", "project-1", false)
	if err != nil || purged != 1 {
		t.Fatalf("project-only empty = %d, %v", purged, err)
	}
	if _, err := s.GetTrash(p1.Task.ID); !errors.Is(err, store.ErrTrashNotFound) {
		t.Fatalf("project-1 trash survived: %v", err)
	}
	for _, id := range []string{p2.Task.ID, shared.Task.ID} {
		if _, err := s.GetTrash(id); err != nil {
			t.Fatalf("project-only empty deleted %s: %v", id, err)
		}
	}

	// The destructive transaction records the actor and precise scope in the store
	// operation diagnostic, rather than silently widening to the physical pool.
	lock, err := os.ReadFile(filepath.Join(s.Root(), ".carbon", "write.lock"))
	if err != nil || !strings.Contains(string(lock), "human:project-1") || !strings.Contains(string(lock), "empty project trash project_id=project-1 include_cluster_wide=false") {
		t.Fatalf("project empty audit diagnostic = %q, err=%v", lock, err)
	}

	purged, err = m.EmptyProject(context.Background(), "human:project-1", "project-1", true)
	if err != nil || purged != 1 {
		t.Fatalf("project empty including shared = %d, %v", purged, err)
	}
	if _, err := s.GetTrash(shared.Task.ID); !errors.Is(err, store.ErrTrashNotFound) {
		t.Fatalf("explicit shared empty did not purge shared entry: %v", err)
	}
	if _, err := s.GetTrash(p2.Task.ID); err != nil {
		t.Fatalf("including shared deleted other project: %v", err)
	}
	if _, err := m.EmptyProject(context.Background(), "human:project-1", "", false); !errors.Is(err, store.ErrProjectIDRequired) {
		t.Fatalf("empty project scope = %v, want project id required", err)
	}

	purged, err = m.Empty(context.Background(), "human:cluster-admin")
	if err != nil || purged != 1 {
		t.Fatalf("cluster empty = %d, %v", purged, err)
	}
	if _, err := s.GetTrash(p2.Task.ID); !errors.Is(err, store.ErrTrashNotFound) {
		t.Fatalf("cluster empty did not purge remaining entry: %v", err)
	}
}
