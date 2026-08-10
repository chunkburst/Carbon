package mcp

import (
	"errors"
	"fmt"
	"strings"

	"carbon/internal/home"
)

var (
	// ErrCarbonHomeScopeRequired keeps catalog operations bound to the local Carbon
	// home selected by the transport. A legacy --repo connection must not infer or
	// inspect an unrelated home directory.
	ErrCarbonHomeScopeRequired = errors.New("Carbon home scope is required")
	// ErrCreateApprovalRequired makes agent-created catalog entries intentional. The
	// reason is part of the MCP request contract even though it is not durable home
	// identity metadata; callers can record it in their task/provenance workflow.
	ErrCreateApprovalRequired = errors.New("creation requires allow_create=true and a reason")
)

// ClusterCatalog is the discovery-safe summary of a cluster. IDs are always the
// canonical values callers should save after resolving a human-facing reference.
type ClusterCatalog struct {
	CanonicalID  string   `json:"canonicalId"`
	Name         string   `json:"name"`
	Slug         string   `json:"slug,omitempty"`
	Description  string   `json:"description,omitempty"`
	SlugAliases  []string `json:"slugAliases,omitempty"`
	Prefix       string   `json:"prefix"`
	ProjectCount int      `json:"projectCount"`
}

// ProjectCatalog is one project inside a cluster. SourcePath is configuration
// metadata needed to bootstrap a local agent, not task data or a credential.
type ProjectCatalog struct {
	CanonicalID string           `json:"canonicalId"`
	ClusterID   string           `json:"clusterId"`
	ClusterSlug string           `json:"clusterSlug,omitempty"`
	Name        string           `json:"name"`
	Slug        string           `json:"slug,omitempty"`
	Description string           `json:"description,omitempty"`
	SlugAliases []string         `json:"slugAliases,omitempty"`
	Kind        home.ProjectKind `json:"kind"`
	SourcePath  string           `json:"sourcePath,omitempty"`
	// Standalone identifies a top-level private project with no shared cluster
	// task pool. ClusterID is intentionally empty for this shape.
	Standalone bool `json:"standalone,omitempty"`
}

// ClusterDescription gives agents all non-task metadata necessary to choose a
// cluster and inspect its project surfaces.
type ClusterDescription struct {
	Cluster  ClusterCatalog   `json:"cluster"`
	Projects []ProjectCatalog `json:"projects"`
}

// ProjectDescription makes both the project and its canonical parent explicit.
type ProjectDescription struct {
	Cluster    ClusterCatalog `json:"cluster"`
	Project    ProjectCatalog `json:"project"`
	Standalone bool           `json:"standalone,omitempty"`
}

// CatalogCreateClusterInput is intentionally explicit. Agents must assert
// AllowCreate and provide a human-readable reason before durable home metadata is
// created; an omitted or false flag never falls back to implicit creation.
type CatalogCreateClusterInput struct {
	Name        string
	Slug        string
	Description string
	SlugAliases []string
	Prefix      string
	AllowCreate bool
	Reason      string
}

// CatalogCreateProjectInput follows the same approval contract. SourcePath remains
// required because a project without a locally verified source directory cannot run
// sessions/checks safely.
type CatalogCreateProjectInput struct {
	// Cluster is optional. Omitting it creates an isolated top-level project;
	// supplying one retains the shared-cluster project behavior.
	Cluster     string
	Name        string
	Slug        string
	Description string
	SlugAliases []string
	Kind        home.ProjectKind
	SourcePath  string
	AllowCreate bool
	Reason      string
}

func (svc *Service) catalogHome() (*home.Home, error) {
	if !svc.scope.IsCarbonHome() {
		return nil, ErrCarbonHomeScopeRequired
	}
	return home.Open(svc.scope.Home)
}

// ListCatalogClusters returns only summary metadata, preserving all project details
// for list/describe projects so a large home does not make every discovery call heavy.
func (svc *Service) ListCatalogClusters() ([]ClusterCatalog, error) {
	h, err := svc.catalogHome()
	if err != nil {
		return nil, err
	}
	clusters, err := h.ListClusters()
	if err != nil {
		return nil, err
	}
	out := make([]ClusterCatalog, 0, len(clusters))
	for _, cluster := range clusters {
		out = append(out, clusterCatalog(cluster))
	}
	return out, nil
}

// ResolveCatalogCluster accepts ID -> slug/alias -> unique display name and returns
// a canonical id suitable for subsequent MCP calls.
func (svc *Service) ResolveCatalogCluster(reference string) (ClusterCatalog, error) {
	h, err := svc.catalogHome()
	if err != nil {
		return ClusterCatalog{}, err
	}
	cluster, err := h.ResolveCluster(reference)
	if err != nil {
		return ClusterCatalog{}, err
	}
	return clusterCatalog(cluster), nil
}

// DescribeCatalogCluster returns one cluster and all of its project descriptions.
func (svc *Service) DescribeCatalogCluster(reference string) (ClusterDescription, error) {
	h, err := svc.catalogHome()
	if err != nil {
		return ClusterDescription{}, err
	}
	cluster, err := h.ResolveCluster(reference)
	if err != nil {
		return ClusterDescription{}, err
	}
	projects := make([]ProjectCatalog, 0, len(cluster.Projects))
	for _, project := range cluster.Projects {
		projects = append(projects, projectCatalog(cluster, project, false))
	}
	return ClusterDescription{Cluster: clusterCatalog(cluster), Projects: projects}, nil
}

// CreateCatalogCluster performs the explicit agent-approved home mutation and
// returns the generated canonical ID. It creates the selected home only after the
// approval fields have been checked.
func (svc *Service) CreateCatalogCluster(input CatalogCreateClusterInput) (ClusterCatalog, error) {
	if err := requireCreateApproval(input.AllowCreate, input.Reason); err != nil {
		return ClusterCatalog{}, err
	}
	if !svc.scope.IsCarbonHome() {
		return ClusterCatalog{}, ErrCarbonHomeScopeRequired
	}
	cluster, err := home.CreateCluster(svc.scope.Home, home.CreateClusterRequest{
		Name: input.Name, Slug: input.Slug, Description: input.Description,
		SlugAliases: input.SlugAliases, Prefix: input.Prefix,
	})
	if err != nil {
		return ClusterCatalog{}, err
	}
	return clusterCatalog(cluster), nil
}

// ListCatalogProjects returns every project in a home when clusterRef is empty,
// including top-level standalone projects. Supplying clusterRef retains the old
// shared-cluster-only filtering behavior. Each result carries its canonical parent
// IDs (or Standalone=true) so callers cannot infer a task root from a display name.
func (svc *Service) ListCatalogProjects(clusterRef string) ([]ProjectCatalog, error) {
	h, err := svc.catalogHome()
	if err != nil {
		return nil, err
	}
	var clusters []home.Cluster
	if strings.TrimSpace(clusterRef) != "" {
		cluster, err := h.ResolveCluster(clusterRef)
		if err != nil {
			return nil, err
		}
		clusters = []home.Cluster{cluster}
	} else {
		clusters, err = h.ListClusters()
		if err != nil {
			return nil, err
		}
	}
	out := make([]ProjectCatalog, 0)
	for _, cluster := range clusters {
		for _, project := range cluster.Projects {
			out = append(out, projectCatalog(cluster, project, false))
		}
	}
	if strings.TrimSpace(clusterRef) == "" {
		projects, err := h.ListProjects()
		if err != nil {
			return nil, err
		}
		for _, project := range projects {
			out = append(out, projectCatalog(home.Cluster{}, project, true))
		}
	}
	return out, nil
}

// ResolveCatalogProject resolves both references without performing a source
// fingerprint probe. The returned ID pair is stable; execution paths re-resolve via
// home.ResolveProject before they operate in the source folder.
func (svc *Service) ResolveCatalogProject(clusterRef, projectRef string) (ProjectDescription, error) {
	h, err := svc.catalogHome()
	if err != nil {
		return ProjectDescription{}, err
	}
	resolved, err := h.ResolveProjectMetadata(clusterRef, projectRef)
	if err != nil {
		return ProjectDescription{}, err
	}
	if strings.TrimSpace(clusterRef) != "" && resolved.Standalone {
		return ProjectDescription{}, home.ErrProjectNotFound
	}
	return ProjectDescription{
		Cluster:    clusterCatalog(resolved.Cluster),
		Project:    projectCatalog(resolved.Cluster, resolved.Project, resolved.Standalone),
		Standalone: resolved.Standalone,
	}, nil
}

// DescribeCatalogProject is intentionally an alias for ResolveCatalogProject: both
// identify a project through friendly references and return its complete catalog
// description plus canonical IDs.
func (svc *Service) DescribeCatalogProject(clusterRef, projectRef string) (ProjectDescription, error) {
	return svc.ResolveCatalogProject(clusterRef, projectRef)
}

// CreateCatalogProject adds either an isolated top-level project (the default when
// Cluster is omitted) or a surface in one selected shared cluster. Both paths require
// explicit agent approval and a present source path.
func (svc *Service) CreateCatalogProject(input CatalogCreateProjectInput) (ProjectDescription, error) {
	if err := requireCreateApproval(input.AllowCreate, input.Reason); err != nil {
		return ProjectDescription{}, err
	}
	if strings.TrimSpace(input.SourcePath) == "" {
		return ProjectDescription{}, fmt.Errorf("%w: source_path is required", ErrCreateApprovalRequired)
	}
	if !svc.scope.IsCarbonHome() {
		return ProjectDescription{}, ErrCarbonHomeScopeRequired
	}
	request := home.AddProjectRequest{
		Name: input.Name, Slug: input.Slug, Description: input.Description,
		SlugAliases: input.SlugAliases, Kind: input.Kind, SourcePath: input.SourcePath,
	}
	if strings.TrimSpace(input.Cluster) == "" {
		// The top-level default mirrors create_cluster: it can bootstrap a missing
		// home because no shared pool needs to be selected first.
		project, err := home.AddStandaloneProject(svc.scope.Home, request)
		if err != nil {
			return ProjectDescription{}, err
		}
		return svc.ResolveCatalogProject("", project.ID)
	}
	h, err := svc.catalogHome()
	if err != nil {
		return ProjectDescription{}, err
	}
	cluster, err := h.ResolveCluster(input.Cluster)
	if err != nil {
		return ProjectDescription{}, err
	}
	project, err := h.AddProject(cluster.ID, request)
	if err != nil {
		return ProjectDescription{}, err
	}
	// Reload through metadata after the write so callers get the current canonical
	// cluster description rather than a stale in-memory copy.
	return svc.ResolveCatalogProject(cluster.ID, project.ID)
}

func requireCreateApproval(allowCreate bool, reason string) error {
	if !allowCreate || strings.TrimSpace(reason) == "" {
		return ErrCreateApprovalRequired
	}
	return nil
}

func clusterCatalog(cluster home.Cluster) ClusterCatalog {
	return ClusterCatalog{
		CanonicalID: cluster.ID, Name: cluster.Name, Slug: cluster.Slug,
		Description: cluster.Description, SlugAliases: append([]string(nil), cluster.SlugAliases...),
		Prefix: cluster.Prefix, ProjectCount: len(cluster.Projects),
	}
}

func projectCatalog(cluster home.Cluster, project home.Project, standalone bool) ProjectCatalog {
	return ProjectCatalog{
		CanonicalID: project.ID, ClusterID: cluster.ID, ClusterSlug: cluster.Slug,
		Name: project.Name, Slug: project.Slug, Description: project.Description,
		SlugAliases: append([]string(nil), project.SlugAliases...), Kind: project.Kind,
		SourcePath: project.Source.Path, Standalone: standalone,
	}
}
