package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"carbon/internal/lease"
	"carbon/internal/search"
	"carbon/internal/stats"
	"carbon/internal/store"
	"carbon/internal/task"
	"carbon/internal/templates"
	"carbon/internal/trash"
	"carbon/internal/views"
)

// LeaseClaimInput is the adapter-neutral ownership request used by REST and MCP.
// A conflicting claim deliberately persists a pending approval request and returns
// lease.ErrApprovalPending together with its ClaimResult.
type LeaseClaimInput struct {
	TaskID          string
	TTL             time.Duration
	RequestID       string
	Reason          string
	ExpectedVersion string
}

// ClaimLease uses the durable lease manager rather than the historical assignment-only
// claim path. This is the one ownership primitive for Carbon and legacy adapters alike.
func (svc *Service) ClaimLease(ctx context.Context, input LeaseClaimInput) (lease.ClaimResult, error) {
	if _, err := svc.writableTask(input.TaskID); err != nil {
		return lease.ClaimResult{}, err
	}
	return lease.New(svc.store, svc.now, 0).Claim(ctx, lease.ClaimInput{
		TaskID: input.TaskID, Actor: svc.actor, TTL: input.TTL, RequestID: input.RequestID,
		Reason: input.Reason, ExpectedVersion: input.ExpectedVersion,
	})
}

// AuthorizeTaskWrite lets transports reject an out-of-scope write before they create
// ancillary directories or files. It performs no mutation and preserves shared-task
// write semantics for a project-bound Carbon connection.
func (svc *Service) AuthorizeTaskWrite(taskID string) error {
	_, err := svc.writableTask(taskID)
	return err
}

func (svc *Service) RenewLease(ctx context.Context, taskID, leaseID string, ttl time.Duration, expectedVersion string) (*store.Doc, error) {
	if _, err := svc.writableTask(taskID); err != nil {
		return nil, err
	}
	return lease.New(svc.store, svc.now, 0).Renew(ctx, lease.RenewInput{
		TaskID: taskID, Actor: svc.actor, LeaseID: leaseID, TTL: ttl, ExpectedVersion: expectedVersion,
	})
}

func (svc *Service) ReleaseLease(ctx context.Context, taskID, leaseID, reason, expectedVersion string, keepAssignee bool) (*store.Doc, error) {
	if _, err := svc.writableTask(taskID); err != nil {
		return nil, err
	}
	return lease.New(svc.store, svc.now, 0).Release(ctx, lease.ReleaseInput{
		TaskID: taskID, Actor: svc.actor, LeaseID: leaseID, Reason: reason,
		ExpectedVersion: expectedVersion, KeepAssignee: keepAssignee,
	})
}

func (svc *Service) ReassignLease(ctx context.Context, taskID, assignee, reason, expectedVersion string, force bool) (*store.Doc, error) {
	if _, err := svc.writableTask(taskID); err != nil {
		return nil, err
	}
	return lease.New(svc.store, svc.now, 0).Reassign(ctx, lease.ReassignInput{
		TaskID: taskID, Actor: svc.actor, Assignee: assignee, Force: force,
		Reason: reason, ExpectedVersion: expectedVersion,
	})
}

func (svc *Service) ApproveLeaseClaim(ctx context.Context, taskID, requestID, reason, expectedVersion string, approve bool) (*store.Doc, error) {
	if _, err := svc.writableTask(taskID); err != nil {
		return nil, err
	}
	return lease.New(svc.store, svc.now, 0).Approve(ctx, lease.ApproveInput{
		TaskID: taskID, Approver: svc.actor, RequestID: requestID, Approve: approve,
		Reason: reason, ExpectedVersion: expectedVersion,
	})
}

// ExpireLeases is the host scheduler hook. It is intentionally exposed on Service so
// stdio MCP, HTTP, and CLI hosts share the same durable timeout sweep instead of leaving
// expired ownership records on disk until another claimant happens to touch the task.
func (svc *Service) ExpireLeases(ctx context.Context) ([]lease.Expired, error) {
	// A request bound to one project must never use opportunistic maintenance to
	// mutate another project's task. Hosts run the scheduler with a cluster scope.
	if svc.scope.IsCarbon() && svc.scope.ProjectID != "" && !svc.scope.IsStandalone() {
		return nil, nil
	}
	return lease.New(svc.store, svc.now, 0).Expire(ctx)
}

// TrashTask is the default task deletion path. It is deliberately soft-delete only;
// permanent trash emptying remains an explicit human-facing REST operation and is not
// exposed through MCP.
func (svc *Service) TrashTask(ctx context.Context, id, reason, expectedVersion string) (trash.Entry, error) {
	if _, err := svc.writableTask(id); err != nil {
		return trash.Entry{}, err
	}
	return trash.New(svc.store, svc.now).Trash(ctx, trash.Input{
		ID: id, Actor: svc.actor, Reason: reason, ExpectedVersion: expectedVersion,
	})
}

func (svc *Service) TrashTasks(ctx context.Context, ids []string, reason string, expectedVersions map[string]string) ([]trash.Entry, error) {
	if len(ids) > 100 {
		return nil, fmt.Errorf("bulk trash exceeds 100 tasks")
	}
	if err := svc.writableTasks(ids); err != nil {
		return nil, err
	}
	if svc.scope.IsCarbon() && !hasExpectedVersions(ids, expectedVersions) {
		return nil, ErrExpectedVersionsRequired
	}
	return trash.New(svc.store, svc.now).TrashMany(ctx, trash.Input{
		IDs: ids, Actor: svc.actor, Reason: reason, ExpectedVersions: expectedVersions,
	})
}

func (svc *Service) ListTrash(includeCluster bool) ([]trash.Entry, error) {
	if err := svc.validateIncludeCluster(includeCluster); err != nil {
		return nil, err
	}
	entries, err := trash.New(svc.store, svc.now).List()
	if err != nil || !svc.scope.IsCarbon() || (includeCluster && !svc.scope.IsStandalone()) || svc.scope.ProjectID == "" {
		return entries, err
	}
	out := entries[:0]
	for _, entry := range entries {
		if entry.ProjectID == svc.scope.ProjectID {
			out = append(out, entry)
		}
	}
	return out, nil
}

func (svc *Service) RestoreTrash(ctx context.Context, id string, targetProjectID *string, expectedVersion string) (*store.Doc, error) {
	doc, err := svc.store.GetTrash(id)
	if err != nil {
		return nil, err
	}
	if err := svc.writeAllowed(doc.Task); err != nil {
		return nil, err
	}
	if targetProjectID != nil {
		if svc.scope.IsStandalone() && *targetProjectID != svc.scope.ProjectID {
			return nil, fmt.Errorf("%w: standalone restore target %q", ErrProjectWriteScope, *targetProjectID)
		}
		if svc.scope.IsCarbon() && svc.scope.ProjectID != "" && *targetProjectID != "" && *targetProjectID != svc.scope.ProjectID {
			return nil, fmt.Errorf("%w: restore target %s", ErrProjectWriteScope, *targetProjectID)
		}
		if err := svc.validateProject(*targetProjectID); err != nil {
			return nil, err
		}
	}
	return trash.New(svc.store, svc.now).Restore(ctx, trash.RestoreInput{
		ID: id, Actor: svc.actor, TargetProjectID: targetProjectID, ExpectedVersion: expectedVersion,
	})
}

// Search makes a project-bound Carbon connection least-privilege by default. The
// includeCluster switch only broadens to the current physical cluster store; it can
// never select another cluster.
func (svc *Service) Search(query search.Query, includeCluster bool) ([]search.Result, error) {
	if err := svc.validateIncludeCluster(includeCluster); err != nil {
		return nil, err
	}
	if svc.scope.IsCarbon() {
		query.ClusterID = svc.scope.ClusterID
		if (!includeCluster || svc.scope.IsStandalone()) && svc.scope.ProjectID != "" {
			project := svc.scope.ProjectID
			query.ProjectID = &project
		}
	}
	return search.SearchStore(svc.store, query)
}

// BulkUpdate is all-or-nothing within the current physical store. A Carbon target
// project is resolved before entering store's write transaction, which prevents an id
// from another cluster being smuggled into a shared task pool.
func (svc *Service) BulkUpdate(ctx context.Context, update store.BulkUpdate) ([]*store.Doc, error) {
	if len(update.IDs) == 0 || len(update.IDs) > 100 {
		return nil, fmt.Errorf("bulk update requires 1 to 100 task ids")
	}
	// Carbon ownership is lease-backed. Direct bulk assignment would bypass both
	// lease conflict handling and its durable approval/audit path, including when
	// the task currently has no visible assignee. Legacy workspaces retain their
	// historical bulk-assignee behavior below.
	if svc.scope.IsCarbon() && update.Assignee != nil {
		return nil, ErrAssigneeLeaseRequired
	}
	if svc.scope.IsCarbon() && !hasExpectedVersions(update.IDs, update.ExpectedVersions) {
		return nil, ErrExpectedVersionsRequired
	}
	if err := svc.writableTasks(update.IDs); err != nil {
		return nil, err
	}
	if update.ProjectID != nil {
		if err := svc.authorizeBulkProjectMove(update.IDs, *update.ProjectID, update.Force, update.Reason); err != nil {
			return nil, err
		}
		if err := svc.validateProject(*update.ProjectID); err != nil {
			return nil, err
		}
	}
	return svc.store.BulkUpdate(ctx, svc.actor, update)
}

func (svc *Service) BulkMove(ctx context.Context, move store.BulkMove) ([]*store.Doc, error) {
	return svc.BulkMoveWithAuthorization(ctx, move, false)
}

// BulkMoveWithAuthorization adds the adapter-level force flag without changing the
// store primitive's durable shape. The underlying mutation persists move.Reason in
// provenance; the force acknowledgement is enforced before the transaction starts.
func (svc *Service) BulkMoveWithAuthorization(ctx context.Context, move store.BulkMove, force bool) ([]*store.Doc, error) {
	if len(move.IDs) == 0 || len(move.IDs) > 100 {
		return nil, fmt.Errorf("bulk move requires 1 to 100 task ids")
	}
	if err := store.ValidateBulkMove(move); err != nil {
		// REST maps scope violations to 422 without teaching its generic error mapper a
		// transport-specific bulk-move sentinel. Store enforces the identical shape for
		// direct callers below.
		return nil, fmt.Errorf("%w: %v", ErrProjectWriteScope, err)
	}
	if svc.scope.IsCarbon() && !hasExpectedVersions(move.IDs, move.ExpectedVersions) {
		return nil, ErrExpectedVersionsRequired
	}
	if err := svc.writableTasks(move.IDs); err != nil {
		return nil, err
	}
	if err := svc.authorizeBulkProjectMove(move.IDs, move.ProjectID, force, move.Reason); err != nil {
		return nil, err
	}
	if err := svc.validateProject(move.ProjectID); err != nil {
		return nil, err
	}
	return svc.store.BulkMove(ctx, svc.actor, move)
}

// authorizeBulkProjectMove makes every actual Carbon project-scope change explicit,
// including a cluster-bound connection and project<->cluster-wide moves. The source is
// loaded per task so a mixed batch cannot use a same-project item to bypass the changed
// item's force/reason acknowledgement.
func (svc *Service) authorizeBulkProjectMove(ids []string, targetProjectID string, force bool, reason string) error {
	if !svc.scope.IsCarbon() {
		return nil
	}
	if svc.scope.IsStandalone() && targetProjectID != svc.scope.ProjectID {
		return fmt.Errorf("%w: standalone project cannot move tasks to %q", ErrProjectWriteScope, targetProjectID)
	}
	for _, id := range ids {
		doc, err := svc.store.Get(id)
		if err != nil {
			return err
		}
		if doc.Task.ProjectID == targetProjectID {
			continue
		}
		if !force || strings.TrimSpace(reason) == "" {
			return fmt.Errorf("%w: moving %s from %q to %q requires force=true and reason", ErrProjectWriteScope, id, doc.Task.ProjectID, targetProjectID)
		}
	}
	return nil
}

func (svc *Service) ListViews() ([]views.View, error) {
	return views.New(svc.store, svc.now).List()
}

func (svc *Service) GetView(id string) (views.View, error) {
	return views.New(svc.store, svc.now).Get(id)
}

func (svc *Service) CreateView(ctx context.Context, view views.View, includeCluster bool) (views.View, error) {
	if err := svc.validateIncludeCluster(includeCluster); err != nil {
		return views.View{}, err
	}
	view.Query = svc.scopedQuery(view.Query, includeCluster)
	return views.New(svc.store, svc.now).Create(ctx, svc.actor, view)
}

func (svc *Service) SaveView(ctx context.Context, view views.View, expectedVersion string, includeCluster bool) (views.View, error) {
	if err := svc.validateIncludeCluster(includeCluster); err != nil {
		return views.View{}, err
	}
	view.Query = svc.scopedQuery(view.Query, includeCluster)
	return views.New(svc.store, svc.now).Save(ctx, svc.actor, view, expectedVersion)
}

func (svc *Service) DeleteView(ctx context.Context, id, expectedVersion string) error {
	return views.New(svc.store, svc.now).Delete(ctx, svc.actor, id, expectedVersion)
}

func (svc *Service) ApplyView(id string, includeCluster bool) ([]search.Result, error) {
	if err := svc.validateIncludeCluster(includeCluster); err != nil {
		return nil, err
	}
	view, err := svc.GetView(id)
	if err != nil {
		return nil, err
	}
	return svc.Search(view.Query, includeCluster)
}

func (svc *Service) scopedQuery(query search.Query, includeCluster bool) search.Query {
	if svc.scope.IsCarbon() {
		query.ClusterID = svc.scope.ClusterID
		if (!includeCluster || svc.scope.IsStandalone()) && svc.scope.ProjectID != "" {
			project := svc.scope.ProjectID
			query.ProjectID = &project
		}
	}
	return query
}

func (svc *Service) ListTemplates() ([]templates.Template, error) {
	items, err := templates.New(svc.store, svc.now).List()
	if err != nil || !svc.scope.IsCarbon() || svc.scope.ProjectID == "" {
		return items, err
	}
	out := items[:0]
	for _, item := range items {
		if (!svc.scope.IsStandalone() && item.ClusterWide) || item.ProjectID == svc.scope.ProjectID {
			out = append(out, item)
		}
	}
	return out, nil
}

func (svc *Service) GetTemplate(id string) (templates.Template, error) {
	item, err := templates.New(svc.store, svc.now).Get(id)
	if err != nil {
		return templates.Template{}, err
	}
	if err := svc.templateReadAllowed(item); err != nil {
		return templates.Template{}, err
	}
	return item, nil
}

func (svc *Service) CreateTemplate(ctx context.Context, template templates.Template) (templates.Template, error) {
	if err := svc.validateTemplateProject(template.ProjectID, template.ClusterWide); err != nil {
		return templates.Template{}, err
	}
	return templates.New(svc.store, svc.now).Create(ctx, svc.actor, template)
}

func (svc *Service) SaveTemplate(ctx context.Context, template templates.Template, expectedVersion string) (templates.Template, error) {
	manager := templates.New(svc.store, svc.now)
	existing, err := manager.Get(template.ID)
	if err != nil {
		return templates.Template{}, err
	}
	if err := svc.templateWriteAllowed(existing); err != nil {
		return templates.Template{}, err
	}
	if err := svc.validateTemplateProject(template.ProjectID, template.ClusterWide); err != nil {
		return templates.Template{}, err
	}
	return manager.Save(ctx, svc.actor, template, expectedVersion)
}

func (svc *Service) DeleteTemplate(ctx context.Context, id, expectedVersion string) error {
	manager := templates.New(svc.store, svc.now)
	item, err := manager.Get(id)
	if err != nil {
		return err
	}
	if err := svc.templateWriteAllowed(item); err != nil {
		return err
	}
	return manager.Delete(ctx, svc.actor, id, expectedVersion)
}

func (svc *Service) InstantiateTemplate(ctx context.Context, input templates.InstantiateInput) (*store.Doc, error) {
	manager := templates.New(svc.store, svc.now)
	item, err := manager.Get(input.TemplateID)
	if err != nil {
		return nil, err
	}
	if err := svc.templateWriteAllowed(item); err != nil {
		return nil, err
	}
	if input.Actor == "" {
		input.Actor = svc.actor
	}
	if input.Actor != svc.actor {
		return nil, ErrIdentityMismatch
	}
	if input.ProjectID != nil {
		if svc.scope.IsStandalone() && *input.ProjectID != svc.scope.ProjectID {
			return nil, fmt.Errorf("%w: standalone template target %q", ErrProjectWriteScope, *input.ProjectID)
		}
		if svc.scope.IsCarbon() && svc.scope.ProjectID != "" && *input.ProjectID != "" && *input.ProjectID != svc.scope.ProjectID {
			return nil, fmt.Errorf("%w: template target %s", ErrProjectWriteScope, *input.ProjectID)
		}
		if err := svc.validateProject(*input.ProjectID); err != nil {
			return nil, err
		}
	} else if svc.scope.IsCarbon() && svc.scope.ProjectID != "" {
		project := svc.scope.ProjectID
		input.ProjectID = &project
	}
	return manager.Instantiate(ctx, input)
}

func (svc *Service) validateTemplateProject(projectID string, clusterWide bool) error {
	if svc.scope.IsStandalone() {
		if clusterWide || projectID == "" || projectID != svc.scope.ProjectID {
			return fmt.Errorf("%w: standalone templates must stay in project %s", ErrProjectWriteScope, svc.scope.ProjectID)
		}
		return nil
	}
	if clusterWide {
		if strings.TrimSpace(projectID) != "" {
			return errors.New("cluster-wide template cannot set project id")
		}
		return nil
	}
	if projectID == "" && svc.scope.IsCarbon() && svc.scope.ProjectID != "" {
		return errors.New("Carbon template must name a project or be explicitly cluster-wide")
	}
	if svc.scope.IsCarbon() && svc.scope.ProjectID != "" && projectID != "" && projectID != svc.scope.ProjectID {
		return fmt.Errorf("%w: template project %s", ErrProjectWriteScope, projectID)
	}
	return svc.validateProject(projectID)
}

func (svc *Service) templateReadAllowed(item templates.Template) error {
	if svc.scope.IsStandalone() {
		if !item.ClusterWide && item.ProjectID == svc.scope.ProjectID {
			return nil
		}
		return fmt.Errorf("%w: template %s", ErrProjectScope, item.ID)
	}
	if !svc.scope.IsCarbon() || svc.scope.ProjectID == "" || item.ClusterWide || item.ProjectID == svc.scope.ProjectID {
		return nil
	}
	return fmt.Errorf("%w: template %s", ErrProjectScope, item.ID)
}

func (svc *Service) templateWriteAllowed(item templates.Template) error {
	if svc.scope.IsStandalone() {
		if !item.ClusterWide && item.ProjectID == svc.scope.ProjectID {
			return nil
		}
		return fmt.Errorf("%w: template %s", ErrProjectWriteScope, item.ID)
	}
	if !svc.scope.IsCarbon() || svc.scope.ProjectID == "" || item.ClusterWide || item.ProjectID == svc.scope.ProjectID {
		return nil
	}
	return fmt.Errorf("%w: template %s", ErrProjectWriteScope, item.ID)
}

func (svc *Service) WorkerStats(includeCluster bool) (stats.Report, error) {
	if err := svc.validateIncludeCluster(includeCluster); err != nil {
		return stats.Report{}, err
	}
	filter := stats.Filter{Scope: stats.ScopeAll}
	if svc.scope.IsCarbon() {
		filter.ClusterID = svc.scope.ClusterID
		if (!includeCluster || svc.scope.IsStandalone()) && svc.scope.ProjectID != "" {
			project := svc.scope.ProjectID
			filter.Scope = stats.ScopeProject
			filter.ProjectID = &project
		} else {
			filter.Scope = stats.ScopeCluster
		}
	}
	return stats.ComputeStore(svc.store, filter)
}

// NormalizeExpectedVersions copies untrusted adapter maps before a mutation. It avoids
// caller-side reuse/mutation races without imposing a map shape on every transport.
func NormalizeExpectedVersions(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for id, version := range in {
		out[id] = strings.TrimSpace(version)
	}
	return out
}

// CloneStrings is intentionally exported for adapter request parsing where optional
// label slices must survive a manager/store write without sharing the JSON decoder's
// backing array.
func CloneStrings(values []string) []string { return slices.Clone(values) }

func hasExpectedVersions(ids []string, versions map[string]string) bool {
	if len(versions) == 0 {
		return false
	}
	for _, id := range ids {
		if strings.TrimSpace(versions[id]) == "" {
			return false
		}
	}
	return true
}

// Ensure compiler catches accidentally unused task evolution imports in this adapter
// surface; task remains useful to callers through the task-bearing response types.
var _ = task.Task{}
