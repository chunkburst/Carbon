package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"carbon/internal/compat"
	"carbon/internal/home"
	"carbon/internal/store"
)

func TestCatalogCreatesOnlyWithExplicitApprovalAndResolvesCanonicalIDs(t *testing.T) {
	main := t.TempDir()
	svc := NewScopedService(store.New(t.TempDir()), "agent:catalog", Scope{Home: main}, nil)
	if _, err := svc.CreateCatalogCluster(CatalogCreateClusterInput{Name: "App", AllowCreate: true}); !errors.Is(err, ErrCreateApprovalRequired) {
		t.Fatalf("cluster create without reason = %v, want ErrCreateApprovalRequired", err)
	}
	if _, err := svc.CreateCatalogCluster(CatalogCreateClusterInput{Name: "App", Reason: "needed"}); !errors.Is(err, ErrCreateApprovalRequired) {
		t.Fatalf("cluster create without allow_create = %v, want ErrCreateApprovalRequired", err)
	}
	cluster, err := svc.CreateCatalogCluster(CatalogCreateClusterInput{
		Name: "Application", Slug: "app", Description: "one product family", AllowCreate: true, Reason: "bootstrap product",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cluster.CanonicalID == "" || cluster.Slug != "app" {
		t.Fatalf("created cluster = %#v", cluster)
	}
	if got, err := svc.ResolveCatalogCluster("APP"); err != nil || got.CanonicalID != cluster.CanonicalID {
		t.Fatalf("resolve cluster = %#v, %v", got, err)
	}
	if _, err := svc.CreateCatalogProject(CatalogCreateProjectInput{
		Cluster: cluster.CanonicalID, Name: "Desktop", SourcePath: t.TempDir(), Reason: "missing approval",
	}); !errors.Is(err, ErrCreateApprovalRequired) {
		t.Fatalf("project create without allow_create = %v, want ErrCreateApprovalRequired", err)
	}
	if _, err := svc.CreateCatalogProject(CatalogCreateProjectInput{
		Cluster: cluster.CanonicalID, Name: "Desktop", AllowCreate: true, Reason: "missing path",
	}); !errors.Is(err, ErrCreateApprovalRequired) {
		t.Fatalf("project create without source path = %v, want ErrCreateApprovalRequired", err)
	}
	project, err := svc.CreateCatalogProject(CatalogCreateProjectInput{
		Cluster: cluster.CanonicalID, Name: "Desktop", Slug: "desktop", Kind: home.ProjectPC,
		SourcePath: t.TempDir(), AllowCreate: true, Reason: "desktop surface",
	})
	if err != nil {
		t.Fatal(err)
	}
	if project.Project.CanonicalID == "" || project.Cluster.CanonicalID != cluster.CanonicalID {
		t.Fatalf("created project = %#v", project)
	}
	updatedSlug := "windows"
	if _, err := home.UpdateProject(main, cluster.CanonicalID, project.Project.CanonicalID, home.UpdateProjectRequest{Slug: &updatedSlug}); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.ResolveCatalogProject("app", "DESKTOP"); err != nil || got.Project.CanonicalID != project.Project.CanonicalID {
		t.Fatalf("resolve historical project alias = %#v, %v", got, err)
	}
	all, err := svc.ListCatalogProjects("")
	if err != nil || len(all) != 1 || all[0].CanonicalID != project.Project.CanonicalID {
		t.Fatalf("all projects = %#v, %v", all, err)
	}
	description, err := svc.DescribeCatalogCluster(cluster.CanonicalID)
	if err != nil || len(description.Projects) != 1 || description.Projects[0].SourcePath == "" {
		t.Fatalf("cluster description = %#v, %v", description, err)
	}
}

func TestCatalogStandaloneProjectsDefaultWithoutClusterAndFailClosedWhenAmbiguous(t *testing.T) {
	main := t.TempDir()
	svc := NewScopedService(store.New(t.TempDir()), "agent:catalog", Scope{Home: main}, nil)
	standalone, err := svc.CreateCatalogProject(CatalogCreateProjectInput{
		Name: "Desktop", Slug: "desktop-private", Kind: home.ProjectPC, SourcePath: t.TempDir(),
		AllowCreate: true, Reason: "independent desktop surface",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !standalone.Standalone || !standalone.Project.Standalone || standalone.Cluster.CanonicalID != "" || standalone.Project.ClusterID != "" {
		t.Fatalf("standalone catalog result = %#v", standalone)
	}
	if got, err := svc.ResolveCatalogProject("", "DESKTOP-PRIVATE"); err != nil || !got.Standalone || got.Project.CanonicalID != standalone.Project.CanonicalID {
		t.Fatalf("unscoped standalone resolve = %#v, %v", got, err)
	}
	if _, err := svc.ResolveCatalogProject("missing-cluster", standalone.Project.CanonicalID); !errors.Is(err, home.ErrClusterNotFound) {
		t.Fatalf("standalone with explicit cluster = %v, want ErrClusterNotFound", err)
	}

	cluster, err := svc.CreateCatalogCluster(CatalogCreateClusterInput{Name: "Shared", Prefix: "SHR", AllowCreate: true, Reason: "shared pool"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateCatalogProject(CatalogCreateProjectInput{
		Cluster: cluster.CanonicalID, Name: "Desktop", Slug: "desktop-shared", Kind: home.ProjectPC, SourcePath: t.TempDir(),
		AllowCreate: true, Reason: "legacy shared surface",
	}); err != nil {
		t.Fatal(err)
	}
	all, err := svc.ListCatalogProjects("")
	if err != nil || len(all) != 2 {
		t.Fatalf("all projects = %#v, %v", all, err)
	}
	if _, err := svc.ResolveCatalogProject("", "Desktop"); !errors.Is(err, home.ErrAmbiguousProjectReference) {
		t.Fatalf("ambiguous global project resolve = %v, want ErrAmbiguousProjectReference", err)
	}
	if got, err := svc.DescribeCatalogProject("", standalone.Project.CanonicalID); err != nil || !got.Standalone {
		t.Fatalf("standalone describe = %#v, %v", got, err)
	}
}

func TestCatalogRejectsLegacyScopeAndIdentityAdvertisesCompatibility(t *testing.T) {
	legacy := NewService(store.New(t.TempDir()), "agent:legacy", nil)
	if _, err := legacy.ListCatalogClusters(); !errors.Is(err, ErrCarbonHomeScopeRequired) {
		t.Fatalf("legacy catalog = %v, want ErrCarbonHomeScopeRequired", err)
	}
	legacyIdentity := legacy.Identity()
	if legacyIdentity.Scope.Mode != "legacy" || legacyIdentity.Compatibility.RequestedCompatLayer != compat.LegacyLayer || legacyIdentity.Compatibility.StableCompatLayer != compat.StableLayer {
		t.Fatalf("legacy identity = %#v", legacyIdentity)
	}
	carbon := NewScopedService(store.New(t.TempDir()), "agent:carbon", Scope{Home: t.TempDir(), CompatLayer: compat.StableLayer}, nil)
	carbonIdentity := carbon.Identity()
	if carbonIdentity.Scope.Mode != "carbon_home" || carbonIdentity.Compatibility.RequestedCompatLayer != compat.StableLayer || carbonIdentity.Compatibility.StableCompatLayer != compat.StableLayer {
		t.Fatalf("Carbon identity = %#v", carbonIdentity)
	}
}

func TestServerCatalogToolsRequireApprovalAndReturnCanonicalIDs(t *testing.T) {
	main := t.TempDir()
	source := t.TempDir()
	svc := NewScopedService(store.New(t.TempDir()), "agent:catalog", Scope{Home: main}, nil)
	srv := NewServer(svc)
	ctx := context.Background()
	clientT, serverT := mcpsdk.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "catalog-test", Version: "0"}, nil)
	clientSession, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	call := func(name string, args map[string]any, out any) bool {
		t.Helper()
		result, err := clientSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if result.IsError {
			return false
		}
		data, _ := json.Marshal(result.StructuredContent)
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		return true
	}
	if call("create_cluster", map[string]any{"name": "Denied", "reason": "not approved"}, &clusterOut{}) {
		t.Fatal("create_cluster without allow_create unexpectedly succeeded")
	}
	var cluster clusterOut
	if !call("create_cluster", map[string]any{
		"name": "Product", "slug": "product", "allow_create": true, "reason": "bootstrap",
	}, &cluster) {
		t.Fatal("approved create_cluster failed")
	}
	if cluster.Cluster.CanonicalID == "" {
		t.Fatalf("cluster = %#v", cluster)
	}
	var project ProjectDescription
	if !call("create_project", map[string]any{
		"cluster": "PRODUCT", "name": "Desktop", "slug": "desktop", "kind": "pc",
		"source_path": source, "allow_create": true, "reason": "desktop client",
	}, &project) {
		t.Fatal("approved create_project failed")
	}
	if project.Cluster.CanonicalID != cluster.Cluster.CanonicalID || project.Project.CanonicalID == "" {
		t.Fatalf("project = %#v", project)
	}
	var resolved ProjectDescription
	if !call("resolve_project", map[string]any{"cluster": cluster.Cluster.CanonicalID, "project": "DESKTOP"}, &resolved) {
		t.Fatal("resolve_project failed")
	}
	if resolved.Project.CanonicalID != project.Project.CanonicalID {
		t.Fatalf("resolved = %#v, want %s", resolved, project.Project.CanonicalID)
	}
}
