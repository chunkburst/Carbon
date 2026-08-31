package incident

import (
	"context"
	"testing"
	"time"

	"carbon/internal/identity"
	"carbon/internal/repo"
	"carbon/internal/store"
)

func testManager(t *testing.T) (*Manager, *store.Store, func(time.Time)) {
	t.Helper()
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "INC"); err != nil {
		t.Fatal(err)
	}
	var now = time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
	return NewScoped(store.New(root), func() time.Time { return now }, true), store.New(root), func(next time.Time) { now = next }
}

func TestManualAndAutomaticIncidentsKeepIndependentRepliesAcrossRestart(t *testing.T) {
	manager, data, advance := testManager(t)
	ctx := context.Background()
	manual, err := manager.Create(ctx, "agent:one", CreateInput{ProjectID: "project_one", Kind: KindInvestigation, Title: "429 still unexplained", Body: "lower concurrency did not help", Severity: SeverityHigh})
	if err != nil {
		t.Fatal(err)
	}
	if manual.Origin != OriginManual || manual.Kind != KindInvestigation {
		t.Fatalf("manual incident = %#v", manual)
	}
	if _, err := manager.Reply(ctx, "agent:one", "project_one", manual.ID, "先保留现场，继续观察。"); err != nil {
		t.Fatal(err)
	}
	advance(time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC))
	identityManager, err := identity.NewProject(data, func() time.Time { return time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC) }, "project_one", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identityManager.ClaimOrChangeWithOptions(ctx, "human:lead", identity.ClaimInput{Actor: "agent:reviewer", Roles: []string{"reviewer"}, Types: []string{"patch"}}, identity.ChangeOptions{ProjectID: "project_one"}); err != nil {
		t.Fatal(err)
	}
	autos, err := identityManager.ListAutoIncidents("project_one")
	if err != nil || len(autos) != 1 || autos[0].Kind != "identity_change" {
		t.Fatalf("auto incidents = %#v err=%v", autos, err)
	}
	if _, err := manager.Reply(ctx, "agent:reviewer", "project_one", autos[0].ID, "身份已确认，可以接收审核目标。"); err != nil {
		t.Fatal(err)
	}

	// Reconstruct managers as a restart simulation. IDs and reply links are read from
	// managed data rather than held in memory.
	restarted := NewScoped(data, func() time.Time { return time.Date(2026, 8, 30, 11, 0, 0, 0, time.UTC) }, true)
	gotManual, err := restarted.Get("project_one", manual.ID)
	if err != nil || len(gotManual.Replies) != 1 || gotManual.Replies[0].Author != "agent:one" {
		t.Fatalf("persisted manual incident = %#v err=%v", gotManual, err)
	}
	gotAuto, err := restarted.Get("project_one", autos[0].ID)
	if err != nil || gotAuto.ID != autos[0].ID || gotAuto.RelatedAuditID == "" || len(gotAuto.Replies) != 1 {
		t.Fatalf("persisted automatic incident = %#v err=%v", gotAuto, err)
	}
	if _, err := restarted.Get("project_two", manual.ID); err == nil {
		t.Fatal("cross-project incident read succeeded")
	}
	updated, err := restarted.UpdateLifecycle(ctx, "agent:reviewer", "project_one", autos[0].ID, UpdateInput{Status: StatusInvestigating})
	if err != nil || updated.Status != StatusInvestigating {
		t.Fatalf("automatic investigating lifecycle = %#v err=%v", updated, err)
	}
}

func TestIncidentValidationRejectsDuplicateRelatedTaskIDs(t *testing.T) {
	manager, _, _ := testManager(t)
	_, err := manager.Create(context.Background(), "agent:one", CreateInput{ProjectID: "project_one", Kind: KindSudden, RelatedTaskIDs: []string{"TASK-1", "TASK-1"}, Title: "duplicate", Severity: SeverityNormal})
	if err == nil {
		t.Fatal("duplicate related tasks accepted")
	}
}
