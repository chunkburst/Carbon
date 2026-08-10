package home

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"carbon/internal/config"
	"carbon/internal/repo"
)

const detachReceiptVersion = 1

// DetachProjectOptions makes copying a multi-project shared store explicit. The zero
// value permits detaching only the sole project in a cluster. Reason is audit metadata
// supplied by an owner when a peer-containing shared store is intentionally copied.
type DetachProjectOptions struct {
	AllowSharedStoreCopy bool   `json:"allowSharedStoreCopy"`
	Reason               string `json:"reason,omitempty"`
}

// DetachProjectReceipt records the file-system and manifest publication boundary. A
// completed receipt proves that the project was moved to Manifest.Projects; a failed
// receipt leaves the original cluster entry untouched and records the inert copied data
// path for owner review.
type DetachProjectReceipt struct {
	Version              int    `json:"version"`
	ID                   string `json:"id"`
	Status               string `json:"status"`
	CreatedAt            string `json:"createdAt"`
	CompletedAt          string `json:"completedAt,omitempty"`
	ClusterID            string `json:"clusterId"`
	ProjectID            string `json:"projectId"`
	SourceDataPath       string `json:"sourceDataPath"`
	TargetDataPath       string `json:"targetDataPath"`
	StagingDataPath      string `json:"stagingDataPath"`
	SourceDigest         string `json:"sourceDigest"`
	SourceProjectCount   int    `json:"sourceProjectCount"`
	SharedStoreCopy      bool   `json:"sharedStoreCopy"`
	AllowSharedStoreCopy bool   `json:"allowSharedStoreCopy"`
	Reason               string `json:"reason,omitempty"`
	Failure              string `json:"failure,omitempty"`
}

// DetachProjectResult summarizes the new standalone location while retaining the old
// shared root for audit and rollback. Detach never removes or changes the cluster data
// root itself.
type DetachProjectResult struct {
	Project            Project              `json:"project"`
	SourceDataRoot     string               `json:"sourceDataRoot"`
	DataRoot           string               `json:"dataRoot"`
	ReceiptPath        string               `json:"receiptPath"`
	SourceProjectCount int                  `json:"sourceProjectCount"`
	SharedStoreCopy    bool                 `json:"sharedStoreCopy"`
	Receipt            DetachProjectReceipt `json:"receipt"`
}

// DetachProject explicitly converts a clustered project into a standalone project. It
// keeps the stable project ID, copies (never moves or deletes) the shared cluster store,
// and atomically publishes the manifest change only after the new root is ready.
func DetachProject(main, clusterRef, projectRef string, options DetachProjectOptions) (DetachProjectResult, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return DetachProjectResult{}, err
	}
	if !validDescription(strings.TrimSpace(options.Reason)) {
		return DetachProjectResult{}, fmt.Errorf("%w: invalid detach review reason", ErrInvalidManifest)
	}
	var result DetachProjectResult
	err = withLock(root, func() error {
		carbonRoot, exists, err := carbonDir(root, false)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotInitialized
		}
		manifest, exists, err := readManifest(carbonRoot)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotInitialized
		}
		cluster, err := findCluster(&manifest, clusterRef)
		if err != nil {
			return err
		}
		project, err := findProject(cluster, projectRef)
		if err != nil {
			return err
		}
		sourceProjectCount := len(cluster.Projects)
		if sourceProjectCount > 1 && !options.AllowSharedStoreCopy {
			return fmt.Errorf("%w: cluster %s contains %d projects", ErrDetachRequiresReview, cluster.ID, sourceProjectCount)
		}
		sourceRoot, err := dataRoot(carbonRoot, cluster.DataPath)
		if err != nil {
			return err
		}
		sourceDigest, err := hashTree(sourceRoot)
		if err != nil {
			return fmt.Errorf("carbon: hash shared project store: %w", err)
		}
		operationID, err := newID("detach", allIDs(manifest))
		if err != nil {
			return err
		}
		targetDataPath, err := standaloneProjectDataPath(project.ID)
		if err != nil {
			return err
		}
		projectsRoot, err := ensureDataRoot(carbonRoot, projectDataDirectory)
		if err != nil {
			return err
		}
		targetRoot, err := absentDetachTarget(projectsRoot, project.ID)
		if err != nil {
			return err
		}
		stagingDirectory := filepath.Join(carbonRoot, "staging", operationID)
		if _, err := os.Lstat(stagingDirectory); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return fmt.Errorf("%w: detach staging directory %s already exists", ErrDetachTargetExists, stagingDirectory)
			}
			return fmt.Errorf("%w: inspect detach staging directory %s: %v", ErrUnsafePath, stagingDirectory, err)
		}
		stagingDataPath := path.Join("staging", operationID, "data")
		stagingRoot, err := ensureDataRoot(carbonRoot, stagingDataPath)
		if err != nil {
			return err
		}
		receiptName := operationID + ".receipt.json"
		receipt := DetachProjectReceipt{
			Version:              detachReceiptVersion,
			ID:                   operationID,
			Status:               "prepared",
			CreatedAt:            nowUTC(),
			ClusterID:            cluster.ID,
			ProjectID:            project.ID,
			SourceDataPath:       cluster.DataPath,
			TargetDataPath:       targetDataPath,
			StagingDataPath:      stagingDataPath,
			SourceDigest:         sourceDigest,
			SourceProjectCount:   sourceProjectCount,
			SharedStoreCopy:      sourceProjectCount > 1,
			AllowSharedStoreCopy: options.AllowSharedStoreCopy,
			Reason:               strings.TrimSpace(options.Reason),
		}
		if err := writeDetachReceipt(carbonRoot, receiptName, receipt); err != nil {
			return err
		}
		fail := func(cause error) error {
			receipt.Status = "failed"
			receipt.CompletedAt = nowUTC()
			receipt.Failure = detachFailureDetail(cause)
			if receiptErr := writeDetachReceipt(carbonRoot, receiptName, receipt); receiptErr != nil {
				return fmt.Errorf("%w; additionally record detach failure: %v", cause, receiptErr)
			}
			return cause
		}
		if err := copyStrictTree(sourceRoot, stagingRoot); err != nil {
			return fail(fmt.Errorf("carbon: copy shared project store: %w", err))
		}
		if digest, err := hashTree(sourceRoot); err != nil {
			return fail(fmt.Errorf("carbon: rehash shared project store: %w", err))
		} else if digest != sourceDigest {
			return fail(fmt.Errorf("%w: %s", ErrDetachSourceChanged, cluster.ID))
		}
		if digest, err := hashTree(stagingRoot); err != nil {
			return fail(fmt.Errorf("carbon: verify detached project copy: %w", err))
		} else if digest != sourceDigest {
			return fail(fmt.Errorf("%w: copied store digest mismatch", ErrDetachSourceChanged))
		}
		if err := bindDetachedStoreProjectID(stagingRoot, project.ID); err != nil {
			return fail(err)
		}
		if err := os.Rename(stagingRoot, targetRoot); err != nil {
			return fail(fmt.Errorf("carbon: publish detached project store: %w", err))
		}
		if _, err := dataRoot(carbonRoot, targetDataPath); err != nil {
			return fail(err)
		}
		candidate, detached, err := detachedManifest(manifest, cluster.ID, project.ID)
		if err != nil {
			return fail(err)
		}
		if err := writeManifest(carbonRoot, candidate); err != nil {
			return fail(err)
		}
		receipt.Status = "completed"
		receipt.CompletedAt = nowUTC()
		if err := writeDetachReceipt(carbonRoot, receiptName, receipt); err != nil {
			result = DetachProjectResult{
				Project: detached, SourceDataRoot: sourceRoot, DataRoot: targetRoot,
				ReceiptPath: filepath.Join(carbonRoot, receiptName), SourceProjectCount: sourceProjectCount,
				SharedStoreCopy: sourceProjectCount > 1, Receipt: receipt,
			}
			return fmt.Errorf("carbon: detached project manifest published but receipt finalization failed: %w", err)
		}
		result = DetachProjectResult{
			Project: detached, SourceDataRoot: sourceRoot, DataRoot: targetRoot,
			ReceiptPath: filepath.Join(carbonRoot, receiptName), SourceProjectCount: sourceProjectCount,
			SharedStoreCopy: sourceProjectCount > 1, Receipt: receipt,
		}
		return nil
	})
	return result, err
}

// DetachProject detaches a project from this already-open home.
func (h *Home) DetachProject(clusterRef, projectRef string, options DetachProjectOptions) (DetachProjectResult, error) {
	if h == nil {
		return DetachProjectResult{}, ErrNotInitialized
	}
	return DetachProject(h.Root, clusterRef, projectRef, options)
}

// MoveProjectToStandalone is a descriptive compatibility alias for DetachProject.
func MoveProjectToStandalone(main, clusterRef, projectRef string, options DetachProjectOptions) (DetachProjectResult, error) {
	return DetachProject(main, clusterRef, projectRef, options)
}

// MoveProjectToStandalone is the handle method alias for DetachProject.
func (h *Home) MoveProjectToStandalone(clusterRef, projectRef string, options DetachProjectOptions) (DetachProjectResult, error) {
	return h.DetachProject(clusterRef, projectRef, options)
}

func absentDetachTarget(projectsRoot, projectID string) (string, error) {
	target := filepath.Join(projectsRoot, projectID)
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return target, nil
	}
	if err != nil {
		return "", fmt.Errorf("%w: inspect standalone project root %s: %v", ErrUnsafePath, target, err)
	}
	if isReparsePoint(target, info) || !info.IsDir() {
		return "", fmt.Errorf("%w: refusing standalone project root %s", ErrUnsafePath, target)
	}
	return "", fmt.Errorf("%w: %s", ErrDetachTargetExists, projectID)
}

func bindDetachedStoreProjectID(root, projectID string) error {
	filename := filepath.Join(root, repo.CarbonDirName, "config.yaml")
	if _, _, err := safeRegularFile(root, filename, false); err != nil {
		return err
	}
	cfg, err := config.Load(filename)
	if err != nil {
		return fmt.Errorf("carbon: load detached project task-store config: %w", err)
	}
	if cfg.ProjectID != "" && cfg.ProjectID != projectID {
		return fmt.Errorf("%w: copied shared store belongs to project %s", ErrInvalidManifest, cfg.ProjectID)
	}
	if cfg.ProjectID == projectID {
		return nil
	}
	cfg.ProjectID = projectID
	if err := config.Save(filename, cfg); err != nil {
		return fmt.Errorf("carbon: bind detached project task-store config: %w", err)
	}
	return nil
}

func detachedManifest(manifest Manifest, clusterID, projectID string) (Manifest, Project, error) {
	candidate := manifest
	candidate.Version = Version
	candidate.Clusters = append([]Cluster(nil), manifest.Clusters...)
	var detached Project
	found := false
	for clusterIndex := range candidate.Clusters {
		cluster := &candidate.Clusters[clusterIndex]
		if cluster.ID != clusterID {
			continue
		}
		projects := make([]Project, 0, len(cluster.Projects)-1)
		for _, project := range cluster.Projects {
			if project.ID == projectID {
				detached = project
				found = true
				continue
			}
			projects = append(projects, project)
		}
		cluster.Projects = projects
		break
	}
	if !found {
		return Manifest{}, Project{}, fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
	}
	candidate.Projects = append(append([]Project(nil), manifest.Projects...), detached)
	if err := validateManifest(candidate); err != nil {
		return Manifest{}, Project{}, err
	}
	return candidate, detached, nil
}

func writeDetachReceipt(carbonRoot, filename string, receipt DetachProjectReceipt) error {
	data, err := jsonMarshalIndented(receipt)
	if err != nil {
		return fmt.Errorf("carbon: encode detach receipt: %w", err)
	}
	if err := atomicWriteJSON(carbonRoot, filename, data); err != nil {
		return fmt.Errorf("carbon: write detach receipt: %w", err)
	}
	return nil
}

func detachFailureDetail(err error) string {
	value := strings.TrimSpace(err.Error())
	if len(value) > 2048 {
		return value[:2048]
	}
	return value
}
