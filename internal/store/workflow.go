package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"carbon/internal/task"
	tasktypes "carbon/internal/types"
)

// CreateTaskType persists one explicitly-created custom task type. The whole read,
// quota/rate validation, and write happens beneath the repository lock, so a burst of
// concurrent clients cannot circumvent the policy by reading the same old config.
func (s *Store) CreateTaskType(ctx context.Context, actor, key string, at time.Time) (tasktypes.Definition, error) {
	return s.CreateTaskTypeWithDisplayName(ctx, actor, key, "", at)
}

// CreateTaskTypeWithDisplayName persists a stable custom type key plus an optional
// human-readable Unicode display name. The key is what task.Type stores.
func (s *Store) CreateTaskTypeWithDisplayName(ctx context.Context, actor, key, displayName string, at time.Time) (tasktypes.Definition, error) {
	var created tasktypes.Definition
	err := s.Write(ctx, actor, "create task type", func(tx *WriteTx) error {
		cfg, err := tx.Config()
		if err != nil {
			return err
		}
		next, definition, err := cfg.WithTaskTypeDisplayName(key, displayName, actor, at)
		if err != nil {
			return err
		}
		if err := tx.SaveConfig(next); err != nil {
			return err
		}
		created = definition
		return nil
	})
	return created, err
}

// ListDocsByProjectID is a read-fresh project filter. An empty project id intentionally
// selects legacy/cluster-wide tasks; callers that need all projects should call ListDocs.
func (s *Store) ListDocsByProjectID(projectID string) ([]*Doc, error) {
	docs, err := s.ListDocs()
	if err != nil {
		return nil, err
	}
	out := make([]*Doc, 0, len(docs))
	for _, d := range docs {
		if d.Task.ProjectID == projectID {
			out = append(out, d)
		}
	}
	return out, nil
}

// ListByProjectID is the typed-map counterpart of ListDocsByProjectID.
func (s *Store) ListByProjectID(projectID string) (map[string]task.Task, error) {
	docs, err := s.ListDocsByProjectID(projectID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]task.Task, len(docs))
	for _, d := range docs {
		out[d.Task.ID] = d.Task
	}
	return out, nil
}

// normalizeIDs checks a caller-provided batch once and returns deterministic id order.
func normalizeIDs(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("no task ids supplied")
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if err := validateTaskID(id); err != nil {
			return nil, err
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func batchText(parts ...string) string {
	nonempty := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			nonempty = append(nonempty, part)
		}
	}
	return strings.Join(nonempty, "; ")
}
