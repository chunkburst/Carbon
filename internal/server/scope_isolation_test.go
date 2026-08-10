package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"carbon/internal/home"
	"carbon/internal/repo"
	"carbon/internal/session"
	"carbon/internal/store"
	"carbon/internal/task"
)

type projectScopeFixture struct {
	homeRoot string
	dataRoot string
	project1 home.Project
	project2 home.Project
	store    *store.Store
	server   *Server
	handler  http.Handler
}

func newProjectScopeFixture(t *testing.T) projectScopeFixture {
	t.Helper()
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	cluster, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Scope", Prefix: "SCP"})
	if err != nil {
		t.Fatal(err)
	}
	project1, err := home.AddProject(homeRoot, cluster.ID, home.AddProjectRequest{Name: "One", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	project2, err := home.AddProject(homeRoot, cluster.ID, home.AddProjectRequest{Name: "Two", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	dataRoot, err := home.ClusterDataRoot(homeRoot, cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	s := NewWithScope("human:test", ScopeDefaults{Home: homeRoot, ClusterID: cluster.ID, ProjectID: project1.ID, HomeByDefault: true})
	return projectScopeFixture{
		homeRoot: homeRoot, dataRoot: dataRoot, project1: project1, project2: project2,
		store: store.New(dataRoot), server: s, handler: s.Handler(),
	}
}

func (f projectScopeFixture) createTask(t *testing.T, projectID, title string) *store.Doc {
	t.Helper()
	doc, err := f.store.Create(store.Draft{Title: title, ProjectID: projectID, ProjectIDSet: true}, "human:test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestProjectScopeRunsRequireExplicitClusterRead(t *testing.T) {
	f := newProjectScopeFixture(t)
	foreign := f.createTask(t, f.project2.ID, "foreign run")
	if err := os.WriteFile(filepath.Join(f.store.RunsDir(), foreign.Task.ID+"-20260805-120000.000.log"), []byte("cmd: echo secret\n----\nsecret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, body := raw(f.handler, http.MethodGet, "/api/tasks/"+foreign.Task.ID+"/runs", ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("default foreign runs = %d %s, want 422", code, body)
	}
	if code, body := raw(f.handler, http.MethodGet, "/api/tasks/"+foreign.Task.ID+"/runs?include_cluster=true", ""); code != http.StatusOK {
		t.Fatalf("include_cluster foreign runs = %d %s, want 200", code, body)
	}
}

func TestCarbonProjectScopedRunChecksKeepSourceAndLogRootsSeparate(t *testing.T) {
	f := newProjectScopeFixture(t)
	doc, err := f.store.Create(store.Draft{
		Title:        "separate check log root",
		ProjectID:    f.project1.ID,
		ProjectIDSet: true,
		Checks:       []task.Check{{Desc: "pass", Cmd: "exit 0"}},
	}, "human:test", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	var checked taskDTO
	call(t, f.handler, http.MethodPost, "/api/tasks/"+doc.Task.ID+"/run_checks", `{}`, &checked)
	if len(checked.Checks) != 1 || checked.Checks[0].Result != "pass" {
		t.Fatalf("run checks response = %+v, want passing check", checked.Checks)
	}

	pattern := filepath.Join(f.dataRoot, repo.CarbonDirName, "runs", doc.Task.ID+"-*.log")
	logs, err := filepath.Glob(pattern)
	if err != nil || len(logs) != 1 {
		t.Fatalf("data-root run logs %q = %v, %v; want one", pattern, logs, err)
	}
	sourceRuns := filepath.Join(f.project1.Source.Path, repo.CarbonDirName, "runs")
	if sourceRuns == filepath.Join(f.dataRoot, repo.CarbonDirName, "runs") {
		t.Fatal("fixture source and data roots unexpectedly match")
	}
	if _, err := os.Stat(sourceRuns); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source checkout unexpectedly received a run-log directory: %v", err)
	}

	var runs runResp
	call(t, f.handler, http.MethodGet, "/api/tasks/"+doc.Task.ID+"/runs", "", &runs)
	if len(runs.Runs) != 1 || runs.Runs[0].File != filepath.Base(logs[0]) {
		t.Fatalf("run-log API = %+v, want data-root log %q", runs.Runs, filepath.Base(logs[0]))
	}
}

func TestProjectScopeSessionReadsRequireExplicitClusterRead(t *testing.T) {
	f := newProjectScopeFixture(t)
	foreign := f.createTask(t, f.project2.ID, "foreign session")
	created, err := f.store.CreateSession(context.Background(), "agent:two", session.Session{
		ID: "ses_foreign", TaskID: foreign.Task.ID, AttemptID: "att_foreign", Actor: "agent:two",
		Status: session.StatusActive, IdempotencyKey: "foreign-session", StartedAt: time.Now(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if code, body := raw(f.handler, http.MethodGet, "/api/sessions/"+created.Session.ID, ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("default foreign session = %d %s, want 422", code, body)
	}
	if code, body := raw(f.handler, http.MethodGet, "/api/sessions/"+created.Session.ID+"?include_cluster=true", ""); code != http.StatusOK {
		t.Fatalf("include_cluster foreign session = %d %s, want 200", code, body)
	}
	var defaultList struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	call(t, f.handler, http.MethodGet, "/api/sessions", "", &defaultList)
	if len(defaultList.Sessions) != 0 {
		t.Fatalf("default foreign sessions = %d, want 0", len(defaultList.Sessions))
	}
	var clusterList struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	call(t, f.handler, http.MethodGet, "/api/sessions?include_cluster=true", "", &clusterList)
	if len(clusterList.Sessions) != 1 {
		t.Fatalf("include_cluster foreign sessions = %d, want 1", len(clusterList.Sessions))
	}
}

func TestBeginSessionRejectsForeignTaskBeforeCreatingSessionArtifacts(t *testing.T) {
	f := newProjectScopeFixture(t)
	foreign := f.createTask(t, f.project2.ID, "foreign begin")
	for _, name := range []string{"sessions", "live"} {
		path := filepath.Join(f.dataRoot, repo.CarbonDirName, name)
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}

	code, body := raw(f.handler, http.MethodPost, "/api/tasks/"+foreign.Task.ID+"/sessions/begin", `{"idempotencyKey":"rejected"}`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("foreign begin = %d %s, want 422", code, body)
	}
	for _, name := range []string{"sessions", "live"} {
		if _, err := os.Stat(filepath.Join(f.dataRoot, repo.CarbonDirName, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rejected foreign begin created %s: %v", name, err)
		}
	}
}

func TestBulkMoveRequiresExplicitClusterWideTargetOverHTTP(t *testing.T) {
	f := newProjectScopeFixture(t)
	owned := f.createTask(t, f.project1.ID, "owned move")
	for _, request := range []map[string]any{
		{
			"ids":              []string{owned.Task.ID},
			"expectedVersions": map[string]string{owned.Task.ID: owned.Version()},
		},
		{
			"ids":              []string{owned.Task.ID},
			"expectedVersions": map[string]string{owned.Task.ID: owned.Version()},
			"projectId":        f.project2.ID,
			"clusterWide":      true,
		},
	} {
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		if code, response := raw(f.handler, http.MethodPost, "/api/tasks/bulk/move", string(body)); code != http.StatusUnprocessableEntity {
			t.Fatalf("ambiguous bulk move = %d %s, want 422", code, response)
		}
	}
}

func TestCarbonCreateRejectsDirectAssigneeBeforeTaskMutation(t *testing.T) {
	f := newProjectScopeFixture(t)
	if code, body := raw(f.handler, http.MethodPost, "/api/tasks", `{"title":"no direct assignment","assignee":"human:other"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("Carbon direct assignee create = %d %s, want 422", code, body)
	}
	tasks, err := f.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("rejected Carbon assignee create wrote tasks: %#v", tasks)
	}
}

func TestHomeManagementRejectsSelectedClusterProjectScopes(t *testing.T) {
	f := newProjectScopeFixture(t)
	if code, body := raw(f.handler, http.MethodGet, "/api/home", ""); code != http.StatusBadRequest {
		t.Fatalf("default selected scope home read = %d %s, want 400", code, body)
	}
	if code, body := raw(f.handler, http.MethodPost, "/api/home/migrations/legacy/apply", `{"legacyCluster":"ignored","expectedDigest":"ignored"}`); code != http.StatusBadRequest {
		t.Fatalf("default selected scope migration apply = %d %s, want 400", code, body)
	}
	homeOnly := NewWithScope("human:test", ScopeDefaults{Home: f.homeRoot, HomeByDefault: true})
	if code, body := raw(homeOnly.Handler(), http.MethodGet, "/api/home", ""); code != http.StatusOK {
		t.Fatalf("home-only home read = %d %s, want 200", code, body)
	}
	if code, body := raw(homeOnly.Handler(), http.MethodGet, "/api/home?cluster="+url.QueryEscape("cluster-selected"), ""); code != http.StatusBadRequest {
		t.Fatalf("query selected scope home read = %d %s, want 400", code, body)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/home/doctor", nil)
	req.Header.Set("X-Carbon-Project", f.project1.ID)
	resp := httptest.NewRecorder()
	homeOnly.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("header selected scope doctor = %d %s, want 400", resp.Code, resp.Body.String())
	}
}
