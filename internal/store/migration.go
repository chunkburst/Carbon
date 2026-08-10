package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"carbon/internal/task"

	"gopkg.in/yaml.v3"
)

// ImportTaskInput describes a controlled, lossless task import. TargetID may equal the
// source id when the shared pool has no collision; TargetProjectID nil preserves the
// source scope, while non-nil empty deliberately imports as cluster-wide.
type ImportTaskInput struct {
	Doc             *Doc
	TargetID        string
	TargetProjectID *string
}

// ImportReceipt is an auditable mapping suitable for a home/cluster migration manifest.
// It records the new opaque version after the import write, not a mutable revision number.
type ImportReceipt struct {
	SourceID   string `json:"source_id"`
	TargetID   string `json:"target_id"`
	ProjectID  string `json:"project_id,omitempty"`
	ImportedAt string `json:"imported_at"`
	Version    string `json:"version"`
	ETag       string `json:"etag"`
}

// ImportTask copies a parsed Doc into this store without round-tripping its frontmatter
// through a plain struct. Unknown YAML fields, comments/nodes, and markdown body survive;
// only the explicitly requested id/project fields and one provenance receipt are changed.
func (s *Store) ImportTask(ctx context.Context, actor string, input ImportTaskInput) (*Doc, ImportReceipt, error) {
	if input.Doc == nil {
		return nil, ImportReceipt{}, errors.New("store: import task document is required")
	}
	targetID := input.TargetID
	if targetID == "" {
		targetID = input.Doc.Task.ID
	}
	if err := validateTaskID(targetID); err != nil {
		return nil, ImportReceipt{}, err
	}
	copy := cloneDoc(input.Doc)
	if err := copy.SetIDForMigration(targetID); err != nil {
		return nil, ImportReceipt{}, err
	}
	if input.TargetProjectID != nil {
		copy.SetProjectID(*input.TargetProjectID)
	}
	now := time.Now().UTC()
	copy.AppendProvenance(actor, "imported", "source_id="+input.Doc.Task.ID+"; target_id="+targetID, now)
	var receipt ImportReceipt
	err := s.Write(ctx, actor, "import task", func(tx *WriteTx) error {
		active, err := tx.store.taskFilePath(targetID, true, false, true)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(active); err == nil {
			return fmt.Errorf("%w: active task id %s already exists", ErrConflict, targetID)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		trashed, err := tx.store.trashFilePath(targetID, false, false, true)
		if err == nil {
			if _, statErr := os.Lstat(trashed); statErr == nil {
				return fmt.Errorf("%w: trashed task id %s already exists", ErrConflict, targetID)
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return statErr
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// Imported docs are new to this filesystem; do not compare their source-store hash
		// against a target path. saveToPath still performs an atomic managed write.
		if err := tx.store.saveToPath(copy, active, false); err != nil {
			return err
		}
		receipt = ImportReceipt{SourceID: input.Doc.Task.ID, TargetID: targetID, ProjectID: copy.Task.ProjectID,
			ImportedAt: now.Format(time.RFC3339), Version: copy.Version(), ETag: copy.ETag()}
		return nil
	})
	if err != nil {
		return nil, ImportReceipt{}, err
	}
	return copy, receipt, nil
}

// cloneDoc deep-copies every YAML node rather than marshal/unmarshal round-tripping it.
// yaml.Node contains pointers, so a shallow copy would let a migration mutate the source
// document's unknown fields in memory.
func cloneDoc(source *Doc) *Doc {
	copy := &Doc{
		Task:       cloneTask(source.Task),
		Provenance: slices.Clone(source.Provenance),
		Body:       source.Body,
		node:       cloneNode(source.node),
		version:    "", // imported content has no target-store baseline
	}
	copy.Task.Version = ""
	return copy
}

func cloneTask(value task.Task) task.Task {
	value.Deps = slices.Clone(value.Deps)
	value.Labels = slices.Clone(value.Labels)
	value.Checks = slices.Clone(value.Checks)
	value.Evidence = cloneEvidence(value.Evidence)
	value.PendingClaims = slices.Clone(value.PendingClaims)
	value.Lease = cloneLease(value.Lease)
	value.Trash = cloneTrashInfo(value.Trash)
	return value
}

func cloneNode(value yaml.Node) yaml.Node {
	copy := value
	copy.Content = make([]*yaml.Node, len(value.Content))
	for i, child := range value.Content {
		if child == nil {
			continue
		}
		cloned := cloneNode(*child)
		copy.Content[i] = &cloned
	}
	// Frontmatter aliases are unusual; retain their original pointer rather than risking
	// an infinite recursive clone. YAML aliases are never mutated by our surgical setters.
	copy.Alias = value.Alias
	return copy
}
