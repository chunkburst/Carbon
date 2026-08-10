package connect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"carbon/internal/compat"
)

// ErrUnsafeConfigPath means a generated agent-config path is outside its explicit
// write root or includes a symlink/reparse-point/non-directory component.
var ErrUnsafeConfigPath = errors.New("unsafe agent config path")

// AgentStatus is the per-agent view returned to the UI.
type AgentStatus struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Mode       Mode   `json:"mode"`
	Installed  bool   `json:"installed"`
	Connected  bool   `json:"connected"`
	TargetPath string `json:"targetPath,omitempty"`
	DocsURL    string `json:"docsURL,omitempty"`
}

// Guide is a manual-setup snippet for one agent: the exact file content to write and the
// path to write it to.
type Guide struct {
	Path   string `json:"path,omitempty"`
	Lang   string `json:"lang"`
	Config string `json:"config"`
}

// CarbonScopeMode controls whether a Carbon MCP server is pinned to a project, is
// deliberately allowed to see a whole cluster, or starts an explicit project-routing
// session.
type CarbonScopeMode string

const (
	// CarbonScopeModeProject is the default and requires ProjectID. Keeping the default
	// narrow prevents an agent-config file from accidentally gaining access to every
	// project in a cluster.
	CarbonScopeModeProject CarbonScopeMode = "project"
	// CarbonScopeModeCluster is an explicit opt-in to a cluster-wide MCP server.
	CarbonScopeModeCluster CarbonScopeMode = "cluster"
	// CarbonScopeModeStandalone binds an agent to one isolated top-level project.
	// It intentionally serializes --home plus --project without a --cluster flag.
	CarbonScopeModeStandalone CarbonScopeMode = "standalone"
	// CarbonScopeModeSession starts from a Carbon home and lets the MCP service select
	// a project during its own lifecycle. It intentionally serializes neither a
	// cluster nor a project identifier, so reconnecting an agent does not freeze its
	// future work to whichever project happened to contain the local config file.
	CarbonScopeModeSession CarbonScopeMode = "session"

	// CarbonCompatLayer is the approved stable v2 compatibility layer required by
	// generated Carbon agent configurations. Product versions are not
	// compatibility-layer values; frozen legacy --repo connections omit this flag.
	CarbonCompatLayer = compat.StableLayer
)

// CarbonScope is the stable cluster/project launch contract written into an agent's
// local MCP configuration. A project binding is required unless the caller explicitly
// chooses a shared cluster scope or a project-routing session. Omitting ClusterID with
// a concrete ProjectID selects one isolated standalone project. A session has a Home
// only and intentionally serializes --project-session instead of cluster/project ids.
// SourceRoot is intentionally only the location of the agent config file; it is never
// passed to `carbon serve` as a data-store root.
type CarbonScope struct {
	Home      string
	ClusterID string
	ProjectID string
	// ScopeMode defaults to CarbonScopeModeProject. CarbonScopeModeCluster and
	// CarbonScopeModeSession deliberately omit --project and must be selected by the
	// caller; neither is inferred from an empty ProjectID.
	ScopeMode CarbonScopeMode
	// AllowClusterScope is a compatibility escape hatch for callers that cannot yet
	// send ScopeMode. It is intentionally opt-in and only takes effect with no project.
	AllowClusterScope bool
}

// Detect returns the status of every known agent for repo — whether it looks installed and
// whether its config already points at Carbon. Installed agents sort first.
func Detect(repo string) ([]AgentStatus, error) {
	s, err := defaultSys()
	if err != nil {
		return nil, err
	}
	return detectWith(s, repo), nil
}

func detectWith(s sys, repo string) []AgentStatus {
	return detectWithMatch(s, repo, func(f format, config []byte) bool {
		return f.connected(config, repo)
	})
}

// DetectCarbon returns the per-agent connection state for one exact Carbon scope. A
// local config which points to another home, cluster, project, or compatibility layer
// is intentionally reported disconnected rather than being treated as interchangeable.
func DetectCarbon(sourceRoot string, scope CarbonScope) ([]AgentStatus, error) {
	if _, err := normalizeCarbonScope(scope); err != nil {
		return nil, err
	}
	s, err := defaultSys()
	if err != nil {
		return nil, err
	}
	return detectCarbonWith(s, sourceRoot, scope), nil
}

func detectCarbonWith(s sys, sourceRoot string, scope CarbonScope) []AgentStatus {
	return detectWithMatch(s, sourceRoot, func(f format, config []byte) bool {
		return f.connectedCarbon(config, scope)
	})
}

func detectWithMatch(s sys, targetRoot string, matches func(format, []byte) bool) []AgentStatus {
	reg := registry()
	out := make([]AgentStatus, 0, len(reg))
	for _, a := range reg {
		st := AgentStatus{ID: a.id, Name: a.name, Mode: a.mode, DocsURL: a.docsURL}
		if a.detect != nil {
			st.Installed = a.detect(s)
		}
		if a.target != nil {
			st.TargetPath = a.target(s, targetRoot)
			if a.format != nil {
				trustedRoot := targetRoot
				if a.trustedRoot != nil {
					trustedRoot = a.trustedRoot(s, targetRoot)
				}
				if safePath, err := trustedConfigPath(trustedRoot, st.TargetPath, false); err == nil {
					b, err := os.ReadFile(safePath)
					if err == nil {
						st.Connected = matches(a.format, b)
					}
				}
			}
		}
		out = append(out, st)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Installed && !out[j].Installed
	})
	return out
}

// Connect merges the Carbon MCP entry into agentID's config for repo, stamping actor. It
// writes atomically, backs up any existing file to <path>.bak, then verifies the entry is
// present by re-reading. Returns the path written.
func Connect(agentID, repo, actor string) (string, error) {
	s, err := defaultSys()
	if err != nil {
		return "", err
	}
	bin, err := selfPath()
	if err != nil {
		return "", err
	}
	return connectWith(s, bin, agentID, repo, actor)
}

// ConnectCarbon writes a Carbon-scoped stdio MCP entry into a selected source
// configuration. The resulting process opens the shared cluster data root via stable
// home/cluster ids. The launch is pinned to ProjectID unless the caller explicitly opts
// into CarbonScopeModeCluster (or AllowClusterScope for a compatibility caller).
func ConnectCarbon(agentID, sourceRoot, actor string, scope CarbonScope) (string, error) {
	s, err := defaultSys()
	if err != nil {
		return "", err
	}
	bin, err := selfPath()
	if err != nil {
		return "", err
	}
	return connectCarbonWith(s, bin, agentID, sourceRoot, actor, scope)
}

func connectCarbonWith(s sys, bin, agentID, sourceRoot, actor string, scope CarbonScope) (string, error) {
	cfg, err := carbonServerConfig(bin, agentID, actor, scope)
	if err != nil {
		return "", err
	}
	return connectWithConfigVerified(s, agentID, sourceRoot, cfg, func(f format, config []byte) bool {
		return f.connectedCarbon(config, scope)
	})
}

func connectWith(s sys, bin, agentID, repo, actor string) (string, error) {
	return connectWithConfig(s, agentID, repo, newServerConfig(bin, repo, resolveActor(agentID, actor)))
}

func connectWithConfig(s sys, agentID, targetRoot string, cfg serverConfig) (string, error) {
	return connectWithConfigVerified(s, agentID, targetRoot, cfg, nil)
}

// connectWithConfigVerified preserves the legacy presence-only verification for callers
// that pass nil. Carbon callers pass a scope-aware verifier so a raced or malformed write
// cannot be accepted merely because a Carbon key is present.
func connectWithConfigVerified(s sys, agentID, targetRoot string, cfg serverConfig, verify func(format, []byte) bool) (string, error) {
	a, ok := find(agentID)
	if !ok {
		return "", fmt.Errorf("unknown agent %q", agentID)
	}
	if a.mode != ModeAuto || a.format == nil || a.target == nil {
		return "", fmt.Errorf("agent %q has no auto-connect; use the manual guide", agentID)
	}
	path := a.target(s, targetRoot)
	trustedRoot := targetRoot
	if a.trustedRoot != nil {
		trustedRoot = a.trustedRoot(s, targetRoot)
	}
	safePath, err := trustedConfigPath(trustedRoot, path, true)
	if err != nil {
		return "", err
	}
	existing, err := os.ReadFile(safePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read agent config %s: %w", path, err)
	}
	next, err := a.format.upsert(existing, cfg)
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(trustedRoot, path, existing, next); err != nil {
		return "", err
	}
	safePath, err = trustedConfigPath(trustedRoot, path, false)
	if err != nil {
		return "", err
	}
	if verify == nil {
		verify = func(f format, config []byte) bool { return f.has(config) }
	}
	if got, err := os.ReadFile(safePath); err != nil || !verify(a.format, got) {
		return "", fmt.Errorf("wrote %s but could not verify the Carbon entry", path)
	}
	return path, nil
}

// Disconnect removes Carbon's canonical entry and its legacy Cairn fallback from agentID's
// config for repo, leaving the file and
// any other MCP servers intact (backing up to <path>.bak first). It's a no-op if the file is
// absent or already has no Carbon entry. Returns the config path.
func Disconnect(agentID, repo string) (string, error) {
	s, err := defaultSys()
	if err != nil {
		return "", err
	}
	return disconnectWith(s, agentID, repo, func(f format, config []byte) bool {
		// A stale or malformed legacy Cairn key is removed during cleanup too.
		return f.has(config)
	})
}

// DisconnectCarbon removes the Carbon entry only when it is bound to exactly scope. It
// will not disconnect an agent configuration that belongs to another Carbon home,
// cluster, project, or compatibility layer.
func DisconnectCarbon(agentID, sourceRoot string, scope CarbonScope) (string, error) {
	if _, err := normalizeCarbonScope(scope); err != nil {
		return "", err
	}
	s, err := defaultSys()
	if err != nil {
		return "", err
	}
	return disconnectCarbonWith(s, agentID, sourceRoot, scope)
}

func disconnectCarbonWith(s sys, agentID, sourceRoot string, scope CarbonScope) (string, error) {
	return disconnectWith(s, agentID, sourceRoot, func(f format, config []byte) bool {
		return f.connectedCarbon(config, scope)
	})
}

func disconnectWith(s sys, agentID, targetRoot string, matches func(format, []byte) bool) (string, error) {
	a, ok := find(agentID)
	if !ok {
		return "", fmt.Errorf("unknown agent %q", agentID)
	}
	if a.format == nil || a.target == nil {
		return "", fmt.Errorf("agent %q has no config to disconnect", agentID)
	}
	path := a.target(s, targetRoot)
	trustedRoot := targetRoot
	if a.trustedRoot != nil {
		trustedRoot = a.trustedRoot(s, targetRoot)
	}
	safePath, err := trustedConfigPath(trustedRoot, path, false)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil // no config parent/file to remove from
	}
	if err != nil {
		return "", err
	}
	existing, err := os.ReadFile(safePath)
	if err != nil || !matches(a.format, existing) {
		return path, nil // nothing to remove
	}
	next, err := a.format.remove(existing)
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(trustedRoot, path, existing, next); err != nil {
		return "", err
	}
	safePath, err = trustedConfigPath(trustedRoot, path, false)
	if err != nil {
		return "", err
	}
	if got, err := os.ReadFile(safePath); err != nil || a.format.has(got) {
		return "", fmt.Errorf("removed Carbon from %s but it's still present", path)
	}
	return path, nil
}

// ManualGuide renders the standalone config text auto-connect would write for agentID, for
// display when the user wants to wire it up by hand.
func ManualGuide(agentID, repo, actor string) (Guide, error) {
	s, err := defaultSys()
	if err != nil {
		return Guide{}, err
	}
	a, ok := find(agentID)
	if !ok || a.format == nil {
		return Guide{}, fmt.Errorf("no guide for agent %q", agentID)
	}
	bin, err := selfPath()
	if err != nil {
		bin = serverName // fall back to bare name in the snippet
	}
	b, err := a.format.upsert(nil, newServerConfig(bin, repo, resolveActor(agentID, actor)))
	if err != nil {
		return Guide{}, err
	}
	g := Guide{Lang: a.format.lang(), Config: string(b)}
	if a.target != nil {
		g.Path = a.target(s, repo)
	}
	return g, nil
}

// ManualGuideCarbon is the non-writing counterpart to ConnectCarbon.
func ManualGuideCarbon(agentID, sourceRoot, actor string, scope CarbonScope) (Guide, error) {
	s, err := defaultSys()
	if err != nil {
		return Guide{}, err
	}
	a, ok := find(agentID)
	if !ok || a.format == nil {
		return Guide{}, fmt.Errorf("no guide for agent %q", agentID)
	}
	bin, err := selfPath()
	if err != nil {
		bin = serverName
	}
	cfg, err := carbonServerConfig(bin, agentID, actor, scope)
	if err != nil {
		return Guide{}, err
	}
	b, err := a.format.upsert(nil, cfg)
	if err != nil {
		return Guide{}, err
	}
	guide := Guide{Lang: a.format.lang(), Config: string(b)}
	if a.target != nil {
		guide.Path = a.target(s, sourceRoot)
	}
	return guide, nil
}

// carbonServerConfig is shared by auto-connect and the manual guide so their process
// contracts cannot drift. Carbon configs are project-bound by default; omitting
// --project requires a deliberate cluster-scope or project-session opt-in.
func carbonServerConfig(bin, agentID, actor string, scope CarbonScope) (serverConfig, error) {
	normalized, err := normalizeCarbonScope(scope)
	if err != nil {
		return serverConfig{}, err
	}
	args := []string{
		"serve", "--actor", resolveActor(agentID, actor),
		"--home", normalized.Home,
	}
	if normalized.ClusterID != "" {
		args = append(args, "--cluster", normalized.ClusterID)
	}
	if normalized.ProjectID != "" {
		args = append(args, "--project", normalized.ProjectID)
	}
	if normalized.ScopeMode == CarbonScopeModeSession {
		args = append(args, "--project-session")
	}
	args = append(args, "--compat-layer", CarbonCompatLayer)
	return serverConfig{Name: serverName, Bin: bin, Args: args}, nil
}

// normalizeCarbonScope validates the narrow-by-default launch boundary and canonicalizes
// the one filesystem value before it is serialized into a local agent config. Cluster and
// project identifiers are stable metadata keys, so they remain exact/case-sensitive.
func normalizeCarbonScope(scope CarbonScope) (CarbonScope, error) {
	scope.Home = canonicalLocalPath(scope.Home)
	scope.ClusterID = strings.TrimSpace(scope.ClusterID)
	scope.ProjectID = strings.TrimSpace(scope.ProjectID)
	if scope.Home == "" {
		return CarbonScope{}, fmt.Errorf("Carbon connect requires a home path")
	}
	if strings.HasPrefix(scope.ClusterID, "--") || strings.HasPrefix(scope.ProjectID, "--") {
		return CarbonScope{}, fmt.Errorf("Carbon cluster and project ids cannot start with --")
	}
	if scope.ScopeMode == CarbonScopeModeSession {
		if scope.ClusterID != "" || scope.ProjectID != "" {
			return CarbonScope{}, fmt.Errorf("Carbon project-session scope cannot include a cluster or project id")
		}
		return scope, nil
	}

	if scope.ClusterID == "" {
		if scope.ProjectID == "" {
			return CarbonScope{}, fmt.Errorf("Carbon standalone connect requires a project id")
		}
		if scope.ScopeMode == CarbonScopeModeCluster {
			return CarbonScope{}, fmt.Errorf("Carbon cluster scope requires a cluster id")
		}
		if scope.ScopeMode != "" && scope.ScopeMode != CarbonScopeModeProject && scope.ScopeMode != CarbonScopeModeStandalone {
			return CarbonScope{}, fmt.Errorf("unknown Carbon scope mode %q", scope.ScopeMode)
		}
		scope.ScopeMode = CarbonScopeModeStandalone
		return scope, nil
	}

	if scope.ProjectID != "" {
		if scope.ScopeMode == CarbonScopeModeCluster {
			return CarbonScope{}, fmt.Errorf("Carbon cluster scope cannot include a project id")
		}
		if scope.ScopeMode != "" && scope.ScopeMode != CarbonScopeModeProject {
			return CarbonScope{}, fmt.Errorf("unknown Carbon scope mode %q", scope.ScopeMode)
		}
		scope.ScopeMode = CarbonScopeModeProject
		return scope, nil
	}

	switch scope.ScopeMode {
	case CarbonScopeModeCluster:
		return scope, nil
	case "":
		if scope.AllowClusterScope {
			scope.ScopeMode = CarbonScopeModeCluster
			return scope, nil
		}
		return CarbonScope{}, fmt.Errorf("Carbon connect requires a project id; set ScopeMode=cluster for an explicit cluster-wide connection")
	case CarbonScopeModeProject:
		return CarbonScope{}, fmt.Errorf("Carbon project scope requires a project id")
	default:
		return CarbonScope{}, fmt.Errorf("unknown Carbon scope mode %q", scope.ScopeMode)
	}
}

func newServerConfig(bin, repo, actor string) serverConfig {
	return serverConfig{Name: serverName, Bin: bin, Args: []string{"serve", "--actor", actor, "--repo", repo}}
}

// resolveActor gives each agent its own identity by default (e.g. agent:cursor) so its task
// writes are attributed to it in provenance — never to the human operator. A caller-supplied
// actor (to run multiple instances, e.g. agent:cursor-2) overrides it.
func resolveActor(agentID, actor string) string {
	if actor == "" {
		return "agent:" + agentID
	}
	return actor
}

// selfPath returns the absolute, symlink-resolved path of the running Carbon binary. In the
// desktop app this is the Tauri sidecar path, which is exactly what agents must launch.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved, nil
	}
	return exe, nil
}

// writeFileAtomic writes data to a path already constrained by trustedRoot. It creates
// each missing parent only after trustedConfigPath has inspected the existing chain, then
// backs up any existing file to <path>.bak before replacing the final directory entry.
func writeFileAtomic(trustedRoot, path string, existing, data []byte) error {
	safePath, err := trustedConfigPath(trustedRoot, path, true)
	if err != nil {
		return err
	}
	dir := filepath.Dir(safePath)
	if len(existing) > 0 {
		if err := replaceFileAtomically(dir, safePath+".bak", existing); err != nil {
			return err
		}
	}
	return replaceFileAtomically(dir, safePath, data)
}

// replaceFileAtomically writes data through a unique temp file in dir and then replaces
// path with a rename. Rename replaces the final directory entry rather than dereferencing
// it, so a pre-existing symlink at path cannot redirect the write outside dir.
func replaceFileAtomically(dir, path string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".carbon-connect-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
