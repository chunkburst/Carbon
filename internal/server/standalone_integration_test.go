package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"carbon/internal/home"
	"carbon/internal/store"
)

func TestStandaloneProjectScopeKeepsSearchStatsAndEventsPrivate(t *testing.T) {
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	first, err := home.AddStandaloneProject(homeRoot, home.AddProjectRequest{Name: "First", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	second, err := home.AddStandaloneProject(homeRoot, home.AddProjectRequest{Name: "Second", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	firstRoot, err := home.ProjectDataRoot(homeRoot, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondRoot, err := home.ProjectDataRoot(homeRoot, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstDoc, err := store.New(firstRoot).Create(store.Draft{
		Title: "needle private first", ProjectID: first.ID, ProjectIDSet: true,
	}, "agent:first", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.New(secondRoot).Create(store.Draft{
		Title: "needle private second", ProjectID: second.ID, ProjectIDSet: true,
	}, "agent:second", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	server := NewWithScope("human:test", ScopeDefaults{Home: homeRoot, HomeByDefault: true})
	query := url.Values{"home": {homeRoot}, "project": {first.ID}, "q": {"needle"}}
	request := httptest.NewRequest(http.MethodGet, "/api/search?"+query.Encode(), nil)
	scope, err := server.resolveScope(request)
	if err != nil {
		t.Fatal(err)
	}
	if !scope.Standalone || scope.ClusterID != "" || scope.ProjectID != first.ID || scope.Root != firstRoot {
		t.Fatalf("resolved standalone scope = %#v", scope)
	}

	response := rawRecorder(server.Handler(), http.MethodGet, "/api/search?"+query.Encode())
	if response.Code != http.StatusOK {
		t.Fatalf("standalone search = %d %s", response.Code, response.Body.String())
	}
	var searchResponse struct {
		Results []struct {
			Task struct {
				ID        string `json:"id"`
				ProjectID string `json:"projectId"`
			} `json:"task"`
		} `json:"results"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &searchResponse); err != nil {
		t.Fatal(err)
	}
	if len(searchResponse.Results) != 1 || searchResponse.Results[0].Task.ID != firstDoc.Task.ID || searchResponse.Results[0].Task.ProjectID != first.ID {
		t.Fatalf("standalone search results = %#v", searchResponse.Results)
	}
	query.Set("include_cluster", "true")
	if response := rawRecorder(server.Handler(), http.MethodGet, "/api/search?"+query.Encode()); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("standalone search include_cluster = %d %s, want 422", response.Code, response.Body.String())
	}

	statsQuery := url.Values{"home": {homeRoot}, "project": {first.ID}}
	response = rawRecorder(server.Handler(), http.MethodGet, "/api/stats/workers?"+statsQuery.Encode())
	if response.Code != http.StatusOK {
		t.Fatalf("standalone worker stats = %d %s", response.Code, response.Body.String())
	}
	var statsResponse struct {
		Scope struct {
			Standalone bool   `json:"standalone"`
			ProjectID  string `json:"projectId"`
		} `json:"scope"`
		Aggregate struct {
			TaskCount int `json:"taskCount"`
		} `json:"aggregate"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &statsResponse); err != nil {
		t.Fatal(err)
	}
	if !statsResponse.Scope.Standalone || statsResponse.Scope.ProjectID != first.ID || statsResponse.Aggregate.TaskCount != 1 {
		t.Fatalf("standalone worker stats = %#v", statsResponse)
	}
	statsQuery.Set("include_cluster", "true")
	if response := rawRecorder(server.Handler(), http.MethodGet, "/api/stats/workers?"+statsQuery.Encode()); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("standalone stats include_cluster = %d %s, want 422", response.Code, response.Body.String())
	}

	if !eventVisibleToScope(scope, Event{Type: evtTaskChanged, ID: "other-store-task"}, false) {
		t.Fatal("standalone event visibility rejected its private root")
	}
	if eventVisibleToScope(scope, Event{Type: evtTaskChanged, ID: firstDoc.Task.ID}, true) {
		t.Fatal("standalone event visibility accepted include_cluster")
	}
	if report, err := server.homeWorkerStats(context.Background(), homeRoot, "", first.ID); err != nil || report.Aggregate.TaskCount != 1 {
		t.Fatalf("standalone project-only home stats = %#v, %v", report, err)
	}
}

func TestExplicitStandaloneProjectOverridesServerDefaultCluster(t *testing.T) {
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	cluster, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Default shared", Prefix: "DEF"})
	if err != nil {
		t.Fatal(err)
	}
	standalone, err := home.AddStandaloneProject(homeRoot, home.AddProjectRequest{Name: "Private", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithScope("human:test", ScopeDefaults{
		Home: homeRoot, ClusterID: cluster.ID, HomeByDefault: true,
	})
	query := url.Values{"home": {homeRoot}, "project": {standalone.ID}}
	scope, err := server.resolveScope(httptest.NewRequest(http.MethodGet, "/api/status?"+query.Encode(), nil))
	if err != nil {
		t.Fatal(err)
	}
	if !scope.Standalone || scope.ClusterID != "" || scope.ProjectID != standalone.ID {
		t.Fatalf("standalone request inherited default cluster: %#v", scope)
	}
}

func rawRecorder(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
