package templates

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"carbon/internal/config"
	"carbon/internal/store"
)

func templateStore(t *testing.T) *store.Store {
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

func TestTemplateInstantiationUsesExplicitWorkflowFields(t *testing.T) {
	m := New(templateStore(t), func() time.Time { return time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC) })
	created, err := m.Create(context.Background(), "human:li", Template{
		Name: "Plugin feature", Title: "Ship plugin", ProjectID: "project-a", Type: "plugin", Importance: "important", Priority: "high", Labels: []string{"release"},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := m.Instantiate(context.Background(), InstantiateInput{TemplateID: created.ID, Actor: "agent:a", ExpectedVersion: created.ETag()})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Task.ProjectID != "project-a" || doc.Task.Type != "plugin" || doc.Task.Importance != "important" || doc.Task.Priority != "high" {
		t.Fatalf("instantiated task = %+v", doc.Task)
	}
}

func TestTemplateInstantiationRejectsChangedVersionBeforeCreate(t *testing.T) {
	m := New(templateStore(t), func() time.Time { return time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC) })
	created, err := m.Create(context.Background(), "human:li", Template{
		Name: "Versioned", Title: "Original", Type: "foundation", Importance: "normal",
	})
	if err != nil {
		t.Fatal(err)
	}
	stale := created.ETag()
	created.Title = "Updated"
	updated, err := m.Save(context.Background(), "human:li", created, stale)
	if err != nil {
		t.Fatal(err)
	}

	_, err = m.Instantiate(context.Background(), InstantiateInput{TemplateID: updated.ID, Actor: "agent:a", ExpectedVersion: stale})
	if !errors.Is(err, store.ErrVersionMismatch) {
		t.Fatalf("stale instantiate = %v, want version mismatch", err)
	}
	docs, err := m.Store.ListDocs()
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 0 {
		t.Fatalf("stale instantiate created tasks: %+v", docs)
	}

	doc, err := m.Instantiate(context.Background(), InstantiateInput{TemplateID: updated.ID, Actor: "agent:a", ExpectedVersion: updated.ETag()})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Task.Title != "Updated" {
		t.Fatalf("current instantiate used wrong template: %+v", doc.Task)
	}
}
