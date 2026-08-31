package projectpolicy

import (
	"context"
	"errors"
	"testing"

	"carbon/internal/repo"
	"carbon/internal/store"
)

func TestPolicyDefaultsRoundTripsAndCannotCrossProjectFiles(t *testing.T) {
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "POL"); err != nil {
		t.Fatal(err)
	}
	manager := New(store.New(root))
	missing, err := manager.Get("project_a")
	if err != nil || missing.IdentityMode || missing.NoTraceMode || missing.ProjectID != "project_a" {
		t.Fatalf("missing policy = %#v err=%v", missing, err)
	}
	if _, err := manager.Save(context.Background(), "human:lead", Policy{Version: version, ProjectID: "project_a", IdentityMode: true, NoTraceMode: true}); err != nil {
		t.Fatal(err)
	}
	got, err := manager.Get("project_a")
	if err != nil || !got.IdentityMode || !got.NoTraceMode {
		t.Fatalf("saved policy = %#v err=%v", got, err)
	}
	other, err := manager.Get("project_b")
	if err != nil || other.IdentityMode || other.NoTraceMode {
		t.Fatalf("other project inherited policy = %#v err=%v", other, err)
	}
	if _, err := manager.Get(""); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("missing project scope = %v, want invalid policy", err)
	}
}
