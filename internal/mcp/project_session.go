package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"carbon/internal/compat"
	"carbon/internal/home"
	"carbon/internal/lease"
	"carbon/internal/store"
)

var (
	// ErrActiveProjectRequired is returned by a Carbon Project Session before a
	// task, session, or Work Log operation is allowed to touch a task store. A
	// home catalog is deliberately not a task-store fallback: callers must choose
	// a project with select_project (or create one) first.
	ErrActiveProjectRequired = errors.New("an active Carbon project is required; call select_project or create_project first")

	// ErrProjectSessionHomeRequired prevents a session from accidentally
	// inheriting a legacy repository root. Project sessions are Carbon v2 home
	// connections only.
	ErrProjectSessionHomeRequired = errors.New("Carbon project session requires an existing home directory")
)

// ProjectSessionOptions describes one mutable selection over an otherwise fixed
// Carbon home connection. Actor and client stay fixed for the life of a session;
// only the selected project's immutable Service is replaced.
//
// CompatLayer is optional and defaults to Carbon's stable v2 layer. A session
// never accepts the legacy layer, because it exposes Carbon catalog tools and
// can switch between Home projects.
type ProjectSessionOptions struct {
	Home        string
	Actor       string
	Client      string
	CompatLayer string
	Now         func() time.Time

	// catalogStore is deliberately private: callers that need the compact
	// transport constructor use NewProjectSession. It exists only so that
	// NewProjectSession can preserve a supplied Store without making a store root
	// another mutable session-selection input.
	catalogStore *store.Store
}

// ProjectSessionSelection is the public, non-task binding snapshot. The
// monotonically increasing SelectionVersion lets an agent detect that a
// concurrent peer or an explicit select_project call changed the active project.
type ProjectSessionSelection struct {
	BindingMode      string        `json:"bindingMode"`
	SelectionVersion uint64        `json:"selectionVersion"`
	Scope            ScopeMetadata `json:"scope"`
}

// ProjectSession owns a single MCP connection's active Carbon project. It is
// intentionally stateful, unlike Service: every successful selection creates a
// fresh immutable Service with its own Store, Scope, and source resolver. The
// mutex is held by the MCP receiving middleware for the complete tool call, so a
// handler cannot observe a half-switched binding.
type ProjectSession struct {
	mu sync.Mutex

	home        string
	actor       string
	client      string
	compatLayer string
	now         func() time.Time

	// catalog is permanently home-only and is safe before selection. active is
	// nil until select_project or create_project succeeds.
	catalog *Service
	active  *Service
	version uint64
}

// NewProjectSession creates a Carbon v2 Home catalog session with no active
// project. catalogStore is retained as the inert catalog Service's Store for
// adapter compatibility; project selections always create a fresh Store rooted
// at the selected project's resolved data directory. It does not initialize a
// missing .carbon directory; create_project and create_cluster retain their
// existing explicit approval gates.
func NewProjectSession(catalogStore *store.Store, actor, client, homePath string, now func() time.Time) (*ProjectSession, error) {
	return NewProjectSessionWithOptions(ProjectSessionOptions{
		Home: homePath, Actor: actor, Client: client, Now: now, catalogStore: catalogStore,
	})
}

// NewProjectSessionWithOptions is the extensible Project Session constructor.
// It is useful to adapters that need to pin an explicit stable compatibility
// layer while retaining NewProjectSession's compact transport-facing signature.
func NewProjectSessionWithOptions(options ProjectSessionOptions) (*ProjectSession, error) {
	homePath, err := canonicalProjectSessionHome(options.Home)
	if err != nil {
		return nil, err
	}
	contract, err := compat.Resolve(ServiceVersion, options.CompatLayer, compat.ModeCarbon)
	if err != nil {
		return nil, err
	}
	if contract.RequestedCompatLayer != compat.StableLayer {
		return nil, fmt.Errorf("%w: session requires %s", compat.ErrLayerScopeMismatch, compat.StableLayer)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	catalogStore := options.catalogStore
	if catalogStore == nil {
		catalogStore = store.New(homePath)
	}
	catalog := NewScopedServiceWithClientAndResolver(
		catalogStore, options.Actor, options.Client,
		Scope{Home: homePath, CompatLayer: contract.RequestedCompatLayer}, nil, now,
	)
	return &ProjectSession{
		home:        homePath,
		actor:       options.Actor,
		client:      options.Client,
		compatLayer: contract.RequestedCompatLayer,
		now:         now,
		catalog:     catalog,
	}, nil
}

// Home returns the canonical Carbon home directory fixed for this session.
func (session *ProjectSession) Home() string {
	if session == nil {
		return ""
	}
	return session.home
}

func (session *ProjectSession) catalogService() *Service {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.catalog
}

// Identity returns the current session-aware identity snapshot. It is safe for
// an adapter's status endpoint as well as normal tool calls.
func (session *ProjectSession) Identity() Identity {
	if session == nil {
		return Identity{}
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.identityLocked()
}

// Selection returns the current session binding. Before a project is selected it
// reports the fixed Home catalog scope with SelectionVersion zero.
func (session *ProjectSession) Selection() ProjectSessionSelection {
	if session == nil {
		return ProjectSessionSelection{BindingMode: "session"}
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.selectionLocked()
}

// ActiveService returns the current immutable project Service. It is mainly for
// host-side maintenance hooks; MCP handlers should use NewProjectSessionServer,
// whose middleware keeps the whole tool call on one binding.
func (session *ProjectSession) ActiveService() (*Service, error) {
	if session == nil {
		return nil, ErrActiveProjectRequired
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.active == nil {
		return nil, ErrActiveProjectRequired
	}
	return session.active, nil
}

// SelectProject switches this session to one project in its fixed Home. Cluster
// is optional so a standalone project can be selected; an ambiguous unscoped
// reference fails closed in home.ResolveProject. The old Service remains active
// on every error.
func (session *ProjectSession) SelectProject(cluster, project string) (ProjectSessionSelection, error) {
	if session == nil {
		return ProjectSessionSelection{}, ErrActiveProjectRequired
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.selectProjectLocked(cluster, project)
}

// CreateProject performs the existing approval-gated catalog mutation and then
// activates the created project in the same session. It is useful to adapters
// that want to create a project without going through the MCP tool server.
func (session *ProjectSession) CreateProject(input CatalogCreateProjectInput) (ProjectDescription, ProjectSessionSelection, error) {
	if session == nil {
		return ProjectDescription{}, ProjectSessionSelection{}, ErrActiveProjectRequired
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.createProjectLocked(input)
}

// ExpireLeases is the session-safe scheduler hook for stdio/HTTP hosts. It
// serializes expiration with select_project and returns ErrActiveProjectRequired
// rather than ever sweeping a Home catalog directory.
func (session *ProjectSession) ExpireLeases(ctx context.Context) ([]lease.Expired, error) {
	if session == nil {
		return nil, ErrActiveProjectRequired
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.active == nil {
		return nil, ErrActiveProjectRequired
	}
	return session.active.ExpireLeases(ctx)
}

func (session *ProjectSession) identityLocked() Identity {
	svc := session.currentLocked()
	identity := svc.Identity()
	identity.BindingMode = "session"
	identity.SelectionVersion = session.version
	return identity
}

func (session *ProjectSession) selectionLocked() ProjectSessionSelection {
	return ProjectSessionSelection{
		BindingMode:      "session",
		SelectionVersion: session.version,
		Scope:            session.currentLocked().Scope().Metadata(),
	}
}

func (session *ProjectSession) currentLocked() *Service {
	if session.active != nil {
		return session.active
	}
	return session.catalog
}

// serverMiddleware is deliberately a receiving middleware rather than a
// per-handler lock. The SDK may run tool calls concurrently, and every typed
// handler in tools.go captures the same svc variable. Holding this mutex from
// dispatch through response construction means a handler sees exactly one
// immutable Service and select_project cannot race a task or Work Log call.
func (session *ProjectSession) serverMiddleware(bind func(*Service)) mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, request mcpsdk.Request) (mcpsdk.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, request)
			}
			call, ok := request.(*mcpsdk.CallToolRequest)
			if !ok || call.Params == nil {
				return next(ctx, method, request)
			}
			session.mu.Lock()
			defer session.mu.Unlock()
			bind(session.currentLocked())
			if session.active == nil && !projectSessionPreselectionTool(call.Params.Name) {
				return activeProjectToolError(), nil
			}
			return next(ctx, method, request)
		}
	}
}

func activeProjectToolError() *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: ErrActiveProjectRequired.Error()}},
	}
}

// projectSessionPreselectionTool is the deliberately small safe allowlist for a
// session that has not selected a task root yet. Everything else — including a
// future task tool somebody forgets to classify, and even an unknown tools/call
// name — is rejected before any handler can touch the Home catalog Store. This
// fails closed instead of requiring a growing task-tool denylist.
func projectSessionPreselectionTool(name string) bool {
	_, ok := projectSessionPreselectionTools[name]
	return ok
}

var projectSessionPreselectionTools = map[string]struct{}{
	"identity":      {},
	"list_clusters": {}, "get_cluster": {}, "resolve_cluster": {}, "describe_cluster": {}, "create_cluster": {},
	"list_projects": {}, "get_project": {}, "resolve_project": {}, "describe_project": {}, "create_project": {},
	"select_project": {},
}

func (session *ProjectSession) selectProjectLocked(cluster, project string) (ProjectSessionSelection, error) {
	if strings.TrimSpace(project) == "" {
		return ProjectSessionSelection{}, fmt.Errorf("%w: project is required", ErrActiveProjectRequired)
	}
	resolution, err := home.ResolveProject(session.home, home.ResolveProjectRequest{
		ClusterID: cluster,
		ProjectID: project,
	})
	if err != nil {
		return ProjectSessionSelection{}, err
	}
	if resolution.Offline {
		return ProjectSessionSelection{}, fmt.Errorf("Carbon project %s is offline or its source fingerprint no longer matches", resolution.Project.ID)
	}
	dataStore := store.New(resolution.DataRoot)
	if err := validateProjectSessionDataRoot(dataStore, resolution); err != nil {
		return ProjectSessionSelection{}, err
	}
	scope := Scope{
		Home:        session.home,
		ProjectID:   resolution.Project.ID,
		SourcePath:  resolution.SourcePath,
		Standalone:  resolution.Standalone,
		CompatLayer: session.compatLayer,
	}
	if !resolution.Standalone {
		scope.ClusterID = resolution.Cluster.ID
	}
	// Build every part before replacing active, so a bad manifest/source leaves
	// the old selected Service untouched.
	candidate := NewScopedServiceWithClientAndResolver(
		dataStore, session.actor, session.client, scope,
		projectSessionRootResolver(session.home, resolution.Cluster.ID, resolution.Standalone, resolution.Project.ID), session.now,
	)
	session.active = candidate
	session.version++
	return session.selectionLocked(), nil
}

// validateProjectSessionDataRoot verifies that Home's resolved physical data
// root has a readable, valid Carbon config before it becomes an active Store.
// Standalone roots have an additional durable ownership invariant: their config
// project_id must equal the manifest project ID. Shared cluster roots intentionally
// accept an older/default config project_id because Carbon v2 scopes every task
// explicitly; adapters may normalize it to empty, but selection must not rewrite
// metadata merely to read/switch a project.
func validateProjectSessionDataRoot(dataStore *store.Store, resolution home.ProjectResolution) error {
	if dataStore == nil {
		return fmt.Errorf("%w: selected project has no data store", ErrActiveProjectRequired)
	}
	cfg, err := dataStore.Config()
	if err != nil {
		return fmt.Errorf("validate Carbon project %s data root: %w", resolution.Project.ID, err)
	}
	if resolution.Standalone && cfg.ProjectID != resolution.Project.ID {
		return fmt.Errorf("Carbon standalone project %s data root is bound to %q", resolution.Project.ID, cfg.ProjectID)
	}
	return nil
}

func (session *ProjectSession) createProjectLocked(input CatalogCreateProjectInput) (ProjectDescription, ProjectSessionSelection, error) {
	created, err := session.catalog.CreateCatalogProject(input)
	if err != nil {
		return ProjectDescription{}, ProjectSessionSelection{}, err
	}
	selection, err := session.selectProjectLocked(created.Project.ClusterID, created.Project.CanonicalID)
	if err != nil {
		// The durable project was created before its source was re-resolved. Do
		// not pretend otherwise; callers can select it again after correcting the
		// reported local source condition.
		return created, ProjectSessionSelection{}, fmt.Errorf("project %s was created but could not be activated: %w", created.Project.CanonicalID, err)
	}
	return created, selection, nil
}

func projectSessionRootResolver(homePath, clusterID string, standalone bool, boundProjectID string) ProjectRootResolver {
	return func(projectID string) (string, error) {
		if standalone && projectID != boundProjectID {
			return "", fmt.Errorf("standalone project %s cannot resolve sibling project %s", boundProjectID, projectID)
		}
		resolution, err := home.ResolveProject(homePath, home.ResolveProjectRequest{
			ClusterID: clusterID,
			ProjectID: projectID,
		})
		if err != nil {
			return "", err
		}
		if resolution.Standalone != standalone {
			return "", fmt.Errorf("project %s resolved outside selected Carbon storage scope", projectID)
		}
		if resolution.Offline {
			return "", fmt.Errorf("Carbon project %s is offline or its source fingerprint no longer matches", projectID)
		}
		return resolution.SourcePath, nil
	}
}

func canonicalProjectSessionHome(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", ErrProjectSessionHomeRequired
	}
	h, err := home.Open(raw)
	if err == nil {
		return h.Root, nil
	}
	if !errors.Is(err, home.ErrNotInitialized) {
		return "", err
	}
	// Match the catalog-server rule: an existing safe directory may host a
	// session before create_project initializes .carbon, but construction itself
	// must never write metadata.
	abs, absErr := filepath.Abs(raw)
	if absErr != nil {
		return "", absErr
	}
	resolved, resolveErr := filepath.EvalSymlinks(abs)
	if resolveErr != nil {
		return "", resolveErr
	}
	info, statErr := os.Stat(resolved)
	if statErr != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrProjectSessionHomeRequired, abs)
	}
	return filepath.Clean(resolved), nil
}
