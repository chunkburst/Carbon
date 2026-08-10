package home

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"carbon/internal/store"
)

var (
	// ErrProjectClearNameConfirmation is deliberately distinct from ordinary project
	// lookup errors. The HTTP adapter can report a typed confirmation mismatch without
	// revealing or mutating task data.
	ErrProjectClearNameConfirmation = errors.New("project name confirmation does not match")
)

// ClearProjectTaskDataRequest binds a destructive project-data clear to immutable
// catalog identity and the exact current display name. ProjectID must be the stable id;
// display names/slugs are intentionally not accepted here because they can be renamed.
type ClearProjectTaskDataRequest struct {
	ProjectID        string
	ConfirmationName string
	Actor            string
}

// ClearProjectTaskDataResult keeps catalog identity outside the Store result. Home owns
// the manifest lock and confirms a clustered or standalone project before the Store
// changes its private data root.
type ClearProjectTaskDataResult struct {
	Project    Project
	ClusterID  string
	Standalone bool
	Data       store.ClearProjectTaskDataResult
}

// ClearProjectTaskData resolves the fresh manifest, checks an exact GitHub-style name
// confirmation, derives a safe private data root, and calls Store's compound clear all
// beneath one Home lock. It never writes the manifest, catalog presentation, icon,
// source binding, Worker registry, Work Logs, templates, or views.
func ClearProjectTaskData(ctx context.Context, main string, request ClearProjectTaskDataRequest) (ClearProjectTaskDataResult, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return ClearProjectTaskDataResult{}, err
	}
	var result ClearProjectTaskDataResult
	err = withLock(root, func() error {
		h, err := Open(root)
		if err != nil {
			return err
		}
		manifest, err := h.Manifest()
		if err != nil {
			return err
		}
		result, err = clearProjectTaskDataLocked(ctx, h, &manifest, request)
		return err
	})
	return result, err
}

// clearProjectTaskDataLocked is the shared destructive boundary for a current Home
// manifest. The caller must hold that Home's lock. Store takes its own lock only after
// this function has resolved the project's private data root, preserving the required
// Home -> Store lock order for both a task-data clear and project deletion.
func clearProjectTaskDataLocked(ctx context.Context, h *Home, manifest *Manifest, request ClearProjectTaskDataRequest) (ClearProjectTaskDataResult, error) {
	return clearProjectTaskDataLockedWithFinalizer(ctx, h, manifest, request, nil)
}

// clearProjectTaskDataLockedWithFinalizer preserves the same validation and Home ->
// Store lock order as clearProjectTaskDataLocked. When provided, finalizer runs while
// Store's repository write lock remains held. Project deletion uses that narrow hook to
// publish its already-validated manifest before a blocked task writer can enter the
// target store.
func clearProjectTaskDataLockedWithFinalizer(
	ctx context.Context,
	h *Home,
	manifest *Manifest,
	request ClearProjectTaskDataRequest,
	finalizer func(store.ClearProjectTaskDataResult) error,
) (ClearProjectTaskDataResult, error) {
	if h == nil || manifest == nil {
		return ClearProjectTaskDataResult{}, ErrNotInitialized
	}
	projectID := request.ProjectID
	if projectID == "" || strings.TrimSpace(projectID) != projectID {
		return ClearProjectTaskDataResult{}, fmt.Errorf("%w: empty or non-canonical stable project id", ErrProjectNotFound)
	}
	project, cluster, standalone, err := findProjectInManifest(manifest, projectID)
	if err != nil {
		return ClearProjectTaskDataResult{}, err
	}
	// `findProjectInManifest` supports human references for read-only catalog APIs;
	// this destructive entry point requires the stable id exactly.
	if project.ID != projectID {
		return ClearProjectTaskDataResult{}, fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
	}
	if request.ConfirmationName != project.Name {
		return ClearProjectTaskDataResult{}, fmt.Errorf("%w: expected %q", ErrProjectClearNameConfirmation, project.Name)
	}
	dataRoot, err := resolvedProjectDataRoot(h.CarbonRoot, project, cluster, standalone)
	if err != nil {
		return ClearProjectTaskDataResult{}, err
	}
	cleared, err := store.New(dataRoot).ClearProjectTaskDataWithFinalizer(ctx, request.Actor, store.ClearProjectTaskDataOptions{
		ProjectID:  project.ID,
		Standalone: standalone,
	}, finalizer)
	if err != nil {
		return ClearProjectTaskDataResult{}, err
	}
	result := ClearProjectTaskDataResult{Project: *project, Standalone: standalone, Data: cleared}
	if cluster != nil {
		result.ClusterID = cluster.ID
	}
	return result, nil
}
