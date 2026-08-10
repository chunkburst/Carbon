package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"carbon/internal/home"
	"carbon/internal/store"
)

type projectClearHTTPFixture struct {
	homeRoot        string
	grouped         home.Project
	sibling         home.Project
	standalone      home.Project
	groupedStore    *store.Store
	standaloneStore *store.Store
	handler         http.Handler
}

func newProjectClearHTTPFixture(t *testing.T) projectClearHTTPFixture {
	t.Helper()
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	cluster, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Clear cluster", Prefix: "CLR"})
	if err != nil {
		t.Fatal(err)
	}
	grouped, err := home.AddProject(homeRoot, cluster.ID, home.AddProjectRequest{Name: "东京 Team Alpha", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := home.AddProject(homeRoot, cluster.ID, home.AddProjectRequest{Name: "Peer", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	standalone, err := home.AddStandaloneProject(homeRoot, home.AddProjectRequest{Name: "Private Omega", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	groupedRoot, err := home.ClusterDataRoot(homeRoot, cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	standaloneRoot, err := home.ProjectDataRoot(homeRoot, standalone.ID)
	if err != nil {
		t.Fatal(err)
	}
	service := NewWithScope("human:web", ScopeDefaults{Home: homeRoot, HomeByDefault: true})
	return projectClearHTTPFixture{
		homeRoot: homeRoot, grouped: grouped, sibling: sibling, standalone: standalone,
		groupedStore: store.New(groupedRoot), standaloneStore: store.New(standaloneRoot), handler: service.Handler(),
	}
}

func (f projectClearHTTPFixture) createTask(t *testing.T, st *store.Store, projectID, title string) *store.Doc {
	t.Helper()
	doc, err := st.Create(store.Draft{Title: title, ProjectID: projectID, ProjectIDSet: true}, "human:test", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func (f projectClearHTTPFixture) path(projectID string) string {
	query := url.Values{"home": {f.homeRoot}}
	return "/api/home/projects/" + url.PathEscape(projectID) + "/clear-task-data?" + query.Encode()
}

func projectClearRaw(handler http.Handler, path, body, actor string) (int, string) {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if actor != "" {
		request.Header.Set("X-Carbon-Actor", actor)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response.Code, response.Body.String()
}

func TestClearHomeProjectTaskDataHTTPGroupedAndStandalone(t *testing.T) {
	t.Run("grouped stable id clears only selected project", func(t *testing.T) {
		f := newProjectClearHTTPFixture(t)
		owned := f.createTask(t, f.groupedStore, f.grouped.ID, "owned")
		foreign := f.createTask(t, f.groupedStore, f.sibling.ID, "foreign")
		code, body := projectClearRaw(f.handler, f.path(f.grouped.ID), `{"name":"东京 Team Alpha"}`, "")
		if code != http.StatusOK {
			t.Fatalf("grouped clear = %d %s", code, body)
		}
		if _, err := f.groupedStore.Get(owned.Task.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("selected grouped task remains: %v", err)
		}
		if _, err := f.groupedStore.Get(foreign.Task.ID); err != nil {
			t.Fatalf("sibling grouped task was cleared: %v", err)
		}
	})

	t.Run("standalone stable id resolves its private store", func(t *testing.T) {
		f := newProjectClearHTTPFixture(t)
		owned := f.createTask(t, f.standaloneStore, f.standalone.ID, "owned")
		code, body := projectClearRaw(f.handler, f.path(f.standalone.ID), `{"name":"Private Omega"}`, "")
		if code != http.StatusOK || !strings.Contains(body, `"standalone":true`) {
			t.Fatalf("standalone clear = %d %s", code, body)
		}
		if _, err := f.standaloneStore.Get(owned.Task.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("selected standalone task remains: %v", err)
		}
	})
}

func TestClearHomeProjectTaskDataHTTPRejectsStrictAndUnauthorizedRequestsWithoutMutation(t *testing.T) {
	f := newProjectClearHTTPFixture(t)
	owned := f.createTask(t, f.groupedStore, f.grouped.ID, "owned")
	for _, tc := range []struct {
		name  string
		body  string
		actor string
		code  int
	}{
		{name: "unknown JSON field", body: `{"name":"东京 Team Alpha","unexpected":true}`, code: http.StatusBadRequest},
		{name: "multiple JSON values", body: `{"name":"东京 Team Alpha"} {}`, code: http.StatusBadRequest},
		{name: "nonhuman actor", body: `{"name":"东京 Team Alpha"}`, actor: "agent:codex", code: http.StatusForbidden},
		// Unicode, internal spaces, case, and trailing spaces are intentionally exact.
		{name: "exact name mismatch", body: `{"name":"东京 team  Alpha "}`, code: http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := projectClearRaw(f.handler, f.path(f.grouped.ID), tc.body, tc.actor)
			if code != tc.code {
				t.Fatalf("rejected clear = %d %s, want %d", code, body, tc.code)
			}
			if _, err := f.groupedStore.Get(owned.Task.ID); err != nil {
				t.Fatalf("rejected request mutated task: %v", err)
			}
		})
	}
	if code, body := projectClearRaw(f.handler, f.path("project_missing"), `{"name":"东京 Team Alpha"}`, ""); code != http.StatusNotFound {
		t.Fatalf("unknown stable project id = %d %s, want 404", code, body)
	}
}
