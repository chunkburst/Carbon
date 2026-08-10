package mcp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"carbon/internal/home"
	"carbon/internal/store"
	"carbon/internal/worklog"
)

var (
	// ErrWorkLogScopeRequired prevents a legacy project-local connection from creating
	// a record whose durable Carbon home/store boundary cannot be established.
	ErrWorkLogScopeRequired = errors.New("work logs require a Carbon home and selected project or cluster scope")
	// ErrWorkLogNotVisible intentionally covers both a missing visibility grant and a
	// record whose ClusterID does not match the selected physical cluster. HTTP maps it
	// to not-found so ordinary Workers cannot probe another Worker's private history.
	ErrWorkLogNotVisible = errors.New("work log is not visible in this scope")
	// ErrWorkLogOwnerRequired prevents one Worker from mutating another Worker's log.
	// Human UI actors may inspect every record, but reading does not grant edit rights.
	ErrWorkLogOwnerRequired = errors.New("only the owning Worker can modify this work log")
	// ErrWorkLogProjectScope rejects a project-scoped mutation that names another
	// project's log/task. It is separate from task scope errors so transports can return
	// an unambiguous permission response.
	ErrWorkLogProjectScope = errors.New("work log is outside the connection's project scope")
	// ErrWorkLogClusterScope prevents a read from another cluster (for example a global
	// Home-visible log) from being used as a write through the current cluster connection.
	ErrWorkLogClusterScope = errors.New("work log is outside the connection's cluster scope")
	// ErrWorkLogExpectedVersionRequired keeps update/delete truly optimistic. A Worker
	// must send the Version/ETag it observed before replacing or deleting a durable log.
	ErrWorkLogExpectedVersionRequired = errors.New("expectedVersion is required for work log updates and deletes")
)

// WorkLog and WorkLogFilter keep MCP tool/HTTP callers independent of the lower-level
// package path while retaining exactly one durable wire model.
type WorkLog = worklog.Log
type WorkLogFilter = worklog.Filter

// WorkLogPatch permits transport adapters to make a partial replacement without
// exposing immutable Worker, ClusterID, audit, or Version fields. A nil pointer means
// "leave it unchanged"; an explicit empty string/slice clears the corresponding value.
type WorkLogPatch struct {
	Visibility *worklog.Visibility
	ProjectID  *string
	TaskID     *string
	Title      *string
	Body       *string
	Tags       *[]string
}

type workLogStore struct {
	clusterID  string
	projectID  string
	standalone bool
	manager    *worklog.Manager
}

type locatedWorkLog struct {
	workLogStore
	item worklog.Log
}

func (svc *Service) requireWorkLogScope() error {
	if svc == nil || svc.store == nil || !svc.scope.IsCarbon() || strings.TrimSpace(svc.scope.Home) == "" ||
		(!svc.scope.IsStandalone() && strings.TrimSpace(svc.scope.ClusterID) == "") {
		return ErrWorkLogScopeRequired
	}
	return nil
}

func (dataStore workLogStore) identity() string {
	if dataStore.standalone {
		return "standalone project " + dataStore.projectID
	}
	return "cluster " + dataStore.clusterID
}

func (dataStore workLogStore) contains(item worklog.Log) bool {
	if dataStore.standalone {
		return item.Standalone && item.ClusterID == "" && item.ProjectID == dataStore.projectID
	}
	return !item.Standalone && item.ClusterID == dataStore.clusterID
}

func (svc *Service) isCurrentWorkLogStore(dataStore workLogStore) bool {
	if svc.scope.IsStandalone() {
		return dataStore.standalone && dataStore.projectID == svc.scope.ProjectID
	}
	return !dataStore.standalone && dataStore.clusterID == svc.scope.ClusterID
}

// workLogStores resolves physical stores exclusively through the selected Home
// manifest. A standalone connection receives only its private project root; a
// clustered connection retains same-Home discovery and additionally enumerates
// standalone roots for global visibility. It never accepts a caller-supplied path.
func (svc *Service) workLogStores() ([]workLogStore, error) {
	if err := svc.requireWorkLogScope(); err != nil {
		return nil, err
	}
	h, err := home.Open(svc.scope.Home)
	if err != nil {
		return nil, fmt.Errorf("%w: open home: %v", ErrWorkLogScopeRequired, err)
	}
	if svc.scope.IsStandalone() {
		metadata, err := h.ResolveProjectMetadata("", svc.scope.ProjectID)
		if err != nil || !metadata.Standalone || metadata.Project.ID != svc.scope.ProjectID {
			return nil, fmt.Errorf("%w: bound standalone project %s is absent from the selected home", ErrWorkLogScopeRequired, svc.scope.ProjectID)
		}
		root, err := h.ProjectDataRoot(metadata.Project.ID)
		if err != nil {
			return nil, fmt.Errorf("%w: resolve standalone project %s: %v", ErrWorkLogScopeRequired, metadata.Project.ID, err)
		}
		return []workLogStore{{
			projectID:  metadata.Project.ID,
			standalone: true,
			manager:    worklog.New(store.New(root), svc.now),
		}}, nil
	}

	clusters, err := h.ListClusters()
	if err != nil {
		return nil, fmt.Errorf("%w: list clusters: %v", ErrWorkLogScopeRequired, err)
	}
	projects, err := h.ListProjects()
	if err != nil {
		return nil, fmt.Errorf("%w: list standalone projects: %v", ErrWorkLogScopeRequired, err)
	}
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].ID < clusters[j].ID })
	sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
	stores := make([]workLogStore, 0, len(clusters)+len(projects))
	foundCurrent := false
	for _, cluster := range clusters {
		root, err := h.ClusterDataRoot(cluster.ID)
		if err != nil {
			return nil, fmt.Errorf("%w: resolve cluster %s: %v", ErrWorkLogScopeRequired, cluster.ID, err)
		}
		stores = append(stores, workLogStore{
			clusterID: cluster.ID,
			manager:   worklog.New(store.New(root), svc.now),
		})
		if cluster.ID == svc.scope.ClusterID {
			foundCurrent = true
		}
	}
	for _, project := range projects {
		root, err := h.ProjectDataRoot(project.ID)
		if err != nil {
			return nil, fmt.Errorf("%w: resolve standalone project %s: %v", ErrWorkLogScopeRequired, project.ID, err)
		}
		stores = append(stores, workLogStore{
			projectID:  project.ID,
			standalone: true,
			manager:    worklog.New(store.New(root), svc.now),
		})
	}
	if !foundCurrent {
		return nil, fmt.Errorf("%w: bound cluster %s is absent from the selected home", ErrWorkLogScopeRequired, svc.scope.ClusterID)
	}
	return stores, nil
}

func (svc *Service) currentWorkLogStore() (workLogStore, error) {
	stores, err := svc.workLogStores()
	if err != nil {
		return workLogStore{}, err
	}
	for _, dataStore := range stores {
		if svc.isCurrentWorkLogStore(dataStore) {
			return dataStore, nil
		}
	}
	return workLogStore{}, fmt.Errorf("%w: bound scope is absent from the selected home", ErrWorkLogScopeRequired)
}

func (svc *Service) getWorkLog(id string) (locatedWorkLog, error) {
	if err := svc.requireWorkLogScope(); err != nil {
		return locatedWorkLog{}, err
	}
	stores, err := svc.workLogStores()
	if err != nil {
		return locatedWorkLog{}, err
	}
	var found *locatedWorkLog
	for _, dataStore := range stores {
		item, err := dataStore.manager.Get(id)
		if errors.Is(err, worklog.ErrNotFound) {
			continue
		}
		if err != nil {
			return locatedWorkLog{}, err
		}
		if !dataStore.contains(item) {
			return locatedWorkLog{}, fmt.Errorf("%w: %s does not match its %s store", worklog.ErrInvalidWorkLog, item.ID, dataStore.identity())
		}
		if found != nil {
			return locatedWorkLog{}, fmt.Errorf("%w: duplicate id %s in %s and %s", worklog.ErrInvalidWorkLog, item.ID, found.workLogStore.identity(), dataStore.identity())
		}
		candidate := locatedWorkLog{workLogStore: dataStore, item: item}
		found = &candidate
	}
	if found == nil {
		return locatedWorkLog{}, fmt.Errorf("%w: %s", worklog.ErrNotFound, id)
	}
	if err := svc.workLogReadAllowed(found.item, found.workLogStore); err != nil {
		return locatedWorkLog{}, err
	}
	return *found, nil
}

// workLogListFilters moves visibility constraints down to each manager before its
// MaxListLimit is applied. That prevents 200 invisible private records from crowding a
// visible project/global record out of a normal Worker's response.
func (svc *Service) workLogListFilters(filter worklog.Filter) []worklog.Filter {
	visibilities := []worklog.Visibility{filter.Visibility}
	if filter.Visibility == "" {
		visibilities = []worklog.Visibility{worklog.WorkerPrivate, worklog.ProjectPublic, worklog.GlobalPublic}
	}
	filters := make([]worklog.Filter, 0, len(visibilities))
	for _, visibility := range visibilities {
		candidate := filter
		candidate.Visibility = visibility
		candidate.Limit = worklog.MaxListLimit
		if !isWorkLogHumanReader(svc.actor) {
			switch visibility {
			case worklog.WorkerPrivate:
				if candidate.Worker != "" && candidate.Worker != svc.actor {
					continue
				}
				candidate.Worker = svc.actor
			case worklog.ProjectPublic:
				if svc.scope.ProjectID != "" && candidate.ProjectID != "" && candidate.ProjectID != svc.scope.ProjectID {
					continue
				}
				if svc.scope.ProjectID != "" {
					candidate.ProjectID = svc.scope.ProjectID
				}
			}
		}
		filters = append(filters, candidate)
	}
	return filters
}

// ListWorkLogs merges records across the selected Carbon Home. Global logs can be read
// from every Home store by a clustered connection; an isolated standalone connection
// is intentionally limited to its own root. Inaccessible records are omitted so an
// ordinary Worker cannot probe private log existence through list filters.
func (svc *Service) ListWorkLogs(filter worklog.Filter) ([]worklog.Log, error) {
	if err := svc.requireWorkLogScope(); err != nil {
		return nil, err
	}
	if filter.Limit < 1 || filter.Limit > worklog.MaxListLimit {
		return nil, fmt.Errorf("%w: limit must be 1..%d", worklog.ErrInvalidFilter, worklog.MaxListLimit)
	}
	stores, err := svc.workLogStores()
	if err != nil {
		return nil, err
	}
	filters := svc.workLogListFilters(filter)
	seen := make(map[string]string)
	out := make([]worklog.Log, 0, len(stores)*len(filters))
	humanReader := isWorkLogHumanReader(svc.actor)
	for _, dataStore := range stores {
		for _, scopedFilter := range filters {
			// A non-human Worker can only see remote global_public logs. Avoiding
			// a full YAML directory scan for remote private/project records keeps
			// the normal Work Log panel responsive as a Home gains clusters.
			if !humanReader && !svc.isCurrentWorkLogStore(dataStore) && scopedFilter.Visibility != worklog.GlobalPublic {
				continue
			}
			items, err := dataStore.manager.List(scopedFilter)
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				if !dataStore.contains(item) {
					return nil, fmt.Errorf("%w: %s does not match its %s store", worklog.ErrInvalidWorkLog, item.ID, dataStore.identity())
				}
				if previous, exists := seen[item.ID]; exists && previous != dataStore.identity() {
					return nil, fmt.Errorf("%w: duplicate id %s in %s and %s", worklog.ErrInvalidWorkLog, item.ID, previous, dataStore.identity())
				}
				seen[item.ID] = dataStore.identity()
				if svc.workLogReadAllowed(item, dataStore) == nil {
					out = append(out, item)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339Nano, out[i].UpdatedAt)
		right, _ := time.Parse(time.RFC3339Nano, out[j].UpdatedAt)
		if !left.Equal(right) {
			return left.After(right)
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

// GetWorkLog loads one visible Work Log from the selected Home. A global log can be
// discovered across clusters, while private/project records remain local to an ordinary
// Worker. The public API returns only the durable record, never its physical data root.
func (svc *Service) GetWorkLog(id string) (worklog.Log, error) {
	located, err := svc.getWorkLog(id)
	if err != nil {
		return worklog.Log{}, err
	}
	return located.item, nil
}

// CreateWorkLog stamps immutable identity/scope metadata from the bound connection.
// Caller-supplied ID, Worker, ClusterID, audit fields, and Version are ignored so an MCP
// client cannot forge another Worker or write a log into a different cluster.
func (svc *Service) CreateWorkLog(ctx context.Context, item worklog.Log) (worklog.Log, error) {
	if err := svc.requireWorkLogScope(); err != nil {
		return worklog.Log{}, err
	}
	item.ID = ""
	item.Worker = svc.actor
	item.Standalone = svc.scope.IsStandalone()
	item.ClusterID = svc.scope.ClusterID
	item.CreatedAt, item.CreatedBy, item.UpdatedAt, item.UpdatedBy, item.Version = "", "", "", "", ""
	if err := svc.bindWorkLogTask(&item); err != nil {
		return worklog.Log{}, err
	}
	if err := svc.bindWorkLogCreateScope(&item); err != nil {
		return worklog.Log{}, err
	}
	current, err := svc.currentWorkLogStore()
	if err != nil {
		return worklog.Log{}, err
	}
	return current.manager.Create(ctx, svc.actor, item)
}

// UpdateWorkLog changes a visible log owned by this connection's Worker. Worker and
// ClusterID remain immutable and are restored from the current durable record before
// persistence; Created metadata is also protected by worklog.Manager.
func (svc *Service) UpdateWorkLog(ctx context.Context, item worklog.Log, expectedVersion string) (worklog.Log, error) {
	if err := svc.requireWorkLogScope(); err != nil {
		return worklog.Log{}, err
	}
	if strings.TrimSpace(expectedVersion) == "" {
		return worklog.Log{}, ErrWorkLogExpectedVersionRequired
	}
	located, err := svc.getWorkLog(item.ID)
	if err != nil {
		return worklog.Log{}, err
	}
	if err := svc.workLogWriteAllowed(located.item, located.workLogStore); err != nil {
		return worklog.Log{}, err
	}
	item.Worker, item.Standalone, item.ClusterID = located.item.Worker, located.item.Standalone, located.item.ClusterID
	item.CreatedAt, item.CreatedBy, item.UpdatedAt, item.UpdatedBy, item.Version = "", "", "", "", ""
	if err := svc.bindWorkLogTask(&item); err != nil {
		return worklog.Log{}, err
	}
	if err := svc.bindWorkLogUpdateScope(located.item, &item); err != nil {
		return worklog.Log{}, err
	}
	return located.manager.Update(ctx, svc.actor, item, expectedVersion)
}

// PatchWorkLog merges editable fields over the caller-visible current record and then
// performs the same owner/scope/version-enforced update as UpdateWorkLog. The version
// precondition is checked before the read so callers cannot use an invalid patch as a
// side channel for discovering global records.
func (svc *Service) PatchWorkLog(ctx context.Context, id string, patch WorkLogPatch, expectedVersion string) (worklog.Log, error) {
	if strings.TrimSpace(expectedVersion) == "" {
		return worklog.Log{}, ErrWorkLogExpectedVersionRequired
	}
	current, err := svc.GetWorkLog(id)
	if err != nil {
		return worklog.Log{}, err
	}
	if patch.Visibility != nil {
		current.Visibility = *patch.Visibility
	}
	if patch.ProjectID != nil {
		current.ProjectID = *patch.ProjectID
	}
	if patch.TaskID != nil {
		current.TaskID = *patch.TaskID
	}
	if patch.Title != nil {
		current.Title = *patch.Title
	}
	if patch.Body != nil {
		current.Body = *patch.Body
	}
	if patch.Tags != nil {
		current.Tags = append([]string(nil), (*patch.Tags)...)
	}
	return svc.UpdateWorkLog(ctx, current, expectedVersion)
}

// DeleteWorkLog deletes only a record owned by this bound Worker. Human web/admin
// actors retain broad read access but cannot silently rewrite an Agent's audit trail.
func (svc *Service) DeleteWorkLog(ctx context.Context, id, expectedVersion string) error {
	if err := svc.requireWorkLogScope(); err != nil {
		return err
	}
	if strings.TrimSpace(expectedVersion) == "" {
		return ErrWorkLogExpectedVersionRequired
	}
	located, err := svc.getWorkLog(id)
	if err != nil {
		return err
	}
	if err := svc.workLogWriteAllowed(located.item, located.workLogStore); err != nil {
		return err
	}
	return located.manager.Delete(ctx, svc.actor, id, expectedVersion)
}

func (svc *Service) workLogReadAllowed(item worklog.Log, origin workLogStore) error {
	if !origin.contains(item) {
		return ErrWorkLogNotVisible
	}
	if isWorkLogHumanReader(svc.actor) {
		return nil
	}
	if !svc.isCurrentWorkLogStore(origin) {
		if item.Visibility == worklog.GlobalPublic {
			return nil
		}
		return ErrWorkLogNotVisible
	}
	switch item.Visibility {
	case worklog.WorkerPrivate:
		if item.Worker != svc.actor {
			return ErrWorkLogNotVisible
		}
		// A Worker cannot use a project-bound connection to inspect its own private
		// project log from a different project. Cluster-wide private logs remain
		// intentionally usable from a bound project.
		if svc.scope.ProjectID != "" && item.ProjectID != "" && item.ProjectID != svc.scope.ProjectID {
			return ErrWorkLogNotVisible
		}
		return nil
	case worklog.ProjectPublic:
		// An explicit cluster-bound connection is already an intentionally broad
		// authority for this physical task pool. A project-bound connection remains
		// strict and may see only its exact project id.
		if svc.scope.ProjectID != "" && item.ProjectID != svc.scope.ProjectID {
			return ErrWorkLogNotVisible
		}
		return nil
	case worklog.GlobalPublic:
		// Global means every selected cluster/project in this same Home may read it.
		// workLogStores derives all roots exclusively from the bound Home manifest;
		// standalone scopes enumerate only their own private root.
		return nil
	default:
		return ErrWorkLogNotVisible
	}
}

func (svc *Service) workLogWriteAllowed(item worklog.Log, origin workLogStore) error {
	if !svc.isCurrentWorkLogStore(origin) {
		return ErrWorkLogClusterScope
	}
	if err := svc.workLogReadAllowed(item, origin); err != nil {
		return err
	}
	if item.Worker != svc.actor {
		return ErrWorkLogOwnerRequired
	}
	if svc.scope.ProjectID != "" && item.ProjectID != "" && item.ProjectID != svc.scope.ProjectID {
		return ErrWorkLogProjectScope
	}
	return nil
}

func (svc *Service) bindWorkLogCreateScope(item *worklog.Log) error {
	if svc.scope.ProjectID != "" {
		if item.ProjectID == "" {
			item.ProjectID = svc.scope.ProjectID
		}
		if item.ProjectID != svc.scope.ProjectID {
			return fmt.Errorf("%w: requested project %s, bound project %s", ErrWorkLogProjectScope, item.ProjectID, svc.scope.ProjectID)
		}
	}
	if item.Visibility == worklog.ProjectPublic && item.ProjectID == "" {
		return fmt.Errorf("%w: project_public requires project_id", ErrWorkLogProjectScope)
	}
	return svc.validateWorkLogProjectScope(item.ProjectID)
}

func (svc *Service) bindWorkLogUpdateScope(current worklog.Log, item *worklog.Log) error {
	if item.Standalone != current.Standalone || item.ClusterID != current.ClusterID {
		return ErrWorkLogNotVisible
	}
	if svc.scope.ProjectID != "" {
		if item.ProjectID != "" && item.ProjectID != svc.scope.ProjectID {
			return fmt.Errorf("%w: requested project %s, bound project %s", ErrWorkLogProjectScope, item.ProjectID, svc.scope.ProjectID)
		}
		// A project-bound Worker may keep editing its own pre-existing cluster-wide
		// log, but may not clear an existing project association to broaden it into
		// a cluster-wide record. Creating such a record requires an explicit
		// cluster-bound connection.
		if current.ProjectID != "" && item.ProjectID == "" {
			return fmt.Errorf("%w: project-bound updates cannot clear project_id", ErrWorkLogProjectScope)
		}
	}
	if item.Visibility == worklog.ProjectPublic && item.ProjectID == "" {
		return fmt.Errorf("%w: project_public requires project_id", ErrWorkLogProjectScope)
	}
	return svc.validateWorkLogProjectScope(item.ProjectID)
}

func (svc *Service) bindWorkLogTask(item *worklog.Log) error {
	if item.TaskID == "" {
		return nil
	}
	current, err := svc.currentWorkLogStore()
	if err != nil {
		return err
	}
	doc, err := current.manager.Store.Get(item.TaskID)
	if err != nil {
		return err
	}
	if svc.scope.ProjectID != "" && doc.Task.ProjectID != "" && doc.Task.ProjectID != svc.scope.ProjectID {
		return fmt.Errorf("%w: task project %s, bound project %s", ErrWorkLogProjectScope, doc.Task.ProjectID, svc.scope.ProjectID)
	}
	if item.ProjectID == "" && doc.Task.ProjectID != "" {
		item.ProjectID = doc.Task.ProjectID
	}
	if item.ProjectID != "" && doc.Task.ProjectID != "" && item.ProjectID != doc.Task.ProjectID {
		return fmt.Errorf("%w: task %s belongs to project %s", ErrWorkLogProjectScope, item.TaskID, doc.Task.ProjectID)
	}
	return svc.validateWorkLogProjectScope(item.ProjectID)
}

// validateWorkLogProjectScope verifies a non-empty project id against the selected
// home boundary. A standalone scope accepts only its own top-level project; a
// cluster-bound connection may target any project in its selected shared pool.
func (svc *Service) validateWorkLogProjectScope(projectID string) error {
	if projectID == "" {
		return nil
	}
	resolved, err := home.ResolveProjectMetadata(svc.scope.Home, svc.scope.ClusterID, projectID)
	if err != nil {
		return fmt.Errorf("%w: project %s is not in bound scope: %v", ErrWorkLogProjectScope, projectID, err)
	}
	if svc.scope.IsStandalone() {
		if !resolved.Standalone || resolved.Project.ID != svc.scope.ProjectID {
			return fmt.Errorf("%w: project %s is not the bound standalone project", ErrWorkLogProjectScope, projectID)
		}
		return nil
	}
	if resolved.Standalone || resolved.Cluster.ID != svc.scope.ClusterID {
		return fmt.Errorf("%w: project %s is not in bound cluster", ErrWorkLogProjectScope, projectID)
	}
	return nil
}

func isWorkLogHumanReader(actor string) bool {
	return strings.HasPrefix(actor, "human:")
}

// WorkLogVersionCurrent resolves a visible current version for HTTP conflict responses.
// It is deliberately narrow so transports cannot accidentally bypass Work Log visibility
// by reaching the manager directly.
func (svc *Service) WorkLogVersionCurrent(id string) (string, string, error) {
	located, err := svc.getWorkLog(id)
	if err != nil {
		return "", "", err
	}
	return located.item.Version, located.item.ETag(), nil
}

// IsWorkLogError reports whether err belongs to the Work Log service/persistence domain.
// HTTP adapters use this to keep failure mapping self-contained while generic task
// endpoints remain unchanged until the routes are deliberately registered.
func IsWorkLogError(err error) bool {
	return errors.Is(err, ErrWorkLogScopeRequired) ||
		errors.Is(err, ErrWorkLogNotVisible) ||
		errors.Is(err, ErrWorkLogOwnerRequired) ||
		errors.Is(err, ErrWorkLogProjectScope) ||
		errors.Is(err, ErrWorkLogClusterScope) ||
		errors.Is(err, ErrWorkLogExpectedVersionRequired) ||
		errors.Is(err, worklog.ErrNotFound) ||
		errors.Is(err, worklog.ErrInvalidWorkLog) ||
		errors.Is(err, worklog.ErrInvalidFilter) ||
		errors.Is(err, store.ErrVersionMismatch)
}

// registerWorkLogTools adds the approved stable Carbon v2 Work Log surface. NewServer owns the
// compatibility/version gate and calls this function only from its carbonStableTasks
// branch, keeping the frozen legacy tool catalog unchanged. Every operation still enters
// the Service layer, where Home, scope, actor ownership, and optimistic versions are
// enforced independently of tool registration.
func registerWorkLogTools(srv *mcpsdk.Server, svc *Service) {
	if svc == nil {
		return
	}
	registerWorkLogToolsWithAccessor(srv, func() *Service { return svc })
}

// registerWorkLogToolsWithAccessor keeps Work Log handlers aligned with the
// current immutable Service in Project Session mode. Unlike most tool closures
// in tools.go, worklogs.go used to receive a by-value *Service argument, which
// would otherwise keep pointing at the initial home-only catalog service after
// select_project. The accessor is called only while NewProjectSessionServer's
// receiving middleware holds its session mutex.
func registerWorkLogToolsWithAccessor(srv *mcpsdk.Server, current func() *Service) {
	if srv == nil || current == nil {
		return
	}
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "worklog_create",
		Description: "Carbon v2 stable write: create a durable Worker Work Log in the active/bound project scope (a standalone project or a member of one shared cluster). visibility and title are required; Worker, cluster/project, audit fields, and ID are server-owned, and project_public requires project_id.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in workLogCreateIn) (*mcpsdk.CallToolResult, workLogOut, error) {
		svc := current()
		if svc == nil {
			return nil, workLogOut{}, ErrActiveProjectRequired
		}
		item, err := svc.CreateWorkLog(ctx, worklog.Log{
			Visibility: in.Visibility,
			ProjectID:  in.ProjectID,
			TaskID:     in.TaskID,
			Title:      in.Title,
			Body:       in.Body,
			Tags:       append([]string(nil), in.Tags...),
		})
		return nil, workLogOut{WorkLog: item}, err
	})

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "worklog_get",
		Description: "Carbon v2 stable read: get one visible Work Log. worker_private is visible only to its Worker; project_public honors the active/bound project scope (standalone or shared-cluster member); global_public is readable only within this Carbon home.",
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, in workLogGetIn) (*mcpsdk.CallToolResult, workLogOut, error) {
		svc := current()
		if svc == nil {
			return nil, workLogOut{}, ErrActiveProjectRequired
		}
		item, err := svc.GetWorkLog(in.ID)
		return nil, workLogOut{WorkLog: item}, err
	})

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "worklog_list",
		Description: "Carbon v2 stable read: list visible Work Logs deterministically. limit is required (1..200); worker, visibility, project_id, and task_id are conjunctive filters and cannot bypass the active/bound project scope.",
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, in workLogListIn) (*mcpsdk.CallToolResult, workLogsOut, error) {
		svc := current()
		if svc == nil {
			return nil, workLogsOut{}, ErrActiveProjectRequired
		}
		items, err := svc.ListWorkLogs(worklog.Filter{
			Worker:     in.Worker,
			Visibility: in.Visibility,
			ProjectID:  in.ProjectID,
			TaskID:     in.TaskID,
			Limit:      in.Limit,
		})
		return nil, workLogsOut{WorkLogs: items}, err
	})

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "worklog_update",
		Description: "Carbon v2 stable write: partially update the caller-owned Work Log. id and current expected_version are required (raw version or quoted ETag); omitted editable fields stay unchanged and stale writes are rejected.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in workLogUpdateIn) (*mcpsdk.CallToolResult, workLogOut, error) {
		svc := current()
		if svc == nil {
			return nil, workLogOut{}, ErrActiveProjectRequired
		}
		item, err := svc.PatchWorkLog(ctx, in.ID, WorkLogPatch{
			Visibility: in.Visibility,
			ProjectID:  in.ProjectID,
			TaskID:     in.TaskID,
			Title:      in.Title,
			Body:       in.Body,
			Tags:       in.Tags,
		}, in.ExpectedVersion)
		return nil, workLogOut{WorkLog: item}, err
	})

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "worklog_delete",
		Description: "Carbon v2 stable write: delete the caller-owned Work Log. id and current expected_version are required (raw version or quoted ETag); stale writes are rejected.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in workLogDeleteIn) (*mcpsdk.CallToolResult, workLogDeleteOut, error) {
		svc := current()
		if svc == nil {
			return nil, workLogDeleteOut{}, ErrActiveProjectRequired
		}
		err := svc.DeleteWorkLog(ctx, in.ID, in.ExpectedVersion)
		return nil, workLogDeleteOut{ID: in.ID, Deleted: err == nil}, err
	})
}

type workLogCreateIn struct {
	Visibility worklog.Visibility `json:"visibility" jsonschema:"required: worker_private, project_public, or global_public"`
	ProjectID  string             `json:"project_id,omitempty" jsonschema:"optional same-cluster project id; required for project_public"`
	TaskID     string             `json:"task_id,omitempty" jsonschema:"optional task id in the bound cluster"`
	Title      string             `json:"title" jsonschema:"required work log title"`
	Body       string             `json:"body,omitempty" jsonschema:"optional markdown work log body"`
	Tags       []string           `json:"tags,omitempty" jsonschema:"optional replacement tag list, up to 32"`
}

type workLogGetIn struct {
	ID string `json:"id" jsonschema:"required work log id"`
}

type workLogListIn struct {
	Worker     string             `json:"worker,omitempty" jsonschema:"optional canonical Worker actor filter"`
	Visibility worklog.Visibility `json:"visibility,omitempty" jsonschema:"optional: worker_private, project_public, or global_public"`
	ProjectID  string             `json:"project_id,omitempty" jsonschema:"optional project id filter"`
	TaskID     string             `json:"task_id,omitempty" jsonschema:"optional task id filter"`
	Limit      int                `json:"limit" jsonschema:"required integer from 1 through 200"`
}

type workLogUpdateIn struct {
	ID              string              `json:"id" jsonschema:"required work log id"`
	Visibility      *worklog.Visibility `json:"visibility,omitempty" jsonschema:"set visibility; omit to leave unchanged"`
	ProjectID       *string             `json:"project_id,omitempty" jsonschema:"set project id; explicit empty clears it unless visibility is project_public"`
	TaskID          *string             `json:"task_id,omitempty" jsonschema:"set task id; explicit empty clears it"`
	Title           *string             `json:"title,omitempty" jsonschema:"set title; omit to leave unchanged"`
	Body            *string             `json:"body,omitempty" jsonschema:"set markdown body; explicit empty clears it"`
	Tags            *[]string           `json:"tags,omitempty" jsonschema:"replace tags; explicit empty list clears them"`
	ExpectedVersion string              `json:"expected_version" jsonschema:"required raw version or quoted ETag optimistic-concurrency token"`
}

type workLogDeleteIn struct {
	ID              string `json:"id" jsonschema:"required work log id"`
	ExpectedVersion string `json:"expected_version" jsonschema:"required raw version or quoted ETag optimistic-concurrency token"`
}

type workLogOut struct {
	WorkLog worklog.Log `json:"work_log"`
}

type workLogsOut struct {
	WorkLogs []worklog.Log `json:"work_logs"`
}

type workLogDeleteOut struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}
