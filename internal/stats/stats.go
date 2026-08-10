// Package stats derives Worker metrics from task state and provenance. It has no home
// or cluster dependency: callers pass project ids (or an opaque matcher) for their
// scope, plus optional per-Worker lifecycle cutoffs from a home registry.
package stats

import (
	"sort"
	"strings"
	"time"

	"carbon/internal/store"
	"carbon/internal/task"
)

type Scope string

const (
	ScopeAll     Scope = "all"
	ScopeCluster Scope = "cluster"
	ScopeProject Scope = "project"

	// MaxRecentWork is the bounded amount of task activity exposed for one Worker.
	// Keeping it small makes the Worker overview useful without leaking task bodies or
	// turning every stats refresh into a complete activity feed.
	MaxRecentWork = 12
)

// WorkerCutoff is supplied by the home-global Worker registry. ResetAt clears one
// Worker's derived history; DeletedAt both hides an inactive tombstone and forms a
// second history boundary if the actor later produces new activity.
type WorkerCutoff struct {
	ResetAt   time.Time
	DeletedAt time.Time
}

// Filter accepts both direct project scope and a caller-resolved Cluster project list.
// ClusterID is retained for API/UI attribution only; the stats package never reaches
// into home/cluster storage to resolve it. IncludeProject can provide a dynamic mapping.
// WorkerCutoffs is keyed by exact canonical actor identity and is optional, preserving
// the historical metrics result for callers that do not have a Carbon home registry.
type Filter struct {
	Scope           Scope
	ClusterID       string
	ProjectID       *string
	ClusterProjects []string
	IncludeProject  func(projectID string) bool
	WorkerCutoffs   map[string]WorkerCutoff
}

// RecentWork is intentionally task-summary-only evidence of a Worker's latest work.
// It never includes a task body, note text, check output, or other potentially large
// private content.
type RecentWork struct {
	TaskID    string `json:"taskId"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	ProjectID string `json:"projectId,omitempty"`
	Activity  string `json:"activity"`
	At        string `json:"at"`
}

type Worker struct {
	Actor               string         `json:"actor"`
	Active              int            `json:"active"`
	Completed           int            `json:"completed"`
	CompletedByPriority map[string]int `json:"completed_by_priority"`
	AverageCycle        time.Duration  `json:"average_cycle"`
	AverageCycleSeconds float64        `json:"average_cycle_seconds"`
	Reopened            int            `json:"reopened"`
	ReopenRate          float64        `json:"reopen_rate"`
	CycleSamples        int            `json:"cycle_samples"`
	LastActivity        *time.Time     `json:"lastActivity,omitempty"`
	RecentWork          []RecentWork   `json:"recent_work,omitempty"`
}

// Aggregate is a task-level view of the selected scope. Unlike per-Worker metrics it
// deliberately does not inherit a single Worker's reset/delete cutoff: a Worker
// lifecycle action must not change the actual project completion totals.
type Aggregate struct {
	TaskCount           int     `json:"taskCount"`
	Active              int     `json:"active"`
	Completed           int     `json:"completed"`
	Open                int     `json:"open"`
	AverageCycleSeconds float64 `json:"averageCycleSeconds"`
	CycleSamples        int     `json:"cycleSamples"`
	Reopened            int     `json:"reopened"`
	ReopenRate          float64 `json:"reopenRate"`
}

type Report struct {
	Filter    Filter    `json:"-"`
	Workers   []Worker  `json:"workers"`
	Aggregate Aggregate `json:"aggregate"`
}

type accumulator struct {
	worker     Worker
	cycleTotal time.Duration
	recent     map[string]recentEntry
}

type recentEntry struct {
	item RecentWork
	at   time.Time
}

type activityPoint struct {
	at       time.Time
	activity string
	order    int
}

type lifecycleInfo struct {
	created         time.Time
	completed       time.Time
	completionActor string
	reopenedAt      []time.Time
}

// Compute derives stats over an already-loaded task collection. The configured Rules
// define which statuses are closed; no hardcoded "done" assumption leaks into metrics.
func Compute(docs []*store.Doc, rules task.Rules, filter Filter) Report {
	// Capture the clock once so every task in this report is evaluated against the
	// same ownership boundary. A durable lease is active only until expires_at;
	// stale task files should not inflate the current-worker count before a lease
	// sweep has had a chance to persist their release.
	now := time.Now().UTC()
	activities := make(map[string]activityPoint)
	for _, doc := range docs {
		if doc == nil || !matchProject(doc.Task.ProjectID, filter) {
			continue
		}
		mergeActivityPoints(activities, activitiesForDoc(doc))
	}

	accs := map[string]*accumulator{}
	get := func(actor string) *accumulator {
		actor = metricActor(actor)
		if accs[actor] == nil {
			counts := make(map[string]int, len(task.Priorities)+1)
			for _, priority := range task.Priorities {
				counts[priority] = 0
			}
			accs[actor] = &accumulator{
				worker: Worker{Actor: actor, CompletedByPriority: counts},
				recent: make(map[string]recentEntry),
			}
		}
		return accs[actor]
	}

	var aggregate Aggregate
	var aggregateCycleTotal time.Duration
	for _, doc := range docs {
		if doc == nil || !matchProject(doc.Task.ProjectID, filter) {
			continue
		}
		aggregate.TaskCount++
		docActivities := activitiesForDoc(doc)

		// Every actor with visible provenance/lease activity is a Worker candidate,
		// even if they did not close the task. This powers the per-Worker work view
		// without letting task bodies or note text escape through statistics.
		for actor, point := range docActivities {
			if !workerVisible(actor, filter, activities[actor].at) || !afterWorkerCutoff(point.at, workerBoundary(actor, filter)) {
				continue
			}
			get(actor).addRecent(doc, point)
		}

		closed := rules.IsClosed(doc.Task.Status)
		if !closed {
			aggregate.Open++
			owner := activeOwner(doc.Task, now)
			if owner == "" {
				continue
			}
			aggregate.Active++
			// A reset clears completed history, not current execution ownership. A
			// tombstoned worker, however, must show a genuine later provenance or
			// lease claim/renew event before current work makes it visible again.
			if workerVisible(owner, filter, activities[owner].at) {
				acc := get(owner)
				acc.worker.Active++
				point, exists := docActivities[owner]
				if !exists {
					// A manual assignment may not have a same-actor provenance entry.
					// Keep it visible as current work for a non-tombstoned Worker without
					// treating somebody else's assignment audit as activity that could
					// revive a deleted Worker.
					point = activeOwnerPoint(doc)
				}
				if !point.at.IsZero() && afterWorkerCutoff(point.at, workerBoundary(owner, filter)) {
					acc.addRecent(doc, point)
				}
			}
			continue
		}

		aggregate.Completed++
		info := lifecycleDetails(doc.Provenance, rules)
		if info.reopenedAfter(time.Time{}) {
			aggregate.Reopened++
		}
		if !info.created.IsZero() && !info.completed.IsZero() && !info.completed.Before(info.created) {
			aggregateCycleTotal += info.completed.Sub(info.created)
			aggregate.CycleSamples++
		}

		owner := taskOwner(doc.Task)
		// Closed work belongs to the actor recorded at the completion transition,
		// not a later/current reassignee. Fall back only for legacy files without a
		// usable transition audit entry.
		if info.completionActor != "" {
			owner = info.completionActor
		}
		owner = metricActor(owner)
		boundary := workerBoundary(owner, filter)
		// A post-reset/deletion report must only include a completion event that is
		// strictly newer than the individual boundary. Legacy closed files with no
		// completion audit remain countable only when the Worker has no boundary.
		completionAllowed := afterWorkerCutoff(info.completed, boundary)
		if info.completed.IsZero() {
			completionAllowed = boundary.IsZero()
		}
		if completionAllowed && workerVisible(owner, filter, activities[owner].at) {
			acc := get(owner)
			acc.worker.Completed++
			if task.ValidPriority(doc.Task.Priority) && doc.Task.Priority != "" {
				acc.worker.CompletedByPriority[doc.Task.Priority]++
			} else {
				acc.worker.CompletedByPriority["none"]++
			}
			if info.reopenedAfter(boundary) {
				acc.worker.Reopened++
			}
			if !info.created.IsZero() && !info.completed.IsZero() && !info.completed.Before(info.created) {
				start := info.created
				if !boundary.IsZero() && boundary.After(start) {
					start = boundary
				}
				if !info.completed.Before(start) {
					acc.cycleTotal += info.completed.Sub(start)
					acc.worker.CycleSamples++
				}
			}
		}
	}

	if aggregate.CycleSamples > 0 {
		aggregate.AverageCycleSeconds = (aggregateCycleTotal / time.Duration(aggregate.CycleSamples)).Seconds()
	}
	if aggregate.Completed > 0 {
		aggregate.ReopenRate = float64(aggregate.Reopened) / float64(aggregate.Completed)
	}

	workers := make([]Worker, 0, len(accs))
	for _, acc := range accs {
		if acc.worker.CycleSamples > 0 {
			acc.worker.AverageCycle = acc.cycleTotal / time.Duration(acc.worker.CycleSamples)
			acc.worker.AverageCycleSeconds = acc.worker.AverageCycle.Seconds()
		}
		if acc.worker.Completed > 0 {
			acc.worker.ReopenRate = float64(acc.worker.Reopened) / float64(acc.worker.Completed)
		}
		acc.worker.RecentWork = sortedRecentWork(acc.recent)
		workers = append(workers, acc.worker)
	}
	sort.Slice(workers, func(i, j int) bool { return workers[i].Actor < workers[j].Actor })
	return Report{Filter: filter, Workers: workers, Aggregate: aggregate}
}

// Activities derives each actor's latest qualifying task activity without exposing
// task content. It includes every valid provenance timestamp and active/expired lease
// acquired/renewed timestamps so a home registry can revive a deleted Worker only when
// there is durable, timestamped evidence after the tombstone.
func Activities(docs []*store.Doc) map[string]time.Time {
	points := make(map[string]activityPoint)
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		mergeActivityPoints(points, activitiesForDoc(doc))
	}
	out := make(map[string]time.Time, len(points))
	for actor, point := range points {
		out[actor] = point.at
	}
	return out
}

// ComputeStore reads config and tasks fresh, then applies Compute.
func ComputeStore(s *store.Store, filter Filter) (Report, error) {
	docs, err := s.ListDocs()
	if err != nil {
		return Report{}, err
	}
	cfg, err := s.Config()
	if err != nil {
		return Report{}, err
	}
	rules := task.Rules{Initial: cfg.Initial, Closed: cfg.Closed, States: cfg.States, Review: cfg.Review()}
	return Compute(docs, rules, filter), nil
}

func (a *accumulator) addRecent(doc *store.Doc, point activityPoint) {
	if point.at.IsZero() {
		return
	}
	item := RecentWork{
		TaskID: doc.Task.ID, Title: doc.Task.Title, Status: doc.Task.Status,
		ProjectID: doc.Task.ProjectID, Activity: point.activity,
		At: point.at.UTC().Format(time.RFC3339Nano),
	}
	existing, exists := a.recent[item.TaskID]
	if !exists || point.at.After(existing.at) || (point.at.Equal(existing.at) && recentLess(existing.item, item)) {
		a.recent[item.TaskID] = recentEntry{item: item, at: point.at}
	}
	if a.worker.LastActivity == nil || point.at.After(*a.worker.LastActivity) {
		at := point.at.UTC()
		a.worker.LastActivity = &at
	}
}

func sortedRecentWork(items map[string]recentEntry) []RecentWork {
	if len(items) == 0 {
		return nil
	}
	entries := make([]recentEntry, 0, len(items))
	for _, entry := range items {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].at.Equal(entries[j].at) {
			return entries[i].at.After(entries[j].at)
		}
		return recentLess(entries[i].item, entries[j].item)
	})
	if len(entries) > MaxRecentWork {
		entries = entries[:MaxRecentWork]
	}
	out := make([]RecentWork, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.item)
	}
	return out
}

func recentLess(left, right RecentWork) bool {
	if left.TaskID != right.TaskID {
		return left.TaskID < right.TaskID
	}
	if left.Activity != right.Activity {
		return left.Activity < right.Activity
	}
	return left.Title < right.Title
}

func activitiesForDoc(doc *store.Doc) map[string]activityPoint {
	points := map[string]activityPoint{}
	for index, event := range doc.Provenance {
		if event.Who == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, event.At)
		if err != nil {
			continue
		}
		mergeActivityPoint(points, event.Who, activityPoint{at: at.UTC(), activity: event.Did, order: index})
	}
	if doc.Task.Lease != nil && doc.Task.Lease.Holder != "" {
		lease := doc.Task.Lease
		at, label := leaseActivity(lease)
		if !at.IsZero() {
			mergeActivityPoint(points, lease.Holder, activityPoint{at: at, activity: label, order: len(doc.Provenance) + 1})
		}
	}
	return points
}

func activeOwnerPoint(doc *store.Doc) activityPoint {
	var latest activityPoint
	for index, event := range doc.Provenance {
		at, err := time.Parse(time.RFC3339, event.At)
		if err != nil {
			continue
		}
		candidate := activityPoint{at: at.UTC(), activity: "active", order: index}
		if latest.at.IsZero() || candidate.at.After(latest.at) || (candidate.at.Equal(latest.at) && candidate.order > latest.order) {
			latest = candidate
		}
	}
	return latest
}

func leaseActivity(lease *task.Lease) (time.Time, string) {
	if lease == nil {
		return time.Time{}, ""
	}
	if lease.RenewedAt != "" {
		if at, err := time.Parse(time.RFC3339, lease.RenewedAt); err == nil {
			return at.UTC(), "lease renewed"
		}
	}
	if lease.AcquiredAt != "" {
		if at, err := time.Parse(time.RFC3339, lease.AcquiredAt); err == nil {
			return at.UTC(), "lease claimed"
		}
	}
	return time.Time{}, ""
}

func mergeActivityPoints(destination, source map[string]activityPoint) {
	for actor, point := range source {
		mergeActivityPoint(destination, actor, point)
	}
}

func mergeActivityPoint(points map[string]activityPoint, actor string, candidate activityPoint) {
	if actor == "" || candidate.at.IsZero() {
		return
	}
	current, exists := points[actor]
	if !exists || candidate.at.After(current.at) || (candidate.at.Equal(current.at) && (candidate.order > current.order || (candidate.order == current.order && candidate.activity > current.activity))) {
		points[actor] = candidate
	}
}

func metricActor(actor string) string {
	if actor == "" {
		return "unassigned"
	}
	return actor
}

func workerBoundary(actor string, filter Filter) time.Time {
	cutoff, exists := filter.WorkerCutoffs[actor]
	if !exists {
		return time.Time{}
	}
	if cutoff.DeletedAt.After(cutoff.ResetAt) {
		return cutoff.DeletedAt
	}
	return cutoff.ResetAt
}

func workerVisible(actor string, filter Filter, activity time.Time) bool {
	cutoff, exists := filter.WorkerCutoffs[actor]
	if !exists || cutoff.DeletedAt.IsZero() {
		return true
	}
	return activity.After(cutoff.DeletedAt)
}

func afterWorkerCutoff(at, boundary time.Time) bool {
	if boundary.IsZero() {
		return true
	}
	return !at.IsZero() && at.After(boundary)
}

func matchProject(projectID string, filter Filter) bool {
	// Cluster-wide tasks intentionally participate in every cluster aggregation. They
	// have no project-specific membership but are shared work by definition.
	if filter.Scope == ScopeCluster && projectID == "" {
		return true
	}
	if filter.IncludeProject != nil {
		return filter.IncludeProject(projectID)
	}
	if filter.ProjectID != nil {
		return projectID == *filter.ProjectID
	}
	if filter.Scope == ScopeCluster && len(filter.ClusterProjects) > 0 {
		for _, id := range filter.ClusterProjects {
			if projectID == id {
				return true
			}
		}
		return false
	}
	return true
}

func taskOwner(t task.Task) string {
	if t.Lease != nil && t.Lease.Holder != "" {
		return t.Lease.Holder
	}
	return t.Assignee
}

// activeOwner is deliberately stricter than taskOwner: an active metric describes
// current execution ownership, so an expired (or malformed) lease contributes no
// active Worker even if its stale assignee field has not been cleared by Expire yet.
// Tasks without a lease retain the normal manually-assigned behavior.
func activeOwner(t task.Task, now time.Time) string {
	if t.Lease == nil {
		return t.Assignee
	}
	expires, err := time.Parse(time.RFC3339, t.Lease.ExpiresAt)
	if err != nil || !now.Before(expires) || t.Lease.Holder == "" {
		return ""
	}
	return t.Lease.Holder
}

// lifecycle reconstructs the latest completion cycle and whether a completed task was
// reopened at least once. It recognizes legacy/current transition wording and falls back
// to the first provenance timestamp for creation when old files lack an explicit created
// event. It remains for package compatibility; new callers needing cutoffs use details.
func lifecycle(events []store.Provenance, rules task.Rules) (created, completed time.Time, completionActor string, reopened bool) {
	info := lifecycleDetails(events, rules)
	return info.created, info.completed, info.completionActor, info.reopenedAfter(time.Time{})
}

func lifecycleDetails(events []store.Provenance, rules task.Rules) lifecycleInfo {
	type timedEvent struct {
		event store.Provenance
		at    time.Time
		index int
	}
	parsed := make([]timedEvent, 0, len(events))
	for index, event := range events {
		at, err := time.Parse(time.RFC3339, event.At)
		if err != nil {
			continue
		}
		parsed = append(parsed, timedEvent{event: event, at: at.UTC(), index: index})
	}
	sort.SliceStable(parsed, func(i, j int) bool {
		if !parsed[i].at.Equal(parsed[j].at) {
			return parsed[i].at.Before(parsed[j].at)
		}
		return parsed[i].index < parsed[j].index
	})

	var info lifecycleInfo
	wasClosed := false
	for _, timed := range parsed {
		event := timed.event
		if info.created.IsZero() || (event.Did == "created" && timed.at.Before(info.created)) {
			info.created = timed.at
		}
		if status, ok := transitionTarget(event.Did); ok {
			closed := rules.IsClosed(status)
			if wasClosed && !closed {
				info.reopenedAt = append(info.reopenedAt, timed.at)
			}
			if closed {
				info.completed, info.completionActor = timed.at, event.Who
			}
			wasClosed = closed
		}
	}
	return info
}

func (info lifecycleInfo) reopenedAfter(boundary time.Time) bool {
	for _, at := range info.reopenedAt {
		if boundary.IsZero() || at.After(boundary) {
			return true
		}
	}
	return false
}

func transitionTarget(did string) (string, bool) {
	for _, prefix := range []string{"transitioned to ", "bulk transitioned to "} {
		if strings.HasPrefix(did, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(did, prefix)), true
		}
	}
	return "", false
}
