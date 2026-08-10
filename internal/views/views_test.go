package views

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"carbon/internal/config"
	"carbon/internal/search"
	"carbon/internal/store"
)

func viewStore(t *testing.T) *store.Store {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".carbon", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(root, ".carbon", "config.yaml"), config.Default("PROJ")); err != nil {
		t.Fatal(err)
	}
	return store.New(root)
}

func TestPersistViewWithOptimisticVersion(t *testing.T) {
	m := New(viewStore(t), func() time.Time { return time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC) })
	created, err := m.Create(context.Background(), "human:li", View{Name: "Core backend", Query: search.Query{Importance: "core", Labels: []string{"backend"}}})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.ETag() == "" {
		t.Fatalf("created = %+v", created)
	}
	old := created.ETag()
	created.Name = "Core backend work"
	saved, err := m.Save(context.Background(), "human:li", created, old)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ETag() == old {
		t.Fatal("view etag did not advance")
	}
	if _, err := m.Save(context.Background(), "human:li", saved, old); !errors.Is(err, store.ErrVersionMismatch) {
		t.Fatalf("stale save = %v", err)
	}
	listed, err := m.List()
	if err != nil || len(listed) != 1 || listed[0].Name != saved.Name {
		t.Fatalf("list = %+v err=%v", listed, err)
	}
}
