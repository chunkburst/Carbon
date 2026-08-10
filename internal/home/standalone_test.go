package home

import (
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"carbon/internal/config"
	"carbon/internal/repo"
)

func TestManifestV1ReadUnchangedThenStandaloneUpgradePreservesNestedProjects(t *testing.T) {
	useHomeTestCache(t)
	main := t.TempDir()
	source := t.TempDir()
	if _, err := Ensure(main); err != nil {
		t.Fatal(err)
	}
	canonical, fingerprint, err := observeSource(source)
	if err != nil {
		t.Fatal(err)
	}
	clusterID := "cluster_11111111111111111111111111111111"
	nestedID := "project_22222222222222222222222222222222"
	timestamp := "2026-01-01T00:00:00Z"
	v1 := struct {
		Version   int       `json:"version"`
		ID        string    `json:"id"`
		CreatedAt string    `json:"createdAt"`
		Clusters  []Cluster `json:"clusters"`
	}{
		Version:   legacyManifestVersion,
		ID:        "home_00000000000000000000000000000000",
		CreatedAt: timestamp,
		Clusters: []Cluster{{
			ID: clusterID, Name: "Legacy cluster", Prefix: "LEG", DataPath: path.Join(clusterDataDirectory, clusterID), CreatedAt: timestamp,
			Projects: []Project{{
				ID: nestedID, Name: "Legacy nested", Kind: ProjectGeneric, CreatedAt: timestamp,
				Source: Source{Path: canonical, Aliases: []string{canonical}, Fingerprint: fingerprint, LastSeen: timestamp},
			}},
		}},
	}
	raw, err := json.Marshal(v1)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	filename := filepath.Join(main, CarbonDirName, ManifestFilename)
	if err := os.WriteFile(filename, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	h, err := Open(main)
	if err != nil {
		t.Fatal(err)
	}
	before, err := h.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if before.Version != legacyManifestVersion || before.Projects != nil || len(before.Clusters) != 1 || before.Clusters[0].Projects[0].ID != nestedID {
		t.Fatalf("v1 manifest decode = %#v", before)
	}
	persisted, err := os.ReadFile(filename)
	if err != nil || string(persisted) != string(raw) {
		t.Fatalf("v1 read rewrote manifest: got=%q err=%v", persisted, err)
	}

	standalone, err := AddStandaloneProject(main, AddProjectRequest{Name: "Standalone", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	after, err := h.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != Version || len(after.Projects) != 1 || after.Projects[0].ID != standalone.ID {
		t.Fatalf("upgraded manifest = %#v", after)
	}
	if len(after.Clusters) != 1 || len(after.Clusters[0].Projects) != 1 || after.Clusters[0].Projects[0].ID != nestedID || after.Clusters[0].DataPath != path.Join(clusterDataDirectory, clusterID) {
		t.Fatalf("standalone upgrade moved nested project data: %#v", after.Clusters)
	}
}

func TestManifestV2StandaloneRoundTrip(t *testing.T) {
	useHomeTestCache(t)
	main := t.TempDir()
	project, err := AddStandaloneProject(main, AddProjectRequest{Name: "Round trip", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(main, CarbonDirName, ManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"version": 2`) || !strings.Contains(string(raw), `"projects": [`) {
		t.Fatalf("v2 manifest is missing standalone shape: %s", raw)
	}
	decoded, err := decodeManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Version != Version || len(decoded.Projects) != 1 || decoded.Projects[0].ID != project.ID || decoded.Clusters == nil {
		t.Fatalf("v2 round trip = %#v", decoded)
	}
}

func TestStandaloneProjectCRUDRelinkAndRootIsolation(t *testing.T) {
	useHomeTestCache(t)
	main := t.TempDir()
	source := t.TempDir()
	project, err := AddStandaloneProject(main, AddProjectRequest{
		Name: "Desktop", Slug: "desktop", Kind: ProjectPC, SourcePath: source,
	})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := ListProjects(main)
	if err != nil || len(listed) != 1 || listed[0].ID != project.ID {
		t.Fatalf("standalone list = %#v, %v", listed, err)
	}
	root, err := ProjectDataRoot(main, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := filepath.Join(main, CarbonDirName, projectDataDirectory, project.ID)
	if !samePath(root, wantRoot) {
		t.Fatalf("standalone root = %q, want %q", root, wantRoot)
	}
	cfg, err := config.Load(filepath.Join(root, repo.CarbonDirName, "config.yaml"))
	if err != nil || cfg.ProjectID != project.ID {
		t.Fatalf("standalone config = %#v, %v", cfg, err)
	}
	name := "Renamed desktop"
	kind := ProjectWeb
	updated, err := UpdateStandaloneProject(main, project.ID, UpdateProjectRequest{Name: &name, Kind: &kind})
	if err != nil || updated.ID != project.ID || updated.Name != name || updated.Kind != kind {
		t.Fatalf("standalone update = %#v, %v", updated, err)
	}
	newSource := t.TempDir()
	relinked, err := RelinkStandaloneProject(main, project.ID, newSource)
	if err != nil || !samePath(relinked.Source.Path, newSource) || !containsPath(relinked.Source.Aliases, source) || !containsPath(relinked.Source.Aliases, newSource) {
		t.Fatalf("standalone relink = %#v, %v", relinked, err)
	}
	resolved, err := ResolveProject(main, ResolveProjectRequest{ProjectID: project.ID})
	if err != nil || !resolved.Standalone || resolved.Cluster.ID != "" || resolved.Project.ID != project.ID || !samePath(resolved.DataRoot, root) {
		t.Fatalf("unscoped standalone resolution = %#v, %v", resolved, err)
	}

	cluster, err := CreateCluster(main, CreateClusterRequest{Name: "Shared"})
	if err != nil {
		t.Fatal(err)
	}
	nested, err := AddProject(main, cluster.ID, AddProjectRequest{Name: "Nested", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	nestedRoot, err := ProjectDataRoot(main, nested.ID)
	if err != nil {
		t.Fatal(err)
	}
	clusterRoot, err := ClusterDataRoot(main, cluster.ID)
	if err != nil || !samePath(nestedRoot, clusterRoot) || samePath(root, nestedRoot) {
		t.Fatalf("root isolation standalone=%q nested=%q cluster=%q err=%v", root, nestedRoot, clusterRoot, err)
	}
	listed, err = ListProjects(main)
	if err != nil || len(listed) != 1 || listed[0].ID != project.ID {
		t.Fatalf("cluster changed standalone list: %#v, %v", listed, err)
	}
}

func TestStandaloneProjectReferencesFailClosedAndRejectReparseRoot(t *testing.T) {
	useHomeTestCache(t)
	main := t.TempDir()
	source := t.TempDir()
	standalone, err := AddStandaloneProject(main, AddProjectRequest{Name: "Same Name", Slug: "standalone", SourcePath: source})
	if err != nil {
		t.Fatal(err)
	}
	cluster, err := CreateCluster(main, CreateClusterRequest{Name: "Shared"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AddProject(main, cluster.ID, AddProjectRequest{Name: "Same Name", Slug: "nested", SourcePath: source}); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveProject(main, ResolveProjectRequest{SourcePath: source}); !errors.Is(err, ErrAmbiguousProject) {
		t.Fatalf("unscoped duplicate source = %v, want ErrAmbiguousProject", err)
	}
	if _, err := ResolveProjectMetadata(main, "", "same name"); !errors.Is(err, ErrAmbiguousProjectReference) {
		t.Fatalf("unscoped duplicate name = %v, want ErrAmbiguousProjectReference", err)
	}
	if _, err := AddStandaloneProject(main, AddProjectRequest{Name: "Collision", Slug: "nested", SourcePath: t.TempDir()}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("cross-namespace slug collision = %v, want ErrInvalidManifest", err)
	}
	if _, err := standaloneProjectDataPath("../escape"); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("traversal project id = %v, want ErrInvalidManifest", err)
	}

	root, err := ProjectDataRoot(main, standalone.ID)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if _, err := ProjectDataRoot(main, standalone.ID); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("reparse standalone root = %v, want ErrUnsafePath", err)
	}
}

func TestManifestRejectsDuplicateProjectIDAcrossStandaloneAndCluster(t *testing.T) {
	useHomeTestCache(t)
	main := t.TempDir()
	standalone, err := AddStandaloneProject(main, AddProjectRequest{Name: "Standalone", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cluster, err := CreateCluster(main, CreateClusterRequest{Name: "Cluster"})
	if err != nil {
		t.Fatal(err)
	}
	nested, err := AddProject(main, cluster.ID, AddProjectRequest{Name: "Nested", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	h, err := Open(main)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := h.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.Projects[0].ID = nested.ID
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeManifest(raw); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("cross-namespace duplicate project id = %v, want ErrInvalidManifest", err)
	}
	if standalone.ID == nested.ID {
		t.Fatal("fixture unexpectedly generated duplicate stable IDs")
	}
}

func TestDoctorRestoresStandaloneProjectStore(t *testing.T) {
	useHomeTestCache(t)
	main := t.TempDir()
	project, err := AddStandaloneProject(main, AddProjectRequest{Name: "Doctor", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	root, err := ProjectDataRoot(main, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	dryRun, err := Doctor(main, DoctorOptions{})
	if err != nil || !dryRun.Changed || !doctorRepairContainsProject(dryRun.Repairs, project.ID) {
		t.Fatalf("standalone doctor dry run = %#v, %v", dryRun, err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("doctor dry run recreated standalone root: %v", err)
	}
	applied, err := Doctor(main, DoctorOptions{Apply: true})
	if err != nil || !applied.Applied {
		t.Fatalf("standalone doctor apply = %#v, %v", applied, err)
	}
	cfg, err := config.Load(filepath.Join(root, repo.CarbonDirName, "config.yaml"))
	if err != nil || cfg.ProjectID != project.ID {
		t.Fatalf("doctor standalone config = %#v, %v", cfg, err)
	}
}

func TestDetachProjectCopiesStoreAndPreservesClusterRoot(t *testing.T) {
	useHomeTestCache(t)
	main := t.TempDir()
	cluster, err := CreateCluster(main, CreateClusterRequest{Name: "Detach", Prefix: "DET"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := AddProject(main, cluster.ID, AddProjectRequest{Name: "Only", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot, err := ClusterDataRoot(main, cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(sourceRoot, repo.CarbonDirName, "tasks", "DET-1.md")
	if err := os.WriteFile(taskPath, []byte("copied task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := DetachProject(main, cluster.ID, project.ID, DetachProjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Project.ID != project.ID || result.SourceProjectCount != 1 || result.SharedStoreCopy || !samePath(result.SourceDataRoot, sourceRoot) {
		t.Fatalf("detach result = %#v", result)
	}
	if samePath(result.DataRoot, sourceRoot) {
		t.Fatalf("detach reused shared root %q", sourceRoot)
	}
	if data, err := os.ReadFile(taskPath); err != nil || string(data) != "copied task\n" {
		t.Fatalf("detach changed old shared root: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(result.DataRoot, repo.CarbonDirName, "tasks", "DET-1.md")); err != nil || string(data) != "copied task\n" {
		t.Fatalf("detach did not copy shared store: %q, %v", data, err)
	}
	cfg, err := config.Load(filepath.Join(result.DataRoot, repo.CarbonDirName, "config.yaml"))
	if err != nil || cfg.ProjectID != project.ID {
		t.Fatalf("detached config = %#v, %v", cfg, err)
	}
	manifest, err := Open(main)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manifest.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Projects) != 1 || snapshot.Projects[0].ID != project.ID || len(snapshot.Clusters) != 1 || len(snapshot.Clusters[0].Projects) != 0 {
		t.Fatalf("detach manifest = %#v", snapshot)
	}
	resolved, err := ResolveProject(main, ResolveProjectRequest{ProjectID: project.ID})
	if err != nil || !resolved.Standalone || !samePath(resolved.DataRoot, result.DataRoot) {
		t.Fatalf("detached resolve = %#v, %v", resolved, err)
	}
	raw, err := os.ReadFile(result.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var receipt DetachProjectReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "completed" || receipt.ProjectID != project.ID || receipt.ClusterID != cluster.ID || receipt.SourceDigest == "" {
		t.Fatalf("detach receipt = %#v", receipt)
	}
}

func TestDetachProjectRequiresExplicitSharedStoreReview(t *testing.T) {
	useHomeTestCache(t)
	main := t.TempDir()
	cluster, err := CreateCluster(main, CreateClusterRequest{Name: "Shared"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := AddProject(main, cluster.ID, AddProjectRequest{Name: "First", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	second, err := AddProject(main, cluster.ID, AddProjectRequest{Name: "Second", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DetachProject(main, cluster.ID, first.ID, DetachProjectOptions{}); !errors.Is(err, ErrDetachRequiresReview) {
		t.Fatalf("unreviewed shared detach = %v, want ErrDetachRequiresReview", err)
	}
	projects, err := ListProjects(main)
	if err != nil || len(projects) != 0 {
		t.Fatalf("unreviewed detach changed standalone projects: %#v, %v", projects, err)
	}
	result, err := MoveProjectToStandalone(main, cluster.ID, first.ID, DetachProjectOptions{
		AllowSharedStoreCopy: true,
		Reason:               "Reviewed shared-store snapshot before detaching First",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.SharedStoreCopy || result.SourceProjectCount != 2 || result.Receipt.Reason == "" {
		t.Fatalf("reviewed shared detach = %#v", result)
	}
	metadata, err := ResolveProjectMetadata(main, cluster.ID, second.ID)
	if err != nil || metadata.Standalone || metadata.Project.ID != second.ID {
		t.Fatalf("peer project changed by detach = %#v, %v", metadata, err)
	}
	if _, err := os.Stat(result.SourceDataRoot); err != nil {
		t.Fatalf("detach removed shared source root: %v", err)
	}
}

func doctorRepairContainsProject(repairs []DoctorRepair, projectID string) bool {
	for _, repair := range repairs {
		if repair.ProjectID == projectID {
			return true
		}
	}
	return false
}
