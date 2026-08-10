package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"carbon/internal/home"
)

func TestHomeMetadataHTTPRoundTrip(t *testing.T) {
	root := t.TempDir()
	service := NewWithScope("human:test", ScopeDefaults{Home: root, HomeByDefault: true})
	handler := service.Handler()

	if code, body := raw(handler, http.MethodPost, "/api/home", `{}`); code != http.StatusOK {
		t.Fatalf("ensure home = %d %s", code, body)
	}
	clusterBody, err := json.Marshal(map[string]any{
		"name": "Experiments", "slug": "lab", "description": "product experiments", "prefix": "LAB",
	})
	if err != nil {
		t.Fatal(err)
	}
	code, body := raw(handler, http.MethodPost, "/api/home/clusters", string(clusterBody))
	if code != http.StatusCreated {
		t.Fatalf("create cluster = %d %s", code, body)
	}
	var cluster home.Cluster
	if err := json.Unmarshal([]byte(body), &cluster); err != nil {
		t.Fatal(err)
	}
	if cluster.Slug != "lab" || cluster.Description != "product experiments" {
		t.Fatalf("created cluster metadata = %+v", cluster)
	}

	code, body = raw(handler, http.MethodPatch, "/api/home/clusters/"+cluster.ID,
		`{"slug":"experiments","description":"desktop and mobile experiments"}`)
	if code != http.StatusOK {
		t.Fatalf("update cluster = %d %s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &cluster); err != nil {
		t.Fatal(err)
	}
	if cluster.Slug != "experiments" || len(cluster.SlugAliases) != 1 || cluster.SlugAliases[0] != "lab" {
		t.Fatalf("updated cluster metadata = %+v", cluster)
	}

	projectBody, err := json.Marshal(map[string]any{
		"name": "Desktop", "slug": "desktop", "description": "Windows client",
		"kind": "pc", "sourcePath": t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	code, body = raw(handler, http.MethodPost, "/api/home/clusters/"+cluster.ID+"/projects", string(projectBody))
	if code != http.StatusCreated {
		t.Fatalf("create project = %d %s", code, body)
	}
	var project home.Project
	if err := json.Unmarshal([]byte(body), &project); err != nil {
		t.Fatal(err)
	}
	if project.Slug != "desktop" || project.Description != "Windows client" {
		t.Fatalf("created project metadata = %+v", project)
	}

	code, body = raw(handler, http.MethodPatch, "/api/home/clusters/"+cluster.ID+"/projects/"+project.ID,
		`{"slug":"windows","description":"portable Windows client"}`)
	if code != http.StatusOK {
		t.Fatalf("update project = %d %s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &project); err != nil {
		t.Fatal(err)
	}
	if project.Slug != "windows" || len(project.SlugAliases) != 1 || project.SlugAliases[0] != "desktop" {
		t.Fatalf("updated project metadata = %+v", project)
	}

	code, body = raw(handler, http.MethodGet, "/api/home", "")
	if code != http.StatusOK || !containsAll(body, `"slug":"experiments"`, `"slugAliases":["lab"]`, `"slug":"windows"`, `"slugAliases":["desktop"]`) {
		t.Fatalf("home metadata response = %d %s", code, body)
	}
}

func TestStandaloneHomeProjectHTTPCRUDAndRelink(t *testing.T) {
	root := t.TempDir()
	service := NewWithScope("human:test", ScopeDefaults{Home: root, HomeByDefault: true})
	handler := service.Handler()
	if code, body := raw(handler, http.MethodPost, "/api/home", `{}`); code != http.StatusOK {
		t.Fatalf("ensure home = %d %s", code, body)
	}
	createBody, err := json.Marshal(map[string]any{
		"name": "Desktop", "slug": "desktop", "description": "private app", "kind": "pc", "sourcePath": t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	code, body := raw(handler, http.MethodPost, "/api/home/projects", string(createBody))
	if code != http.StatusCreated {
		t.Fatalf("create standalone project = %d %s", code, body)
	}
	var project home.Project
	if err := json.Unmarshal([]byte(body), &project); err != nil {
		t.Fatal(err)
	}
	if project.ID == "" || project.Slug != "desktop" {
		t.Fatalf("created standalone project = %#v", project)
	}
	if code, body = raw(handler, http.MethodGet, "/api/home/projects/"+project.ID, ""); code != http.StatusOK {
		t.Fatalf("get standalone project = %d %s", code, body)
	}
	if code, body = raw(handler, http.MethodPatch, "/api/home/projects/"+project.ID, `{"slug":"desktop-next","description":"updated private app"}`); code != http.StatusOK {
		t.Fatalf("update standalone project = %d %s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &project); err != nil {
		t.Fatal(err)
	}
	if project.Slug != "desktop-next" || len(project.SlugAliases) != 1 || project.SlugAliases[0] != "desktop" {
		t.Fatalf("updated standalone metadata = %#v", project)
	}
	relinkBody, err := json.Marshal(map[string]any{"sourcePath": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if code, body = raw(handler, http.MethodPost, "/api/home/projects/"+project.ID+"/relink", string(relinkBody)); code != http.StatusOK {
		t.Fatalf("relink standalone project = %d %s", code, body)
	}
	if code, body = raw(handler, http.MethodGet, "/api/home/projects", ""); code != http.StatusOK || !strings.Contains(body, project.ID) {
		t.Fatalf("list standalone projects = %d %s", code, body)
	}
	if dataRoot, err := home.ProjectDataRoot(root, project.ID); err != nil || dataRoot == "" {
		t.Fatalf("standalone private data root = %q, %v", dataRoot, err)
	}
}

func TestDetachNestedProjectHTTPUsesSafeDefaultPath(t *testing.T) {
	root := t.TempDir()
	if _, err := home.Ensure(root); err != nil {
		t.Fatal(err)
	}
	cluster, err := home.CreateCluster(root, home.CreateClusterRequest{Name: "Legacy shared", Prefix: "LEG"})
	if err != nil {
		t.Fatal(err)
	}
	nested, err := home.AddProject(root, cluster.ID, home.AddProjectRequest{Name: "Desktop", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	service := NewWithScope("human:test", ScopeDefaults{Home: root, HomeByDefault: true})
	code, body := raw(service.Handler(), http.MethodPost, "/api/home/clusters/"+cluster.ID+"/projects/"+nested.ID+"/detach", `{}`)
	if code != http.StatusOK {
		t.Fatalf("default safe detach = %d %s", code, body)
	}
	var result home.DetachProjectResult
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	if result.Project.ID != nested.ID || result.SharedStoreCopy || result.DataRoot == "" || result.ReceiptPath == "" {
		t.Fatalf("detach result = %#v", result)
	}
	projects, err := home.ListProjects(root)
	if err != nil || len(projects) != 1 || projects[0].ID != nested.ID {
		t.Fatalf("top-level projects after detach = %#v, %v", projects, err)
	}
}

func containsAll(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(value, fragment) {
			return false
		}
	}
	return true
}
