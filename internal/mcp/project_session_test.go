package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"carbon/internal/compat"
	"carbon/internal/home"
	"carbon/internal/store"
	"carbon/internal/worklog"
)

func TestProjectSessionStaticCatalogActivationAndSwitching(t *testing.T) {
	main := t.TempDir()
	firstSource := t.TempDir()
	secondSource := t.TempDir()
	binding, err := NewProjectSession(store.New(main), "agent:session", "codex", main, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewProjectSessionServer(binding)
	ctx, client := connectProjectSessionServer(t, srv)

	names := listedProjectSessionToolNames(t, client, ctx)
	for _, name := range []string{
		"identity", "list_projects", "create_project", "select_project",
		"list", "create", "begin", "worklog_create", "worker_stats",
	} {
		if !names[name] {
			t.Errorf("Project Session tool catalog omitted %q: %v", name, names)
		}
	}

	var before Identity
	callProjectSessionTool(t, client, ctx, "identity", nil, &before)
	if before.BindingMode != "session" || before.SelectionVersion != 0 || before.Scope.Mode != "carbon_home" || before.Scope.ProjectID != "" {
		t.Fatalf("unselected identity = %#v", before)
	}
	if _, err := binding.ActiveService(); !errors.Is(err, ErrActiveProjectRequired) {
		t.Fatalf("unselected ActiveService = %v, want ErrActiveProjectRequired", err)
	}

	// Every registered tool outside the small Home-catalog allowlist must fail
	// before schema decoding or Service dispatch. This catches a future task
	// tool that is added to NewProjectSessionServer without a pre-selection
	// guard and would otherwise be able to fall through to Store.New(home).
	for name := range names {
		if projectSessionPreselectionTool(name) {
			continue
		}
		assertProjectSessionRequiresActive(t, client, ctx, name)
	}
	assertProjectSessionRequiresActive(t, client, ctx, "future_task_tool")
	if _, err := home.Open(main); err == nil {
		t.Fatal("unselected task tool unexpectedly initialized a Home task store")
	}

	first := createProjectThroughSession(t, client, ctx, "First", firstSource)
	var selected Identity
	callProjectSessionTool(t, client, ctx, "identity", nil, &selected)
	if selected.BindingMode != "session" || selected.SelectionVersion != 1 || selected.Scope.ProjectID != first.Project.CanonicalID || !selected.Scope.Standalone {
		t.Fatalf("identity after automatic create selection = %#v", selected)
	}
	firstService, err := binding.ActiveService()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := firstService.store.Root(), mustProjectDataRoot(t, main, first.Project.CanonicalID); got != want {
		t.Fatalf("first Store root = %q, want %q", got, want)
	}

	var firstTask taskOut
	callProjectSessionTool(t, client, ctx, "create", map[string]any{
		"title": "first task", "type": "patch", "importance": "normal",
	}, &firstTask)
	if firstTask.ProjectID != first.Project.CanonicalID {
		t.Fatalf("first task project = %q, want %q", firstTask.ProjectID, first.Project.CanonicalID)
	}
	var firstLog workLogOut
	callProjectSessionTool(t, client, ctx, "worklog_create", map[string]any{
		"visibility": "project_public", "title": "first work log",
	}, &firstLog)
	if firstLog.WorkLog.ProjectID != first.Project.CanonicalID {
		t.Fatalf("first Work Log project = %q, want %q", firstLog.WorkLog.ProjectID, first.Project.CanonicalID)
	}

	second := createProjectThroughSession(t, client, ctx, "Second", secondSource)
	secondService, err := binding.ActiveService()
	if err != nil {
		t.Fatal(err)
	}
	if secondService == firstService || secondService.Scope().ProjectID != second.Project.CanonicalID {
		t.Fatalf("create_project did not atomically replace immutable Service: first=%p second=%p scope=%#v", firstService, secondService, secondService.Scope())
	}
	if got, want := secondService.store.Root(), mustProjectDataRoot(t, main, second.Project.CanonicalID); got != want {
		t.Fatalf("second Store root = %q, want %q", got, want)
	}
	var secondLog workLogOut
	callProjectSessionTool(t, client, ctx, "worklog_create", map[string]any{
		"visibility": "project_public", "title": "second work log",
	}, &secondLog)
	if secondLog.WorkLog.ProjectID != second.Project.CanonicalID {
		t.Fatalf("dynamic Work Log accessor stayed on old project: %#v", secondLog.WorkLog)
	}

	var switched ProjectSessionSelection
	callProjectSessionTool(t, client, ctx, "select_project", map[string]any{"project": first.Project.CanonicalID}, &switched)
	if switched.BindingMode != "session" || switched.SelectionVersion != 3 || switched.Scope.ProjectID != first.Project.CanonicalID {
		t.Fatalf("select_project result = %#v", switched)
	}
	var listed listOut
	callProjectSessionTool(t, client, ctx, "list", map[string]any{}, &listed)
	if len(listed.Tasks) != 1 || listed.Tasks[0].ID != firstTask.ID || listed.Tasks[0].ProjectID != first.Project.CanonicalID {
		t.Fatalf("switched list = %#v", listed)
	}
}

func TestProjectSessionDoesNotAddSelectToFixedConnection(t *testing.T) {
	main := t.TempDir()
	svc := NewScopedService(store.New(main), "agent:fixed", Scope{Home: main}, nil)
	srv := NewServer(svc)
	ctx, client := connectProjectSessionServer(t, srv)
	if listedProjectSessionToolNames(t, client, ctx)["select_project"] {
		t.Fatal("fixed NewServer unexpectedly registered select_project")
	}
}

func TestFixedProjectConnectionCreateProjectKeepsBinding(t *testing.T) {
	main := t.TempDir()
	firstSource := t.TempDir()
	catalog := NewScopedService(store.New(main), "agent:fixed", Scope{Home: main, CompatLayer: compat.StableLayer}, nil)
	first, err := catalog.CreateCatalogProject(CatalogCreateProjectInput{
		Name: "Pinned", SourcePath: firstSource, AllowCreate: true, Reason: "test pinned project",
	})
	if err != nil {
		t.Fatal(err)
	}
	root := mustProjectDataRoot(t, main, first.Project.CanonicalID)
	fixed := NewScopedServiceWithClientAndResolver(store.New(root), "agent:fixed", "codex", Scope{
		Home: main, ProjectID: first.Project.CanonicalID, SourcePath: firstSource,
		Standalone: true, CompatLayer: compat.StableLayer,
	}, nil, nil)
	srv := NewServer(fixed)
	ctx, client := connectProjectSessionServer(t, srv)

	var before Identity
	callProjectSessionTool(t, client, ctx, "identity", nil, &before)
	var created ProjectDescription
	callProjectSessionTool(t, client, ctx, "create_project", map[string]any{
		"name": "New project", "source_path": t.TempDir(), "allow_create": true, "reason": "catalog test",
	}, &created)
	if created.Project.CanonicalID == "" || !created.Standalone {
		t.Fatalf("fixed create_project = %#v", created)
	}
	var after Identity
	callProjectSessionTool(t, client, ctx, "identity", nil, &after)
	if before.BindingMode != "" || after.BindingMode != "" || before.Scope.ProjectID != first.Project.CanonicalID || after.Scope.ProjectID != first.Project.CanonicalID || after.Scope.SourcePath != firstSource {
		t.Fatalf("fixed create_project changed identity binding: before=%#v after=%#v", before, after)
	}
	if fixed.store.Root() != root {
		t.Fatalf("fixed create_project changed Store root = %q, want %q", fixed.store.Root(), root)
	}
}

func TestProjectSessionSelectsClusterProjectAndKeepsOldBindingOnFailure(t *testing.T) {
	main := t.TempDir()
	binding, err := NewProjectSession(store.New(main), "agent:session", "codex", main, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewProjectSessionServer(binding)
	ctx, client := connectProjectSessionServer(t, srv)

	var cluster clusterOut
	callProjectSessionTool(t, client, ctx, "create_cluster", map[string]any{
		"name": "Shared", "slug": "shared", "allow_create": true, "reason": "test shared pool",
	}, &cluster)
	var nested ProjectDescription
	callProjectSessionTool(t, client, ctx, "create_project", map[string]any{
		"cluster": cluster.Cluster.CanonicalID, "name": "Desktop", "slug": "desktop",
		"source_path": t.TempDir(), "allow_create": true, "reason": "test shared project",
	}, &nested)
	if nested.Standalone || nested.Project.ClusterID != cluster.Cluster.CanonicalID {
		t.Fatalf("clustered create_project = %#v", nested)
	}
	current, err := binding.ActiveService()
	if err != nil {
		t.Fatal(err)
	}
	if current.Scope().ClusterID != cluster.Cluster.CanonicalID || current.Scope().Standalone {
		t.Fatalf("automatic clustered binding = %#v", current.Scope())
	}
	version := binding.Selection().SelectionVersion
	if _, err := binding.SelectProject(cluster.Cluster.CanonicalID, "missing"); err == nil {
		t.Fatal("select missing project unexpectedly succeeded")
	}
	afterFailure, err := binding.ActiveService()
	if err != nil || afterFailure != current || binding.Selection().SelectionVersion != version {
		t.Fatalf("failed select changed binding: service=%p want=%p version=%d want=%d err=%v", afterFailure, current, binding.Selection().SelectionVersion, version, err)
	}

	var selected ProjectSessionSelection
	callProjectSessionTool(t, client, ctx, "select_project", map[string]any{
		"cluster": "SHARED", "project": "DESKTOP",
	}, &selected)
	if selected.Scope.ClusterID != cluster.Cluster.CanonicalID || selected.Scope.ProjectID != nested.Project.CanonicalID || selected.Scope.Standalone {
		t.Fatalf("select cluster project = %#v", selected)
	}
}

func TestProjectSessionRejectsMisboundStandaloneDataRoot(t *testing.T) {
	main := t.TempDir()
	binding, err := NewProjectSession(store.New(main), "agent:session", "codex", main, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := binding.CreateProject(CatalogCreateProjectInput{Name: "First", SourcePath: t.TempDir(), AllowCreate: true, Reason: "test first"})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := binding.CreateProject(CatalogCreateProjectInput{Name: "Second", SourcePath: t.TempDir(), AllowCreate: true, Reason: "test second"})
	if err != nil {
		t.Fatal(err)
	}
	firstRoot := mustProjectDataRoot(t, main, first.Project.CanonicalID)
	firstStore := store.New(firstRoot)
	cfg, err := firstStore.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProjectID = second.Project.CanonicalID
	if err := firstStore.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	before, err := binding.ActiveService()
	if err != nil {
		t.Fatal(err)
	}
	version := binding.Selection().SelectionVersion
	if _, err := binding.SelectProject("", first.Project.CanonicalID); err == nil || !strings.Contains(err.Error(), "data root is bound") {
		t.Fatalf("misbound standalone select = %v, want data-root binding failure", err)
	}
	after, err := binding.ActiveService()
	if err != nil || after != before || binding.Selection().SelectionVersion != version || after.Scope().ProjectID != second.Project.CanonicalID {
		t.Fatalf("misbound selection replaced active service: after=%p before=%p selection=%#v err=%v", after, before, binding.Selection(), err)
	}
}

func TestProjectSessionSerializesConcurrentToolCalls(t *testing.T) {
	main := t.TempDir()
	binding, err := NewProjectSession(store.New(main), "agent:session", "codex", main, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Create through the direct API so this test can exercise concurrent MCP
	// select_project/tool calls without an initial catalog write in the workers.
	first, _, err := binding.CreateProject(CatalogCreateProjectInput{Name: "First", SourcePath: t.TempDir(), AllowCreate: true, Reason: "test first"})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := binding.CreateProject(CatalogCreateProjectInput{Name: "Second", SourcePath: t.TempDir(), AllowCreate: true, Reason: "test second"})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewProjectSessionServer(binding)
	ctx, client := connectProjectSessionServer(t, srv)

	projects := []string{first.Project.CanonicalID, second.Project.CanonicalID}
	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for index := 0; index < workers; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			projectID := projects[index%len(projects)]
			if result, err := client.CallTool(ctx, &mcpsdk.CallToolParams{Name: "select_project", Arguments: map[string]any{"project": projectID}}); err != nil || result.IsError {
				errs <- toolCallFailure("select_project", result, err)
				return
			}
			// The session selection is intentionally global to this one MCP server;
			// another concurrent caller may choose either project between these two
			// requests. What must never happen is a Home-root write or a malformed
			// binding, so every task is checked after the concurrent burst below.
			result, err := client.CallTool(ctx, &mcpsdk.CallToolParams{Name: "create", Arguments: map[string]any{
				"title": "concurrent task", "type": "patch", "importance": "normal",
			}})
			if err != nil || result.IsError {
				errs <- toolCallFailure("create", result, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	for _, projectID := range projects {
		root := mustProjectDataRoot(t, main, projectID)
		docs, err := store.New(root).ListDocs()
		if err != nil {
			t.Fatalf("list %s: %v", projectID, err)
		}
		for _, doc := range docs {
			if doc.Task.ProjectID != projectID {
				t.Fatalf("task %s in %s has project %q", doc.Task.ID, projectID, doc.Task.ProjectID)
			}
		}
	}
}

// TestProjectSessionSerializesMixedProjectBoundToolCalls keeps selection calls racing
// with every project-bound write shape that carries durable state. Each lifecycle
// retry intentionally selects its target first: another peer may switch the shared
// session before the next request, in which case the operation must fail harmlessly
// and retry rather than write into a Home catalog or the other project.
func TestProjectSessionSerializesMixedProjectBoundToolCalls(t *testing.T) {
	main := t.TempDir()
	binding, err := NewProjectSession(store.New(main), "agent:session", "codex", main, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := binding.CreateProject(CatalogCreateProjectInput{Name: "First", SourcePath: t.TempDir(), AllowCreate: true, Reason: "test first"})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := binding.CreateProject(CatalogCreateProjectInput{Name: "Second", SourcePath: t.TempDir(), AllowCreate: true, Reason: "test second"})
	if err != nil {
		t.Fatal(err)
	}
	projects := []string{first.Project.CanonicalID, second.Project.CanonicalID}

	srv := NewProjectSessionServer(binding)
	ctx, client := connectProjectSessionServer(t, srv)

	// Seed unique tasks in each root before the concurrent phase. Unique IDs make a
	// race-induced switch observable: an operation can only succeed in its intended
	// root, never against a matching task in the other project.
	const lifecycleTasksPerProject = 2
	tasks := make([]projectSessionRaceTask, 0, len(projects)*lifecycleTasksPerProject)
	for _, projectID := range projects {
		callProjectSessionTool(t, client, ctx, "select_project", map[string]any{"project": projectID}, nil)
		for index := 0; index < lifecycleTasksPerProject; index++ {
			var created taskOut
			callProjectSessionTool(t, client, ctx, "create", map[string]any{
				"title": fmt.Sprintf("lifecycle %s %d", projectID, index),
				"type":  "patch", "importance": "normal",
				"checks": []map[string]any{{"desc": "pass", "cmd": "exit 0"}},
			}, &created)
			if created.ProjectID != projectID {
				t.Fatalf("seeded task project = %q, want %q", created.ProjectID, projectID)
			}
			tasks = append(tasks, projectSessionRaceTask{projectID: projectID, taskID: created.ID})
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	const workLogWriters = 12
	const selectorWorkers = 8
	const switchesPerSelector = 4

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, len(tasks)+workLogWriters+selectorWorkers)
	logs := make(chan projectSessionRaceLog, workLogWriters)
	sessions := make(chan projectSessionRaceSession, len(tasks))

	for worker := 0; worker < selectorWorkers; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for index := 0; index < switchesPerSelector; index++ {
				projectID := projects[(worker+index)%len(projects)]
				result, err := client.CallTool(callCtx, &mcpsdk.CallToolParams{Name: "select_project", Arguments: map[string]any{"project": projectID}})
				if err != nil || result.IsError {
					errs <- toolCallFailure("select_project", result, err)
					return
				}
			}
		}()
	}

	for worker := 0; worker < workLogWriters; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			projectID := projects[worker%len(projects)]
			var created workLogOut
			if err := retryProjectSessionBoundTool(callCtx, client, projectID, "worklog_create", map[string]any{
				"visibility": "project_public", "project_id": projectID,
				"title": fmt.Sprintf("concurrent work log %d", worker),
			}, &created); err != nil {
				errs <- err
				return
			}
			if created.WorkLog.ProjectID != projectID || !created.WorkLog.Standalone || created.WorkLog.ClusterID != "" {
				errs <- fmt.Errorf("worklog_create binding = %#v, want standalone project %s", created.WorkLog, projectID)
				return
			}
			logs <- projectSessionRaceLog{projectID: projectID, id: created.WorkLog.ID}
		}()
	}

	for index, seeded := range tasks {
		index, seeded := index, seeded
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			sessionID, err := runProjectSessionRaceLifecycle(callCtx, client, seeded, index)
			if err != nil {
				errs <- err
				return
			}
			sessions <- projectSessionRaceSession{projectID: seeded.projectID, taskID: seeded.taskID, id: sessionID}
		}()
	}

	close(start)
	wg.Wait()
	close(errs)
	close(logs)
	close(sessions)
	failed := false
	for err := range errs {
		if err != nil {
			failed = true
			t.Error(err)
		}
	}
	if failed {
		return
	}

	expectedLogs := make([]projectSessionRaceLog, 0, workLogWriters)
	for log := range logs {
		expectedLogs = append(expectedLogs, log)
	}
	if len(expectedLogs) != workLogWriters {
		t.Fatalf("successful work logs = %d, want %d", len(expectedLogs), workLogWriters)
	}
	expectedSessions := make([]projectSessionRaceSession, 0, len(tasks))
	for view := range sessions {
		expectedSessions = append(expectedSessions, view)
	}
	if len(expectedSessions) != len(tasks) {
		t.Fatalf("successful sessions = %d, want %d", len(expectedSessions), len(tasks))
	}
	assertProjectSessionRaceRoots(t, main, projects, tasks, expectedSessions, expectedLogs)
}

type projectSessionRaceTask struct {
	projectID string
	taskID    string
}

type projectSessionRaceSession struct {
	projectID string
	taskID    string
	id        string
}

type projectSessionRaceLog struct {
	projectID string
	id        string
}

func runProjectSessionRaceLifecycle(ctx context.Context, client *mcpsdk.ClientSession, seeded projectSessionRaceTask, index int) (string, error) {
	key := fmt.Sprintf("project-session-race-%d", index)
	var begun SessionView
	if err := retryProjectSessionBoundTool(ctx, client, seeded.projectID, "begin", map[string]any{
		"id": seeded.taskID, "expected_actor": "agent:session", "client": "codex", "idempotency_key": key,
	}, &begun); err != nil {
		return "", err
	}
	if begun.ID == "" || begun.TaskID != seeded.taskID || begun.Status != "active" {
		return "", fmt.Errorf("begin for %s = %#v", seeded.taskID, begun)
	}

	var heartbeat SessionView
	if err := retryProjectSessionBoundTool(ctx, client, seeded.projectID, "heartbeat", map[string]any{
		"session": begun.ID, "progress": "concurrent verification",
	}, &heartbeat); err != nil {
		return "", err
	}
	if heartbeat.ID != begun.ID || heartbeat.TaskID != seeded.taskID || heartbeat.Live == nil || heartbeat.Live.Progress != "concurrent verification" {
		return "", fmt.Errorf("heartbeat for %s = %#v", begun.ID, heartbeat)
	}

	var checked taskOut
	if err := retryProjectSessionBoundTool(ctx, client, seeded.projectID, "run_checks", map[string]any{"id": seeded.taskID}, &checked); err != nil {
		return "", err
	}
	if checked.ProjectID != seeded.projectID || len(checked.Checks) != 1 || checked.Checks[0].Result != "pass" {
		return "", fmt.Errorf("run_checks for %s = %#v", seeded.taskID, checked)
	}

	var finished SessionView
	if err := retryProjectSessionBoundTool(ctx, client, seeded.projectID, "finish", map[string]any{
		"session": begun.ID, "summary": "concurrent verification complete",
	}, &finished); err != nil {
		return "", err
	}
	if finished.ID != begun.ID || finished.TaskID != seeded.taskID || finished.Status != "finished" {
		return "", fmt.Errorf("finish for %s = %#v", begun.ID, finished)
	}
	return begun.ID, nil
}

// retryProjectSessionBoundTool makes an intended project selection and then performs
// exactly one tool call. A concurrent peer can legally select another project between
// those requests; project-scoped operations must reject that mismatch without writing,
// so the helper retries until its call reaches the intended immutable Service.
func retryProjectSessionBoundTool(ctx context.Context, client *mcpsdk.ClientSession, projectID, name string, args map[string]any, out any) error {
	var last string
	for attempt := 0; attempt < 96; attempt++ {
		selected, err := client.CallTool(ctx, &mcpsdk.CallToolParams{Name: "select_project", Arguments: map[string]any{"project": projectID}})
		if err != nil {
			return fmt.Errorf("select_project for %s: %w", projectID, err)
		}
		if selected.IsError {
			return fmt.Errorf("select_project for %s: %s", projectID, toolResultText(selected))
		}
		result, err := client.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			return fmt.Errorf("%s for %s: %w", name, projectID, err)
		}
		if result.IsError {
			last = toolResultText(result)
			continue
		}
		if out == nil {
			return nil
		}
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return fmt.Errorf("encode %s result: %w", name, err)
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode %s result: %w", name, err)
		}
		return nil
	}
	return fmt.Errorf("%s for project %s did not reach its selected binding after retries: %s", name, projectID, last)
}

func assertProjectSessionRaceRoots(t *testing.T, main string, projects []string, expectedTasks []projectSessionRaceTask, expectedSessions []projectSessionRaceSession, expectedLogs []projectSessionRaceLog) {
	t.Helper()
	tasksByID := make(map[string]projectSessionRaceTask, len(expectedTasks))
	for _, expected := range expectedTasks {
		if _, exists := tasksByID[expected.taskID]; exists {
			t.Fatalf("duplicate seeded task %s", expected.taskID)
		}
		tasksByID[expected.taskID] = expected
	}
	sessionsByID := make(map[string]projectSessionRaceSession, len(expectedSessions))
	for _, expected := range expectedSessions {
		if _, exists := sessionsByID[expected.id]; exists {
			t.Fatalf("duplicate session %s", expected.id)
		}
		sessionsByID[expected.id] = expected
	}
	logsByID := make(map[string]projectSessionRaceLog, len(expectedLogs))
	for _, expected := range expectedLogs {
		if _, exists := logsByID[expected.id]; exists {
			t.Fatalf("duplicate work log %s", expected.id)
		}
		logsByID[expected.id] = expected
	}

	seenTasks := make(map[string]bool, len(expectedTasks))
	seenSessions := make(map[string]bool, len(expectedSessions))
	seenLogs := make(map[string]bool, len(expectedLogs))
	for _, projectID := range projects {
		root := mustProjectDataRoot(t, main, projectID)
		data := store.New(root)
		docs, err := data.ListDocs()
		if err != nil {
			t.Fatalf("list tasks in %s: %v", projectID, err)
		}
		for _, doc := range docs {
			expected, ok := tasksByID[doc.Task.ID]
			if !ok || expected.projectID != projectID || doc.Task.ProjectID != projectID {
				t.Fatalf("task %s persisted in %s with project %q; expected=%#v", doc.Task.ID, projectID, doc.Task.ProjectID, expected)
			}
			seenTasks[doc.Task.ID] = true
		}

		docsByID := make(map[string]*store.Doc, len(docs))
		for _, doc := range docs {
			docsByID[doc.Task.ID] = doc
		}
		sessionDocs, err := data.ListSessions()
		if err != nil {
			t.Fatalf("list sessions in %s: %v", projectID, err)
		}
		for _, doc := range sessionDocs {
			expected, ok := sessionsByID[doc.Session.ID]
			taskDoc := docsByID[doc.Session.TaskID]
			if !ok || expected.projectID != projectID || expected.taskID != doc.Session.TaskID || taskDoc == nil || taskDoc.Task.ProjectID != projectID {
				t.Fatalf("session %#v persisted in %s; expected=%#v task=%#v", doc.Session, projectID, expected, taskDoc)
			}
			seenSessions[doc.Session.ID] = true
		}

		items, err := worklog.New(data, nil).List(worklog.Filter{Limit: worklog.MaxListLimit})
		if err != nil {
			t.Fatalf("list work logs in %s: %v", projectID, err)
		}
		for _, item := range items {
			expected, ok := logsByID[item.ID]
			if !ok || expected.projectID != projectID || item.ProjectID != projectID || !item.Standalone || item.ClusterID != "" {
				t.Fatalf("work log %#v persisted in %s; expected=%#v", item, projectID, expected)
			}
			seenLogs[item.ID] = true
		}
	}
	if len(seenTasks) != len(expectedTasks) || len(seenSessions) != len(expectedSessions) || len(seenLogs) != len(expectedLogs) {
		t.Fatalf("persisted project artifacts tasks=%d/%d sessions=%d/%d logs=%d/%d", len(seenTasks), len(expectedTasks), len(seenSessions), len(expectedSessions), len(seenLogs), len(expectedLogs))
	}
	assertNoProjectSessionArtifactsAtHome(t, main)
}

func assertNoProjectSessionArtifactsAtHome(t *testing.T, homeRoot string) {
	t.Helper()
	for _, name := range []string{"tasks", "sessions", "live", "runs", "worklogs"} {
		entries, err := os.ReadDir(filepath.Join(homeRoot, ".carbon", name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("read Home %s: %v", name, err)
		}
		if len(entries) > 0 {
			t.Fatalf("Project Session wrote %d %s artifact(s) into Home root", len(entries), name)
		}
	}
}

func createProjectThroughSession(t *testing.T, client *mcpsdk.ClientSession, ctx context.Context, name, source string) ProjectDescription {
	t.Helper()
	var out ProjectDescription
	callProjectSessionTool(t, client, ctx, "create_project", map[string]any{
		"name": name, "source_path": source, "allow_create": true, "reason": "session test project",
	}, &out)
	if out.Project.CanonicalID == "" || !out.Standalone {
		t.Fatalf("create_project %s = %#v", name, out)
	}
	return out
}

func connectProjectSessionServer(t *testing.T, srv *mcpsdk.Server) (context.Context, *mcpsdk.ClientSession) {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "project-session-test", Version: "0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return ctx, clientSession
}

func listedProjectSessionToolNames(t *testing.T, client *mcpsdk.ClientSession, ctx context.Context) map[string]bool {
	t.Helper()
	result, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(result.Tools))
	for _, tool := range result.Tools {
		names[tool.Name] = true
	}
	return names
}

func callProjectSessionTool(t *testing.T, client *mcpsdk.ClientSession, ctx context.Context, name string, args map[string]any, out any) {
	t.Helper()
	result, err := client.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s transport: %v", name, err)
	}
	if result.IsError {
		t.Fatalf("%s result error: %s", name, toolResultText(result))
	}
	if out == nil {
		return
	}
	data, _ := json.Marshal(result.StructuredContent)
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

func toolResultText(result *mcpsdk.CallToolResult) string {
	if result == nil {
		return "<nil result>"
	}
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcpsdk.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func toolCallFailure(name string, result *mcpsdk.CallToolResult, err error) error {
	if err != nil {
		return &projectSessionToolError{name: name, detail: err.Error()}
	}
	return &projectSessionToolError{name: name, detail: toolResultText(result)}
}

func assertProjectSessionRequiresActive(t *testing.T, client *mcpsdk.ClientSession, ctx context.Context, name string) {
	t.Helper()
	result, err := client.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("unselected %s transport error: %v", name, err)
	}
	if !result.IsError || !strings.Contains(toolResultText(result), ErrActiveProjectRequired.Error()) {
		t.Fatalf("unselected %s = %+v, want ErrActiveProjectRequired", name, result)
	}
}

type projectSessionToolError struct {
	name   string
	detail string
}

func (err *projectSessionToolError) Error() string {
	return err.name + ": " + err.detail
}

func mustProjectDataRoot(t *testing.T, main, projectID string) string {
	t.Helper()
	root, err := home.ProjectDataRoot(main, projectID)
	if err != nil {
		t.Fatal(err)
	}
	return root
}
