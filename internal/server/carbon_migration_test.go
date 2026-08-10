package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"carbon/internal/backup"
	"carbon/internal/cluster"
	"carbon/internal/home"
	"carbon/internal/store"
)

func legacyImportRoots(t *testing.T) (target, legacy string) {
	t.Helper()
	target = t.TempDir()
	legacy = t.TempDir()
	source := t.TempDir()
	if _, err := cluster.Ensure(legacy, "Legacy import", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cluster.AddProject(legacy, source, "Source"); err != nil {
		t.Fatal(err)
	}
	return target, legacy
}

func applyReviewedLegacyImport(t *testing.T, target, legacy string) struct {
	SnapshotID     string `json:"snapshotId"`
	SnapshotTiming string `json:"snapshotTiming"`
} {
	t.Helper()
	plan, err := home.PlanLegacyImport(target, legacy)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"legacyCluster": legacy, "expectedDigest": plan.ReviewDigest})
	if err != nil {
		t.Fatal(err)
	}
	s := NewWithScope("human:test", ScopeDefaults{Home: target, HomeByDefault: true})
	code, rawBody := raw(s.Handler(), http.MethodPost, "/api/home/migrations/legacy/apply", string(body))
	if code != http.StatusOK {
		t.Fatalf("legacy apply = %d %s", code, rawBody)
	}
	var out struct {
		SnapshotID     string `json:"snapshotId"`
		SnapshotTiming string `json:"snapshotTiming"`
	}
	if err := json.Unmarshal([]byte(rawBody), &out); err != nil {
		t.Fatal(err)
	}
	if out.SnapshotID == "" {
		t.Fatalf("missing snapshot receipt: %s", rawBody)
	}
	h, err := home.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := localBackupRepository(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Verify(context.Background(), backup.Snapshot{ID: out.SnapshotID}); err != nil {
		t.Fatalf("migration snapshot %s did not verify: %v", out.SnapshotID, err)
	}
	return out
}

func TestLegacyMigrationFreshHomeCreatesPostImportBaseline(t *testing.T) {
	target, legacy := legacyImportRoots(t)
	out := applyReviewedLegacyImport(t, target, legacy)
	if out.SnapshotTiming != "post-import" {
		t.Fatalf("fresh migration snapshot timing = %q, want post-import", out.SnapshotTiming)
	}
}

func TestLegacyMigrationExistingHomeCreatesPreImportSnapshot(t *testing.T) {
	target, legacy := legacyImportRoots(t)
	if _, err := home.Ensure(target); err != nil {
		t.Fatal(err)
	}
	out := applyReviewedLegacyImport(t, target, legacy)
	if out.SnapshotTiming != "pre-import" {
		t.Fatalf("existing migration snapshot timing = %q, want pre-import", out.SnapshotTiming)
	}
}

func TestHomeWorkerStatsProjectOnlyUsesOwnerCluster(t *testing.T) {
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	clusterOne, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "One", Prefix: "ONE"})
	if err != nil {
		t.Fatal(err)
	}
	clusterTwo, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Two", Prefix: "TWO"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := home.AddProject(homeRoot, clusterOne.ID, home.AddProjectRequest{Name: "One app", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	rootOne, err := home.ClusterDataRoot(homeRoot, clusterOne.ID)
	if err != nil {
		t.Fatal(err)
	}
	rootTwo, err := home.ClusterDataRoot(homeRoot, clusterTwo.ID)
	if err != nil {
		t.Fatal(err)
	}
	createStatsTask(t, rootOne, project.ID, "agent:project")
	createStatsTask(t, rootOne, "", "agent:one-shared")
	createStatsTask(t, rootTwo, "", "agent:two-shared")

	s := NewWithScope("human:test", ScopeDefaults{Home: homeRoot, HomeByDefault: true})
	report, err := s.homeWorkerStats(context.Background(), homeRoot, "", project.ID)
	if err != nil {
		t.Fatal(err)
	}
	workers := make(map[string]bool, len(report.Workers))
	for _, worker := range report.Workers {
		workers[worker.Actor] = true
	}
	if !workers["agent:project"] || !workers["agent:one-shared"] || workers["agent:two-shared"] {
		t.Fatalf("project stats crossed cluster boundary: %+v", report.Workers)
	}
	if _, err := s.homeWorkerStats(context.Background(), homeRoot, clusterTwo.ID, project.ID); err == nil {
		t.Fatal("project filter paired with the wrong cluster should fail")
	}
}

func createStatsTask(t *testing.T, root, projectID, actor string) {
	t.Helper()
	st := store.New(root)
	doc, err := st.Create(store.Draft{Title: actor, ProjectID: projectID, ProjectIDSet: true}, "human:test", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	doc.SetAssignee(actor)
	if err := st.Save(doc); err != nil {
		t.Fatal(err)
	}
}

func TestCarbonConfigExposesAndValidatesTrashRetention(t *testing.T) {
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	cluster, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Config", Prefix: "CFG"})
	if err != nil {
		t.Fatal(err)
	}
	s := NewWithScope("human:test", ScopeDefaults{Home: homeRoot, ClusterID: cluster.ID, HomeByDefault: true})
	h := s.Handler()
	var before configResp
	call(t, h, http.MethodGet, "/api/config", "", &before)
	if before.TrashRetentionDays != 30 {
		t.Fatalf("default retention = %d, want 30", before.TrashRetentionDays)
	}
	var changed configResp
	call(t, h, http.MethodPost, "/api/config", `{"trashRetentionDays":14}`, &changed)
	if changed.TrashRetentionDays != 14 {
		t.Fatalf("changed retention = %d, want 14", changed.TrashRetentionDays)
	}
	if code, body := raw(h, http.MethodPost, "/api/config", `{"trashRetentionDays":0}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid retention = %d %s, want 422", code, body)
	}
}

func TestProjectScopedTrashEmptyOnlyPurgesOwnProjectUnlessSharedIsExplicit(t *testing.T) {
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	cluster, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Trash", Prefix: "TRS"})
	if err != nil {
		t.Fatal(err)
	}
	projectOne, err := home.AddProject(homeRoot, cluster.ID, home.AddProjectRequest{Name: "One", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	projectTwo, err := home.AddProject(homeRoot, cluster.ID, home.AddProjectRequest{Name: "Two", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	dataRoot, err := home.ClusterDataRoot(homeRoot, cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(dataRoot)
	createAndTrash := func(projectID string) string {
		t.Helper()
		doc, err := st.Create(store.Draft{Title: projectID, ProjectID: projectID, ProjectIDSet: true}, "human:test", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.TrashTasks(context.Background(), "human:test", []string{doc.Task.ID}, "test", nil, time.Now()); err != nil {
			t.Fatal(err)
		}
		return doc.Task.ID
	}
	oneID := createAndTrash(projectOne.ID)
	twoID := createAndTrash(projectTwo.ID)
	sharedID := createAndTrash("")
	s := NewWithScope("human:test", ScopeDefaults{Home: homeRoot, ClusterID: cluster.ID, ProjectID: projectOne.ID, HomeByDefault: true})
	h := s.Handler()
	if code, body := raw(h, http.MethodDelete, "/api/trash?confirm=true", ""); code != http.StatusOK {
		t.Fatalf("project empty = %d %s", code, body)
	}
	if _, err := st.GetTrash(oneID); !errors.Is(err, store.ErrTrashNotFound) {
		t.Fatalf("own project trash remains: %v", err)
	}
	if _, err := st.GetTrash(twoID); err != nil {
		t.Fatalf("other project trash was purged: %v", err)
	}
	if _, err := st.GetTrash(sharedID); err != nil {
		t.Fatalf("shared trash was purged without explicit acknowledgement: %v", err)
	}
	if code, body := raw(h, http.MethodDelete, "/api/trash?confirm=true&include_cluster=true", ""); code != http.StatusOK {
		t.Fatalf("project empty shared = %d %s", code, body)
	}
	if _, err := st.GetTrash(sharedID); !errors.Is(err, store.ErrTrashNotFound) {
		t.Fatalf("explicit shared purge did not remove shared trash: %v", err)
	}
	if _, err := st.GetTrash(twoID); err != nil {
		t.Fatalf("other project trash was purged with shared acknowledgement: %v", err)
	}
}

func TestClusterScopedConnectWithoutConfigProjectReturnsManualGuide(t *testing.T) {
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	cluster, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Connect", Prefix: "CON"})
	if err != nil {
		t.Fatal(err)
	}
	s := NewWithScope("human:test", ScopeDefaults{Home: homeRoot, ClusterID: cluster.ID, HomeByDefault: true})
	code, body := raw(s.Handler(), http.MethodPost, "/api/connect/codex", `{}`)
	if code != http.StatusOK {
		t.Fatalf("cluster connect without config project = %d %s", code, body)
	}
	var out struct {
		Connected bool `json:"connected"`
		Manual    bool `json:"manual"`
		Guide     struct {
			Config string `json:"config"`
		} `json:"guide"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Connected || !out.Manual {
		t.Fatalf("cluster connect response = %s", body)
	}
	if !strings.Contains(out.Guide.Config, "--cluster") || strings.Contains(out.Guide.Config, "--project") {
		t.Fatalf("cluster guide must omit project binding: %s", out.Guide.Config)
	}
}
