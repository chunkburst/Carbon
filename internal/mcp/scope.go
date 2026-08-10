package mcp

// Scope binds a service instance to one Carbon task pool. Carbon clusters share a
// physical task store, while ProjectID limits ordinary reads and all writes to one
// project in that pool. Legacy services leave every field empty and continue to
// operate on their one standalone legacy .cairn workspace.
//
// Scope is deliberately transport-neutral: cmd, Streamable HTTP, and the web API
// resolve home paths and stable IDs before constructing a Service. Keeping only the
// already-resolved identifiers here means the task engine never needs to know about
// machine-local Carbon home manifests.
type Scope struct {
	Home       string
	ClusterID  string
	ProjectID  string
	SourcePath string
	// Standalone marks a top-level Carbon project. Standalone projects own a
	// private data root rather than participating in a cluster's shared pool, so
	// an empty ClusterID must not make this scope look like legacy mode.
	Standalone bool
	// ClusterScope records an intentionally selected cluster-only connection.
	// It prevents a Carbon default that lacks a project from silently creating
	// shared tasks. It has no effect on the historical Legacy constructors.
	ClusterScope bool
	// CompatLayer is the optional caller-selected compatibility layer. Transports
	// validate it through internal/compat before serving mutations; an empty value
	// selects frozen legacy v1 for --repo or approved stable Carbon v2 for a home.
	CompatLayer string
	Legacy      bool
}

// ProjectRootResolver maps a stable project id in the bound Carbon cluster to its
// source directory. It is supplied by the home-aware adapter, never persisted in the
// task store. A nil resolver is acceptable for legacy services and for Carbon reads
// that never run checks or start sessions.
type ProjectRootResolver func(projectID string) (string, error)

// IsCarbon reports whether this scope addresses a Carbon data store rather than a
// legacy project-local .cairn store. A standalone project intentionally has no
// ClusterID, but remains a Carbon scope with the stable v2 safety rules.
func (s Scope) IsCarbon() bool { return !s.Legacy && (s.ClusterID != "" || s.IsStandalone()) }

// IsStandalone reports whether this scope is one isolated top-level Carbon project.
// It is deliberately explicit: a malformed empty-cluster scope must not accidentally
// acquire Carbon authority merely because it happens to contain a project-looking id.
func (s Scope) IsStandalone() bool {
	return !s.Legacy && s.Standalone && s.Home != "" && s.ClusterID == "" && s.ProjectID != ""
}

// IsCarbonHome reports a Carbon home catalog scope, including a home-only discovery
// connection before a concrete cluster has been selected.
func (s Scope) IsCarbonHome() bool { return !s.Legacy && s.Home != "" }

// IsProject reports whether this scope is narrowed to exactly one project. Mutating
// operations require this in Carbon mode so a caller cannot accidentally create an
// unlabelled task in a cluster-wide shared pool.
func (s Scope) IsProject() bool { return s.ProjectID != "" }

// IsExplicitCluster reports that a caller deliberately selected the shared cluster
// pool rather than merely inheriting an empty project default. Creation of a
// cluster-wide task requires this plus an explicit empty project_id input.
func (s Scope) IsExplicitCluster() bool {
	return s.IsCarbon() && s.ProjectID == "" && s.ClusterScope
}

// ScopeMetadata is safe to return from identity and catalog tools. It gives agents
// canonical opaque IDs and the selected boundary without leaking any task contents
// or credentials.
type ScopeMetadata struct {
	Mode            string `json:"mode"`
	Home            string `json:"home,omitempty"`
	ClusterID       string `json:"clusterId,omitempty"`
	ProjectID       string `json:"projectId,omitempty"`
	SourcePath      string `json:"sourcePath,omitempty"`
	Standalone      bool   `json:"standalone,omitempty"`
	ExplicitCluster bool   `json:"explicitCluster,omitempty"`
}

// Metadata turns the transport-neutral scope into its public identity shape.
func (s Scope) Metadata() ScopeMetadata {
	mode := "legacy"
	if s.IsCarbonHome() {
		mode = "carbon_home"
	}
	if s.IsCarbon() {
		mode = "carbon"
	}
	return ScopeMetadata{
		Mode:            mode,
		Home:            s.Home,
		ClusterID:       s.ClusterID,
		ProjectID:       s.ProjectID,
		SourcePath:      s.SourcePath,
		Standalone:      s.IsStandalone(),
		ExplicitCluster: s.IsExplicitCluster(),
	}
}
