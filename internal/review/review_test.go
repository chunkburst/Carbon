package review

import (
	"context"
	"errors"
	"testing"
	"time"

	"carbon/internal/repo"
	"carbon/internal/store"
)

func testReviewManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "REV"); err != nil {
		t.Fatal(err)
	}
	data := store.New(root)
	return New(data, func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) }), data
}

func TestExplicitReviewTargetPersistsAndDecisionIsIdempotent(t *testing.T) {
	manager, data := testReviewManager(t)
	doc, err := data.Create(store.Draft{Title: "review target", ProjectID: "project_one", ProjectIDSet: true}, "human:lead", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.Create(context.Background(), "human:lead", CreateInput{ProjectID: "project_one", TargetKind: TargetPlan, TargetID: doc.Task.ID, TaskID: doc.Task.ID, ReviewerActor: "agent:reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != StatusPending || created.ReviewerActor != "agent:reviewer" {
		t.Fatalf("created = %#v", created)
	}
	decided, err := manager.Decide(context.Background(), "agent:reviewer", "project_one", created.ID, DecideInput{Status: StatusApproved, Decision: "计划范围清楚，可以继续。"})
	if err != nil {
		t.Fatal(err)
	}
	if decided.Status != StatusApproved || decided.ResolvedBy != "agent:reviewer" {
		t.Fatalf("decided = %#v", decided)
	}
	retry, err := manager.Decide(context.Background(), "agent:reviewer", "project_one", created.ID, DecideInput{Status: StatusApproved, Decision: "计划范围清楚，可以继续。"})
	if err != nil || retry.ResolvedAt != decided.ResolvedAt {
		t.Fatalf("idempotent decision = %#v err=%v", retry, err)
	}
	if _, err := manager.Decide(context.Background(), "agent:reviewer", "project_one", created.ID, DecideInput{Status: StatusRejected, Decision: "changed"}); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("changed decision = %v, want ErrAlreadyDecided", err)
	}
	if _, err := manager.Get("project_two", created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross project get = %v, want not found", err)
	}
}

func TestReviewTargetMetadataMustMatchPlanOrManualCheckShape(t *testing.T) {
	manager, _ := testReviewManager(t)
	for name, input := range map[string]CreateInput{
		"plan without task":  {ProjectID: "project_one", TargetKind: TargetPlan, TargetID: "plan:free", ReviewerActor: "human:lead"},
		"plan mismatched id": {ProjectID: "project_one", TargetKind: TargetPlan, TargetID: "TASK-1", TaskID: "TASK-2", ReviewerActor: "human:lead"},
		"manual bad index":   {ProjectID: "project_one", TargetKind: TargetManualCheck, TargetID: "TASK-1#check:01", TaskID: "TASK-1", CheckID: "01", ReviewerActor: "human:lead"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := manager.Create(context.Background(), "human:lead", input); !errors.Is(err, ErrInvalidReview) {
				t.Fatalf("Create = %v, want invalid review", err)
			}
		})
	}
}
