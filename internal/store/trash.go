package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"carbon/internal/task"
)

// ErrProjectIDRequired protects project-scoped destructive operations from falling
// back to the shared cluster-wide pool when a caller has not resolved its scope.
var ErrProjectIDRequired = errors.New("project id is required")

type trashMoveBackup struct {
	source string
	dest   string
	data   []byte
}

// GetTrash reads one soft-deleted task without validating it against the live dependency
// graph (a trashed task is expected to reference other trashed tasks sometimes).
func (s *Store) GetTrash(id string) (*Doc, error) {
	if err := s.projectClearReadBarrier(); err != nil {
		return nil, err
	}
	if err := validateTaskID(id); err != nil {
		return nil, err
	}
	path, err := s.trashFilePath(id, false, true, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrTrashNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("store: resolve trash %s: %w", id, err)
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrTrashNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	d, err := parse(b)
	if err != nil {
		return nil, err
	}
	if d.Task.ID != id {
		return nil, fmt.Errorf("%w: trash file %s declares %q", ErrInvalidID, id, d.Task.ID)
	}
	d.version = hashBytes(b)
	d.Task.Version = d.version
	return d, nil
}

// ListTrashDocs returns all soft-deleted task documents in id order. A repository with no
// trash directory is a valid old repository and yields an empty result.
func (s *Store) ListTrashDocs() ([]*Doc, error) {
	if err := s.projectClearReadBarrier(); err != nil {
		return nil, err
	}
	dir, err := s.managedDir(false, carbonStoreDir, "trash")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	docs := make([]*Doc, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".md")
		if err := validateTaskID(id); err != nil {
			return nil, err
		}
		path, err := s.managedFile(dir, entry.Name(), true, false)
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		d, err := parse(b)
		if err != nil {
			return nil, err
		}
		if d.Task.ID != id {
			return nil, fmt.Errorf("%w: trash file %s declares %q", ErrInvalidID, entry.Name(), d.Task.ID)
		}
		d.version = hashBytes(b)
		d.Task.Version = d.version
		docs = append(docs, d)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Task.ID < docs[j].Task.ID })
	return docs, nil
}

// TrashTasks soft-deletes a validated set with snapshot compensation. expectedVersions is
// optional and accepts per-task raw Versions or ETags. A failed compensation is reported
// as ErrRollbackIncomplete with the original error; callers must not assume an atomic
// no-op. Calls do NOT run GC: trash.Manager is the sole caller that triggers collection,
// ensuring GC happens only after a new trash entry.
func (s *Store) TrashTasks(ctx context.Context, actor string, ids []string, reason string, expectedVersions map[string]string, at time.Time) ([]*Doc, error) {
	ids, err := normalizeIDs(ids)
	if err != nil {
		return nil, err
	}
	var moved []*Doc
	err = s.Write(ctx, actor, "trash tasks", func(tx *WriteTx) error {
		all, err := tx.store.List()
		if err != nil {
			return err
		}
		candidate := make(map[string]bool, len(ids))
		for _, id := range ids {
			if _, ok := all[id]; !ok {
				return fmt.Errorf("%w: %s", ErrNotFound, id)
			}
			candidate[id] = true
		}
		if err := validateTrashSet(all, candidate); err != nil {
			return err
		}

		docs := make([]*Doc, 0, len(ids))
		backups := make([]trashMoveBackup, 0, len(ids))
		for _, id := range ids {
			doc, err := tx.GetTask(id)
			if err != nil {
				return err
			}
			if expected := expectedVersions[id]; expected != "" {
				if err := doc.MatchVersion(expected); err != nil {
					return err
				}
			}
			source, err := tx.store.taskFilePath(id, false, true, true)
			if err != nil {
				return err
			}
			dest, err := tx.store.trashFilePath(id, true, false, true)
			if err != nil {
				return err
			}
			if _, err := os.Lstat(dest); err == nil {
				return fmt.Errorf("%w: %s", ErrAlreadyTrashed, id)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			raw, err := os.ReadFile(source)
			if err != nil {
				return err
			}
			info := &task.TrashInfo{TrashedAt: at.UTC().Format(time.RFC3339), TrashedBy: actor, Reason: reason, OriginalProjectID: doc.Task.ProjectID}
			doc.SetTrashInfo(info)
			doc.AppendProvenance(actor, "trashed", reason, at)
			docs = append(docs, doc)
			backups = append(backups, trashMoveBackup{source: source, dest: dest, data: raw})
		}

		movedCount := 0
		for i, doc := range docs {
			backup := backups[i]
			if err := tx.store.saveToPath(doc, backup.source, true); err != nil {
				rollbackErr := tx.store.rollbackTrashMove(backups, movedCount, i+1)
				return joinRollbackError(err, rollbackErr)
			}
			if err := tx.store.renameManaged(backup.source, backup.dest); err != nil {
				movedForRollback := movedCount
				if errors.Is(err, ErrAtomicWritePublished) {
					movedForRollback++
				}
				rollbackErr := tx.store.rollbackTrashMove(backups, movedForRollback, i+1)
				return joinRollbackError(fmt.Errorf("store: move %s to trash: %w", doc.Task.ID, err), rollbackErr)
			}
			movedCount++
		}
		moved = docs
		return nil
	})
	return moved, err
}

func validateTrashSet(all map[string]task.Task, candidate map[string]bool) error {
	for id, current := range all {
		if candidate[id] {
			continue
		}
		if current.Parent != "" && candidate[current.Parent] {
			return fmt.Errorf("%w: %s blocks trashing parent %s", task.ErrHasChildren, id, current.Parent)
		}
		for _, dep := range current.Deps {
			if candidate[dep] {
				return fmt.Errorf("%w: %s blocks trashing dependency %s", task.ErrHasDependents, id, dep)
			}
		}
	}
	return nil
}

func (s *Store) rollbackTrashMove(backups []trashMoveBackup, moved, touched int) error {
	if moved > len(backups) {
		moved = len(backups)
	}
	if touched > len(backups) {
		touched = len(backups)
	}
	var rollbackErrors []error
	for i := moved - 1; i >= 0; i-- {
		if err := s.renameManaged(backups[i].dest, backups[i].source); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("move %s back from trash: %w", backups[i].source, err))
		}
	}
	for i := 0; i < touched; i++ {
		if err := s.writeAtomic(backups[i].source, backups[i].data); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore %s: %w", backups[i].source, err))
		}
	}
	if len(rollbackErrors) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrRollbackIncomplete, errors.Join(rollbackErrors...))
}

func joinRollbackError(primary, rollback error) error {
	if rollback == nil {
		return primary
	}
	return errors.Join(primary, rollback)
}

// RestoreTrashTask restores one task, optionally overriding its project id. targetProjectID
// nil means retain the original task project; a non-nil empty string is an explicit
// cluster-wide restore.
func (s *Store) RestoreTrashTask(ctx context.Context, actor, id string, targetProjectID *string, expectedVersion string, at time.Time) (*Doc, error) {
	if err := validateTaskID(id); err != nil {
		return nil, err
	}
	var restored *Doc
	err := s.Write(ctx, actor, "restore trashed task", func(tx *WriteTx) error {
		source, err := tx.store.trashFilePath(id, false, true, true)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrTrashNotFound, id)
		}
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		doc, err := parse(raw)
		if err != nil {
			return err
		}
		if doc.Task.ID != id {
			return fmt.Errorf("%w: trash file %s declares %q", ErrInvalidID, id, doc.Task.ID)
		}
		doc.version = hashBytes(raw)
		doc.Task.Version = doc.version
		if err := doc.MatchVersion(expectedVersion); err != nil {
			return err
		}
		dest, err := tx.store.taskFilePath(id, true, false, true)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(dest); err == nil {
			return fmt.Errorf("%w: cannot restore %s; active task already exists", ErrConflict, id)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if targetProjectID != nil {
			doc.SetProjectID(*targetProjectID)
		}
		doc.SetTrashInfo(nil)
		doc.AppendProvenance(actor, "restored from trash", restoreText(targetProjectID), at)
		if err := tx.store.saveToPath(doc, source, true); err != nil {
			rollbackErr := tx.store.rollbackTrashMove([]trashMoveBackup{{source: source, dest: dest, data: raw}}, 0, 1)
			return joinRollbackError(err, rollbackErr)
		}
		if err := tx.store.renameManaged(source, dest); err != nil {
			movedForRollback := 0
			if errors.Is(err, ErrAtomicWritePublished) {
				movedForRollback = 1
			}
			rollbackErr := tx.store.rollbackTrashMove([]trashMoveBackup{{source: source, dest: dest, data: raw}}, movedForRollback, 1)
			return joinRollbackError(fmt.Errorf("store: restore %s: %w", id, err), rollbackErr)
		}
		restored = doc
		return nil
	})
	return restored, err
}

func restoreText(target *string) string {
	if target == nil {
		return "project_id=original"
	}
	return "project_id=" + *target
}

// PurgeTrash irreversibly removes the named soft-deleted files. It is deliberately
// separate from DeleteTask so normal delete routes can remain soft-delete only.
func (s *Store) PurgeTrash(ctx context.Context, actor string, ids []string) (int, error) {
	ids, err := normalizeIDs(ids)
	if err != nil {
		return 0, err
	}
	purged := 0
	err = s.Write(ctx, actor, "purge trash", func(tx *WriteTx) error {
		for _, id := range ids {
			path, err := tx.store.trashFilePath(id, false, true, true)
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%w: %s", ErrTrashNotFound, id)
			}
			if err != nil {
				return err
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			purged++
		}
		return nil
	})
	return purged, err
}

// EmptyTrash irreversibly purges every current trash entry in one locked operation.
// It is intentionally the cluster-administrator primitive; project-scoped callers must
// use EmptyTrashByProject instead.
func (s *Store) EmptyTrash(ctx context.Context, actor string) (int, error) {
	return s.emptyTrashMatching(ctx, actor, "empty cluster trash", func(*Doc) bool { return true })
}

// EmptyTrashByProject permanently removes only trash entries owned by projectID. An
// empty project id is rejected rather than interpreted as cluster-wide, and
// includeClusterWide must be explicitly true before project-empty shared work is included.
// Selection, path preflight, and deletion are serialized under one repository write lock
// and the actor/scope are recorded in the lock operation diagnostic.
func (s *Store) EmptyTrashByProject(ctx context.Context, actor, projectID string, includeClusterWide bool) (int, error) {
	if strings.TrimSpace(projectID) == "" {
		return 0, ErrProjectIDRequired
	}
	operation := fmt.Sprintf("empty project trash project_id=%s include_cluster_wide=%t", projectID, includeClusterWide)
	return s.emptyTrashMatching(ctx, actor, operation, func(doc *Doc) bool {
		return doc.Task.ProjectID == projectID || (includeClusterWide && doc.Task.ProjectID == "")
	})
}

// emptyTrashMatching selects and purges beneath one Store.Write call. It deliberately
// reads the trash collection after acquiring the lock, so another Carbon process cannot
// restore/add an entry between selection and permanent deletion. Every selected path is
// resolved before the first unlink to avoid partial work from a bad managed path.
func (s *Store) emptyTrashMatching(ctx context.Context, actor, operation string, match func(*Doc) bool) (int, error) {
	purged := 0
	err := s.Write(ctx, actor, operation, func(tx *WriteTx) error {
		docs, err := tx.store.ListTrashDocs()
		if err != nil {
			return err
		}
		paths := make([]string, 0, len(docs))
		for _, doc := range docs {
			if !match(doc) {
				continue
			}
			path, err := tx.store.trashFilePath(doc.Task.ID, false, true, true)
			if err != nil {
				return err
			}
			paths = append(paths, path)
		}
		for _, path := range paths {
			if err := os.Remove(path); err != nil {
				return err
			}
			purged++
		}
		return nil
	})
	return purged, err
}

// GCTrash permanently removes only entries whose trashed_at is at or before cutoff. It
// does not run implicitly here; callers decide when a new trash operation should trigger
// collection. Invalid/missing timestamps are retained rather than risk data loss.
func (s *Store) GCTrash(ctx context.Context, actor string, cutoff time.Time) ([]string, error) {
	docs, err := s.ListTrashDocs()
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, doc := range docs {
		if doc.Task.Trash == nil || doc.Task.Trash.TrashedAt == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, doc.Task.Trash.TrashedAt)
		if err != nil {
			continue
		}
		if !at.After(cutoff) {
			ids = append(ids, doc.Task.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if _, err := s.PurgeTrash(ctx, actor, ids); err != nil {
		return nil, err
	}
	return ids, nil
}

// TrashPath exposes the managed location for diagnostics only. Mutations must go through
// the trash primitives so graph validation and provenance are never bypassed.
func (s *Store) TrashPath(id string) (string, error) {
	if err := validateTaskID(id); err != nil {
		return "", err
	}
	return s.trashFilePath(id, false, false, true)
}

// IsTrashFile is a small helper for watchers that need to classify a managed path.
func (s *Store) IsTrashFile(path string) bool {
	dir, err := s.managedDir(false, carbonStoreDir, "trash")
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
