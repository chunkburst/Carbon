package home

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"carbon/internal/cluster"
	"carbon/internal/config"
	"carbon/internal/repo"
)

func TestClusterProjectsShareStoreButNotIdentity(t *testing.T) {
	useHomeTestCache(t)
	main := t.TempDir()
	source := t.TempDir()
	cluster, err := CreateCluster(main, CreateClusterRequest{Name: "App", Prefix: "app"})
	if err != nil {
		t.Fatal(err)
	}
	pc, err := AddProject(main, cluster.ID, AddProjectRequest{Name: "Desktop", Kind: ProjectPC, SourcePath: source})
	if err != nil {
		t.Fatal(err)
	}
	mobile, err := AddProject(main, cluster.ID, AddProjectRequest{Name: "Phone", Kind: ProjectMobile, SourcePath: source})
	if err != nil {
		t.Fatal(err)
	}
	if pc.ID == mobile.ID || pc.Source.Path != mobile.Source.Path {
		t.Fatalf("same source should produce independent project identities: %#v %#v", pc, mobile)
	}
	if _, err := ResolveProject(main, ResolveProjectRequest{ClusterID: cluster.ID, SourcePath: source}); !errors.Is(err, ErrAmbiguousProject) {
		t.Fatalf("source resolution = %v, want ErrAmbiguousProject", err)
	}
	root, err := ClusterDataRoot(main, cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(main, CarbonDirName, "clusters", cluster.ID); !samePath(root, want) {
		t.Fatalf("data root = %q, want %q", root, want)
	}
	cfg, err := config.Load(filepath.Join(root, repo.CarbonDirName, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Prefix != "APP" || cfg.ProjectID != "" {
		t.Fatalf("shared store config = %+v, want APP and empty project id", cfg)
	}
	for _, unexpected := range []string{".gitignore", "AGENTS.md", "CLAUDE.md", path.Join(repo.CarbonDirName, "WORKFLOW.md")} {
		if _, err := os.Lstat(filepath.Join(root, unexpected)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("central data root unexpectedly has %s: %v", unexpected, err)
		}
	}
}

func TestUpdateProjectChangesOnlyDisplayMetadata(t *testing.T) {
	useHomeTestCache(t)
	main := t.TempDir()
	source := t.TempDir()
	cluster, err := CreateCluster(main, CreateClusterRequest{Name: "App"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := AddProject(main, cluster.ID, AddProjectRequest{Name: "Old", Kind: ProjectPC, SourcePath: source})
	if err != nil {
		t.Fatal(err)
	}
	name := "New display name"
	kind := ProjectWeb
	updated, err := UpdateProject(main, cluster.ID, project.ID, UpdateProjectRequest{Name: &name, Kind: &kind})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != project.ID || updated.Name != name || updated.Kind != ProjectWeb || updated.Source.Path != project.Source.Path || updated.Source.Fingerprint != project.Source.Fingerprint || !slicesEqual(updated.Source.Aliases, project.Source.Aliases) {
		t.Fatalf("unexpected project update: before=%#v after=%#v", project, updated)
	}
	invalid := ProjectKind("not valid!")
	if _, err := UpdateProject(main, cluster.ID, project.ID, UpdateProjectRequest{Kind: &invalid}); !errors.Is(err, ErrInvalidProjectKind) {
		t.Fatalf("invalid kind update = %v, want ErrInvalidProjectKind", err)
	}
	resolved, err := ResolveProject(main, ResolveProjectRequest{ClusterID: cluster.ID, ProjectID: project.ID})
	if err != nil || resolved.Project.Name != name || resolved.Project.Kind != ProjectWeb {
		t.Fatalf("invalid update changed project: %#v, %v", resolved.Project, err)
	}
}

func TestMachineSlugsResolveSafelyAndRetainHistoricalAliases(t *testing.T) {
	useHomeTestCache(t)
	main := t.TempDir()
	first, err := CreateCluster(main, CreateClusterRequest{
		Name: "实验集群", Slug: "lab", Description: "initial description",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateCluster(main, CreateClusterRequest{Name: "lab", Slug: "other-lab"})
	if err != nil {
		t.Fatal(err)
	}
	// A machine slug wins over a display-name coincidence, and exact IDs remain
	// canonical even if another display name happens to equal that ID.
	if resolved, err := ResolveCluster(main, "LAB"); err != nil || resolved.ID != first.ID {
		t.Fatalf("slug resolution = %#v, %v", resolved, err)
	}
	if resolved, err := ResolveCluster(main, second.ID); err != nil || resolved.ID != second.ID {
		t.Fatalf("stable id resolution = %#v, %v", resolved, err)
	}
	newSlug := "experiment"
	updated, err := UpdateCluster(main, first.ID, UpdateClusterRequest{Slug: &newSlug})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Slug != newSlug || len(updated.SlugAliases) != 1 || updated.SlugAliases[0] != "lab" {
		t.Fatalf("cluster slug history = %#v", updated)
	}
	if resolved, err := ResolveCluster(main, "Lab"); err != nil || resolved.ID != first.ID {
		t.Fatalf("historical cluster alias = %#v, %v", resolved, err)
	}
	beforeCollision, err := os.ReadDir(filepath.Join(main, CarbonDirName, clusterDataDirectory))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateCluster(main, CreateClusterRequest{Name: "collision", Slug: "LAB"}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("case-insensitive cluster collision = %v, want ErrInvalidManifest", err)
	}
	afterCollision, err := os.ReadDir(filepath.Join(main, CarbonDirName, clusterDataDirectory))
	if err != nil || len(afterCollision) != len(beforeCollision) {
		t.Fatalf("invalid cluster creation left a data root: before=%d after=%d err=%v", len(beforeCollision), len(afterCollision), err)
	}

	projectSource := t.TempDir()
	project, err := AddProject(main, updated.ID, AddProjectRequest{
		Name: "桌面端", Slug: "desktop", Description: "desktop client", SourcePath: projectSource,
	})
	if err != nil {
		t.Fatal(err)
	}
	projectSlug := "windows"
	project, err = UpdateProject(main, updated.ID, project.ID, UpdateProjectRequest{Slug: &projectSlug})
	if err != nil {
		t.Fatal(err)
	}
	if project.Slug != projectSlug || len(project.SlugAliases) != 1 || project.SlugAliases[0] != "desktop" {
		t.Fatalf("project slug history = %#v", project)
	}
	metadata, err := ResolveProjectMetadata(main, "EXPERIMENT", "Desktop")
	if err != nil || metadata.Cluster.ID != updated.ID || metadata.Project.ID != project.ID {
		t.Fatalf("project metadata alias resolution = %#v, %v", metadata, err)
	}
	if _, err := AddProject(main, updated.ID, AddProjectRequest{Name: "Duplicate", Slug: "WINDOWS", SourcePath: t.TempDir()}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("case-insensitive project collision = %v, want ErrInvalidManifest", err)
	}
	if _, err := AddProject(main, updated.ID, AddProjectRequest{Name: "bad slug", Slug: "not a safe slug", SourcePath: t.TempDir()}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("unsafe project slug = %v, want ErrInvalidManifest", err)
	}
}

func TestDisplayNameResolutionFailsWhenAmbiguous(t *testing.T) {
	useHomeTestCache(t)
	main := t.TempDir()
	first, err := CreateCluster(main, CreateClusterRequest{Name: "same cluster", Slug: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateCluster(main, CreateClusterRequest{Name: "SAME CLUSTER", Slug: "two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCluster(main, "same cluster"); !errors.Is(err, ErrAmbiguousCluster) {
		t.Fatalf("ambiguous cluster display name = %v, want ErrAmbiguousCluster", err)
	}
	if _, err := AddProject(main, first.ID, AddProjectRequest{Name: "same project", Slug: "one-project", SourcePath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddProject(main, first.ID, AddProjectRequest{Name: "SAME PROJECT", Slug: "two-project", SourcePath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveProjectMetadata(main, first.ID, "same project"); !errors.Is(err, ErrAmbiguousProjectReference) {
		t.Fatalf("ambiguous project display name = %v, want ErrAmbiguousProjectReference", err)
	}
}

func TestSlugFieldsRemainOptionalForExistingV1Manifest(t *testing.T) {
	useHomeTestCache(t)
	main := t.TempDir()
	if _, err := Ensure(main); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	canonical, fingerprint, err := observeSource(source)
	if err != nil {
		t.Fatal(err)
	}
	clusterID := "cluster_11111111111111111111111111111111"
	projectID := "project_22222222222222222222222222222222"
	manifest := Manifest{
		Version: Version, ID: "home_00000000000000000000000000000000", CreatedAt: "2026-01-01T00:00:00Z",
		Clusters: []Cluster{{
			ID: clusterID, Name: "Legacy cluster", Prefix: "LEG", DataPath: path.Join(clusterDataDirectory, clusterID), CreatedAt: "2026-01-01T00:00:00Z",
			Projects: []Project{{
				ID: projectID, Name: "Legacy project", Kind: ProjectGeneric, CreatedAt: "2026-01-01T00:00:00Z",
				Source: Source{Path: canonical, Aliases: []string{canonical}, Fingerprint: fingerprint, LastSeen: "2026-01-01T00:00:00Z"},
			}},
		}},
	}
	if err := writeManifest(filepath.Join(main, CarbonDirName), manifest); err != nil {
		t.Fatal(err)
	}
	cluster, err := ResolveCluster(main, "legacy cluster")
	if err != nil || cluster.ID != clusterID || cluster.Slug != "" || cluster.SlugAliases != nil {
		t.Fatalf("legacy cluster compatibility = %#v, %v", cluster, err)
	}
	project, err := ResolveProjectMetadata(main, clusterID, "legacy project")
	if err != nil || project.Project.ID != projectID || project.Project.Slug != "" || project.Project.SlugAliases != nil {
		t.Fatalf("legacy project compatibility = %#v, %v", project, err)
	}
}

func TestClustersAreIsolatedAndMoveRecognitionUsesFingerprint(t *testing.T) {
	useHomeTestCache(t)
	main := t.TempDir()
	first, err := CreateCluster(main, CreateClusterRequest{Name: "One"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateCluster(main, CreateClusterRequest{Name: "Two"})
	if err != nil {
		t.Fatal(err)
	}
	firstRoot, _ := ClusterDataRoot(main, first.ID)
	secondRoot, _ := ClusterDataRoot(main, second.ID)
	if samePath(firstRoot, secondRoot) {
		t.Fatalf("clusters share data root %q", firstRoot)
	}

	sourceParent := t.TempDir()
	source := filepath.Join(sourceParent, "before")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := AddProject(main, first.ID, AddProjectRequest{SourcePath: source})
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(sourceParent, "after")
	if err := os.Rename(source, moved); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveProject(main, ResolveProjectRequest{ClusterID: first.ID, SourcePath: moved})
	if err != nil {
		t.Fatalf("resolve moved project: %v", err)
	}
	if resolved.Project.ID != project.ID || !samePath(resolved.SourcePath, moved) || resolved.Offline {
		t.Fatalf("moved resolution = %#v", resolved)
	}
	manifest, err := Open(main)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manifest.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(snapshot.Clusters[0].Projects[0].Source.Aliases, moved) {
		t.Fatalf("moved source not retained as alias: %#v", snapshot.Clusters[0].Projects[0].Source)
	}
}

func TestManifestFailsClosedForFutureAndDuplicate(t *testing.T) {
	useHomeTestCache(t)
	root := t.TempDir()
	if _, err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, CarbonDirName, ManifestFilename)
	if err := os.WriteFile(manifestPath, []byte(`{"version":99,"id":"home_00000000000000000000000000000000","createdAt":"2026-01-01T00:00:00Z","clusters":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); !errors.Is(err, ErrFutureVersion) {
		t.Fatalf("future manifest = %v, want ErrFutureVersion", err)
	}
	valid := Manifest{Version: Version, ID: "home_00000000000000000000000000000000", CreatedAt: "2026-01-01T00:00:00Z", Clusters: []Cluster{}}
	if err := writeManifest(filepath.Join(root, CarbonDirName), valid); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := strings.Replace(string(data), `"clusters": []`, `"clusters": [], "clusters": []`, 1)
	if err := os.WriteFile(manifestPath, []byte(duplicate), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("duplicate JSON key = %v, want ErrInvalidManifest", err)
	}
}

func TestConcurrentClusterCreationIsAtomicAndNoRootLock(t *testing.T) {
	useHomeTestCache(t)
	root := t.TempDir()
	const count = 16
	start := make(chan struct{})
	errs := make(chan error, count)
	var wait sync.WaitGroup
	for i := 0; i < count; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := CreateCluster(root, CreateClusterRequest{Name: "Concurrent"})
			errs <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	clusters, err := ListClusters(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) != count {
		t.Fatalf("clusters = %d, want %d", len(clusters), count)
	}
	for _, lock := range []string{".carbon/home.lock", ".carbon/.home.lock", "home.lock"} {
		if _, err := os.Lstat(filepath.Join(root, lock)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected home-root lock %s: %v", lock, err)
		}
	}
}

func TestExplicitLegacyImportCopiesSharedTasksWithoutChangingSource(t *testing.T) {
	useHomeTestCache(t)
	legacyRoot := t.TempDir()
	source := t.TempDir()
	if err := repo.Init(source, "OLD"); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(source, repo.CarbonDirName, "tasks", "OLD-1.md")
	taskBytes := []byte("---\nid: OLD-1\ntitle: imported\nstatus: backlog\nactive_attempt: att-old\nprovenance:\n  - {who: human:test, at: 2026-01-01T00:00:00Z, did: began session ses-old}\n---\nbody\n")
	if err := os.WriteFile(taskPath, taskBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(source, repo.CarbonDirName, "sessions", "ses-old.yaml")
	if err := os.WriteFile(sessionPath, []byte("id: ses-old\ntask: OLD-1\nattempt: att-old\nactor: human:test\nstatus: finished\nidempotency_key: key\nstarted_at: 2026-01-01T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, repo.CarbonDirName, "runs", "OLD-1-20260101.log"), []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cluster.Ensure(legacyRoot, "Legacy", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cluster.AddProject(legacyRoot, source, "Old"); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	plan, err := PlanLegacyImport(target, legacyRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, CarbonDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plan created target metadata: %v", err)
	}
	result, err := ApplyLegacyImport(target, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatal("expected applied import")
	}
	if got, err := os.ReadFile(taskPath); err != nil || string(got) != string(taskBytes) {
		t.Fatalf("source task changed: %v %q", err, got)
	}
	dataRoot, err := ClusterDataRoot(target, result.Plan.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := os.ReadFile(filepath.Join(dataRoot, repo.CarbonDirName, "tasks", "OLD-1.md"))
	if err != nil {
		t.Fatal(err)
	}
	projectID := result.Plan.Projects[0].TargetID
	if !strings.Contains(string(imported), "project_id: "+projectID) || !strings.Contains(string(imported), "active_attempt: att-old") {
		t.Fatalf("imported task missing Carbon scope: %s", imported)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, repo.CarbonDirName, "runs", "OLD-1-20260101.log")); err != nil {
		t.Fatalf("run evidence missing: %v", err)
	}
	if _, err := os.Stat(result.BackupPath); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if _, err := os.Stat(result.ReceiptPath); err != nil {
		t.Fatalf("receipt missing: %v", err)
	}
}

func TestLegacyImportAppendsToExistingHomeAndBlocksConfigConflictUntilExplicit(t *testing.T) {
	useHomeTestCache(t)
	target := t.TempDir()
	existing, err := CreateCluster(target, CreateClusterRequest{Name: "Existing", Prefix: "EX"})
	if err != nil {
		t.Fatal(err)
	}
	legacyRoot := t.TempDir()
	first := t.TempDir()
	second := t.TempDir()
	for _, source := range []string{first, second} {
		if err := repo.Init(source, "SRC"); err != nil {
			t.Fatal(err)
		}
	}
	secondConfigPath := filepath.Join(second, repo.CarbonDirName, "config.yaml")
	secondConfig, err := config.Load(secondConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	secondConfig.CheckTimeoutDefault = 999
	if err := config.Save(secondConfigPath, secondConfig); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{first, second} {
		task := []byte("---\nid: SAME-1\ntitle: same\nstatus: backlog\nactive_attempt: att-same\nprovenance:\n  - {who: human:test, at: 2026-01-01T00:00:00Z, did: began session ses-same}\n---\n")
		if err := os.WriteFile(filepath.Join(source, repo.CarbonDirName, "tasks", "SAME-1.md"), task, 0o600); err != nil {
			t.Fatal(err)
		}
		session := []byte("id: ses-same\ntask: SAME-1\nattempt: att-same\nactor: human:test\nstatus: finished\nidempotency_key: key\nstarted_at: 2026-01-01T00:00:00Z\n")
		if err := os.WriteFile(filepath.Join(source, repo.CarbonDirName, "sessions", "ses-same.yaml"), session, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cluster.Ensure(legacyRoot, "Legacy", false); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{first, second} {
		if _, _, err := cluster.AddProject(legacyRoot, source, "Source"); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := PlanLegacyImport(target, legacyRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Manifest.Clusters) != 2 || plan.Manifest.Clusters[0].ID != existing.ID {
		t.Fatalf("existing home was not preserved in plan: %#v", plan.Manifest.Clusters)
	}
	if len(plan.ConfigConflicts) == 0 {
		t.Fatal("expected workflow config conflict")
	}
	if _, err := ApplyLegacyImport(target, plan); !errors.Is(err, ErrInvalidMigrationPlan) {
		t.Fatalf("conflicted apply = %v, want blocked", err)
	}
	plan.ConfigPolicy = "primary"
	result, err := ApplyLegacyImport(target, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatal("explicit primary policy did not apply")
	}
	if _, err := ClusterDataRoot(target, existing.ID); err != nil {
		t.Fatalf("existing cluster changed or lost: %v", err)
	}
	importRoot, err := ClusterDataRoot(target, result.Plan.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(importRoot, repo.CarbonDirName, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() == entries[1].Name() {
		t.Fatalf("colliding task ids not deterministically remapped: %#v", entries)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(importRoot, repo.CarbonDirName, "tasks", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "active_attempt: project_") || !strings.Contains(string(data), "began session project_") {
			t.Fatalf("task references were not remapped: %s", data)
		}
	}
	sessions, err := os.ReadDir(filepath.Join(importRoot, repo.CarbonDirName, "sessions"))
	if err != nil || len(sessions) != 2 || sessions[0].Name() == sessions[1].Name() {
		t.Fatalf("session collision remap = %#v, %v", sessions, err)
	}
	if _, err := PlanLegacyImport(target, legacyRoot); !errors.Is(err, ErrLegacyAlreadyImported) {
		t.Fatalf("repeat import = %v, want ErrLegacyAlreadyImported", err)
	}
}

func TestApplyLegacyImportReplansTamperedSerializedPlan(t *testing.T) {
	useHomeTestCache(t)
	fixture := newLegacyImportFixture(t)
	plan, err := PlanLegacyImport(fixture.target, fixture.legacyRoot)
	if err != nil {
		t.Fatal(err)
	}
	secretRoot := t.TempDir()
	if err := repo.Init(secretRoot, "SECRET"); err != nil {
		t.Fatal(err)
	}
	secretTaskPath := filepath.Join(secretRoot, repo.CarbonDirName, "tasks", "SAFE-1.md")
	secretTask := []byte("---\nid: SAFE-1\ntitle: stolen\nstatus: backlog\n---\nsecret-body\n")
	if err := os.WriteFile(secretTaskPath, secretTask, 0o600); err != nil {
		t.Fatal(err)
	}
	secretCairn := filepath.Join(secretRoot, repo.CarbonDirName)
	secretSnapshot, err := hashTree(secretCairn)
	if err != nil {
		t.Fatal(err)
	}

	// This represents a serialized plan modified by an untrusted client. The legacy
	// root/review digest remain valid so an insecure apply would accept its arbitrary
	// CairnPath, SourceFile, and target manifest.
	tampered := plan
	tampered.Projects[0].CairnPath = secretCairn
	tampered.Projects[0].SnapshotDigest = secretSnapshot
	tampered.Tasks[0].SourceFile = secretTaskPath
	tampered.Tasks[0].SourceHash = hashBytesHex(secretTask)
	for index := range tampered.Manifest.Clusters {
		if tampered.Manifest.Clusters[index].ID == tampered.ClusterID {
			tampered.Manifest.Clusters[index].Name = "Attacker Controlled"
		}
	}

	result, err := ApplyLegacyImport(fixture.target, tampered)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.Plan.ReviewDigest != plan.ReviewDigest {
		t.Fatalf("tampered apply result = %#v", result)
	}
	if result.Plan.Manifest.Clusters[len(result.Plan.Manifest.Clusters)-1].Name == "Attacker Controlled" {
		t.Fatal("apply trusted the serialized target manifest")
	}
	dataRoot, err := ClusterDataRoot(fixture.target, result.Plan.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := os.ReadFile(filepath.Join(dataRoot, repo.CarbonDirName, "tasks", result.Plan.Tasks[0].TargetID+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(imported), "secret-body") || !strings.Contains(string(imported), "safe-body") {
		t.Fatalf("apply trusted tampered source path: %s", imported)
	}
}

func TestLegacyImportReviewDigestRejectsChangedSourceSnapshot(t *testing.T) {
	useHomeTestCache(t)
	fixture := newLegacyImportFixture(t)
	plan, err := PlanLegacyImport(fixture.target, fixture.legacyRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.taskPath, []byte("---\nid: SAFE-1\ntitle: changed\nstatus: backlog\n---\nchanged-body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = ApplyLegacyImportRequest(fixture.target, LegacyImportApplyRequest{
		LegacyRoot: fixture.legacyRoot, ExpectedDigest: plan.ReviewDigest,
	})
	if !errors.Is(err, ErrLegacyChanged) {
		t.Fatalf("changed source apply = %v, want ErrLegacyChanged", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.target, CarbonDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("digest mismatch wrote target metadata: %v", err)
	}
}

func TestLegacyImportStagingFailureCanRetryWithoutChangingSource(t *testing.T) {
	useHomeTestCache(t)
	fixture := newLegacyImportFixture(t)
	plan, err := PlanLegacyImport(fixture.target, fixture.legacyRoot)
	if err != nil {
		t.Fatal(err)
	}
	before, err := hashTree(filepath.Join(fixture.source, repo.CarbonDirName))
	if err != nil {
		t.Fatal(err)
	}
	previousHook := legacyImportApplyHook
	legacyImportApplyHook = func(stage string) error {
		if stage == "after_staged" {
			return errors.New("injected staging failure")
		}
		return nil
	}
	result, err := ApplyLegacyImportRequest(fixture.target, LegacyImportApplyRequest{
		LegacyRoot: fixture.legacyRoot, ExpectedDigest: plan.ReviewDigest,
	})
	legacyImportApplyHook = previousHook
	if err == nil || result.Applied {
		t.Fatalf("injected staging failure result = %#v, %v", result, err)
	}
	after, err := hashTree(filepath.Join(fixture.source, repo.CarbonDirName))
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("failed import changed source snapshot")
	}
	if _, err := Open(fixture.target); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("failed staging import published a home: %v", err)
	}
	carbonRoot := filepath.Join(fixture.target, CarbonDirName)
	if imported, err := legacyAlreadyImported(carbonRoot, fixture.legacyRoot); err != nil || imported {
		t.Fatalf("incomplete receipt blocks retry: imported=%v err=%v", imported, err)
	}
	if _, err := os.Stat(filepath.Join(carbonRoot, "staging", result.Plan.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial staging data remained active: %v", err)
	}

	retryPlan, err := PlanLegacyImport(fixture.target, fixture.legacyRoot)
	if err != nil {
		t.Fatalf("retry plan after failed import: %v", err)
	}
	if retryPlan.ReviewDigest != plan.ReviewDigest {
		t.Fatalf("source-stable retry digest = %s, want %s", retryPlan.ReviewDigest, plan.ReviewDigest)
	}
	retry, err := ApplyLegacyImportRequest(fixture.target, LegacyImportApplyRequest{
		LegacyRoot: fixture.legacyRoot, ExpectedDigest: retryPlan.ReviewDigest,
	})
	if err != nil || !retry.Applied {
		t.Fatalf("retry apply = %#v, %v", retry, err)
	}
	if _, err := PlanLegacyImport(fixture.target, fixture.legacyRoot); !errors.Is(err, ErrLegacyAlreadyImported) {
		t.Fatalf("completed retry did not block duplicate import: %v", err)
	}
}

func TestLegacyImportRecoversPreparedReceiptWithoutBlockingRetry(t *testing.T) {
	useHomeTestCache(t)
	fixture := newLegacyImportFixture(t)
	if _, err := CreateCluster(fixture.target, CreateClusterRequest{Name: "Existing"}); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanLegacyImport(fixture.target, fixture.legacyRoot)
	if err != nil {
		t.Fatal(err)
	}
	before, err := hashTree(filepath.Join(fixture.source, repo.CarbonDirName))
	if err != nil {
		t.Fatal(err)
	}
	carbonRoot := filepath.Join(fixture.target, CarbonDirName)
	receiptDir, err := ensureDataRoot(carbonRoot, path.Dir(plan.ReceiptPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeImportReceipt(receiptDir, path.Base(plan.ReceiptPath), LegacyImportReceipt{
		Version: Version, ID: plan.ID, Status: "prepared", Plan: plan,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureClusterStore(carbonRoot, importStagingDataPath(plan), "PREPARED"); err != nil {
		t.Fatal(err)
	}
	if imported, err := legacyAlreadyImported(carbonRoot, fixture.legacyRoot); err != nil || imported {
		t.Fatalf("prepared receipt must not block retry: imported=%v err=%v", imported, err)
	}
	if _, err := PlanLegacyImport(fixture.target, fixture.legacyRoot); err != nil {
		t.Fatalf("prepared receipt blocked planning: %v", err)
	}
	result, err := ApplyLegacyImportRequest(fixture.target, LegacyImportApplyRequest{
		LegacyRoot: fixture.legacyRoot, ExpectedDigest: plan.ReviewDigest,
	})
	if err != nil || !result.Applied {
		t.Fatalf("prepared receipt recovery/apply = %#v, %v", result, err)
	}
	after, err := hashTree(filepath.Join(fixture.source, repo.CarbonDirName))
	if err != nil || after != before {
		t.Fatalf("prepared recovery changed source: before=%s after=%s err=%v", before, after, err)
	}
	if _, err := os.Stat(filepath.Join(carbonRoot, "staging", plan.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared staging was not recovered: %v", err)
	}
}

func TestResolveProjectByIDRejectsReusedSourceDirectory(t *testing.T) {
	useHomeTestCache(t)
	main := t.TempDir()
	sourceParent := t.TempDir()
	source := filepath.Join(sourceParent, "project")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	cluster, err := CreateCluster(main, CreateClusterRequest{Name: "App"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := AddProject(main, cluster.ID, AddProjectRequest{SourcePath: source})
	if err != nil {
		t.Fatal(err)
	}
	// Allocate the replacement while the original source still exists. A delete followed
	// immediately by Mkdir can legally reuse the same POSIX inode, which would not be a
	// distinct filesystem identity for this test to resolve.
	replacement := filepath.Join(sourceParent, "replacement")
	if err := os.Mkdir(replacement, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, replacementFingerprint, err := observeSource(replacement); err != nil {
		t.Fatal(err)
	} else if replacementFingerprint == project.Source.Fingerprint {
		t.Fatal("preallocated replacement unexpectedly shares the original source identity")
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, source); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveProject(main, ResolveProjectRequest{ClusterID: cluster.ID, ProjectID: project.ID}); !errors.Is(err, ErrProjectSourceMismatch) {
		t.Fatalf("reused path resolution = %v, want ErrProjectSourceMismatch", err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	offline, err := ResolveProject(main, ResolveProjectRequest{ClusterID: cluster.ID, ProjectID: project.ID})
	if err != nil || !offline.Offline {
		t.Fatalf("missing source resolution = %#v, %v", offline, err)
	}
}

func TestDoctorRestoresCompleteClusterStore(t *testing.T) {
	useHomeTestCache(t)
	main := t.TempDir()
	cluster, err := CreateCluster(main, CreateClusterRequest{Name: "Doctor", Prefix: "DOC"})
	if err != nil {
		t.Fatal(err)
	}
	dataRoot, err := ClusterDataRoot(main, cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dataRoot); err != nil {
		t.Fatal(err)
	}
	dryRun, err := Doctor(main, DoctorOptions{})
	if err != nil || !dryRun.Changed {
		t.Fatalf("doctor dry-run = %#v, %v", dryRun, err)
	}
	if _, err := os.Stat(dataRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("doctor dry run recreated data root: %v", err)
	}
	applied, err := Doctor(main, DoctorOptions{Apply: true})
	if err != nil || !applied.Applied {
		t.Fatalf("doctor apply = %#v, %v", applied, err)
	}
	for _, relative := range []string{repo.CarbonDirName, path.Join(repo.CarbonDirName, "tasks"), path.Join(repo.CarbonDirName, "runs"), path.Join(repo.CarbonDirName, "sessions"), path.Join(repo.CarbonDirName, "live"), path.Join(repo.CarbonDirName, "config.yaml")} {
		if _, err := os.Stat(filepath.Join(dataRoot, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("doctor did not restore %s: %v", relative, err)
		}
	}
	cfg, err := config.Load(filepath.Join(dataRoot, repo.CarbonDirName, "config.yaml"))
	if err != nil || cfg.Prefix != "DOC" || cfg.ProjectID != "" {
		t.Fatalf("doctor restored invalid shared config: %+v, %v", cfg, err)
	}
	if err := os.RemoveAll(filepath.Join(dataRoot, repo.CarbonDirName, "live")); err != nil {
		t.Fatal(err)
	}
	if _, err := Doctor(main, DoctorOptions{Apply: true}); err != nil {
		t.Fatalf("doctor did not restore missing store child: %v", err)
	}
	if info, err := os.Stat(filepath.Join(dataRoot, repo.CarbonDirName, "live")); err != nil || !info.IsDir() {
		t.Fatalf("doctor did not restore live directory: %v", err)
	}
}

type legacyImportFixture struct {
	target     string
	legacyRoot string
	source     string
	taskPath   string
}

func newLegacyImportFixture(t *testing.T) legacyImportFixture {
	t.Helper()
	legacyRoot := t.TempDir()
	source := t.TempDir()
	if err := repo.Init(source, "SAFE"); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(source, repo.CarbonDirName, "tasks", "SAFE-1.md")
	if err := os.WriteFile(taskPath, []byte("---\nid: SAFE-1\ntitle: safe\nstatus: backlog\n---\nsafe-body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cluster.Ensure(legacyRoot, "Legacy", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cluster.AddProject(legacyRoot, source, "Safe source"); err != nil {
		t.Fatal(err)
	}
	return legacyImportFixture{target: t.TempDir(), legacyRoot: legacyRoot, source: source, taskPath: taskPath}
}

func useHomeTestCache(t *testing.T) {
	t.Helper()
	cache := t.TempDir()
	previous := userCacheDir
	userCacheDir = func() (string, error) { return cache, nil }
	t.Cleanup(func() { userCacheDir = previous })
}
