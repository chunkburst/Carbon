package stats

import (
	"testing"
	"time"

	"carbon/internal/store"
	"carbon/internal/task"
)

func TestComputeWorkerMetricsAndProjectFilter(t *testing.T) {
	p1 := "p1"
	docs := []*store.Doc{
		{Task: task.Task{ID: "A", ProjectID: p1, Status: "done", Assignee: "agent:b", Priority: "high"}, Provenance: []store.Provenance{
			{Who: "agent:a", At: "2026-08-05T10:00:00Z", Did: "created"},
			{Who: "agent:a", At: "2026-08-05T12:00:00Z", Did: "transitioned to done"},
		}},
		{Task: task.Task{ID: "B", ProjectID: p1, Status: "done", Assignee: "agent:a", Priority: "medium"}, Provenance: []store.Provenance{
			{Who: "agent:a", At: "2026-08-05T10:00:00Z", Did: "created"},
			{Who: "agent:a", At: "2026-08-05T11:00:00Z", Did: "transitioned to done"},
			{Who: "agent:a", At: "2026-08-05T12:00:00Z", Did: "transitioned to in_progress"},
			{Who: "agent:a", At: "2026-08-05T13:00:00Z", Did: "transitioned to done"},
		}},
		{Task: task.Task{ID: "C", ProjectID: p1, Status: "in_progress", Assignee: "agent:a"}},
		{Task: task.Task{ID: "D", ProjectID: "p2", Status: "done", Assignee: "agent:b", Priority: "urgent"}},
	}
	rules := task.Rules{Closed: []string{"done"}}
	report := Compute(docs, rules, Filter{Scope: ScopeProject, ProjectID: &p1})
	if len(report.Workers) != 1 {
		t.Fatalf("workers = %+v", report.Workers)
	}
	worker := report.Workers[0]
	if worker.Actor != "agent:a" || worker.Active != 1 || worker.Completed != 2 || worker.CompletedByPriority["high"] != 1 || worker.Reopened != 1 || worker.ReopenRate != 0.5 {
		t.Fatalf("worker stats = %+v", worker)
	}
	if worker.AverageCycleSeconds != 9000 { // (2h + 3h) / 2
		t.Fatalf("average cycle = %v", worker.AverageCycle)
	}
	if !matchProject("", Filter{Scope: ScopeCluster, ClusterProjects: []string{"p1"}}) {
		t.Fatal("cluster-wide task should be included in a cluster filter")
	}
}

func TestComputeExcludesExpiredLeaseFromActive(t *testing.T) {
	p1 := "p1"
	report := Compute([]*store.Doc{
		{
			Task: task.Task{
				ID: "expired", ProjectID: p1, Status: "in_progress", Assignee: "agent:stale",
				Lease: &task.Lease{Holder: "agent:stale", ExpiresAt: "2000-01-01T00:00:00Z"},
			},
		},
		{
			Task: task.Task{ID: "manual", ProjectID: p1, Status: "in_progress", Assignee: "agent:live"},
		},
		{
			Task: task.Task{
				ID: "live-lease", ProjectID: p1, Status: "in_progress", Assignee: "agent:old",
				Lease: &task.Lease{Holder: "agent:lease", ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339)},
			},
		},
	}, task.Rules{Closed: []string{"done"}}, Filter{Scope: ScopeProject, ProjectID: &p1})

	if len(report.Workers) != 2 {
		t.Fatalf("workers = %+v", report.Workers)
	}
	if report.Workers[0].Actor != "agent:lease" || report.Workers[0].Active != 1 {
		t.Fatalf("live lease stats = %+v", report.Workers[0])
	}
	if report.Workers[1].Actor != "agent:live" || report.Workers[1].Active != 1 {
		t.Fatalf("manual assignment stats = %+v", report.Workers[1])
	}
}

func TestComputeWorkerResetCutoffTruncatesClosedHistoryButKeepsCurrentActive(t *testing.T) {
	p1 := "p1"
	cutoff := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	report := Compute([]*store.Doc{
		{
			Task: task.Task{ID: "reopened", ProjectID: p1, Status: "done", Assignee: "agent:a", Priority: "high"},
			Provenance: []store.Provenance{
				{Who: "agent:a", At: "2026-08-08T10:00:00Z", Did: "created"},
				{Who: "agent:a", At: "2026-08-08T11:00:00Z", Did: "transitioned to done"},
				{Who: "agent:a", At: "2026-08-08T12:10:00Z", Did: "transitioned to in_progress"},
				{Who: "agent:a", At: "2026-08-08T14:00:00Z", Did: "transitioned to done"},
			},
		},
		{Task: task.Task{ID: "current", ProjectID: p1, Status: "in_progress", Assignee: "agent:a"}},
		{
			Task: task.Task{ID: "old", ProjectID: p1, Status: "done", Assignee: "agent:b"},
			Provenance: []store.Provenance{
				{Who: "agent:b", At: "2026-08-08T10:00:00Z", Did: "created"},
				{Who: "agent:b", At: "2026-08-08T11:00:00Z", Did: "transitioned to done"},
			},
		},
	}, task.Rules{Closed: []string{"done"}}, Filter{
		Scope: ScopeProject, ProjectID: &p1,
		WorkerCutoffs: map[string]WorkerCutoff{
			"agent:a": {ResetAt: cutoff},
			"agent:b": {ResetAt: cutoff},
		},
	})

	if len(report.Workers) != 1 {
		t.Fatalf("workers after reset = %+v", report.Workers)
	}
	worker := report.Workers[0]
	if worker.Actor != "agent:a" || worker.Active != 1 || worker.Completed != 1 || worker.Reopened != 1 || worker.CycleSamples != 1 {
		t.Fatalf("reset worker = %+v", worker)
	}
	if got, want := worker.AverageCycleSeconds, 2*60*60.0; got != want {
		t.Fatalf("reset cycle = %v, want %v", got, want)
	}
	if worker.LastActivity == nil || worker.LastActivity.Format(time.RFC3339) != "2026-08-08T14:00:00Z" {
		t.Fatalf("last activity = %v", worker.LastActivity)
	}
	if len(worker.RecentWork) != 1 || worker.RecentWork[0].TaskID != "reopened" || worker.RecentWork[0].Activity != "transitioned to done" {
		t.Fatalf("recent work = %+v", worker.RecentWork)
	}
}

func TestComputeDeletedWorkerRequiresLaterDurableActivity(t *testing.T) {
	p1 := "p1"
	deletedAt := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	doc := &store.Doc{
		Task: task.Task{ID: "prior", ProjectID: p1, Status: "done", Assignee: "agent:gone"},
		Provenance: []store.Provenance{
			{Who: "agent:gone", At: "2026-08-08T10:00:00Z", Did: "created"},
			{Who: "agent:gone", At: "2026-08-08T11:00:00Z", Did: "transitioned to done"},
		},
	}
	filter := Filter{Scope: ScopeProject, ProjectID: &p1, WorkerCutoffs: map[string]WorkerCutoff{"agent:gone": {DeletedAt: deletedAt}}}
	first := Compute([]*store.Doc{doc}, task.Rules{Closed: []string{"done"}}, filter)
	if len(first.Workers) != 0 {
		t.Fatalf("deleted Worker remained visible: %+v", first.Workers)
	}

	doc.Provenance = append(doc.Provenance, store.Provenance{Who: "agent:gone", At: "2026-08-08T13:00:00Z", Did: "note added"})
	revived := Compute([]*store.Doc{doc}, task.Rules{Closed: []string{"done"}}, filter)
	if len(revived.Workers) != 1 || revived.Workers[0].Actor != "agent:gone" {
		t.Fatalf("post-delete activity did not revive Worker: %+v", revived.Workers)
	}
	worker := revived.Workers[0]
	if worker.Completed != 0 || len(worker.RecentWork) != 1 || worker.RecentWork[0].Activity != "note added" {
		t.Fatalf("revived Worker leaked pre-delete metrics: %+v", worker)
	}
}

func TestComputeAggregateAndRecentWorkAreWeightedAndBounded(t *testing.T) {
	p1 := "p1"
	docs := []*store.Doc{
		{
			Task: task.Task{ID: "one", ProjectID: p1, Status: "done", Assignee: "agent:a"},
			Provenance: []store.Provenance{
				{Who: "agent:a", At: "2026-08-08T10:00:00Z", Did: "created"},
				{Who: "agent:a", At: "2026-08-08T12:00:00Z", Did: "transitioned to done"},
			},
		},
		{
			Task: task.Task{ID: "two", ProjectID: p1, Status: "done", Assignee: "agent:a"},
			Provenance: []store.Provenance{
				{Who: "agent:a", At: "2026-08-08T10:00:00Z", Did: "created"},
				{Who: "agent:a", At: "2026-08-08T14:00:00Z", Did: "transitioned to done"},
			},
		},
		{Task: task.Task{ID: "open", ProjectID: p1, Status: "in_progress", Assignee: "agent:a"}},
	}
	for i := 0; i < MaxRecentWork+3; i++ {
		at := time.Date(2026, 8, 9, 0, 0, i, 0, time.UTC)
		docs = append(docs, &store.Doc{
			Task:       task.Task{ID: "recent-" + string(rune('a'+i)), Title: "recent", ProjectID: p1, Status: "backlog"},
			Provenance: []store.Provenance{{Who: "agent:a", At: at.Format(time.RFC3339), Did: "note added"}},
		})
	}
	report := Compute(docs, task.Rules{Closed: []string{"done"}}, Filter{Scope: ScopeProject, ProjectID: &p1})
	if report.Aggregate.TaskCount != len(docs) || report.Aggregate.Active != 1 || report.Aggregate.Completed != 2 || report.Aggregate.Open != len(docs)-2 || report.Aggregate.CycleSamples != 2 || report.Aggregate.AverageCycleSeconds != 3*60*60 {
		t.Fatalf("aggregate = %+v", report.Aggregate)
	}
	if len(report.Workers) != 1 || len(report.Workers[0].RecentWork) != MaxRecentWork {
		t.Fatalf("recent bound = %+v", report.Workers)
	}
	recent := report.Workers[0].RecentWork
	if recent[0].TaskID != "recent-o" || recent[len(recent)-1].TaskID != "recent-d" {
		t.Fatalf("recent order = %+v", recent)
	}
}

func TestActivitiesIncludesLeaseAcquireAndRenewal(t *testing.T) {
	docs := []*store.Doc{{Task: task.Task{
		ID: "lease", Status: "in_progress",
		Lease: &task.Lease{Holder: "agent:lease", AcquiredAt: "2026-08-08T10:00:00Z", RenewedAt: "2026-08-08T11:00:00Z"},
	}}}
	activities := Activities(docs)
	if got := activities["agent:lease"].Format(time.RFC3339); got != "2026-08-08T11:00:00Z" {
		t.Fatalf("lease activity = %q", got)
	}
}
