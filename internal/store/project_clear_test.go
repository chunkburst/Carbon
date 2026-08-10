package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"carbon/internal/session"
)

func newProjectClearStore(t *testing.T) *Store {
	t.Helper()
	return New(repo(t, map[string]string{}))
}

func createProjectClearTask(t *testing.T, st *Store, projectID, title string) *Doc {
	t.Helper()
	doc, err := st.Create(Draft{Title: title, ProjectID: projectID, ProjectIDSet: true}, "human:seed", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func createProjectClearSession(t *testing.T, st *Store, id, taskID string, live bool) {
	t.Helper()
	value := session.Session{ID: id, TaskID: taskID, AttemptID: "att_" + id, Actor: "agent:test", Status: session.StatusActive, IdempotencyKey: id, StartedAt: time.Now().UTC()}
	var state *session.Live
	if live {
		state = &session.Live{SessionID: id, HeartbeatAt: value.StartedAt, Worktree: st.Root()}
	}
	if _, err := st.CreateSession(context.Background(), "agent:test", value, state); err != nil {
		t.Fatal(err)
	}
}

func TestClearProjectTaskDataRemovesOnlySelectedProjectArtifacts(t *testing.T) {
	st := newProjectClearStore(t)
	owned := createProjectClearTask(t, st, "project-one", "owned active")
	trashed := createProjectClearTask(t, st, "project-one", "owned trash")
	foreign := createProjectClearTask(t, st, "project-two", "foreign")
	shared := createProjectClearTask(t, st, "", "cluster wide")
	if _, err := st.TrashTasks(context.Background(), "human:seed", []string{trashed.Task.ID}, "test", nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	createProjectClearSession(t, st, "ses_owned", owned.Task.ID, true)
	createProjectClearSession(t, st, "ses_foreign", foreign.Task.ID, true)
	createProjectClearSession(t, st, "ses_orphan", "ORPHAN-1", true)
	runs := st.RunsDir()
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		owned.Task.ID + "-20260809-101010.000.log",
		trashed.Task.ID + "-20260809-101011.000.log",
		foreign.Task.ID + "-20260809-101012.000.log",
		"ORPHAN-1-20260809-101013.000.log",
	} {
		if err := os.WriteFile(filepath.Join(runs, name), []byte("run"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := st.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Counter = 73
	if err := st.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(st.Root(), ".carbon", "config.yaml")
	beforeConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"templates", "views", "worklogs"} {
		if err := os.MkdirAll(filepath.Join(st.Root(), ".carbon", kind), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(st.Root(), ".carbon", kind, "keep.json"), []byte("keep-"+kind), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	result, err := st.ClearProjectTaskData(context.Background(), "human:owner", ClearProjectTaskDataOptions{ProjectID: "project-one"})
	if err != nil {
		t.Fatal(err)
	}
	if result.TasksDeleted != 1 || result.TrashDeleted != 1 || result.SessionsDeleted != 1 || result.LiveDeleted != 1 || result.RunsDeleted != 2 || result.ReceiptID == "" {
		t.Fatalf("clear result = %+v", result)
	}
	if _, err := st.Get(owned.Task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("owned active task remains: %v", err)
	}
	if _, err := st.GetTrash(trashed.Task.ID); !errors.Is(err, ErrTrashNotFound) {
		t.Fatalf("owned trash remains: %v", err)
	}
	if _, err := st.Get(foreign.Task.ID); err != nil {
		t.Fatalf("foreign task removed: %v", err)
	}
	if _, err := st.Get(shared.Task.ID); err != nil {
		t.Fatalf("cluster-wide task removed: %v", err)
	}
	if _, err := st.GetSession("ses_owned"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("owned session remains: %v", err)
	}
	if _, err := st.GetSession("ses_foreign"); err != nil {
		t.Fatalf("foreign session removed: %v", err)
	}
	if _, err := st.GetSession("ses_orphan"); err != nil {
		t.Fatalf("orphan session removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.Root(), ".carbon", "live", "ses_owned.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned live remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.Root(), ".carbon", "live", "ses_foreign.json")); err != nil {
		t.Fatalf("foreign live removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.Root(), ".carbon", "live", "ses_orphan.json")); err != nil {
		t.Fatalf("orphan live removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runs, owned.Task.ID+"-20260809-101010.000.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned run remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runs, trashed.Task.ID+"-20260809-101011.000.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("trashed-task run remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runs, foreign.Task.ID+"-20260809-101012.000.log")); err != nil {
		t.Fatalf("foreign run removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runs, "ORPHAN-1-20260809-101013.000.log")); err != nil {
		t.Fatalf("orphan run removed: %v", err)
	}
	afterConfig, err := os.ReadFile(configPath)
	if err != nil || string(beforeConfig) != string(afterConfig) {
		t.Fatalf("config changed by clear: %q -> %q (%v)", beforeConfig, afterConfig, err)
	}
	for _, kind := range []string{"templates", "views", "worklogs"} {
		if data, err := os.ReadFile(filepath.Join(st.Root(), ".carbon", kind, "keep.json")); err != nil || string(data) != "keep-"+kind {
			t.Fatalf("%s changed by clear: %q %v", kind, data, err)
		}
	}
	afterCfg, err := st.Config()
	if err != nil || afterCfg.Counter != 73 {
		t.Fatalf("project config counter changed by clear: %+v %v", afterCfg, err)
	}
	newTask := createProjectClearTask(t, st, "project-one", "created after clear")
	if newTask.Task.ID == owned.Task.ID || newTask.Task.ID == trashed.Task.ID {
		t.Fatalf("new task reused cleared task id %s", newTask.Task.ID)
	}
	if _, err := os.Stat(filepath.Join(st.Root(), ".carbon", projectClearReceiptsDir, result.ReceiptID+".json")); err != nil {
		t.Fatalf("receipt missing: %v", err)
	}
}

func TestClearProjectTaskDataRejectsSurvivingReferenceWithoutMutation(t *testing.T) {
	st := newProjectClearStore(t)
	owned := createProjectClearTask(t, st, "project-one", "owned")
	foreign := createProjectClearTask(t, st, "project-two", "foreign")
	foreign.SetDeps([]string{owned.Task.ID})
	if err := st.Save(foreign); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(st.Root(), ".carbon", "tasks", owned.Task.ID+".md"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.ClearProjectTaskData(context.Background(), "human:owner", ClearProjectTaskDataOptions{ProjectID: "project-one"})
	if !errors.Is(err, ErrProjectTaskDataReferenced) {
		t.Fatalf("clear referenced data = %v, want ErrProjectTaskDataReferenced", err)
	}
	after, err := os.ReadFile(filepath.Join(st.Root(), ".carbon", "tasks", owned.Task.ID+".md"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("referenced task changed after rejected clear: %v", err)
	}
}

func TestClearProjectTaskDataRejectsTrashedForeignReferenceWithoutMutation(t *testing.T) {
	st := newProjectClearStore(t)
	owned := createProjectClearTask(t, st, "project-one", "owned")
	foreign := createProjectClearTask(t, st, "project-two", "foreign")
	if _, err := st.TrashTasks(context.Background(), "human:seed", []string{foreign.Task.ID}, "test", nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	trashDoc, err := st.GetTrash(foreign.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	trashDoc.SetDeps([]string{owned.Task.ID})
	if err := st.Write(context.Background(), "human:seed", "set trashed foreign dependency", func(tx *WriteTx) error {
		path, err := st.trashFilePath(foreign.Task.ID, false, true, true)
		if err != nil {
			return err
		}
		return st.saveToPath(trashDoc, path, true)
	}); err != nil {
		t.Fatal(err)
	}
	beforeOwned, err := os.ReadFile(filepath.Join(st.Root(), ".carbon", "tasks", owned.Task.ID+".md"))
	if err != nil {
		t.Fatal(err)
	}
	beforeForeign, err := os.ReadFile(filepath.Join(st.Root(), ".carbon", "trash", foreign.Task.ID+".md"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.ClearProjectTaskData(context.Background(), "human:owner", ClearProjectTaskDataOptions{ProjectID: "project-one"})
	if !errors.Is(err, ErrProjectTaskDataReferenced) {
		t.Fatalf("clear with trashed foreign reference = %v, want ErrProjectTaskDataReferenced", err)
	}
	afterOwned, ownedErr := os.ReadFile(filepath.Join(st.Root(), ".carbon", "tasks", owned.Task.ID+".md"))
	afterForeign, foreignErr := os.ReadFile(filepath.Join(st.Root(), ".carbon", "trash", foreign.Task.ID+".md"))
	if ownedErr != nil || foreignErr != nil || string(beforeOwned) != string(afterOwned) || string(beforeForeign) != string(afterForeign) {
		t.Fatalf("reference rejection mutated data: owned=%v foreign=%v", ownedErr, foreignErr)
	}
}

func TestClearProjectTaskDataStandaloneFailsClosedForForeignScope(t *testing.T) {
	st := newProjectClearStore(t)
	cfg, err := st.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProjectID = "project-one"
	if err := st.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	owned := createProjectClearTask(t, st, "project-one", "owned")
	_ = createProjectClearTask(t, st, "project-two", "foreign")
	_, err = st.ClearProjectTaskData(context.Background(), "human:owner", ClearProjectTaskDataOptions{ProjectID: "project-one", Standalone: true})
	if !errors.Is(err, ErrProjectTaskDataScope) {
		t.Fatalf("standalone foreign scope clear = %v, want ErrProjectTaskDataScope", err)
	}
	if _, err := st.Get(owned.Task.ID); err != nil {
		t.Fatalf("owned task changed after failed standalone clear: %v", err)
	}
}

func TestClearProjectTaskDataRollsBackQuarantineRenameFailure(t *testing.T) {
	st := newProjectClearStore(t)
	owned := createProjectClearTask(t, st, "project-one", "owned")
	trashed := createProjectClearTask(t, st, "project-one", "trash")
	if _, err := st.TrashTasks(context.Background(), "human:seed", []string{trashed.Task.ID}, "test", nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("simulated second collection publish failure")
	calls := 0
	st.renameFn = func(oldPath, newPath string) error {
		calls++
		if calls == 4 {
			return boom
		}
		return os.Rename(oldPath, newPath)
	}
	_, err := st.ClearProjectTaskData(context.Background(), "human:owner", ClearProjectTaskDataOptions{ProjectID: "project-one"})
	if !errors.Is(err, boom) || errors.Is(err, ErrProjectTaskDataRollback) {
		t.Fatalf("clear injected failure = %v", err)
	}
	st.renameFn = nil
	if _, err := st.Get(owned.Task.ID); err != nil {
		t.Fatalf("rollback lost active task: %v", err)
	}
	if _, err := st.GetTrash(trashed.Task.ID); err != nil {
		t.Fatalf("rollback lost trash task: %v", err)
	}
	stages, err := filepath.Glob(filepath.Join(st.Root(), ".carbon", projectClearStagingDir, "*"))
	if err != nil || len(stages) != 0 {
		t.Fatalf("rollback left project clear stage: %v %v", stages, err)
	}
}

func TestClearProjectTaskDataEmptyIsIdempotent(t *testing.T) {
	st := newProjectClearStore(t)
	first, err := st.ClearProjectTaskData(context.Background(), "human:owner", ClearProjectTaskDataOptions{ProjectID: "project-empty"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.ClearProjectTaskData(context.Background(), "human:owner", ClearProjectTaskDataOptions{ProjectID: "project-empty"})
	if err != nil {
		t.Fatal(err)
	}
	if first.TasksDeleted != 0 || second.TasksDeleted != 0 || first.ReceiptID == second.ReceiptID {
		t.Fatalf("empty clears = %+v / %+v", first, second)
	}
	if files, err := os.ReadDir(filepath.Join(st.Root(), ".carbon", projectClearReceiptsDir)); err != nil || len(files) != 2 {
		t.Fatalf("empty clear receipts = %v, %v", files, err)
	}
}

func TestProjectClearRunMatchingIsExactTaskPrefix(t *testing.T) {
	match := projectClearRunName(map[string]struct{}{"TASK-1": {}})
	for _, name := range []string{"TASK-1-20260809.log", "TASK-1-x.log"} {
		if !match(name) {
			t.Fatalf("expected run %q to match", name)
		}
	}
	for _, name := range []string{"TASK-10-20260809.log", "TASK-1.txt", "x-TASK-1-20260809.log"} {
		if match(name) {
			t.Fatalf("unexpected run match %q", name)
		}
	}
}

func TestClearProjectTaskDataKeepsLegacyForeignRunPublishedDuringClear(t *testing.T) {
	st := newProjectClearStore(t)
	owned := createProjectClearTask(t, st, "project-one", "owned")
	foreign := createProjectClearTask(t, st, "project-two", "foreign")
	runs := st.RunsDir()
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	ownedRun := owned.Task.ID + "-20260809-101010.000.log"
	foreignRun := foreign.Task.ID + "-20260809-101011.000.log"
	if err := os.WriteFile(filepath.Join(runs, ownedRun), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	injected := false
	st.renameFn = func(oldPath, newPath string) error {
		// buildProjectClearRuns has already snapshotted selected runs before the
		// first collection swap. This simulates an old sidecar publishing a
		// foreign run without Store.Write in that exact window.
		if !injected {
			injected = true
			if err := os.WriteFile(filepath.Join(runs, foreignRun), []byte("foreign"), 0o600); err != nil {
				return err
			}
		}
		return os.Rename(oldPath, newPath)
	}
	defer func() { st.renameFn = nil }()
	if _, err := st.ClearProjectTaskData(context.Background(), "human:owner", ClearProjectTaskDataOptions{ProjectID: "project-one"}); err != nil {
		t.Fatal(err)
	}
	if !injected {
		t.Fatal("test did not inject legacy foreign run")
	}
	if _, err := os.Stat(filepath.Join(runs, ownedRun)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selected run remains: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(runs, foreignRun)); err != nil || string(data) != "foreign" {
		t.Fatalf("legacy foreign run was lost or changed: %q %v", data, err)
	}
}

func TestProjectClearRecoveryRestoresPreparedTaskAndPartialRuns(t *testing.T) {
	st := newProjectClearStore(t)
	owned := createProjectClearTask(t, st, "project-one", "owned")
	runs := st.RunsDir()
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	runOne := owned.Task.ID + "-20260809-101010.000.log"
	runTwo := owned.Task.ID + "-20260809-101011.000.log"
	for _, name := range []string{runOne, runTwo} {
		if err := os.WriteFile(filepath.Join(runs, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stageID := "clear_prepared_recovery"
	var stage string
	if err := st.Write(context.Background(), "human:test", "make interrupted project clear", func(tx *WriteTx) error {
		var err error
		stage, err = st.newProjectClearStage(stageID)
		if err != nil {
			return err
		}
		taskPlan, err := st.buildProjectClearCollection(stage, "tasks", exactProjectClearNames(map[string]struct{}{owned.Task.ID: {}}, ".md"), 1)
		if err != nil {
			return err
		}
		runPlan, err := st.buildProjectClearRuns(stage, projectClearRunName(map[string]struct{}{owned.Task.ID: {}}))
		if err != nil {
			return err
		}
		if taskPlan == nil || runPlan == nil || len(runPlan.Files) != 2 {
			return errors.New("test setup did not build expected clear plans")
		}
		receipt := projectClearReceipt{
			Version: 1, ID: stageID, State: "switching", ProjectID: "project-one", Actor: "human:test",
			Plans: []projectClearPlan{{Kind: "tasks"}, {Kind: "runs", Files: projectClearFileNames(runPlan.Files)}},
		}
		if err := st.writeProjectClearStageReceipt(stage, receipt); err != nil {
			return err
		}
		if err := st.commitProjectClearCollections([]*projectClearCollection{taskPlan}); err != nil {
			return err
		}
		// Simulate a process death after only the first selected run reached
		// quarantine. Recovery must restore it and retain the un-moved second run.
		source, err := st.managedFile(runPlan.SourceDir, runPlan.Files[0].Name, true, true)
		if err != nil {
			return err
		}
		backup, err := st.managedFile(runPlan.BackupDir, runPlan.Files[0].Name, false, true)
		if err != nil {
			return err
		}
		_, err = st.moveProjectClearRunFile(source, backup)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(context.Background(), "human:test", "trigger project clear recovery", func(tx *WriteTx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(owned.Task.ID); err != nil {
		t.Fatalf("prepared recovery did not restore task: %v", err)
	}
	for _, name := range []string{runOne, runTwo} {
		if data, err := os.ReadFile(filepath.Join(runs, name)); err != nil || string(data) != name {
			t.Fatalf("prepared recovery did not restore run %s: %q %v", name, data, err)
		}
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared recovery retained staging directory: %v", err)
	}
}

func TestProjectClearRecoveryDropsDuplicateHardLinkAfterLinkBeforeSourceRemove(t *testing.T) {
	st := newProjectClearStore(t)
	owned := createProjectClearTask(t, st, "project-one", "owned")
	runs := st.RunsDir()
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	runName := owned.Task.ID + "-20260809-101010.000.log"
	if err := os.WriteFile(filepath.Join(runs, runName), []byte("run"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageID := "clear_link_crash_recovery"
	var stage string
	if err := st.Write(context.Background(), "human:test", "make link-before-remove crash", func(tx *WriteTx) error {
		var err error
		stage, err = st.newProjectClearStage(stageID)
		if err != nil {
			return err
		}
		runPlan, err := st.buildProjectClearRuns(stage, projectClearRunName(map[string]struct{}{owned.Task.ID: {}}))
		if err != nil {
			return err
		}
		if runPlan == nil || len(runPlan.Files) != 1 {
			return errors.New("test setup did not build expected run plan")
		}
		receipt := projectClearReceipt{Version: 1, ID: stageID, State: "switching", ProjectID: "project-one", Actor: "human:test", Plans: []projectClearPlan{{Kind: "runs", Files: projectClearFileNames(runPlan.Files)}}}
		if err := st.writeProjectClearStageReceipt(stage, receipt); err != nil {
			return err
		}
		source, err := st.managedFile(runPlan.SourceDir, runName, true, true)
		if err != nil {
			return err
		}
		backup, err := st.managedFile(runPlan.BackupDir, runName, false, true)
		if err != nil {
			return err
		}
		if err := os.Link(source, backup); err != nil {
			return err
		}
		return nil
	}); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skipf("hard links unavailable in test filesystem: %v", err)
		}
		t.Fatal(err)
	}
	if err := st.Write(context.Background(), "human:test", "recover linked stage", func(tx *WriteTx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(runs, runName)); err != nil || string(data) != "run" {
		t.Fatalf("source run was not retained after hard-link recovery: %q %v", data, err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hard-link recovery retained staging directory: %v", err)
	}
}

func TestProjectClearCommittedRecoveryOnlyPurgesQuarantine(t *testing.T) {
	st := newProjectClearStore(t)
	owned := createProjectClearTask(t, st, "project-one", "owned")
	stageID := "clear_committed_recovery"
	var stage string
	if err := st.Write(context.Background(), "human:test", "make committed project clear", func(tx *WriteTx) error {
		var err error
		stage, err = st.newProjectClearStage(stageID)
		if err != nil {
			return err
		}
		plan, err := st.buildProjectClearCollection(stage, "tasks", exactProjectClearNames(map[string]struct{}{owned.Task.ID: {}}, ".md"), 1)
		if err != nil {
			return err
		}
		if plan == nil {
			return errors.New("test setup did not build task plan")
		}
		prepared := projectClearReceipt{Version: 1, ID: stageID, State: "switching", ProjectID: "project-one", Actor: "human:test", Plans: []projectClearPlan{{Kind: "tasks"}}}
		if err := st.writeProjectClearStageReceipt(stage, prepared); err != nil {
			return err
		}
		if err := st.commitProjectClearCollections([]*projectClearCollection{plan}); err != nil {
			return err
		}
		committed := prepared
		committed.State = "committed"
		if err := st.writeProjectClearReceipt(committed); err != nil {
			return err
		}
		return st.writeProjectClearStageReceipt(stage, committed)
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(context.Background(), "human:test", "trigger committed project clear cleanup", func(tx *WriteTx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(owned.Task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("committed recovery restored cleared task: %v", err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed recovery retained staging directory: %v", err)
	}
}

func TestClearProjectTaskDataFailsClosedForSelectedRunSymlink(t *testing.T) {
	st := newProjectClearStore(t)
	owned := createProjectClearTask(t, st, "project-one", "owned")
	runs := st.RunsDir()
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(runs, owned.Task.ID+"-20260809-101010.000.log")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable in test environment: %v", err)
	}
	_, err := st.ClearProjectTaskData(context.Background(), "human:owner", ClearProjectTaskDataOptions{ProjectID: "project-one"})
	if !errors.Is(err, ErrProjectTaskDataChanged) {
		t.Fatalf("clear selected run symlink = %v, want ErrProjectTaskDataChanged", err)
	}
	if _, err := st.Get(owned.Task.ID); err != nil {
		t.Fatalf("task changed after selected run symlink rejection: %v", err)
	}
}
