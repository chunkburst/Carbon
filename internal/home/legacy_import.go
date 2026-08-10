package home

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"carbon/internal/cluster"
	"carbon/internal/config"
)

// LegacyImportOptions keeps the cross-home import safe by default. The zero value only
// returns a plan; Apply is the explicit authorization to copy and rewrite task metadata
// into a new shared cluster store.
type LegacyImportOptions struct {
	Apply          bool
	Plan           *LegacyImportPlan
	ExpectedDigest string
	ConfigPolicy   string
}

// LegacyImportApplyRequest is the only input trusted by the direct apply API. The
// reviewed plan itself is deliberately not accepted here: it contains derived paths,
// task mappings, and a target manifest that must be regenerated under the home lock.
// ExpectedDigest is LegacyImportPlan.ReviewDigest from the reviewed preflight plan.
type LegacyImportApplyRequest struct {
	LegacyRoot     string `json:"legacyRoot"`
	ExpectedDigest string `json:"expectedDigest"`
	ConfigPolicy   string `json:"configPolicy,omitempty"`
}

// LegacyImportPreflight describes a source legacy cluster and a distinct target home.
// It reads source metadata and files but does not create a target Carbon directory.
type LegacyImportPreflight struct {
	TargetHome         string                `json:"targetHome"`
	LegacyRoot         string                `json:"legacyRoot"`
	LegacyPath         string                `json:"legacyPath"`
	LegacyDigest       string                `json:"legacyDigest"`
	Name               string                `json:"name"`
	Projects           []LegacyImportProject `json:"projects"`
	HomeExists         bool                  `json:"homeExists"`
	ExistingHomeID     string                `json:"existingHomeId,omitempty"`
	ExistingHomeDigest string                `json:"existingHomeDigest,omitempty"`
}

// LegacyImportProject is one source project and its source .cairn snapshot boundary.
// SnapshotDigest is empty when the source is offline or has no .cairn directory.
type LegacyImportProject struct {
	LegacyID       string `json:"legacyId"`
	TargetID       string `json:"targetId,omitempty"`
	Name           string `json:"name"`
	SourcePath     string `json:"sourcePath"`
	Offline        bool   `json:"offline"`
	Fingerprint    string `json:"fingerprint,omitempty"`
	CairnPath      string `json:"cairnPath,omitempty"`
	SnapshotDigest string `json:"snapshotDigest,omitempty"`
}

// TaskImport records a deterministic old-to-new task identity mapping. The target task
// content is generated during Apply from the verified source bytes, adding project_id and
// rewriting local/global references through the plan's complete mapping table.
type TaskImport struct {
	ProjectID  string `json:"projectId"`
	SourceFile string `json:"sourceFile"`
	SourceHash string `json:"sourceHash"`
	SourceID   string `json:"sourceId"`
	TargetID   string `json:"targetId"`
}

// SessionImport similarly records filename/id/task/attempt remaps for durable sessions.
type SessionImport struct {
	ProjectID       string `json:"projectId"`
	SourceFile      string `json:"sourceFile"`
	SourceHash      string `json:"sourceHash"`
	SourceID        string `json:"sourceId"`
	TargetID        string `json:"targetId"`
	SourceTaskID    string `json:"sourceTaskId"`
	TargetTaskID    string `json:"targetTaskId"`
	SourceAttemptID string `json:"sourceAttemptId,omitempty"`
	TargetAttemptID string `json:"targetAttemptId,omitempty"`
}

// ConfigImport copies source configuration into a namespaced audit copy under the shared
// store. The first parseable config may also seed the shared root config with ProjectID
// cleared so new Carbon tasks always receive their scoped project id from the caller.
type ConfigImport struct {
	ProjectID  string `json:"projectId"`
	SourceFile string `json:"sourceFile"`
	SourceHash string `json:"sourceHash"`
	Primary    bool   `json:"primary"`
}

// RunImport retains historical check evidence. Live state and write.lock are ephemeral
// and deliberately excluded from a durable cross-home import.
type RunImport struct {
	ProjectID      string `json:"projectId"`
	SourceFile     string `json:"sourceFile"`
	SourceHash     string `json:"sourceHash"`
	SourceTaskID   string `json:"sourceTaskId"`
	TargetFilename string `json:"targetFilename"`
}

// ConfigConflict records divergent workflow semantics. A multi-project import cannot
// silently choose gates; Apply requires an explicit ConfigPolicy="primary" when any
// conflict exists. Merge policy is deliberately not inferred in v1.
type ConfigConflict struct {
	ProjectID string `json:"projectId"`
	Field     string `json:"field"`
	Detail    string `json:"detail"`
}

// LegacyImportPlan is a reviewable cross-home migration. BackupPath and ReceiptPath are
// relative to targetHome/.carbon. Source files and hashes make a plan fail closed if the
// legacy cluster changes between preflight and explicit apply.
type LegacyImportPlan struct {
	Version            int                   `json:"version"`
	ID                 string                `json:"id"`
	ClusterID          string                `json:"clusterId"`
	TargetHome         string                `json:"targetHome"`
	LegacyRoot         string                `json:"legacyRoot"`
	LegacyPath         string                `json:"legacyPath"`
	LegacyDigest       string                `json:"legacyDigest"`
	ReviewDigest       string                `json:"reviewDigest"`
	BaseHomeDigest     string                `json:"baseHomeDigest,omitempty"`
	BackupPath         string                `json:"backupPath"`
	ReceiptPath        string                `json:"receiptPath"`
	Manifest           Manifest              `json:"manifest"`
	Projects           []LegacyImportProject `json:"projects"`
	Tasks              []TaskImport          `json:"tasks"`
	Sessions           []SessionImport       `json:"sessions"`
	Configs            []ConfigImport        `json:"configs"`
	Runs               []RunImport           `json:"runs"`
	ConfigConflicts    []ConfigConflict      `json:"configConflicts,omitempty"`
	ConfigPolicy       string                `json:"configPolicy,omitempty"`
	TaskFilesRewritten bool                  `json:"taskFilesRewritten"`
}

// LegacyImportReceipt captures the verified source/target boundary and mapping table.
// Prepared is written before target task files; completed is written only after home.json
// and the staged data root have both been published.
type LegacyImportReceipt struct {
	Version   int              `json:"version"`
	ID        string           `json:"id"`
	Status    string           `json:"status"`
	AppliedAt string           `json:"appliedAt,omitempty"`
	Plan      LegacyImportPlan `json:"plan"`
}

// LegacyImportResult is returned for dry-run and applied imports.
type LegacyImportResult struct {
	Plan        LegacyImportPlan `json:"plan"`
	Applied     bool             `json:"applied"`
	BackupPath  string           `json:"backupPath,omitempty"`
	ReceiptPath string           `json:"receiptPath,omitempty"`
}

// PreflightLegacyImport validates a source v1 registry and records source .cairn snapshot
// hashes. TargetHome is separate from LegacyRoot by design; same-root imports are allowed
// but still create a new isolated Carbon data root rather than editing the old .cairn.
func PreflightLegacyImport(targetHome, legacyClusterRoot string) (LegacyImportPreflight, error) {
	target, err := resolveRoot(targetHome)
	if err != nil {
		return LegacyImportPreflight{}, err
	}
	legacyRoot, err := resolveRoot(legacyClusterRoot)
	if err != nil {
		return LegacyImportPreflight{}, err
	}
	legacy, _, digest, err := readLegacyManifest(legacyRoot)
	if err != nil {
		return LegacyImportPreflight{}, err
	}
	preflight := LegacyImportPreflight{
		TargetHome:   target,
		LegacyRoot:   legacyRoot,
		LegacyPath:   filepath.Join(legacyRoot, cluster.ManifestFilename),
		LegacyDigest: digest,
		Name:         legacy.Name,
		Projects:     make([]LegacyImportProject, 0, len(legacy.Projects)),
	}
	if carbonRoot, exists, err := carbonDir(target, false); err != nil {
		return LegacyImportPreflight{}, err
	} else if exists {
		raw, homeExists, err := readManifestBytes(carbonRoot)
		if err != nil {
			return LegacyImportPreflight{}, err
		}
		preflight.HomeExists = homeExists
		if homeExists {
			home, err := decodeManifest(raw)
			if err != nil {
				return LegacyImportPreflight{}, err
			}
			preflight.ExistingHomeID = home.ID
			preflight.ExistingHomeDigest = hashBytesHex(raw)
			if imported, err := legacyAlreadyImported(carbonRoot, legacyRoot); err != nil {
				return LegacyImportPreflight{}, err
			} else if imported {
				return LegacyImportPreflight{}, ErrLegacyAlreadyImported
			}
		}
	}
	for _, source := range legacy.Projects {
		project := LegacyImportProject{
			LegacyID:   source.ID,
			Name:       source.Name,
			SourcePath: source.Path,
			Offline:    true,
		}
		if _, fingerprint, err := observeSource(source.Path); err == nil {
			project.Offline = false
			project.Fingerprint = fingerprint
			if cairnPath, exists, err := sourceCairnDir(source.Path); err != nil {
				return LegacyImportPreflight{}, err
			} else if exists {
				digest, err := hashTree(cairnPath)
				if err != nil {
					return LegacyImportPreflight{}, err
				}
				project.CairnPath = cairnPath
				project.SnapshotDigest = digest
			}
		}
		preflight.Projects = append(preflight.Projects, project)
	}
	return preflight, nil
}

// PlanLegacyImport creates a cross-home migration plan without writing either source or
// target. Task/session IDs are preserved when globally unique and deterministically
// namespaced by their random target project id only when collisions require it.
func PlanLegacyImport(targetHome, legacyClusterRoot string) (LegacyImportPlan, error) {
	preflight, err := PreflightLegacyImport(targetHome, legacyClusterRoot)
	if err != nil {
		return LegacyImportPlan{}, err
	}
	return buildLegacyImportPlan(preflight)
}

// MigrateLegacyImport returns a plan unless Apply is explicitly set. Supplying Plan makes
// the review/apply boundary transportable across a server, CLI, or MCP confirmation step.
func MigrateLegacyImport(targetHome, legacyClusterRoot string, options LegacyImportOptions) (LegacyImportResult, error) {
	plan := options.Plan
	if plan == nil {
		generated, err := PlanLegacyImport(targetHome, legacyClusterRoot)
		if err != nil {
			return LegacyImportResult{}, err
		}
		plan = &generated
	}
	if !options.Apply {
		if err := validateLegacyImportPlan(*plan); err != nil {
			return LegacyImportResult{}, err
		}
		return LegacyImportResult{Plan: *plan}, nil
	}
	expectedDigest := options.ExpectedDigest
	if expectedDigest == "" {
		expectedDigest = plan.ReviewDigest
	}
	policy := options.ConfigPolicy
	if policy == "" {
		policy = plan.ConfigPolicy
	}
	return ApplyLegacyImportRequest(targetHome, LegacyImportApplyRequest{
		LegacyRoot:     legacyClusterRoot,
		ExpectedDigest: expectedDigest,
		ConfigPolicy:   policy,
	})
}

func buildLegacyImportPlan(preflight LegacyImportPreflight) (LegacyImportPlan, error) {
	var base Manifest
	used := map[string]struct{}{}
	if preflight.HomeExists {
		carbonRoot, exists, err := carbonDir(preflight.TargetHome, false)
		if err != nil || !exists {
			if err != nil {
				return LegacyImportPlan{}, err
			}
			return LegacyImportPlan{}, ErrLegacyChanged
		}
		var manifestExists bool
		base, manifestExists, err = readManifest(carbonRoot)
		if err != nil {
			return LegacyImportPlan{}, err
		}
		if !manifestExists || base.ID != preflight.ExistingHomeID {
			return LegacyImportPlan{}, ErrLegacyChanged
		}
		used = allIDs(base)
	}
	planID, err := newID("migration", used)
	if err != nil {
		return LegacyImportPlan{}, err
	}
	used[planID] = struct{}{}
	homeID := base.ID
	if !preflight.HomeExists {
		homeID, err = newID("home", used)
		if err != nil {
			return LegacyImportPlan{}, err
		}
		used[homeID] = struct{}{}
	}
	clusterID, err := newID("cluster", used)
	if err != nil {
		return LegacyImportPlan{}, err
	}
	used[clusterID] = struct{}{}
	targetCluster := Cluster{ID: clusterID, Name: normalizedName(preflight.Name, "Imported cluster"), Prefix: normalizePrefix(preflight.Name, "Imported cluster"), DataPath: path.Join(clusterDataDirectory, clusterID), CreatedAt: nowUTC(), Projects: []Project{}}
	projects := make([]LegacyImportProject, len(preflight.Projects))
	copy(projects, preflight.Projects)
	for i := range projects {
		projectID, err := newID("project", used)
		if err != nil {
			return LegacyImportPlan{}, err
		}
		used[projectID] = struct{}{}
		projects[i].TargetID = projectID
		source := Source{Path: projects[i].SourcePath, Aliases: []string{projects[i].SourcePath}, LastSeen: nowUTC()}
		if projects[i].Fingerprint != "" {
			source.Fingerprint = projects[i].Fingerprint
		} else {
			opaque, err := newID("legacy", used)
			if err != nil {
				return LegacyImportPlan{}, err
			}
			used[opaque] = struct{}{}
			source.Fingerprint = "legacy:" + opaque
		}
		targetCluster.Projects = append(targetCluster.Projects, Project{ID: projectID, Name: normalizedName(projects[i].Name, filepath.Base(projects[i].SourcePath)), Kind: ProjectGeneric, Source: source, CreatedAt: nowUTC()})
	}

	clusters := append([]Cluster{}, base.Clusters...)
	clusters = append(clusters, targetCluster)
	standaloneProjects := append([]Project{}, base.Projects...)
	createdAt := base.CreatedAt
	if createdAt == "" {
		createdAt = nowUTC()
	}
	reviewDigest, err := legacyImportReviewDigest(preflight)
	if err != nil {
		return LegacyImportPlan{}, err
	}
	plan := LegacyImportPlan{
		Version: Version, ID: planID, ClusterID: clusterID, TargetHome: preflight.TargetHome, LegacyRoot: preflight.LegacyRoot,
		LegacyPath: preflight.LegacyPath, LegacyDigest: preflight.LegacyDigest, ReviewDigest: reviewDigest,
		BaseHomeDigest: preflight.ExistingHomeDigest,
		BackupPath:     path.Join("backups", planID), ReceiptPath: path.Join("receipts", planID+".json"),
		Manifest: Manifest{Version: Version, ID: homeID, CreatedAt: createdAt, Clusters: clusters, Projects: standaloneProjects},
		Projects: projects, Tasks: []TaskImport{}, Sessions: []SessionImport{}, Configs: []ConfigImport{}, Runs: []RunImport{}, TaskFilesRewritten: true,
	}
	if err := populateImportPlan(&plan); err != nil {
		return LegacyImportPlan{}, err
	}
	if err := validateLegacyImportPlan(plan); err != nil {
		return LegacyImportPlan{}, err
	}
	return plan, nil
}

// legacyImportReviewDigest binds every preflight fact whose change would make a reviewed
// import unsafe: source registry bytes, source .cairn snapshots, and the target-home
// generation. It intentionally excludes random target IDs and timestamps so Apply can
// regenerate an independent trusted plan while still proving it describes the same
// reviewed source/target state.
func legacyImportReviewDigest(preflight LegacyImportPreflight) (string, error) {
	type reviewProject struct {
		LegacyID       string `json:"legacyId"`
		Name           string `json:"name"`
		SourcePath     string `json:"sourcePath"`
		Offline        bool   `json:"offline"`
		Fingerprint    string `json:"fingerprint,omitempty"`
		CairnPath      string `json:"cairnPath,omitempty"`
		SnapshotDigest string `json:"snapshotDigest,omitempty"`
	}
	type review struct {
		Version            int             `json:"version"`
		TargetHome         string          `json:"targetHome"`
		LegacyRoot         string          `json:"legacyRoot"`
		LegacyPath         string          `json:"legacyPath"`
		LegacyDigest       string          `json:"legacyDigest"`
		HomeExists         bool            `json:"homeExists"`
		ExistingHomeID     string          `json:"existingHomeId,omitempty"`
		ExistingHomeDigest string          `json:"existingHomeDigest,omitempty"`
		Projects           []reviewProject `json:"projects"`
	}
	projects := make([]reviewProject, len(preflight.Projects))
	for index, project := range preflight.Projects {
		projects[index] = reviewProject{
			LegacyID: project.LegacyID, Name: project.Name, SourcePath: project.SourcePath,
			Offline: project.Offline, Fingerprint: project.Fingerprint, CairnPath: project.CairnPath,
			SnapshotDigest: project.SnapshotDigest,
		}
	}
	data, err := json.Marshal(review{
		Version: Version, TargetHome: preflight.TargetHome, LegacyRoot: preflight.LegacyRoot,
		LegacyPath: preflight.LegacyPath, LegacyDigest: preflight.LegacyDigest,
		HomeExists: preflight.HomeExists, ExistingHomeID: preflight.ExistingHomeID,
		ExistingHomeDigest: preflight.ExistingHomeDigest, Projects: projects,
	})
	if err != nil {
		return "", err
	}
	return hashBytesHex(data), nil
}

func populateImportPlan(plan *LegacyImportPlan) error {
	var taskInputs []taskInput
	var sessionInputs []sessionInput
	for _, project := range plan.Projects {
		if project.CairnPath == "" {
			continue
		}
		tasks, err := scanImportTasks(project.TargetID, filepath.Join(project.CairnPath, "tasks"))
		if err != nil {
			return err
		}
		taskInputs = append(taskInputs, tasks...)
		sessions, err := scanImportSessions(project.TargetID, filepath.Join(project.CairnPath, "sessions"))
		if err != nil {
			return err
		}
		sessionInputs = append(sessionInputs, sessions...)
		configFile := filepath.Join(project.CairnPath, "config.yaml")
		if hash, exists, err := hashRegularFile(configFile); err != nil {
			return err
		} else if exists {
			plan.Configs = append(plan.Configs, ConfigImport{ProjectID: project.TargetID, SourceFile: configFile, SourceHash: hash})
		}
	}
	sort.Slice(taskInputs, func(i, j int) bool {
		if taskInputs[i].projectID == taskInputs[j].projectID {
			return taskInputs[i].id < taskInputs[j].id
		}
		return taskInputs[i].projectID < taskInputs[j].projectID
	})
	assignTaskIDs(taskInputs)
	for _, input := range taskInputs {
		plan.Tasks = append(plan.Tasks, TaskImport{ProjectID: input.projectID, SourceFile: input.filename, SourceHash: input.hash, SourceID: input.id, TargetID: input.targetID})
	}
	sort.Slice(sessionInputs, func(i, j int) bool {
		if sessionInputs[i].projectID == sessionInputs[j].projectID {
			return sessionInputs[i].id < sessionInputs[j].id
		}
		return sessionInputs[i].projectID < sessionInputs[j].projectID
	})
	assignSessionIDs(sessionInputs)
	taskMaps, globalTasks := taskReferenceMaps(plan.Tasks)
	for _, input := range sessionInputs {
		targetTask, ok := resolveImportedReference(input.projectID, input.taskID, taskMaps, globalTasks)
		if !ok {
			return fmt.Errorf("%w: session %s refers to task %s not included in import", ErrInvalidMigrationPlan, input.filename, input.taskID)
		}
		plan.Sessions = append(plan.Sessions, SessionImport{ProjectID: input.projectID, SourceFile: input.filename, SourceHash: input.hash, SourceID: input.id, TargetID: input.targetID, SourceTaskID: input.taskID, TargetTaskID: targetTask, SourceAttemptID: input.attemptID, TargetAttemptID: input.targetAttemptID})
	}
	if len(plan.Configs) > 0 {
		plan.Configs[0].Primary = true
	}
	if err := populateRunImports(plan); err != nil {
		return err
	}
	conflicts, err := importConfigConflicts(plan.Configs)
	if err != nil {
		return err
	}
	plan.ConfigConflicts = conflicts
	return validateTaskReferences(plan.Tasks)
}

type taskInput struct {
	projectID string
	filename  string
	hash      string
	id        string
	targetID  string
	deps      []string
	parent    string
	attempt   string
}

type sessionInput struct {
	projectID       string
	filename        string
	hash            string
	id              string
	targetID        string
	taskID          string
	attemptID       string
	targetAttemptID string
}

func assignTaskIDs(inputs []taskInput) {
	counts := map[string]int{}
	for _, input := range inputs {
		counts[input.id]++
	}
	used := map[string]struct{}{}
	for i := range inputs {
		candidate := inputs[i].id
		if counts[candidate] > 1 {
			candidate = namespacedImportedID(inputs[i].projectID, candidate)
		}
		for suffix := 2; ; suffix++ {
			if _, exists := used[candidate]; !exists {
				break
			}
			candidate = fmt.Sprintf("%s-%d", candidate, suffix)
		}
		inputs[i].targetID = candidate
		used[candidate] = struct{}{}
	}
}

func assignSessionIDs(inputs []sessionInput) {
	counts := map[string]int{}
	attemptCounts := map[string]int{}
	for _, input := range inputs {
		counts[input.id]++
		if input.attemptID != "" {
			attemptCounts[input.attemptID]++
		}
	}
	used := map[string]struct{}{}
	usedAttempts := map[string]struct{}{}
	for i := range inputs {
		candidate := inputs[i].id
		if counts[candidate] > 1 {
			candidate = namespacedImportedID(inputs[i].projectID, candidate)
		}
		for suffix := 2; ; suffix++ {
			if _, exists := used[candidate]; !exists {
				break
			}
			candidate = fmt.Sprintf("%s-%d", candidate, suffix)
		}
		inputs[i].targetID = candidate
		used[candidate] = struct{}{}
		if inputs[i].attemptID == "" {
			continue
		}
		attempt := inputs[i].attemptID
		if attemptCounts[attempt] > 1 {
			attempt = namespacedImportedID(inputs[i].projectID, attempt)
		}
		for suffix := 2; ; suffix++ {
			if _, exists := usedAttempts[attempt]; !exists {
				break
			}
			attempt = fmt.Sprintf("%s-%d", attempt, suffix)
		}
		inputs[i].targetAttemptID = attempt
		usedAttempts[attempt] = struct{}{}
	}
}

func namespacedImportedID(projectID, value string) string {
	return projectID + "-" + value
}

func taskReferenceMaps(tasks []TaskImport) (map[string]map[string]string, map[string]string) {
	local := map[string]map[string]string{}
	counts := map[string]int{}
	global := map[string]string{}
	for _, task := range tasks {
		if local[task.ProjectID] == nil {
			local[task.ProjectID] = map[string]string{}
		}
		local[task.ProjectID][task.SourceID] = task.TargetID
		counts[task.SourceID]++
		global[task.SourceID] = task.TargetID
	}
	for sourceID, count := range counts {
		if count != 1 {
			delete(global, sourceID)
		}
	}
	return local, global
}

func resolveImportedReference(projectID, sourceID string, local map[string]map[string]string, global map[string]string) (string, bool) {
	if value, ok := local[projectID][sourceID]; ok {
		return value, true
	}
	value, ok := global[sourceID]
	return value, ok
}

func validateTaskReferences(tasks []TaskImport) error {
	// Full source task content is rechecked immediately before Apply. This compact
	// plan-level check deliberately leaves legacy dangling deps untouched rather than
	// pretending an import can safely infer their intended target.
	for _, task := range tasks {
		if !validImportedID(task.SourceID) || !validImportedID(task.TargetID) || !validDigest(task.SourceHash) {
			return fmt.Errorf("%w: invalid task mapping", ErrInvalidMigrationPlan)
		}
	}
	return nil
}

func validateLegacyImportPlan(plan LegacyImportPlan) error {
	if plan.Version != Version || !validID(plan.ID, "migration") || !validID(plan.ClusterID, "cluster") || !plan.TaskFilesRewritten {
		return fmt.Errorf("%w: invalid import plan id/version", ErrInvalidMigrationPlan)
	}
	for _, p := range []struct{ name, value string }{{"target home", plan.TargetHome}, {"legacy root", plan.LegacyRoot}, {"legacy path", plan.LegacyPath}} {
		if !filepath.IsAbs(p.value) || filepath.Clean(p.value) != p.value {
			return fmt.Errorf("%w: invalid %s", ErrInvalidMigrationPlan, p.name)
		}
	}
	if !validDigest(plan.LegacyDigest) || !validDigest(plan.ReviewDigest) || (plan.BaseHomeDigest != "" && !validDigest(plan.BaseHomeDigest)) || !validCarbonRelativePath(plan.BackupPath) || !validCarbonRelativePath(plan.ReceiptPath) {
		return fmt.Errorf("%w: invalid import backup/receipt path", ErrInvalidMigrationPlan)
	}
	if err := validateManifest(plan.Manifest); err != nil {
		return fmt.Errorf("%w: target manifest: %v", ErrInvalidMigrationPlan, err)
	}
	clusterFound := false
	for _, cluster := range plan.Manifest.Clusters {
		if cluster.ID == plan.ClusterID {
			clusterFound = true
			break
		}
	}
	if !clusterFound {
		return fmt.Errorf("%w: imported cluster is missing", ErrInvalidMigrationPlan)
	}
	projectIDs := map[string]struct{}{}
	for _, project := range plan.Projects {
		if !validID(project.TargetID, "project") || !validStoredPath(project.SourcePath) {
			return fmt.Errorf("%w: invalid imported project", ErrInvalidMigrationPlan)
		}
		projectIDs[project.TargetID] = struct{}{}
		if project.SnapshotDigest != "" && !validDigest(project.SnapshotDigest) {
			return fmt.Errorf("%w: invalid source snapshot hash", ErrInvalidMigrationPlan)
		}
	}
	for _, task := range plan.Tasks {
		if _, ok := projectIDs[task.ProjectID]; !ok || !validImportedID(task.SourceID) || !validImportedID(task.TargetID) || !validDigest(task.SourceHash) {
			return fmt.Errorf("%w: invalid task import", ErrInvalidMigrationPlan)
		}
	}
	for _, session := range plan.Sessions {
		if _, ok := projectIDs[session.ProjectID]; !ok || !validImportedID(session.SourceID) || !validImportedID(session.TargetID) || !validDigest(session.SourceHash) || !validImportedID(session.SourceTaskID) || !validImportedID(session.TargetTaskID) {
			return fmt.Errorf("%w: invalid session import", ErrInvalidMigrationPlan)
		}
	}
	for _, cfg := range plan.Configs {
		if _, ok := projectIDs[cfg.ProjectID]; !ok || !validDigest(cfg.SourceHash) {
			return fmt.Errorf("%w: invalid config import", ErrInvalidMigrationPlan)
		}
	}
	for _, run := range plan.Runs {
		if _, ok := projectIDs[run.ProjectID]; !ok || !validDigest(run.SourceHash) || !validImportedID(run.SourceTaskID) || filepath.Base(run.TargetFilename) != run.TargetFilename {
			return fmt.Errorf("%w: invalid run import", ErrInvalidMigrationPlan)
		}
	}
	if plan.ConfigPolicy != "" && plan.ConfigPolicy != "primary" {
		return fmt.Errorf("%w: unsupported config policy", ErrInvalidMigrationPlan)
	}
	return nil
}

func validateImportApplyPolicy(plan LegacyImportPlan) error {
	if len(plan.ConfigConflicts) > 0 && plan.ConfigPolicy != "primary" {
		return fmt.Errorf("%w: config workflow conflicts require explicit ConfigPolicy=primary", ErrInvalidMigrationPlan)
	}
	return nil
}

func importConfigConflicts(imports []ConfigImport) ([]ConfigConflict, error) {
	var baseline *config.Config
	var baselineProject string
	var conflicts []ConfigConflict
	for _, imported := range imports {
		cfg, err := config.Load(imported.SourceFile)
		if err != nil {
			conflicts = append(conflicts, ConfigConflict{ProjectID: imported.ProjectID, Field: "config", Detail: "source config is invalid: " + err.Error()})
			continue
		}
		if baseline == nil {
			copy := cfg
			baseline = &copy
			baselineProject = imported.ProjectID
			continue
		}
		for _, field := range differingWorkflowFields(*baseline, cfg) {
			conflicts = append(conflicts, ConfigConflict{ProjectID: imported.ProjectID, Field: field, Detail: "differs from primary project " + baselineProject})
		}
	}
	return conflicts, nil
}

func differingWorkflowFields(left, right config.Config) []string {
	var fields []string
	if !slicesEqual(left.States, right.States) {
		fields = append(fields, "states")
	}
	if !slicesEqual(left.Closed, right.Closed) {
		fields = append(fields, "closed")
	}
	if left.Initial != right.Initial {
		fields = append(fields, "initial")
	}
	if left.WorkingState != right.WorkingState {
		fields = append(fields, "working_state")
	}
	if left.ReviewState != right.ReviewState {
		fields = append(fields, "review_state")
	}
	if left.CheckTimeoutDefault != right.CheckTimeoutDefault {
		fields = append(fields, "check_timeout_default")
	}
	if left.CheckShell != right.CheckShell {
		fields = append(fields, "check_shell")
	}
	if left.SessionHeartbeat != right.SessionHeartbeat {
		fields = append(fields, "session_heartbeat_interval")
	}
	if left.SessionStaleAfter != right.SessionStaleAfter {
		fields = append(fields, "session_stale_after")
	}
	return fields
}

func slicesEqual[T comparable](left, right []T) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func validImportedID(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 512 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func validCarbonRelativePath(value string) bool {
	return validDataPath(value) && !strings.HasPrefix(value, clusterDataDirectory+"/")
}

func hashBytesHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Compile-time guards make accidental import source mutations stand out in review.
var _ = errors.Is
var _ = os.ErrNotExist
