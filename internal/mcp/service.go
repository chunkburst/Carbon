package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"carbon/internal/check"
	"carbon/internal/config"
	"carbon/internal/gitctx"
	"carbon/internal/lease"
	"carbon/internal/session"
	"carbon/internal/store"
	"carbon/internal/task"
	tasktypes "carbon/internal/types"
)

// ErrAlreadyClaimed is returned when a task is claimed by a different actor (SPEC §7).
var ErrAlreadyClaimed = errors.New("task already claimed by another actor")

// ErrProjectScope is returned when a project-bound client attempts an ordinary read
// outside its default project without explicitly opting into its own cluster. The
// store is already one cluster, so this is a least-privilege default rather than a
// cross-cluster authorization mechanism.
var ErrProjectScope = errors.New("task is outside the connection's default project scope")

// ErrStandaloneClusterScope rejects a cluster-only expansion on an isolated
// top-level project. Unlike a project inside a shared cluster, a standalone project
// has no sibling task pool to enumerate.
var ErrStandaloneClusterScope = errors.New("standalone project scope cannot include cluster tasks")

// ErrExecutionProjectRequired prevents commands, git probes, and sessions from ever
// using a Carbon cluster data directory as if it were source code. Cluster-wide tasks
// must name an execution project (or use a bound project connection) before they can
// run a command check or begin a session.
var ErrExecutionProjectRequired = errors.New("operation requires an execution project source path")

// ErrAssigneeLeaseRequired prevents a generic Carbon task edit from silently
// overwriting durable lease/approval state. Carbon ownership changes go through the
// lease reassignment endpoint, which enforces force/reason audit rules. Legacy callers
// retain the historic generic assignment edit for compatibility.
var ErrAssigneeLeaseRequired = errors.New("Carbon assignee changes require lease reassignment")

// ErrExpectedVersionsRequired makes Carbon batch writes genuinely optimistic: every
// task in a cluster batch must carry the version observed by the caller. Legacy batch
// callers retain their permissive historical behavior.
var ErrExpectedVersionsRequired = errors.New("Carbon bulk writes require expected versions for every task")

// ErrProjectWriteScope is returned when a project-bound Carbon connection attempts to
// mutate another project's task. Cluster-wide tasks remain shared by design; a
// cluster-bound connection (no ProjectID) can manage the full physical cluster.
var ErrProjectWriteScope = errors.New("task is outside the connection's writable project scope")

// ErrProjectMoveRequired prevents generic task PATCH from changing project_id in Carbon.
// Project moves must use the auditable batch/explicit move path with version and reason.
var ErrProjectMoveRequired = errors.New("Carbon project changes require the explicit move operation")

// ErrProjectBindingRequired prevents a Carbon cluster pool from silently accepting an
// unlabelled ordinary task. Project-bound connections receive their bound project by
// default; cluster-wide work must be an explicit empty project_id on an intentionally
// selected cluster scope.
var ErrProjectBindingRequired = errors.New("Carbon task creation requires a project binding or explicit cluster scope")

// ErrNotManual is returned when attesting a check that has a command — those are executed
// by the engine (RunChecks), never attested.
var ErrNotManual = errors.New("cannot attest a command check; it is run by the engine")

// ErrEvidenceNotFound keeps evidence deletion/replacement failures distinct from a
// task lookup. A caller must use an existing durable evidence id; new evidence ids are
// minted by the service so creation audit metadata cannot be forged.
var ErrEvidenceNotFound = errors.New("task evidence not found")

// ErrExpectedVersionRequired is used by the dedicated blocker/evidence methods. Their
// purpose is to provide a narrow optimistic-concurrency surface, unlike the retained
// legacy generic Update method which intentionally permits an omitted precondition.
var ErrExpectedVersionRequired = errors.New("expected version is required")

// Service implements task and session tools as thin orchestration over store + task +
// check. Gate logic is never reimplemented here — it calls task.Ready / task.CanTransition.
// Identity (actor) is fixed at construction, not passed per call; every write stamps a
// provenance entry with it.
type Service struct {
	store       *store.Store
	actor       string
	client      string
	scope       Scope
	projectRoot ProjectRootResolver
	now         func() time.Time
}

// NewService binds the verbs to a store and an actor identity. now is injectable for
// deterministic provenance timestamps; nil uses the wall clock.
func NewService(s *store.Store, actor string, now func() time.Time) *Service {
	return NewScopedServiceWithClient(s, actor, "", Scope{Legacy: true}, now)
}

// NewServiceWithClient binds the verbs to a store, actor, and declared client identity.
func NewServiceWithClient(s *store.Store, actor, client string, now func() time.Time) *Service {
	return NewScopedServiceWithClient(s, actor, client, Scope{Legacy: true}, now)
}

// NewScopedService binds a service to a resolved Carbon or legacy scope. Callers use
// the legacy constructors above for the established project-local behaviour.
func NewScopedService(s *store.Store, actor string, scope Scope, now func() time.Time) *Service {
	return NewScopedServiceWithClientAndResolver(s, actor, "", scope, nil, now)
}

// NewScopedServiceWithClient is NewScopedService with a declared MCP client identity.
// Scope resolution belongs to the HTTP/CLI adapters; this service only enforces the
// resulting project boundary around task operations.
func NewScopedServiceWithClient(s *store.Store, actor, client string, scope Scope, now func() time.Time) *Service {
	return NewScopedServiceWithClientAndResolver(s, actor, client, scope, nil, now)
}

// NewScopedServiceWithClientAndResolver supplies a stable-id-to-source resolver for
// Carbon operations that need a real source checkout. It intentionally stays optional
// so existing legacy callers and tests keep their historical constructors.
func NewScopedServiceWithClientAndResolver(s *store.Store, actor, client string, scope Scope, projectRoot ProjectRootResolver, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: s, actor: actor, client: client, scope: scope, projectRoot: projectRoot, now: now}
}

// Scope returns the resolved connection boundary. It is safe to expose in identity
// and status responses because it contains only paths/opaque IDs supplied by the
// local caller, never credentials.
func (svc *Service) Scope() Scope { return svc.scope }

func rulesOf(c config.Config) task.Rules {
	return task.Rules{Initial: c.Initial, Closed: c.Closed, States: c.States, Review: c.Review()}
}

// depResolver fetches a single task by id for the deps gate, reading just that file instead
// of scanning the whole board. A missing/unreadable dep resolves to not-found, which the gate
// treats as "not closed" — matching the loaded-map behaviour without the full List() cost.
func (svc *Service) depResolver() task.DepResolver {
	return func(id string) (task.Task, bool) {
		d, err := svc.store.Get(id)
		if err != nil {
			return task.Task{}, false
		}
		return d.Task, true
	}
}

// TaskView is a task plus derived fields: readiness (SPEC §4: computed, not stored) and the
// last-activity timestamp (newest provenance entry) for "updated X ago" displays.
type TaskView struct {
	task.Task
	Ready          bool   `json:"ready"`
	UpdatedAt      string `json:"updatedAt,omitempty"`
	ExecutionState string `json:"executionState,omitempty"`
	SessionID      string `json:"sessionId,omitempty"`
}

// List returns tasks, optionally filtered by status, assignee, and readiness. A nil
// ready pointer means "don't filter on readiness". list(ready=true, status=initial) is
// the agent's "what can I start now" query (SPEC §7).
func (svc *Service) List(status, assignee string, ready *bool) ([]TaskView, error) {
	return svc.ListScoped(status, assignee, ready, "", false)
}

// ListWithExecution adds an optional derived execution-state filter.
func (svc *Service) ListWithExecution(status, assignee string, ready *bool, execution string) ([]TaskView, error) {
	return svc.ListScoped(status, assignee, ready, execution, false)
}

// ListScoped applies the connection's default project filter unless includeCluster is
// explicitly true. The physical Store belongs to exactly one cluster in Carbon mode,
// so opting in cannot expose another cluster. Legacy stores keep their historical
// unfiltered behaviour.
func (svc *Service) ListScoped(status, assignee string, ready *bool, execution string, includeCluster bool) ([]TaskView, error) {
	if err := svc.validateIncludeCluster(includeCluster); err != nil {
		return nil, err
	}
	docs, err := svc.store.ListDocs()
	if err != nil {
		return nil, err
	}
	cfg, err := svc.store.Config()
	if err != nil {
		return nil, err
	}
	rules := rulesOf(cfg)
	sessionDocs, err := svc.store.ListSessions()
	if err != nil {
		return nil, err
	}
	latestSession := make(map[string]*store.SessionDoc)
	for _, d := range sessionDocs {
		if latestSession[d.Session.TaskID] == nil {
			latestSession[d.Session.TaskID] = d
		}
	}

	all := make(map[string]task.Task, len(docs))
	for _, d := range docs {
		all[d.Task.ID] = d.Task
	}

	var out []TaskView
	for _, d := range docs {
		t := d.Task
		if !svc.readAllowed(t, includeCluster) {
			continue
		}
		if status != "" && t.Status != status {
			continue
		}
		if assignee != "" && t.Assignee != assignee {
			continue
		}
		r := task.Ready(t, all, rules)
		if ready != nil && *ready != r {
			continue
		}
		executionState, sessionID, err := svc.executionFor(t, latestSession[t.ID], cfg)
		if err != nil {
			return nil, err
		}
		if execution != "" && execution != executionState {
			continue
		}
		out = append(out, TaskView{Task: t, Ready: r, UpdatedAt: lastActivity(d), ExecutionState: executionState, SessionID: sessionID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (svc *Service) executionFor(t task.Task, d *store.SessionDoc, cfg config.Config) (string, string, error) {
	if d == nil || d.Session.AttemptID != t.ActiveAttempt {
		return "", "", nil
	}
	switch d.Session.Status {
	case session.StatusActive:
		live, err := svc.readLiveForTask(d.Session.ID, t)
		if err != nil {
			return "", "", err
		}
		health := session.DeriveHealth(d.Session, live, svc.now(), cfg.SessionStaleDuration())
		if health == session.HealthStalled {
			return ExecutionStalled, d.Session.ID, nil
		}
		return ExecutionActive, d.Session.ID, nil
	case session.StatusFinished:
		if !slices.Contains(cfg.Closed, t.Status) {
			return ExecutionAwaitingReview, d.Session.ID, nil
		}
	}
	return "", d.Session.ID, nil
}

// lastActivity returns the timestamp of the newest provenance entry, or "" if none.
func lastActivity(d *store.Doc) string {
	if n := len(d.Provenance); n > 0 {
		return d.Provenance[n-1].At
	}
	return ""
}

// Get returns the full task: typed fields, checks (+results), provenance, and body.
func (svc *Service) Get(id string) (*store.Doc, error) {
	return svc.GetScoped(id, false)
}

// GetScoped returns a task when it is visible in the default project or the caller
// explicitly requested a same-cluster read. This API makes least privilege explicit
// without treating project ids as separate task pools.
func (svc *Service) GetScoped(id string, includeCluster bool) (*store.Doc, error) {
	if err := svc.validateIncludeCluster(includeCluster); err != nil {
		return nil, err
	}
	doc, err := svc.store.Get(id)
	if err != nil {
		return nil, err
	}
	if !svc.readAllowed(doc.Task, includeCluster) {
		return nil, fmt.Errorf("%w: %s", ErrProjectScope, id)
	}
	return doc, nil
}

func (svc *Service) readAllowed(t task.Task, includeCluster bool) bool {
	if !svc.scope.IsCarbon() || svc.scope.ProjectID == "" {
		return true
	}
	if includeCluster && !svc.scope.IsStandalone() {
		return true
	}
	return t.ProjectID == svc.scope.ProjectID
}

// validateIncludeCluster keeps the shared-store expansion opt-in unavailable on an
// isolated standalone root. Calling it at every public read method also protects
// direct in-process callers that do not pass through the HTTP adapter.
func (svc *Service) validateIncludeCluster(includeCluster bool) error {
	if includeCluster && svc.scope.IsStandalone() {
		return ErrStandaloneClusterScope
	}
	return nil
}

func (svc *Service) writeAllowed(t task.Task) error {
	if !svc.scope.IsCarbon() || svc.scope.ProjectID == "" {
		return nil
	}
	if svc.scope.IsStandalone() {
		if t.ProjectID == svc.scope.ProjectID {
			return nil
		}
		return fmt.Errorf("%w: task project %s, bound standalone project %s", ErrProjectWriteScope, t.ProjectID, svc.scope.ProjectID)
	}
	// Cluster-wide tasks are explicitly shared work in this cluster. They are writable
	// from each bound project, while another concrete project's tasks are not.
	if t.ProjectID == "" || t.ProjectID == svc.scope.ProjectID {
		return nil
	}
	return fmt.Errorf("%w: task project %s, bound project %s", ErrProjectWriteScope, t.ProjectID, svc.scope.ProjectID)
}

func (svc *Service) writableTask(id string) (*store.Doc, error) {
	doc, err := svc.store.Get(id)
	if err != nil {
		return nil, err
	}
	if err := svc.writeAllowed(doc.Task); err != nil {
		return nil, err
	}
	return doc, nil
}

func (svc *Service) writableTasks(ids []string) error {
	for _, id := range ids {
		if _, err := svc.writableTask(id); err != nil {
			return err
		}
	}
	return nil
}

// authorizeProjectMove validates an intentional write from a bound project/shared task
// to another concrete project. validateProject resolves that target through the trusted
// same-cluster resolver; force+reason makes the cross-project audit explicit.
func (svc *Service) authorizeProjectMove(projectID string, force bool, reason string) error {
	if svc.scope.IsStandalone() && projectID != svc.scope.ProjectID {
		return fmt.Errorf("%w: standalone project cannot move tasks to %q", ErrProjectWriteScope, projectID)
	}
	if !svc.scope.IsCarbon() || svc.scope.ProjectID == "" || projectID == "" || projectID == svc.scope.ProjectID {
		return nil
	}
	if !force || strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: cross-project move requires force=true and reason", ErrProjectWriteScope)
	}
	return svc.validateProject(projectID)
}

// sourceRoot returns the actual source directory for a task. It is intentionally
// never Store.Root() in Carbon mode: Store.Root is the shared data directory.
func (svc *Service) sourceRoot(t task.Task) (string, error) {
	if !svc.scope.IsCarbon() {
		return svc.store.Root(), nil
	}
	projectID := t.ProjectID
	if projectID == "" {
		projectID = svc.scope.ProjectID
	}
	if projectID == "" {
		return "", ErrExecutionProjectRequired
	}
	if projectID == svc.scope.ProjectID && svc.scope.SourcePath != "" {
		return checkedSourceRoot(svc.scope.SourcePath, projectID)
	}
	if svc.projectRoot == nil {
		return "", fmt.Errorf("%w: project %s is not bound to a source resolver", ErrExecutionProjectRequired, projectID)
	}
	root, err := svc.projectRoot(projectID)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrExecutionProjectRequired, err)
	}
	return checkedSourceRoot(root, projectID)
}

func checkedSourceRoot(root, projectID string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("%w: project %s has no source path", ErrExecutionProjectRequired, projectID)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: project %s source is offline", ErrExecutionProjectRequired, projectID)
	}
	return root, nil
}

func (svc *Service) validateProject(projectID string) error {
	if !svc.scope.IsCarbon() || projectID == "" || projectID == svc.scope.ProjectID {
		return nil
	}
	if svc.scope.IsStandalone() {
		return fmt.Errorf("%w: standalone project cannot target project %s", ErrProjectScope, projectID)
	}
	if svc.projectRoot == nil {
		return fmt.Errorf("%w: project %s cannot be resolved in this cluster", ErrProjectScope, projectID)
	}
	if _, err := svc.projectRoot(projectID); err != nil {
		return fmt.Errorf("%w: %v", ErrProjectScope, err)
	}
	return nil
}

// Create mints a new task in the initial state. Deps must already exist, or the graph
// would be born dangling (SPEC §4).
func (svc *Service) Create(d store.Draft) (*store.Doc, error) {
	return svc.CreateContext(context.Background(), d)
}

// CreateContext uses strict Carbon creation when bound to a Carbon cluster. Legacy
// constructors retain the established permissive Draft semantics for compatibility.
func (svc *Service) CreateContext(ctx context.Context, d store.Draft) (*store.Doc, error) {
	if strings.TrimSpace(d.Title) == "" {
		return nil, ErrEmptyTitle
	}
	if len(d.Evidence) > 0 {
		evidence, err := svc.normalizeEvidence(nil, d.Evidence)
		if err != nil {
			return nil, err
		}
		d.Evidence = evidence
	}
	if !task.ValidPriority(d.Priority) {
		return nil, fmt.Errorf("%w: %q", task.ErrInvalidPriority, d.Priority)
	}
	if len(d.Deps) > 0 || d.Parent != "" {
		all, err := svc.store.List()
		if err != nil {
			return nil, err
		}
		for _, dep := range d.Deps {
			if _, ok := all[dep]; !ok {
				return nil, fmt.Errorf("%w: %s", task.ErrDanglingDep, dep)
			}
		}
		if d.Parent != "" {
			if _, ok := all[d.Parent]; !ok {
				return nil, fmt.Errorf("%w: %s", task.ErrParentMissing, d.Parent)
			}
		}
	}
	if svc.scope.IsCarbon() {
		if d.ProjectID == "" && !d.ProjectIDSet {
			if svc.scope.ProjectID == "" {
				return nil, ErrProjectBindingRequired
			}
			d.ProjectID = svc.scope.ProjectID
			d.ProjectIDSet = true
		}
		if d.ProjectID == "" && d.ProjectIDSet && !svc.scope.IsExplicitCluster() {
			return nil, ErrProjectBindingRequired
		}
		if svc.scope.ProjectID != "" && d.ProjectID != "" && d.ProjectID != svc.scope.ProjectID {
			return nil, fmt.Errorf("%w: create target %s", ErrProjectWriteScope, d.ProjectID)
		}
		if err := svc.validateProject(d.ProjectID); err != nil {
			return nil, err
		}
		return svc.store.CreateExplicit(ctx, svc.actor, store.ExplicitDraft{
			Title: d.Title, Body: d.Body, BlockerReason: d.BlockerReason, Evidence: d.Evidence, Deps: d.Deps, Checks: d.Checks, Labels: d.Labels,
			Priority: d.Priority, Parent: d.Parent, Rank: d.Rank, ProjectID: d.ProjectID,
			ClusterWide: d.ProjectID == "" && d.ProjectIDSet, Type: d.Type, Importance: d.Importance,
		})
	}
	return svc.store.Create(d, svc.actor, svc.now())
}

// ListTaskTypes returns built-ins plus configured custom types. The catalog itself
// enforces the finite custom-type limit; callers should prefer existing primitives
// and create a custom type only when no standard workflow type fits.
func (svc *Service) ListTaskTypes() ([]string, []tasktypes.Definition, error) {
	cfg, err := svc.store.Config()
	if err != nil {
		return nil, nil, err
	}
	catalog := cfg.TypeCatalog()
	return catalog.Keys(), catalog.Custom, nil
}

// CreateTaskType persists one rate-limited, quota-limited custom type. It is explicit
// rather than inferred from task creation so agents do not accidentally proliferate
// one-off types.
func (svc *Service) CreateTaskType(ctx context.Context, key string) (tasktypes.Definition, error) {
	if svc.scope.IsCarbon() && !svc.scope.IsStandalone() && svc.scope.ProjectID != "" {
		return tasktypes.Definition{}, fmt.Errorf("%w: custom task types are cluster configuration", ErrProjectWriteScope)
	}
	return svc.store.CreateTaskType(ctx, svc.actor, key, svc.now())
}

// ErrEmptyTitle is returned when an edit would set a task's title to blank.
var ErrEmptyTitle = errors.New("title cannot be empty")

// UpdateFields are the fields editable after create. A nil pointer leaves a field unchanged;
// a non-nil pointer sets it (empty clears, where clearing is meaningful). Title/Body/Checks
// edit the task's content; Priority/Labels/Deps/Parent are the organization fields.
type UpdateFields struct {
	Priority      *string
	Labels        *[]string
	Deps          *[]string
	Parent        *string
	Title         *string
	Body          *string
	Checks        *[]task.Check
	ProjectID     *string
	Type          *string
	Importance    *string
	Assignee      *string
	BlockerReason *string
	Evidence      *[]task.Evidence
}

// Update edits a task's content and organization fields (SPEC §7-style write; appends one
// provenance entry). Parent changes are validated to exist and not create a cycle; a Title,
// when provided, must be non-empty.
func (svc *Service) Update(id string, f UpdateFields) (*store.Doc, error) {
	return svc.UpdateWithVersion(id, f, "")
}

// UpdateWithVersion applies an optional optimistic Version/ETag precondition. The
// legacy Update method stays unconditional for existing tool wrappers.
func (svc *Service) UpdateWithVersion(id string, f UpdateFields, expectedVersion string) (*store.Doc, error) {
	doc, err := svc.writableTask(id)
	if err != nil {
		return nil, err
	}
	if err := doc.MatchVersion(expectedVersion); err != nil {
		return nil, err
	}
	if f.Priority == nil && f.Labels == nil && f.Deps == nil && f.Parent == nil && f.Title == nil && f.Body == nil && f.Checks == nil && f.ProjectID == nil && f.Type == nil && f.Importance == nil && f.Assignee == nil && f.BlockerReason == nil && f.Evidence == nil {
		return doc, nil // nothing to change — don't write a spurious provenance entry
	}
	if f.Assignee != nil && svc.scope.IsCarbon() {
		return nil, ErrAssigneeLeaseRequired
	}
	if f.ProjectID != nil && svc.scope.IsCarbon() {
		return nil, ErrProjectMoveRequired
	}
	if f.Priority != nil && !task.ValidPriority(*f.Priority) {
		return nil, fmt.Errorf("%w: %q", task.ErrInvalidPriority, *f.Priority)
	}
	if f.Title != nil && strings.TrimSpace(*f.Title) == "" {
		return nil, ErrEmptyTitle
	}
	if f.Importance != nil && !task.ValidImportance(*f.Importance) {
		return nil, fmt.Errorf("%w: %q", task.ErrInvalidImportance, *f.Importance)
	}
	if f.Type != nil {
		cfg, err := svc.store.Config()
		if err != nil {
			return nil, err
		}
		if !cfg.TypeCatalog().Allowed(*f.Type) {
			return nil, fmt.Errorf("%w: %q", task.ErrInvalidType, *f.Type)
		}
	}
	if f.ProjectID != nil {
		if err := svc.validateProject(*f.ProjectID); err != nil {
			return nil, err
		}
	}
	var evidence []task.Evidence
	if f.Evidence != nil {
		evidence, err = svc.normalizeEvidence(doc.Task.Evidence, *f.Evidence)
		if err != nil {
			return nil, err
		}
	}
	if f.Deps != nil || f.Parent != nil {
		all, err := svc.store.List()
		if err != nil {
			return nil, err
		}
		candidate, ok := all[id]
		if !ok {
			return nil, fmt.Errorf("%w: %s", store.ErrNotFound, id)
		}
		if f.Deps != nil {
			candidate.Deps = slices.Clone(*f.Deps)
		}
		if f.Parent != nil {
			if *f.Parent != "" {
				if _, ok := all[*f.Parent]; !ok {
					return nil, fmt.Errorf("%w: %s", task.ErrParentMissing, *f.Parent)
				}
			}
			candidate.Parent = *f.Parent
		}
		all[id] = candidate
		if f.Deps != nil {
			if err := task.ValidateDeps(all); err != nil {
				return nil, err
			}
		}
		if f.Parent != nil {
			if err := task.ValidateParents(all); err != nil {
				return nil, err
			}
		}
	}
	if f.Priority != nil {
		doc.SetPriority(*f.Priority)
	}
	if f.Labels != nil {
		doc.SetLabels(*f.Labels)
	}
	if f.Deps != nil {
		doc.SetDeps(*f.Deps)
	}
	if f.Parent != nil {
		doc.SetParent(*f.Parent)
	}
	if f.Title != nil {
		doc.SetTitle(*f.Title)
	}
	if f.Body != nil {
		doc.SetBody(*f.Body)
	}
	if f.Checks != nil {
		doc.SetChecks(*f.Checks)
	}
	if f.ProjectID != nil {
		doc.SetProjectID(*f.ProjectID)
	}
	if f.Type != nil {
		doc.SetType(*f.Type)
	}
	if f.Importance != nil {
		doc.SetImportance(*f.Importance)
	}
	if f.Assignee != nil {
		doc.SetAssignee(*f.Assignee)
	}
	if f.BlockerReason != nil {
		if err := doc.SetBlockerReason(*f.BlockerReason); err != nil {
			return nil, err
		}
	}
	if f.Evidence != nil {
		if err := doc.SetEvidence(evidence); err != nil {
			return nil, err
		}
	}
	doc.AppendProvenance(svc.actor, "updated", "", svc.now())
	if err := svc.store.SaveIfVersion(doc, expectedVersion); err != nil {
		return nil, err
	}
	return doc, nil
}

// SetBlockerWithVersion updates or clears a task's blocked-state explanation with an
// optimistic version check. The reason intentionally survives a later state transition
// until a caller explicitly changes it.
func (svc *Service) SetBlockerWithVersion(id, reason, expectedVersion string) (*store.Doc, error) {
	if strings.TrimSpace(expectedVersion) == "" {
		return nil, ErrExpectedVersionRequired
	}
	return svc.UpdateWithVersion(id, UpdateFields{BlockerReason: &reason}, expectedVersion)
}

// AddEvidenceWithVersion appends one evidence item. Creation audit fields and the
// evidence ID are always service-owned; callers may only choose its visible payload.
func (svc *Service) AddEvidenceWithVersion(id string, evidence task.Evidence, expectedVersion string) (*store.Doc, error) {
	if strings.TrimSpace(expectedVersion) == "" {
		return nil, ErrExpectedVersionRequired
	}
	doc, err := svc.writableTask(id)
	if err != nil {
		return nil, err
	}
	if err := doc.MatchVersion(expectedVersion); err != nil {
		return nil, err
	}
	items := append(slices.Clone(doc.Task.Evidence), evidence)
	return svc.UpdateWithVersion(id, UpdateFields{Evidence: &items}, expectedVersion)
}

// RemoveEvidenceWithVersion deletes one evidence item by its durable ID. The remaining
// items retain their original audit fields through UpdateWithVersion's normalization.
func (svc *Service) RemoveEvidenceWithVersion(id, evidenceID, expectedVersion string) (*store.Doc, error) {
	if strings.TrimSpace(expectedVersion) == "" {
		return nil, ErrExpectedVersionRequired
	}
	doc, err := svc.writableTask(id)
	if err != nil {
		return nil, err
	}
	if err := doc.MatchVersion(expectedVersion); err != nil {
		return nil, err
	}
	items := make([]task.Evidence, 0, len(doc.Task.Evidence))
	found := false
	for _, item := range doc.Task.Evidence {
		if item.ID == evidenceID {
			found = true
			continue
		}
		items = append(items, item)
	}
	if !found {
		return nil, fmt.Errorf("%w: %s", ErrEvidenceNotFound, evidenceID)
	}
	return svc.UpdateWithVersion(id, UpdateFields{Evidence: &items}, expectedVersion)
}

// normalizeEvidence preserves creation provenance for retained ids and stamps every
// new entry. Unknown non-empty ids are rejected so a client cannot select its own audit
// identity or make a later replacement look like a historical entry.
func (svc *Service) normalizeEvidence(existing, incoming []task.Evidence) ([]task.Evidence, error) {
	byID := make(map[string]task.Evidence, len(existing))
	for _, item := range existing {
		if item.ID != "" {
			byID[item.ID] = item
		}
	}
	now := svc.now().UTC().Format(time.RFC3339)
	out := make([]task.Evidence, 0, len(incoming))
	for _, item := range incoming {
		item.Kind = strings.TrimSpace(item.Kind)
		item.Value = strings.TrimSpace(item.Value)
		item.Label = strings.TrimSpace(item.Label)
		item.URL = strings.TrimSpace(item.URL)
		if item.ID == "" {
			id, err := mintEvidenceID()
			if err != nil {
				return nil, err
			}
			item.ID = id
			item.CreatedAt = now
			item.CreatedBy = svc.actor
		} else if previous, ok := byID[item.ID]; ok {
			item.CreatedAt = previous.CreatedAt
			item.CreatedBy = previous.CreatedBy
		} else {
			return nil, fmt.Errorf("%w: %s", ErrEvidenceNotFound, item.ID)
		}
		out = append(out, item)
	}
	if err := task.ValidateEvidence(out); err != nil {
		return nil, err
	}
	return out, nil
}

func mintEvidenceID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate evidence id: %w", err)
	}
	return "e_" + hex.EncodeToString(raw[:]), nil
}

// Delete removes a task. It refuses when other tasks reference it (children via parent,
// dependents via deps); the caller must reparent/remove those first.
func (svc *Service) Delete(id string) error {
	_, err := svc.TrashTask(context.Background(), id, "deleted via MCP", "")
	return err
}

// Reorder sets a task's board ordering rank. Reordering is cosmetic, so it deliberately
// does NOT append a provenance entry (keeps the activity log meaningful).
func (svc *Service) Reorder(id string, rank float64) (*store.Doc, error) {
	doc, err := svc.writableTask(id)
	if err != nil {
		return nil, err
	}
	doc.SetRank(rank)
	if err := svc.store.Save(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// Claim sets the assignee to this actor. Re-claiming one's own task is a no-op; claiming
// a task held by someone else fails (SPEC §7).
func (svc *Service) Claim(id string) (*store.Doc, error) {
	result, err := svc.ClaimLease(context.Background(), LeaseClaimInput{TaskID: id})
	if errors.Is(err, lease.ErrApprovalPending) {
		return nil, fmt.Errorf("%w: %v", ErrAlreadyClaimed, err)
	}
	return result.Doc, err
}

// Transition applies the two gates (SPEC §5). When closing is blocked solely because
// checks have not passed, it auto-runs the checks and retries — refusing only if they
// still don't pass. Deps-gate and unknown-state failures are returned without side effects.
func (svc *Service) Transition(id, to string) (*store.Doc, error) {
	return svc.TransitionContext(context.Background(), id, to)
}

// TransitionContext is Transition with caller-driven cancellation for command checks.
func (svc *Service) TransitionContext(ctx context.Context, id, to string) (*store.Doc, error) {
	doc, err := svc.writableTask(id)
	if err != nil {
		return nil, err
	}
	cfg, err := svc.store.Config()
	if err != nil {
		return nil, err
	}
	rules := rulesOf(cfg)
	// The deps gate needs only this task's listed deps, so resolve them on demand rather than
	// scanning and re-validating the entire board on every status change.
	deps := svc.depResolver()

	// Report deps/unknown-state failures before touching checks (deps are reported first).
	gateErr := task.CanTransitionFunc(doc.Task, to, deps, rules)
	if gateErr != nil && !errors.Is(gateErr, task.ErrChecksNotPassed) {
		return nil, gateErr
	}

	// Verification-asserting transitions (entering the review state or a closed state) ALWAYS
	// re-run the command checks fresh and never trust a previously-recorded pass — a stored
	// result can be stale relative to the current code. Other transitions commit directly.
	gated := rules.IsClosed(to) || (rules.Review != "" && to == rules.Review)
	if !gated {
		return svc.commitTransition(doc, to) // gateErr is nil: only closed/review gate checks
	}

	if err := svc.runCmdChecks(ctx, doc, cfg, nil); err != nil {
		return nil, err
	}
	doc.AppendProvenance(svc.actor, "ran checks", "", svc.now())

	if again := task.CanTransitionFunc(doc.Task, to, deps, rules); again != nil {
		if saveErr := svc.store.Save(doc); saveErr != nil { // persist the recorded results
			return nil, saveErr
		}
		return doc, again
	}
	doc.SetStatus(to)
	doc.AppendProvenance(svc.actor, "transitioned to "+to, "", svc.now())
	if err := svc.store.Save(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func (svc *Service) commitTransition(doc *store.Doc, to string) (*store.Doc, error) {
	doc.SetStatus(to)
	doc.AppendProvenance(svc.actor, "transitioned to "+to, "", svc.now())
	if err := svc.store.Save(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// RunChecks runs the cmd checks (all by default, or the indices in `only`) and writes
// their results. Manual checks have no cmd and are skipped (SPEC §6, §7).
func (svc *Service) RunChecks(id string, only []int) (*store.Doc, error) {
	return svc.RunChecksContext(context.Background(), id, only)
}

// RunChecksContext is RunChecks with caller-driven cancellation for command checks.
func (svc *Service) RunChecksContext(ctx context.Context, id string, only []int) (*store.Doc, error) {
	doc, err := svc.writableTask(id)
	if err != nil {
		return nil, err
	}
	cfg, err := svc.store.Config()
	if err != nil {
		return nil, err
	}
	var filter map[int]bool
	if len(only) > 0 {
		filter = make(map[int]bool, len(only))
		for _, i := range only {
			filter[i] = true
		}
	}
	if err := svc.runCmdChecks(ctx, doc, cfg, filter); err != nil {
		return nil, err
	}
	doc.AppendProvenance(svc.actor, "ran checks", "", svc.now())
	if err := svc.store.Save(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// runCmdChecks executes each cmd check (optionally filtered) and records pass/fail on the
// doc. It mutates but does not save.
func (svc *Service) runCmdChecks(ctx context.Context, doc *store.Doc, cfg config.Config, only map[int]bool) error {
	root, err := svc.sourceRoot(doc.Task)
	if err != nil {
		return err
	}
	gitHead := ""
	if ref, err := gitctx.Current(ctx, root); err == nil {
		gitHead = ref.Head
	}
	// Commands execute only from the task's project source, while Carbon run logs
	// belong to the trusted cluster data store. Runner keeps these containment
	// boundaries separate; in legacy mode store.Root() and root are the same path.
	// Command execution deliberately remains outside Store.Write, but final run-log
	// publication shares the same repository lock as a project-data clear. Re-read the
	// task and require its original version inside that short lock: a clear (or another
	// task mutation) that won the race makes the check discard its diagnostic rather
	// than recreating/deleting a run file in a swapped collection.
	expectedVersion := doc.Version()
	runner := check.Runner{
		Root: root, LogRoot: svc.store.Root(), LogDir: svc.store.RunsDir(), Now: svc.now, Shell: cfg.CheckShell, GitHead: gitHead,
		LogWriteLock: func(writeLog func() error) error {
			return svc.store.Write(ctx, svc.actor, "publish check run log", func(tx *store.WriteTx) error {
				current, err := tx.GetTask(doc.Task.ID)
				if err != nil {
					return err
				}
				if err := current.MatchVersion(expectedVersion); err != nil {
					return err
				}
				return writeLog()
			})
		},
	}
	for i, c := range doc.Task.Checks {
		if only != nil && !only[i] {
			continue
		}
		if c.Cmd == "" {
			continue // manual check: result set by attestation, not execution
		}
		res, err := runner.RunContext(ctx, doc.Task.ID, check.Spec{Cmd: c.Cmd, Cwd: c.Cwd, Timeout: cfg.CheckTimeout(c.Timeout)})
		if err != nil {
			return err
		}
		result := "fail"
		if res.Pass {
			result = "pass"
		}
		if err := doc.SetCheckResult(i, result); err != nil {
			return err
		}
	}
	return nil
}

// Note appends a free-text provenance entry (SPEC §7).
// Attest sets a manual check's result (SPEC §6: a check with no command is set by
// attestation, not execution). It refuses checks that have a command and out-of-range
// indices. pass=false records a failed attestation.
func (svc *Service) Attest(id string, index int, pass bool) (*store.Doc, error) {
	doc, err := svc.writableTask(id)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(doc.Task.Checks) {
		return nil, fmt.Errorf("attest: check index %d out of range", index)
	}
	if doc.Task.Checks[index].Cmd != "" {
		return nil, ErrNotManual
	}
	result := "fail"
	if pass {
		result = "pass"
	}
	if err := doc.SetCheckResult(index, result); err != nil {
		return nil, err
	}
	doc.AppendProvenance(svc.actor, "attested", fmt.Sprintf("check %d %s", index, result), svc.now())
	if err := svc.store.Save(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func (svc *Service) Note(id, text string) (*store.Doc, error) {
	doc, err := svc.writableTask(id)
	if err != nil {
		return nil, err
	}
	doc.AppendProvenance(svc.actor, "note", text, svc.now())
	if err := svc.store.Save(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// EditNote edits a note's text in place and marks it editedAt. Anyone may edit any note;
// only note entries are editable. Address by note id (preferred) or, for a legacy note with
// no id, by 0-based provenance index (pass noteID=="").
func (svc *Service) EditNote(id, noteID string, index int, text string) (*store.Doc, error) {
	doc, err := svc.writableTask(id)
	if err != nil {
		return nil, err
	}
	if err := doc.EditNote(noteID, index, text, svc.now()); err != nil {
		return nil, err
	}
	if err := svc.store.Save(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// DeleteNote removes a note. Anyone may delete any note; only note entries are deletable.
// Address by note id (preferred) or, for a legacy note with no id, by 0-based provenance
// index (pass noteID==""). No provenance entry is appended; the deletion leaves no trace.
func (svc *Service) DeleteNote(id, noteID string, index int) (*store.Doc, error) {
	doc, err := svc.writableTask(id)
	if err != nil {
		return nil, err
	}
	if err := doc.DeleteNote(noteID, index); err != nil {
		return nil, err
	}
	if err := svc.store.Save(doc); err != nil {
		return nil, err
	}
	return doc, nil
}
