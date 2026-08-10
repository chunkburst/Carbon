package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"carbon/internal/home"
)

type catalogPresentationHTTPFixture struct {
	root    string
	cluster home.Cluster
	project home.Project
	server  *Server
	handler http.Handler
}

func newCatalogPresentationHTTPFixture(t *testing.T) catalogPresentationHTTPFixture {
	t.Helper()
	root := t.TempDir()
	if _, err := home.Ensure(root); err != nil {
		t.Fatal(err)
	}
	cluster, err := home.CreateCluster(root, home.CreateClusterRequest{Name: "Catalog", Prefix: "CAT"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := home.AddProject(root, cluster.ID, home.AddProjectRequest{
		Name: "Desktop", Kind: home.ProjectPC, SourcePath: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewWithScope("human:test", ScopeDefaults{Home: root, HomeByDefault: true})
	return catalogPresentationHTTPFixture{
		root: root, cluster: cluster, project: project, server: service,
		handler: catalogPresentationTestHandler(service),
	}
}

// catalogPresentationTestHandler keeps this file independent from the main route
// registry, which is intentionally owned by the integration change in server.go.
func catalogPresentationTestHandler(service *Server) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/home/presentation", service.handleGetCatalogPresentation)
	mux.HandleFunc("PUT /api/home/presentation/{kind}/{id}/icon", service.handlePutCatalogPresentationIcon)
	mux.HandleFunc("GET /api/home/presentation/{kind}/{id}/asset", service.handleGetCatalogPresentationAsset)
	mux.HandleFunc("PUT /api/home/presentation/{kind}/{id}/asset", service.handlePutCatalogPresentationAsset)
	mux.HandleFunc("DELETE /api/home/presentation/{kind}/{id}/asset", service.handleDeleteCatalogPresentationAsset)
	return mux
}

func TestCatalogPresentationHTTPRoundTripUsesHomeGlobalDocument(t *testing.T) {
	f := newCatalogPresentationHTTPFixture(t)
	manifestPath := filepath.Join(f.root, home.CarbonDirName, home.ManifestFilename)
	beforeManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	var initial home.CatalogPresentation
	call(t, f.handler, http.MethodGet, "/api/home/presentation", "", &initial)
	if initial.Version != home.CatalogPresentationVersion || len(initial.Clusters) != 0 || len(initial.Projects) != 0 {
		t.Fatalf("initial presentation = %#v, want v1 empty maps", initial)
	}
	if _, err := os.Lstat(filepath.Join(f.root, home.CarbonDirName, home.CatalogPresentationFilename)); !os.IsNotExist(err) {
		t.Fatalf("GET created presentation file: %v", err)
	}

	clusterPath := "/api/home/presentation/cluster/" + f.cluster.ID + "/icon"
	code, body := raw(f.handler, http.MethodPut, clusterPath, `{"icon":{"kind":"builtin","key":"layers"}}`)
	if code != http.StatusOK {
		t.Fatalf("cluster PUT = %d %s", code, body)
	}
	var updated home.CatalogPresentation
	if err := json.Unmarshal([]byte(body), &updated); err != nil {
		t.Fatal(err)
	}
	if got := updated.Clusters[f.cluster.ID]; got != (home.Icon{Kind: "builtin", Key: "layers"}) {
		t.Fatalf("cluster response icon = %#v", got)
	}

	projectPath := "/api/home/presentation/project/" + f.project.ID + "/icon"
	code, body = raw(f.handler, http.MethodPut, projectPath, `{"icon":{"kind":"emoji","key":"rocket"}}`)
	if code != http.StatusOK {
		t.Fatalf("project PUT = %d %s", code, body)
	}
	updated = home.CatalogPresentation{}
	if err := json.Unmarshal([]byte(body), &updated); err != nil {
		t.Fatal(err)
	}
	if got := updated.Projects[f.project.ID]; got != (home.Icon{Kind: "emoji", Key: "rocket"}) {
		t.Fatalf("project response icon = %#v", got)
	}

	clearRequest := httptest.NewRequest(http.MethodPut, clusterPath, strings.NewReader(`{"icon":null}`))
	clearRecorder := httptest.NewRecorder()
	clearIcon, clearOK := decodeStrictCatalogPresentationIcon(clearRecorder, clearRequest)
	if !clearOK || clearIcon != nil {
		t.Fatalf("null icon decode = %#v, ok=%v, body=%s", clearIcon, clearOK, clearRecorder.Body.String())
	}
	code, body = raw(f.handler, http.MethodPut, clusterPath, `{"icon":null}`)
	if code != http.StatusOK {
		t.Fatalf("cluster clear = %d %s", code, body)
	}
	updated = home.CatalogPresentation{}
	if err := json.Unmarshal([]byte(body), &updated); err != nil {
		t.Fatal(err)
	}
	if _, exists := updated.Clusters[f.cluster.ID]; exists {
		t.Fatalf("cleared cluster still present: %#v", updated.Clusters)
	}

	var listed home.CatalogPresentation
	call(t, f.handler, http.MethodGet, "/api/home/presentation", "", &listed)
	if got := listed.Projects[f.project.ID]; got != (home.Icon{Kind: "emoji", Key: "rocket"}) {
		t.Fatalf("listed project icon = %#v", got)
	}
	afterManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeManifest, afterManifest) {
		t.Fatalf("presentation HTTP changed home.json\nbefore=%s\nafter=%s", beforeManifest, afterManifest)
	}
}

func TestCatalogPresentationHTTPRejectsStrictAndUnsafeBodies(t *testing.T) {
	f := newCatalogPresentationHTTPFixture(t)
	path := "/api/home/presentation/cluster/" + f.cluster.ID + "/icon"
	for _, test := range []struct {
		name string
		body string
		want int
	}{
		{"missing icon", `{}`, http.StatusBadRequest},
		{"unknown top level", `{"icon":null,"home":"C:/other"}`, http.StatusBadRequest},
		{"duplicate top level", `{"icon":null,"icon":null}`, http.StatusBadRequest},
		{"unknown nested icon field", `{"icon":{"kind":"builtin","key":"folder","svg":"<svg/>"}}`, http.StatusBadRequest},
		{"duplicate nested icon field", `{"icon":{"kind":"builtin","key":"folder","key":"layers"}}`, http.StatusBadRequest},
		{"multiple values", `{"icon":null} {"icon":null}`, http.StatusBadRequest},
		{"array body", `[]`, http.StatusBadRequest},
		{"unsafe path token", `{"icon":{"kind":"builtin","key":"../../icon.svg"}}`, http.StatusUnprocessableEntity},
		{"data URI token", `{"icon":{"kind":"builtin","key":"data:image/svg+xml;base64,PHN2Zz4="}}`, http.StatusUnprocessableEntity},
		{"unknown kind", `{"icon":{"kind":"url","key":"https://example.test"}}`, http.StatusUnprocessableEntity},
	} {
		t.Run(test.name, func(t *testing.T) {
			if code, body := raw(f.handler, http.MethodPut, path, test.body); code != test.want {
				t.Fatalf("PUT = %d %s, want %d", code, body, test.want)
			}
		})
	}
	if code, body := raw(f.handler, http.MethodPut, path, strings.Repeat("x", int(maxJSONBodyBytes)+1)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized PUT = %d %s, want 413", code, body)
	}
	if code, body := raw(f.handler, http.MethodPut, "/api/home/presentation/clusters/"+f.cluster.ID+"/icon", `{"icon":null}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid target kind = %d %s, want 422", code, body)
	}
	missingCluster := "cluster_" + strings.Repeat("0", 32)
	if code, body := raw(f.handler, http.MethodPut, "/api/home/presentation/cluster/"+missingCluster+"/icon", `{"icon":null}`); code != http.StatusNotFound {
		t.Fatalf("missing cluster = %d %s, want 404", code, body)
	}
}

func TestCatalogPresentationHTTPRequiresHomeOnlyScope(t *testing.T) {
	f := newProjectScopeFixture(t)
	selected := catalogPresentationTestHandler(f.server)
	for _, test := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/home/presentation", ""},
		{http.MethodPut, "/api/home/presentation/cluster/cluster_" + strings.Repeat("0", 32) + "/icon", `{"icon":null}`},
	} {
		if code, body := raw(selected, test.method, test.path, test.body); code != http.StatusBadRequest {
			t.Fatalf("selected %s = %d %s, want 400", test.method, code, body)
		}
	}

	homeOnly := NewWithScope("human:test", ScopeDefaults{Home: f.homeRoot, HomeByDefault: true})
	handler := catalogPresentationTestHandler(homeOnly)
	if code, body := raw(handler, http.MethodGet, "/api/home/presentation?cluster="+url.QueryEscape(f.project1.ID), ""); code != http.StatusBadRequest {
		t.Fatalf("query cluster scope = %d %s, want 400", code, body)
	}
	if code, body := raw(handler, http.MethodGet, "/api/home/presentation?path="+url.QueryEscape(f.homeRoot), ""); code != http.StatusBadRequest {
		t.Fatalf("legacy path scope = %d %s, want 400", code, body)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/home/presentation", nil)
	req.Header.Set("X-Carbon-Project", f.project1.ID)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("header project scope = %d %s, want 400", resp.Code, resp.Body.String())
	}
}
