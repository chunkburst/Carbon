package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"carbon/internal/repo"
	"carbon/internal/store"
	"carbon/internal/task"
)

// taskIDRe matches the time-ordered ids minted by store.mintTaskID (prefix + 16 base32 chars).
var taskIDRe = regexp.MustCompile(`^PROJ-[0-9a-z]{10}$`)

func service(t *testing.T, actor string) *Service {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, repo.CarbonDirName, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "prefix: PROJ\ncounter: 0\nstates: [backlog, in_progress, in_review, done, canceled]\nclosed: [done, canceled]\ninitial: backlog\ncheck_timeout_default: 30\n"
	if err := os.WriteFile(filepath.Join(root, repo.CarbonDirName, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 6, 21, 9, 0, 0, 0, time.UTC)
	return NewService(store.New(root), actor, func() time.Time { return at })
}

func TestCreateAndGet(t *testing.T) {
	svc := service(t, "agent:claude-1")
	d, err := svc.Create(store.Draft{Title: "first", Body: "body\n"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !taskIDRe.MatchString(d.Task.ID) || d.Task.Status != "backlog" {
		t.Fatalf("bad created task: %+v", d.Task)
	}
	got, err := svc.Get(d.Task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Provenance[0].Who != "agent:claude-1" || got.Provenance[0].Did != "created" {
		t.Fatalf("bad provenance: %+v", got.Provenance)
	}
}

func TestTaskActivityHealthUsesMeaningfulProvenanceAndConfiguredWindow(t *testing.T) {
	base := time.Date(2026, 6, 21, 9, 0, 0, 0, time.UTC)
	now := base
	svc := service(t, "agent:activity")
	svc.now = func() time.Time { return now }

	doc, err := svc.Create(store.Draft{Title: "observed"})
	if err != nil {
		t.Fatal(err)
	}

	// A bookkeeping lease renewal must not hide a task that has had no real work
	// since creation. It is deliberately newer than the creation provenance.
	if err := doc.AppendProvenance("agent:activity", "session lease renewed", "lease_id=lease_1", base.Add(23*time.Hour)); err != nil {
		t.Fatal(err)
	}
	cfg, err := svc.store.Config()
	if err != nil {
		t.Fatal(err)
	}

	now = base.Add(24*time.Hour - time.Second)
	fresh := deriveTaskActivity(doc.Task, doc.Provenance, cfg, svc.now())
	if fresh.Health != ActivityHealthFresh || fresh.LastMeaningfulAt != base.Format(time.RFC3339) || fresh.StagnantAt != base.Add(24*time.Hour).Format(time.RFC3339) {
		t.Fatalf("23:59:59 activity = %+v", fresh)
	}

	now = base.Add(24 * time.Hour)
	stagnant := deriveTaskActivity(doc.Task, doc.Provenance, cfg, svc.now())
	if stagnant.Health != ActivityHealthStagnant {
		t.Fatalf("24h activity = %+v, want stagnant", stagnant)
	}

	if err := doc.SetStatus("done"); err != nil {
		t.Fatal(err)
	}
	closed := deriveTaskActivity(doc.Task, doc.Provenance, cfg, svc.now())
	if closed.Health == ActivityHealthStagnant {
		t.Fatalf("closed task reported stagnant: %+v", closed)
	}

	cfg.TaskStagnationAfter = 60 * 60
	now = base.Add(59*time.Minute + 59*time.Second)
	if explicit := deriveTaskActivity(task.Task{Status: "in_progress"}, doc.Provenance, cfg, svc.now()); explicit.Health != ActivityHealthFresh {
		t.Fatalf("explicit 1h window before boundary = %+v", explicit)
	}
	now = base.Add(time.Hour)
	if explicit := deriveTaskActivity(task.Task{Status: "in_progress"}, doc.Provenance, cfg, svc.now()); explicit.Health != ActivityHealthStagnant {
		t.Fatalf("explicit 1h window at boundary = %+v", explicit)
	}

	unknown := deriveTaskActivity(task.Task{Status: "in_progress"}, nil, cfg, svc.now())
	if unknown.Health != ActivityHealthUnknown || unknown.LastMeaningfulAt != "" || unknown.StagnantAt != "" {
		t.Fatalf("missing provenance activity = %+v", unknown)
	}
}

func TestCreateRejectsBlankTitle(t *testing.T) {
	svc := service(t, "agent:a")
	if _, err := svc.Create(store.Draft{Title: "  "}); !errors.Is(err, ErrEmptyTitle) {
		t.Fatalf("Create blank title = %v, want ErrEmptyTitle", err)
	}
}

func TestCreateRejectsMissingDep(t *testing.T) {
	svc := service(t, "agent:a")
	if _, err := svc.Create(store.Draft{Title: "x", Deps: []string{"GHOST"}}); err == nil {
		t.Fatal("expected error creating task with missing dep")
	}
}

func TestClaim(t *testing.T) {
	svc := service(t, "agent:a")
	d, _ := svc.Create(store.Draft{Title: "x"})

	if _, err := svc.Claim(d.Task.ID); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// Same actor re-claiming is a no-op/ok.
	if _, err := svc.Claim(d.Task.ID); err != nil {
		t.Fatalf("re-claim by same actor should be ok: %v", err)
	}
	// Different actor is refused.
	other := NewService(svc.store, "agent:b", svc.now)
	if _, err := other.Claim(d.Task.ID); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("other claim = %v, want ErrAlreadyClaimed", err)
	}
}

func TestTransitionDepsGate(t *testing.T) {
	svc := service(t, "agent:a")
	dep, _ := svc.Create(store.Draft{Title: "dep"})
	blocked, _ := svc.Create(store.Draft{Title: "blocked", Deps: []string{dep.Task.ID}})

	if _, err := svc.Transition(blocked.Task.ID, "in_progress"); !errors.Is(err, task.ErrDepsNotClosed) {
		t.Fatalf("transition = %v, want deps gate", err)
	}
	// Close the dep, then the blocked task may start.
	if _, err := svc.Transition(dep.Task.ID, "done"); err != nil {
		t.Fatalf("close dep: %v", err)
	}
	if _, err := svc.Transition(blocked.Task.ID, "in_progress"); err != nil {
		t.Fatalf("transition after dep closed: %v", err)
	}
}

func TestTransitionAutoRunsChecksAndCloses(t *testing.T) {
	svc := service(t, "agent:a")
	d, _ := svc.Create(store.Draft{Title: "x", Checks: []task.Check{{Desc: "ok", Cmd: "exit 0"}}})

	got, err := svc.Transition(d.Task.ID, "done")
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	if got.Task.Status != "done" {
		t.Fatalf("status = %q, want done", got.Task.Status)
	}
	if got.Task.Checks[0].Result != "pass" {
		t.Fatalf("check result = %q, want pass (auto-run)", got.Task.Checks[0].Result)
	}
}

func TestTransitionRefusesOnFailingCheck(t *testing.T) {
	svc := service(t, "agent:a")
	d, _ := svc.Create(store.Draft{Title: "x", Checks: []task.Check{{Desc: "bad", Cmd: "exit 1"}}})

	if _, err := svc.Transition(d.Task.ID, "done"); !errors.Is(err, task.ErrChecksNotPassed) {
		t.Fatalf("transition = %v, want checks gate refusal", err)
	}
	got, _ := svc.Get(d.Task.ID)
	if got.Task.Status == "done" {
		t.Fatal("task closed despite failing check")
	}
	if got.Task.Checks[0].Result != "fail" {
		t.Fatalf("check result = %q, want fail (recorded)", got.Task.Checks[0].Result)
	}
}

// TestTransitionDoesNotTrustStaleStoredPass is the #2 guard: a recorded `pass` is never
// trusted at the close gate — the cmd checks are re-run fresh. Here the file claims `pass`
// but the command now fails, so the close must be refused and the result corrected to fail.
func TestTransitionDoesNotTrustStaleStoredPass(t *testing.T) {
	svc := service(t, "agent:a")
	// Seed a task whose stored check result lies: result=pass on a command that exits 1.
	stale := "---\nid: STALE-1\ntitle: stale pass\nstatus: in_review\nchecks:\n  - desc: build\n    cmd: exit 1\n    result: pass\nprovenance:\n  - {who: agent:a, at: 2026-06-21T10:00:00Z, did: created}\n---\nbody\n"
	path := filepath.Join(svc.store.Root(), repo.CarbonDirName, "tasks", "STALE-1.md")
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Transition("STALE-1", "done"); !errors.Is(err, task.ErrChecksNotPassed) {
		t.Fatalf("transition = %v, want ErrChecksNotPassed (stale pass must be re-verified)", err)
	}
	got, _ := svc.Get("STALE-1")
	if got.Task.Status == "done" {
		t.Fatal("task closed on a stale stored pass")
	}
	if got.Task.Checks[0].Result != "fail" {
		t.Fatalf("check result = %q, want fail (re-run corrected the stale pass)", got.Task.Checks[0].Result)
	}
}

func TestTransitionRefusesOnPendingManualCheck(t *testing.T) {
	svc := service(t, "agent:a")
	d, _ := svc.Create(store.Draft{Title: "x", Checks: []task.Check{{Desc: "review", Type: "manual"}}})

	if _, err := svc.Transition(d.Task.ID, "done"); !errors.Is(err, task.ErrChecksNotPassed) {
		t.Fatalf("transition = %v, want refusal on pending manual check", err)
	}
}

func TestRunChecks(t *testing.T) {
	svc := service(t, "agent:a")
	d, _ := svc.Create(store.Draft{Title: "x", Checks: []task.Check{{Desc: "a", Cmd: "exit 0"}, {Desc: "b", Cmd: "exit 1"}}})

	got, err := svc.RunChecks(d.Task.ID, nil)
	if err != nil {
		t.Fatalf("RunChecks: %v", err)
	}
	if got.Task.Checks[0].Result != "pass" || got.Task.Checks[1].Result != "fail" {
		t.Fatalf("results = %q,%q want pass,fail", got.Task.Checks[0].Result, got.Task.Checks[1].Result)
	}
}

func TestNoteAppendsProvenance(t *testing.T) {
	svc := service(t, "agent:a")
	d, _ := svc.Create(store.Draft{Title: "x"})
	if _, err := svc.Note(d.Task.ID, "looked into it"); err != nil {
		t.Fatalf("Note: %v", err)
	}
	got, _ := svc.Get(d.Task.ID)
	last := got.Provenance[len(got.Provenance)-1]
	if last.Did != "note" || last.Text != "looked into it" {
		t.Fatalf("bad note provenance: %+v", last)
	}
}

func TestListFilters(t *testing.T) {
	svc := service(t, "agent:a")
	dep, _ := svc.Create(store.Draft{Title: "dep"})
	svc.Create(store.Draft{Title: "blocked", Deps: []string{dep.Task.ID}})
	free, _ := svc.Create(store.Draft{Title: "free"})

	// ready=true should include the depless tasks but exclude the blocked one.
	ready := true
	views, err := svc.List("", "", &ready)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	ids := map[string]bool{}
	for _, v := range views {
		if !v.Ready {
			t.Fatalf("ready filter returned not-ready task %s", v.ID)
		}
		ids[v.ID] = true
	}
	if !ids[dep.Task.ID] || !ids[free.Task.ID] {
		t.Fatalf("ready set missing depless tasks: %v", ids)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 ready tasks, got %d (%v)", len(ids), ids)
	}
}

func TestAttestManualCheck(t *testing.T) {
	svc := service(t, "agent:a")
	d, err := svc.Create(store.Draft{Title: "x", Checks: []task.Check{
		{Desc: "human review", Type: "manual", Result: "pending"},
		{Desc: "build", Cmd: "exit 0", Result: "pending"},
	}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := d.Task.ID

	got, err := svc.Attest(id, 0, true)
	if err != nil {
		t.Fatalf("Attest: %v", err)
	}
	if got.Task.Checks[0].Result != "pass" {
		t.Fatalf("manual check = %q, want pass", got.Task.Checks[0].Result)
	}
	last := got.Provenance[len(got.Provenance)-1]
	if last.Did != "attested" || last.Who != "agent:a" {
		t.Fatalf("provenance = %+v, want attested by agent:a", last)
	}

	if _, err := svc.Attest(id, 1, true); !errors.Is(err, ErrNotManual) {
		t.Fatalf("attesting a cmd check = %v, want ErrNotManual", err)
	}
	if _, err := svc.Attest(id, 9, true); err == nil {
		t.Fatal("expected error for out-of-range index")
	}
}

func TestAttestUnblocksClose(t *testing.T) {
	svc := service(t, "agent:a")
	d, _ := svc.Create(store.Draft{Title: "x", Checks: []task.Check{{Desc: "review", Type: "manual", Result: "pending"}}})
	id := d.Task.ID

	if _, err := svc.Transition(id, "done"); err == nil {
		t.Fatal("expected refusal closing with a pending manual check")
	}
	if _, err := svc.Attest(id, 0, true); err != nil {
		t.Fatalf("Attest: %v", err)
	}
	if _, err := svc.Transition(id, "done"); err != nil {
		t.Fatalf("close after attest should pass: %v", err)
	}
}

func TestUpdateFields(t *testing.T) {
	svc := service(t, "agent:a")
	a, _ := svc.Create(store.Draft{Title: "epic"})
	b, _ := svc.Create(store.Draft{Title: "child"})

	// set priority + labels + parent
	got, err := svc.Update(b.Task.ID, UpdateFields{
		Priority: ptr("high"),
		Labels:   ptrSlice([]string{"backend", "db"}),
		Parent:   ptr(a.Task.ID),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Task.Priority != "high" || got.Task.Parent != a.Task.ID || len(got.Task.Labels) != 2 {
		t.Fatalf("update not applied: %+v", got.Task)
	}

	// invalid priority rejected
	if _, err := svc.Update(b.Task.ID, UpdateFields{Priority: ptr("ASAP")}); !errors.Is(err, task.ErrInvalidPriority) {
		t.Fatalf("invalid priority = %v, want ErrInvalidPriority", err)
	}
	// self-parent rejected as a cycle
	if _, err := svc.Update(b.Task.ID, UpdateFields{Parent: ptr(b.Task.ID)}); !errors.Is(err, task.ErrParentCycle) {
		t.Fatalf("self parent = %v, want ErrParentCycle", err)
	}
	// no-op update appends no provenance
	before := len(got.Provenance)
	noop, _ := svc.Update(b.Task.ID, UpdateFields{})
	if len(noop.Provenance) != before {
		t.Fatalf("no-op update changed provenance: %d -> %d", before, len(noop.Provenance))
	}
}

func ptr(s string) *string          { return &s }
func ptrSlice(s []string) *[]string { return &s }

func TestUpdateTitleBodyChecks(t *testing.T) {
	svc := service(t, "agent:a")
	d, _ := svc.Create(store.Draft{Title: "old", Body: "old body\n"})

	got, err := svc.Update(d.Task.ID, UpdateFields{
		Title:  ptr("new title"),
		Body:   ptr("new body\n"),
		Checks: &[]task.Check{{Desc: "tests", Cmd: "go test ./...", Result: "pending"}},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Task.Title != "new title" || got.Body != "new body\n" || len(got.Task.Checks) != 1 {
		t.Fatalf("edit not applied: %+v body=%q", got.Task, got.Body)
	}

	// empty title rejected
	if _, err := svc.Update(d.Task.ID, UpdateFields{Title: ptr("  ")}); !errors.Is(err, ErrEmptyTitle) {
		t.Fatalf("empty title = %v, want ErrEmptyTitle", err)
	}
}

func TestBlockerStateChangesHaveDedicatedProvenance(t *testing.T) {
	svc := service(t, "agent:writer")
	created, err := svc.Create(store.Draft{Title: "blocked work"})
	if err != nil {
		t.Fatal(err)
	}

	reason := "waiting for credentials"
	blocked, err := svc.SetBlockerWithVersion(created.Task.ID, reason, created.Version())
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked.Provenance) != len(created.Provenance)+1 {
		t.Fatalf("blocker state switch added unexpected provenance: %d -> %d", len(created.Provenance), len(blocked.Provenance))
	}
	entry := blocked.Provenance[len(blocked.Provenance)-1]
	if entry.Did != "blocked" || entry.Text != reason {
		t.Fatalf("blocked provenance = %+v", entry)
	}

	updatedReason := "waiting for deployment window"
	updated, err := svc.UpdateWithVersion(created.Task.ID, UpdateFields{BlockerReason: &updatedReason}, blocked.Version())
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Provenance) != len(blocked.Provenance)+1 || updated.Provenance[len(updated.Provenance)-1].Did != "updated" {
		t.Fatalf("non-empty blocker rewrite should remain an update: %+v", updated.Provenance)
	}

	clear := ""
	unblocked, err := svc.SetBlockerWithVersion(created.Task.ID, clear, updated.Version())
	if err != nil {
		t.Fatal(err)
	}
	if len(unblocked.Provenance) != len(updated.Provenance)+1 {
		t.Fatalf("unblock state switch added unexpected provenance: %d -> %d", len(updated.Provenance), len(unblocked.Provenance))
	}
	entry = unblocked.Provenance[len(unblocked.Provenance)-1]
	if entry.Did != "unblocked" || entry.Text != "" {
		t.Fatalf("unblocked provenance = %+v", entry)
	}

	reblocked, err := svc.SetBlockerWithVersion(created.Task.ID, "waiting for review", unblocked.Version())
	if err != nil {
		t.Fatal(err)
	}
	newTitle := "blocked work, revised"
	combined, err := svc.UpdateWithVersion(created.Task.ID, UpdateFields{
		BlockerReason: &clear,
		Title:         &newTitle,
	}, reblocked.Version())
	if err != nil {
		t.Fatal(err)
	}
	if combined.Task.Title != newTitle || combined.Task.BlockerReason != "" {
		t.Fatalf("combined update not applied: %+v", combined.Task)
	}
	if len(combined.Provenance) != len(reblocked.Provenance)+2 {
		t.Fatalf("combined blocker/update provenance = %d, want %d", len(combined.Provenance), len(reblocked.Provenance)+2)
	}
	last := combined.Provenance[len(combined.Provenance)-2:]
	if last[0].Did != "unblocked" || last[1].Did != "updated" {
		t.Fatalf("combined blocker/update provenance = %+v", last)
	}
}

func TestBlockerAndEvidenceAreVersionProtectedAndAudited(t *testing.T) {
	svc := service(t, "agent:writer")
	created, err := svc.Create(store.Draft{
		Title:         "proof task",
		BlockerReason: "waiting on credentials",
		Evidence: []task.Evidence{{
			Kind: "git_commit", Value: "abc123", CreatedAt: "2000-01-01T00:00:00Z", CreatedBy: "agent:forged",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Task.BlockerReason != "waiting on credentials" || len(created.Task.Evidence) != 1 {
		t.Fatalf("create fields = %+v", created.Task)
	}
	if _, err := svc.SetBlockerWithVersion(created.Task.ID, "missing version", " "); !errors.Is(err, ErrExpectedVersionRequired) {
		t.Fatalf("blocker without version = %v, want ErrExpectedVersionRequired", err)
	}
	first := created.Task.Evidence[0]
	if first.ID == "" || first.CreatedAt != "2026-06-21T09:00:00Z" || first.CreatedBy != "agent:writer" {
		t.Fatalf("new evidence audit was forgeable: %+v", first)
	}

	replacement := []task.Evidence{{
		ID: first.ID, Kind: "git_commit", Value: "def456", Label: "updated proof",
		CreatedAt: "1999-01-01T00:00:00Z", CreatedBy: "agent:forged",
	}}
	updated, err := svc.UpdateWithVersion(created.Task.ID, UpdateFields{Evidence: &replacement}, created.Version())
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.Task.Evidence[0]; got.Value != "def456" || got.CreatedAt != first.CreatedAt || got.CreatedBy != first.CreatedBy {
		t.Fatalf("existing evidence audit changed: %+v (original %+v)", got, first)
	}
	if len(updated.Provenance) != len(created.Provenance)+1 {
		t.Fatalf("ordinary evidence update omitted provenance: %d -> %d", len(created.Provenance), len(updated.Provenance))
	}

	if _, err := svc.SetBlockerWithVersion(created.Task.ID, "stale", created.Version()); !errors.Is(err, store.ErrVersionMismatch) {
		t.Fatalf("stale blocker update = %v, want ErrVersionMismatch", err)
	}
	added, err := svc.AddEvidenceWithVersion(created.Task.ID, task.Evidence{Kind: "ci_job", Value: "run-42", CreatedBy: "agent:forged"}, updated.Version())
	if err != nil {
		t.Fatal(err)
	}
	if len(added.Task.Evidence) != 2 {
		t.Fatalf("added evidence = %+v", added.Task.Evidence)
	}
	second := added.Task.Evidence[1]
	if second.ID == "" || second.CreatedAt != "2026-06-21T09:00:00Z" || second.CreatedBy != "agent:writer" {
		t.Fatalf("added evidence audit was forgeable: %+v", second)
	}
	removed, err := svc.RemoveEvidenceWithVersion(created.Task.ID, first.ID, added.Version())
	if err != nil {
		t.Fatal(err)
	}
	if len(removed.Task.Evidence) != 1 || removed.Task.Evidence[0].ID != second.ID {
		t.Fatalf("remove evidence = %+v", removed.Task.Evidence)
	}
	transitioned, err := svc.Transition(created.Task.ID, "in_progress")
	if err != nil {
		t.Fatal(err)
	}
	if transitioned.Task.BlockerReason != "waiting on credentials" {
		t.Fatalf("transition unexpectedly cleared blocker reason: %+v", transitioned.Task)
	}
	invalid := []task.Evidence{{Kind: "artifact", Value: ""}}
	if _, err := svc.UpdateWithVersion(created.Task.ID, UpdateFields{Evidence: &invalid}, transitioned.Version()); !errors.Is(err, task.ErrInvalidEvidence) {
		t.Fatalf("invalid evidence update = %v, want ErrInvalidEvidence", err)
	}
	badBlocker := "bad\x00reason"
	if _, err := svc.UpdateWithVersion(created.Task.ID, UpdateFields{BlockerReason: &badBlocker}, transitioned.Version()); !errors.Is(err, task.ErrInvalidBlockerReason) {
		t.Fatalf("invalid blocker update = %v, want ErrInvalidBlockerReason", err)
	}
}

func TestDelete(t *testing.T) {
	svc := service(t, "agent:a")
	parent, _ := svc.Create(store.Draft{Title: "parent"})
	child, _ := svc.Create(store.Draft{Title: "child", Parent: parent.Task.ID})

	// parent blocked by child
	if err := svc.Delete(parent.Task.ID); !errors.Is(err, task.ErrHasChildren) {
		t.Fatalf("delete parent = %v, want ErrHasChildren", err)
	}
	// child is a leaf: deletable
	if err := svc.Delete(child.Task.ID); err != nil {
		t.Fatalf("delete child: %v", err)
	}
	if _, err := svc.Get(child.Task.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get deleted = %v, want ErrNotFound", err)
	}
	// now parent is a leaf too
	if err := svc.Delete(parent.Task.ID); err != nil {
		t.Fatalf("delete parent after child gone: %v", err)
	}
}

func TestEditAndDeleteNote(t *testing.T) {
	svc := service(t, "agent:a")
	d, _ := svc.Create(store.Draft{Title: "x"})
	noted, _ := svc.Note(d.Task.ID, "first")
	noteID := noted.Provenance[len(noted.Provenance)-1].ID
	if noteID == "" {
		t.Fatal("note should have an id")
	}

	// anyone (a different actor) can edit
	other := NewService(svc.store, "agent:b", svc.now)
	edited, err := other.EditNote(d.Task.ID, noteID, -1, "edited")
	if err != nil {
		t.Fatalf("EditNote: %v", err)
	}
	last := edited.Provenance[len(edited.Provenance)-1]
	if last.Text != "edited" || last.EditedAt == "" {
		t.Fatalf("note not edited: %+v", last)
	}

	// editing a system entry is refused
	if _, err := svc.EditNote(d.Task.ID, "", 0, "nope"); !errors.Is(err, store.ErrNotEditable) {
		t.Fatalf("edit system = %v, want ErrNotEditable", err)
	}

	// delete the note
	after, err := other.DeleteNote(d.Task.ID, noteID, -1)
	if err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	for _, p := range after.Provenance {
		if p.ID == noteID {
			t.Fatal("note not deleted")
		}
	}
}

func TestReorderAddsNoProvenance(t *testing.T) {
	svc := service(t, "agent:a")
	d, _ := svc.Create(store.Draft{Title: "x"})
	before := len(d.Provenance)

	got, err := svc.Reorder(d.Task.ID, 4096)
	if err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	if got.Task.Rank != 4096 {
		t.Fatalf("rank = %v, want 4096", got.Task.Rank)
	}
	if len(got.Provenance) != before {
		t.Fatalf("reorder appended provenance: %d -> %d", before, len(got.Provenance))
	}
}
