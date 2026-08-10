// Package trash is the policy layer over store's graph-safe soft-delete primitives.
// It is intentionally the only layer that triggers retention GC, and only after an
// operation has actually placed at least one new task into trash.
package trash

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"carbon/internal/store"
	"carbon/internal/task"
)

type Manager struct {
	Store *store.Store
	Now   func() time.Time
}

func New(s *store.Store, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{Store: s, Now: now}
}

func (m *Manager) now() time.Time {
	if m.Now == nil {
		return time.Now().UTC()
	}
	return m.Now().UTC()
}

type Input struct {
	ID               string
	IDs              []string
	Actor            string
	Reason           string
	ExpectedVersion  string
	ExpectedVersions map[string]string
}

type Filter struct {
	Labels    []string
	Assignee  string
	ProjectID *string
}

type RestoreInput struct {
	ID    string
	Actor string
	// TargetProjectID nil restores to the original project; non-nil empty means
	// deliberate cluster-wide restoration.
	TargetProjectID *string
	ExpectedVersion string
}

type Entry struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	ProjectID  string         `json:"project_id,omitempty"`
	Type       string         `json:"type,omitempty"`
	Importance string         `json:"importance,omitempty"`
	Assignee   string         `json:"assignee,omitempty"`
	Labels     []string       `json:"labels,omitempty"`
	Trash      task.TrashInfo `json:"trash"`
	Version    string         `json:"version"`
	ETag       string         `json:"etag"`
}

// Trash soft-deletes exactly one task, rejecting accidentally broad input. A successful
// new entry triggers exactly one retention GC pass.
func (m *Manager) Trash(ctx context.Context, input Input) (Entry, error) {
	ids := input.IDs
	if input.ID != "" {
		if len(ids) > 0 {
			return Entry{}, errors.New("trash input cannot contain both id and ids")
		}
		ids = []string{input.ID}
	}
	if len(ids) != 1 {
		return Entry{}, errors.New("trash requires exactly one task id")
	}
	entries, err := m.TrashMany(ctx, Input{IDs: ids, Actor: input.Actor, Reason: input.Reason, ExpectedVersion: input.ExpectedVersion, ExpectedVersions: input.ExpectedVersions})
	if err != nil {
		return Entry{}, err
	}
	return entries[0], nil
}

// TrashMany soft-deletes a same-transaction batch. It supports removing a parent and
// its children/dependents together because store validates the graph after the entire
// candidate set is removed.
func (m *Manager) TrashMany(ctx context.Context, input Input) ([]Entry, error) {
	if m.Store == nil {
		return nil, errors.New("trash manager has no store")
	}
	ids := input.IDs
	if len(ids) == 0 && input.ID != "" {
		ids = []string{input.ID}
	}
	if len(ids) == 0 {
		return nil, errors.New("trash requires task ids")
	}
	expected := make(map[string]string, len(input.ExpectedVersions)+1)
	for id, version := range input.ExpectedVersions {
		expected[id] = version
	}
	if input.ID != "" && input.ExpectedVersion != "" {
		expected[input.ID] = input.ExpectedVersion
	}
	// Resolve retention before mutation. A config error must never report a failed delete
	// after it has already moved files; the default lives in Config for old repos.
	cfg, err := m.Store.Config()
	if err != nil {
		return nil, err
	}
	now := m.now()
	docs, err := m.Store.TrashTasks(ctx, input.Actor, ids, input.Reason, expected, now)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(docs))
	for _, doc := range docs {
		entries = append(entries, entryFromDoc(doc))
	}
	// Only this new-entry path invokes GC. Listing, restoring, emptying, and manual GC do
	// not accidentally collect old data.
	if _, err := m.Store.GCTrash(ctx, "system:trash", now.Add(-cfg.TrashRetentionDuration())); err != nil {
		return entries, err
	}
	return entries, nil
}

// TrashByFilter selects active tasks by all requested labels and/or assignee, then passes
// the exact selection through the same batch primitive. ProjectID is a pointer so an empty
// project id can be selected deliberately.
func (m *Manager) TrashByFilter(ctx context.Context, actor, reason string, filter Filter) ([]Entry, error) {
	if m.Store == nil {
		return nil, errors.New("trash manager has no store")
	}
	docs, err := m.Store.ListDocs()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	for _, doc := range docs {
		if filter.Assignee != "" && doc.Task.Assignee != filter.Assignee {
			continue
		}
		if filter.ProjectID != nil && doc.Task.ProjectID != *filter.ProjectID {
			continue
		}
		if !hasAllLabels(doc.Task.Labels, filter.Labels) {
			continue
		}
		ids = append(ids, doc.Task.ID)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return m.TrashMany(ctx, Input{IDs: ids, Actor: actor, Reason: reason})
}

func hasAllLabels(have, want []string) bool {
	for _, label := range want {
		if !slices.Contains(have, label) {
			return false
		}
	}
	return true
}

func (m *Manager) List() ([]Entry, error) {
	if m.Store == nil {
		return nil, errors.New("trash manager has no store")
	}
	docs, err := m.Store.ListTrashDocs()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(docs))
	for _, doc := range docs {
		out = append(out, entryFromDoc(doc))
	}
	return out, nil
}

func (m *Manager) Restore(ctx context.Context, input RestoreInput) (*store.Doc, error) {
	if m.Store == nil {
		return nil, errors.New("trash manager has no store")
	}
	return m.Store.RestoreTrashTask(ctx, input.Actor, input.ID, input.TargetProjectID, input.ExpectedVersion, m.now())
}

// Empty is the one-click permanent clear operation. It does not perform retention GC;
// it asks store to purge exactly the entries the user has explicitly requested to clear.
func (m *Manager) Empty(ctx context.Context, actor string) (int, error) {
	if m.Store == nil {
		return 0, errors.New("trash manager has no store")
	}
	return m.Store.EmptyTrash(ctx, actor)
}

// EmptyProject permanently clears this project's trash only. Cluster-wide entries have
// no project_id, so they are excluded by default and require the explicit
// includeClusterWide acknowledgement. Empty remains the administrator-only full-cluster
// primitive.
func (m *Manager) EmptyProject(ctx context.Context, actor, projectID string, includeClusterWide bool) (int, error) {
	if m.Store == nil {
		return 0, errors.New("trash manager has no store")
	}
	return m.Store.EmptyTrashByProject(ctx, actor, projectID, includeClusterWide)
}

// GC exposes an explicit collection pass for maintenance tools. Normal workflows should
// rely on Trash/TrashMany, which invoke it only after new trash entries.
func (m *Manager) GC(ctx context.Context) ([]string, error) {
	if m.Store == nil {
		return nil, errors.New("trash manager has no store")
	}
	cfg, err := m.Store.Config()
	if err != nil {
		return nil, err
	}
	return m.Store.GCTrash(ctx, "system:trash", m.now().Add(-cfg.TrashRetentionDuration()))
}

func entryFromDoc(doc *store.Doc) Entry {
	info := task.TrashInfo{}
	if doc.Task.Trash != nil {
		info = *doc.Task.Trash
	}
	return Entry{ID: doc.Task.ID, Title: doc.Task.Title, ProjectID: doc.Task.ProjectID,
		Type: doc.Task.Type, Importance: doc.Task.Importance, Assignee: doc.Task.Assignee,
		Labels: slices.Clone(doc.Task.Labels), Trash: info, Version: doc.Version(), ETag: doc.ETag()}
}

// String is intentionally concise when an Entry appears in a log/error.
func (e Entry) String() string { return fmt.Sprintf("%s (%s)", e.ID, e.Trash.TrashedAt) }
