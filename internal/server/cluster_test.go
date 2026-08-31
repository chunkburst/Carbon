package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"carbon/internal/cluster"
	"carbon/internal/mcp"
	"carbon/internal/repo"
	"carbon/internal/store"
)

func TestClusterCreateDoesNotInitializeProject(t *testing.T) {
	root := t.TempDir()
	s := New(root, "human:test")
	h := s.Handler()

	var before clusterResp
	call(t, h, "GET", clusterURL(root), "", &before)
	if before.Initialized || before.LegacyAvailable || len(before.Projects) != 0 {
		t.Fatalf("uninitialized cluster = %+v", before)
	}

	var created clusterResp
	call(t, h, "POST", "/api/cluster", clusterJSON(t, clusterReq{Path: root, Name: "Roadmap"}), &created)
	if !created.Initialized || created.Name != "Roadmap" || created.Root != root || len(created.Projects) != 0 {
		t.Fatalf("created cluster = %+v", created)
	}
	if _, err := os.Stat(filepath.Join(root, cluster.ManifestFilename)); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if repo.IsInitialized(root) {
		t.Fatal("creating a cluster initialized the cluster root project")
	}
}

func TestClusterOpenAddsLegacyWorkspaceWithoutChangingIt(t *testing.T) {
	root := t.TempDir()
	if err := repo.Init(root, "LEG"); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, filepath.Join(root, repo.CarbonDirName))
	s := New(root, "human:test")
	var available clusterResp
	call(t, s.Handler(), "GET", clusterURL(root), "", &available)
	if available.Initialized || !available.LegacyAvailable || len(available.Projects) != 0 {
		t.Fatalf("legacy availability = %+v", available)
	}
	// Create the registry first without the legacy flag, then prove opening it adds the
	// existing workspace rather than replacing the manifest or calling repo.Init again.
	if _, err := cluster.Ensure(root, "Existing cluster", false); err != nil {
		t.Fatal(err)
	}

	var opened clusterResp
	call(t, s.Handler(), "POST", "/api/cluster", clusterJSON(t, clusterReq{Path: root}), &opened)
	if !opened.Initialized || len(opened.Projects) != 1 {
		t.Fatalf("opened cluster = %+v", opened)
	}
	project := opened.Projects[0]
	if !project.Legacy || project.Path != root || !project.Initialized || project.Prefix != "LEG" {
		t.Fatalf("legacy project = %+v", project)
	}
	if after := snapshotTree(t, filepath.Join(root, repo.CarbonDirName)); !reflect.DeepEqual(before, after) {
		t.Fatal("opening legacy cluster changed project .carbon bytes")
	}
}

func TestClusterSummarySeparatesSameTaskIDAcrossProjects(t *testing.T) {
	clusterRoot := t.TempDir()
	p1 := filepath.Join(t.TempDir(), "project-one")
	p2 := filepath.Join(t.TempDir(), "project-two")
	for _, project := range []string{p1, p2} {
		if err := os.Mkdir(project, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := repo.Init(project, "SAME"); err != nil {
			t.Fatal(err)
		}
		writeClusterTask(t, project, "SAME-1")
		if _, err := mcp.NewService(store.New(project), "human:test", nil).BeginSession(context.Background(), mcp.BeginSessionInput{
			TaskID: "SAME-1", ExpectedActor: "human:test", IdempotencyKey: filepath.Base(project),
		}); err != nil {
			t.Fatalf("begin session for %s: %v", project, err)
		}
	}

	s := New(clusterRoot, "human:test")
	h := s.Handler()
	var created clusterResp
	call(t, h, "POST", "/api/cluster", clusterJSON(t, clusterReq{Path: clusterRoot}), &created)
	for _, project := range []string{p1, p2} {
		var updated clusterResp
		call(t, h, "POST", "/api/cluster/projects", clusterJSON(t, clusterProjectReq{ClusterPath: clusterRoot, Path: project}), &updated)
	}

	var got clusterResp
	call(t, h, "GET", clusterURL(clusterRoot), "", &got)
	if len(got.Projects) != 2 || got.Projects[0].ID == got.Projects[1].ID {
		t.Fatalf("projects = %+v", got.Projects)
	}
	for _, project := range got.Projects {
		if project.Tasks != 1 || project.Active != 1 || project.Stalled != 0 || project.Stagnant != 0 || project.Review != 0 || project.LiveAgents != 1 {
			t.Fatalf("summary for %s = %+v; duplicate task ids must not merge", project.Path, project)
		}
	}
}

func TestClusterSummaryCountsTaskStagnationSeparatelyFromSessionStalls(t *testing.T) {
	clusterRoot := t.TempDir()
	project := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := repo.Init(project, "OLD"); err != nil {
		t.Fatal(err)
	}
	writeClusterTaskAt(t, project, "OLD-1", time.Now().UTC().Add(-48*time.Hour))

	s := New(clusterRoot, "human:test")
	h := s.Handler()
	call(t, h, "POST", "/api/cluster", clusterJSON(t, clusterReq{Path: clusterRoot}), &clusterResp{})
	var updated clusterResp
	call(t, h, "POST", "/api/cluster/projects", clusterJSON(t, clusterProjectReq{ClusterPath: clusterRoot, Path: project}), &updated)

	var got clusterResp
	call(t, h, "GET", clusterURL(clusterRoot), "", &got)
	if len(got.Projects) != 1 {
		t.Fatalf("projects = %+v", got.Projects)
	}
	view := got.Projects[0]
	if view.Tasks != 1 || view.Stagnant != 1 || view.Stalled != 0 || view.Active != 0 {
		t.Fatalf("task stagnation was conflated with session stall: %+v", view)
	}
}

func TestClusterRejectsDuplicateCanonicalProjectPath(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(root, "human:test")
	h := s.Handler()
	call(t, h, "POST", "/api/cluster", clusterJSON(t, clusterReq{Path: root}), &clusterResp{})
	call(t, h, "POST", "/api/cluster/projects", clusterJSON(t, clusterProjectReq{ClusterPath: root, Path: project}), &clusterResp{})
	if code, body := raw(h, "POST", "/api/cluster/projects", clusterJSON(t, clusterProjectReq{
		ClusterPath: root, Path: filepath.Join(project, "."), Name: "duplicate",
	})); code != http.StatusConflict || !strings.Contains(body, "already registered") {
		t.Fatalf("duplicate canonical path = %d %s", code, body)
	}
}

func TestClusterRemoveOnlyChangesManifest(t *testing.T) {
	clusterRoot := t.TempDir()
	project := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := repo.Init(project, "KEEP"); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, filepath.Join(project, repo.CarbonDirName))

	s := New(clusterRoot, "human:test")
	h := s.Handler()
	call(t, h, "POST", "/api/cluster", clusterJSON(t, clusterReq{Path: clusterRoot}), &clusterResp{})
	var registered clusterResp
	call(t, h, "POST", "/api/cluster/projects", clusterJSON(t, clusterProjectReq{ClusterPath: clusterRoot, Path: project}), &registered)
	if len(registered.Projects) != 1 {
		t.Fatalf("registered = %+v", registered)
	}
	var removed clusterResp
	call(t, h, "DELETE", "/api/cluster/projects/"+registered.Projects[0].ID+"?path="+url.QueryEscape(clusterRoot), "", &removed)
	if len(removed.Projects) != 0 {
		t.Fatalf("removed cluster = %+v", removed)
	}
	if after := snapshotTree(t, filepath.Join(project, repo.CarbonDirName)); !reflect.DeepEqual(before, after) {
		t.Fatal("removing a project changed its .carbon bytes")
	}
}

func TestClusterReportsOfflineProject(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(t.TempDir(), "offline")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(root, "human:test")
	h := s.Handler()
	call(t, h, "POST", "/api/cluster", clusterJSON(t, clusterReq{Path: root}), &clusterResp{})
	call(t, h, "POST", "/api/cluster/projects", clusterJSON(t, clusterProjectReq{ClusterPath: root, Path: project}), &clusterResp{})
	if err := os.Remove(project); err != nil {
		t.Fatal(err)
	}

	var got clusterResp
	call(t, h, "GET", clusterURL(root), "", &got)
	if len(got.Projects) != 1 || !got.Projects[0].Offline || got.Projects[0].Initialized {
		t.Fatalf("offline project = %+v", got.Projects)
	}
}

func TestClusterRejectsMalformedManifestAndOversizeRequest(t *testing.T) {
	root := t.TempDir()
	s := New(root, "human:test")
	h := s.Handler()
	if err := os.WriteFile(filepath.Join(root, cluster.ManifestFilename), []byte(`{"version":1,"name":"bad","projects":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, body := raw(h, "GET", clusterURL(root), ""); code != http.StatusBadRequest || !strings.Contains(body, "invalid cluster manifest") {
		t.Fatalf("malformed manifest = %d %s", code, body)
	}

	oversized := clusterJSON(t, clusterReq{Path: root, Name: strings.Repeat("x", int(maxJSONBodyBytes))})
	if code, body := raw(h, "POST", "/api/cluster", oversized); code != http.StatusRequestEntityTooLarge || !strings.Contains(body, "exceeds") {
		t.Fatalf("oversized cluster request = %d %s", code, body)
	}
}

func TestClusterRejectsSymlinkedManifest(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"secret":"must-not-read"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, cluster.ManifestFilename)); err != nil {
		t.Skipf("manifest symlink unavailable: %v", err)
	}

	s := New(root, "human:test")
	if code, body := raw(s.Handler(), "GET", clusterURL(root), ""); code != http.StatusBadRequest || strings.Contains(body, "must-not-read") {
		t.Fatalf("symlinked manifest = %d %s", code, body)
	}
}

func clusterURL(root string) string {
	return "/api/cluster?path=" + url.QueryEscape(root)
}

func clusterJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func snapshotTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[rel] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func writeClusterTask(t *testing.T, root, id string) {
	writeClusterTaskAt(t, root, id, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
}

func writeClusterTaskAt(t *testing.T, root, id string, at time.Time) {
	t.Helper()
	body := "---\n" +
		"id: " + id + "\n" +
		"title: same task id\n" +
		"status: backlog\n" +
		"provenance:\n" +
		"  - {who: human:test, at: " + at.UTC().Format(time.RFC3339) + ", did: created}\n" +
		"---\n"
	if err := os.WriteFile(filepath.Join(root, repo.CarbonDirName, "tasks", id+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
