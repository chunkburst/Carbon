package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"carbon/internal/task"
)

var (
	ErrAssigneeForceRequired = fmt.Errorf("force is required to replace an active assignee")
	ErrAuditReasonRequired   = fmt.Errorf("reason is required for this assignment change")
	// ErrRollbackIncomplete means a multi-file operation could not fully restore its
	// pre-mutation snapshots. The returned error is joined with both the original
	// operation error and every failed compensation so callers never mistake it for an
	// atomic no-op.
	ErrRollbackIncomplete = errors.New("store rollback incomplete; durable state may be partial")
	// ErrBulkMoveClusterWideRequired prevents an empty project id from silently
	// becoming shared work. The caller must explicitly acknowledge that scope.
	ErrBulkMoveClusterWideRequired = errors.New("bulk move to an empty project requires cluster_wide=true")
	// ErrBulkMoveProjectConflict prevents contradictory target declarations.
	ErrBulkMoveProjectConflict = errors.New("bulk move cannot set cluster_wide=true with a non-empty project id")
)

// BulkUpdate is the coordinated mutation primitive for field edits. Nil pointers mean
// "leave unchanged"; non-nil empty values clear a field where that is meaningful.
// Validation is completed before the first write, then a filesystem failure triggers
// snapshot compensation. It is not a database transaction: callers must handle
// ErrRollbackIncomplete as a durable partial-failure result.
// ExpectedVersions accepts raw Versions or quoted ETags per task id and is optional for
// legacy callers.
type BulkUpdate struct {
	IDs              []string
	ExpectedVersions map[string]string
	ProjectID        *string
	Type             *string
	Importance       *string
	Priority         *string
	Labels           *[]string
	Assignee         *string
	Parent           *string
	Status           *string
	// Force + Reason are required when replacing an occupied assignee or active lease.
	// Reason is preserved in dedicated provenance events, not hidden in a generic batch log.
	Force  bool
	Reason string
}

// BulkMove is the focused project/parent move primitive. It exists separately from
// BulkUpdate so adapters can express a move intentionally instead of treating it as an
// arbitrary field edit.
type BulkMove struct {
	IDs              []string
	ExpectedVersions map[string]string
	ProjectID        string
	// ClusterWide preserves an intentionally empty project id rather than falling back to
	// a config default. Parent nil leaves hierarchy unchanged.
	ClusterWide bool
	Parent      *string
	Reason      string
}

// ValidateBulkMove keeps every adapter and direct store caller on the same explicit
// target-scope contract: empty project_id means shared work only with cluster_wide=true;
// a concrete project and cluster_wide=true are contradictory.
func ValidateBulkMove(move BulkMove) error {
	if strings.TrimSpace(move.ProjectID) == "" && !move.ClusterWide {
		return ErrBulkMoveClusterWideRequired
	}
	if strings.TrimSpace(move.ProjectID) != "" && move.ClusterWide {
		return ErrBulkMoveProjectConflict
	}
	return nil
}

// BulkUpdate validates every target and every optimistic token before writing any file.
// Files are saved beneath one repository lock and compensated from byte snapshots if an
// unexpected write fails. A failed compensation is returned as ErrRollbackIncomplete,
// joined with the primary write failure, rather than being presented as an atomic no-op.
func (s *Store) BulkUpdate(ctx context.Context, actor string, update BulkUpdate) ([]*Doc, error) {
	var changed []*Doc
	err := s.Write(ctx, actor, "bulk update tasks", func(tx *WriteTx) error {
		var err error
		changed, err = tx.bulkUpdate(actor, update, time.Now())
		return err
	})
	return changed, err
}

// BulkMove coordinates task moves to a target project and optionally reparents them.
// It has the same explicit partial-failure contract as BulkUpdate.
func (s *Store) BulkMove(ctx context.Context, actor string, move BulkMove) ([]*Doc, error) {
	if err := ValidateBulkMove(move); err != nil {
		return nil, err
	}
	project := move.ProjectID
	update := BulkUpdate{
		IDs: move.IDs, ExpectedVersions: move.ExpectedVersions, ProjectID: &project, Parent: move.Parent, Reason: move.Reason,
	}
	if move.ClusterWide {
		// ProjectID is already the explicit empty string. BulkUpdate never applies config
		// fallback, unlike task creation, so the boolean is primarily self-documenting.
		update.ProjectID = &project
	}
	var changed []*Doc
	err := s.Write(ctx, actor, "bulk move tasks", func(tx *WriteTx) error {
		var err error
		changed, err = tx.bulkUpdate(actor, update, time.Now())
		return err
	})
	return changed, err
}

func (tx *WriteTx) bulkUpdate(actor string, update BulkUpdate, at time.Time) ([]*Doc, error) {
	ids, err := normalizeIDs(update.IDs)
	if err != nil {
		return nil, err
	}
	if update.Type != nil {
		cfg, err := tx.Config()
		if err != nil {
			return nil, err
		}
		if !cfg.TypeCatalog().Allowed(*update.Type) {
			return nil, fmt.Errorf("%w: %q", task.ErrInvalidType, *update.Type)
		}
	}
	if update.Importance != nil && !task.ValidImportance(*update.Importance) {
		return nil, fmt.Errorf("%w: %q", task.ErrInvalidImportance, *update.Importance)
	}
	if update.Priority != nil && !task.ValidPriority(*update.Priority) {
		return nil, fmt.Errorf("%w: %q", task.ErrInvalidPriority, *update.Priority)
	}

	docs := make([]*Doc, 0, len(ids))
	all, err := tx.store.List()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		doc, err := tx.GetTask(id)
		if err != nil {
			return nil, err
		}
		if expected := update.ExpectedVersions[id]; expected != "" {
			if err := doc.MatchVersion(expected); err != nil {
				return nil, err
			}
		}
		docs = append(docs, doc)
		all[id] = doc.Task
	}

	// Validate parent and status changes against the post-edit in-memory graph before any
	// durable write. Status uses the existing pure gates; batch transitions do not get to
	// bypass dependencies/checks merely because they are batched.
	for _, d := range docs {
		candidate := d.Task
		if update.Parent != nil {
			candidate.Parent = *update.Parent
		}
		if update.Status != nil {
			cfg, err := tx.Config()
			if err != nil {
				return nil, err
			}
			rules := task.Rules{Initial: cfg.Initial, Closed: cfg.Closed, States: cfg.States, Review: cfg.Review()}
			if err := task.CanTransition(candidate, *update.Status, all, rules); err != nil {
				return nil, err
			}
			candidate.Status = *update.Status
		}
		all[d.Task.ID] = candidate
		if update.Assignee != nil {
			current := d.Task.Assignee
			if d.Task.Lease != nil {
				current = d.Task.Lease.Holder
			}
			if current != *update.Assignee && (current != "" || d.Task.Lease != nil) {
				if !update.Force {
					return nil, fmt.Errorf("%w: %s", ErrAssigneeForceRequired, d.Task.ID)
				}
				if strings.TrimSpace(update.Reason) == "" {
					return nil, ErrAuditReasonRequired
				}
			}
		}
	}
	if update.Parent != nil {
		if err := task.ValidateParents(all); err != nil {
			return nil, err
		}
	}

	details := bulkUpdateDetails(update)
	for _, d := range docs {
		if update.ProjectID != nil {
			from := d.Task.ProjectID
			d.SetProjectID(*update.ProjectID)
			if from != *update.ProjectID {
				d.AppendProvenance(actor, "project moved", auditChangeText("from="+from, "to="+*update.ProjectID, update.Reason), at)
			}
		}
		if update.Type != nil {
			d.SetType(*update.Type)
		}
		if update.Importance != nil {
			d.SetImportance(*update.Importance)
		}
		if update.Priority != nil {
			d.SetPriority(*update.Priority)
		}
		if update.Labels != nil {
			d.SetLabels(slices.Clone(*update.Labels))
		}
		if update.Assignee != nil {
			from := d.Task.Assignee
			if d.Task.Lease != nil {
				from = d.Task.Lease.Holder
			}
			if from != *update.Assignee {
				d.SetLease(nil)
			}
			d.SetAssignee(*update.Assignee)
			if from != *update.Assignee {
				d.AppendProvenance(actor, "assignee changed", auditChangeText("from="+from, "to="+*update.Assignee, "force="+fmt.Sprint(update.Force), update.Reason), at)
			}
		}
		if update.Parent != nil {
			d.SetParent(*update.Parent)
		}
		if update.Status != nil {
			d.SetStatus(*update.Status)
			d.AppendProvenance(actor, "bulk transitioned to "+*update.Status, details, at)
		} else if !hasDedicatedAudit(d, update) {
			d.AppendProvenance(actor, "bulk updated", details, at)
		}
	}
	if err := tx.saveMany(docs); err != nil {
		return nil, err
	}
	return docs, nil
}

func hasDedicatedAudit(d *Doc, update BulkUpdate) bool {
	if update.ProjectID != nil {
		// Current value matches necessarily after mutation, so inspect detail count through
		// the field presence; an idempotent project move still needs no generic audit when
		// it was the only requested mutation.
		if update.Type == nil && update.Importance == nil && update.Priority == nil && update.Labels == nil && update.Assignee == nil && update.Parent == nil {
			return true
		}
	}
	if update.Assignee != nil && update.Type == nil && update.Importance == nil && update.Priority == nil && update.Labels == nil && update.Parent == nil {
		return true
	}
	return false
}

func bulkUpdateDetails(update BulkUpdate) string {
	var details []string
	if update.ProjectID != nil {
		details = append(details, "project_id="+*update.ProjectID)
	}
	if update.Type != nil {
		details = append(details, "type="+*update.Type)
	}
	if update.Importance != nil {
		details = append(details, "importance="+*update.Importance)
	}
	if update.Priority != nil {
		details = append(details, "priority="+*update.Priority)
	}
	if update.Assignee != nil {
		details = append(details, "assignee="+*update.Assignee)
	}
	if update.Parent != nil {
		details = append(details, "parent="+*update.Parent)
	}
	if update.Status != nil {
		details = append(details, "status="+*update.Status)
	}
	if update.Labels != nil {
		details = append(details, "labels="+strings.Join(*update.Labels, ","))
	}
	if update.Reason != "" {
		details = append(details, "reason="+update.Reason)
	}
	return strings.Join(details, "; ")
}

func auditChangeText(parts ...string) string {
	parts = slices.DeleteFunc(parts, func(part string) bool { return strings.TrimSpace(part) == "" })
	return strings.Join(parts, "; ")
}

type fileBackup struct {
	path string
	data []byte
}

// saveMany applies pre-mutated Docs with snapshot compensation. It snapshots all current
// files before the first write; normal validation failures happen earlier. A rare
// filesystem failure half-way through the batch is compensated best-effort and returns
// ErrRollbackIncomplete if any restore fails.
func (tx *WriteTx) saveMany(docs []*Doc) error {
	backups := make([]fileBackup, 0, len(docs))
	for _, d := range docs {
		path, err := tx.store.taskFilePath(d.Task.ID, false, true, true)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		backups = append(backups, fileBackup{path: path, data: data})
	}
	for i, d := range docs {
		if err := tx.SaveTask(d); err != nil {
			lastPublished := i - 1
			if errors.Is(err, ErrAtomicWritePublished) {
				// Parent-directory Sync/verification failures happen after the final
				// rename, so the current document can be visible even though SaveTask
				// returned an error.
				lastPublished = i
			}
			rollbackErr := tx.rollbackFileBackups(backups, lastPublished)
			primary := fmt.Errorf("store: bulk save failed: %w", err)
			if rollbackErr != nil {
				return errors.Join(primary, rollbackErr)
			}
			return fmt.Errorf("store: bulk save rolled back: %w", err)
		}
	}
	return nil
}

func (tx *WriteTx) rollbackFileBackups(backups []fileBackup, lastPublished int) error {
	if lastPublished < 0 {
		return nil
	}
	if lastPublished >= len(backups) {
		lastPublished = len(backups) - 1
	}
	var rollbackErrors []error
	for i := lastPublished; i >= 0; i-- {
		if err := tx.store.writeAtomic(backups[i].path, backups[i].data); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore %s: %w", backups[i].path, err))
		}
	}
	if len(rollbackErrors) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrRollbackIncomplete, errors.Join(rollbackErrors...))
}
