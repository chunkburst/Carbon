package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"carbon/internal/compat"
	"carbon/internal/home"
	"carbon/internal/mcp"
)

// ScopeDefaults is the resolved launch-time default. A desktop sidecar should pass
// DefaultHome explicitly; command-line callers can still deliberately select the
// legacy project-local store with --repo. The zero value preserves the pre-Carbon
// behaviour of New.
type ScopeDefaults struct {
	Home       string
	ClusterID  string
	ProjectID  string
	LegacyRoot string
	// HomeByDefault selects a Carbon home even when a request contains no scope
	// query parameters. It is used by `carbon web --home …`; legacy New() leaves it
	// false so existing callers keep their project-local default.
	HomeByDefault bool
}

// requestScope is the adapter's fully resolved boundary. Root is the physical store
// root: a legacy repository in legacy mode or the cluster data root in Carbon mode.
// ProjectID is a default project/filter, not a security boundary: a cluster is the
// boundary and cluster-wide tasks intentionally have an empty project id. SourcePath
// is deliberately separate because checks, sessions, and integration config must
// operate in source code, never in Carbon's shared data directory.
type requestScope struct {
	Mode       string
	Home       string
	ClusterID  string
	ProjectID  string
	Root       string
	SourcePath string
	// Standalone is true when Root belongs to an isolated top-level project rather
	// than a cluster's shared task pool. Such a scope has no valid sibling expansion.
	Standalone bool
	// SourceOffline is informational for read-only requests. Execution helpers must
	// resolve the project again and reject it rather than falling back to Root.
	SourceOffline bool
	Legacy        bool
}

// scopeDTO is intentionally small and stable: the UI and remote MCP clients can use
// it to verify the selected Carbon home/cluster/project without learning any private
// home-manifest implementation details.
type scopeDTO struct {
	Mode          string `json:"mode"`
	Home          string `json:"home,omitempty"`
	ClusterID     string `json:"clusterId,omitempty"`
	ProjectID     string `json:"projectId,omitempty"`
	Root          string `json:"root,omitempty"`
	SourceOffline bool   `json:"sourceOffline,omitempty"`
	Standalone    bool   `json:"standalone,omitempty"`
	Legacy        bool   `json:"legacy"`
}

func scopeDTOFrom(scope requestScope) scopeDTO {
	return scopeDTO{
		Mode:          scope.Mode,
		Home:          scope.Home,
		ClusterID:     scope.ClusterID,
		ProjectID:     scope.ProjectID,
		Root:          scope.Root,
		SourceOffline: scope.SourceOffline,
		Standalone:    scope.Standalone,
		Legacy:        scope.Legacy,
	}
}

func carbonCapabilities() []string {
	return compat.Capabilities(compat.StableLayer)
}

// compatModeForDefaults describes the launch-time default only. A request can still
// select another valid scope, in which case compatModeForScope below chooses the
// scope-appropriate default instead of inheriting an unrelated launch mode.
func compatModeForDefaults(defaults ScopeDefaults) compat.Mode {
	if defaults.HomeByDefault || defaults.Home != "" || defaults.ClusterID != "" || defaults.ProjectID != "" {
		return compat.ModeCarbon
	}
	return compat.ModeLegacy
}

func compatModeForScope(scope requestScope) compat.Mode {
	if scope.Legacy || scope.Mode == "legacy" {
		return compat.ModeLegacy
	}
	return compat.ModeCarbon
}

func (s requestScope) mcpScope() mcp.Scope {
	return mcp.Scope{
		Home:       s.Home,
		ClusterID:  s.ClusterID,
		ProjectID:  s.ProjectID,
		SourcePath: s.SourcePath,
		Standalone: s.Standalone,
		Legacy:     s.Legacy,
	}
}

func (s requestScope) hasStore() bool   { return s.Root != "" }
func (s requestScope) hasProject() bool { return s.ProjectID != "" }

// isHomeOnly is the deliberately read/catalog-only Carbon connection. It has no
// cluster task store, so adapters must never initialize a .carbon data root for it.
func (s requestScope) isHomeOnly() bool {
	return s.Mode == "carbon" && !s.Legacy && s.Home != "" && s.ClusterID == "" && s.ProjectID == "" && !s.Standalone && s.Root == ""
}

// scopeValue returns a query value first and then a transport header. Headers make
// a bound desktop/client scope convenient without weakening the explicit query API.
func scopeValue(r *http.Request, query, header string) string {
	if value := strings.TrimSpace(r.URL.Query().Get(query)); value != "" {
		return value
	}
	return strings.TrimSpace(r.Header.Get(header))
}

// resolveScope resolves either the new stable-ID Carbon boundary or the established
// `?path`/`?repo` legacy workspace. A request that mixes the two is rejected instead
// of silently choosing a storage location. It does not initialize a home, cluster, or
// task store; read endpoints therefore remain side-effect free.
func (s *Server) resolveScope(r *http.Request) (scope requestScope, err error) {
	// A server can receive per-request legacy and Carbon scopes. Validate the
	// selected compatibility layer after resolving that boundary so a process
	// launched with an explicit layer cannot be used to cross into the other
	// contract through query parameters or headers.
	defer func() {
		if err != nil {
			return
		}
		if _, compatErr := compat.Resolve(s.productVersion, s.compatLayer, compatModeForScope(scope)); compatErr != nil {
			scope = requestScope{}
			err = compatErr
		}
	}()

	q := r.URL.Query()
	legacyPath := strings.TrimSpace(q.Get("path"))
	if repo := strings.TrimSpace(q.Get("repo")); repo != "" {
		if legacyPath != "" && legacyPath != repo {
			return requestScope{}, errors.New("path and repo must name the same legacy project")
		}
		legacyPath = repo
	}
	homeRoot := scopeValue(r, "home", "X-Carbon-Home")
	clusterID := scopeValue(r, "cluster", "X-Carbon-Cluster")
	projectID := scopeValue(r, "project", "X-Carbon-Project")
	// An explicit project without a cluster is the standalone selection contract.
	// Do not inject a launch-time default cluster before resolving it, or a web
	// server started on a shared pool could never switch to a private project.
	if clusterID == "" && projectID == "" && legacyPath == "" {
		clusterID = s.defaultCluster
	}
	if projectID == "" && legacyPath == "" {
		projectID = s.defaultProject
	}

	if legacyPath != "" && (homeRoot != "" || clusterID != "" || projectID != "") {
		return requestScope{}, errors.New("legacy path/repo cannot be combined with Carbon home, cluster, or project scope")
	}
	// An explicitly selected cluster is always Carbon. A default home only becomes
	// Carbon-by-default for the new CLI mode; the historical New(root, actor) stays
	// legacy until a caller opts in through ScopeDefaults.
	if projectID != "" && clusterID == "" {
		if homeRoot == "" {
			homeRoot = s.defaultHome
		}
		if homeRoot == "" {
			return requestScope{}, errors.New("Carbon standalone project scope requires a home path")
		}
		resolvedHome, err := s.resolveRoot(homeRoot)
		if err != nil {
			return requestScope{}, err
		}
		resolution, err := home.ResolveProject(resolvedHome, home.ResolveProjectRequest{ProjectID: projectID})
		if err != nil {
			return requestScope{}, err
		}
		if !resolution.Standalone {
			return requestScope{}, fmt.Errorf("project %s belongs to cluster %s; select an explicit cluster scope", projectID, resolution.Cluster.ID)
		}
		scope = requestScope{
			Mode: "carbon", Home: resolvedHome, ProjectID: resolution.Project.ID,
			Root: resolution.DataRoot, Standalone: true,
		}
		if resolution.Offline {
			scope.SourceOffline = true
			return scope, nil
		}
		scope.SourcePath = resolution.SourcePath
		return scope, nil
	}

	if clusterID != "" {
		if homeRoot == "" {
			homeRoot = s.defaultHome
		}
		if homeRoot == "" {
			return requestScope{}, errors.New("Carbon cluster scope requires a home path")
		}
		resolvedHome, err := s.resolveRoot(homeRoot)
		if err != nil {
			return requestScope{}, err
		}
		cluster, err := home.ResolveCluster(resolvedHome, clusterID)
		if err != nil {
			return requestScope{}, err
		}
		clusterID = cluster.ID
		dataRoot, err := home.ClusterDataRoot(resolvedHome, clusterID)
		if err != nil {
			return requestScope{}, err
		}
		scope = requestScope{Mode: "carbon", Home: resolvedHome, ClusterID: clusterID, ProjectID: projectID, Root: dataRoot}
		if projectID == "" {
			return scope, nil
		}
		resolution, err := home.ResolveProject(resolvedHome, home.ResolveProjectRequest{ClusterID: clusterID, ProjectID: projectID})
		if err != nil {
			return requestScope{}, err
		}
		if resolution.Standalone {
			return requestScope{}, errors.New("standalone project cannot be selected through a cluster scope")
		}
		// ResolveProject is the authoritative membership check. Its data root must
		// agree with ClusterDataRoot, otherwise fail closed rather than risking a
		// task pool from another cluster.
		if !sameScopePath(dataRoot, resolution.DataRoot) {
			return requestScope{}, errors.New("project resolved outside its cluster data root")
		}
		scope.ProjectID = resolution.Project.ID
		if resolution.Offline {
			scope.SourceOffline = true
			return scope, nil
		}
		scope.SourcePath = resolution.SourcePath
		return scope, nil
	}

	if homeRoot != "" || (s.homeByDefault && s.defaultHome != "") {
		if homeRoot == "" {
			homeRoot = s.defaultHome
		}
		resolvedHome, err := s.resolveRoot(homeRoot)
		if err != nil {
			return requestScope{}, err
		}
		return requestScope{Mode: "carbon", Home: resolvedHome}, nil
	}

	if legacyPath == "" {
		legacyPath = s.defaultRoot
	}
	root, err := s.resolveRoot(legacyPath)
	if err != nil {
		return requestScope{}, err
	}
	return requestScope{Mode: "legacy", Root: root, Legacy: true}, nil
}

func sameScopePath(left, right string) bool {
	// Both paths were produced by canonicalizing helpers. A string comparison is
	// enough on Unix; on Windows use a case-insensitive comparison to honor the
	// filesystem's usual case behavior without importing a second path policy.
	return strings.EqualFold(left, right)
}

// defaultProject resolves a create request's effective project id. An empty return is
// valid for a deliberately cluster-wide task; adapters only require a project later
// when an operation needs a source-code execution directory (checks/sessions/connect).
func defaultProject(scope requestScope, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if scope.Mode != "carbon" {
		if requested != "" {
			return "", fmt.Errorf("project_id is only valid in a Carbon cluster scope")
		}
		return "", nil
	}
	if requested == "" {
		return scope.ProjectID, nil
	}
	return requested, nil
}

func scopeErrStatus(err error) int {
	switch {
	case errors.Is(err, home.ErrClusterNotFound), errors.Is(err, home.ErrProjectNotFound), errors.Is(err, home.ErrNotInitialized):
		return http.StatusNotFound
	case errors.Is(err, home.ErrAmbiguousProject):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}
