package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"carbon/internal/home"
)

type workLogHTTPFixture struct {
	homeRoot string
	cluster1 home.Cluster
	cluster2 home.Cluster
	project1 home.Project
	project2 home.Project
	project3 home.Project
	server   *Server
	handler  http.Handler
}

func newWorkLogHTTPFixture(t *testing.T) workLogHTTPFixture {
	t.Helper()
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	cluster1, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Work Log One", Prefix: "WLO"})
	if err != nil {
		t.Fatal(err)
	}
	cluster2, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Work Log Two", Prefix: "WLT"})
	if err != nil {
		t.Fatal(err)
	}
	project1, err := home.AddProject(homeRoot, cluster1.ID, home.AddProjectRequest{Name: "One A", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	project2, err := home.AddProject(homeRoot, cluster1.ID, home.AddProjectRequest{Name: "One B", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	project3, err := home.AddProject(homeRoot, cluster2.ID, home.AddProjectRequest{Name: "Two A", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s := NewWithScope("human:web", ScopeDefaults{
		Home: homeRoot, ClusterID: cluster1.ID, ProjectID: project1.ID, HomeByDefault: true,
	})
	return workLogHTTPFixture{
		homeRoot: homeRoot,
		cluster1: cluster1,
		cluster2: cluster2,
		project1: project1,
		project2: project2,
		project3: project3,
		server:   s,
		handler:  s.Handler(),
	}
}

func (f workLogHTTPFixture) scopedPath(path string, cluster home.Cluster, projectID string) string {
	query := url.Values{"home": {f.homeRoot}, "cluster": {cluster.ID}}
	if projectID != "" {
		query.Set("project", projectID)
	}
	return path + "?" + query.Encode()
}

func workLogHTTPCall(t *testing.T, h http.Handler, method, path, body, actor string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if actor != "" {
		request.Header.Set("X-Carbon-Actor", actor)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

func decodeWorkLogHTTP(t *testing.T, response *httptest.ResponseRecorder) workLogDTO {
	t.Helper()
	var value workLogDTO
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode work log response %q: %v", response.Body.String(), err)
	}
	return value
}

func TestWorkLogHandlersStampStrictlyAndEnforceVisibility(t *testing.T) {
	fixture := newWorkLogHTTPFixture(t)
	createPath := fixture.scopedPath("/api/worklogs", fixture.cluster1, fixture.project1.ID)

	forged := workLogHTTPCall(t, fixture.handler, http.MethodPost, createPath,
		`{"visibility":"worker_private","title":"forged","worker":"agent:forged"}`, "agent:owner", nil)
	if forged.Code != http.StatusBadRequest {
		t.Fatalf("client audit/worker field = %d %s, want 400", forged.Code, forged.Body.String())
	}
	createdResponse := workLogHTTPCall(t, fixture.handler, http.MethodPost, createPath,
		`{"visibility":"worker_private","title":"private work","body":"details","tags":["backend"]}`, "agent:owner", nil)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create private = %d %s, want 201", createdResponse.Code, createdResponse.Body.String())
	}
	private := decodeWorkLogHTTP(t, createdResponse)
	if private.Worker != "agent:owner" || private.ClusterID != fixture.cluster1.ID || private.ProjectID != fixture.project1.ID || private.CreatedBy != "agent:owner" || private.Version == "" || createdResponse.Header().Get("ETag") != `"`+private.Version+`"` {
		t.Fatalf("server did not stamp immutable record: %#v, ETag=%q", private, createdResponse.Header().Get("ETag"))
	}
	getPath := fixture.scopedPath("/api/worklogs/"+private.ID, fixture.cluster1, fixture.project1.ID)
	if response := workLogHTTPCall(t, fixture.handler, http.MethodGet, getPath, "", "agent:peer", nil); response.Code != http.StatusNotFound {
		t.Fatalf("peer private get = %d %s, want 404", response.Code, response.Body.String())
	}
	crossHumanPath := fixture.scopedPath("/api/worklogs/"+private.ID, fixture.cluster2, fixture.project3.ID)
	if response := workLogHTTPCall(t, fixture.handler, http.MethodGet, crossHumanPath, "", "human:admin", nil); response.Code != http.StatusOK {
		t.Fatalf("human cross-cluster private get = %d %s, want 200", response.Code, response.Body.String())
	}

	projectResponse := workLogHTTPCall(t, fixture.handler, http.MethodPost, createPath,
		`{"visibility":"project_public","title":"project work"}`, "agent:owner", nil)
	if projectResponse.Code != http.StatusCreated {
		t.Fatalf("create project log = %d %s", projectResponse.Code, projectResponse.Body.String())
	}
	project := decodeWorkLogHTTP(t, projectResponse)
	if response := workLogHTTPCall(t, fixture.handler, http.MethodGet, fixture.scopedPath("/api/worklogs/"+project.ID, fixture.cluster1, fixture.project1.ID), "", "agent:peer", nil); response.Code != http.StatusOK {
		t.Fatalf("same project public get = %d %s, want 200", response.Code, response.Body.String())
	}
	if response := workLogHTTPCall(t, fixture.handler, http.MethodGet, fixture.scopedPath("/api/worklogs/"+project.ID, fixture.cluster1, fixture.project2.ID), "", "agent:peer", nil); response.Code != http.StatusNotFound {
		t.Fatalf("foreign project public get = %d %s, want 404", response.Code, response.Body.String())
	}
	if response := workLogHTTPCall(t, fixture.handler, http.MethodGet, fixture.scopedPath("/api/worklogs/"+project.ID, fixture.cluster1, ""), "", "agent:peer", nil); response.Code != http.StatusOK {
		t.Fatalf("cluster-scoped project public get = %d %s, want 200", response.Code, response.Body.String())
	}

	globalResponse := workLogHTTPCall(t, fixture.handler, http.MethodPost, createPath,
		`{"visibility":"global_public","title":"global work"}`, "agent:owner", nil)
	if globalResponse.Code != http.StatusCreated {
		t.Fatalf("create global log = %d %s", globalResponse.Code, globalResponse.Body.String())
	}
	global := decodeWorkLogHTTP(t, globalResponse)
	if response := workLogHTTPCall(t, fixture.handler, http.MethodGet, fixture.scopedPath("/api/worklogs/"+global.ID, fixture.cluster2, fixture.project3.ID), "", "agent:peer", nil); response.Code != http.StatusOK {
		t.Fatalf("same-Home cross-cluster global get = %d %s, want 200", response.Code, response.Body.String())
	}
}

func TestWorkLogHandlersListLimitAndPartialETagUpdates(t *testing.T) {
	fixture := newWorkLogHTTPFixture(t)
	createPath := fixture.scopedPath("/api/worklogs", fixture.cluster1, fixture.project1.ID)
	if response := workLogHTTPCall(t, fixture.handler, http.MethodGet, createPath, "", "agent:owner", nil); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing list limit = %d %s, want 422", response.Code, response.Body.String())
	}
	if response := workLogHTTPCall(t, fixture.handler, http.MethodGet, createPath+"&limit=201", "", "agent:owner", nil); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("oversized list limit = %d %s, want 422", response.Code, response.Body.String())
	}
	createdResponse := workLogHTTPCall(t, fixture.handler, http.MethodPost, createPath,
		`{"visibility":"project_public","title":"before update","body":"keep","tags":["one"]}`, "agent:owner", nil)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", createdResponse.Code, createdResponse.Body.String())
	}
	created := decodeWorkLogHTTP(t, createdResponse)
	updatePath := fixture.scopedPath("/api/worklogs/"+created.ID, fixture.cluster1, fixture.project1.ID)
	updatedResponse := workLogHTTPCall(t, fixture.handler, http.MethodPut, updatePath,
		`{"title":"after update","expectedVersion":"incorrect-body-version"}`, "agent:owner", map[string]string{"If-Match": `"` + created.Version + `"`})
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("header-priority partial update = %d %s", updatedResponse.Code, updatedResponse.Body.String())
	}
	updated := decodeWorkLogHTTP(t, updatedResponse)
	if updated.Title != "after update" || updated.Body != "keep" || len(updated.Tags) != 1 || updated.Tags[0] != "one" || updated.Version == created.Version {
		t.Fatalf("partial update lost data/version: %#v", updated)
	}
	if updatedResponse.Header().Get("ETag") != `"`+updated.Version+`"` {
		t.Fatalf("updated ETag = %q, want %q", updatedResponse.Header().Get("ETag"), `"`+updated.Version+`"`)
	}
	staleResponse := workLogHTTPCall(t, fixture.handler, http.MethodPut, updatePath,
		`{"body":"stale","expectedVersion":"`+created.Version+`"}`, "agent:owner", nil)
	if staleResponse.Code != http.StatusConflict {
		t.Fatalf("stale update = %d %s, want 409", staleResponse.Code, staleResponse.Body.String())
	}
	if staleResponse.Header().Get("ETag") != `"`+updated.Version+`"` || !strings.Contains(staleResponse.Body.String(), `"currentVersion":"`+updated.Version+`"`) {
		t.Fatalf("stale conflict lacks current version/ETag: ETag=%q body=%s", staleResponse.Header().Get("ETag"), staleResponse.Body.String())
	}
	if response := workLogHTTPCall(t, fixture.handler, http.MethodDelete, updatePath, "", "agent:owner", nil); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("delete without expected version = %d %s, want 422", response.Code, response.Body.String())
	}
	deletedResponse := workLogHTTPCall(t, fixture.handler, http.MethodDelete, updatePath, "", "agent:owner", map[string]string{"If-Match": `"` + updated.Version + `"`})
	if deletedResponse.Code != http.StatusNoContent {
		t.Fatalf("delete with ETag = %d %s, want 204", deletedResponse.Code, deletedResponse.Body.String())
	}
	if response := workLogHTTPCall(t, fixture.handler, http.MethodGet, updatePath, "", "agent:owner", nil); response.Code != http.StatusNotFound {
		t.Fatalf("get deleted = %d %s, want 404", response.Code, response.Body.String())
	}
}

func TestWorkLogHandlersUseStandalonePrivateProjectRoot(t *testing.T) {
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
	service := NewWithScope("human:web", ScopeDefaults{Home: homeRoot, HomeByDefault: true})
	pathFor := func(projectID string) string {
		query := url.Values{"home": {homeRoot}, "project": {projectID}}
		return "/api/worklogs?" + query.Encode()
	}
	pathForID := func(id, projectID string) string {
		query := url.Values{"home": {homeRoot}, "project": {projectID}}
		return "/api/worklogs/" + id + "?" + query.Encode()
	}
	createdResponse := workLogHTTPCall(t, service.Handler(), http.MethodPost, pathFor(first.ID),
		`{"visibility":"global_public","title":"private standalone work"}`, "agent:owner", nil)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create standalone work log = %d %s", createdResponse.Code, createdResponse.Body.String())
	}
	created := decodeWorkLogHTTP(t, createdResponse)
	if !created.Standalone || created.ClusterID != "" || created.ProjectID != first.ID {
		t.Fatalf("standalone work log response = %#v", created)
	}
	if response := workLogHTTPCall(t, service.Handler(), http.MethodGet, pathFor(second.ID)+"&limit=10", "", "agent:peer", nil); response.Code != http.StatusOK || strings.Contains(response.Body.String(), created.ID) {
		t.Fatalf("sibling standalone list = %d %s", response.Code, response.Body.String())
	}
	if response := workLogHTTPCall(t, service.Handler(), http.MethodGet, pathForID(created.ID, second.ID), "", "agent:peer", nil); response.Code == http.StatusOK {
		t.Fatalf("sibling standalone unexpectedly read work log: %s", response.Body.String())
	}
	if response := workLogHTTPCall(t, service.Handler(), http.MethodGet, pathFor(first.ID)+"&include_cluster=true&limit=10", "", "agent:owner", nil); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("standalone work log include_cluster = %d %s, want 422", response.Code, response.Body.String())
	}
}
