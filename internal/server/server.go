// Package server is the HTTP front-end for the web UI. It mirrors the MCP verbs over
// HTTP by reusing mcp.Service, so the web and agent front-ends share one rule-set and
// can't drift (SPEC §0).
//
// Every endpoint accepts a `?path=` (the project folder to operate on), falling back to
// the server's launch `--repo`. Arbitrary path access is intentional: carbon web is a
// local, single-user tool. Paths must resolve to an existing directory.
//
// Endpoints:
//
//	GET  /api/status                       -> { initialized, root, prefix?, suggestedPrefix, states, closed, initial }
//	POST /api/init        { path?, prefix? }
//	GET  /api/tasks       ?status&assignee&ready
//	POST /api/tasks       { title, body?, deps?, checks? }
//	GET    /api/tasks/{id}
//	DELETE /api/tasks/{id}                            -> { id, deleted } ; refused if it has children/dependents
//	GET    /api/tasks/{id}/runs            -> { runs: [{ file, at, cmd, cwd, exit, timedout, duration, output }] }
//	GET    /api/tasks/{id}/git_context     -> { sessions: [{ session, context }] }
//	POST   /api/tasks/{id}/update    { title?, body?, checks?, priority?, labels?, deps?, parent?, blockerReason?, evidence? }
//	POST   /api/tasks/{id}/transition  { to }
//	POST   /api/tasks/{id}/claim
//	POST   /api/tasks/{id}/run_checks  { only? }
//	POST   /api/tasks/{id}/attest      { index, pass? }   -> attest a manual check (pass defaults true)
//	POST   /api/tasks/{id}/note        { text }
//	PATCH  /api/tasks/{id}/notes/{note}  { text }         -> edit a note (?index= for a legacy note, {note}="-")
//	DELETE /api/tasks/{id}/notes/{note}                   -> delete a note (?index= for a legacy note, {note}="-")
//	GET    /api/events      ?path                  -> text/event-stream of { type, id? } task-store change signals
//	GET    /api/home/events ?home                  -> text/event-stream of { type: "catalog-changed" } Home hints
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"carbon/internal/check"
	"carbon/internal/compat"
	"carbon/internal/config"
	"carbon/internal/home"
	"carbon/internal/lease"
	"carbon/internal/mcp"
	"carbon/internal/repo"
	"carbon/internal/session"
	"carbon/internal/store"
	"carbon/internal/task"
	"carbon/internal/templates"
	tasktypes "carbon/internal/types"
	"carbon/internal/views"
	"carbon/web"
)

// Server serves the web API. defaultRoot is used when a request omits `path`; actor is
// stamped on every write as provenance.
type Server struct {
	defaultRoot       string
	defaultHome       string
	defaultCluster    string
	defaultProject    string
	homeByDefault     bool
	productVersion    string
	compatLayer       string
	defaultCompatMode compat.Mode
	actor             string
	allowRemote       bool
	hub               *Hub
	homeHub           *HomeHub
	leaseSweepMu      sync.Mutex
	leaseSweepCancel  context.CancelFunc
}

// CompatibilityOptions carries the intentionally separate build and compatibility
// inputs for a server instance. A zero RequestedCompatLayer means each resolved
// scope picks its safe default: frozen legacy v1 for project-local --repo
// workspaces and approved stable Carbon v2 for home scopes.
type CompatibilityOptions struct {
	ProductVersion       string
	RequestedCompatLayer string
}

const maxJSONBodyBytes int64 = 1 << 20

// ErrNonLoopbackAddr means an unauthenticated HTTP API was asked to bind beyond
// localhost. Callers that need remote access must terminate their own authenticated
// tunnel at a loopback listener instead.
var ErrNonLoopbackAddr = errors.New("server address must be localhost or a loopback IP")

// New returns a Server. actor defaults to human:web.
func New(defaultRoot, actor string) *Server {
	return NewWithScope(actor, ScopeDefaults{LegacyRoot: defaultRoot})
}

// NewWithScope returns a Server bound to launch-time scope defaults. New remains the
// compatibility constructor for callers that serve one legacy repository; Carbon-aware
// CLI entry points use this constructor so `--home`, `--cluster`, and `--project` are
// reflected consistently by HTTP and Streamable MCP.
func NewWithScope(actor string, defaults ScopeDefaults) *Server {
	s, err := NewWithScopeAndCompatibility(actor, defaults, CompatibilityOptions{ProductVersion: "dev"})
	if err != nil {
		// The legacy constructor does not receive untrusted input. Keep its long-standing
		// no-error shape while making invalid options impossible to ignore for CLI hosts.
		panic(err)
	}
	return s
}

// NewWithScopeAndCompatibility binds a server to validated product/build and
// compatibility-layer metadata. Command-line hosts should use this constructor so an
// unknown --compat-layer is rejected before a listener is opened.
func NewWithScopeAndCompatibility(actor string, defaults ScopeDefaults, options CompatibilityOptions) (*Server, error) {
	defaultMode := compatModeForDefaults(defaults)
	contract, err := compat.Resolve(options.ProductVersion, options.RequestedCompatLayer, defaultMode)
	if err != nil {
		return nil, err
	}
	actor = sanitizeActor(actor)
	if actor == "" {
		actor = "human:web"
	}
	// Preserve an empty request so alternate per-request scope defaults remain
	// possible, but never retain a legacy input alias (0.3/0.4) internally.
	resolvedLayer := ""
	if strings.TrimSpace(options.RequestedCompatLayer) != "" {
		resolvedLayer = contract.RequestedCompatLayer
	}
	return &Server{
		defaultRoot:       defaults.LegacyRoot,
		defaultHome:       defaults.Home,
		defaultCluster:    defaults.ClusterID,
		defaultProject:    defaults.ProjectID,
		homeByDefault:     defaults.HomeByDefault,
		productVersion:    contract.ProductVersion,
		compatLayer:       resolvedLayer,
		defaultCompatMode: defaultMode,
		actor:             actor,
		hub:               NewHub(0),
		homeHub:           NewHomeHub(0),
	}, nil
}

// Handler builds the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/version", s.handleVersion)
	mux.HandleFunc("GET /api/home", s.handleGetHome)
	mux.HandleFunc("POST /api/home", s.handleEnsureHome)
	mux.HandleFunc("GET /api/home/events", s.handleHomeEvents)
	mux.HandleFunc("GET /api/home/clusters", s.handleListHomeClusters)
	mux.HandleFunc("POST /api/home/clusters", s.handleCreateHomeCluster)
	mux.HandleFunc("GET /api/home/clusters/{cluster}", s.handleGetHomeCluster)
	mux.HandleFunc("PATCH /api/home/clusters/{cluster}", s.handleUpdateHomeCluster)
	// Top-level projects are isolated Carbon task roots. The older nested cluster
	// project routes below remain compatibility aliases for shared-pool projects.
	mux.HandleFunc("GET /api/home/projects", s.handleListHomeProjects)
	mux.HandleFunc("POST /api/home/projects", s.handleCreateHomeProject)
	mux.HandleFunc("POST /api/home/projects/{project}/clear-task-data", s.handleClearHomeProjectTaskData)
	mux.HandleFunc("POST /api/home/projects/{project}/delete", s.handleDeleteHomeProject)
	mux.HandleFunc("GET /api/home/projects/{project}", s.handleGetHomeProject)
	mux.HandleFunc("PATCH /api/home/projects/{project}", s.handleUpdateStandaloneHomeProject)
	mux.HandleFunc("POST /api/home/projects/{project}/relink", s.handleRelinkStandaloneHomeProject)
	mux.HandleFunc("POST /api/home/clusters/{cluster}/projects", s.handleAddHomeProject)
	mux.HandleFunc("PATCH /api/home/clusters/{cluster}/projects/{project}", s.handleUpdateHomeProject)
	mux.HandleFunc("POST /api/home/clusters/{cluster}/projects/{project}/relink", s.handleRelinkHomeProject)
	mux.HandleFunc("POST /api/home/clusters/{cluster}/projects/{project}/detach", s.handleDetachHomeProject)
	mux.HandleFunc("GET /api/home/workers/aliases", s.handleGetWorkerAliases)
	mux.HandleFunc("PATCH /api/home/workers/aliases", s.handlePatchWorkerAlias)
	mux.HandleFunc("POST /api/home/workers/reset", s.handleResetWorker)
	mux.HandleFunc("DELETE /api/home/workers/{actor}", s.handleDeleteWorker)
	mux.HandleFunc("GET /api/home/presentation", s.handleGetCatalogPresentation)
	mux.HandleFunc("PUT /api/home/presentation/{kind}/{id}/icon", s.handlePutCatalogPresentationIcon)
	mux.HandleFunc("GET /api/home/presentation/{kind}/{id}/asset", s.handleGetCatalogPresentationAsset)
	mux.HandleFunc("PUT /api/home/presentation/{kind}/{id}/asset", s.handlePutCatalogPresentationAsset)
	mux.HandleFunc("DELETE /api/home/presentation/{kind}/{id}/asset", s.handleDeleteCatalogPresentationAsset)
	mux.HandleFunc("GET /api/worklogs", s.handleListWorkLogs)
	mux.HandleFunc("POST /api/worklogs", s.handleCreateWorkLog)
	mux.HandleFunc("GET /api/worklogs/{id}", s.handleGetWorkLog)
	mux.HandleFunc("PUT /api/worklogs/{id}", s.handleUpdateWorkLog)
	mux.HandleFunc("DELETE /api/worklogs/{id}", s.handleDeleteWorkLog)
	mux.HandleFunc("GET /api/home/doctor", s.handleHomeDoctor)
	mux.HandleFunc("GET /api/home/migrations/legacy/preflight", s.handleLegacyMigrationPreflight)
	mux.HandleFunc("POST /api/home/migrations/legacy/preview", s.handleLegacyMigrationPreview)
	mux.HandleFunc("POST /api/home/migrations/legacy/apply", s.handleLegacyMigrationApply)
	mux.HandleFunc("GET /api/home/migrations/legacy/receipt", s.handleLegacyMigrationReceipts)
	mux.HandleFunc("GET /api/cluster", s.handleGetCluster)
	mux.HandleFunc("POST /api/cluster", s.handlePostCluster)
	mux.HandleFunc("POST /api/cluster/projects", s.handlePostClusterProject)
	mux.HandleFunc("DELETE /api/cluster/projects/{id}", s.handleDeleteClusterProject)
	// This wrapper keeps the historical actor/client/version fields while adding the
	// full compatibility contract. The older session handler remains available to
	// internal callers but should not become the public route again.
	mux.HandleFunc("GET /api/identity", s.handleCompatibilityIdentity)
	mux.HandleFunc("POST /api/init", s.handleInit)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("POST /api/config", s.handleSetConfig)
	mux.HandleFunc("GET /api/tasks", s.handleList)
	mux.HandleFunc("POST /api/tasks", s.handleCreate)
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/types", s.handleListTypes)
	mux.HandleFunc("POST /api/types", s.handleCreateType)
	mux.HandleFunc("GET /api/trash", s.handleTrashList)
	mux.HandleFunc("POST /api/trash", s.handleTrashMany)
	mux.HandleFunc("DELETE /api/trash", s.handleTrashEmpty)
	mux.HandleFunc("POST /api/trash/{id}/restore", s.handleTrashRestore)
	mux.HandleFunc("POST /api/tasks/bulk/update", s.handleBulkUpdate)
	mux.HandleFunc("POST /api/tasks/bulk/move", s.handleBulkMove)
	mux.HandleFunc("GET /api/views", s.handleViewsList)
	mux.HandleFunc("POST /api/views", s.handleViewCreate)
	mux.HandleFunc("GET /api/views/{id}", s.handleViewGet)
	mux.HandleFunc("PUT /api/views/{id}", s.handleViewSave)
	mux.HandleFunc("DELETE /api/views/{id}", s.handleViewDelete)
	mux.HandleFunc("GET /api/views/{id}/apply", s.handleViewApply)
	mux.HandleFunc("GET /api/templates", s.handleTemplatesList)
	mux.HandleFunc("POST /api/templates", s.handleTemplateCreate)
	mux.HandleFunc("GET /api/templates/{id}", s.handleTemplateGet)
	mux.HandleFunc("PUT /api/templates/{id}", s.handleTemplateSave)
	mux.HandleFunc("DELETE /api/templates/{id}", s.handleTemplateDelete)
	mux.HandleFunc("POST /api/templates/{id}/instantiate", s.handleTemplateInstantiate)
	mux.HandleFunc("GET /api/stats/workers", s.handleWorkerStats)
	// Transitional alias used by early Carbon desktop builds. Documentation names
	// /api/stats/workers as canonical.
	mux.HandleFunc("GET /api/workers/stats", s.handleWorkerStats)
	mux.HandleFunc("GET /api/backup/config", s.handleBackupConfigGet)
	mux.HandleFunc("PUT /api/backup/config", s.handleBackupConfigPut)
	mux.HandleFunc("GET /api/backup/status", s.handleBackupStatus)
	mux.HandleFunc("GET /api/backup/snapshots", s.handleBackupList)
	mux.HandleFunc("POST /api/backup/snapshots", s.handleBackupCreate)
	mux.HandleFunc("POST /api/backup/run-now", s.handleBackupRunNow)
	mux.HandleFunc("POST /api/backup/prune", s.handleBackupPrune)
	mux.HandleFunc("POST /api/backup/continuous-authorization", s.handleBackupContinuousAuthorization)
	mux.HandleFunc("POST /api/backup/snapshots/{id}/upload", s.handleBackupUpload)
	mux.HandleFunc("POST /api/backup/snapshots/{id}/verify", s.handleBackupVerify)
	mux.HandleFunc("POST /api/backup/snapshots/{id}/restore-plan", s.handleBackupRestorePlan)
	mux.HandleFunc("GET /api/tasks/{id}", s.handleGet)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.handleDelete)
	mux.HandleFunc("POST /api/tasks/{id}/lease/claim", s.handleLeaseClaim)
	mux.HandleFunc("POST /api/tasks/{id}/lease/renew", s.handleLeaseRenew)
	mux.HandleFunc("POST /api/tasks/{id}/lease/release", s.handleLeaseRelease)
	mux.HandleFunc("POST /api/tasks/{id}/lease/reassign", s.handleLeaseReassign)
	mux.HandleFunc("POST /api/tasks/{id}/approval", s.handleLeaseApproval)
	mux.HandleFunc("GET /api/tasks/{id}/runs", s.handleRuns)
	mux.HandleFunc("GET /api/tasks/{id}/git_context", s.handleTaskGitContext)
	mux.HandleFunc("GET /api/tasks/{id}/sessions", s.handleListTaskSessions)
	mux.HandleFunc("POST /api/tasks/{id}/sessions/begin", s.handleBeginSession)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("GET /api/sessions/{session}", s.handleGetSession)
	mux.HandleFunc("GET /api/sessions/{session}/git_context", s.handleSessionGitContext)
	mux.HandleFunc("POST /api/sessions/{session}/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("POST /api/sessions/{session}/finish", s.handleFinishSession)
	mux.HandleFunc("POST /api/sessions/{session}/cancel", s.handleCancelSession)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/tasks/{id}/update", s.handleUpdate)
	mux.HandleFunc("POST /api/tasks/{id}/reorder", s.handleReorder)
	mux.HandleFunc("POST /api/tasks/{id}/transition", s.handleTransition)
	mux.HandleFunc("POST /api/tasks/{id}/claim", s.handleClaim)
	mux.HandleFunc("POST /api/tasks/{id}/run_checks", s.handleRunChecks)
	mux.HandleFunc("POST /api/tasks/{id}/attest", s.handleAttest)
	mux.HandleFunc("POST /api/tasks/{id}/note", s.handleNote)
	mux.HandleFunc("PATCH /api/tasks/{id}/notes/{note}", s.handleEditNote)
	mux.HandleFunc("DELETE /api/tasks/{id}/notes/{note}", s.handleDeleteNote)

	// Integrations: detect agents and write their MCP config (one-click connect).
	mux.HandleFunc("GET /api/connect", s.handleListIntegrations)
	mux.HandleFunc("POST /api/connect/{agent}", s.handleConnectAgent)
	mux.HandleFunc("DELETE /api/connect/{agent}", s.handleDisconnectAgent)
	mux.HandleFunc("GET /api/connect/{agent}/manual", s.handleAgentManual)

	// Readiness probe for the Tauri shell: no ?path needed, returns once the
	// server is accepting requests.
	mux.HandleFunc("GET /healthz", s.handleHealth)

	// MCP over Streamable HTTP, so an agent can connect to the running app by URL
	// (the same rule-set as `carbon serve` and the /api front-end). Fixed legacy and
	// Carbon scope connections bind via query; Carbon v2 session routing is the
	// explicit home-only form /mcp?home=/abs/home&actor=agent:x&routing=session.
	mux.Handle("/mcp", s.mcpHandler())
	mux.Handle("/mcp/", s.mcpHandler())

	// Embedded UI last: the catch-all serves the SPA, falling back to index.html.
	mux.Handle("/", spaHandler(web.FS()))
	return withSecurityHeaders(withWriteOriginCheck(mux, s.allowRemote))
}

// compatibilityFor returns the active public contract for a resolved request
// boundary. The requested layer is explicit only when a caller passed
// --compat-layer; otherwise the storage scope chooses the documented default.
func (s *Server) compatibilityFor(scope requestScope) compat.Contract {
	contract, err := compat.Resolve(s.productVersion, s.compatLayer, compatModeForScope(scope))
	if err != nil {
		// NewWithScopeAndCompatibility validates the only caller-controlled input. A
		// failure here would be a programming error, not a condition an HTTP request
		// may silently downgrade around.
		panic(err)
	}
	return contract
}

func (s *Server) defaultCompatibility() compat.Contract {
	contract, err := compat.Resolve(s.productVersion, s.compatLayer, s.defaultCompatMode)
	if err != nil {
		panic(err)
	}
	return contract
}

// carbonVersionAlias preserves the deprecated, non-authoritative generation hint
// used by existing web bundles. It is deliberately separate from
// requestedCompatLayer: only canonical v1/v2 govern API compatibility, while
// 0.3/0.4 describe the corresponding historical product semantics for clients that
// have not yet learned the explicit envelope.
func carbonVersionAlias(contract compat.Contract) string {
	if contract.RequestedCompatLayer == compat.StableLayer {
		return "0.4"
	}
	return "0.3"
}

// handleHealth preserves the exact plain `ok` readiness body used by existing
// portable shells. All version governance values are emitted in headers; callers
// that need a structured probe can opt into the identical JSON contract with
// Accept: application/json or ?format=json. APIVersion remains the v1 transport
// protocol while Requested/StableCompatLayer can correctly report Carbon stable v2.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	contract := s.defaultCompatibility()
	w.Header().Set("X-Carbon-Version", carbonVersionAlias(contract)) // retained product-semantic alias
	w.Header().Set("X-Carbon-Product-Version", contract.ProductVersion)
	w.Header().Set("X-Carbon-API-Version", contract.APIVersion)
	w.Header().Set("X-Carbon-Requested-Compat-Layer", contract.RequestedCompatLayer)
	w.Header().Set("X-Carbon-Supported-Compat-Layers", strings.Join(contract.SupportedCompatLayers, ","))
	w.Header().Set("X-Carbon-Stable-Compat-Layer", contract.StableCompatLayer)
	w.Header().Set("X-Carbon-Capabilities", strings.Join(contract.Capabilities, ","))
	if strings.EqualFold(r.URL.Query().Get("format"), "json") || strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json") {
		writeJSON(w, http.StatusOK, healthResp{Status: "ok", Contract: contract})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

type healthResp struct {
	Status string `json:"status"`
	compat.Contract
}

// withWriteOriginCheck blocks cross-site browser writes to Carbon's loopback API. Native
// MCP/CLI clients normally omit Origin and remain compatible; browser requests that do
// carry Origin must be same-origin, except for the loopback Vite development server.
func withWriteOriginCheck(next http.Handler, allowRemote bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
				http.Error(w, "cross-origin writes are not allowed", http.StatusForbidden)
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" && !allowedWriteOrigin(r, origin, allowRemote) {
				http.Error(w, "cross-origin writes are not allowed", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func allowedWriteOrigin(r *http.Request, rawOrigin string, allowRemote bool) bool {
	origin, err := url.Parse(rawOrigin)
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Hostname() == "" || origin.User != nil {
		return false
	}
	// Requiring a literal loopback host in the default mode prevents DNS rebinding: an
	// attacker-controlled hostname can resolve to 127.0.0.1 and match Host, but it is not
	// itself a loopback name or address.
	if !allowRemote && !isLoopbackHost(origin.Hostname()) {
		return false
	}
	if strings.EqualFold(origin.Host, r.Host) {
		return true
	}
	// Vite proxies /api to the Go server during local development. Some proxy versions
	// preserve the browser's Origin while rewriting Host to the target address.
	return origin.Port() == "5173" && isLoopbackHost(origin.Hostname()) && isLoopbackHost(requestHostname(r.Host))
}

func requestHostname(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err == nil {
		return host
	}
	return strings.Trim(hostport, "[]")
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Keep the local UI from loading executable content or sending data anywhere except
		// Carbon's loopback origin and Tauri's IPC bridge. SSE and REST requests are same-origin;
		// the explicit loopback/WebSocket entries also cover the desktop and Vite dev views.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'self'; form-action 'self'; object-src 'none'; frame-ancestors 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' asset: data: blob:; font-src 'self' data:; connect-src 'self' ipc: http://ipc.localhost http://127.0.0.1:* ws://127.0.0.1:* ws://localhost:5173; worker-src 'self' blob:")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		// API and MCP requests are JSON today. Apply the same bound before routing so
		// Streamable MCP bodies, which do not use decode below, cannot bypass it. SSE
		// is a response-only GET and therefore has no request body to wrap.
		if r.Body != nil && r.ContentLength != 0 {
			if r.ContentLength > maxJSONBodyBytes {
				writeJSON(w, http.StatusRequestEntityTooLarge, errBody(fmt.Errorf("request body exceeds %d bytes", maxJSONBodyBytes)))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// mcpHandler serves MCP over Streamable HTTP. It validates ?repo and ?actor up
// front (clear 400s) and builds a per-connection mcp.Server bound to that repo
// and identity, reusing the same Service rule-set as the stdio server. The sole
// exception is the explicit Carbon v2 `routing=session` home connection: it starts
// catalog-only and may select exactly one active project for the lifetime of that
// MCP transport session.
func (s *Server) mcpHandler() http.Handler {
	// Separate streamable handlers own separate MCP-session maps. If a client drops
	// `routing=session` after initialization, its session ID is unknown to the fixed
	// handler and is rejected instead of silently retaining a mutable project binding
	// through a home-only catalog URL.
	fixedHandler := mcpsdk.NewStreamableHTTPHandler(s.fixedMCPServer, nil)
	projectSessionHandler := mcpsdk.NewStreamableHTTPHandler(s.projectSessionMCPServer, nil)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routing, err := mcpRouting(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sanitizeActor(r.URL.Query().Get("actor")) == "" {
			http.Error(w, "missing or invalid ?actor= (e.g. agent:claude-1)", http.StatusBadRequest)
			return
		}
		scope, err := s.resolveScope(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if routing == mcpRoutingSession {
			if !mcpSessionRoutingHomeOnlyRequest(r) || !scope.isHomeOnly() {
				http.Error(w, "MCP routing=session requires a home-only Carbon scope (use ?home= without repo, cluster, or project)", http.StatusBadRequest)
				return
			}
			if s.compatibilityFor(scope).RequestedCompatLayer != compat.StableLayer {
				http.Error(w, "MCP routing=session requires Carbon v2", http.StatusBadRequest)
				return
			}
			projectSessionHandler.ServeHTTP(w, r)
			return
		}
		if !scope.hasStore() && !scope.isHomeOnly() {
			http.Error(w, "MCP requires legacy ?repo=, Carbon ?home=&cluster=, or a home-only ?home= catalog scope", http.StatusBadRequest)
			return
		}
		fixedHandler.ServeHTTP(w, r)
	})
}

// fixedMCPServer preserves the original per-connection fixed binding. It never
// consults `routing=session`; that route has its own streamable session map above.
func (s *Server) fixedMCPServer(r *http.Request) *mcpsdk.Server {
	scope, err := s.resolveScope(r)
	if err != nil || (!scope.hasStore() && !scope.isHomeOnly()) {
		return nil
	}
	actor := sanitizeActor(r.URL.Query().Get("actor"))
	if actor == "" {
		return nil
	}
	client := r.URL.Query().Get("client")
	if scope.isHomeOnly() {
		// Home-only is a catalog/identity connection. Store.New is inert; the
		// registration boundary in mcp.NewServer omits every task tool, so this
		// cannot initialize or mutate a cluster task store.
		return mcp.NewServer(mcp.NewScopedServiceWithClientAndResolver(store.New(scope.Home), actor, client, s.scopedMCPScope(scope), nil, nil))
	}
	// Legacy MCP owns a source project and can use the full initializer. Carbon
	// MCP owns only a private cluster data root, so never emit workflow/agent
	// files beside the data store.
	var initErr error
	if scope.Legacy {
		initErr = repo.Init(scope.Root, "")
	} else if scope.Standalone {
		// Home creates a standalone root with a permanently bound config project_id.
		// Do not reuse the shared-cluster initializer here: it deliberately clears
		// that binding and would turn an isolated project into an unlabelled pool.
		initErr = validateStandaloneDataRoot(scope.Root, scope.ProjectID)
	} else {
		initErr = initCarbonDataRoot(scope.Root, "")
	}
	if initErr != nil {
		return nil
	}
	return mcp.NewServer(mcp.NewScopedServiceWithClientAndResolver(store.New(scope.Root), actor, client, s.scopedMCPScope(scope), s.projectRootResolver(scope), nil))
}

// projectSessionMCPServer is intentionally narrower than fixedMCPServer: it
// creates one mutable ProjectSession only for a Carbon v2 home catalog request.
// ProjectSession resolves every selected project itself, so no task root is captured
// at HTTP connection startup.
func (s *Server) projectSessionMCPServer(r *http.Request) *mcpsdk.Server {
	if !mcpSessionRoutingHomeOnlyRequest(r) {
		return nil
	}
	scope, err := s.resolveScope(r)
	if err != nil || !scope.isHomeOnly() || s.compatibilityFor(scope).RequestedCompatLayer != compat.StableLayer {
		return nil
	}
	actor := sanitizeActor(r.URL.Query().Get("actor"))
	if actor == "" {
		return nil
	}
	binding, err := mcp.NewProjectSession(store.New(scope.Home), actor, r.URL.Query().Get("client"), scope.Home, nil)
	if err != nil {
		return nil
	}
	return mcp.NewProjectSessionServer(binding)
}

const mcpRoutingSession = "session"

// mcpRouting deliberately accepts no routing aliases. An omitted or empty value
// retains the established fixed/catalog transport behavior, while `session` is an
// explicit opt-in to the per-MCP-session active project binding. Repeated values are
// rejected rather than allowing a proxy's query normalization to alter the mode.
func mcpRouting(r *http.Request) (string, error) {
	values, present := r.URL.Query()["routing"]
	if !present {
		return "", nil
	}
	if len(values) != 1 {
		return "", errors.New("MCP routing must be specified at most once")
	}
	routing := strings.TrimSpace(values[0])
	if routing == "" || routing == mcpRoutingSession {
		return routing, nil
	}
	return "", fmt.Errorf("unsupported MCP routing %q (want session)", routing)
}

// mcpSessionRoutingHomeOnlyRequest checks the raw request as well as the resolved
// scope. resolveScope intentionally permits a Carbon web process to have default
// Home values, but a `?repo=` or empty `?project=` must not be masked by those
// defaults and accidentally become a selectable session. The session route has no
// legacy compatibility surface, so fail closed on every explicit fixed-scope key.
func mcpSessionRoutingHomeOnlyRequest(r *http.Request) bool {
	query := r.URL.Query()
	for _, key := range []string{"path", "repo", "cluster", "project"} {
		if _, present := query[key]; present {
			return false
		}
	}
	for _, header := range []string{"X-Carbon-Cluster", "X-Carbon-Project"} {
		if len(r.Header.Values(header)) != 0 {
			return false
		}
	}
	return true
}

// spaHandler serves embedded static assets, falling back to index.html so
// client-side routes resolve. Tolerates a missing index.html (placeholder dist)
// by letting the file server return its own 404.
func spaHandler(root fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(root, strings.TrimPrefix(r.URL.Path, "/")); err != nil && r.URL.Path != "/" {
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}

// Run serves on a loopback addr until the process exits. Handler is intentionally
// available separately for in-process callers and httptest; only a real listener is
// constrained here.
func (s *Server) Run(addr string) error {
	if err := ValidateLoopbackAddr(addr); err != nil {
		return err
	}
	s.StartLeaseSweep(context.Background())
	defer s.StopLeaseSweep()
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	return srv.ListenAndServe()
}

// ValidateLoopbackAddr accepts only an explicit loopback TCP listener. It deliberately
// accepts exact lowercase localhost as a familiar local spelling, while rejecting empty
// hosts, wildcards, and every other hostname so DNS cannot widen the API's exposure.
func ValidateLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return fmt.Errorf("%w: %q", ErrNonLoopbackAddr, addr)
	}
	if host == "localhost" {
		return nil
	}
	// A zone on an IPv6 loopback address does not change whether the address is local.
	if i := strings.LastIndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%w: %q", ErrNonLoopbackAddr, addr)
}

// --- request helpers ---

// resolveRoot turns a raw path (query/body) into an absolute, existing directory.
// Carbon is a local project manager and deliberately allows the user to select projects
// outside the directory it was launched from. The selected directory is canonicalized
// first; the repo/store layers then enforce containment for every .carbon path below it.
func (s *Server) resolveRoot(raw string) (string, error) {
	if raw == "" {
		raw = s.defaultRoot
	}
	raw = expandHome(raw)
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}

	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("no such folder: %s", abs)
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("no such folder: %s", abs)
	}
	return real, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func (s *Server) service(root, actor string) *mcp.Service {
	return mcp.NewService(store.New(root), actor, nil)
}

func (s *Server) scopedService(scope requestScope, actor string) *mcp.Service {
	return mcp.NewScopedServiceWithClientAndResolver(store.New(scope.Root), actor, "", s.scopedMCPScope(scope), s.projectRootResolver(scope), nil)
}

// scopedMCPScope carries the HTTP launch/request compatibility decision into the
// MCP service's identity envelope. It also records that selecting a Carbon cluster
// with no project was deliberate, which prevents MCP task creation from treating an
// accidental empty project id as cluster-wide work.
func (s *Server) scopedMCPScope(scope requestScope) mcp.Scope {
	mcpScope := scope.mcpScope()
	mcpScope.CompatLayer = s.compatibilityFor(scope).RequestedCompatLayer
	mcpScope.ClusterScope = scope.Mode == "carbon" && scope.ClusterID != "" && scope.ProjectID == ""
	return mcpScope
}

func (s *Server) projectRootResolver(scope requestScope) mcp.ProjectRootResolver {
	if !s.scopedMCPScope(scope).IsCarbon() {
		return nil
	}
	return func(projectID string) (string, error) {
		if scope.Standalone && projectID != scope.ProjectID {
			return "", fmt.Errorf("standalone project %s cannot resolve sibling project %s", scope.ProjectID, projectID)
		}
		resolution, err := home.ResolveProject(scope.Home, home.ResolveProjectRequest{
			ClusterID: scope.ClusterID,
			ProjectID: projectID,
		})
		if err != nil {
			return "", err
		}
		if resolution.Standalone != scope.Standalone {
			return "", errors.New("project resolved outside the selected Carbon storage scope")
		}
		if resolution.Offline {
			return "", fmt.Errorf("Carbon project %s is offline or its source fingerprint no longer matches", projectID)
		}
		return resolution.SourcePath, nil
	}
}

// actorFor resolves who is making this request: the client-asserted identity from the
// X-Carbon-Actor header (URL-encoded; legacy X-Cairn-Actor then falls back to ?actor=), sanitized, else the server
// default. Trust model is local-dev (like a git author) — no auth.
func (s *Server) actorFor(r *http.Request) string {
	raw := r.Header.Get("X-Carbon-Actor")
	if raw == "" {
		// X-Cairn-Actor is a read-only compatibility fallback for clients that have
		// not yet updated their local desktop/web shell.
		raw = r.Header.Get("X-Cairn-Actor")
	}
	if dec, err := url.QueryUnescape(raw); err == nil {
		raw = dec
	}
	if raw == "" {
		raw = r.URL.Query().Get("actor")
	}
	if a := sanitizeActor(raw); a != "" {
		return a
	}
	return s.actor
}

// sanitizeActor keeps an actor string safe to store in YAML/provenance: single line, trimmed,
// bounded length. Returns "" when nothing usable remains.
func sanitizeActor(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range raw {
		if r == '\n' || r == '\r' || r == '\t' || r < 0x20 {
			continue // drop control chars / newlines
		}
		b.WriteRune(r)
		if b.Len() >= 64 {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// --- handlers ---

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	scope, err := s.resolveScope(r)
	if err != nil {
		writeJSON(w, scopeErrStatus(err), errBody(err))
		return
	}
	writeJSON(w, http.StatusOK, s.statusFor(scope))
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	scope, err := s.resolveScope(r)
	if err != nil {
		writeJSON(w, scopeErrStatus(err), errBody(err))
		return
	}
	contract := s.compatibilityFor(scope)
	// carbonVersion is a retained product-semantic alias consumed by existing 0.4 web bundles;
	// the explicit contract fields below are authoritative for new clients.
	writeJSON(w, http.StatusOK, versionResp{CarbonVersion: carbonVersionAlias(contract), Contract: contract})
}

type versionResp struct {
	// CarbonVersion is deprecated and non-authoritative. Clients must negotiate from
	// the embedded canonical compatibility contract instead.
	CarbonVersion string `json:"carbonVersion"`
	compat.Contract
}

// handleCompatibilityIdentity preserves the established identity shape while
// attaching the negotiated compatibility envelope. It intentionally resolves the
// same request scope as task operations, so `?repo` defaults to frozen legacy v1
// and Carbon home scope defaults to approved stable v2 unless --compat-layer made
// an explicit choice.
func (s *Server) handleCompatibilityIdentity(w http.ResponseWriter, r *http.Request) {
	// A home-only request intentionally has no task store and must not trigger the
	// task-service lease sweep. It still has a useful catalog/identity contract.
	if scope, err := s.resolveScope(r); err != nil {
		writeJSON(w, scopeErrStatus(err), errBody(err))
		return
	} else if scope.isHomeOnly() {
		svc := mcp.NewScopedServiceWithClientAndResolver(store.New(scope.Home), s.actorFor(r), "", s.scopedMCPScope(scope), nil, nil)
		s.writeCompatibilityIdentity(w, svc, scope)
		return
	}
	svc, scope, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	s.writeCompatibilityIdentity(w, svc, scope)
}

func (s *Server) writeCompatibilityIdentity(w http.ResponseWriter, svc *mcp.Service, scope requestScope) {
	identity := svc.Identity()
	contract := s.compatibilityFor(scope)
	identity.Version = contract.ProductVersion // historical alias for product/build version
	// The MCP service also embeds a compatibility envelope. Override it with the
	// HTTP server's launch/request contract so a test/embedded Server constructed
	// with a product build cannot report contradictory nested and top-level values.
	identity.Compatibility = contract
	identity.CompatibilityError = ""
	writeJSON(w, http.StatusOK, identityResp{Identity: identity, Contract: contract})
}

type identityResp struct {
	mcp.Identity
	compat.Contract
}

type initReq struct {
	Path   string `json:"path"`
	Prefix string `json:"prefix"`
}

func (s *Server) handleInit(w http.ResponseWriter, r *http.Request) {
	var req initReq
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Path) != "" && (r.URL.Query().Get("home") != "" || r.URL.Query().Get("cluster") != "" || r.URL.Query().Get("project") != "") {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("init path cannot be combined with Carbon scope")))
		return
	}
	// Keep the old `{path}` body field as a legacy shorthand. Carbon callers select
	// a cluster in the query/header and initialize only its private data root.
	if strings.TrimSpace(req.Path) != "" {
		r.URL.RawQuery = r.URL.Query().Encode()
		q := r.URL.Query()
		q.Set("path", req.Path)
		r.URL.RawQuery = q.Encode()
	}
	scope, err := s.resolveScope(r)
	if err != nil {
		writeJSON(w, scopeErrStatus(err), errBody(err))
		return
	}
	if !scope.hasStore() {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("initializing Carbon requires a cluster scope")))
		return
	}
	var initErr error
	if scope.Legacy {
		initErr = repo.Init(scope.Root, req.Prefix)
	} else {
		initErr = initCarbonDataRoot(scope.Root, req.Prefix)
	}
	if initErr != nil {
		writeErr(w, initErr)
		return
	}
	writeJSON(w, http.StatusOK, s.statusFor(scope))
}

type configReq struct {
	// Path is retained as a legacy body shorthand. Carbon callers must use the
	// ordinary home/cluster/project scope query/header fields instead.
	Path string `json:"path"`
	// CheckShell is a pointer so an omitted field is left unchanged, while "" clears it
	// (back to the default sh).
	CheckShell         *string `json:"checkShell"`
	TrashRetentionDays *int    `json:"trashRetentionDays"`
}

type configResp struct {
	Scope              scopeDTO `json:"scope"`
	CheckShell         string   `json:"checkShell,omitempty"`
	TrashRetentionDays int      `json:"trashRetentionDays"`
}

func (s *Server) configScope(r *http.Request, legacyPath string) (requestScope, error) {
	if strings.TrimSpace(legacyPath) == "" {
		return s.resolveScope(r)
	}
	q := r.URL.Query()
	if q.Get("home") != "" || q.Get("cluster") != "" || q.Get("project") != "" || q.Get("repo") != "" || q.Get("path") != "" {
		return requestScope{}, errors.New("config path cannot be combined with an explicit scope")
	}
	q.Set("path", legacyPath)
	r.URL.RawQuery = q.Encode()
	return s.resolveScope(r)
}

func (s *Server) configResponse(scope requestScope, cfg config.Config) configResp {
	days := cfg.TrashRetentionDays
	if days <= 0 {
		days = 30
	}
	return configResp{Scope: scopeDTOFrom(scope), CheckShell: cfg.CheckShell, TrashRetentionDays: days}
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	scope, err := s.resolveScope(r)
	if err != nil {
		writeJSON(w, scopeErrStatus(err), errBody(err))
		return
	}
	if !scope.hasStore() {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("config requires legacy path/repo or Carbon cluster scope")))
		return
	}
	cfg, err := store.New(scope.Root).Config()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.configResponse(scope, cfg))
}

// handleSetConfig edits the workspace's config.yaml. It loads the current config, applies the
// provided fields, and saves — so it never clobbers unrelated settings.
func (s *Server) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	var req configReq
	if !decode(w, r, &req) {
		return
	}
	scope, err := s.configScope(r, req.Path)
	if err != nil {
		writeJSON(w, scopeErrStatus(err), errBody(err))
		return
	}
	if !scope.hasStore() {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("config requires legacy path/repo or Carbon cluster scope")))
		return
	}
	if scope.Mode == "carbon" && scope.ProjectID != "" {
		writeErr(w, mcp.ErrProjectWriteScope)
		return
	}
	st := store.New(scope.Root)
	cfg, err := st.Config()
	if err != nil {
		writeErr(w, err)
		return
	}
	if req.CheckShell != nil {
		// A shell can be a path with spaces; only trim ends and drop newlines.
		shell := strings.TrimSpace(*req.CheckShell)
		shell = strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == '\t' {
				return -1
			}
			return r
		}, shell)
		cfg.CheckShell = shell
	}
	if req.TrashRetentionDays != nil {
		if *req.TrashRetentionDays < 1 || *req.TrashRetentionDays > 3650 {
			writeJSON(w, http.StatusUnprocessableEntity, errBody(errors.New("trashRetentionDays must be between 1 and 3650")))
			return
		}
		cfg.TrashRetentionDays = *req.TrashRetentionDays
	}
	if err := st.SaveConfig(cfg); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.configResponse(scope, cfg))
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var ready *bool
	if v := q.Get("ready"); v != "" {
		b, _ := strconv.ParseBool(v)
		ready = &b
	}
	views, err := svc.ListScoped(q.Get("status"), q.Get("assignee"), ready, q.Get("execution"), includeCluster(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	tasks := make([]taskDTO, 0, len(views))
	for _, v := range views {
		dto := dtoFromTask(v.Task, v.Ready)
		dto.UpdatedAt = v.UpdatedAt
		dto.ExecutionState = v.ExecutionState
		dto.SessionID = v.SessionID
		tasks = append(tasks, dto)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	doc, err := svc.GetScoped(r.PathValue("id"), includeCluster(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeTaskJSON(w, http.StatusOK, svc, doc)
}

type createReq struct {
	Title         string          `json:"title"`
	Body          string          `json:"body"`
	BlockerReason string          `json:"blockerReason"`
	Evidence      []task.Evidence `json:"evidence"`
	Deps          []string        `json:"deps"`
	Checks        []checkDTO      `json:"checks"`
	Labels        []string        `json:"labels"`
	Priority      string          `json:"priority"`
	Parent        string          `json:"parent"`
	ProjectID     *string         `json:"projectId"`
	Type          string          `json:"type"`
	Importance    string          `json:"importance"`
	// Assignee is retained for legacy creation only. Carbon routes assignment through
	// lease_reassign so ownership/audit semantics cannot be bypassed at creation time.
	Assignee string `json:"assignee"`
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	svc, scope, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req createReq
	if !decode(w, r, &req) {
		return
	}
	// Carbon task ownership is lease-backed. Reject before CreateContext so a direct
	// assignee cannot leave a task or provenance record behind when it is refused.
	if scope.Mode == "carbon" && strings.TrimSpace(req.Assignee) != "" {
		writeErr(w, mcp.ErrAssigneeLeaseRequired)
		return
	}
	checks := make([]task.Check, 0, len(req.Checks))
	for _, c := range req.Checks {
		checks = append(checks, task.Check{Desc: c.Desc, Cmd: c.Cmd, Type: c.Type, Cwd: c.Cwd, Timeout: c.Timeout, Result: "pending"})
	}
	draft := store.Draft{
		Title: req.Title, Body: req.Body, BlockerReason: req.BlockerReason, Evidence: req.Evidence, Deps: req.Deps, Checks: checks,
		Labels: req.Labels, Priority: req.Priority, Parent: req.Parent, Type: req.Type, Importance: req.Importance,
	}
	if req.ProjectID != nil {
		draft.ProjectID = *req.ProjectID
		draft.ProjectIDSet = true
	}
	doc, err := svc.CreateContext(r.Context(), draft)
	if err != nil {
		writeErr(w, err)
		return
	}
	if strings.TrimSpace(req.Assignee) != "" {
		// Creation may carry an explicit human/workflow assignee, but it remains a
		// manual assignment rather than a lease claim. ReassignLease records the
		// durable audit event and never creates execution ownership for this actor.
		doc, err = svc.ReassignLease(r.Context(), doc.Task.ID, req.Assignee, "initial assignment on task creation", doc.Version(), false)
		if err != nil {
			writeErr(w, err)
			return
		}
	}
	writeTaskJSON(w, http.StatusOK, svc, doc)
}

type updateReq struct {
	Priority        *string          `json:"priority"`
	Labels          *[]string        `json:"labels"`
	Deps            *[]string        `json:"deps"`
	Parent          *string          `json:"parent"`
	Title           *string          `json:"title"`
	Body            *string          `json:"body"`
	Checks          *[]checkDTO      `json:"checks"`
	ProjectID       *string          `json:"projectId"`
	Type            *string          `json:"type"`
	Importance      *string          `json:"importance"`
	Assignee        *string          `json:"assignee"`
	BlockerReason   *string          `json:"blockerReason"`
	Evidence        *[]task.Evidence `json:"evidence"`
	ExpectedVersion string           `json:"expectedVersion"`
}

type reorderReq struct {
	Rank float64 `json:"rank"`
}

func (s *Server) handleReorder(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req reorderReq
	if !decode(w, r, &req) {
		return
	}
	doc, err := svc.Reorder(r.PathValue("id"), req.Rank)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dtoFromDoc(svc, doc))
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req updateReq
	if !decode(w, r, &req) {
		return
	}
	f := mcp.UpdateFields{Priority: req.Priority, Labels: req.Labels, Deps: req.Deps, Parent: req.Parent, Title: req.Title, Body: req.Body,
		ProjectID: req.ProjectID, Type: req.Type, Importance: req.Importance, Assignee: req.Assignee,
		BlockerReason: req.BlockerReason, Evidence: req.Evidence}
	if req.Checks != nil {
		checks := make([]task.Check, 0, len(*req.Checks))
		for _, c := range *req.Checks {
			result := c.Result
			if result == "" {
				result = "pending"
			}
			checks = append(checks, task.Check{Desc: c.Desc, Cmd: c.Cmd, Type: c.Type, Cwd: c.Cwd, Timeout: c.Timeout, Result: result})
		}
		f.Checks = &checks
	}
	doc, err := svc.UpdateWithVersion(r.PathValue("id"), f, expectedVersion(r, req.ExpectedVersion))
	if err != nil {
		if errors.Is(err, store.ErrVersionMismatch) {
			writeVersionConflict(w, svc, r.PathValue("id"), err)
			return
		}
		writeErr(w, err)
		return
	}
	writeTaskJSON(w, http.StatusOK, svc, doc)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	entry, err := svc.TrashTask(r.Context(), id, "deleted via HTTP", expectedVersion(r, ""))
	if errors.Is(err, store.ErrVersionMismatch) {
		writeVersionConflict(w, svc, id, err)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if entry.ETag != "" {
		w.Header().Set("ETag", entry.ETag)
	}
	// deleted remains for old web clients; trashed makes the recoverable lifecycle
	// explicit for Carbon clients.
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true, "trashed": true, "entry": entry})
}

type editNoteReq struct {
	Text string `json:"text"`
}

// noteRef resolves how a note sub-resource request addresses its target: by stable id
// (the {note} path segment) or, for a legacy note, by 0-based index (?index=, with {note}
// set to the "-" sentinel).
func noteRef(r *http.Request) (noteID string, index int) {
	noteID = r.PathValue("note")
	index = -1
	if v := r.URL.Query().Get("index"); v != "" {
		index, _ = strconv.Atoi(v)
	}
	if noteID == "-" {
		noteID = ""
	}
	return noteID, index
}

func (s *Server) handleEditNote(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req editNoteReq
	if !decode(w, r, &req) {
		return
	}
	noteID, index := noteRef(r)
	doc, err := svc.EditNote(r.PathValue("id"), noteID, index, req.Text)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dtoFromDoc(svc, doc))
}

func (s *Server) handleDeleteNote(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	noteID, index := noteRef(r)
	doc, err := svc.DeleteNote(r.PathValue("id"), noteID, index)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dtoFromDoc(svc, doc))
}

type transitionReq struct {
	To string `json:"to"`
}

func (s *Server) handleTransition(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req transitionReq
	if !decode(w, r, &req) {
		return
	}
	doc, err := svc.TransitionContext(r.Context(), r.PathValue("id"), req.To)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dtoFromDoc(svc, doc))
}

func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	// The historical assignment-only claim is the frozen project-local v1
	// contract. Resolve and reject Carbon before svcFor, whose lease sweep can
	// mutate expired work; Carbon ownership must go through lease/claim.
	scope, err := s.resolveScope(r)
	if err != nil {
		writeJSON(w, scopeErrStatus(err), errBody(err))
		return
	}
	if !scope.Legacy || s.compatibilityFor(scope).RequestedCompatLayer != compat.LegacyLayer {
		writeJSON(w, http.StatusGone, errBody(errors.New("legacy claim endpoint is unavailable in Carbon v2; use POST /api/tasks/{id}/lease/claim")))
		return
	}
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	doc, err := svc.Claim(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dtoFromDoc(svc, doc))
}

type runChecksReq struct {
	Only []int `json:"only"`
}

func (s *Server) handleRunChecks(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req runChecksReq
	if !decode(w, r, &req) {
		return
	}
	doc, err := svc.RunChecksContext(r.Context(), r.PathValue("id"), req.Only)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dtoFromDoc(svc, doc))
}

type attestReq struct {
	Index int   `json:"index"`
	Pass  *bool `json:"pass"`
}

func (s *Server) handleAttest(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req attestReq
	if !decode(w, r, &req) {
		return
	}
	doc, err := svc.Attest(r.PathValue("id"), req.Index, req.Pass == nil || *req.Pass)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dtoFromDoc(svc, doc))
}

type noteReq struct {
	Text string `json:"text"`
}

func (s *Server) handleNote(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req noteReq
	if !decode(w, r, &req) {
		return
	}
	doc, err := svc.Note(r.PathValue("id"), req.Text)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dtoFromDoc(svc, doc))
}

// svcFor resolves the root and returns a Service, writing an error response on failure.
func (s *Server) svcFor(w http.ResponseWriter, r *http.Request) (*mcp.Service, requestScope, bool) {
	scope, err := s.resolveScope(r)
	if err != nil {
		writeJSON(w, scopeErrStatus(err), errBody(err))
		return nil, requestScope{}, false
	}
	if !scope.hasStore() {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("task operation requires legacy path/repo, Carbon cluster scope, or Carbon standalone project scope")))
		return nil, requestScope{}, false
	}
	if scope.Standalone && includeCluster(r) {
		writeJSON(w, http.StatusUnprocessableEntity, errBody(mcp.ErrStandaloneClusterScope))
		return nil, requestScope{}, false
	}
	if scope.Standalone {
		if err := validateStandaloneDataRoot(scope.Root, scope.ProjectID); err != nil {
			writeErr(w, err)
			return nil, requestScope{}, false
		}
	}
	svc := s.scopedService(scope, s.actorFor(r))
	// Endpoint traffic is also an opportunity to collect abandoned ownership. The
	// background sweep handles idle stores; this keeps request-driven hosts correct
	// even when an embedder does not call StartLeaseSweep.
	if _, err := svc.ExpireLeases(r.Context()); err != nil {
		writeErr(w, err)
		return nil, requestScope{}, false
	}
	return svc, scope, true
}

func (s *Server) status(root string) statusResp {
	return s.statusFor(requestScope{Mode: "legacy", Root: root, Legacy: true})
}

func (s *Server) statusFor(scope requestScope) statusResp {
	contract := s.compatibilityFor(scope)
	resp := statusResp{
		CarbonVersion:  carbonVersionAlias(contract),
		Contract:       contract,
		Scope:          scopeDTOFrom(scope),
		Initialized:    scope.hasStore() && repo.IsInitialized(scope.Root),
		Root:           scope.Root,
		Actor:          s.actor,
		SuggestedActor: repo.DeriveActor(),
	}
	if scope.hasStore() {
		resp.SuggestedPrefix = repo.DerivePrefix(scope.Root)
	}
	if resp.Initialized {
		if cfg, err := store.New(scope.Root).Config(); err == nil {
			resp.Prefix = cfg.Prefix
			resp.States = cfg.States
			resp.Closed = cfg.Closed
			resp.Initial = cfg.Initial
			resp.Review = cfg.Review()
			resp.CheckShell = cfg.CheckShell
		}
	}
	return resp
}

// --- DTOs ---

type statusResp struct {
	// CarbonVersion is a deprecated, non-authoritative product-semantic alias. New
	// clients must use requestedCompatLayer together with the embedded contract.
	CarbonVersion string `json:"carbonVersion"`
	compat.Contract
	Scope           scopeDTO `json:"scope"`
	Initialized     bool     `json:"initialized"`
	Root            string   `json:"root"`
	Prefix          string   `json:"prefix,omitempty"`
	SuggestedPrefix string   `json:"suggestedPrefix"`
	States          []string `json:"states,omitempty"`
	Closed          []string `json:"closed,omitempty"`
	Initial         string   `json:"initial,omitempty"`
	Review          string   `json:"review,omitempty"`
	CheckShell      string   `json:"checkShell,omitempty"`
	Actor           string   `json:"actor"`
	SuggestedActor  string   `json:"suggestedActor"`
}

type taskDTO struct {
	ID             string              `json:"id"`
	Title          string              `json:"title"`
	Status         string              `json:"status"`
	Assignee       string              `json:"assignee,omitempty"`
	ProjectID      string              `json:"projectId,omitempty"`
	Type           string              `json:"type,omitempty"`
	Importance     string              `json:"importance,omitempty"`
	Version        string              `json:"version,omitempty"`
	Deps           []string            `json:"deps,omitempty"`
	Ready          bool                `json:"ready"`
	UpdatedAt      string              `json:"updatedAt,omitempty"`
	Rank           float64             `json:"rank,omitempty"`
	Labels         []string            `json:"labels,omitempty"`
	Priority       string              `json:"priority,omitempty"`
	Parent         string              `json:"parent,omitempty"`
	BlockerReason  string              `json:"blockerReason,omitempty"`
	Evidence       []task.Evidence     `json:"evidence,omitempty"`
	ActiveAttempt  string              `json:"activeAttempt,omitempty"`
	Lease          *task.Lease         `json:"lease,omitempty"`
	PendingClaims  []task.ClaimRequest `json:"pendingClaims,omitempty"`
	ExecutionState string              `json:"executionState,omitempty"`
	SessionID      string              `json:"sessionId,omitempty"`
	Checks         []checkDTO          `json:"checks,omitempty"`
	Provenance     []provDTO           `json:"provenance,omitempty"`
	Body           string              `json:"body,omitempty"`
}

type checkDTO struct {
	Desc    string `json:"desc"`
	Cmd     string `json:"cmd,omitempty"`
	Type    string `json:"type,omitempty"`
	Result  string `json:"result,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

type provDTO struct {
	ID       string `json:"id,omitempty"`
	Who      string `json:"who"`
	At       string `json:"at"`
	Did      string `json:"did"`
	Text     string `json:"text,omitempty"`
	EditedAt string `json:"editedAt,omitempty"`
}

func dtoFromTask(t task.Task, ready bool) taskDTO {
	d := taskDTO{ID: t.ID, Title: t.Title, Status: t.Status, Assignee: t.Assignee, ProjectID: t.ProjectID,
		Type: t.Type, Importance: t.Importance, Version: t.Version, Deps: t.Deps,
		Ready: ready, Rank: t.Rank, Labels: t.Labels, Priority: t.Priority, Parent: t.Parent,
		BlockerReason: t.BlockerReason, Evidence: t.Evidence, ActiveAttempt: t.ActiveAttempt, Lease: t.Lease, PendingClaims: t.PendingClaims}
	for _, c := range t.Checks {
		d.Checks = append(d.Checks, checkDTO{Desc: c.Desc, Cmd: c.Cmd, Type: c.Type, Result: c.Result, Cwd: c.Cwd, Timeout: c.Timeout})
	}
	return d
}

func dtoFromDoc(svc *mcp.Service, doc *store.Doc) taskDTO {
	d := dtoFromTask(doc.Task, svc.ReadyOf(doc.Task))
	d.ExecutionState, d.SessionID = svc.ExecutionOf(doc.Task)
	d.Body = doc.Body
	if n := len(doc.Provenance); n > 0 {
		d.UpdatedAt = doc.Provenance[n-1].At
	}
	for _, p := range doc.Provenance {
		d.Provenance = append(d.Provenance, provDTO{ID: p.ID, Who: p.Who, At: p.At, Did: p.Did, Text: p.Text, EditedAt: p.EditedAt})
	}
	return d
}

// --- responses ---

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Body == nil {
		return true
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBodyBytes))
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errBody(fmt.Errorf("JSON body exceeds %d bytes", maxJSONBodyBytes)))
			return false
		}
		writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("invalid JSON: %w", err)))
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			writeJSON(w, http.StatusBadRequest, errBody(errors.New("invalid JSON: multiple values")))
		} else {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeJSON(w, http.StatusRequestEntityTooLarge, errBody(fmt.Errorf("JSON body exceeds %d bytes", maxJSONBodyBytes)))
			} else {
				writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("invalid JSON: %w", err)))
			}
		}
		return false
	}
	return true
}

// expectedVersion prefers the standard HTTP If-Match header while accepting the
// body-level field used by MCP/JSON clients that cannot set headers. A weak ETag is
// intentionally rejected: task versions are strong content fingerprints.
func expectedVersion(r *http.Request, body string) string {
	if value := strings.TrimSpace(r.Header.Get("If-Match")); value != "" {
		return value
	}
	return strings.TrimSpace(body)
}

func includeCluster(r *http.Request) bool {
	value := r.URL.Query().Get("include_cluster")
	if value == "" {
		value = r.URL.Query().Get("all_projects")
	}
	b, _ := strconv.ParseBool(value)
	return b
}

func writeTaskJSON(w http.ResponseWriter, code int, svc *mcp.Service, doc *store.Doc) {
	if tag := doc.ETag(); tag != "" {
		w.Header().Set("ETag", tag)
	}
	writeJSON(w, code, dtoFromDoc(svc, doc))
}

func writeVersionConflict(w http.ResponseWriter, svc *mcp.Service, id string, err error) {
	current := ""
	etag := ""
	includeCluster := !svc.Scope().IsStandalone()
	if doc, getErr := svc.GetScoped(id, includeCluster); getErr == nil {
		current = doc.Version()
		etag = doc.ETag()
	}
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"error": err.Error(),
		"code":  "version_mismatch",
		"conflict": map[string]any{
			"retryable":      true,
			"currentVersion": current,
			"currentEtag":    etag,
		},
	})
}

func errBody(err error) map[string]string { return map[string]string{"error": err.Error()} }

// writeErr maps domain errors to HTTP status codes so the UI can react (and show the
// gate reason for a refused transition).
func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	conflictCode := ""
	switch {
	case errors.Is(err, store.ErrNotFound),
		errors.Is(err, store.ErrTrashNotFound),
		errors.Is(err, views.ErrNotFound),
		errors.Is(err, templates.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, store.ErrNoteNotFound):
		code = http.StatusNotFound
	case errors.Is(err, store.ErrSessionNotFound):
		code = http.StatusNotFound
	case errors.Is(err, mcp.ErrAlreadyClaimed),
		errors.Is(err, lease.ErrLeaseHeld),
		errors.Is(err, lease.ErrLeaseOwner),
		errors.Is(err, lease.ErrApprovalPending),
		errors.Is(err, lease.ErrForceRequired),
		errors.Is(err, store.ErrAssigneeForceRequired),
		errors.Is(err, store.ErrConflict),
		errors.Is(err, store.ErrVersionMismatch),
		errors.Is(err, store.ErrSessionConflict),
		errors.Is(err, store.ErrLiveSession),
		errors.Is(err, session.ErrTerminal):
		code = http.StatusConflict
		if errors.Is(err, store.ErrVersionMismatch) {
			conflictCode = "version_mismatch"
		} else {
			conflictCode = "conflict"
		}
	case errors.Is(err, task.ErrDepsNotClosed),
		errors.Is(err, task.ErrChecksNotPassed),
		errors.Is(err, task.ErrUnknownState),
		errors.Is(err, check.ErrCwdOutsideRoot),
		errors.Is(err, store.ErrInvalidID),
		errors.Is(err, store.ErrInvalidSessionID),
		errors.Is(err, store.ErrWorktreeOutsideRoot),
		errors.Is(err, task.ErrParentMissing),
		errors.Is(err, task.ErrParentCycle),
		errors.Is(err, task.ErrDanglingDep),
		errors.Is(err, task.ErrCycle),
		errors.Is(err, task.ErrInvalidPriority),
		errors.Is(err, task.ErrInvalidType),
		errors.Is(err, task.ErrInvalidImportance),
		errors.Is(err, task.ErrInvalidBlockerReason),
		errors.Is(err, task.ErrInvalidEvidence),
		errors.Is(err, task.ErrHasChildren),
		errors.Is(err, task.ErrHasDependents),
		errors.Is(err, store.ErrNotEditable),
		errors.Is(err, mcp.ErrEmptyTitle),
		errors.Is(err, mcp.ErrNotManual),
		errors.Is(err, mcp.ErrProjectScope),
		errors.Is(err, mcp.ErrStandaloneClusterScope),
		errors.Is(err, mcp.ErrExecutionProjectRequired),
		errors.Is(err, mcp.ErrAssigneeLeaseRequired),
		errors.Is(err, mcp.ErrExpectedVersionsRequired),
		errors.Is(err, mcp.ErrProjectWriteScope),
		errors.Is(err, mcp.ErrProjectMoveRequired),
		errors.Is(err, mcp.ErrIdentityMismatch),
		errors.Is(err, mcp.ErrClientMismatch),
		errors.Is(err, mcp.ErrIdempotencyRequired),
		errors.Is(err, mcp.ErrSessionActor),
		errors.Is(err, mcp.ErrTaskClosed),
		errors.Is(err, lease.ErrLeaseNotFound),
		errors.Is(err, lease.ErrInvalidTTL),
		errors.Is(err, lease.ErrInvalidActor),
		errors.Is(err, lease.ErrInvalidLease),
		errors.Is(err, lease.ErrRequestNotFound),
		errors.Is(err, lease.ErrReasonRequired),
		errors.Is(err, store.ErrAuditReasonRequired),
		errors.Is(err, views.ErrInvalidView),
		errors.Is(err, templates.ErrInvalidTemplate),
		errors.Is(err, tasktypes.ErrInvalidKey),
		errors.Is(err, tasktypes.ErrInvalidDisplayName),
		errors.Is(err, config.ErrInvalidConfig),
		errors.Is(err, session.ErrSummaryRequired),
		errors.Is(err, session.ErrReasonRequired):
		code = http.StatusUnprocessableEntity
	}
	if code == http.StatusConflict {
		writeJSON(w, code, map[string]any{
			"error": err.Error(),
			"code":  conflictCode,
			"conflict": map[string]any{
				"retryable": conflictCode == "version_mismatch",
			},
		})
		return
	}
	writeJSON(w, code, errBody(err))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
