package lease

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

func leaseStore(t *testing.T) *store.Store {
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

func TestConflictApprovalExpiryAndKeepAssignee(t *testing.T) {
	s := leaseStore(t)
	created, err := s.Create(store.Draft{Title: "leased"}, "human:owner", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	manager := New(s, func() time.Time { return now }, time.Minute)
	first, err := manager.Claim(context.Background(), ClaimInput{TaskID: created.Task.ID, Actor: "agent:a", TTL: time.Minute, ExpectedVersion: created.ETag()})
	if err != nil {
		t.Fatal(err)
	}
	if first.Lease == nil || first.Lease.Holder != "agent:a" {
		t.Fatalf("claim = %+v", first)
	}
	// Same-name reassignment must be a no-op; it cannot clear an active lease.
	noOp, err := manager.Reassign(context.Background(), ReassignInput{TaskID: created.Task.ID, Actor: "human:lead", Assignee: "agent:a", ExpectedVersion: first.Doc.ETag()})
	if err != nil {
		t.Fatal(err)
	}
	if noOp.Task.Lease == nil || noOp.Task.Lease.ID != first.Lease.ID || noOp.ETag() != first.Doc.ETag() {
		t.Fatalf("same-owner reassign mutated lease: %+v", noOp.Task)
	}
	conflict, err := manager.Claim(context.Background(), ClaimInput{TaskID: created.Task.ID, Actor: "agent:b", TTL: 2 * time.Minute})
	if !errors.Is(err, ErrApprovalPending) || !conflict.Pending || conflict.Request == nil || conflict.Request.RequestID == "" {
		t.Fatalf("conflict = %+v err=%v", conflict, err)
	}
	retry, err := manager.Claim(context.Background(), ClaimInput{TaskID: created.Task.ID, Actor: "agent:b", TTL: 2 * time.Minute})
	if !errors.Is(err, ErrApprovalPending) || retry.Request == nil || retry.Request.RequestID != conflict.Request.RequestID || len(retry.Doc.Task.PendingClaims) != 1 {
		t.Fatalf("same actor retry created another request: %+v err=%v", retry, err)
	}
	approved, err := manager.Approve(context.Background(), ApproveInput{TaskID: created.Task.ID, Approver: "human:lead", RequestID: conflict.Request.RequestID, Approve: true, Reason: "handoff"})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Task.Lease == nil || approved.Task.Lease.Holder != "agent:b" || len(approved.Task.PendingClaims) != 0 {
		t.Fatalf("approval = %+v", approved.Task)
	}
	released, err := manager.Release(context.Background(), ReleaseInput{TaskID: created.Task.ID, Actor: "agent:b", LeaseID: approved.Task.Lease.ID, KeepAssignee: true, Reason: "ready for review", ExpectedVersion: approved.ETag()})
	if err != nil {
		t.Fatal(err)
	}
	if released.Task.Lease != nil || released.Task.Assignee != "agent:b" {
		t.Fatalf("keep-assignee release = %+v", released.Task)
	}

	// A stale token must fail before it can override the visible owner.
	_, err = manager.Reassign(context.Background(), ReassignInput{TaskID: created.Task.ID, Actor: "human:lead", Assignee: "agent:c", Force: true, Reason: "override", ExpectedVersion: approved.ETag()})
	if !errors.Is(err, store.ErrVersionMismatch) {
		t.Fatalf("stale reassign = %v", err)
	}
}

func TestRenewThenExpireReleasesLease(t *testing.T) {
	s := leaseStore(t)
	created, err := s.Create(store.Draft{Title: "renewed lease"}, "human:owner", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	manager := New(s, func() time.Time { return now }, time.Minute)
	claimed, err := manager.Claim(context.Background(), ClaimInput{TaskID: created.Task.ID, Actor: "agent:a", TTL: time.Minute, ExpectedVersion: created.ETag()})
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(30 * time.Second)
	renewed, err := manager.Renew(context.Background(), RenewInput{
		TaskID: created.Task.ID, Actor: "agent:a", LeaseID: claimed.Lease.ID, TTL: 2 * time.Minute, ExpectedVersion: claimed.Doc.ETag(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Task.Lease == nil || renewed.Task.Lease.ID != claimed.Lease.ID || renewed.Task.Lease.RenewedAt != now.Format(time.RFC3339) {
		t.Fatalf("renewed lease = %+v", renewed.Task.Lease)
	}
	if got, want := renewed.Task.Lease.ExpiresAt, now.Add(2*time.Minute).Format(time.RFC3339); got != want {
		t.Fatalf("renewed expiry = %q, want %q", got, want)
	}

	now = now.Add(2 * time.Minute)
	expired, err := manager.Expire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].TaskID != created.Task.ID || expired[0].Lease.ID != claimed.Lease.ID {
		t.Fatalf("expired = %+v", expired)
	}
	reloaded, err := s.Get(created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Task.Lease != nil || reloaded.Task.Assignee != "" {
		t.Fatalf("expired task still owned = %+v", reloaded.Task)
	}
}
