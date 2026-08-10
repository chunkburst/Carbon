package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	projectClearStagingDir  = "project-clear-staging"
	projectClearReceiptsDir = "project-clear-receipts"
)

var (
	// ErrProjectTaskDataReferenced prevents a project clear from leaving a surviving
	// peer or cluster-wide task with a dangling parent/dependency reference.
	ErrProjectTaskDataReferenced = errors.New("project task data is referenced by a surviving task")
	// ErrProjectTaskDataScope means a standalone store contains work not bound to its
	// owner project. A clear must never guess whether that work is safe to remove.
	ErrProjectTaskDataScope = errors.New("standalone project task data has an unexpected project scope")
	// ErrProjectTaskDataChanged means a collection changed after it was copied into
	// quarantine but before the atomic directory switch. No source is changed.
	ErrProjectTaskDataChanged = errors.New("project task data changed during clear")
	// ErrProjectTaskDataRollback means a failed directory switch could not be fully
	// restored. The quarantine is retained for deterministic recovery on the next
	// Store.Write instead of pretending the operation was atomic.
	ErrProjectTaskDataRollback = errors.New("project task data clear rollback is incomplete")
)

// ClearProjectTaskDataOptions binds the destructive operation to a stable project id.
// Standalone adds a fail-closed integrity check: every task/trash document in its
// private store must carry this exact project id before any data is touched.
type ClearProjectTaskDataOptions struct {
	ProjectID  string
	Standalone bool
}

// ClearProjectTaskDataResult is intentionally count-only. The durable receipt records
// the same counts and hashes, while no removed task/session/run content is returned to
// HTTP callers.
type ClearProjectTaskDataResult struct {
	ProjectID       string `json:"projectId"`
	TasksDeleted    int    `json:"tasksDeleted"`
	TrashDeleted    int    `json:"trashDeleted"`
	SessionsDeleted int    `json:"sessionsDeleted"`
	LiveDeleted     int    `json:"liveDeleted"`
	RunsDeleted     int    `json:"runsDeleted"`
	ReceiptID       string `json:"receiptId"`
	ClearedAt       string `json:"clearedAt"`
	// BackupRetained is only true if post-commit cleanup could not remove the
	// quarantine. The active project data is still cleared and the next Store.Write
	// retries cleanup from the committed receipt.
	BackupRetained bool `json:"backupRetained,omitempty"`
}

type projectClearCounts struct {
	Tasks    int `json:"tasksDeleted"`
	Trash    int `json:"trashDeleted"`
	Sessions int `json:"sessionsDeleted"`
	Live     int `json:"liveDeleted"`
	Runs     int `json:"runsDeleted"`
}

type projectClearReceipt struct {
	Version        int                `json:"version"`
	ID             string             `json:"id"`
	State          string             `json:"state"`
	ProjectID      string             `json:"projectId"`
	Actor          string             `json:"actor"`
	PreparedAt     string             `json:"preparedAt"`
	CommittedAt    string             `json:"committedAt,omitempty"`
	Counts         projectClearCounts `json:"counts"`
	Plans          []projectClearPlan `json:"plans,omitempty"`
	BackupRetained bool               `json:"backupRetained,omitempty"`
}

// projectClearPlan stores only deterministic collection names. Source, replacement,
// and quarantine paths are derived below .carbon on recovery; no caller-controlled
// filesystem paths are journaled.
type projectClearPlan struct {
	Kind  string   `json:"kind"`
	Files []string `json:"files,omitempty"`
}

type projectClearFile struct {
	Name string
	Data []byte
	Hash [sha256.Size]byte
}

type projectClearCollection struct {
	Kind        string
	SourceDir   string
	NextDir     string
	BackupDir   string
	Files       []projectClearFile
	Removed     []string
	SourceMoved bool
	Published   bool
}

// projectClearRuns intentionally does not replace the whole runs directory. Older
// sidecar processes published check logs without taking Store.Write, so swapping the
// directory would be able to discard a concurrently-created foreign-project log.
// Instead, each selected task's regular log is moved into the same-filesystem stage
// individually. Foreign names remain in the live directory throughout the clear.
type projectClearRuns struct {
	SourceDir string
	BackupDir string
	Files     []projectClearFile
	Moved     []bool
}

// ClearProjectTaskData permanently clears only one project's task-shaped data. It
// deliberately does not touch config.yaml, views, templates, worklogs, worker state,
// catalog presentation, or Home metadata. The operation builds complete replacement
// collections first, journals a prepared receipt, then swaps each collection through a
// same-filesystem quarantine under one Store.Write lock. A failed switch is rolled back;
// a process crash is recovered by the next Store.Write before its caller runs.
func (s *Store) ClearProjectTaskData(ctx context.Context, actor string, options ClearProjectTaskDataOptions) (ClearProjectTaskDataResult, error) {
	return s.ClearProjectTaskDataWithFinalizer(ctx, actor, options, nil)
}

// ClearProjectTaskDataWithFinalizer performs the same compound clear as
// ClearProjectTaskData, then invokes finalizer before releasing the Store write lock.
// The finalizer must be short, must not attempt another write to this Store, and should
// return an error if its coupled publication could not complete. It is intentionally
// useful to a higher-level catalog transaction that must publish a manifest only after
// selected task data is gone, without reopening a window for another Store writer.
func (s *Store) ClearProjectTaskDataWithFinalizer(
	ctx context.Context,
	actor string,
	options ClearProjectTaskDataOptions,
	finalizer func(ClearProjectTaskDataResult) error,
) (ClearProjectTaskDataResult, error) {
	projectID := strings.TrimSpace(options.ProjectID)
	if projectID == "" {
		return ClearProjectTaskDataResult{}, ErrProjectIDRequired
	}
	var result ClearProjectTaskDataResult
	err := s.Write(ctx, actor, "clear project task data project_id="+projectID, func(tx *WriteTx) error {
		var err error
		result, err = tx.clearProjectTaskData(actor, ClearProjectTaskDataOptions{ProjectID: projectID, Standalone: options.Standalone})
		if err != nil {
			return err
		}
		if finalizer != nil {
			return finalizer(result)
		}
		return nil
	})
	return result, err
}

func (tx *WriteTx) clearProjectTaskData(actor string, options ClearProjectTaskDataOptions) (ClearProjectTaskDataResult, error) {
	projectID := options.ProjectID
	if options.Standalone {
		cfg, err := tx.Config()
		if err != nil {
			return ClearProjectTaskDataResult{}, err
		}
		if cfg.ProjectID != projectID {
			return ClearProjectTaskDataResult{}, fmt.Errorf("%w: config project_id=%q, selected=%q", ErrProjectTaskDataScope, cfg.ProjectID, projectID)
		}
	}

	active, err := tx.store.clearListDocs()
	if err != nil {
		return ClearProjectTaskDataResult{}, err
	}
	trashed, err := tx.store.ListTrashDocs()
	if err != nil {
		return ClearProjectTaskDataResult{}, err
	}
	if options.Standalone {
		for _, doc := range append(append([]*Doc(nil), active...), trashed...) {
			if doc.Task.ProjectID != projectID {
				return ClearProjectTaskDataResult{}, fmt.Errorf("%w: task %s project_id=%q", ErrProjectTaskDataScope, doc.Task.ID, doc.Task.ProjectID)
			}
		}
	}

	activeIDs := make(map[string]struct{})
	trashIDs := make(map[string]struct{})
	selectedTaskIDs := make(map[string]struct{})
	for _, doc := range active {
		if doc.Task.ProjectID == projectID {
			activeIDs[doc.Task.ID] = struct{}{}
			selectedTaskIDs[doc.Task.ID] = struct{}{}
		}
	}
	for _, doc := range trashed {
		if doc.Task.ProjectID == projectID {
			if _, exists := activeIDs[doc.Task.ID]; exists {
				return ClearProjectTaskDataResult{}, fmt.Errorf("%w: task %s is both active and trashed", ErrProjectTaskDataChanged, doc.Task.ID)
			}
			trashIDs[doc.Task.ID] = struct{}{}
			selectedTaskIDs[doc.Task.ID] = struct{}{}
		}
	}
	for _, doc := range append(append([]*Doc(nil), active...), trashed...) {
		if _, selected := selectedTaskIDs[doc.Task.ID]; selected {
			continue
		}
		if _, selected := selectedTaskIDs[doc.Task.Parent]; selected {
			return ClearProjectTaskDataResult{}, fmt.Errorf("%w: task %s has parent %s", ErrProjectTaskDataReferenced, doc.Task.ID, doc.Task.Parent)
		}
		for _, dep := range doc.Task.Deps {
			if _, selected := selectedTaskIDs[dep]; selected {
				return ClearProjectTaskDataResult{}, fmt.Errorf("%w: task %s depends on %s", ErrProjectTaskDataReferenced, doc.Task.ID, dep)
			}
		}
	}

	sessions, err := tx.store.ListSessions()
	if err != nil {
		return ClearProjectTaskDataResult{}, err
	}
	sessionNames := make(map[string]struct{})
	selectedSessionIDs := make(map[string]struct{})
	for _, doc := range sessions {
		if _, selected := selectedTaskIDs[doc.Session.TaskID]; selected {
			sessionNames[doc.Session.ID+".yaml"] = struct{}{}
			selectedSessionIDs[doc.Session.ID] = struct{}{}
		}
	}

	receiptID, err := newProjectClearID()
	if err != nil {
		return ClearProjectTaskDataResult{}, err
	}
	now := time.Now().UTC()
	counts := projectClearCounts{Tasks: len(activeIDs), Trash: len(trashIDs), Sessions: len(sessionNames)}
	result := ClearProjectTaskDataResult{
		ProjectID: projectID, TasksDeleted: counts.Tasks, TrashDeleted: counts.Trash,
		SessionsDeleted: counts.Sessions, ReceiptID: receiptID, ClearedAt: now.Format(time.RFC3339Nano),
	}

	// An empty clear is intentionally idempotent but still leaves a count-only audit
	// receipt. It does not create a quarantine directory or modify project counters.
	if len(selectedTaskIDs) == 0 {
		receipt := projectClearReceipt{Version: 1, ID: receiptID, State: "committed", ProjectID: projectID, Actor: actor, PreparedAt: result.ClearedAt, CommittedAt: result.ClearedAt, Counts: counts}
		if err := tx.store.writeProjectClearReceipt(receipt); err != nil {
			return ClearProjectTaskDataResult{}, err
		}
		return result, nil
	}

	stageRoot, err := tx.store.newProjectClearStage(receiptID)
	if err != nil {
		return ClearProjectTaskDataResult{}, err
	}
	building := projectClearReceipt{Version: 1, ID: receiptID, State: "building", ProjectID: projectID, Actor: actor, PreparedAt: result.ClearedAt, Counts: counts}
	if err := tx.store.writeProjectClearStageReceipt(stageRoot, building); err != nil {
		_ = tx.store.removeProjectClearTree(stageRoot)
		return ClearProjectTaskDataResult{}, err
	}

	cleanupStage := func() { _ = tx.store.removeProjectClearTree(stageRoot) }
	activeNames := exactProjectClearNames(activeIDs, ".md")
	trashNames := exactProjectClearNames(trashIDs, ".md")
	plans := make([]*projectClearCollection, 0, 4)
	for _, request := range []struct {
		kind     string
		remove   func(string) bool
		expected int
	}{
		{kind: "tasks", remove: activeNames, expected: len(activeIDs)},
		{kind: "trash", remove: trashNames, expected: len(trashIDs)},
		{kind: "sessions", remove: exactProjectClearNames(sessionNames, ""), expected: len(sessionNames)},
		{kind: "live", remove: exactProjectClearNames(sessionIDFileNames(selectedSessionIDs, ".json"), ""), expected: -1},
	} {
		plan, err := tx.store.buildProjectClearCollection(stageRoot, request.kind, request.remove, request.expected)
		if err != nil {
			cleanupStage()
			return ClearProjectTaskDataResult{}, err
		}
		if plan != nil {
			plans = append(plans, plan)
			switch plan.Kind {
			case "live":
				counts.Live = len(plan.Removed)
			}
		}
	}
	runPlan, err := tx.store.buildProjectClearRuns(stageRoot, projectClearRunName(selectedTaskIDs))
	if err != nil {
		cleanupStage()
		return ClearProjectTaskDataResult{}, err
	}
	if runPlan != nil {
		counts.Runs = len(runPlan.Files)
	}
	result.LiveDeleted = counts.Live
	result.RunsDeleted = counts.Runs

	journal := building
	journal.State = "prepared"
	journal.Counts = counts
	journal.Plans = make([]projectClearPlan, 0, len(plans)+1)
	for _, plan := range plans {
		journal.Plans = append(journal.Plans, projectClearPlan{Kind: plan.Kind})
		if err := tx.store.verifyProjectClearCollection(plan); err != nil {
			cleanupStage()
			return ClearProjectTaskDataResult{}, err
		}
	}
	if runPlan != nil {
		journal.Plans = append(journal.Plans, projectClearPlan{Kind: "runs", Files: projectClearFileNames(runPlan.Files)})
		if err := tx.store.verifyProjectClearRuns(runPlan); err != nil {
			cleanupStage()
			return ClearProjectTaskDataResult{}, err
		}
	}
	if err := tx.store.writeProjectClearStageReceipt(stageRoot, journal); err != nil {
		cleanupStage()
		return ClearProjectTaskDataResult{}, err
	}
	journal.State = "switching"
	if err := tx.store.writeProjectClearStageReceipt(stageRoot, journal); err != nil {
		cleanupStage()
		return ClearProjectTaskDataResult{}, err
	}

	if err := tx.store.commitProjectClearCollections(plans); err != nil {
		rollbackErr := tx.store.rollbackProjectClearCollections(plans)
		if rollbackErr != nil {
			return ClearProjectTaskDataResult{}, errors.Join(err, rollbackErr)
		}
		cleanupStage()
		return ClearProjectTaskDataResult{}, err
	}
	if err := tx.store.commitProjectClearRuns(runPlan); err != nil {
		runRollbackErr := tx.store.rollbackProjectClearRuns(runPlan)
		collectionRollbackErr := tx.store.rollbackProjectClearCollections(plans)
		if runRollbackErr != nil || collectionRollbackErr != nil {
			return ClearProjectTaskDataResult{}, errors.Join(err, runRollbackErr, collectionRollbackErr)
		}
		cleanupStage()
		return ClearProjectTaskDataResult{}, err
	}

	committed := journal
	committed.State = "committed"
	committed.CommittedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := tx.store.writeProjectClearReceipt(committed); err != nil {
		runRollbackErr := tx.store.rollbackProjectClearRuns(runPlan)
		collectionRollbackErr := tx.store.rollbackProjectClearCollections(plans)
		if runRollbackErr != nil || collectionRollbackErr != nil {
			return ClearProjectTaskDataResult{}, errors.Join(err, runRollbackErr, collectionRollbackErr)
		}
		cleanupStage()
		return ClearProjectTaskDataResult{}, err
	}
	// A committed external receipt is authoritative during crash recovery. This stage
	// update is best-effort; a failure leaves a prepared journal whose receipt still
	// proves that recovery must finish cleanup rather than restore task data.
	_ = tx.store.writeProjectClearStageReceipt(stageRoot, committed)
	if err := tx.store.removeProjectClearTree(stageRoot); err != nil {
		result.BackupRetained = true
		committed.BackupRetained = true
		_ = tx.store.writeProjectClearReceipt(committed)
	}
	return result, nil
}

func (s *Store) clearListDocs() ([]*Doc, error) {
	docs, err := s.ListDocs()
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return docs, err
}

func exactProjectClearNames(ids map[string]struct{}, suffix string) func(string) bool {
	return func(name string) bool {
		_, ok := ids[strings.TrimSuffix(name, suffix)]
		return ok && (suffix == "" || strings.HasSuffix(name, suffix))
	}
}

func sessionIDFileNames(ids map[string]struct{}, suffix string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for id := range ids {
		out[id+suffix] = struct{}{}
	}
	return out
}

func projectClearRunName(taskIDs map[string]struct{}) func(string) bool {
	return func(name string) bool {
		if !strings.HasSuffix(name, ".log") {
			return false
		}
		for id := range taskIDs {
			if strings.HasPrefix(name, id+"-") {
				return true
			}
		}
		return false
	}
}

func newProjectClearID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("store: generate project clear receipt id: %w", err)
	}
	return "clear_" + hex.EncodeToString(data[:]), nil
}

func projectClearFileNames(files []projectClearFile) []string {
	names := make([]string, len(files))
	for index, file := range files {
		names[index] = file.Name
	}
	return names
}

func (s *Store) newProjectClearStage(id string) (string, error) {
	if err := validateDataComponent(id); err != nil {
		return "", err
	}
	parent, err := s.managedDir(true, carbonStoreDir, projectClearStagingDir)
	if err != nil {
		return "", err
	}
	stage := filepath.Join(parent, id)
	if exists, err := s.projectClearDirectoryState(stage); err != nil {
		return "", err
	} else if exists {
		return "", fmt.Errorf("%w: duplicate staging id %s", ErrProjectTaskDataChanged, id)
	}
	if err := os.Mkdir(stage, 0o700); err != nil {
		return "", fmt.Errorf("store: create project clear stage: %w", err)
	}
	if exists, err := s.projectClearDirectoryState(stage); err != nil || !exists {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("%w: staging directory disappeared", ErrProjectTaskDataChanged)
	}
	return stage, nil
}

// buildProjectClearRuns snapshots only the selected task's log names. It must not
// snapshot or replace the whole directory: a legacy sidecar can publish a foreign
// task's log without participating in Store.Write.
func (s *Store) buildProjectClearRuns(stageRoot string, selected func(string) bool) (*projectClearRuns, error) {
	source, err := s.managedDir(false, carbonStoreDir, "runs")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return nil, err
	}
	files := make([]projectClearFile, 0)
	for _, entry := range entries {
		if !selected(entry.Name()) {
			continue
		}
		path, err := s.managedFile(source, entry.Name(), true, true)
		if err != nil {
			return nil, fmt.Errorf("%w: run %s", ErrProjectTaskDataChanged, err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if isStoreReparsePoint(path, info) || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: non-regular selected run %s", ErrProjectTaskDataChanged, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		files = append(files, projectClearFile{Name: entry.Name(), Data: data, Hash: sha256.Sum256(data)})
	}
	if len(files) == 0 {
		return nil, nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	backup := filepath.Join(stageRoot, "runs")
	if err := s.makeProjectClearDirectory(backup); err != nil {
		return nil, err
	}
	return &projectClearRuns{SourceDir: source, BackupDir: backup, Files: files, Moved: make([]bool, len(files))}, nil
}

func (s *Store) verifyProjectClearRuns(plan *projectClearRuns) error {
	if plan == nil {
		return nil
	}
	for _, expected := range plan.Files {
		path, err := s.managedFile(plan.SourceDir, expected.Name, true, true)
		if err != nil {
			return fmt.Errorf("%w: selected run %s disappeared: %v", ErrProjectTaskDataChanged, expected.Name, err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if isStoreReparsePoint(path, info) || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: selected run changed to non-regular entry %s", ErrProjectTaskDataChanged, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if sha256.Sum256(data) != expected.Hash {
			return fmt.Errorf("%w: selected run %s changed", ErrProjectTaskDataChanged, expected.Name)
		}
	}
	return nil
}

func (s *Store) commitProjectClearRuns(plan *projectClearRuns) error {
	if plan == nil {
		return nil
	}
	for index, file := range plan.Files {
		source, err := s.managedFile(plan.SourceDir, file.Name, true, true)
		if err != nil {
			return fmt.Errorf("%w: selected run %s disappeared: %v", ErrProjectTaskDataChanged, file.Name, err)
		}
		if err := s.verifyProjectClearFile(source, file.Hash); err != nil {
			return err
		}
		destination, err := s.managedFile(plan.BackupDir, file.Name, false, true)
		if err != nil {
			return err
		}
		moved, err := s.moveProjectClearRunFile(source, destination)
		plan.Moved[index] = moved
		if err != nil {
			return fmt.Errorf("store: quarantine run %s: %w", file.Name, err)
		}
	}
	return nil
}

func (s *Store) rollbackProjectClearRuns(plan *projectClearRuns) error {
	if plan == nil {
		return nil
	}
	var rollbackErrors []error
	for index := len(plan.Files) - 1; index >= 0; index-- {
		if !plan.Moved[index] {
			continue
		}
		file := plan.Files[index]
		backup, err := s.managedFile(plan.BackupDir, file.Name, true, true)
		if err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("resolve quarantined run %s: %w", file.Name, err))
			continue
		}
		source, err := s.managedFile(plan.SourceDir, file.Name, false, true)
		if err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("resolve run restore %s: %w", file.Name, err))
			continue
		}
		if _, err := s.moveProjectClearRunFile(backup, source); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore quarantined run %s: %w", file.Name, err))
		}
	}
	if len(rollbackErrors) != 0 {
		return fmt.Errorf("%w: %w", ErrProjectTaskDataRollback, errors.Join(rollbackErrors...))
	}
	return nil
}

func (s *Store) verifyProjectClearFile(path string, expected [sha256.Size]byte) error {
	if err := s.validateManagedWritePath(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if isStoreReparsePoint(path, info) || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: non-regular selected clear file %s", ErrProjectTaskDataChanged, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if sha256.Sum256(data) != expected {
		return fmt.Errorf("%w: selected clear file %s changed", ErrProjectTaskDataChanged, path)
	}
	return nil
}

// moveProjectClearRunFile is a no-replace, same-filesystem quarantine move. os.Link
// atomically refuses an existing destination (unlike os.Rename on POSIX), then the
// source link is removed. That preserves a concurrently-published foreign run and
// also fails closed if a legacy writer recreates this selected filename during rollback.
func (s *Store) moveProjectClearRunFile(source, destination string) (bool, error) {
	if err := s.validateManagedWritePath(source); err != nil {
		return false, err
	}
	if err := s.validateManagedWritePath(destination); err != nil {
		return false, err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return false, err
	}
	if isStoreReparsePoint(source, info) || !info.Mode().IsRegular() {
		return false, fmt.Errorf("%w: source run is not a regular file %s", ErrProjectTaskDataChanged, source)
	}
	if _, err := os.Lstat(destination); err == nil {
		return false, fmt.Errorf("%w: run quarantine destination already exists: %s", ErrProjectTaskDataChanged, destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}

	// The test seam deliberately retains the normal rename failure injection used by
	// Store mutation tests. Production uses Link+Remove for no-clobber semantics.
	if s.renameFn != nil {
		if err := s.renameFn(source, destination); err != nil {
			return false, err
		}
	} else {
		if err := os.Link(source, destination); err != nil {
			return false, err
		}
		if err := os.Remove(source); err != nil {
			if cleanupErr := os.Remove(destination); cleanupErr != nil {
				return false, fmt.Errorf("remove source %s: %w (also remove staged link: %v)", source, err, cleanupErr)
			}
			return false, err
		}
	}

	var syncErrors []error
	if err := syncAtomicParent(filepath.Dir(source)); err != nil {
		syncErrors = append(syncErrors, err)
	}
	if filepath.Clean(filepath.Dir(source)) != filepath.Clean(filepath.Dir(destination)) {
		if err := syncAtomicParent(filepath.Dir(destination)); err != nil {
			syncErrors = append(syncErrors, err)
		}
	}
	if len(syncErrors) != 0 {
		return true, fmt.Errorf("%w: project clear run move: %w", ErrAtomicWritePublished, errors.Join(syncErrors...))
	}
	return true, nil
}

func (s *Store) buildProjectClearCollection(stageRoot, kind string, remove func(string) bool, expectedRemoved int) (*projectClearCollection, error) {
	source, err := s.managedDir(false, carbonStoreDir, kind)
	if errors.Is(err, os.ErrNotExist) {
		if expectedRemoved > 0 {
			return nil, fmt.Errorf("%w: %s collection disappeared", ErrProjectTaskDataChanged, kind)
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	files, err := s.readProjectClearCollection(source)
	if err != nil {
		return nil, err
	}
	removed := make([]string, 0)
	for _, file := range files {
		if remove(file.Name) {
			removed = append(removed, file.Name)
		}
	}
	if expectedRemoved >= 0 && len(removed) != expectedRemoved {
		return nil, fmt.Errorf("%w: %s selection changed", ErrProjectTaskDataChanged, kind)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	next := filepath.Join(stageRoot, "next-"+kind)
	if err := s.makeProjectClearDirectory(next); err != nil {
		return nil, err
	}
	removedSet := make(map[string]struct{}, len(removed))
	for _, name := range removed {
		removedSet[name] = struct{}{}
	}
	for _, file := range files {
		if _, deleted := removedSet[file.Name]; deleted {
			continue
		}
		dest, err := s.managedFile(next, file.Name, false, true)
		if err != nil {
			return nil, err
		}
		if err := s.writeAtomic(dest, file.Data); err != nil {
			return nil, err
		}
		copy, err := os.ReadFile(dest)
		if err != nil || sha256.Sum256(copy) != file.Hash {
			if err != nil {
				return nil, fmt.Errorf("store: verify project clear staged %s: %w", file.Name, err)
			}
			return nil, fmt.Errorf("%w: staged %s differs", ErrProjectTaskDataChanged, file.Name)
		}
	}
	return &projectClearCollection{Kind: kind, SourceDir: source, NextDir: next, BackupDir: filepath.Join(stageRoot, "backup-"+kind), Files: files, Removed: removed}, nil
}

func (s *Store) readProjectClearCollection(dir string) ([]projectClearFile, error) {
	if exists, err := s.projectClearDirectoryState(dir); err != nil || !exists {
		if err != nil {
			return nil, err
		}
		return nil, os.ErrNotExist
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]projectClearFile, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		path, err := s.managedFile(dir, name, true, true)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrProjectTaskDataChanged, err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if isStoreReparsePoint(path, info) || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: non-regular collection entry %s", ErrProjectTaskDataChanged, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		files = append(files, projectClearFile{Name: name, Data: data, Hash: sha256.Sum256(data)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

func (s *Store) verifyProjectClearCollection(plan *projectClearCollection) error {
	current, err := s.readProjectClearCollection(plan.SourceDir)
	if err != nil {
		return err
	}
	if len(current) != len(plan.Files) {
		return fmt.Errorf("%w: %s collection entry count changed", ErrProjectTaskDataChanged, plan.Kind)
	}
	for i := range current {
		if current[i].Name != plan.Files[i].Name || current[i].Hash != plan.Files[i].Hash {
			return fmt.Errorf("%w: %s collection changed", ErrProjectTaskDataChanged, plan.Kind)
		}
	}
	return nil
}

func (s *Store) makeProjectClearDirectory(path string) error {
	if exists, err := s.projectClearDirectoryState(path); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("%w: unexpected existing clear directory %s", ErrProjectTaskDataChanged, path)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return err
	}
	if exists, err := s.projectClearDirectoryState(path); err != nil || !exists {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: clear directory disappeared", ErrProjectTaskDataChanged)
	}
	return nil
}

func (s *Store) commitProjectClearCollections(plans []*projectClearCollection) error {
	for _, plan := range plans {
		moved, err := s.renameProjectClearDirectory(plan.SourceDir, plan.BackupDir)
		plan.SourceMoved = moved
		if err != nil {
			return fmt.Errorf("store: quarantine %s: %w", plan.Kind, err)
		}
		published, err := s.renameProjectClearDirectory(plan.NextDir, plan.SourceDir)
		plan.Published = published
		if err != nil {
			return fmt.Errorf("store: publish cleared %s: %w", plan.Kind, err)
		}
	}
	return nil
}

func (s *Store) rollbackProjectClearCollections(plans []*projectClearCollection) error {
	var rollbackErrors []error
	for index := len(plans) - 1; index >= 0; index-- {
		plan := plans[index]
		if plan.Published {
			if _, err := s.renameProjectClearDirectory(plan.SourceDir, plan.NextDir); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("move replacement %s back to staging: %w", plan.Kind, err))
			}
		}
		if plan.SourceMoved {
			if _, err := s.renameProjectClearDirectory(plan.BackupDir, plan.SourceDir); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore quarantined %s: %w", plan.Kind, err))
			}
		}
	}
	if len(rollbackErrors) > 0 {
		return fmt.Errorf("%w: %w", ErrProjectTaskDataRollback, errors.Join(rollbackErrors...))
	}
	return nil
}

// renameProjectClearDirectory uses the same injected rename seam as other Store
// mutations. The boolean tells the caller whether the namespace change happened even
// though a durability sync subsequently failed, so compensation can make the right
// choice instead of assuming an error always means no publication.
func (s *Store) renameProjectClearDirectory(oldPath, newPath string) (bool, error) {
	oldExists, err := s.projectClearDirectoryState(oldPath)
	if err != nil {
		return false, err
	}
	if !oldExists {
		return false, fmt.Errorf("%w: source directory is missing: %s", ErrProjectTaskDataChanged, oldPath)
	}
	newExists, err := s.projectClearDirectoryState(newPath)
	if err != nil {
		return false, err
	}
	if newExists {
		return false, fmt.Errorf("%w: destination directory already exists: %s", ErrProjectTaskDataChanged, newPath)
	}
	if s.renameFn != nil {
		if err := s.renameFn(oldPath, newPath); err != nil {
			return false, err
		}
	} else if err := os.Rename(oldPath, newPath); err != nil {
		return false, err
	}
	var syncErrors []error
	if err := syncAtomicParent(filepath.Dir(oldPath)); err != nil {
		syncErrors = append(syncErrors, err)
	}
	if filepath.Clean(filepath.Dir(oldPath)) != filepath.Clean(filepath.Dir(newPath)) {
		if err := syncAtomicParent(filepath.Dir(newPath)); err != nil {
			syncErrors = append(syncErrors, err)
		}
	}
	if len(syncErrors) > 0 {
		return true, fmt.Errorf("%w: project clear directory rename: %w", ErrAtomicWritePublished, errors.Join(syncErrors...))
	}
	return true, nil
}

func (s *Store) projectClearDirectoryState(path string) (bool, error) {
	root, err := s.storeRoot()
	if err != nil {
		return false, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	abs = filepath.Clean(abs)
	if !storePathWithin(root, abs) || filepath.Clean(abs) == filepath.Clean(root) {
		return false, fmt.Errorf("%w: clear path outside store root %s", ErrPathOutsideRoot, path)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false, fmt.Errorf("%w: invalid clear path %s", ErrPathOutsideRoot, path)
	}
	current := root
	parts := strings.Split(rel, string(filepath.Separator))
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false, fmt.Errorf("%w: invalid clear path component", ErrPathOutsideRoot)
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if index != len(parts)-1 {
				return false, fmt.Errorf("%w: missing clear parent %s", ErrPathOutsideRoot, current)
			}
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if isStoreReparsePoint(current, info) || !info.IsDir() {
			return false, fmt.Errorf("%w: clear directory is not physical %s", ErrPathOutsideRoot, current)
		}
	}
	return true, nil
}

func (s *Store) writeProjectClearStageReceipt(stageRoot string, receipt projectClearReceipt) error {
	path, err := s.managedFile(stageRoot, "receipt.json", false, true)
	if err != nil {
		return err
	}
	return s.writeProjectClearReceiptAt(path, receipt)
}

func (s *Store) writeProjectClearReceipt(receipt projectClearReceipt) error {
	dir, err := s.managedDir(true, carbonStoreDir, projectClearReceiptsDir)
	if err != nil {
		return err
	}
	path, err := s.managedFile(dir, receipt.ID+".json", false, true)
	if err != nil {
		return err
	}
	return s.writeProjectClearReceiptAt(path, receipt)
}

func (s *Store) writeProjectClearReceiptAt(path string, receipt projectClearReceipt) error {
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := s.writeAtomic(path, data); err != nil {
		if errors.Is(err, ErrAtomicWritePublished) {
			if current, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(current, data) {
				return nil
			}
		}
		return err
	}
	return nil
}

func (s *Store) removeProjectClearTree(root string) error {
	exists, err := s.projectClearDirectoryState(root)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if isStoreReparsePoint(path, info) {
			return fmt.Errorf("%w: reparse point in clear stage %s", ErrPathOutsideRoot, path)
		}
		if info.IsDir() {
			if err := s.removeProjectClearTree(path); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: non-regular clear stage entry %s", ErrProjectTaskDataChanged, path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	if err := os.Remove(root); err != nil {
		return err
	}
	return syncAtomicParent(filepath.Dir(root))
}

// recoverPendingProjectTaskDataClears runs beneath Store.Write's already-held lock.
// Prepared journals mean the external committed receipt does not exist, so their
// quarantines are restored. Once a committed receipt exists, active data was already
// switched and recovery completes only quarantine cleanup.
func (s *Store) recoverPendingProjectTaskDataClears() error {
	parent, err := s.managedDir(false, carbonStoreDir, projectClearStagingDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || validateDataComponent(entry.Name()) != nil {
			return fmt.Errorf("%w: unsafe project clear staging entry %s", ErrProjectTaskDataChanged, entry.Name())
		}
		stage := filepath.Join(parent, entry.Name())
		if exists, err := s.projectClearDirectoryState(stage); err != nil || !exists {
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: clear stage disappeared", ErrProjectTaskDataChanged)
		}
		receipt, err := s.readProjectClearStageReceipt(stage)
		if err != nil {
			return err
		}
		if receipt.ID != entry.Name() || receipt.Version != 1 || receipt.ProjectID == "" {
			return fmt.Errorf("%w: invalid project clear stage receipt", ErrProjectTaskDataChanged)
		}
		committed, err := s.projectClearReceiptCommitted(receipt.ID)
		if err != nil {
			return err
		}
		if committed {
			if err := s.removeProjectClearTree(stage); err != nil {
				return err
			}
			continue
		}
		switch receipt.State {
		case "building":
			if err := s.removeProjectClearTree(stage); err != nil {
				return err
			}
		case "prepared", "switching", "committed":
			if err := s.recoverPreparedProjectClear(stage, receipt); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: unknown project clear stage state %q", ErrProjectTaskDataChanged, receipt.State)
		}
	}
	return nil
}

// EnsureConsistentRead is a lightweight read barrier for adapters that enumerate raw
// run logs rather than going through Store.List*. A switching marker makes readers
// retry instead of observing the short interval between two collection renames.
func (s *Store) EnsureConsistentRead() error { return s.projectClearReadBarrier() }

func (s *Store) projectClearReadBarrier() error {
	parent, err := s.managedDir(false, carbonStoreDir, projectClearStagingDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || validateDataComponent(entry.Name()) != nil {
			return fmt.Errorf("%w: unsafe project clear staging entry %s", ErrProjectTaskDataChanged, entry.Name())
		}
		receipt, err := s.readProjectClearStageReceipt(filepath.Join(parent, entry.Name()))
		if err != nil {
			return err
		}
		if receipt.State != "switching" {
			continue
		}
		committed, err := s.projectClearReceiptCommitted(receipt.ID)
		if err != nil {
			return err
		}
		if !committed {
			return fmt.Errorf("%w: project task-data maintenance is in progress; retry the read", ErrProjectTaskDataChanged)
		}
	}
	return nil
}

func (s *Store) readProjectClearStageReceipt(stage string) (projectClearReceipt, error) {
	path, err := s.managedFile(stage, "receipt.json", true, true)
	if err != nil {
		return projectClearReceipt{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return projectClearReceipt{}, err
	}
	var receipt projectClearReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return projectClearReceipt{}, err
	}
	return receipt, nil
}

func (s *Store) projectClearReceiptCommitted(id string) (bool, error) {
	dir, err := s.managedDir(false, carbonStoreDir, projectClearReceiptsDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	path, err := s.managedFile(dir, id+".json", false, true)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var receipt projectClearReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return false, err
	}
	return receipt.ID == id && receipt.State == "committed", nil
}

func (s *Store) recoverPreparedProjectClear(stage string, receipt projectClearReceipt) error {
	root, err := s.storeRoot()
	if err != nil {
		return err
	}
	plans := make([]*projectClearCollection, 0, len(receipt.Plans))
	var runPlan *projectClearRuns
	for _, item := range receipt.Plans {
		if item.Kind != "tasks" && item.Kind != "trash" && item.Kind != "sessions" && item.Kind != "live" && item.Kind != "runs" {
			return fmt.Errorf("%w: invalid project clear collection %q", ErrProjectTaskDataChanged, item.Kind)
		}
		if item.Kind == "runs" {
			if runPlan != nil || len(item.Files) == 0 {
				return fmt.Errorf("%w: invalid project clear run receipt", ErrProjectTaskDataChanged)
			}
			source, err := s.managedDir(false, carbonStoreDir, "runs")
			if err != nil {
				return err
			}
			backup := filepath.Join(stage, "runs")
			if exists, err := s.projectClearDirectoryState(backup); err != nil || !exists {
				if err != nil {
					return err
				}
				return fmt.Errorf("%w: missing run quarantine", ErrProjectTaskDataChanged)
			}
			files := make([]projectClearFile, 0, len(item.Files))
			seen := make(map[string]struct{}, len(item.Files))
			for _, name := range item.Files {
				if _, duplicate := seen[name]; duplicate {
					return fmt.Errorf("%w: duplicate selected run %q", ErrProjectTaskDataChanged, name)
				}
				seen[name] = struct{}{}
				if _, err := s.managedFile(backup, name, false, true); err != nil {
					return err
				}
				files = append(files, projectClearFile{Name: name})
			}
			runPlan = &projectClearRuns{SourceDir: source, BackupDir: backup, Files: files, Moved: make([]bool, len(files))}
			continue
		}
		if len(item.Files) != 0 {
			return fmt.Errorf("%w: unexpected file list for %s", ErrProjectTaskDataChanged, item.Kind)
		}
		source, err := s.managedDir(false, carbonStoreDir, item.Kind)
		if errors.Is(err, os.ErrNotExist) {
			source = filepath.Join(root, carbonStoreDir, item.Kind)
		} else if err != nil {
			return err
		}
		plans = append(plans, &projectClearCollection{Kind: item.Kind, SourceDir: source, NextDir: filepath.Join(stage, "next-"+item.Kind), BackupDir: filepath.Join(stage, "backup-"+item.Kind)})
	}
	// Runs are moved individually after the collection swaps. Restore their
	// quarantine first so a failed recovery cannot leave a selected run stranded.
	if runPlan != nil {
		if err := s.recoverPreparedProjectClearRuns(runPlan); err != nil {
			return err
		}
	}
	for index := len(plans) - 1; index >= 0; index-- {
		plan := plans[index]
		sourceExists, err := s.projectClearDirectoryState(plan.SourceDir)
		if err != nil {
			return err
		}
		backupExists, err := s.projectClearDirectoryState(plan.BackupDir)
		if err != nil {
			return err
		}
		nextExists, err := s.projectClearDirectoryState(plan.NextDir)
		if err != nil {
			return err
		}
		if !backupExists {
			if !sourceExists || !nextExists {
				return fmt.Errorf("%w: incomplete uncommitted clear stage for %s", ErrProjectTaskDataChanged, plan.Kind)
			}
			continue
		}
		if sourceExists {
			if nextExists {
				return fmt.Errorf("%w: ambiguous prepared clear stage for %s", ErrProjectTaskDataChanged, plan.Kind)
			}
			if _, err := s.renameProjectClearDirectory(plan.SourceDir, plan.NextDir); err != nil {
				return fmt.Errorf("%w: move replacement back for %s: %v", ErrProjectTaskDataRollback, plan.Kind, err)
			}
		}
		if _, err := s.renameProjectClearDirectory(plan.BackupDir, plan.SourceDir); err != nil {
			return fmt.Errorf("%w: restore quarantine for %s: %v", ErrProjectTaskDataRollback, plan.Kind, err)
		}
	}
	return s.removeProjectClearTree(stage)
}

func (s *Store) recoverPreparedProjectClearRuns(plan *projectClearRuns) error {
	for index, file := range plan.Files {
		backup, err := s.managedFile(plan.BackupDir, file.Name, false, true)
		if err != nil {
			return err
		}
		source, err := s.managedFile(plan.SourceDir, file.Name, false, true)
		if err != nil {
			return err
		}
		backupExists, err := s.projectClearRegularFileState(backup)
		if err != nil {
			return err
		}
		sourceExists, err := s.projectClearRegularFileState(source)
		if err != nil {
			return err
		}
		if !backupExists {
			if !sourceExists {
				return fmt.Errorf("%w: selected run %s is missing from an uncommitted clear", ErrProjectTaskDataChanged, file.Name)
			}
			continue
		}
		if sourceExists {
			// A crash may occur after Link(source, backup) but before Remove(source).
			// The two paths then intentionally name the same inode; source already
			// contains the original run, so discard only the staged duplicate. If
			// they are different files, do not guess which legacy writer won.
			same, err := s.projectClearSameRegularFile(source, backup)
			if err != nil {
				return err
			}
			if !same {
				return fmt.Errorf("%w: selected run %s exists in source and quarantine", ErrProjectTaskDataChanged, file.Name)
			}
			if err := os.Remove(backup); err != nil {
				return fmt.Errorf("%w: discard duplicated staged run %s: %v", ErrProjectTaskDataRollback, file.Name, err)
			}
			if err := syncAtomicParent(filepath.Dir(backup)); err != nil {
				return fmt.Errorf("%w: sync duplicated staged run removal %s: %v", ErrProjectTaskDataRollback, file.Name, err)
			}
			continue
		}
		moved, err := s.moveProjectClearRunFile(backup, source)
		plan.Moved[index] = moved
		if err != nil {
			return fmt.Errorf("%w: restore quarantined run %s: %v", ErrProjectTaskDataRollback, file.Name, err)
		}
	}
	return nil
}

func (s *Store) projectClearRegularFileState(path string) (bool, error) {
	if err := s.validateManagedWritePath(path); err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if isStoreReparsePoint(path, info) || !info.Mode().IsRegular() {
		return false, fmt.Errorf("%w: clear file is not regular %s", ErrProjectTaskDataChanged, path)
	}
	return true, nil
}

func (s *Store) projectClearSameRegularFile(first, second string) (bool, error) {
	if err := s.validateManagedWritePath(first); err != nil {
		return false, err
	}
	if err := s.validateManagedWritePath(second); err != nil {
		return false, err
	}
	firstInfo, err := os.Lstat(first)
	if err != nil {
		return false, err
	}
	secondInfo, err := os.Lstat(second)
	if err != nil {
		return false, err
	}
	if isStoreReparsePoint(first, firstInfo) || isStoreReparsePoint(second, secondInfo) || !firstInfo.Mode().IsRegular() || !secondInfo.Mode().IsRegular() {
		return false, fmt.Errorf("%w: duplicate run recovery requires regular files", ErrProjectTaskDataChanged)
	}
	return os.SameFile(firstInfo, secondInfo), nil
}
