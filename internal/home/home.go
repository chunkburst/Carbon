package home

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	// CarbonDirName is the private metadata directory below a selected home root.
	CarbonDirName = ".carbon"
	// ManifestFilename is the home manifest filename inside CarbonDirName.
	ManifestFilename = "home.json"
	// Version is the latest home manifest schema supported for writes. Version 1
	// manifests remain readable so existing clustered homes are never rewritten just
	// by opening them.
	Version = 2

	legacyManifestVersion = 1

	clusterDataDirectory = "clusters"
	projectDataDirectory = "projects"
)

var (
	// ErrNotInitialized means the selected home does not contain a Carbon manifest.
	ErrNotInitialized = errors.New("carbon home is not initialized")
	// ErrAlreadyInitialized means an operation that must create a new home found one.
	ErrAlreadyInitialized = errors.New("carbon home is already initialized")
	// ErrInvalidRoot means a requested home or source path is not an existing directory.
	ErrInvalidRoot = errors.New("carbon path is not an existing directory")
	// ErrInvalidManifest wraps malformed or semantically invalid home metadata.
	ErrInvalidManifest = errors.New("invalid carbon home manifest")
	// ErrFutureVersion means a newer schema was found. Callers must not rewrite it.
	ErrFutureVersion = errors.New("unsupported future carbon home manifest version")
	// ErrUnsafePath means metadata, data roots, or lock-cache paths are symlinks/reparse
	// points or otherwise escape their canonical parent.
	ErrUnsafePath = errors.New("unsafe carbon metadata path")
	// ErrClusterNotFound means no cluster has the requested stable id.
	ErrClusterNotFound = errors.New("carbon cluster not found")
	// ErrAmbiguousCluster means a human-facing cluster reference matches more than
	// one entry. Callers must use the stable cluster id in that case.
	ErrAmbiguousCluster = errors.New("carbon cluster reference is ambiguous")
	// ErrProjectNotFound means no project has the requested stable id.
	ErrProjectNotFound = errors.New("carbon project not found")
	// ErrAmbiguousProjectReference means a human-facing project reference matches
	// more than one project in a cluster. It is distinct from ErrAmbiguousProject,
	// which describes a source-directory fingerprint matching multiple projects.
	ErrAmbiguousProjectReference = errors.New("carbon project reference is ambiguous")
	// ErrProjectSourceMismatch means a registered source path now resolves to a
	// different filesystem object. Callers must not use that path for project-scoped
	// operations until the owner explicitly Relinks the project.
	ErrProjectSourceMismatch = errors.New("carbon project source identity mismatch")
	// ErrAmbiguousProject means a source folder belongs to more than one project and a
	// caller must use the stable project id.
	ErrAmbiguousProject = errors.New("carbon project source is ambiguous")
	// ErrInvalidID means a caller-provided id cannot safely identify a manifest entry.
	ErrInvalidID = errors.New("invalid carbon id")
	// ErrInvalidProjectKind means the requested project target is unsupported.
	ErrInvalidProjectKind = errors.New("invalid carbon project kind")
	// ErrLockTimeout means another process held the per-home cache lock too long.
	ErrLockTimeout = errors.New("carbon home lock timeout")
	// ErrLegacyNotFound means no compatible .cairn-cluster.json v1 manifest exists.
	ErrLegacyNotFound = errors.New("legacy cairn cluster manifest not found")
	// ErrLegacyChanged means a previously reviewed migration plan no longer matches its
	// source manifest and must be preflighted again.
	ErrLegacyChanged = errors.New("legacy cairn cluster manifest changed")
	// ErrLegacyAlreadyImported means receipts show this legacy cluster was already imported
	// into the selected Carbon home. A second import would duplicate durable task history.
	ErrLegacyAlreadyImported = errors.New("legacy cairn cluster already imported")
	// ErrInvalidMigrationPlan means a migration plan cannot be safely applied.
	ErrInvalidMigrationPlan = errors.New("invalid carbon migration plan")
	// ErrDetachRequiresReview means a project belongs to a shared cluster store with
	// peers, so duplicating that store requires an explicit caller acknowledgement.
	ErrDetachRequiresReview = errors.New("carbon project detach requires shared-store review")
	// ErrDetachSourceChanged means the shared store changed while a detach snapshot
	// was being copied. Callers must retry from a stable source rather than receive a
	// silently inconsistent standalone copy.
	ErrDetachSourceChanged = errors.New("carbon project detach source changed during copy")
	// ErrDetachTargetExists means an unregistered standalone root is already present
	// for the project ID. It is never overwritten automatically.
	ErrDetachTargetExists = errors.New("carbon standalone project data root already exists")
)

// ProjectKind identifies a software surface inside one cluster. It is an open, safe slug
// rather than a closed enum: target taxonomy changes more quickly than a durable manifest
// schema. The exported constants are common UI defaults, not an exhaustive list.
type ProjectKind string

const (
	ProjectGeneric ProjectKind = "generic"
	ProjectPC      ProjectKind = "pc"
	ProjectMobile  ProjectKind = "mobile"
	ProjectIOS     ProjectKind = "ios"
	ProjectWeb     ProjectKind = "web"
	ProjectBackend ProjectKind = "backend"
	ProjectLibrary ProjectKind = "library"
	ProjectService ProjectKind = "service"
	ProjectOther   ProjectKind = "other"
)

// Manifest is the on-disk Carbon home registry. Version 1 contains only clustered
// projects; version 2 adds standalone Projects. Cluster.DataPath is always relative to
// <main>/.carbon, never an absolute path.
type Manifest struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	CreatedAt string    `json:"createdAt"`
	Clusters  []Cluster `json:"clusters"`
	// Projects are standalone stores introduced in manifest v2. Their data roots are
	// derived from their stable IDs under .carbon/projects rather than persisted as a
	// caller-controlled path.
	Projects []Project `json:"projects"`
}

// Cluster is one software target. Every project in a cluster uses the same DataPath as
// its task-store root, so tasks can carry project_id without duplicating a store.
type Cluster struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Slug is the machine-safe, case-insensitive-friendly reference for this
	// cluster. It remains optional so existing v1 home manifests stay valid.
	Slug        string `json:"slug,omitempty"`
	Description string `json:"description,omitempty"`
	// SlugAliases retains earlier Slug values after a rename. It is deliberately
	// metadata only: the stable ID remains the canonical durable identity.
	SlugAliases []string  `json:"slugAliases,omitempty"`
	Prefix      string    `json:"prefix"`
	DataPath    string    `json:"dataPath"`
	CreatedAt   string    `json:"createdAt"`
	Projects    []Project `json:"projects"`
}

// Project is a user-facing surface (for example PC, Mobile, or iOS). It can be a
// standalone home project or a member of a cluster; identity is random and stable and
// Source.Path is operational metadata, not identity.
type Project struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Slug        string      `json:"slug,omitempty"`
	Description string      `json:"description,omitempty"`
	SlugAliases []string    `json:"slugAliases,omitempty"`
	Kind        ProjectKind `json:"kind"`
	Source      Source      `json:"source"`
	CreatedAt   string      `json:"createdAt"`
}

// Source preserves the latest canonical source folder plus historical canonical aliases.
// Fingerprint is a filesystem identity when available, rather than a hash of Path, so a
// rename on the same volume can re-identify a project. LastSeen is the last successful
// canonical observation of the source.
type Source struct {
	Path        string   `json:"path"`
	Aliases     []string `json:"aliases"`
	Fingerprint string   `json:"fingerprint"`
	LastSeen    string   `json:"lastSeen"`
}

// Home is a lightweight handle. It intentionally does not cache a manifest: every
// operation reloads it so a second Carbon process cannot be hidden by stale state.
type Home struct {
	Root       string
	CarbonRoot string
}

// CreateClusterRequest contains caller-selected display metadata for a new target.
type CreateClusterRequest struct {
	Name        string
	Slug        string
	Description string
	SlugAliases []string
	Prefix      string
}

// UpdateClusterRequest permits changes to presentation metadata only. A changed
// Slug automatically retains the former slug as a historical alias. Nil fields are
// left unchanged; an explicit empty Description clears it. Prefix is intentionally
// absent: it controls already-created task IDs and cannot be changed safely here.
type UpdateClusterRequest struct {
	Name        *string
	Slug        *string
	Description *string
	SlugAliases *[]string
}

// AddProjectRequest registers one surface. SourcePath must currently exist; an offline
// project can only originate from an imported legacy manifest.
type AddProjectRequest struct {
	Name        string
	Slug        string
	Description string
	SlugAliases []string
	Kind        ProjectKind
	SourcePath  string
}

// UpdateProjectRequest permits only mutable display metadata. Nil fields are left
// unchanged; source binding, stable identity, and cluster ownership are intentionally
// absent so a generic PATCH endpoint cannot silently relink a project.
type UpdateProjectRequest struct {
	Name        *string      `json:"name,omitempty"`
	Slug        *string      `json:"slug,omitempty"`
	Description *string      `json:"description,omitempty"`
	SlugAliases *[]string    `json:"slugAliases,omitempty"`
	Kind        *ProjectKind `json:"kind,omitempty"`
}

// ResolveProjectRequest addresses a project either by ProjectID or by SourcePath. A
// source path that belongs to multiple projects is intentionally ambiguous.
type ResolveProjectRequest struct {
	ClusterID  string
	ProjectID  string
	SourcePath string
}

// ProjectResolution couples a project with the physical data root a later store.New call
// should use. Offline is true when no present source folder can be verified.
type ProjectResolution struct {
	Cluster    Cluster
	Project    Project
	DataRoot   string
	SourcePath string
	Offline    bool
	// Standalone distinguishes a project-owned root from a legacy shared cluster
	// root. Cluster is the zero value when Standalone is true.
	Standalone bool
}

// ProjectMetadataResolution resolves user-facing identifiers without touching the
// project's source folder. It is suitable for discovery UIs and MCP catalog tools;
// command/session execution must use ResolveProject so source identity is verified.
type ProjectMetadataResolution struct {
	Cluster    Cluster
	Project    Project
	Standalone bool
}

// Open returns a handle for an existing home without writing anything.
func Open(main string) (*Home, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return nil, err
	}
	carbonRoot, exists, err := carbonDir(root, false)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotInitialized
	}
	if _, exists, err := readManifest(carbonRoot); err != nil {
		return nil, err
	} else if !exists {
		return nil, ErrNotInitialized
	}
	return &Home{Root: root, CarbonRoot: carbonRoot}, nil
}

// Ensure creates a minimal home manifest if it is absent. It never reads or mutates a
// sibling .cairn directory.
func Ensure(main string) (*Home, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return nil, err
	}
	var carbonRoot string
	err = withLock(root, func() error {
		var exists bool
		carbonRoot, exists, err = carbonDir(root, true)
		if err != nil {
			return err
		}
		manifest, manifestExists, err := readManifest(carbonRoot)
		if err != nil {
			return err
		}
		if manifestExists {
			_ = manifest
			return nil
		}
		if !exists {
			return fmt.Errorf("%w: Carbon directory disappeared", ErrUnsafePath)
		}
		id, err := newID("home", nil)
		if err != nil {
			return err
		}
		return writeManifest(carbonRoot, Manifest{
			Version:   Version,
			ID:        id,
			CreatedAt: nowUTC(),
			Clusters:  []Cluster{},
			Projects:  []Project{},
		})
	})
	if err != nil {
		return nil, err
	}
	return &Home{Root: root, CarbonRoot: carbonRoot}, nil
}

// Manifest reads the current manifest from disk.
func (h *Home) Manifest() (Manifest, error) {
	if h == nil {
		return Manifest{}, ErrNotInitialized
	}
	// Re-resolve before every metadata read instead of trusting the cached CarbonRoot.
	// This detects a moved/replaced Windows junction or other path change between Open
	// and a later operation, and keeps all reads on the same canonical root policy as
	// mutations.
	root, err := resolveRoot(h.Root)
	if err != nil {
		return Manifest{}, err
	}
	carbonRoot, exists, err := carbonDir(root, false)
	if err != nil {
		return Manifest{}, err
	}
	if !exists {
		return Manifest{}, ErrNotInitialized
	}
	// Home handles are normally request-local. Refresh the exported paths only after
	// both root and metadata child have passed the same local/reparse checks, so
	// callers that need CarbonRoot for a sibling safe operation retain canonical data.
	h.Root = root
	h.CarbonRoot = carbonRoot
	manifest, exists, err := readManifest(carbonRoot)
	if err != nil {
		return Manifest{}, err
	}
	if !exists {
		return Manifest{}, ErrNotInitialized
	}
	return manifest, nil
}

// ListClusters returns independent targets in this home. It is read-only.
func ListClusters(main string) ([]Cluster, error) {
	home, err := Open(main)
	if err != nil {
		return nil, err
	}
	return home.ListClusters()
}

// ListClusters returns independent targets in this home. It is read-only.
func (h *Home) ListClusters() ([]Cluster, error) {
	manifest, err := h.Manifest()
	if err != nil {
		return nil, err
	}
	clusters := make([]Cluster, len(manifest.Clusters))
	copy(clusters, manifest.Clusters)
	return clusters, nil
}

// ListProjects returns standalone projects in this home. Cluster-owned projects remain
// available through ListClusters so existing callers keep their shared-store context.
func ListProjects(main string) ([]Project, error) {
	home, err := Open(main)
	if err != nil {
		return nil, err
	}
	return home.ListProjects()
}

// ListProjects returns standalone projects in an already-open home.
func (h *Home) ListProjects() ([]Project, error) {
	manifest, err := h.Manifest()
	if err != nil {
		return nil, err
	}
	projects := make([]Project, len(manifest.Projects))
	copy(projects, manifest.Projects)
	return projects, nil
}

// ResolveCluster resolves a stable id first, then a unique current/historical slug,
// then a unique display name. It performs no mutation and never treats a display name
// as durable identity; callers should persist the returned ID.
func ResolveCluster(main, reference string) (Cluster, error) {
	h, err := Open(main)
	if err != nil {
		return Cluster{}, err
	}
	return h.ResolveCluster(reference)
}

// ResolveCluster resolves a cluster reference in an already-open home.
func (h *Home) ResolveCluster(reference string) (Cluster, error) {
	if h == nil {
		return Cluster{}, ErrNotInitialized
	}
	manifest, err := h.Manifest()
	if err != nil {
		return Cluster{}, err
	}
	cluster, err := findCluster(&manifest, reference)
	if err != nil {
		return Cluster{}, err
	}
	return *cluster, nil
}

// ResolveProjectMetadata resolves cluster and project identifiers without observing a
// source directory. An empty clusterRef searches standalone and clustered projects
// together, failing closed when a slug or display name is ambiguous. Use ResolveProject
// when a task/session needs the source folder's current filesystem identity verified.
func ResolveProjectMetadata(main, clusterRef, projectRef string) (ProjectMetadataResolution, error) {
	h, err := Open(main)
	if err != nil {
		return ProjectMetadataResolution{}, err
	}
	return h.ResolveProjectMetadata(clusterRef, projectRef)
}

// ResolveProjectMetadata resolves catalog references in an already-open home.
func (h *Home) ResolveProjectMetadata(clusterRef, projectRef string) (ProjectMetadataResolution, error) {
	if h == nil {
		return ProjectMetadataResolution{}, ErrNotInitialized
	}
	manifest, err := h.Manifest()
	if err != nil {
		return ProjectMetadataResolution{}, err
	}
	if strings.TrimSpace(clusterRef) == "" {
		project, cluster, standalone, err := findProjectInManifest(&manifest, projectRef)
		if err != nil {
			return ProjectMetadataResolution{}, err
		}
		resolution := ProjectMetadataResolution{Project: *project, Standalone: standalone}
		if cluster != nil {
			resolution.Cluster = *cluster
		}
		return resolution, nil
	}
	cluster, err := findCluster(&manifest, clusterRef)
	if err != nil {
		return ProjectMetadataResolution{}, err
	}
	project, err := findProject(cluster, projectRef)
	if err != nil {
		return ProjectMetadataResolution{}, err
	}
	return ProjectMetadataResolution{Cluster: *cluster, Project: *project}, nil
}

// CreateCluster creates an isolated data root and a cluster manifest entry. It creates a
// missing home first, but does not create or change any source repository.
func CreateCluster(main string, request CreateClusterRequest) (Cluster, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return Cluster{}, err
	}
	var created Cluster
	err = mutate(root, true, func(carbonRoot string, manifest *Manifest) error {
		name := normalizedName(request.Name, "Cluster")
		if !validName(name) {
			return fmt.Errorf("%w: invalid cluster name", ErrInvalidManifest)
		}
		slug, err := requestedOrSuggestedClusterSlug(*manifest, request.Slug, name, "")
		if err != nil {
			return err
		}
		aliases, err := normalizedSlugAliases(request.SlugAliases, slug)
		if err != nil {
			return err
		}
		if !validDescription(request.Description) {
			return fmt.Errorf("%w: invalid cluster description", ErrInvalidManifest)
		}
		prefix := normalizePrefix(request.Prefix, name)
		if !validPrefix(prefix) {
			return fmt.Errorf("%w: invalid cluster task prefix", ErrInvalidManifest)
		}
		id, err := newID("cluster", allIDs(*manifest))
		if err != nil {
			return err
		}
		created = Cluster{
			ID:          id,
			Name:        name,
			Slug:        slug,
			Description: strings.TrimSpace(request.Description),
			SlugAliases: aliases,
			Prefix:      prefix,
			DataPath:    path.Join(clusterDataDirectory, id),
			CreatedAt:   nowUTC(),
			Projects:    []Project{},
		}
		candidate := *manifest
		candidate.Clusters = append(append([]Cluster(nil), manifest.Clusters...), created)
		if err := validateManifest(candidate); err != nil {
			return err
		}
		if _, err := ensureClusterStore(carbonRoot, created.DataPath, created.Prefix); err != nil {
			return err
		}
		manifest.Clusters = append(manifest.Clusters, created)
		return nil
	})
	if err != nil {
		return Cluster{}, err
	}
	return created, nil
}

// CreateCluster creates a cluster below this already-open home.
func (h *Home) CreateCluster(request CreateClusterRequest) (Cluster, error) {
	if h == nil {
		return Cluster{}, ErrNotInitialized
	}
	return CreateCluster(h.Root, request)
}

// UpdateCluster changes a cluster's display metadata without changing its stable ID,
// data root, or task prefix. A renamed machine slug is retained as an alias so existing
// agent configurations keep resolving.
func UpdateCluster(main, clusterRef string, request UpdateClusterRequest) (Cluster, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return Cluster{}, err
	}
	var updated Cluster
	err = mutate(root, false, func(_ string, manifest *Manifest) error {
		cluster, err := findCluster(manifest, clusterRef)
		if err != nil {
			return err
		}
		if request.Name != nil {
			name := strings.TrimSpace(*request.Name)
			if !validName(name) {
				return fmt.Errorf("%w: invalid cluster name", ErrInvalidManifest)
			}
			cluster.Name = name
		}
		if request.Description != nil {
			description := strings.TrimSpace(*request.Description)
			if !validDescription(description) {
				return fmt.Errorf("%w: invalid cluster description", ErrInvalidManifest)
			}
			cluster.Description = description
		}
		currentSlug := cluster.Slug
		if request.Slug != nil {
			slug, err := requestedOrSuggestedClusterSlug(*manifest, *request.Slug, cluster.Name, cluster.ID)
			if err != nil {
				return err
			}
			cluster.Slug = slug
		}
		aliases := append([]string(nil), cluster.SlugAliases...)
		if request.SlugAliases != nil {
			aliases = append([]string(nil), (*request.SlugAliases)...)
		}
		if currentSlug != "" && currentSlug != cluster.Slug {
			aliases = append(aliases, currentSlug)
		}
		aliases, err = normalizedSlugAliases(aliases, cluster.Slug)
		if err != nil {
			return err
		}
		cluster.SlugAliases = aliases
		if err := validateManifest(*manifest); err != nil {
			return err
		}
		updated = *cluster
		return nil
	})
	if err != nil {
		return Cluster{}, err
	}
	return updated, nil
}

// UpdateCluster updates mutable cluster metadata in this home.
func (h *Home) UpdateCluster(clusterRef string, request UpdateClusterRequest) (Cluster, error) {
	if h == nil {
		return Cluster{}, ErrNotInitialized
	}
	return UpdateCluster(h.Root, clusterRef, request)
}

// AddProject adds a stable random project identity. The same source directory may be
// intentionally registered many times, including with different kinds.
func AddProject(main, clusterID string, request AddProjectRequest) (Project, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return Project{}, err
	}
	request.Kind = normalizeProjectKind(request.Kind)
	if !validProjectKind(request.Kind) {
		return Project{}, fmt.Errorf("%w: %q", ErrInvalidProjectKind, request.Kind)
	}
	canonical, fingerprint, err := observeSource(request.SourcePath)
	if err != nil {
		return Project{}, err
	}
	var created Project
	err = mutate(root, false, func(carbonRoot string, manifest *Manifest) error {
		cluster, err := findCluster(manifest, clusterID)
		if err != nil {
			return err
		}
		id, err := newID("project", allIDs(*manifest))
		if err != nil {
			return err
		}
		name := normalizedName(request.Name, filepath.Base(canonical))
		if !validName(name) {
			return fmt.Errorf("%w: invalid project name", ErrInvalidManifest)
		}
		slug, err := requestedOrSuggestedProjectSlug(cluster, request.Slug, name, "")
		if err != nil {
			return err
		}
		aliases, err := normalizedSlugAliases(request.SlugAliases, slug)
		if err != nil {
			return err
		}
		if !validDescription(request.Description) {
			return fmt.Errorf("%w: invalid project description", ErrInvalidManifest)
		}
		created = Project{
			ID:          id,
			Name:        name,
			Slug:        slug,
			Description: strings.TrimSpace(request.Description),
			SlugAliases: aliases,
			Kind:        request.Kind,
			Source: Source{
				Path:        canonical,
				Aliases:     []string{canonical},
				Fingerprint: fingerprint,
				LastSeen:    nowUTC(),
			},
			CreatedAt: nowUTC(),
		}
		candidate := *manifest
		candidate.Clusters = append([]Cluster(nil), manifest.Clusters...)
		for index := range candidate.Clusters {
			if candidate.Clusters[index].ID == cluster.ID {
				candidate.Clusters[index].Projects = append(append([]Project(nil), cluster.Projects...), created)
				break
			}
		}
		if err := validateManifest(candidate); err != nil {
			return err
		}
		if _, err := ensureClusterStore(carbonRoot, cluster.DataPath, cluster.Prefix); err != nil {
			return err
		}
		cluster.Projects = append(cluster.Projects, created)
		return nil
	})
	if err != nil {
		return Project{}, err
	}
	return created, nil
}

// AddProject adds a project below this already-open home.
func (h *Home) AddProject(clusterID string, request AddProjectRequest) (Project, error) {
	if h == nil {
		return Project{}, ErrNotInitialized
	}
	return AddProject(h.Root, clusterID, request)
}

// AddStandaloneProject registers a project directly in a home. Unlike AddProject it
// creates the minimal home when absent, making a project-owned store the default path
// while clusters remain an optional shared-store extension.
func AddStandaloneProject(main string, request AddProjectRequest) (Project, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return Project{}, err
	}
	request.Kind = normalizeProjectKind(request.Kind)
	if !validProjectKind(request.Kind) {
		return Project{}, fmt.Errorf("%w: %q", ErrInvalidProjectKind, request.Kind)
	}
	canonical, fingerprint, err := observeSource(request.SourcePath)
	if err != nil {
		return Project{}, err
	}
	var created Project
	err = mutate(root, true, func(carbonRoot string, manifest *Manifest) error {
		id, err := newID("project", allIDs(*manifest))
		if err != nil {
			return err
		}
		name := normalizedName(request.Name, filepath.Base(canonical))
		if !validName(name) {
			return fmt.Errorf("%w: invalid project name", ErrInvalidManifest)
		}
		slug, err := requestedOrSuggestedStandaloneProjectSlug(*manifest, request.Slug, name, "")
		if err != nil {
			return err
		}
		aliases, err := normalizedSlugAliases(request.SlugAliases, slug)
		if err != nil {
			return err
		}
		if !validDescription(request.Description) {
			return fmt.Errorf("%w: invalid project description", ErrInvalidManifest)
		}
		created = Project{
			ID:          id,
			Name:        name,
			Slug:        slug,
			Description: strings.TrimSpace(request.Description),
			SlugAliases: aliases,
			Kind:        request.Kind,
			Source: Source{
				Path:        canonical,
				Aliases:     []string{canonical},
				Fingerprint: fingerprint,
				LastSeen:    nowUTC(),
			},
			CreatedAt: nowUTC(),
		}
		candidate := *manifest
		candidate.Version = Version
		candidate.Projects = append(append([]Project(nil), manifest.Projects...), created)
		if err := validateManifest(candidate); err != nil {
			return err
		}
		if _, err := ensureStandaloneProjectStore(carbonRoot, created.ID, normalizePrefix("", created.Name)); err != nil {
			return err
		}
		manifest.Projects = append(manifest.Projects, created)
		return nil
	})
	if err != nil {
		return Project{}, err
	}
	return created, nil
}

// AddStandaloneProject adds a project-owned store below this already-open home.
func (h *Home) AddStandaloneProject(request AddProjectRequest) (Project, error) {
	if h == nil {
		return Project{}, ErrNotInitialized
	}
	return AddStandaloneProject(h.Root, request)
}

// UpdateProject changes a project's display name and/or kind without changing its source
// binding, stable id, or containing cluster.
func UpdateProject(main, clusterID, projectID string, request UpdateProjectRequest) (Project, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return Project{}, err
	}
	var updated Project
	err = mutate(root, false, func(_ string, manifest *Manifest) error {
		cluster, err := findCluster(manifest, clusterID)
		if err != nil {
			return err
		}
		project, err := findProject(cluster, projectID)
		if err != nil {
			return err
		}
		if request.Name != nil {
			name := strings.TrimSpace(*request.Name)
			if !validName(name) {
				return fmt.Errorf("%w: invalid project name", ErrInvalidManifest)
			}
			project.Name = name
		}
		if request.Description != nil {
			description := strings.TrimSpace(*request.Description)
			if !validDescription(description) {
				return fmt.Errorf("%w: invalid project description", ErrInvalidManifest)
			}
			project.Description = description
		}
		currentSlug := project.Slug
		if request.Slug != nil {
			slug, err := requestedOrSuggestedProjectSlug(cluster, *request.Slug, project.Name, project.ID)
			if err != nil {
				return err
			}
			project.Slug = slug
		}
		aliases := append([]string(nil), project.SlugAliases...)
		if request.SlugAliases != nil {
			aliases = append([]string(nil), (*request.SlugAliases)...)
		}
		if currentSlug != "" && currentSlug != project.Slug {
			aliases = append(aliases, currentSlug)
		}
		aliases, err = normalizedSlugAliases(aliases, project.Slug)
		if err != nil {
			return err
		}
		project.SlugAliases = aliases
		if request.Kind != nil {
			kind := normalizeProjectKind(*request.Kind)
			if !validProjectKind(kind) {
				return fmt.Errorf("%w: %q", ErrInvalidProjectKind, *request.Kind)
			}
			project.Kind = kind
		}
		if err := validateManifest(*manifest); err != nil {
			return err
		}
		updated = *project
		return nil
	})
	if err != nil {
		return Project{}, err
	}
	return updated, nil
}

// UpdateProject updates mutable project metadata in this home.
func (h *Home) UpdateProject(clusterID, projectID string, request UpdateProjectRequest) (Project, error) {
	if h == nil {
		return Project{}, ErrNotInitialized
	}
	return UpdateProject(h.Root, clusterID, projectID, request)
}

// UpdateStandaloneProject changes display metadata for a project-owned store. Its
// reference is resolved across the home first so an ambiguous unscoped slug/name cannot
// accidentally update a project in either namespace.
func UpdateStandaloneProject(main, projectRef string, request UpdateProjectRequest) (Project, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return Project{}, err
	}
	var updated Project
	err = mutate(root, false, func(_ string, manifest *Manifest) error {
		project, _, standalone, err := findProjectInManifest(manifest, projectRef)
		if err != nil {
			return err
		}
		if !standalone {
			return fmt.Errorf("%w: %s is cluster-owned", ErrProjectNotFound, projectRef)
		}
		if request.Name != nil {
			name := strings.TrimSpace(*request.Name)
			if !validName(name) {
				return fmt.Errorf("%w: invalid project name", ErrInvalidManifest)
			}
			project.Name = name
		}
		if request.Description != nil {
			description := strings.TrimSpace(*request.Description)
			if !validDescription(description) {
				return fmt.Errorf("%w: invalid project description", ErrInvalidManifest)
			}
			project.Description = description
		}
		currentSlug := project.Slug
		if request.Slug != nil {
			slug, err := requestedOrSuggestedStandaloneProjectSlug(*manifest, *request.Slug, project.Name, project.ID)
			if err != nil {
				return err
			}
			project.Slug = slug
		}
		aliases := append([]string(nil), project.SlugAliases...)
		if request.SlugAliases != nil {
			aliases = append([]string(nil), (*request.SlugAliases)...)
		}
		if currentSlug != "" && currentSlug != project.Slug {
			aliases = append(aliases, currentSlug)
		}
		aliases, err = normalizedSlugAliases(aliases, project.Slug)
		if err != nil {
			return err
		}
		project.SlugAliases = aliases
		if request.Kind != nil {
			kind := normalizeProjectKind(*request.Kind)
			if !validProjectKind(kind) {
				return fmt.Errorf("%w: %q", ErrInvalidProjectKind, *request.Kind)
			}
			project.Kind = kind
		}
		if err := validateManifest(*manifest); err != nil {
			return err
		}
		updated = *project
		return nil
	})
	if err != nil {
		return Project{}, err
	}
	return updated, nil
}

// UpdateStandaloneProject updates standalone metadata in this home.
func (h *Home) UpdateStandaloneProject(projectRef string, request UpdateProjectRequest) (Project, error) {
	if h == nil {
		return Project{}, ErrNotInitialized
	}
	return UpdateStandaloneProject(h.Root, projectRef, request)
}

// Relink explicitly moves a project to a present source directory. It preserves the stable
// project id, appends the old source path to aliases, and refreshes fingerprint/lastSeen.
// Relink is explicit precisely because a user may intentionally repoint a project across
// filesystem boundaries where a native directory fingerprint changes.
func Relink(main, clusterID, projectID, sourcePath string) (Project, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return Project{}, err
	}
	canonical, fingerprint, err := observeSource(sourcePath)
	if err != nil {
		return Project{}, err
	}
	var updated Project
	err = mutate(root, false, func(_ string, manifest *Manifest) error {
		cluster, err := findCluster(manifest, clusterID)
		if err != nil {
			return err
		}
		project, err := findProject(cluster, projectID)
		if err != nil {
			return err
		}
		project.Source.Path = canonical
		project.Source.Aliases = appendUniquePath(project.Source.Aliases, canonical)
		project.Source.Fingerprint = fingerprint
		project.Source.LastSeen = nowUTC()
		updated = *project
		return nil
	})
	if err != nil {
		return Project{}, err
	}
	return updated, nil
}

// Relink explicitly updates a project source in this home.
func (h *Home) Relink(clusterID, projectID, sourcePath string) (Project, error) {
	if h == nil {
		return Project{}, ErrNotInitialized
	}
	return Relink(h.Root, clusterID, projectID, sourcePath)
}

// RelinkStandaloneProject explicitly updates a project-owned source binding while
// preserving its durable ID and historical aliases.
func RelinkStandaloneProject(main, projectRef, sourcePath string) (Project, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return Project{}, err
	}
	canonical, fingerprint, err := observeSource(sourcePath)
	if err != nil {
		return Project{}, err
	}
	var updated Project
	err = mutate(root, false, func(_ string, manifest *Manifest) error {
		project, _, standalone, err := findProjectInManifest(manifest, projectRef)
		if err != nil {
			return err
		}
		if !standalone {
			return fmt.Errorf("%w: %s is cluster-owned", ErrProjectNotFound, projectRef)
		}
		project.Source.Path = canonical
		project.Source.Aliases = appendUniquePath(project.Source.Aliases, canonical)
		project.Source.Fingerprint = fingerprint
		project.Source.LastSeen = nowUTC()
		updated = *project
		return nil
	})
	if err != nil {
		return Project{}, err
	}
	return updated, nil
}

// RelinkStandaloneProject updates a project-owned source in this home.
func (h *Home) RelinkStandaloneProject(projectRef, sourcePath string) (Project, error) {
	if h == nil {
		return Project{}, ErrNotInitialized
	}
	return RelinkStandaloneProject(h.Root, projectRef, sourcePath)
}

// ResolveProject returns the project and its isolated data root. Resolving by source path
// observes its canonical filesystem fingerprint and, when that fingerprint identifies one
// moved project, atomically refreshes aliases/path/lastSeen. This is the only implicit
// relink path; an ambiguous source must be addressed by stable project id.
func ResolveProject(main string, request ResolveProjectRequest) (ProjectResolution, error) {
	if strings.TrimSpace(request.ProjectID) == "" && strings.TrimSpace(request.SourcePath) == "" {
		return ProjectResolution{}, fmt.Errorf("%w: projectID or sourcePath is required", ErrProjectNotFound)
	}
	root, err := resolveRoot(main)
	if err != nil {
		return ProjectResolution{}, err
	}

	if request.ProjectID != "" {
		return resolveByID(root, request.ClusterID, request.ProjectID)
	}
	canonical, fingerprint, err := observeSource(request.SourcePath)
	if err != nil {
		return ProjectResolution{}, err
	}
	return resolveBySource(root, request.ClusterID, canonical, fingerprint)
}

// ResolveProject resolves a project in this home.
func (h *Home) ResolveProject(request ResolveProjectRequest) (ProjectResolution, error) {
	if h == nil {
		return ProjectResolution{}, ErrNotInitialized
	}
	return ResolveProject(h.Root, request)
}

// ClusterDataRoot returns the physical root a cluster-scoped task store should use. It
// never returns a path from a malformed manifest or through a symlink/reparse point.
func ClusterDataRoot(main, clusterID string) (string, error) {
	home, err := Open(main)
	if err != nil {
		return "", err
	}
	manifest, err := home.Manifest()
	if err != nil {
		return "", err
	}
	cluster, err := findCluster(&manifest, clusterID)
	if err != nil {
		return "", err
	}
	return dataRoot(home.CarbonRoot, cluster.DataPath)
}

// ClusterDataRoot returns the data root for one cluster in this home.
func (h *Home) ClusterDataRoot(clusterID string) (string, error) {
	if h == nil {
		return "", ErrNotInitialized
	}
	return ClusterDataRoot(h.Root, clusterID)
}

// ProjectDataRoot returns the private task-store root for a uniquely resolved project.
// It accepts a stable ID, slug/alias, or display name across standalone and clustered
// projects; ambiguous unscoped references deliberately fail rather than choosing one.
func ProjectDataRoot(main, projectRef string) (string, error) {
	home, err := Open(main)
	if err != nil {
		return "", err
	}
	manifest, err := home.Manifest()
	if err != nil {
		return "", err
	}
	project, cluster, standalone, err := findProjectInManifest(&manifest, projectRef)
	if err != nil {
		return "", err
	}
	return resolvedProjectDataRoot(home.CarbonRoot, project, cluster, standalone)
}

// ProjectDataRoot returns a standalone or clustered project's private task-store root
// in this home.
func (h *Home) ProjectDataRoot(projectRef string) (string, error) {
	if h == nil {
		return "", ErrNotInitialized
	}
	return ProjectDataRoot(h.Root, projectRef)
}

func resolveByID(root, clusterRef, projectRef string) (ProjectResolution, error) {
	home, err := Open(root)
	if err != nil {
		return ProjectResolution{}, err
	}
	manifest, err := home.Manifest()
	if err != nil {
		return ProjectResolution{}, err
	}
	var project *Project
	var cluster *Cluster
	standalone := false
	if strings.TrimSpace(clusterRef) == "" {
		project, cluster, standalone, err = findProjectInManifest(&manifest, projectRef)
	} else {
		cluster, err = findCluster(&manifest, clusterRef)
		if err == nil {
			project, err = findProject(cluster, projectRef)
		}
	}
	if err != nil {
		return ProjectResolution{}, err
	}
	rootPath, err := resolvedProjectDataRoot(home.CarbonRoot, project, cluster, standalone)
	if err != nil {
		return ProjectResolution{}, err
	}
	resolution := ProjectResolution{Project: *project, DataRoot: rootPath, SourcePath: project.Source.Path, Offline: true, Standalone: standalone}
	if cluster != nil {
		resolution.Cluster = *cluster
	}
	canonical, fingerprint, err := observeSource(project.Source.Path)
	if err == nil {
		if fingerprint != project.Source.Fingerprint {
			return ProjectResolution{}, fmt.Errorf("%w: project %s at %s", ErrProjectSourceMismatch, project.ID, canonical)
		}
		resolution.SourcePath = canonical
		resolution.Offline = false
		return resolution, nil
	}
	// A vanished source is legitimately offline. Other failures (a file replacing
	// the directory, an unsafe link, or an access error) are not an executable
	// offline state and must remain visible to callers.
	if _, statErr := os.Lstat(project.Source.Path); errors.Is(statErr, os.ErrNotExist) {
		return resolution, nil
	}
	return ProjectResolution{}, fmt.Errorf("%w: project %s source cannot be verified: %v", ErrProjectSourceMismatch, project.ID, err)
}

func resolveBySource(root, clusterRef, canonical, fingerprint string) (ProjectResolution, error) {
	var resolution ProjectResolution
	err := mutate(root, false, func(carbonRoot string, manifest *Manifest) error {
		matches, err := projectsMatchingFingerprint(manifest, clusterRef, fingerprint)
		if err != nil {
			return err
		}
		if len(matches) == 0 {
			return fmt.Errorf("%w: %s", ErrProjectNotFound, canonical)
		}
		if len(matches) > 1 {
			return fmt.Errorf("%w: %s", ErrAmbiguousProject, canonical)
		}
		match := matches[0]
		project := match.project
		// A matching fingerprint may be an ordinary reopen or a directory move. In both
		// cases canonical metadata is refreshed under the write lock.
		project.Source.Path = canonical
		project.Source.Aliases = appendUniquePath(project.Source.Aliases, canonical)
		project.Source.LastSeen = nowUTC()
		if match.standalone {
			if _, err := ensureStandaloneProjectStore(carbonRoot, project.ID, normalizePrefix("", project.Name)); err != nil {
				return err
			}
		} else if _, err := ensureClusterStore(carbonRoot, match.cluster.DataPath, match.cluster.Prefix); err != nil {
			return err
		}
		rootPath, err := resolvedProjectDataRoot(carbonRoot, project, match.cluster, match.standalone)
		if err != nil {
			return err
		}
		resolution = ProjectResolution{
			Project: *project, DataRoot: rootPath, SourcePath: canonical, Standalone: match.standalone,
		}
		if match.cluster != nil {
			resolution.Cluster = *match.cluster
		}
		return nil
	})
	return resolution, err
}

func resolvedProjectDataRoot(carbonRoot string, project *Project, cluster *Cluster, standalone bool) (string, error) {
	if standalone {
		relative, err := standaloneProjectDataPath(project.ID)
		if err != nil {
			return "", err
		}
		return dataRoot(carbonRoot, relative)
	}
	if cluster == nil {
		return "", fmt.Errorf("%w: clustered project %s has no cluster", ErrInvalidManifest, project.ID)
	}
	return dataRoot(carbonRoot, cluster.DataPath)
}

func mutate(root string, create bool, fn func(carbonRoot string, manifest *Manifest) error) error {
	return withLock(root, func() error {
		carbonRoot, exists, err := carbonDir(root, create)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotInitialized
		}
		manifest, manifestExists, err := readManifest(carbonRoot)
		if err != nil {
			return err
		}
		if !manifestExists {
			if !create {
				return ErrNotInitialized
			}
			id, err := newID("home", nil)
			if err != nil {
				return err
			}
			manifest = Manifest{Version: Version, ID: id, CreatedAt: nowUTC(), Clusters: []Cluster{}, Projects: []Project{}}
		}
		if err := fn(carbonRoot, &manifest); err != nil {
			return err
		}
		return writeManifest(carbonRoot, manifest)
	})
}

// findCluster resolves a canonical ID first, then a unique slug/alias, then a unique
// display name. Keeping this resolver beneath all home operations makes stable IDs the
// preferred path without forcing callers to pre-resolve human-facing selections.
func findCluster(manifest *Manifest, reference string) (*Cluster, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return nil, fmt.Errorf("%w: empty reference", ErrClusterNotFound)
	}
	for i := range manifest.Clusters {
		if manifest.Clusters[i].ID == reference {
			return &manifest.Clusters[i], nil
		}
	}
	slugMatches := make([]*Cluster, 0, 1)
	for i := range manifest.Clusters {
		cluster := &manifest.Clusters[i]
		if slugReferenceMatches(reference, cluster.Slug, cluster.SlugAliases) {
			slugMatches = append(slugMatches, cluster)
		}
	}
	if len(slugMatches) == 1 {
		return slugMatches[0], nil
	}
	if len(slugMatches) > 1 {
		return nil, fmt.Errorf("%w: %q", ErrAmbiguousCluster, reference)
	}
	nameMatches := make([]*Cluster, 0, 1)
	for i := range manifest.Clusters {
		cluster := &manifest.Clusters[i]
		if strings.EqualFold(reference, cluster.Name) {
			nameMatches = append(nameMatches, cluster)
		}
	}
	if len(nameMatches) == 1 {
		return nameMatches[0], nil
	}
	if len(nameMatches) > 1 {
		return nil, fmt.Errorf("%w: %q", ErrAmbiguousCluster, reference)
	}
	return nil, fmt.Errorf("%w: %s", ErrClusterNotFound, reference)
}

// findProject follows the same stable-id -> slug/alias -> display-name ordering as
// findCluster, but only within one cluster. A duplicate display name is allowed in
// storage for backwards compatibility and is intentionally an error at resolution.
func findProject(cluster *Cluster, reference string) (*Project, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return nil, fmt.Errorf("%w: empty reference", ErrProjectNotFound)
	}
	for i := range cluster.Projects {
		if cluster.Projects[i].ID == reference {
			return &cluster.Projects[i], nil
		}
	}
	slugMatches := make([]*Project, 0, 1)
	for i := range cluster.Projects {
		project := &cluster.Projects[i]
		if slugReferenceMatches(reference, project.Slug, project.SlugAliases) {
			slugMatches = append(slugMatches, project)
		}
	}
	if len(slugMatches) == 1 {
		return slugMatches[0], nil
	}
	if len(slugMatches) > 1 {
		return nil, fmt.Errorf("%w: %q", ErrAmbiguousProjectReference, reference)
	}
	nameMatches := make([]*Project, 0, 1)
	for i := range cluster.Projects {
		project := &cluster.Projects[i]
		if strings.EqualFold(reference, project.Name) {
			nameMatches = append(nameMatches, project)
		}
	}
	if len(nameMatches) == 1 {
		return nameMatches[0], nil
	}
	if len(nameMatches) > 1 {
		return nil, fmt.Errorf("%w: %q", ErrAmbiguousProjectReference, reference)
	}
	return nil, fmt.Errorf("%w: %s", ErrProjectNotFound, reference)
}

type projectMatch struct {
	project    *Project
	cluster    *Cluster
	standalone bool
}

// findProjectInManifest resolves an unscoped project reference across standalone and
// clustered entries. IDs are globally unique; slugs/aliases and display names must map
// to exactly one project or the caller receives an ambiguity error.
func findProjectInManifest(manifest *Manifest, reference string) (*Project, *Cluster, bool, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return nil, nil, false, fmt.Errorf("%w: empty reference", ErrProjectNotFound)
	}
	for i := range manifest.Projects {
		if manifest.Projects[i].ID == reference {
			return &manifest.Projects[i], nil, true, nil
		}
	}
	for clusterIndex := range manifest.Clusters {
		cluster := &manifest.Clusters[clusterIndex]
		for projectIndex := range cluster.Projects {
			if cluster.Projects[projectIndex].ID == reference {
				return &cluster.Projects[projectIndex], cluster, false, nil
			}
		}
	}
	collect := func(matches []projectMatch, match func(Project) bool) []projectMatch {
		for projectIndex := range manifest.Projects {
			project := &manifest.Projects[projectIndex]
			if match(*project) {
				matches = append(matches, projectMatch{project: project, standalone: true})
			}
		}
		for clusterIndex := range manifest.Clusters {
			cluster := &manifest.Clusters[clusterIndex]
			for projectIndex := range cluster.Projects {
				project := &cluster.Projects[projectIndex]
				if match(*project) {
					matches = append(matches, projectMatch{project: project, cluster: cluster})
				}
			}
		}
		return matches
	}
	slugMatches := collect(nil, func(project Project) bool {
		return slugReferenceMatches(reference, project.Slug, project.SlugAliases)
	})
	if len(slugMatches) == 1 {
		match := slugMatches[0]
		return match.project, match.cluster, match.standalone, nil
	}
	if len(slugMatches) > 1 {
		return nil, nil, false, fmt.Errorf("%w: %q", ErrAmbiguousProjectReference, reference)
	}
	nameMatches := collect(nil, func(project Project) bool {
		return strings.EqualFold(reference, project.Name)
	})
	if len(nameMatches) == 1 {
		match := nameMatches[0]
		return match.project, match.cluster, match.standalone, nil
	}
	if len(nameMatches) > 1 {
		return nil, nil, false, fmt.Errorf("%w: %q", ErrAmbiguousProjectReference, reference)
	}
	return nil, nil, false, fmt.Errorf("%w: %s", ErrProjectNotFound, reference)
}

// projectIDInManifest intentionally accepts only the stable ID form. It is used by
// metadata ownership checks where a mutable slug/display name must not become an alias
// for a durable project target.
func projectIDInManifest(manifest Manifest, id string) bool {
	for _, project := range manifest.Projects {
		if project.ID == id {
			return true
		}
	}
	for _, cluster := range manifest.Clusters {
		for _, project := range cluster.Projects {
			if project.ID == id {
				return true
			}
		}
	}
	return false
}

func projectsMatchingFingerprint(manifest *Manifest, clusterRef, fingerprint string) ([]projectMatch, error) {
	var matches []projectMatch
	if strings.TrimSpace(clusterRef) != "" {
		cluster, err := findCluster(manifest, clusterRef)
		if err != nil {
			return nil, err
		}
		for projectIndex := range cluster.Projects {
			project := &cluster.Projects[projectIndex]
			if project.Source.Fingerprint == fingerprint {
				matches = append(matches, projectMatch{project: project, cluster: cluster})
			}
		}
		return matches, nil
	}
	for projectIndex := range manifest.Projects {
		project := &manifest.Projects[projectIndex]
		if project.Source.Fingerprint == fingerprint {
			matches = append(matches, projectMatch{project: project, standalone: true})
		}
	}
	for clusterIndex := range manifest.Clusters {
		cluster := &manifest.Clusters[clusterIndex]
		for projectIndex := range cluster.Projects {
			project := &cluster.Projects[projectIndex]
			// A lexical source path is never enough to identify a project: a reused
			// directory must not silently become an old project. The directory identity
			// survives a same-volume move and may be shared by logical projects, which
			// turns the latter into an explicit ambiguity.
			if project.Source.Fingerprint == fingerprint {
				matches = append(matches, projectMatch{project: project, cluster: cluster})
			}
		}
	}
	return matches, nil
}

func slugReferenceMatches(reference, slug string, aliases []string) bool {
	if slug != "" && strings.EqualFold(reference, slug) {
		return true
	}
	for _, alias := range aliases {
		if strings.EqualFold(reference, alias) {
			return true
		}
	}
	return false
}

func normalizedName(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func validName(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validDescription(value string) bool {
	if len(value) > 8192 {
		return false
	}
	for _, r := range value {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' || r == 0x7f {
			return false
		}
	}
	return true
}

// validSlug accepts the canonical machine-safe form persisted in the manifest. Input
// is normalised to lower case before this function is used, while on-disk validation
// remains strict so aliases resolve predictably across Windows and Unix hosts.
func validSlug(value string) bool {
	if len(value) == 0 || len(value) > 64 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	previousDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			previousDash = false
		case r == '-' && !previousDash:
			previousDash = true
		default:
			return false
		}
	}
	return true
}

func normalizedRequestedSlug(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !validSlug(value) {
		return "", fmt.Errorf("%w: invalid machine-safe slug %q", ErrInvalidManifest, value)
	}
	return value, nil
}

func suggestedSlug(name, fallback string) string {
	var out strings.Builder
	separator := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if separator && out.Len() > 0 {
				out.WriteByte('-')
			}
			out.WriteRune(r)
			separator = false
		default:
			separator = out.Len() > 0
		}
	}
	value := strings.Trim(out.String(), "-")
	if value == "" {
		value = fallback
	}
	if len(value) > 64 {
		value = strings.TrimRight(value[:64], "-")
	}
	if !validSlug(value) {
		return fallback
	}
	return value
}

func normalizedSlugAliases(values []string, current string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(values))
	aliases := make([]string, 0, len(values))
	for _, value := range values {
		alias, err := normalizedRequestedSlug(value)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid slug alias", err)
		}
		if alias == current {
			// Promoting a historical alias to the current slug is safe; keeping it
			// twice only makes manifest validation needlessly ambiguous.
			continue
		}
		if _, exists := seen[alias]; exists {
			continue
		}
		seen[alias] = struct{}{}
		aliases = append(aliases, alias)
	}
	return aliases, nil
}

func requestedOrSuggestedClusterSlug(manifest Manifest, requested, name, exceptID string) (string, error) {
	if strings.TrimSpace(requested) != "" {
		slug, err := normalizedRequestedSlug(requested)
		if err != nil {
			return "", err
		}
		if clusterSlugInUse(manifest, slug, exceptID) {
			return "", fmt.Errorf("%w: duplicate cluster slug or alias %q", ErrInvalidManifest, slug)
		}
		return slug, nil
	}
	return nextAvailableSlug(suggestedSlug(name, "cluster"), func(candidate string) bool {
		return clusterSlugInUse(manifest, candidate, exceptID)
	}), nil
}

func requestedOrSuggestedProjectSlug(cluster *Cluster, requested, name, exceptID string) (string, error) {
	if strings.TrimSpace(requested) != "" {
		slug, err := normalizedRequestedSlug(requested)
		if err != nil {
			return "", err
		}
		if projectSlugInUse(cluster, slug, exceptID) {
			return "", fmt.Errorf("%w: duplicate project slug or alias %q", ErrInvalidManifest, slug)
		}
		return slug, nil
	}
	return nextAvailableSlug(suggestedSlug(name, "project"), func(candidate string) bool {
		return projectSlugInUse(cluster, candidate, exceptID)
	}), nil
}

func requestedOrSuggestedStandaloneProjectSlug(manifest Manifest, requested, name, exceptID string) (string, error) {
	if strings.TrimSpace(requested) != "" {
		slug, err := normalizedRequestedSlug(requested)
		if err != nil {
			return "", err
		}
		if standaloneProjectSlugInUse(manifest, slug, exceptID) {
			return "", fmt.Errorf("%w: duplicate standalone project slug or alias %q", ErrInvalidManifest, slug)
		}
		return slug, nil
	}
	return nextAvailableSlug(suggestedSlug(name, "project"), func(candidate string) bool {
		return standaloneProjectSlugInUse(manifest, candidate, exceptID)
	}), nil
}

func nextAvailableSlug(base string, inUse func(string) bool) string {
	if !inUse(base) {
		return base
	}
	for suffix := 2; ; suffix++ {
		tail := fmt.Sprintf("-%d", suffix)
		trimmed := strings.TrimRight(base, "-")
		if len(trimmed)+len(tail) > 64 {
			trimmed = strings.TrimRight(trimmed[:64-len(tail)], "-")
		}
		candidate := trimmed + tail
		if !inUse(candidate) {
			return candidate
		}
	}
}

func clusterSlugInUse(manifest Manifest, candidate, exceptID string) bool {
	for _, cluster := range manifest.Clusters {
		if cluster.ID == exceptID {
			continue
		}
		if slugReferenceMatches(candidate, cluster.Slug, cluster.SlugAliases) {
			return true
		}
	}
	return false
}

func projectSlugInUse(cluster *Cluster, candidate, exceptID string) bool {
	for _, project := range cluster.Projects {
		if project.ID == exceptID {
			continue
		}
		if slugReferenceMatches(candidate, project.Slug, project.SlugAliases) {
			return true
		}
	}
	return false
}

// standaloneProjectSlugInUse includes every clustered project. A standalone target is
// resolved without an enclosing cluster, so it cannot safely reuse any existing
// project slug/alias even though legacy clustered projects retain local namespaces.
func standaloneProjectSlugInUse(manifest Manifest, candidate, exceptID string) bool {
	for _, project := range manifest.Projects {
		if project.ID != exceptID && slugReferenceMatches(candidate, project.Slug, project.SlugAliases) {
			return true
		}
	}
	for _, cluster := range manifest.Clusters {
		if projectSlugInUse(&cluster, candidate, exceptID) {
			return true
		}
	}
	return false
}

func validProjectKind(kind ProjectKind) bool {
	if kind == "" || len(kind) > 64 || ProjectKind(strings.ToLower(string(kind))) != kind {
		return false
	}
	for i, r := range kind {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		if r == '-' && i > 0 && i < len(kind)-1 {
			continue
		}
		return false
	}
	return true
}

func normalizeProjectKind(kind ProjectKind) ProjectKind {
	value := strings.ToLower(strings.TrimSpace(string(kind)))
	if value == "" {
		return ProjectGeneric
	}
	return ProjectKind(value)
}

func normalizePrefix(prefix, fallback string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = fallback
	}
	var out strings.Builder
	for _, r := range strings.ToUpper(prefix) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			out.WriteRune(r)
		}
	}
	value := out.String()
	if value == "" {
		return "TASK"
	}
	if value[0] >= '0' && value[0] <= '9' {
		return "TASK" + value
	}
	return value
}

func validPrefix(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for i, r := range value {
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func validID(id, prefix string) bool {
	wantPrefix := prefix + "_"
	if !strings.HasPrefix(id, wantPrefix) || len(id) != len(wantPrefix)+32 {
		return false
	}
	for _, r := range id[len(wantPrefix):] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func allIDs(manifest Manifest) map[string]struct{} {
	ids := make(map[string]struct{}, 1+len(manifest.Clusters)+len(manifest.Projects))
	ids[manifest.ID] = struct{}{}
	for _, project := range manifest.Projects {
		ids[project.ID] = struct{}{}
	}
	for _, cluster := range manifest.Clusters {
		ids[cluster.ID] = struct{}{}
		for _, project := range cluster.Projects {
			ids[project.ID] = struct{}{}
		}
	}
	return ids
}

var (
	randomReader io.Reader = rand.Reader
	clock                  = time.Now
)

func newID(prefix string, used map[string]struct{}) (string, error) {
	for range 32 {
		bytes := make([]byte, 16)
		if _, err := io.ReadFull(randomReader, bytes); err != nil {
			return "", fmt.Errorf("carbon: generate %s id: %w", prefix, err)
		}
		id := prefix + "_" + hex.EncodeToString(bytes)
		if _, exists := used[id]; !exists {
			return id, nil
		}
	}
	return "", fmt.Errorf("carbon: random %s id collision limit reached", prefix)
}

func nowUTC() string { return clock().UTC().Format(time.RFC3339Nano) }
