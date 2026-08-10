package home

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"carbon/internal/cluster"
)

// MigrationOptions makes migration safe by default: an omitted or zero-valued option
// produces only a plan. Apply must be explicitly true (and may reuse a reviewed Plan).
type MigrationOptions struct {
	Apply bool
	Plan  *MigrationPlan
}

// LegacyPreflight is a read-only summary of the current .cairn-cluster.json v1 registry.
// It never creates .carbon, acquires no write lock, and never opens legacy task files.
type LegacyPreflight struct {
	Main           string          `json:"main"`
	ManifestPath   string          `json:"manifestPath"`
	Digest         string          `json:"digest"`
	Name           string          `json:"name"`
	Projects       []LegacyProject `json:"projects"`
	HomeExists     bool            `json:"homeExists"`
	TasksUntouched bool            `json:"tasksUntouched"`
}

// LegacyProject is the imported view of a current .cairn-cluster.json v1 entry.
type LegacyProject struct {
	LegacyID    string `json:"legacyId"`
	Name        string `json:"name"`
	SourcePath  string `json:"sourcePath"`
	Legacy      bool   `json:"legacy,omitempty"`
	Offline     bool   `json:"offline"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// MigrationPlan is immutable caller-owned data describing a proposed conversion. The
// target Manifest is intentionally separate from the legacy registry and contains new
// random IDs plus cluster-relative data paths. It never includes task-file mutations.
type MigrationPlan struct {
	Version        int      `json:"version"`
	ID             string   `json:"id"`
	Main           string   `json:"main"`
	LegacyPath     string   `json:"legacyPath"`
	LegacyDigest   string   `json:"legacyDigest"`
	BackupPath     string   `json:"backupPath"`
	ReceiptPath    string   `json:"receiptPath"`
	Manifest       Manifest `json:"manifest"`
	TasksUntouched bool     `json:"tasksUntouched"`
}

// MigrationReceipt is durable evidence of a migration attempt. Prepared receipts are
// written before home.json, then atomically changed to applied after home.json succeeds.
// Thus a crash cannot leave an unrecorded migration boundary.
type MigrationReceipt struct {
	Version        int           `json:"version"`
	ID             string        `json:"id"`
	Status         string        `json:"status"`
	AppliedAt      string        `json:"appliedAt,omitempty"`
	Plan           MigrationPlan `json:"plan"`
	TasksUntouched bool          `json:"tasksUntouched"`
}

// MigrationResult returns a plan for dry-runs and adds receipt paths after an explicit
// apply. BackupPath and ReceiptPath are physical paths only after Applied is true.
type MigrationResult struct {
	Plan        MigrationPlan `json:"plan"`
	Applied     bool          `json:"applied"`
	BackupPath  string        `json:"backupPath,omitempty"`
	ReceiptPath string        `json:"receiptPath,omitempty"`
}

// PreflightLegacy reads the current v1 legacy manifest and returns its digest. It fails
// closed for malformed/future legacy metadata and reports a pre-existing Carbon home
// without modifying either format.
func PreflightLegacy(main string) (LegacyPreflight, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return LegacyPreflight{}, err
	}
	legacy, raw, digest, err := readLegacyManifest(root)
	if err != nil {
		return LegacyPreflight{}, err
	}
	_ = raw // digest is calculated from the exact validated bytes.
	preflight := LegacyPreflight{
		Main:           root,
		ManifestPath:   filepath.Join(root, cluster.ManifestFilename),
		Digest:         digest,
		Name:           legacy.Name,
		Projects:       make([]LegacyProject, 0, len(legacy.Projects)),
		TasksUntouched: true,
	}
	if carbonRoot, exists, err := carbonDir(root, false); err != nil {
		return LegacyPreflight{}, err
	} else if exists {
		_, homeExists, err := readManifest(carbonRoot)
		if err != nil {
			return LegacyPreflight{}, err
		}
		preflight.HomeExists = homeExists
	}
	for _, legacyProject := range legacy.Projects {
		view := LegacyProject{
			LegacyID:   legacyProject.ID,
			Name:       legacyProject.Name,
			SourcePath: legacyProject.Path,
			Legacy:     legacyProject.Legacy,
			Offline:    true,
		}
		if _, fingerprint, err := observeSource(legacyProject.Path); err == nil {
			view.Offline = false
			view.Fingerprint = fingerprint
		}
		preflight.Projects = append(preflight.Projects, view)
	}
	return preflight, nil
}

// PlanLegacy makes a new home manifest in memory only. A caller can serialize/review the
// result, use MigrateLegacy with Apply=false, or pass it to ApplyLegacyPlan later.
func PlanLegacy(main string) (MigrationPlan, error) {
	preflight, err := PreflightLegacy(main)
	if err != nil {
		return MigrationPlan{}, err
	}
	if preflight.HomeExists {
		return MigrationPlan{}, ErrAlreadyInitialized
	}
	return buildLegacyPlan(preflight)
}

// MigrateLegacy returns a read-only plan by default. Set MigrationOptions.Apply only
// after reviewing a plan; passing the reviewed plan makes the apply boundary explicit.
func MigrateLegacy(main string, options MigrationOptions) (MigrationResult, error) {
	plan := options.Plan
	if plan == nil {
		generated, err := PlanLegacy(main)
		if err != nil {
			return MigrationResult{}, err
		}
		plan = &generated
	}
	if !options.Apply {
		if err := validateMigrationPlan(*plan); err != nil {
			return MigrationResult{}, err
		}
		return MigrationResult{Plan: *plan}, nil
	}
	return ApplyLegacyPlan(main, *plan)
}

// ApplyLegacyPlan makes the explicitly reviewed plan durable. It backs up the exact
// legacy manifest, writes a prepared receipt, atomically writes home.json, then writes an
// applied receipt. It never reads, copies, moves, or edits a .cairn task file.
func ApplyLegacyPlan(main string, plan MigrationPlan) (MigrationResult, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return MigrationResult{}, err
	}
	if !samePath(root, plan.Main) {
		return MigrationResult{}, fmt.Errorf("%w: plan belongs to %q, not %q", ErrInvalidMigrationPlan, plan.Main, root)
	}
	if err := validateMigrationPlan(plan); err != nil {
		return MigrationResult{}, err
	}
	result := MigrationResult{Plan: plan}
	err = withLock(root, func() error {
		legacy, raw, digest, err := readLegacyManifest(root)
		if err != nil {
			return err
		}
		_ = legacy
		if digest != plan.LegacyDigest {
			return fmt.Errorf("%w: expected %s, found %s", ErrLegacyChanged, plan.LegacyDigest, digest)
		}
		carbonRoot, exists, err := carbonDir(root, true)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: Carbon directory disappeared", ErrUnsafePath)
		}
		if _, homeExists, err := readManifest(carbonRoot); err != nil {
			return err
		} else if homeExists {
			return ErrAlreadyInitialized
		}

		for _, target := range plan.Manifest.Clusters {
			if _, err := ensureClusterStore(carbonRoot, target.DataPath, target.Prefix); err != nil {
				return err
			}
		}
		if err := atomicWriteJSON(carbonRoot, plan.BackupPath, raw); err != nil {
			return fmt.Errorf("carbon: backup legacy manifest: %w", err)
		}
		prepared := MigrationReceipt{Version: Version, ID: plan.ID, Status: "prepared", Plan: plan, TasksUntouched: true}
		if err := writeReceipt(carbonRoot, plan.ReceiptPath, prepared); err != nil {
			return err
		}
		if err := writeManifest(carbonRoot, plan.Manifest); err != nil {
			return err
		}
		prepared.Status = "applied"
		prepared.AppliedAt = nowUTC()
		if err := writeReceipt(carbonRoot, plan.ReceiptPath, prepared); err != nil {
			return err
		}
		result.Applied = true
		result.BackupPath = filepath.Join(carbonRoot, plan.BackupPath)
		result.ReceiptPath = filepath.Join(carbonRoot, plan.ReceiptPath)
		return nil
	})
	return result, err
}

func buildLegacyPlan(preflight LegacyPreflight) (MigrationPlan, error) {
	used := make(map[string]struct{})
	planID, err := newID("migration", used)
	if err != nil {
		return MigrationPlan{}, err
	}
	used[planID] = struct{}{}
	homeID, err := newID("home", used)
	if err != nil {
		return MigrationPlan{}, err
	}
	used[homeID] = struct{}{}
	clusterID, err := newID("cluster", used)
	if err != nil {
		return MigrationPlan{}, err
	}
	used[clusterID] = struct{}{}
	target := Cluster{
		ID:        clusterID,
		Name:      normalizedName(preflight.Name, "Imported cluster"),
		Prefix:    normalizePrefix(preflight.Name, "Imported cluster"),
		DataPath:  path.Join(clusterDataDirectory, clusterID),
		CreatedAt: nowUTC(),
		Projects:  make([]Project, 0, len(preflight.Projects)),
	}
	for _, legacyProject := range preflight.Projects {
		projectID, err := newID("project", used)
		if err != nil {
			return MigrationPlan{}, err
		}
		used[projectID] = struct{}{}
		source := Source{
			Path:     legacyProject.SourcePath,
			Aliases:  []string{legacyProject.SourcePath},
			LastSeen: nowUTC(),
		}
		if legacyProject.Fingerprint != "" {
			source.Fingerprint = legacyProject.Fingerprint
		} else {
			opaque, err := newID("legacy", used)
			if err != nil {
				return MigrationPlan{}, err
			}
			used[opaque] = struct{}{}
			source.Fingerprint = "legacy:" + opaque
		}
		target.Projects = append(target.Projects, Project{
			ID:        projectID,
			Name:      normalizedName(legacyProject.Name, filepath.Base(legacyProject.SourcePath)),
			Kind:      ProjectGeneric,
			Source:    source,
			CreatedAt: nowUTC(),
		})
	}
	manifest := Manifest{Version: Version, ID: homeID, CreatedAt: nowUTC(), Clusters: []Cluster{target}, Projects: []Project{}}
	if err := validateManifest(manifest); err != nil {
		return MigrationPlan{}, err
	}
	return MigrationPlan{
		Version:        Version,
		ID:             planID,
		Main:           preflight.Main,
		LegacyPath:     preflight.ManifestPath,
		LegacyDigest:   preflight.Digest,
		BackupPath:     "legacy-" + planID + ".cairn-cluster.json.bak",
		ReceiptPath:    "migration-" + planID + ".receipt.json",
		Manifest:       manifest,
		TasksUntouched: true,
	}, nil
}

func validateMigrationPlan(plan MigrationPlan) error {
	if plan.Version != Version || !validID(plan.ID, "migration") {
		return fmt.Errorf("%w: invalid plan id or version", ErrInvalidMigrationPlan)
	}
	if strings.TrimSpace(plan.Main) == "" || !filepath.IsAbs(plan.Main) || filepath.Clean(plan.Main) != plan.Main {
		return fmt.Errorf("%w: invalid plan main path", ErrInvalidMigrationPlan)
	}
	if !filepath.IsAbs(plan.LegacyPath) || filepath.Clean(plan.LegacyPath) != plan.LegacyPath {
		return fmt.Errorf("%w: invalid legacy manifest path", ErrInvalidMigrationPlan)
	}
	if !validDigest(plan.LegacyDigest) || !validMetadataFilename(plan.BackupPath) || !validMetadataFilename(plan.ReceiptPath) {
		return fmt.Errorf("%w: invalid digest or receipt path", ErrInvalidMigrationPlan)
	}
	if !plan.TasksUntouched {
		return fmt.Errorf("%w: task mutation plans are not supported", ErrInvalidMigrationPlan)
	}
	if err := validateManifest(plan.Manifest); err != nil {
		return fmt.Errorf("%w: target manifest: %v", ErrInvalidMigrationPlan, err)
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func validMetadataFilename(value string) bool {
	return value != "" && filepath.Base(value) == value && path.Base(value) == value && !strings.Contains(value, `\`) && value != "." && value != ".."
}

func writeReceipt(carbonRoot, filename string, receipt MigrationReceipt) error {
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("carbon: encode migration receipt: %w", err)
	}
	data = append(data, '\n')
	if err := atomicWriteJSON(carbonRoot, filename, data); err != nil {
		return fmt.Errorf("carbon: write migration receipt: %w", err)
	}
	return nil
}

func readLegacyManifest(root string) (cluster.Manifest, []byte, string, error) {
	filename := filepath.Join(root, cluster.ManifestFilename)
	info, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return cluster.Manifest{}, nil, "", ErrLegacyNotFound
	}
	if err != nil {
		return cluster.Manifest{}, nil, "", fmt.Errorf("%w: inspect legacy manifest: %v", ErrInvalidManifest, err)
	}
	if isReparsePoint(filename, info) || !info.Mode().IsRegular() {
		return cluster.Manifest{}, nil, "", fmt.Errorf("%w: unsafe legacy manifest", ErrUnsafePath)
	}
	resolved, err := filepath.EvalSymlinks(filename)
	if err != nil || !samePath(resolved, filename) || !pathWithin(root, resolved) {
		return cluster.Manifest{}, nil, "", fmt.Errorf("%w: legacy manifest escapes home", ErrUnsafePath)
	}
	f, err := os.Open(filename)
	if err != nil {
		return cluster.Manifest{}, nil, "", fmt.Errorf("carbon: open legacy manifest: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return cluster.Manifest{}, nil, "", fmt.Errorf("carbon: read legacy manifest: %w", err)
	}
	if int64(len(raw)) > maxManifestBytes {
		return cluster.Manifest{}, nil, "", fmt.Errorf("%w: legacy manifest exceeds %d bytes", ErrInvalidManifest, maxManifestBytes)
	}
	legacy, exists, err := cluster.Read(root)
	if err != nil {
		return cluster.Manifest{}, nil, "", fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if !exists || legacy.Version != cluster.Version {
		return cluster.Manifest{}, nil, "", ErrLegacyNotFound
	}
	sum := sha256.Sum256(raw)
	return legacy, raw, hex.EncodeToString(sum[:]), nil
}
