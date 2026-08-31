package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"carbon/internal/lease"
	"carbon/internal/projectpolicy"
	"carbon/internal/repo"
	"carbon/internal/store"
)

func newIdentityGuardServices(t *testing.T) (*store.Store, *Service, *Service) {
	t.Helper()
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "IDG"); err != nil {
		t.Fatal(err)
	}
	data := store.New(root)
	if _, err := projectpolicy.New(data).Save(context.Background(), "human:lead", projectpolicy.Policy{Version: 1, ProjectID: "project", IdentityMode: true}); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	scope := Scope{Home: "home", ClusterID: "cluster", ProjectID: "project", SourcePath: source}
	return data,
		NewScopedService(data, "human:lead", scope, nil),
		NewScopedService(data, "agent:worker", scope, nil)
}

func setWorkerIdentityPolicy(t *testing.T, data *store.Store, enabled bool) {
	t.Helper()
	if _, err := projectpolicy.New(data).Save(context.Background(), "human:lead", projectpolicy.Policy{Version: 1, ProjectID: "project", IdentityMode: enabled}); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityModeGuardsClaimBeginReassignAndApproval(t *testing.T) {
	data, human, agent := newIdentityGuardServices(t)
	ctx := context.Background()
	// The optional guard governs Worker agents only. Human/system control-plane
	// actors stay compatible and can still claim typed work without a registry record.
	humanTyped, err := human.CreateContext(ctx, store.Draft{Title: "human typed", Type: "patch", Importance: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := human.ClaimLease(ctx, LeaseClaimInput{TaskID: humanTyped.Task.ID, Reason: "manual coordination", ExpectedVersion: humanTyped.ETag()}); err != nil || result.Doc == nil {
		t.Fatalf("human typed lease exemption = %#v err=%v", result, err)
	}
	system := NewScopedService(data, "system:scheduler", human.Scope(), nil)
	systemTyped, err := human.CreateContext(ctx, store.Draft{Title: "system typed", Type: "patch", Importance: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := system.ClaimLease(ctx, LeaseClaimInput{TaskID: systemTyped.Task.ID, Reason: "system coordination", ExpectedVersion: systemTyped.ETag()}); err != nil || result.Doc == nil {
		t.Fatalf("system typed lease exemption = %#v err=%v", result, err)
	}
	typed, err := human.CreateContext(ctx, store.Draft{Title: "typed", Type: "patch", Importance: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.ClaimLease(ctx, LeaseClaimInput{TaskID: typed.Task.ID, Reason: "开始处理", ExpectedVersion: typed.ETag()}); !errors.Is(err, ErrIdentityRequired) {
		t.Fatalf("unclaimed typed lease = %v, want ErrIdentityRequired", err)
	}
	if _, err := agent.ClaimWorkerIdentity(ctx, WorkerIdentityClaimInput{Role: "后端", Types: []string{"patch"}}); err != nil {
		t.Fatal(err)
	}
	claimed, err := agent.ClaimLease(ctx, LeaseClaimInput{TaskID: typed.Task.ID, Reason: "开始处理", ExpectedVersion: typed.ETag()})
	if err != nil || claimed.Doc == nil || claimed.Doc.Task.Lease == nil || claimed.Doc.Task.Lease.Holder != "agent:worker" {
		t.Fatalf("claimed typed lease = %#v err=%v", claimed, err)
	}

	otherType, err := human.CreateContext(ctx, store.Draft{Title: "foundation", Type: "foundation", Importance: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.BeginSession(ctx, BeginSessionInput{TaskID: otherType.Task.ID, ExpectedActor: "agent:worker", IdempotencyKey: "wrong-type"}); !errors.Is(err, ErrIdentityTaskType) {
		t.Fatalf("begin wrong identity type = %v, want ErrIdentityTaskType", err)
	}
	if _, err := human.ReassignLease(ctx, otherType.Task.ID, "agent:unclaimed", "assign", otherType.ETag(), false); !errors.Is(err, ErrIdentityRequired) {
		t.Fatalf("reassign unclaimed agent = %v, want ErrIdentityRequired", err)
	}

	// Build a pending request through the low-level lease package to verify that the
	// approval path itself, not just claim, validates the eventual target Worker.
	pendingTask, err := human.CreateContext(ctx, store.Draft{Title: "approval", Type: "patch", Importance: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	manager := lease.New(data, nil, 0)
	held, err := manager.Claim(ctx, lease.ClaimInput{TaskID: pendingTask.Task.ID, Actor: "human:holder", ExpectedVersion: pendingTask.ETag()})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := manager.Claim(ctx, lease.ClaimInput{TaskID: pendingTask.Task.ID, Actor: "agent:unclaimed", ExpectedVersion: held.Doc.ETag()})
	if !errors.Is(err, lease.ErrApprovalPending) || pending.Request == nil || pending.Doc == nil {
		t.Fatalf("prepare pending identity approval = %#v err=%v", pending, err)
	}
	if _, err := human.ApproveLeaseClaim(ctx, pendingTask.Task.ID, pending.Request.RequestID, "批准", pending.Doc.ETag(), true); !errors.Is(err, ErrIdentityRequired) {
		t.Fatalf("approve unclaimed agent = %v, want ErrIdentityRequired", err)
	}
}

func TestIdentityModeDisabledAndUntypedTasksRemainCompatible(t *testing.T) {
	data, human, _ := newIdentityGuardServices(t)
	ctx := context.Background()
	setWorkerIdentityPolicy(t, data, false)
	unclaimed := NewScopedService(data, "agent:no-record", human.Scope(), nil)
	typed, err := human.CreateContext(ctx, store.Draft{Title: "compat typed", Type: "patch", Importance: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := unclaimed.ClaimLease(ctx, LeaseClaimInput{TaskID: typed.Task.ID, Reason: "legacy compatible", ExpectedVersion: typed.ETag()}); err != nil || result.Doc == nil {
		t.Fatalf("disabled identity mode changed lease behavior: %#v err=%v", result, err)
	}

	// A historical task with no type remains claimable even after re-enabling mode.
	setWorkerIdentityPolicy(t, data, true)
	legacy, err := data.Create(store.Draft{Title: "old untyped", ProjectID: "project", ProjectIDSet: true}, "human:lead", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	// Store.Create supplies today's default type. Simulate a durable pre-type task
	// through the regular lossless store writer; blank Type remains valid legacy data.
	legacy.SetType("")
	if err := data.Save(legacy); err != nil {
		t.Fatal(err)
	}
	if result, err := unclaimed.ClaimLease(ctx, LeaseClaimInput{TaskID: legacy.Task.ID, Reason: "old data", ExpectedVersion: legacy.ETag()}); err != nil || result.Doc == nil {
		t.Fatalf("untyped compatibility claim = %#v err=%v", result, err)
	}
}
