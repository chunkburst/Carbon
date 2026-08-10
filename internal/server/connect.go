package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"carbon/internal/compat"
	"carbon/internal/connect"
	"carbon/internal/home"
)

// Integration endpoints let the UI wire agents to the locally installed Carbon binary.
// Carbon writes the configuration into an explicitly selected source tree, while the
// generated command carries stable Carbon scope ids to the selected private store. A
// cluster-wide binding is optional; a standalone project instead carries --home plus
// --project and never inherits sibling access from the local config file.
func (s *Server) handleListIntegrations(w http.ResponseWriter, r *http.Request) {
	route, err := connectRouteForRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	scope, err := s.connectScope(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	carbon, err := carbonConnectScopeForRoute(scope, route)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	root, manual, err := executionSourceForConnectRoute(scope, connectConfigProjectID(r, ""), route)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
		return
	}
	if manual {
		writeJSON(w, http.StatusOK, map[string]any{
			"agents": []connect.AgentStatus{}, "manual": true,
			"reason": connectManualReason(route),
		})
		return
	}
	var agents []connect.AgentStatus
	if scope.Legacy {
		agents, err = connect.Detect(root)
	} else {
		// Carbon configuration is an exact stable-id binding, not merely a local
		// config file that happens to contain a Carbon entry. A different home,
		// cluster, project, or stable compatibility layer must remain visibly disconnected.
		agents, err = connect.DetectCarbon(root, carbon)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

type connectReq struct {
	// Path remains a legacy body shorthand. Carbon requests identify scope through the
	// standard query/header fields so a source path can never override stable ids.
	Path            string `json:"path"`
	Actor           string `json:"actor"`
	ConfigProjectID string `json:"configProjectId"`
}

func (s *Server) handleConnectAgent(w http.ResponseWriter, r *http.Request) {
	var req connectReq
	if !decode(w, r, &req) {
		return
	}
	route, err := connectRouteForRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	scope, err := s.connectScope(r, req.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	carbon, err := carbonConnectScopeForRoute(scope, route)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	root, manual, err := executionSourceForConnectRoute(scope, connectConfigProjectID(r, req.ConfigProjectID), route)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
		return
	}
	if manual {
		if route == connectRouteSession {
			writeJSON(w, http.StatusUnprocessableEntity, errBody(errors.New(connectManualReason(route))))
			return
		}
		guide, err := connect.ManualGuideCarbon(r.PathValue("agent"), "", sanitizeActor(req.Actor), carbon)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"connected": false, "manual": true, "guide": guide,
			"reason": "cluster-scoped connection needs configProjectId for one-click configuration; no source path was guessed",
		})
		return
	}
	// Empty actor defaults to agent:<id>; never fall back to the human web actor.
	var path string
	if scope.Legacy {
		path, err = connect.Connect(r.PathValue("agent"), root, sanitizeActor(req.Actor))
	} else {
		path, err = connect.ConnectCarbon(r.PathValue("agent"), root, sanitizeActor(req.Actor), carbon)
	}
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "path": path})
}

func (s *Server) handleDisconnectAgent(w http.ResponseWriter, r *http.Request) {
	route, err := connectRouteForRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	scope, err := s.connectScope(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	carbon, err := carbonConnectScopeForRoute(scope, route)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	root, manual, err := executionSourceForConnectRoute(scope, connectConfigProjectID(r, ""), route)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
		return
	}
	if manual {
		if route == connectRouteSession {
			writeJSON(w, http.StatusUnprocessableEntity, errBody(errors.New(connectManualReason(route))))
		} else {
			writeJSON(w, http.StatusUnprocessableEntity, errBody(errors.New("cluster-scoped disconnect requires configProjectId to locate the agent configuration file")))
		}
		return
	}
	var path string
	if scope.Legacy {
		path, err = connect.Disconnect(r.PathValue("agent"), root)
	} else {
		// Preserve a configuration for another Carbon scope rather than removing a
		// similarly named Carbon entry from the selected source directory.
		path, err = connect.DisconnectCarbon(r.PathValue("agent"), root, carbon)
	}
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": false, "path": path})
}

func (s *Server) handleAgentManual(w http.ResponseWriter, r *http.Request) {
	route, err := connectRouteForRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	scope, err := s.connectScope(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	carbon, err := carbonConnectScopeForRoute(scope, route)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	root, manual, err := executionSourceForConnectRoute(scope, connectConfigProjectID(r, ""), route)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
		return
	}
	var guide connect.Guide
	if scope.Legacy {
		guide, err = connect.ManualGuide(r.PathValue("agent"), root, sanitizeActor(r.URL.Query().Get("actor")))
	} else {
		if manual {
			root = ""
		}
		guide, err = connect.ManualGuideCarbon(r.PathValue("agent"), root, sanitizeActor(r.URL.Query().Get("actor")), carbon)
	}
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
		return
	}
	writeJSON(w, http.StatusOK, guide)
}

func carbonConnectScope(scope requestScope) connect.CarbonScope {
	carbon := connect.CarbonScope{Home: scope.Home, ClusterID: scope.ClusterID, ProjectID: scope.ProjectID}
	if scope.Standalone {
		carbon.ScopeMode = connect.CarbonScopeModeStandalone
		return carbon
	}
	if scope.ProjectID == "" {
		// An empty project id must never quietly broaden an agent connection. This
		// route reaches cluster scope only through the explicit server-selected mode.
		carbon.ScopeMode = connect.CarbonScopeModeCluster
	} else {
		carbon.ScopeMode = connect.CarbonScopeModeProject
	}
	return carbon
}

// connectRoute is intentionally additive: requests without routing retain the
// long-standing exact selected-scope connection. A project-routing session is only
// available when the caller deliberately asks for routing=session.
type connectRoute string

const (
	connectRoutePinned  connectRoute = ""
	connectRouteSession connectRoute = "session"
)

func connectRouteForRequest(r *http.Request) (connectRoute, error) {
	values, present := r.URL.Query()["routing"]
	if !present {
		return connectRoutePinned, nil
	}
	if len(values) != 1 {
		return "", errors.New("routing must be specified at most once")
	}
	switch strings.TrimSpace(values[0]) {
	case "session":
		return connectRouteSession, nil
	default:
		return "", fmt.Errorf("unsupported Carbon connect routing %q", values[0])
	}
}

// carbonConnectScopeForRoute converts the selected HTTP scope into the exact process
// boundary written to the local agent config. The config project is deliberately not
// part of a session boundary: it identifies only which source tree owns the config.
func carbonConnectScopeForRoute(scope requestScope, route connectRoute) (connect.CarbonScope, error) {
	if route == connectRouteSession {
		if !scope.isHomeOnly() {
			return connect.CarbonScope{}, errors.New("routing=session requires a Home-only Carbon scope")
		}
		return connect.CarbonScope{Home: scope.Home, ScopeMode: connect.CarbonScopeModeSession}, nil
	}
	return carbonConnectScope(scope), nil
}

func connectManualReason(route connectRoute) string {
	if route == connectRouteSession {
		return "project-session connection requires configProjectId to locate an agent configuration file"
	}
	return "cluster-scoped connection needs configProjectId to locate an agent configuration file"
}

// connectConfigProjectID selects the source directory that contains an agent's local
// configuration. It does not affect the spawned MCP process's project scope. Accept the
// camelCase JSON/query spelling used by the UI and a snake_case query spelling for CLI
// clients without permitting an arbitrary filesystem path.
func connectConfigProjectID(r *http.Request, body string) string {
	if value := strings.TrimSpace(body); value != "" {
		return value
	}
	for _, key := range []string{"configProjectId", "config_project_id"} {
		if value := strings.TrimSpace(r.URL.Query().Get(key)); value != "" {
			return value
		}
	}
	return strings.TrimSpace(r.Header.Get("X-Carbon-Config-Project"))
}

func (s *Server) connectScope(r *http.Request, legacyPath string) (requestScope, error) {
	route, err := connectRouteForRequest(r)
	if err != nil {
		return requestScope{}, err
	}
	if route == connectRouteSession {
		return s.sessionConnectScope(r, legacyPath)
	}
	if strings.TrimSpace(legacyPath) != "" {
		if scopeValue(r, "home", "X-Carbon-Home") != "" || scopeValue(r, "cluster", "X-Carbon-Cluster") != "" || scopeValue(r, "project", "X-Carbon-Project") != "" {
			return requestScope{}, errors.New("legacy path cannot be combined with Carbon connect scope")
		}
		root, err := s.resolveRoot(legacyPath)
		if err != nil {
			return requestScope{}, err
		}
		return requestScope{Mode: "legacy", Root: root, Legacy: true}, nil
	}
	return s.resolveScope(r)
}

// sessionConnectScope creates the deliberately Home-only boundary used to write a
// project-routing MCP configuration. It bypasses launch-time selected cluster/project
// defaults only for routing=session; absent routing keeps the old pinned selection.
func (s *Server) sessionConnectScope(r *http.Request, legacyPath string) (requestScope, error) {
	query := r.URL.Query()
	// Home-only session routing is deliberately fail-closed. Presence matters here:
	// `?project=` is still an explicit attempt to select a project and must not be
	// collapsed into the Home-only mode by Query().Get's empty-string result.
	if strings.TrimSpace(legacyPath) != "" || hasRawQueryKey(query, "path") || hasRawQueryKey(query, "repo") {
		return requestScope{}, errors.New("routing=session cannot be combined with a legacy path or repo")
	}
	if hasRawQueryKey(query, "cluster") || hasRawQueryKey(query, "project") ||
		len(r.Header.Values("X-Carbon-Cluster")) != 0 || len(r.Header.Values("X-Carbon-Project")) != 0 {
		return requestScope{}, errors.New("routing=session requires a Home-only Carbon scope")
	}
	homeRoot := scopeValue(r, "home", "X-Carbon-Home")
	if homeRoot == "" {
		homeRoot = s.defaultHome
	}
	if homeRoot == "" {
		return requestScope{}, errors.New("routing=session requires a Carbon home")
	}
	resolvedHome, err := s.resolveRoot(homeRoot)
	if err != nil {
		return requestScope{}, err
	}
	if _, err := compat.Resolve(s.productVersion, s.compatLayer, compat.ModeCarbon); err != nil {
		return requestScope{}, err
	}
	return requestScope{Mode: "carbon", Home: resolvedHome}, nil
}

func hasRawQueryKey(values map[string][]string, key string) bool {
	_, present := values[key]
	return present
}

// executionSourceForConnect resolves only a trusted project source from the home
// manifest. A cluster-scoped connection without configProjectID deliberately returns
// manual=true rather than guessing one of several source trees.
func executionSourceForConnect(scope requestScope, configProjectID string) (root string, manual bool, err error) {
	if scope.Legacy {
		if strings.TrimSpace(configProjectID) != "" {
			return "", false, errors.New("configProjectId is only valid for a Carbon connection")
		}
		return scope.Root, false, nil
	}
	if scope.Mode != "carbon" || scope.Home == "" || scope.Root == "" {
		return "", false, errors.New("Carbon connect requires a home and selected task scope")
	}
	if scope.Standalone {
		projectID := strings.TrimSpace(configProjectID)
		if projectID != "" && projectID != scope.ProjectID {
			return "", false, errors.New("configProjectId must match the bound standalone project")
		}
		if scope.SourceOffline || strings.TrimSpace(scope.SourcePath) == "" {
			return "", false, errors.New("Carbon standalone project source is offline or its fingerprint no longer matches")
		}
		return scope.SourcePath, false, nil
	}
	if scope.ClusterID == "" {
		return "", false, errors.New("Carbon connect requires a cluster or standalone project scope")
	}
	projectID := strings.TrimSpace(configProjectID)
	if scope.ProjectID != "" {
		if projectID != "" && projectID != scope.ProjectID {
			return "", false, errors.New("configProjectId must match the bound Carbon project")
		}
		projectID = scope.ProjectID
		if scope.SourceOffline || strings.TrimSpace(scope.SourcePath) == "" {
			return "", false, errors.New("Carbon project source is offline or its fingerprint no longer matches")
		}
		return scope.SourcePath, false, nil
	}
	if projectID == "" {
		return "", true, nil
	}
	resolution, resolveErr := home.ResolveProject(scope.Home, home.ResolveProjectRequest{ClusterID: scope.ClusterID, ProjectID: projectID})
	if resolveErr != nil {
		return "", false, resolveErr
	}
	if resolution.Standalone {
		return "", false, errors.New("config project resolved outside the selected shared cluster")
	}
	if !sameScopePath(scope.Root, resolution.DataRoot) {
		return "", false, errors.New("config project resolved outside its cluster data root")
	}
	if resolution.Offline || strings.TrimSpace(resolution.SourcePath) == "" {
		return "", false, errors.New("Carbon config project source is offline or its fingerprint no longer matches")
	}
	return resolution.SourcePath, false, nil
}

// executionSourceForConnectRoute resolves the local config-file source independently
// from the process boundary. For a session, configProjectId is a lookup key in this
// exact Carbon home only; it never leaks into --cluster or --project arguments.
func executionSourceForConnectRoute(scope requestScope, configProjectID string, route connectRoute) (root string, manual bool, err error) {
	if route != connectRouteSession {
		return executionSourceForConnect(scope, configProjectID)
	}
	if strings.TrimSpace(configProjectID) == "" {
		// A session has a valid process boundary without a source tree. Let list and
		// manual-guide requests expose that exact config, while write/delete callers
		// reject it because they cannot safely choose a local agent-config file.
		return "", true, nil
	}
	root, err = executionSourceForProjectSession(scope, configProjectID)
	return root, false, err
}

func executionSourceForProjectSession(scope requestScope, configProjectID string) (string, error) {
	if !scope.isHomeOnly() {
		return "", errors.New("routing=session requires a Home-only Carbon scope")
	}
	projectID := strings.TrimSpace(configProjectID)
	if projectID == "" {
		return "", errors.New("routing=session requires configProjectId to locate an agent configuration file")
	}
	// Resolve only through scope.Home. This is the same-home check: an id that exists
	// in another Carbon home is not allowed to select that other source directory.
	resolution, err := home.ResolveProject(scope.Home, home.ResolveProjectRequest{ProjectID: projectID})
	if err != nil {
		return "", err
	}
	if resolution.Offline || strings.TrimSpace(resolution.SourcePath) == "" {
		return "", errors.New("Carbon config project source is offline or its fingerprint no longer matches")
	}
	return resolution.SourcePath, nil
}
