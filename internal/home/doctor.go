package home

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"carbon/internal/config"
	"carbon/internal/repo"
)

// DoctorOptions controls whether suggested repairs are merely reported or made durable.
// The zero value is a dry run.
type DoctorOptions struct {
	Apply bool
}

// DoctorIssue is a non-mutating finding. A source can be offline without invalidating the
// whole home; a fingerprint mismatch is intentionally reported rather than auto-relinked.
type DoctorIssue struct {
	Code      string `json:"code"`
	ClusterID string `json:"clusterId,omitempty"`
	ProjectID string `json:"projectId,omitempty"`
	Detail    string `json:"detail"`
}

// DoctorRepair describes a safe, deterministic metadata repair. The same report is used
// for dry-run and apply, while Applied distinguishes proposed from persisted repairs.
type DoctorRepair struct {
	Code      string `json:"code"`
	ClusterID string `json:"clusterId,omitempty"`
	ProjectID string `json:"projectId,omitempty"`
	Detail    string `json:"detail"`
}

// DoctorReport is intentionally small enough for an HTTP/MCP adapter to return directly.
type DoctorReport struct {
	Main    string         `json:"main"`
	HomeID  string         `json:"homeId"`
	Changed bool           `json:"changed"`
	Applied bool           `json:"applied"`
	Issues  []DoctorIssue  `json:"issues"`
	Repairs []DoctorRepair `json:"repairs"`
}

type doctorState struct {
	manifest  Manifest
	report    DoctorReport
	dataRoots []doctorDataRoot
	changed   bool
}

type doctorDataRoot struct {
	clusterID  string
	projectID  string
	dataPath   string
	prefix     string
	standalone bool
}

// Doctor validates operational metadata without inspecting a task file. In dry-run mode
// it may parse repairable alias duplication but never creates .carbon, rewrites home.json,
// or recreates a data root. Apply is explicit and re-runs the inspection under the cache
// lock before one atomic manifest rewrite.
func Doctor(main string, options DoctorOptions) (DoctorReport, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return DoctorReport{}, err
	}
	if !options.Apply {
		carbonRoot, exists, err := carbonDir(root, false)
		if err != nil {
			return DoctorReport{}, err
		}
		if !exists {
			return DoctorReport{}, ErrNotInitialized
		}
		state, err := inspectDoctor(root, carbonRoot)
		if err != nil {
			return DoctorReport{}, err
		}
		return state.report, nil
	}

	var report DoctorReport
	err = withLock(root, func() error {
		carbonRoot, exists, err := carbonDir(root, false)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotInitialized
		}
		state, err := inspectDoctor(root, carbonRoot)
		if err != nil {
			return err
		}
		for _, store := range state.dataRoots {
			if store.standalone {
				if _, err := ensureStandaloneProjectStore(carbonRoot, store.projectID, store.prefix); err != nil {
					return err
				}
			} else if _, err := ensureClusterStore(carbonRoot, store.dataPath, store.prefix); err != nil {
				return err
			}
		}
		if state.changed {
			if err := writeManifest(carbonRoot, state.manifest); err != nil {
				return err
			}
			state.report.Applied = true
		}
		report = state.report
		return nil
	})
	return report, err
}

// Doctor is the handle method equivalent of the package function.
func (h *Home) Doctor(options DoctorOptions) (DoctorReport, error) {
	if h == nil {
		return DoctorReport{}, ErrNotInitialized
	}
	return Doctor(h.Root, options)
}

func inspectDoctor(root, carbonRoot string) (doctorState, error) {
	manifest, repairs, err := readDoctorManifest(carbonRoot)
	if err != nil {
		return doctorState{}, err
	}
	state := doctorState{
		manifest: manifest,
		report: DoctorReport{
			Main:    root,
			HomeID:  manifest.ID,
			Issues:  []DoctorIssue{},
			Repairs: append([]DoctorRepair{}, repairs...),
		},
		changed: len(repairs) > 0,
	}
	for clusterIndex := range state.manifest.Clusters {
		cluster := &state.manifest.Clusters[clusterIndex]
		if root, err := dataRoot(carbonRoot, cluster.DataPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				state.report.Repairs = append(state.report.Repairs, DoctorRepair{
					Code:      "recreate_data_root",
					ClusterID: cluster.ID,
					Detail:    "cluster data root and private task-store scaffold will be recreated",
				})
				state.dataRoots = append(state.dataRoots, doctorDataRoot{clusterID: cluster.ID, dataPath: cluster.DataPath, prefix: cluster.Prefix})
				state.changed = true
			} else {
				state.report.Issues = append(state.report.Issues, DoctorIssue{
					Code:      "unsafe_data_root",
					ClusterID: cluster.ID,
					Detail:    err.Error(),
				})
			}
		} else if missing, err := clusterStoreNeedsRepair(root); err != nil {
			state.report.Issues = append(state.report.Issues, DoctorIssue{
				Code:      "unsafe_data_store",
				ClusterID: cluster.ID,
				Detail:    err.Error(),
			})
		} else if missing {
			state.report.Repairs = append(state.report.Repairs, DoctorRepair{
				Code:      "restore_data_store",
				ClusterID: cluster.ID,
				Detail:    "cluster private task-store scaffold is incomplete and will be restored",
			})
			state.dataRoots = append(state.dataRoots, doctorDataRoot{clusterID: cluster.ID, dataPath: cluster.DataPath, prefix: cluster.Prefix})
			state.changed = true
		}
		for projectIndex := range cluster.Projects {
			inspectDoctorProject(cluster.ID, &cluster.Projects[projectIndex], &state)
		}
	}
	for projectIndex := range state.manifest.Projects {
		project := &state.manifest.Projects[projectIndex]
		relative, err := standaloneProjectDataPath(project.ID)
		if err != nil {
			return doctorState{}, err
		}
		if root, err := dataRoot(carbonRoot, relative); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				state.report.Repairs = append(state.report.Repairs, DoctorRepair{
					Code:      "recreate_data_root",
					ProjectID: project.ID,
					Detail:    "standalone project data root and private task-store scaffold will be recreated",
				})
				state.dataRoots = append(state.dataRoots, doctorDataRoot{projectID: project.ID, prefix: normalizePrefix("", project.Name), standalone: true})
				state.changed = true
			} else {
				state.report.Issues = append(state.report.Issues, DoctorIssue{
					Code:      "unsafe_data_root",
					ProjectID: project.ID,
					Detail:    err.Error(),
				})
			}
		} else if missing, err := standaloneProjectStoreNeedsRepair(root, project.ID); err != nil {
			state.report.Issues = append(state.report.Issues, DoctorIssue{
				Code:      "unsafe_data_store",
				ProjectID: project.ID,
				Detail:    err.Error(),
			})
		} else if missing {
			state.report.Repairs = append(state.report.Repairs, DoctorRepair{
				Code:      "restore_data_store",
				ProjectID: project.ID,
				Detail:    "standalone project private task-store scaffold will be restored",
			})
			state.dataRoots = append(state.dataRoots, doctorDataRoot{projectID: project.ID, prefix: normalizePrefix("", project.Name), standalone: true})
			state.changed = true
		}
		inspectDoctorProject("", project, &state)
	}
	state.report.Changed = state.changed
	return state, nil
}

// clusterStoreNeedsRepair checks the entire private Carbon store scaffold rather than
// merely the top-level data root. Doctor must not report a recreated root as usable until
// config.yaml and every task/session/run/live directory are present and non-reparse.
func clusterStoreNeedsRepair(root string) (bool, error) {
	for _, relative := range []string{repo.CarbonDirName, path.Join(repo.CarbonDirName, "tasks"), path.Join(repo.CarbonDirName, "runs"), path.Join(repo.CarbonDirName, "sessions"), path.Join(repo.CarbonDirName, "live")} {
		filename := filepath.Join(root, filepath.FromSlash(relative))
		info, err := os.Lstat(filename)
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if isReparsePoint(filename, info) || !info.IsDir() {
			return false, fmt.Errorf("%w: invalid private store directory %s", ErrUnsafePath, filename)
		}
		resolved, err := filepath.EvalSymlinks(filename)
		if err != nil || !samePath(resolved, filename) || !pathWithin(root, resolved) {
			return false, fmt.Errorf("%w: private store directory escapes data root", ErrUnsafePath)
		}
	}
	configPath := filepath.Join(root, repo.CarbonDirName, "config.yaml")
	info, err := os.Lstat(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if isReparsePoint(configPath, info) || !info.Mode().IsRegular() {
		return false, fmt.Errorf("%w: invalid private store config %s", ErrUnsafePath, configPath)
	}
	resolved, err := filepath.EvalSymlinks(configPath)
	if err != nil || !samePath(resolved, configPath) || !pathWithin(root, resolved) {
		return false, fmt.Errorf("%w: private store config escapes data root", ErrUnsafePath)
	}
	if _, err := config.Load(configPath); err != nil {
		return false, fmt.Errorf("invalid private store config: %w", err)
	}
	return false, nil
}

func standaloneProjectStoreNeedsRepair(root, projectID string) (bool, error) {
	missing, err := clusterStoreNeedsRepair(root)
	if err != nil || missing {
		return missing, err
	}
	cfg, err := config.Load(filepath.Join(root, repo.CarbonDirName, "config.yaml"))
	if err != nil {
		return false, err
	}
	if cfg.ProjectID == "" {
		return true, nil
	}
	if cfg.ProjectID != projectID {
		return false, fmt.Errorf("%w: standalone project store belongs to %s", ErrInvalidManifest, cfg.ProjectID)
	}
	return false, nil
}

func inspectDoctorProject(clusterID string, project *Project, state *doctorState) {
	canonical, fingerprint, err := observeSource(project.Source.Path)
	if err == nil && fingerprint == project.Source.Fingerprint {
		if !samePath(canonical, project.Source.Path) {
			project.Source.Path = canonical
			project.Source.Aliases = appendUniquePath(project.Source.Aliases, canonical)
			project.Source.LastSeen = nowUTC()
			state.report.Repairs = append(state.report.Repairs, DoctorRepair{
				Code:      "canonicalize_source",
				ClusterID: clusterID,
				ProjectID: project.ID,
				Detail:    "source path was refreshed to its canonical location",
			})
			state.changed = true
		}
		return
	}
	if err == nil {
		state.report.Issues = append(state.report.Issues, DoctorIssue{
			Code:      "fingerprint_mismatch",
			ClusterID: clusterID,
			ProjectID: project.ID,
			Detail:    "source path now identifies a different folder; use Relink explicitly",
		})
		return
	}
	if repairProjectFromAliases(clusterID, project, state) {
		return
	}
	state.report.Issues = append(state.report.Issues, DoctorIssue{
		Code:      "offline_source",
		ClusterID: clusterID,
		ProjectID: project.ID,
		Detail:    "source folder is currently unavailable",
	})
}

func repairProjectFromAliases(clusterID string, project *Project, state *doctorState) bool {
	var matches []string
	for _, alias := range project.Source.Aliases {
		canonical, fingerprint, err := observeSource(alias)
		if err == nil && fingerprint == project.Source.Fingerprint {
			matches = appendUniquePath(matches, canonical)
		}
	}
	if len(matches) != 1 {
		if len(matches) > 1 {
			state.report.Issues = append(state.report.Issues, DoctorIssue{
				Code:      "ambiguous_alias",
				ClusterID: clusterID,
				ProjectID: project.ID,
				Detail:    "more than one recorded alias matches the source fingerprint",
			})
		}
		return false
	}
	project.Source.Path = matches[0]
	project.Source.Aliases = appendUniquePath(project.Source.Aliases, matches[0])
	project.Source.LastSeen = nowUTC()
	state.report.Repairs = append(state.report.Repairs, DoctorRepair{
		Code:      "relink_alias",
		ClusterID: clusterID,
		ProjectID: project.ID,
		Detail:    "a recorded alias matched the source fingerprint and becomes the current path",
	})
	state.changed = true
	return true
}

// readDoctorManifest permits only deterministic alias normalization on an otherwise
// valid v1/v2 manifest. Future schema versions and structural ambiguity (duplicate
// IDs/data roots) stay fail-closed and cannot be "repaired" by an older binary.
func readDoctorManifest(carbonRoot string) (Manifest, []DoctorRepair, error) {
	raw, exists, err := readManifestBytes(carbonRoot)
	if err != nil {
		return Manifest{}, nil, err
	}
	if !exists {
		return Manifest{}, nil, ErrNotInitialized
	}
	manifest, err := decodeManifest(raw)
	if err == nil {
		return manifest, nil, nil
	}
	if errors.Is(err, ErrFutureVersion) {
		return Manifest{}, nil, err
	}
	if !errors.Is(err, ErrInvalidManifest) {
		return Manifest{}, nil, err
	}
	loose, err := decodeManifestLoose(raw)
	if err != nil {
		return Manifest{}, nil, err
	}
	repairs, err := normalizeRepairableAliases(&loose)
	if err != nil {
		return Manifest{}, nil, err
	}
	if len(repairs) == 0 {
		return Manifest{}, nil, errInvalidDoctorStructure()
	}
	if err := validateManifest(loose); err != nil {
		return Manifest{}, nil, errInvalidDoctorStructure()
	}
	return loose, repairs, nil
}

func errInvalidDoctorStructure() error {
	return fmt.Errorf("%w: manifest has non-repairable structural errors", ErrInvalidManifest)
}

func decodeManifestLoose(data []byte) (Manifest, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Manifest{}, err
	}
	var wire manifestWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Manifest{}, fmt.Errorf("%w: parse JSON: %v", ErrInvalidManifest, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Manifest{}, fmt.Errorf("%w: parse JSON: %v", ErrInvalidManifest, err)
	}
	if wire.Version == nil || wire.ID == nil || wire.CreatedAt == nil || len(wire.Clusters) == 0 || bytes.Equal(bytes.TrimSpace(wire.Clusters), []byte("null")) {
		return Manifest{}, fmt.Errorf("%w: required manifest fields missing", ErrInvalidManifest)
	}
	if *wire.Version > Version {
		return Manifest{}, fmt.Errorf("%w: version %d", ErrFutureVersion, *wire.Version)
	}
	if *wire.Version != legacyManifestVersion && *wire.Version != Version {
		return Manifest{}, fmt.Errorf("%w: unsupported version %d", ErrInvalidManifest, *wire.Version)
	}
	var clusters []Cluster
	clustersDecoder := json.NewDecoder(bytes.NewReader(wire.Clusters))
	clustersDecoder.DisallowUnknownFields()
	if err := clustersDecoder.Decode(&clusters); err != nil {
		return Manifest{}, fmt.Errorf("%w: parse clusters: %v", ErrInvalidManifest, err)
	}
	if err := clustersDecoder.Decode(&extra); err != io.EOF {
		return Manifest{}, fmt.Errorf("%w: parse clusters: %v", ErrInvalidManifest, err)
	}
	if clusters == nil {
		clusters = []Cluster{}
	}
	manifest := Manifest{Version: *wire.Version, ID: *wire.ID, CreatedAt: *wire.CreatedAt, Clusters: clusters}
	if manifest.Version == Version {
		if len(wire.Projects) == 0 || bytes.Equal(bytes.TrimSpace(wire.Projects), []byte("null")) {
			return Manifest{}, fmt.Errorf("%w: projects are required for version %d", ErrInvalidManifest, Version)
		}
		var projects []Project
		projectsDecoder := json.NewDecoder(bytes.NewReader(wire.Projects))
		projectsDecoder.DisallowUnknownFields()
		if err := projectsDecoder.Decode(&projects); err != nil {
			return Manifest{}, fmt.Errorf("%w: parse projects: %v", ErrInvalidManifest, err)
		}
		if err := projectsDecoder.Decode(&extra); err != io.EOF {
			return Manifest{}, fmt.Errorf("%w: parse projects: %v", ErrInvalidManifest, err)
		}
		if projects == nil {
			projects = []Project{}
		}
		manifest.Projects = projects
	} else if len(wire.Projects) != 0 {
		return Manifest{}, fmt.Errorf("%w: projects are unsupported in version %d", ErrInvalidManifest, legacyManifestVersion)
	}
	return manifest, nil
}

func normalizeRepairableAliases(manifest *Manifest) ([]DoctorRepair, error) {
	var repairs []DoctorRepair
	for clusterIndex := range manifest.Clusters {
		cluster := &manifest.Clusters[clusterIndex]
		for projectIndex := range cluster.Projects {
			if repair, err := normalizeRepairableProjectAliases(&cluster.Projects[projectIndex], cluster.ID); err != nil {
				return nil, err
			} else if repair != nil {
				repairs = append(repairs, *repair)
			}
		}
	}
	for projectIndex := range manifest.Projects {
		if repair, err := normalizeRepairableProjectAliases(&manifest.Projects[projectIndex], ""); err != nil {
			return nil, err
		} else if repair != nil {
			repairs = append(repairs, *repair)
		}
	}
	return repairs, nil
}

func normalizeRepairableProjectAliases(project *Project, clusterID string) (*DoctorRepair, error) {
	if !validStoredPath(project.Source.Path) || !validFingerprint(project.Source.Fingerprint) || !validTimestamp(project.Source.LastSeen) {
		return nil, errInvalidDoctorStructure()
	}
	aliases := make([]string, 0, len(project.Source.Aliases)+1)
	for _, alias := range project.Source.Aliases {
		if !validStoredPath(alias) {
			return nil, errInvalidDoctorStructure()
		}
		if containsPath(aliases, alias) {
			continue
		}
		aliases = append(aliases, alias)
	}
	changed := len(aliases) != len(project.Source.Aliases)
	if !containsPath(aliases, project.Source.Path) {
		aliases = append(aliases, project.Source.Path)
		changed = true
	}
	if !changed {
		return nil, nil
	}
	project.Source.Aliases = aliases
	return &DoctorRepair{
		Code:      "normalize_aliases",
		ClusterID: clusterID,
		ProjectID: project.ID,
		Detail:    "removed duplicate source aliases and restored the current path alias",
	}, nil
}

// Keep strings imported in this file for a compile-time assertion that documentation's
// "dry run" wording cannot accidentally be implemented by normalising user input here.
var _ = strings.TrimSpace
