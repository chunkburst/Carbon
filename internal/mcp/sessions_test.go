package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"carbon/internal/repo"
	"carbon/internal/session"
	"carbon/internal/store"
	"carbon/internal/task"
)

// beginOn starts a session for taskID as agent:codex and returns its id.
func beginOn(t *testing.T, svc *Service, taskID, key string) string {
	t.Helper()
	ses, err := svc.BeginSession(context.Background(), BeginSessionInput{
		TaskID: taskID, ExpectedActor: "agent:codex", Client: "codex", IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("BeginSession: %v", err)
	}
	return ses.ID
}

// TestFinishRefusesUnrunChecks is the regression guard for the PROJ-045 handoff miss: a
// session cannot finish into review while a command check is still pending. The agent must
// run the checks (recording pass) first; only then does finish move the task to review.
func TestFinishRefusesUnrunChecks(t *testing.T) {
	svc := NewServiceWithClient(service(t, "agent:codex").store, "agent:codex", "codex", nil)
	taskDoc, _ := svc.Create(store.Draft{Title: "x", Checks: []task.Check{{Desc: "build", Cmd: "exit 0"}}})
	id := taskDoc.Task.ID
	sid := beginOn(t, svc, id, "begin-1")

	// Finish without running checks: the cmd check is still pending, so the gate refuses.
	if _, err := svc.FinishSession(context.Background(), FinishSessionInput{SessionID: sid, Summary: "done"}); !errors.Is(err, task.ErrChecksNotPassed) {
		t.Fatalf("FinishSession with pending checks = %v, want ErrChecksNotPassed", err)
	}
	reloaded, _ := svc.Get(id)
	if reloaded.Task.Status != "in_progress" {
		t.Fatalf("status after refused finish = %q, want in_progress (session not handed off)", reloaded.Task.Status)
	}
	if sess, _ := svc.store.GetSession(sid); sess.Session.Status != session.StatusActive {
		t.Fatalf("session status after refused finish = %q, want active", sess.Session.Status)
	}

	// Run the checks (records pass), then finish succeeds and moves to review.
	if _, err := svc.RunChecks(id, nil); err != nil {
		t.Fatalf("RunChecks: %v", err)
	}
	if _, err := svc.FinishSession(context.Background(), FinishSessionInput{SessionID: sid, Summary: "done"}); err != nil {
		t.Fatalf("FinishSession after run_checks: %v", err)
	}
	reloaded, _ = svc.Get(id)
	if reloaded.Task.Status != "in_review" {
		t.Fatalf("status after passing finish = %q, want in_review", reloaded.Task.Status)
	}
}

// TestFinishAllowsPendingManualCheck confirms manual checks are exempt at handoff: they're
// attested during review, so a session finishes into review with the cmd checks passing even
// while a manual check is still pending.
func TestFinishAllowsPendingManualCheck(t *testing.T) {
	svc := NewServiceWithClient(service(t, "agent:codex").store, "agent:codex", "codex", nil)
	taskDoc, _ := svc.Create(store.Draft{Title: "x", Checks: []task.Check{
		{Desc: "build", Cmd: "exit 0"},
		{Desc: "human review", Type: "manual"},
	}})
	id := taskDoc.Task.ID
	sid := beginOn(t, svc, id, "begin-1")

	if _, err := svc.RunChecks(id, nil); err != nil { // records the cmd check pass; manual stays pending
		t.Fatalf("RunChecks: %v", err)
	}
	if _, err := svc.FinishSession(context.Background(), FinishSessionInput{SessionID: sid, Summary: "done"}); err != nil {
		t.Fatalf("FinishSession with pending manual check = %v, want success", err)
	}
	reloaded, _ := svc.Get(id)
	if reloaded.Task.Status != "in_review" {
		t.Fatalf("status = %q, want in_review (manual check attested later)", reloaded.Task.Status)
	}
}

func TestSessionIdentityMismatchWritesNothing(t *testing.T) {
	svc := service(t, "agent:codex")
	taskDoc, err := svc.Create(store.Draft{Title: "observable work"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.BeginSession(context.Background(), BeginSessionInput{
		TaskID: taskDoc.Task.ID, ExpectedActor: "agent:claude", Client: "codex", IdempotencyKey: "begin-1",
	})
	if !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("BeginSession error = %v, want ErrIdentityMismatch", err)
	}
	sessions, err := svc.store.ListSessions()
	if err != nil || len(sessions) != 0 {
		t.Fatalf("sessions after mismatch = %d, %v", len(sessions), err)
	}
	reloaded, _ := svc.Get(taskDoc.Task.ID)
	if reloaded.Task.Status != "backlog" || reloaded.Task.Assignee != "" {
		t.Fatalf("task mutated after mismatch: %+v", reloaded.Task)
	}
}

func TestBeginSessionRejectsOutsideWorktreeBeforeMutation(t *testing.T) {
	svc := service(t, "agent:codex")
	taskDoc, err := svc.Create(store.Draft{Title: "observable work"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.BeginSession(context.Background(), BeginSessionInput{
		TaskID: taskDoc.Task.ID, ExpectedActor: "agent:codex", Client: "codex",
		Worktree: t.TempDir(), IdempotencyKey: "begin-outside",
	})
	if !errors.Is(err, store.ErrWorktreeOutsideRoot) {
		t.Fatalf("BeginSession = %v, want ErrWorktreeOutsideRoot", err)
	}
	if sessions, err := svc.store.ListSessions(); err != nil || len(sessions) != 0 {
		t.Fatalf("sessions after rejected worktree = %d, %v", len(sessions), err)
	}
	reloaded, err := svc.Get(taskDoc.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Task.Status != "backlog" || reloaded.Task.Assignee != "" {
		t.Fatalf("task mutated after rejected worktree: %+v", reloaded.Task)
	}
}

func TestCarbonSessionReadsRequireExplicitClusterOptIn(t *testing.T) {
	dataRoot := t.TempDir()
	if err := repo.InitDataRoot(dataRoot, "CAR"); err != nil {
		t.Fatal(err)
	}
	projectOneRoot := t.TempDir()
	projectTwoRoot := t.TempDir()
	st := store.New(dataRoot)
	foreignTask, err := st.Create(store.Draft{Title: "foreign session", ProjectID: "project-two", ProjectIDSet: true}, "human:two", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	created, err := st.CreateSession(context.Background(), "agent:two", session.Session{
		ID: "ses_foreign", TaskID: foreignTask.Task.ID, AttemptID: "att_foreign", Actor: "agent:two",
		Status: session.StatusActive, IdempotencyKey: "foreign-session", StartedAt: time.Now(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewScopedServiceWithClientAndResolver(st, "agent:one", "", Scope{
		Home: "home", ClusterID: "cluster", ProjectID: "project-one",
	}, func(projectID string) (string, error) {
		switch projectID {
		case "project-one":
			return projectOneRoot, nil
		case "project-two":
			return projectTwoRoot, nil
		default:
			return "", errors.New("unknown project")
		}
	}, nil)

	if _, err := svc.GetSession(created.Session.ID); !errors.Is(err, ErrProjectScope) {
		t.Fatalf("default get foreign session = %v, want ErrProjectScope", err)
	}
	if got, err := svc.GetSessionScoped(created.Session.ID, true); err != nil || got.ID != created.Session.ID {
		t.Fatalf("include_cluster get foreign session = %#v, %v", got, err)
	}
	if views, err := svc.ListSessions("", "", "", ""); err != nil || len(views) != 0 {
		t.Fatalf("default list foreign sessions = %#v, %v", views, err)
	}
	if views, err := svc.ListSessionsScoped("", "", "", "", true); err != nil || len(views) != 1 || views[0].ID != created.Session.ID {
		t.Fatalf("include_cluster list foreign sessions = %#v, %v", views, err)
	}
}

func TestBeginSessionIsIdempotentAndClaimsTask(t *testing.T) {
	svc := NewServiceWithClient(service(t, "agent:codex").store, "agent:codex", "codex", nil)
	taskDoc, _ := svc.Create(store.Draft{Title: "observable work"})
	in := BeginSessionInput{
		TaskID: taskDoc.Task.ID, ExpectedActor: "agent:codex", Client: "codex", Model: "gpt-5",
		Worktree: svc.store.Root(), Branch: "codex/sessions", Head: "abc123", IdempotencyKey: "begin-1",
	}

	first, err := svc.BeginSession(context.Background(), in)
	if err != nil {
		t.Fatalf("BeginSession: %v", err)
	}
	second, err := svc.BeginSession(context.Background(), in)
	if err != nil {
		t.Fatalf("retry BeginSession: %v", err)
	}
	if first.ID != second.ID || first.AttemptID != second.AttemptID {
		t.Fatalf("retry changed identity: first=%+v second=%+v", first.Session, second.Session)
	}

	reloaded, _ := svc.Get(taskDoc.Task.ID)
	if reloaded.Task.Status != "in_progress" || reloaded.Task.Assignee != "agent:codex" || reloaded.Task.ActiveAttempt != first.AttemptID {
		t.Fatalf("task after begin = %+v", reloaded.Task)
	}
	begins := 0
	for _, p := range reloaded.Provenance {
		if p.Did == "began session "+first.ID {
			begins++
		}
	}
	if begins != 1 {
		t.Fatalf("begin provenance count = %d, want 1", begins)
	}

	in.IdempotencyKey = "begin-2"
	if _, err := svc.BeginSession(context.Background(), in); !errors.Is(err, store.ErrLiveSession) {
		t.Fatalf("second live begin error = %v, want ErrLiveSession", err)
	}
}

func TestHeartbeatAndFinishSession(t *testing.T) {
	at := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	rootSvc := service(t, "agent:codex")
	svc := NewServiceWithClient(rootSvc.store, "agent:codex", "codex", func() time.Time { return at })
	taskDoc, _ := svc.Create(store.Draft{Title: "observable work"})
	started, err := svc.BeginSession(context.Background(), BeginSessionInput{
		TaskID: taskDoc.Task.ID, ExpectedActor: "agent:codex", Client: "codex", IdempotencyKey: "begin-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	at = at.Add(time.Minute)
	heartbeat, err := svc.Heartbeat(context.Background(), HeartbeatInput{
		SessionID: started.ID, Progress: "running tests",
	})
	if err != nil {
		t.Fatal(err)
	}
	if heartbeat.Live == nil || heartbeat.Live.Progress != "running tests" {
		t.Fatalf("heartbeat = %+v", heartbeat)
	}

	at = at.Add(time.Minute)
	finished, err := svc.FinishSession(context.Background(), FinishSessionInput{
		SessionID: started.ID, Summary: "Implemented observable sessions", Head: "def456",
	})
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != session.StatusFinished || finished.Health != session.HealthFinished {
		t.Fatalf("finished = %+v", finished)
	}
	if live, _ := svc.store.ReadLive(started.ID); live != nil {
		t.Fatalf("live state retained after finish: %+v", live)
	}
	reloaded, _ := svc.Get(taskDoc.Task.ID)
	if reloaded.Task.Status != "in_review" {
		t.Fatalf("task status = %q, want in_review", reloaded.Task.Status)
	}
	views, err := svc.ListWithExecution("", "", nil, ExecutionAwaitingReview)
	if err != nil || len(views) != 1 || views[0].SessionID != started.ID {
		t.Fatalf("awaiting-review list = %+v, %v", views, err)
	}

	// A retry completes any later task step without mutating the terminal session again.
	if retry, err := svc.FinishSession(context.Background(), FinishSessionInput{SessionID: started.ID}); err != nil || retry.ID != started.ID {
		t.Fatalf("finish retry = %+v, %v", retry, err)
	}
}

func TestCancelSessionReleasesAssignee(t *testing.T) {
	svc := service(t, "agent:codex")
	taskDoc, _ := svc.Create(store.Draft{Title: "observable work"})
	started, _ := svc.BeginSession(context.Background(), BeginSessionInput{
		TaskID: taskDoc.Task.ID, ExpectedActor: "agent:codex", IdempotencyKey: "begin-1",
	})
	canceled, err := svc.CancelSession(context.Background(), CancelSessionInput{SessionID: started.ID, Reason: "superseded"})
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Status != session.StatusCanceled {
		t.Fatalf("status = %q", canceled.Status)
	}
	reloaded, _ := svc.Get(taskDoc.Task.ID)
	if reloaded.Task.Assignee != "" || reloaded.Task.Status != "in_progress" || reloaded.Task.ActiveAttempt != "" {
		t.Fatalf("task after cancel = %+v", reloaded.Task)
	}
	if state, _ := svc.ExecutionOf(reloaded.Task); state != "" {
		t.Fatalf("execution after cancel = %q, want none", state)
	}
}

// A previously-successful begin must remain idempotent even after a human closes the task:
// retrying with the same idempotency key returns the original session, not an error.
func TestBeginAfterCloseIsIdempotent(t *testing.T) {
	svc := service(t, "agent:codex")
	taskDoc, _ := svc.Create(store.Draft{Title: "observable work"})
	in := BeginSessionInput{
		TaskID: taskDoc.Task.ID, ExpectedActor: "agent:codex", IdempotencyKey: "begin-1",
	}
	first, err := svc.BeginSession(context.Background(), in)
	if err != nil {
		t.Fatalf("BeginSession: %v", err)
	}
	if _, err := svc.Transition(taskDoc.Task.ID, "done"); err != nil {
		t.Fatalf("Transition to done: %v", err)
	}
	second, err := svc.BeginSession(context.Background(), in)
	if err != nil {
		t.Fatalf("retry begin after close = %v, want nil", err)
	}
	if second.ID != first.ID || second.AttemptID != first.AttemptID {
		t.Fatalf("retry after close changed identity: first=%+v second=%+v", first.Session, second.Session)
	}
}

// finish retains the task's active_attempt so it derives awaiting_review, while cancel
// clears it; this guards the asymmetry between the two terminal verbs.
func TestFinishRetainsActiveAttempt(t *testing.T) {
	svc := service(t, "agent:codex")
	taskDoc, _ := svc.Create(store.Draft{Title: "observable work"})
	started, _ := svc.BeginSession(context.Background(), BeginSessionInput{
		TaskID: taskDoc.Task.ID, ExpectedActor: "agent:codex", IdempotencyKey: "begin-1",
	})
	if _, err := svc.FinishSession(context.Background(), FinishSessionInput{SessionID: started.ID, Summary: "done work"}); err != nil {
		t.Fatalf("FinishSession: %v", err)
	}
	reloaded, _ := svc.Get(taskDoc.Task.ID)
	if reloaded.Task.ActiveAttempt != started.AttemptID {
		t.Fatalf("active_attempt after finish = %q, want %q", reloaded.Task.ActiveAttempt, started.AttemptID)
	}
	if state, _ := svc.ExecutionOf(reloaded.Task); state != ExecutionAwaitingReview {
		t.Fatalf("execution after finish = %q, want awaiting_review", state)
	}
}

func TestSessionHealthBecomesStalled(t *testing.T) {
	at := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	rootSvc := service(t, "agent:codex")
	svc := NewService(rootSvc.store, "agent:codex", func() time.Time { return at })
	taskDoc, _ := svc.Create(store.Draft{Title: "observable work"})
	started, _ := svc.BeginSession(context.Background(), BeginSessionInput{
		TaskID: taskDoc.Task.ID, ExpectedActor: "agent:codex", IdempotencyKey: "begin-1",
	})
	at = at.Add(4 * time.Minute)
	view, err := svc.GetSession(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Health != session.HealthStalled {
		t.Fatalf("health = %q, want stalled", view.Health)
	}
	views, err := svc.ListWithExecution("", "", nil, ExecutionStalled)
	if err != nil || len(views) != 1 {
		t.Fatalf("stalled list = %+v, %v", views, err)
	}
}

func TestSessionLeaseFollowsBeginHeartbeatFinishAndCancel(t *testing.T) {
	at := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	rootSvc := service(t, "agent:codex")
	svc := NewServiceWithClient(rootSvc.store, "agent:codex", "codex", func() time.Time { return at })

	// A stale window longer than the normal 15-minute lease is the regression case:
	// begin and every heartbeat must keep the lease at least this long.
	cfg, err := svc.store.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SessionStaleAfter = int((20 * time.Minute).Seconds())
	if err := svc.store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	first, err := svc.Create(store.Draft{Title: "lease then review"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := svc.BeginSession(context.Background(), BeginSessionInput{
		TaskID: first.Task.ID, ExpectedActor: "agent:codex", Client: "codex", IdempotencyKey: "lease-begin",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSessionLeaseAtLeast(t, svc, first.Task.ID, at, 20*time.Minute)

	at = at.Add(5 * time.Minute)
	if _, err := svc.Heartbeat(context.Background(), HeartbeatInput{SessionID: started.ID, Progress: "still working"}); err != nil {
		t.Fatal(err)
	}
	assertSessionLeaseAtLeast(t, svc, first.Task.ID, at, 20*time.Minute)

	if _, err := svc.FinishSession(context.Background(), FinishSessionInput{SessionID: started.ID, Summary: "ready for review"}); err != nil {
		t.Fatal(err)
	}
	afterFinish, err := svc.Get(first.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterFinish.Task.Lease != nil || afterFinish.Task.Assignee != "agent:codex" {
		t.Fatalf("finish must release lease but preserve assignee: %+v", afterFinish.Task)
	}

	second, err := svc.Create(store.Draft{Title: "lease then cancel"})
	if err != nil {
		t.Fatal(err)
	}
	canceledSession, err := svc.BeginSession(context.Background(), BeginSessionInput{
		TaskID: second.Task.ID, ExpectedActor: "agent:codex", Client: "codex", IdempotencyKey: "cancel-begin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CancelSession(context.Background(), CancelSessionInput{SessionID: canceledSession.ID, Reason: "superseded"}); err != nil {
		t.Fatal(err)
	}
	afterCancel, err := svc.Get(second.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterCancel.Task.Lease != nil || afterCancel.Task.Assignee != "" {
		t.Fatalf("cancel must release lease and clear assignee: %+v", afterCancel.Task)
	}
}

func assertSessionLeaseAtLeast(t *testing.T, svc *Service, taskID string, now time.Time, want time.Duration) {
	t.Helper()
	doc, err := svc.Get(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Task.Lease == nil || doc.Task.Lease.Holder != "agent:codex" {
		t.Fatalf("missing actor lease: %+v", doc.Task)
	}
	expires, err := time.Parse(time.RFC3339, doc.Task.Lease.ExpiresAt)
	if err != nil {
		t.Fatalf("parse lease expiration %q: %v", doc.Task.Lease.ExpiresAt, err)
	}
	if expires.Before(now.Add(want)) {
		t.Fatalf("lease expiration %s before required %s", expires, now.Add(want))
	}
}
