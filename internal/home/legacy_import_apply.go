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
	"sort"
	"strings"

	"carbon/internal/config"
	"carbon/internal/repo"

	"gopkg.in/yaml.v3"
)

// legacyImportApplyHook is test-only fault injection at transaction boundaries. It is
// intentionally unexported; production callers cannot cause an import to skip a stage.
var legacyImportApplyHook func(stage string) error

func invokeLegacyImportApplyHook(stage string) error {
	if legacyImportApplyHook == nil {
		return nil
	}
	return legacyImportApplyHook(stage)
}

// ApplyLegacyImport is kept as a compatibility wrapper for callers that still carry a
// serialized review plan. It deliberately reads only the immutable review digest, the
// selected legacy root, and the explicit policy; no paths, mappings, snapshots, or target
// manifest from plan are trusted. New CLI/MCP code should call ApplyLegacyImportRequest.
func ApplyLegacyImport(targetHome string, plan LegacyImportPlan) (LegacyImportResult, error) {
	return ApplyLegacyImportRequest(targetHome, LegacyImportApplyRequest{
		LegacyRoot:     plan.LegacyRoot,
		ExpectedDigest: plan.ReviewDigest,
		ConfigPolicy:   plan.ConfigPolicy,
	})
}

// ApplyLegacyImportRequest regenerates a trusted plan while holding the target-home lock.
// The request intentionally contains no source file path, task mapping, or target manifest:
// those values are always derived afresh from targetHome and LegacyRoot, then bound to the
// caller's reviewed ExpectedDigest before any target data is written.
func ApplyLegacyImportRequest(targetHome string, request LegacyImportApplyRequest) (LegacyImportResult, error) {
	target, err := resolveRoot(targetHome)
	if err != nil {
		return LegacyImportResult{}, err
	}
	legacyRoot, err := resolveRoot(request.LegacyRoot)
	if err != nil {
		return LegacyImportResult{}, err
	}
	if !validDigest(request.ExpectedDigest) {
		return LegacyImportResult{}, fmt.Errorf("%w: invalid expected review digest", ErrInvalidMigrationPlan)
	}
	if request.ConfigPolicy != "" && request.ConfigPolicy != "primary" {
		return LegacyImportResult{}, fmt.Errorf("%w: unsupported config policy", ErrInvalidMigrationPlan)
	}

	var result LegacyImportResult
	err = withLock(target, func() error {
		// Finish or roll back a prior interrupted attempt before deriving the current
		// target generation. Incomplete receipts never themselves block a retry.
		if carbonRoot, exists, err := carbonDir(target, false); err != nil {
			return err
		} else if exists {
			if err := recoverIncompleteLegacyImports(carbonRoot); err != nil {
				return err
			}
		}

		trusted, err := PlanLegacyImport(target, legacyRoot)
		if err != nil {
			return err
		}
		if trusted.ReviewDigest != request.ExpectedDigest {
			return fmt.Errorf("%w: expected review %s, found %s", ErrLegacyChanged, request.ExpectedDigest, trusted.ReviewDigest)
		}
		trusted.ConfigPolicy = request.ConfigPolicy
		if err := validateLegacyImportPlan(trusted); err != nil {
			return err
		}
		if err := validateImportApplyPolicy(trusted); err != nil {
			return err
		}
		result.Plan = trusted

		backupPath, receiptPath, err := applyTrustedLegacyImport(target, trusted)
		if err != nil {
			return err
		}
		result.Applied = true
		result.BackupPath = backupPath
		result.ReceiptPath = receiptPath
		return nil
	})
	return result, err
}

// applyTrustedLegacyImport stages the complete shared data root inside .carbon, publishes
// it with one same-filesystem rename, and only then atomically publishes home.json. A
// prepared receipt can therefore always be either rolled back to inactive quarantine or
// finalized by recovery; it is never treated as a completed import.
func applyTrustedLegacyImport(target string, plan LegacyImportPlan) (backupPath, receiptPath string, resultErr error) {
	carbonRoot, _, err := carbonDir(target, true)
	if err != nil {
		return "", "", err
	}
	if err := verifyImportSnapshots(plan); err != nil {
		return "", "", err
	}
	receiptDir, err := ensureDataRoot(carbonRoot, path.Dir(plan.ReceiptPath))
	if err != nil {
		return "", "", err
	}
	receiptName := path.Base(plan.ReceiptPath)
	receipt := LegacyImportReceipt{Version: Version, ID: plan.ID, Status: "prepared", Plan: plan}
	if err := writeImportReceipt(receiptDir, receiptName, receipt); err != nil {
		return "", "", err
	}
	prepared := true
	fail := func(cause error) error {
		if !prepared {
			return cause
		}
		return abortLegacyImport(carbonRoot, receiptDir, receiptName, &receipt, cause)
	}
	if err := invokeLegacyImportApplyHook("after_prepared"); err != nil {
		return "", "", fail(err)
	}

	backupRoot, err := ensureDataRoot(carbonRoot, plan.BackupPath)
	if err != nil {
		return "", "", fail(err)
	}
	if err := backupImportSources(plan, backupRoot); err != nil {
		return "", "", fail(err)
	}
	cluster, err := importedCluster(plan)
	if err != nil {
		return "", "", fail(err)
	}
	stagingPath := importStagingDataPath(plan)
	stagingRoot, err := ensureClusterStore(carbonRoot, stagingPath, cluster.Prefix)
	if err != nil {
		return "", "", fail(err)
	}
	if err := importConfigs(plan, stagingRoot); err != nil {
		return "", "", fail(err)
	}
	if err := importTasksAndSessions(plan, stagingRoot); err != nil {
		return "", "", fail(err)
	}
	if err := importRuns(plan, stagingRoot); err != nil {
		return "", "", fail(err)
	}
	if err := invokeLegacyImportApplyHook("after_staged"); err != nil {
		return "", "", fail(err)
	}
	if err := publishImportStaging(carbonRoot, plan, cluster); err != nil {
		return "", "", fail(err)
	}
	if err := invokeLegacyImportApplyHook("after_data_publish"); err != nil {
		return "", "", fail(err)
	}
	if err := writeManifest(carbonRoot, plan.Manifest); err != nil {
		return "", "", fail(err)
	}
	if err := invokeLegacyImportApplyHook("after_manifest_publish"); err != nil {
		if recovered := fail(err); recovered != nil {
			return "", "", recovered
		}
		return backupRoot, filepath.Join(receiptDir, receiptName), nil
	}
	receipt.Status = "completed"
	receipt.AppliedAt = nowUTC()
	if err := writeImportReceipt(receiptDir, receiptName, receipt); err != nil {
		if recovered := fail(err); recovered != nil {
			return "", "", recovered
		}
		return backupRoot, filepath.Join(receiptDir, receiptName), nil
	}
	return backupRoot, filepath.Join(receiptDir, receiptName), nil
}

func importStagingDataPath(plan LegacyImportPlan) string {
	return path.Join("staging", plan.ID, "data")
}

func publishImportStaging(carbonRoot string, plan LegacyImportPlan, cluster Cluster) error {
	stagingRoot, err := dataRoot(carbonRoot, importStagingDataPath(plan))
	if err != nil {
		return err
	}
	parent, err := ensureDataRoot(carbonRoot, path.Dir(cluster.DataPath))
	if err != nil {
		return err
	}
	target := filepath.Join(parent, filepath.Base(cluster.DataPath))
	if info, err := os.Lstat(target); err == nil {
		if isReparsePoint(target, info) || !info.IsDir() {
			return fmt.Errorf("%w: refusing import publish target %s", ErrUnsafePath, target)
		}
		return fmt.Errorf("%w: import target data root already exists", ErrInvalidMigrationPlan)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(stagingRoot, target); err != nil {
		return fmt.Errorf("carbon: publish staged import data: %w", err)
	}
	syncImportDirectory(parent)
	return nil
}

func syncImportDirectory(directory string) {
	if handle, err := os.Open(directory); err == nil {
		_ = handle.Sync() // best effort on Windows
		_ = handle.Close()
	}
}

// abortLegacyImport keeps active state all-or-nothing. If home.json is already durable,
// recovery finalizes the receipt instead of trying to undo a published manifest. Otherwise
// every inactive staging/final directory is moved out of the operational namespace and the
// receipt becomes failed, so a later reviewed retry is allowed.
func abortLegacyImport(carbonRoot, receiptDir, receiptName string, receipt *LegacyImportReceipt, cause error) error {
	committed, err := legacyImportCommitted(carbonRoot, receipt.Plan)
	if err != nil {
		return errors.Join(cause, err)
	}
	if committed {
		receipt.Status = "completed"
		if receipt.AppliedAt == "" {
			receipt.AppliedAt = nowUTC()
		}
		if err := writeImportReceipt(receiptDir, receiptName, *receipt); err != nil {
			return errors.Join(cause, err)
		}
		return nil
	}
	if err := quarantineImportArtifacts(carbonRoot, receipt.Plan); err != nil {
		return errors.Join(cause, err)
	}
	receipt.Status = "failed"
	if err := writeImportReceipt(receiptDir, receiptName, *receipt); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func legacyImportCommitted(carbonRoot string, plan LegacyImportPlan) (bool, error) {
	if !validID(plan.ID, "migration") || !validID(plan.ClusterID, "cluster") {
		return false, nil
	}
	manifest, exists, err := readManifest(carbonRoot)
	if err != nil || !exists {
		return false, err
	}
	cluster, err := findCluster(&manifest, plan.ClusterID)
	if errors.Is(err, ErrClusterNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if cluster.DataPath != path.Join(clusterDataDirectory, plan.ClusterID) {
		return false, nil
	}
	if len(cluster.Projects) != len(plan.Projects) {
		return false, nil
	}
	projectIDs := make(map[string]struct{}, len(plan.Projects))
	for _, project := range plan.Projects {
		projectIDs[project.TargetID] = struct{}{}
	}
	for _, project := range cluster.Projects {
		if _, ok := projectIDs[project.ID]; !ok {
			return false, nil
		}
	}
	if _, err := dataRoot(carbonRoot, cluster.DataPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func recoverIncompleteLegacyImports(carbonRoot string) error {
	directory := filepath.Join(carbonRoot, "receipts")
	entries, exists, err := strictReadDir(directory)
	if err != nil || !exists {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		filename := filepath.Join(directory, entry.Name())
		data, exists, err := readStrictRegularFile(filename)
		if err != nil || !exists {
			if err != nil {
				return err
			}
			continue
		}
		var receipt LegacyImportReceipt
		if err := json.Unmarshal(data, &receipt); err != nil {
			return fmt.Errorf("%w: invalid import receipt %s", ErrInvalidManifest, filename)
		}
		if receipt.Status == "completed" {
			continue
		}
		if !validID(receipt.ID, "migration") || !validID(receipt.Plan.ID, "migration") || !validID(receipt.Plan.ClusterID, "cluster") || receipt.ID != receipt.Plan.ID {
			// A malformed inactive receipt cannot grant completion or select a path;
			// leave it non-blocking for manual inspection rather than trusting it.
			continue
		}
		committed, err := legacyImportCommitted(carbonRoot, receipt.Plan)
		if err != nil {
			return err
		}
		if committed {
			receipt.Status = "completed"
			if receipt.AppliedAt == "" {
				receipt.AppliedAt = nowUTC()
			}
			if err := writeImportReceipt(directory, entry.Name(), receipt); err != nil {
				return err
			}
			continue
		}
		if err := quarantineImportArtifacts(carbonRoot, receipt.Plan); err != nil {
			return err
		}
		receipt.Status = "failed"
		if err := writeImportReceipt(directory, entry.Name(), receipt); err != nil {
			return err
		}
	}
	return nil
}

func quarantineImportArtifacts(carbonRoot string, plan LegacyImportPlan) error {
	if !validID(plan.ID, "migration") || !validID(plan.ClusterID, "cluster") {
		return nil
	}
	failedRoot, err := ensureDataRoot(carbonRoot, "failed")
	if err != nil {
		return err
	}
	for _, artifact := range []struct {
		relative string
		name     string
	}{
		{relative: path.Join("staging", plan.ID), name: plan.ID + "-staging"},
		{relative: path.Join(clusterDataDirectory, plan.ClusterID), name: plan.ID + "-data"},
	} {
		source, err := dataRoot(carbonRoot, artifact.relative)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		destination, err := uniqueImportQuarantinePath(failedRoot, artifact.name)
		if err != nil {
			return err
		}
		if err := os.Rename(source, destination); err != nil {
			return fmt.Errorf("carbon: quarantine incomplete import: %w", err)
		}
		syncImportDirectory(failedRoot)
	}
	return nil
}

func uniqueImportQuarantinePath(directory, base string) (string, error) {
	for suffix := 1; ; suffix++ {
		name := base
		if suffix > 1 {
			name = fmt.Sprintf("%s-%d", base, suffix)
		}
		candidate := filepath.Join(directory, name)
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
		if isReparsePoint(candidate, info) || !info.IsDir() {
			return "", fmt.Errorf("%w: refusing import quarantine target %s", ErrUnsafePath, candidate)
		}
	}
}

func importedCluster(plan LegacyImportPlan) (Cluster, error) {
	for _, cluster := range plan.Manifest.Clusters {
		if cluster.ID == plan.ClusterID {
			return cluster, nil
		}
	}
	return Cluster{}, fmt.Errorf("%w: imported cluster missing", ErrInvalidMigrationPlan)
}

func verifyImportSnapshots(plan LegacyImportPlan) error {
	for _, project := range plan.Projects {
		if project.CairnPath == "" {
			continue
		}
		digest, err := hashTree(project.CairnPath)
		if err != nil {
			return err
		}
		if digest != project.SnapshotDigest {
			return fmt.Errorf("%w: source .cairn changed for %s", ErrLegacyChanged, project.SourcePath)
		}
	}
	return nil
}

func backupImportSources(plan LegacyImportPlan, backupRoot string) error {
	for _, project := range plan.Projects {
		if project.CairnPath == "" {
			continue
		}
		destination, err := ensureDataRoot(backupRoot, path.Join(project.TargetID, ".cairn"))
		if err != nil {
			return err
		}
		if err := copyStrictTree(project.CairnPath, destination); err != nil {
			return fmt.Errorf("carbon: backup source project %s: %w", project.SourcePath, err)
		}
	}
	return nil
}

func copyStrictTree(source, destination string) error {
	return walkStrictTree(source, func(relative string, info os.FileInfo, data []byte) error {
		if relative == "" {
			return nil
		}
		if info.IsDir() {
			_, err := ensureDataRoot(destination, filepath.ToSlash(relative))
			return err
		}
		dir := filepath.Dir(filepath.FromSlash(relative))
		outDir := destination
		if dir != "." {
			var err error
			outDir, err = ensureDataRoot(destination, filepath.ToSlash(dir))
			if err != nil {
				return err
			}
		}
		return atomicWriteRegular(outDir, filepath.Base(relative), data)
	})
}

func importConfigs(plan LegacyImportPlan, dataRoot string) error {
	var primary *ConfigImport
	primaryPath := ""
	for index := range plan.Configs {
		cfg := &plan.Configs[index]
		data, err := verifiedImportFile(cfg.SourceFile, cfg.SourceHash)
		if err != nil {
			return err
		}
		importsDir, err := ensureDataRoot(dataRoot, path.Join(repo.CarbonDirName, "imports", cfg.ProjectID))
		if err != nil {
			return err
		}
		if err := atomicWriteRegular(importsDir, "config.yaml", data); err != nil {
			return err
		}
		if cfg.Primary {
			primary = cfg
			primaryPath = filepath.Join(importsDir, "config.yaml")
		}
	}
	if primary == nil {
		return nil
	}
	// Parse the just-written verified audit copy rather than reopening the mutable
	// legacy source file. This keeps the selected workflow bound to SourceHash even
	// if a source process races an import after its snapshot was reviewed.
	cfg, err := config.Load(primaryPath)
	if err != nil {
		if plan.ConfigPolicy == "primary" {
			return fmt.Errorf("%w: selected primary config is invalid: %v", ErrInvalidMigrationPlan, err)
		}
		return nil
	}
	// The selected workflow becomes the cluster store's workflow, but no default
	// project id may leak into shared task creation.
	cfg.ProjectID = ""
	if err := config.Save(filepath.Join(dataRoot, repo.CarbonDirName, "config.yaml"), cfg); err != nil {
		return err
	}
	return nil
}

func importTasksAndSessions(plan LegacyImportPlan, dataRoot string) error {
	taskLocal, taskGlobal := taskReferenceMaps(plan.Tasks)
	sessionLocal, sessionGlobal := sessionReferenceMaps(plan.Sessions)
	attemptLocal, attemptGlobal := attemptReferenceMaps(plan.Sessions)
	tasksDir, err := ensureDataRoot(dataRoot, path.Join(repo.CarbonDirName, "tasks"))
	if err != nil {
		return err
	}
	for _, imported := range plan.Tasks {
		data, err := verifiedImportFile(imported.SourceFile, imported.SourceHash)
		if err != nil {
			return err
		}
		converted, err := rewriteImportedTask(data, imported, taskLocal, taskGlobal, sessionLocal, sessionGlobal, attemptLocal, attemptGlobal)
		if err != nil {
			return err
		}
		if err := atomicWriteRegular(tasksDir, imported.TargetID+".md", converted); err != nil {
			return err
		}
	}
	sessionsDir, err := ensureDataRoot(dataRoot, path.Join(repo.CarbonDirName, "sessions"))
	if err != nil {
		return err
	}
	for _, imported := range plan.Sessions {
		data, err := verifiedImportFile(imported.SourceFile, imported.SourceHash)
		if err != nil {
			return err
		}
		converted, err := rewriteImportedSession(data, imported)
		if err != nil {
			return err
		}
		if err := atomicWriteRegular(sessionsDir, imported.TargetID+".yaml", converted); err != nil {
			return err
		}
	}
	return nil
}

func importRuns(plan LegacyImportPlan, dataRoot string) error {
	runsDir, err := ensureDataRoot(dataRoot, path.Join(repo.CarbonDirName, "runs"))
	if err != nil {
		return err
	}
	for _, imported := range plan.Runs {
		data, err := verifiedImportFile(imported.SourceFile, imported.SourceHash)
		if err != nil {
			return err
		}
		filename := uniqueOutputFilename(runsDir, imported.TargetFilename)
		if err := atomicWriteRegular(runsDir, filename, data); err != nil {
			return err
		}
	}
	return nil
}

func verifiedImportFile(filename, wantHash string) ([]byte, error) {
	data, exists, err := readStrictRegularFile(filename)
	if err != nil {
		return nil, err
	}
	if !exists || hashBytesHex(data) != wantHash {
		return nil, fmt.Errorf("%w: import source changed %s", ErrLegacyChanged, filename)
	}
	return data, nil
}

func uniqueOutputFilename(directory, filename string) string {
	if _, err := os.Lstat(filepath.Join(directory, filename)); errors.Is(err, os.ErrNotExist) {
		return filename
	}
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s-%d%s", base, suffix, ext)
		if _, err := os.Lstat(filepath.Join(directory, candidate)); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
}

func atomicWriteRegular(directory, filename string, data []byte) error {
	if filepath.Base(filename) != filename || filename == "" || filename == "." || filename == ".." {
		return fmt.Errorf("%w: invalid target filename", ErrUnsafePath)
	}
	target := filepath.Join(directory, filename)
	if info, err := os.Lstat(target); err == nil {
		if isReparsePoint(target, info) || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: refusing target %s", ErrUnsafePath, target)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temp, err := os.CreateTemp(directory, ".carbon-import-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil && (isReparsePoint(target, info) || !info.Mode().IsRegular()) {
		return fmt.Errorf("%w: refusing target %s", ErrUnsafePath, target)
	}
	if err := os.Rename(tempPath, target); err != nil {
		return err
	}
	return nil
}

func writeImportReceipt(directory, filename string, receipt LegacyImportReceipt) error {
	data, err := jsonMarshalIndented(receipt)
	if err != nil {
		return err
	}
	return atomicWriteRegular(directory, filename, data)
}

func legacyAlreadyImported(carbonRoot, legacyRoot string) (bool, error) {
	directory := filepath.Join(carbonRoot, "receipts")
	entries, exists, err := strictReadDir(directory)
	if err != nil || !exists {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		filename := filepath.Join(directory, entry.Name())
		data, exists, err := readStrictRegularFile(filename)
		if err != nil {
			return false, err
		}
		if !exists {
			continue
		}
		var receipt LegacyImportReceipt
		if err := json.Unmarshal(data, &receipt); err != nil {
			return false, fmt.Errorf("%w: invalid import receipt %s", ErrInvalidManifest, filename)
		}
		// Prepared and failed records are recovery evidence, not a completed import.
		// Only a fully committed receipt may prevent a later reviewed retry.
		if receipt.Status == "completed" && receipt.Plan.LegacyRoot != "" && samePath(receipt.Plan.LegacyRoot, legacyRoot) {
			return true, nil
		}
	}
	return false, nil
}

func jsonMarshalIndented(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func sessionReferenceMaps(sessions []SessionImport) (map[string]map[string]string, map[string]string) {
	local := map[string]map[string]string{}
	counts := map[string]int{}
	global := map[string]string{}
	for _, session := range sessions {
		if local[session.ProjectID] == nil {
			local[session.ProjectID] = map[string]string{}
		}
		local[session.ProjectID][session.SourceID] = session.TargetID
		counts[session.SourceID]++
		global[session.SourceID] = session.TargetID
	}
	for id, count := range counts {
		if count != 1 {
			delete(global, id)
		}
	}
	return local, global
}

func attemptReferenceMaps(sessions []SessionImport) (map[string]map[string]string, map[string]string) {
	local := map[string]map[string]string{}
	counts := map[string]int{}
	global := map[string]string{}
	for _, session := range sessions {
		if session.SourceAttemptID == "" {
			continue
		}
		if local[session.ProjectID] == nil {
			local[session.ProjectID] = map[string]string{}
		}
		local[session.ProjectID][session.SourceAttemptID] = session.TargetAttemptID
		counts[session.SourceAttemptID]++
		global[session.SourceAttemptID] = session.TargetAttemptID
	}
	for id, count := range counts {
		if count != 1 {
			delete(global, id)
		}
	}
	return local, global
}

func rewriteImportedTask(data []byte, imported TaskImport, taskLocal map[string]map[string]string, taskGlobal map[string]string, sessionLocal map[string]map[string]string, sessionGlobal map[string]string, attemptLocal map[string]map[string]string, attemptGlobal map[string]string) ([]byte, error) {
	frontmatter, body, err := splitImportedFrontmatter(data)
	if err != nil {
		return nil, err
	}
	var node yaml.Node
	if err := yaml.Unmarshal(frontmatter, &node); err != nil {
		return nil, err
	}
	mapping, err := yamlMapping(&node)
	if err != nil {
		return nil, err
	}
	id, _ := yamlMappingString(mapping, "id")
	if id != imported.SourceID {
		return nil, fmt.Errorf("%w: source task id changed", ErrLegacyChanged)
	}
	setYAMLScalar(mapping, "id", imported.TargetID)
	setYAMLScalar(mapping, "project_id", imported.ProjectID)
	if deps, err := yamlMappingStrings(mapping, "deps"); err != nil {
		return nil, err
	} else if deps != nil {
		for i, dep := range deps {
			mapped, ok := resolveImportedReference(imported.ProjectID, dep, taskLocal, taskGlobal)
			if !ok {
				return nil, fmt.Errorf("%w: task %s has unresolved dependency %s", ErrInvalidMigrationPlan, imported.SourceID, dep)
			}
			deps[i] = mapped
		}
		setYAMLStrings(mapping, "deps", deps)
	}
	if parent, ok := yamlMappingString(mapping, "parent"); ok && parent != "" {
		mapped, ok := resolveImportedReference(imported.ProjectID, parent, taskLocal, taskGlobal)
		if !ok {
			return nil, fmt.Errorf("%w: task %s has unresolved parent %s", ErrInvalidMigrationPlan, imported.SourceID, parent)
		}
		setYAMLScalar(mapping, "parent", mapped)
	}
	if attempt, ok := yamlMappingString(mapping, "active_attempt"); ok && attempt != "" {
		if mapped, ok := resolveImportedReference(imported.ProjectID, attempt, attemptLocal, attemptGlobal); ok {
			setYAMLScalar(mapping, "active_attempt", mapped)
		}
	}
	rewriteProvenanceSessions(mapping, imported.ProjectID, sessionLocal, sessionGlobal)
	encoded, err := encodeYAMLMapping(mapping)
	if err != nil {
		return nil, err
	}
	return append(append([]byte("---\n"), encoded...), append([]byte("---\n"), body...)...), nil
}

func rewriteImportedSession(data []byte, imported SessionImport) ([]byte, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, err
	}
	mapping, err := yamlMapping(&node)
	if err != nil {
		return nil, err
	}
	id, _ := yamlMappingString(mapping, "id")
	taskID, _ := yamlMappingString(mapping, "task")
	if id != imported.SourceID || taskID != imported.SourceTaskID {
		return nil, fmt.Errorf("%w: source session changed", ErrLegacyChanged)
	}
	setYAMLScalar(mapping, "id", imported.TargetID)
	setYAMLScalar(mapping, "task", imported.TargetTaskID)
	if imported.SourceAttemptID != "" {
		setYAMLScalar(mapping, "attempt", imported.TargetAttemptID)
	}
	return encodeYAMLMapping(mapping)
}

func rewriteProvenanceSessions(mapping *yaml.Node, projectID string, local map[string]map[string]string, global map[string]string) {
	value, ok := yamlMappingValue(mapping, "provenance")
	if !ok || value.Kind != yaml.SequenceNode {
		return
	}
	for _, entry := range value.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		did, ok := yamlMappingString(entry, "did")
		if !ok {
			continue
		}
		words := strings.Fields(did)
		for i := 0; i+1 < len(words); i++ {
			if words[i] != "session" {
				continue
			}
			if mapped, ok := resolveImportedReference(projectID, words[i+1], local, global); ok {
				did = strings.Replace(did, words[i+1], mapped, 1)
			}
		}
		setYAMLScalar(entry, "did", did)
	}
}

func setYAMLScalar(mapping *yaml.Node, key, value string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
			return
		}
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

func setYAMLStrings(mapping *yaml.Node, key string, values []string) {
	sequence := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, value := range values {
		sequence.Content = append(sequence.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = sequence
			return
		}
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, sequence)
}

func encodeYAMLMapping(mapping *yaml.Node) ([]byte, error) {
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(mapping); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

var _ = io.EOF
var _ = sort.Strings
