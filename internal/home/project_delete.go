package home

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"carbon/internal/store"
)

const projectDeleteReceiptVersion = 1

var (
	// ErrProjectDeleteNameConfirmation is deliberately separate from the task-data
	// clear confirmation. HTTP can return the exact GitHub-style confirmation failure
	// without treating it as a malformed or unsafe Home.
	ErrProjectDeleteNameConfirmation = errors.New("project name confirmation does not match")
	// ErrProjectDeleteRecovery means task data was safely cleared but the catalog
	// publication boundary could not be written. A durable receipt remains below the
	// Carbon Home; retrying the same delete finishes catalog removal under the Home lock.
	ErrProjectDeleteRecovery = errors.New("project deletion requires recovery")
)

// DeleteProjectRequest removes one stable project entry from the current Home manifest.
// DeleteData opts into the narrower compound task-data clear first; it never authorizes
// deleting Project.Source.Path, a standalone data root, a cluster root, or configuration.
type DeleteProjectRequest struct {
	ProjectID        string
	ConfirmationName string
	DeleteData       bool
	Actor            string
}

// DeleteProjectResult reports the catalog entry removed and, when requested, the exact
// task-shaped data cleared before it. Physical Carbon roots remain intact by design.
type DeleteProjectResult struct {
	Project    Project
	ClusterID  string
	Standalone bool
	DeleteData bool
	Data       *store.ClearProjectTaskDataResult
}

// projectDeleteReceipt is a minimal durable recovery marker. It contains no source path
// and no task content: the stable project ID is enough to re-resolve a fresh manifest.
// A prepared marker is written before Store changes task data. If manifest publication
// subsequently fails, retrying DeleteProject clears any newly written target tasks again
// and then atomically removes the catalog entry.
type projectDeleteReceipt struct {
	Version           int    `json:"version"`
	State             string `json:"state"`
	ProjectID         string `json:"projectId"`
	DeleteData        bool   `json:"deleteData"`
	CreatedAt         string `json:"createdAt"`
	TaskDataClearedAt string `json:"taskDataClearedAt,omitempty"`
	TaskDataReceiptID string `json:"taskDataReceiptId,omitempty"`
}

// DeleteProject resolves and removes a fresh stable-id project entry beneath one Home
// lock. The optional data clear happens under the same lock before manifest publication,
// so Home -> Store is the only lock order. It intentionally leaves catalog presentation
// assets as harmless orphans; deleting them first would make a manifest rollback unsafe.
func DeleteProject(ctx context.Context, main string, request DeleteProjectRequest) (DeleteProjectResult, error) {
	return deleteProjectWithManifestWriter(ctx, main, request, writeManifest)
}

// deleteProjectWithManifestWriter is a narrow test seam around the final publication.
// Production always passes writeManifest; the seam lets the Home contract prove that a
// failed manifest write leaves a durable recovery receipt instead of claiming success.
func deleteProjectWithManifestWriter(
	ctx context.Context,
	main string,
	request DeleteProjectRequest,
	publishManifest func(string, Manifest) error,
) (DeleteProjectResult, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return DeleteProjectResult{}, err
	}
	if publishManifest == nil {
		return DeleteProjectResult{}, fmt.Errorf("%w: missing manifest publisher", ErrProjectDeleteRecovery)
	}
	projectID := request.ProjectID
	if projectID == "" || strings.TrimSpace(projectID) != projectID {
		return DeleteProjectResult{}, fmt.Errorf("%w: empty or non-canonical stable project id", ErrProjectNotFound)
	}

	var result DeleteProjectResult
	err = withLock(root, func() error {
		h, err := Open(root)
		if err != nil {
			return err
		}
		manifest, err := h.Manifest()
		if err != nil {
			return err
		}
		project, cluster, standalone, err := findProjectInManifest(&manifest, projectID)
		if err != nil {
			return err
		}
		// Stable IDs are the only valid destructive target. findProjectInManifest also
		// supports human references for discovery, which must never become delete aliases.
		if project.ID != projectID {
			return fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
		}
		if request.ConfirmationName != project.Name {
			return fmt.Errorf("%w: expected %q", ErrProjectDeleteNameConfirmation, project.Name)
		}

		candidate, err := manifestWithoutProject(manifest, project.ID)
		if err != nil {
			return err
		}
		result = DeleteProjectResult{Project: *project, Standalone: standalone, DeleteData: request.DeleteData}
		if cluster != nil {
			result.ClusterID = cluster.ID
		}

		if request.DeleteData {
			if _, err := resolvedProjectDataRoot(h.CarbonRoot, project, cluster, standalone); err != nil {
				return err
			}
			receipt, exists, err := readProjectDeleteReceipt(h.CarbonRoot, project.ID)
			if err != nil {
				return err
			}
			if exists && (!receipt.DeleteData || receipt.ProjectID != project.ID) {
				return fmt.Errorf("%w: project delete receipt does not match %s", ErrUnsafePath, project.ID)
			}
			if !exists {
				receipt = projectDeleteReceipt{
					Version: projectDeleteReceiptVersion, State: "prepared", ProjectID: project.ID,
					DeleteData: true, CreatedAt: nowUTC(),
				}
				if err := writeProjectDeleteReceipt(h.CarbonRoot, receipt); err != nil {
					return err
				}
			}

			// Re-run a recovery clear too. Another process can write a task after an
			// earlier manifest publication failure; treating the receipt as proof that
			// no new data exists would leave that task behind in an orphaned store.
			// Crucially, the receipt update and manifest publication happen in the
			// Store finalizer below, before its write lock is released. A writer that
			// loses this race cannot insert a target task between clear and removal.
			clearCommitted := false
			cleared, err := clearProjectTaskDataLockedWithFinalizer(ctx, h, &manifest, ClearProjectTaskDataRequest{
				ProjectID: project.ID, ConfirmationName: request.ConfirmationName, Actor: request.Actor,
			}, func(data store.ClearProjectTaskDataResult) error {
				clearCommitted = true
				receipt.State = "task-data-cleared"
				receipt.TaskDataClearedAt = data.ClearedAt
				receipt.TaskDataReceiptID = data.ReceiptID
				if err := writeProjectDeleteReceipt(h.CarbonRoot, receipt); err != nil {
					return err
				}
				return publishManifest(h.CarbonRoot, candidate)
			})
			if err != nil {
				if clearCommitted {
					return projectDeleteRecoveryError(project.ID, err)
				}
				return err
			}
			result.Data = &cleared.Data
		} else if receipt, exists, err := readProjectDeleteReceipt(h.CarbonRoot, project.ID); err != nil {
			return err
		} else if exists && receipt.DeleteData {
			// A previously interrupted data-delete cannot be converted into catalog-only
			// removal, because its task history may already be gone. Require an explicit
			// retry with DeleteData=true to finish the recorded operation.
			return fmt.Errorf("%w: retry with deleteData=true for project %s", ErrProjectDeleteRecovery, project.ID)
		}

		if !request.DeleteData {
			if err := publishManifest(h.CarbonRoot, candidate); err != nil {
				return err
			}
		}
		if request.DeleteData {
			// A receipt cleanup failure is deliberately non-fatal: the manifest is already
			// atomically published and the stale direct-child receipt cannot re-target a
			// missing project. It is safer than reporting a completed delete as failed.
			_ = removeProjectDeleteReceipt(h.CarbonRoot, project.ID)
		}
		return nil
	})
	return result, err
}

func projectDeleteRecoveryError(projectID string, cause error) error {
	return fmt.Errorf("%w: task data for %s is cleared; retry deletion to remove its catalog entry: %v", ErrProjectDeleteRecovery, projectID, cause)
}

// manifestWithoutProject returns a complete, validated candidate. It never removes a
// cluster itself, even when its last project is removed: shared roots and cluster-wide
// task data have their own lifecycle and must not be swept by a project delete.
func manifestWithoutProject(manifest Manifest, projectID string) (Manifest, error) {
	candidate := manifest
	candidate.Version = Version
	candidate.Clusters = append([]Cluster(nil), manifest.Clusters...)
	candidate.Projects = make([]Project, 0, len(manifest.Projects))
	found := false
	for _, project := range manifest.Projects {
		if project.ID == projectID {
			found = true
			continue
		}
		candidate.Projects = append(candidate.Projects, project)
	}
	for clusterIndex := range candidate.Clusters {
		original := manifest.Clusters[clusterIndex]
		projects := make([]Project, 0, len(original.Projects))
		for _, project := range original.Projects {
			if project.ID == projectID {
				found = true
				continue
			}
			projects = append(projects, project)
		}
		candidate.Clusters[clusterIndex].Projects = projects
	}
	if !found {
		return Manifest{}, fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
	}
	if err := validateManifest(candidate); err != nil {
		return Manifest{}, err
	}
	return candidate, nil
}

func projectDeleteReceiptName(projectID string) (string, error) {
	if !validID(projectID, "project") {
		return "", fmt.Errorf("%w: invalid project delete receipt id", ErrUnsafePath)
	}
	return "project-delete-" + projectID + ".receipt.json", nil
}

func projectDeleteReceiptPath(carbonRoot, projectID string) (string, error) {
	name, err := projectDeleteReceiptName(projectID)
	if err != nil {
		return "", err
	}
	return filepath.Join(carbonRoot, name), nil
}

func writeProjectDeleteReceipt(carbonRoot string, receipt projectDeleteReceipt) error {
	if err := validateProjectDeleteReceipt(receipt); err != nil {
		return err
	}
	name, err := projectDeleteReceiptName(receipt.ProjectID)
	if err != nil {
		return err
	}
	data, err := jsonMarshalIndented(receipt)
	if err != nil {
		return fmt.Errorf("carbon: encode project delete receipt: %w", err)
	}
	if err := atomicWriteJSON(carbonRoot, name, data); err != nil {
		return fmt.Errorf("carbon: write project delete receipt: %w", err)
	}
	return nil
}

func readProjectDeleteReceipt(carbonRoot, projectID string) (projectDeleteReceipt, bool, error) {
	filename, err := projectDeleteReceiptPath(carbonRoot, projectID)
	if err != nil {
		return projectDeleteReceipt{}, false, err
	}
	if _, exists, err := safeRegularFile(carbonRoot, filename, true); err != nil || !exists {
		return projectDeleteReceipt{}, exists, err
	}
	f, err := os.Open(filename)
	if err != nil {
		return projectDeleteReceipt{}, false, fmt.Errorf("carbon: open project delete receipt: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return projectDeleteReceipt{}, false, fmt.Errorf("carbon: read project delete receipt: %w", err)
	}
	if int64(len(data)) > maxManifestBytes {
		return projectDeleteReceipt{}, false, fmt.Errorf("%w: project delete receipt exceeds %d bytes", ErrInvalidManifest, maxManifestBytes)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return projectDeleteReceipt{}, false, err
	}
	var receipt projectDeleteReceipt
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return projectDeleteReceipt{}, false, fmt.Errorf("%w: parse project delete receipt: %v", ErrInvalidManifest, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return projectDeleteReceipt{}, false, fmt.Errorf("%w: project delete receipt has multiple JSON values", ErrInvalidManifest)
		}
		return projectDeleteReceipt{}, false, fmt.Errorf("%w: parse project delete receipt: %v", ErrInvalidManifest, err)
	}
	if err := validateProjectDeleteReceipt(receipt); err != nil {
		return projectDeleteReceipt{}, false, err
	}
	return receipt, true, nil
}

func removeProjectDeleteReceipt(carbonRoot, projectID string) error {
	filename, err := projectDeleteReceiptPath(carbonRoot, projectID)
	if err != nil {
		return err
	}
	if _, exists, err := safeRegularFile(carbonRoot, filename, true); err != nil || !exists {
		return err
	}
	if err := os.Remove(filename); err != nil {
		return fmt.Errorf("carbon: remove project delete receipt: %w", err)
	}
	return nil
}

func validateProjectDeleteReceipt(receipt projectDeleteReceipt) error {
	if receipt.Version != projectDeleteReceiptVersion || !validID(receipt.ProjectID, "project") || !receipt.DeleteData || !validTimestamp(receipt.CreatedAt) {
		return fmt.Errorf("%w: invalid project delete receipt", ErrInvalidManifest)
	}
	if receipt.State != "prepared" && receipt.State != "task-data-cleared" {
		return fmt.Errorf("%w: invalid project delete receipt state", ErrInvalidManifest)
	}
	if receipt.TaskDataClearedAt != "" && !validTimestamp(receipt.TaskDataClearedAt) {
		return fmt.Errorf("%w: invalid project delete receipt timestamp", ErrInvalidManifest)
	}
	if receipt.State == "task-data-cleared" && (receipt.TaskDataClearedAt == "" || receipt.TaskDataReceiptID == "") {
		return fmt.Errorf("%w: incomplete project delete recovery receipt", ErrInvalidManifest)
	}
	return nil
}
