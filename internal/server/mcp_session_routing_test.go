package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"carbon/internal/compat"
	"carbon/internal/home"
	"carbon/internal/store"
)

type projectSessionIdentityWire struct {
	Actor            string `json:"actor"`
	BindingMode      string `json:"bindingMode"`
	SelectionVersion uint64 `json:"selectionVersion"`
	Scope            struct {
		Mode       string `json:"mode"`
		Home       string `json:"home"`
		ClusterID  string `json:"clusterId"`
		ProjectID  string `json:"projectId"`
		Standalone bool   `json:"standalone"`
	} `json:"scope"`
}

type projectSessionProjectWire struct {
	Project struct {
		CanonicalID string `json:"canonicalId"`
		ClusterID   string `json:"clusterId"`
		Standalone  bool   `json:"standalone"`
	} `json:"project"`
}

type projectSessionClusterWire struct {
	Cluster struct {
		CanonicalID string `json:"canonicalId"`
	} `json:"cluster"`
}

type projectSessionTaskWire struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
}

type projectSessionSelectionWire struct {
	BindingMode      string `json:"bindingMode"`
	SelectionVersion uint64 `json:"selectionVersion"`
	Scope            struct {
		ClusterID  string `json:"clusterId"`
		ProjectID  string `json:"projectId"`
		Standalone bool   `json:"standalone"`
	} `json:"scope"`
}

// TestStreamableMCPProjectSessionCreateSelectSwitchAndIdentity is the transport
// contract for a long-lived agent connection. It intentionally exercises both a
// standalone project and a shared-cluster project: no task is allowed before an
// active selection, create_project selects its new project, and later explicit
// select_project calls direct writes to the newly selected physical store.
func TestStreamableMCPProjectSessionCreateSelectSwitchAndIdentity(t *testing.T) {
	homeRoot := t.TempDir()
	alphaSource := t.TempDir()
	betaSource := t.TempDir()
	api, err := NewWithScopeAndCompatibility("human:test", ScopeDefaults{
		Home: homeRoot, HomeByDefault: true,
	}, CompatibilityOptions{ProductVersion: "0.4.8", RequestedCompatLayer: compat.StableLayer})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(api.Handler())
	// streamableToolNames registers a client-session cleanup. Register the HTTP
	// server cleanup first so the MCP DELETE runs before the listener is closed;
	// this avoids leaving a Windows test process with a live file-backed session
	// while TempDir cleanup starts.
	t.Cleanup(httpServer.Close)

	query := url.Values{
		"home":    []string{homeRoot},
		"actor":   []string{"agent:session-routing"},
		"client":  []string{"codex"},
		"routing": []string{mcpRoutingSession},
	}
	mcpSession, names := streamableToolNames(t, httpServer.URL+"/mcp?"+query.Encode())
	for _, name := range []string{"identity", "create_project", "select_project", "create", "list"} {
		if !names[name] {
			t.Errorf("project-session tools/list omitted %q: %v", name, names)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	callTool := func(name string, args map[string]any, out any) *mcpsdk.CallToolResult {
		t.Helper()
		result, err := mcpSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out != nil && !result.IsError {
			data, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatalf("marshal %s result: %v", name, err)
			}
			if err := json.Unmarshal(data, out); err != nil {
				t.Fatalf("decode %s result: %v; raw=%s", name, err, data)
			}
		}
		return result
	}

	var initial projectSessionIdentityWire
	if result := callTool("identity", nil, &initial); result.IsError {
		t.Fatalf("initial identity returned MCP error: %+v", result.Content)
	}
	if initial.Actor != "agent:session-routing" || initial.BindingMode != "session" || initial.SelectionVersion != 0 || initial.Scope.Mode != "carbon_home" || initial.Scope.ProjectID != "" {
		t.Fatalf("initial project-session identity = %+v", initial)
	}
	// The task surface is listed so clients can retain one tool schema, but until a
	// selection it must be an error rather than a fallback write into the home.
	if result := callTool("create", map[string]any{"title": "must select first", "type": "patch", "importance": "normal"}, nil); !result.IsError {
		t.Fatal("unselected project-session create unexpectedly succeeded")
	}

	var alpha projectSessionProjectWire
	if result := callTool("create_project", map[string]any{
		"name": "Alpha", "slug": "alpha-session", "source_path": alphaSource,
		"allow_create": true, "reason": "initialize the agent's first project",
	}, &alpha); result.IsError {
		t.Fatalf("create standalone project returned MCP error: %+v", result.Content)
	}
	if alpha.Project.CanonicalID == "" || !alpha.Project.Standalone || alpha.Project.ClusterID != "" {
		t.Fatalf("created standalone project = %+v", alpha)
	}
	var afterAlpha projectSessionIdentityWire
	callTool("identity", nil, &afterAlpha)
	if afterAlpha.SelectionVersion != 1 || afterAlpha.Scope.ProjectID != alpha.Project.CanonicalID || !afterAlpha.Scope.Standalone {
		t.Fatalf("identity after standalone create = %+v", afterAlpha)
	}
	var alphaTask projectSessionTaskWire
	callTool("create", map[string]any{"title": "alpha before switch", "type": "patch", "importance": "normal"}, &alphaTask)
	if alphaTask.ID == "" || alphaTask.ProjectID != alpha.Project.CanonicalID {
		t.Fatalf("standalone task = %+v", alphaTask)
	}

	var cluster projectSessionClusterWire
	if result := callTool("create_cluster", map[string]any{
		"name": "Team", "slug": "team-session", "prefix": "TEAM", "allow_create": true, "reason": "add shared project surface",
	}, &cluster); result.IsError {
		t.Fatalf("create shared cluster returned MCP error: %+v", result.Content)
	}
	if cluster.Cluster.CanonicalID == "" {
		t.Fatalf("created cluster = %+v", cluster)
	}
	var beta projectSessionProjectWire
	if result := callTool("create_project", map[string]any{
		"cluster": cluster.Cluster.CanonicalID, "name": "Beta", "slug": "beta-session", "source_path": betaSource,
		"allow_create": true, "reason": "add the shared project selected by this agent",
	}, &beta); result.IsError {
		t.Fatalf("create shared project returned MCP error: %+v", result.Content)
	}
	if beta.Project.CanonicalID == "" || beta.Project.Standalone || beta.Project.ClusterID != cluster.Cluster.CanonicalID {
		t.Fatalf("created shared project = %+v", beta)
	}
	var afterBetaCreate projectSessionIdentityWire
	callTool("identity", nil, &afterBetaCreate)
	if afterBetaCreate.SelectionVersion != 2 || afterBetaCreate.Scope.ProjectID != beta.Project.CanonicalID || afterBetaCreate.Scope.ClusterID != cluster.Cluster.CanonicalID || afterBetaCreate.Scope.Standalone {
		t.Fatalf("identity after shared create = %+v", afterBetaCreate)
	}

	// Select Alpha explicitly, write there, then switch back to the shared project.
	var alphaSelection projectSessionSelectionWire
	if result := callTool("select_project", map[string]any{"project": alpha.Project.CanonicalID}, &alphaSelection); result.IsError {
		t.Fatalf("select standalone project returned MCP error: %+v", result.Content)
	}
	if alphaSelection.BindingMode != "session" || alphaSelection.SelectionVersion != 3 || alphaSelection.Scope.ProjectID != alpha.Project.CanonicalID || !alphaSelection.Scope.Standalone {
		t.Fatalf("standalone selection = %+v", alphaSelection)
	}
	var alphaAfterSwitch projectSessionTaskWire
	callTool("create", map[string]any{"title": "alpha after explicit select", "type": "patch", "importance": "normal"}, &alphaAfterSwitch)
	if alphaAfterSwitch.ProjectID != alpha.Project.CanonicalID {
		t.Fatalf("post-select standalone task = %+v", alphaAfterSwitch)
	}
	var betaSelection projectSessionSelectionWire
	if result := callTool("select_project", map[string]any{"cluster": cluster.Cluster.CanonicalID, "project": beta.Project.CanonicalID}, &betaSelection); result.IsError {
		t.Fatalf("switch to shared project returned MCP error: %+v", result.Content)
	}
	if betaSelection.SelectionVersion != 4 || betaSelection.Scope.ProjectID != beta.Project.CanonicalID || betaSelection.Scope.ClusterID != cluster.Cluster.CanonicalID || betaSelection.Scope.Standalone {
		t.Fatalf("shared selection = %+v", betaSelection)
	}
	var betaTask projectSessionTaskWire
	callTool("create", map[string]any{"title": "beta after switch", "type": "patch", "importance": "normal"}, &betaTask)
	if betaTask.ID == "" || betaTask.ProjectID != beta.Project.CanonicalID {
		t.Fatalf("shared task = %+v", betaTask)
	}
	var finalIdentity projectSessionIdentityWire
	callTool("identity", nil, &finalIdentity)
	if finalIdentity.Actor != "agent:session-routing" || finalIdentity.BindingMode != "session" || finalIdentity.SelectionVersion != 4 || finalIdentity.Scope.ProjectID != beta.Project.CanonicalID || finalIdentity.Scope.ClusterID != cluster.Cluster.CanonicalID {
		t.Fatalf("identity after switch = %+v", finalIdentity)
	}

	alphaResolved, err := home.ResolveProject(homeRoot, home.ResolveProjectRequest{ProjectID: alpha.Project.CanonicalID})
	if err != nil {
		t.Fatalf("resolve standalone project: %v", err)
	}
	if doc, err := store.New(alphaResolved.DataRoot).Get(alphaTask.ID); err != nil || doc.Task.ProjectID != alpha.Project.CanonicalID {
		t.Fatalf("alpha task was not retained in standalone root: doc=%#v err=%v", doc, err)
	}
	if doc, err := store.New(alphaResolved.DataRoot).Get(alphaAfterSwitch.ID); err != nil || doc.Task.ProjectID != alpha.Project.CanonicalID {
		t.Fatalf("post-select alpha task was not retained in standalone root: doc=%#v err=%v", doc, err)
	}
	betaResolved, err := home.ResolveProject(homeRoot, home.ResolveProjectRequest{ClusterID: cluster.Cluster.CanonicalID, ProjectID: beta.Project.CanonicalID})
	if err != nil {
		t.Fatalf("resolve shared project: %v", err)
	}
	if doc, err := store.New(betaResolved.DataRoot).Get(betaTask.ID); err != nil || doc.Task.ProjectID != beta.Project.CanonicalID {
		t.Fatalf("beta task was not retained in shared root: doc=%#v err=%v", doc, err)
	}
	if _, err := store.New(betaResolved.DataRoot).Get(alphaTask.ID); err == nil {
		t.Fatal("standalone task leaked into shared project root")
	}
}

// Session routing is a new, opt-in MCP transport only. This test fixes the
// compatibility boundary: ordinary home catalog and already-pinned Carbon routes
// keep their existing tool surfaces, while attempts to mix `routing=session` with
// a selected project or a legacy repository fail before the MCP handler starts.
func TestStreamableMCPProjectSessionRoutingIsOptInAndFixedScopesStayFixed(t *testing.T) {
	homeRoot := t.TempDir()
	fixedSource := t.TempDir()
	fixed, err := home.AddStandaloneProject(homeRoot, home.AddProjectRequest{Name: "Fixed", SourcePath: fixedSource})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Fixed shared", Prefix: "FIX"})
	if err != nil {
		t.Fatal(err)
	}
	fixedShared, err := home.AddProject(homeRoot, shared.ID, home.AddProjectRequest{Name: "Fixed shared project", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	api, err := NewWithScopeAndCompatibility("human:test", ScopeDefaults{
		Home: homeRoot, HomeByDefault: true,
	}, CompatibilityOptions{ProductVersion: "0.4.8", RequestedCompatLayer: compat.StableLayer})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(api.Handler())
	t.Cleanup(httpServer.Close)

	homeQuery := url.Values{"home": []string{homeRoot}, "actor": []string{"agent:catalog"}}
	_, catalogTools := streamableToolNames(t, httpServer.URL+"/mcp?"+homeQuery.Encode())
	if catalogTools["select_project"] || catalogTools["create"] {
		t.Fatalf("ordinary home-only MCP unexpectedly became a session/task connection: %v", catalogTools)
	}

	fixedQuery := url.Values{"home": []string{homeRoot}, "project": []string{fixed.ID}, "actor": []string{"agent:fixed"}}
	_, fixedTools := streamableToolNames(t, httpServer.URL+"/mcp?"+fixedQuery.Encode())
	if fixedTools["select_project"] || !fixedTools["create"] {
		t.Fatalf("pinned project MCP surface changed: %v", fixedTools)
	}
	clusterQuery := url.Values{"home": []string{homeRoot}, "cluster": []string{shared.ID}, "project": []string{fixedShared.ID}, "actor": []string{"agent:fixed-cluster"}}
	_, clusterTools := streamableToolNames(t, httpServer.URL+"/mcp?"+clusterQuery.Encode())
	if clusterTools["select_project"] || !clusterTools["create"] {
		t.Fatalf("pinned cluster project MCP surface changed: %v", clusterTools)
	}

	for _, endpoint := range []string{
		"/mcp?" + url.Values{"home": []string{homeRoot}, "project": []string{fixed.ID}, "actor": []string{"agent:bad"}, "routing": []string{mcpRoutingSession}}.Encode(),
		"/mcp?" + url.Values{"repo": []string{t.TempDir()}, "actor": []string{"agent:bad"}, "routing": []string{mcpRoutingSession}}.Encode(),
		"/mcp?" + url.Values{"home": []string{homeRoot}, "actor": []string{"agent:bad"}, "routing": []string{"unknown"}}.Encode(),
	} {
		if code, body := raw(api.Handler(), http.MethodPost, endpoint, `{}`); code != http.StatusBadRequest || body == "" {
			t.Fatalf("%s = %d %q, want routing validation 400", endpoint, code, body)
		}
	}

	// Streamable HTTP sessions are owned by separate handler maps. A client that
	// initialized a selectable session cannot omit routing=session later and have
	// the fixed catalog handler silently continue the mutable binding.
	sessionQuery := url.Values{"home": []string{homeRoot}, "actor": []string{"agent:drop-routing"}, "routing": []string{mcpRoutingSession}}
	initialize := httptest.NewRequest(http.MethodPost, "/mcp?"+sessionQuery.Encode(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"routing-test","version":"0"}}}`))
	initialize.Header.Set("Content-Type", "application/json")
	initialize.Header.Set("Accept", "application/json, text/event-stream")
	initializeW := httptest.NewRecorder()
	api.Handler().ServeHTTP(initializeW, initialize)
	if initializeW.Code != http.StatusOK {
		t.Fatalf("initialize selectable MCP session = %d %s", initializeW.Code, initializeW.Body.String())
	}
	sessionID := initializeW.Header().Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("selectable MCP initialize returned no Mcp-Session-Id")
	}
	t.Cleanup(func() {
		closeRequest := httptest.NewRequest(http.MethodDelete, "/mcp?"+sessionQuery.Encode(), nil)
		closeRequest.Header.Set("Mcp-Session-Id", sessionID)
		closeW := httptest.NewRecorder()
		api.Handler().ServeHTTP(closeW, closeRequest)
	})
	droppedQuery := url.Values{"home": []string{homeRoot}, "actor": []string{"agent:drop-routing"}}
	dropped := httptest.NewRequest(http.MethodPost, "/mcp?"+droppedQuery.Encode(), strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))
	dropped.Header.Set("Content-Type", "application/json")
	dropped.Header.Set("Accept", "application/json, text/event-stream")
	dropped.Header.Set("Mcp-Session-Id", sessionID)
	dropped.Header.Set("MCP-Protocol-Version", "2025-06-18")
	droppedW := httptest.NewRecorder()
	api.Handler().ServeHTTP(droppedW, dropped)
	if droppedW.Code != http.StatusNotFound {
		t.Fatalf("dropped session routing = %d %s, want 404 from fixed handler", droppedW.Code, droppedW.Body.String())
	}
}
