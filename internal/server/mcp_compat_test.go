package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"carbon/internal/compat"
	"carbon/internal/repo"
)

func streamableToolNames(t *testing.T, endpoint string) (*mcpsdk.ClientSession, map[string]bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "server-compat-test", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint: endpoint, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect streamable MCP: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	names := make(map[string]bool)
	for cursor := ""; ; {
		result, err := session.ListTools(ctx, &mcpsdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		for _, tool := range result.Tools {
			names[tool.Name] = true
		}
		if result.NextCursor == "" {
			return session, names
		}
		cursor = result.NextCursor
	}
}

func TestStreamableMCPHomeOnlyExposesCatalogWithoutTaskStore(t *testing.T) {
	homeRoot := t.TempDir()
	api, err := NewWithScopeAndCompatibility("human:test", ScopeDefaults{
		Home: homeRoot, HomeByDefault: true,
	}, CompatibilityOptions{ProductVersion: "0.4.8", RequestedCompatLayer: compat.StableLayer})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(api.Handler())
	defer httpServer.Close()
	query := url.Values{"home": []string{homeRoot}, "actor": []string{"agent:catalog"}}
	var identity struct {
		Scope struct {
			Mode string `json:"mode"`
		} `json:"scope"`
		compat.Contract
	}
	call(t, api.Handler(), http.MethodGet, "/api/identity?"+query.Encode(), "", &identity)
	if identity.Scope.Mode != "carbon_home" || identity.RequestedCompatLayer != compat.StableLayer || identity.StableCompatLayer != compat.StableLayer {
		t.Fatalf("home-only HTTP identity = %+v", identity)
	}
	session, names := streamableToolNames(t, httpServer.URL+"/mcp?"+query.Encode())

	for _, name := range []string{"identity", "list_clusters", "create_cluster", "resolve_cluster", "list_projects", "create_project", "resolve_project"} {
		if !names[name] {
			t.Errorf("home-only tools/list omitted %q: %v", name, names)
		}
	}
	for _, name := range []string{"list", "get", "create", "update", "claim", "transition", "lease_claim", "trash", "bulk_update", "worker_stats"} {
		if names[name] {
			t.Errorf("home-only tools/list exposed task tool %q: %v", name, names)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	call := func(name string, args map[string]any, out any) {
		t.Helper()
		result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if result.IsError {
			t.Fatalf("%s returned MCP tool error: %+v", name, result.Content)
		}
		data, _ := json.Marshal(result.StructuredContent)
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
	}
	var created struct {
		Cluster struct {
			CanonicalID string `json:"canonicalId"`
		} `json:"cluster"`
	}
	call("create_cluster", map[string]any{"name": "Catalog", "slug": "catalog", "allow_create": true, "reason": "transport regression"}, &created)
	if created.Cluster.CanonicalID == "" {
		t.Fatalf("create_cluster returned no canonical id: %+v", created)
	}
	var listed struct {
		Clusters []struct {
			CanonicalID string `json:"canonicalId"`
		} `json:"clusters"`
	}
	call("list_clusters", nil, &listed)
	if len(listed.Clusters) != 1 || listed.Clusters[0].CanonicalID != created.Cluster.CanonicalID {
		t.Fatalf("list_clusters = %+v, want %q", listed, created.Cluster.CanonicalID)
	}
	var resolved struct {
		Cluster struct {
			CanonicalID string `json:"canonicalId"`
		} `json:"cluster"`
	}
	call("resolve_cluster", map[string]any{"reference": "CATALOG"}, &resolved)
	if resolved.Cluster.CanonicalID != created.Cluster.CanonicalID {
		t.Fatalf("resolve_cluster = %+v, want %q", resolved, created.Cluster.CanonicalID)
	}

	if _, err := os.Stat(filepath.Join(homeRoot, repo.CarbonDirName, "tasks")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("home-only catalog transport initialized a task store: %v", err)
	}
}

func TestCompatibilityRejectsCrossScopeLayerBeforeMCPTransport(t *testing.T) {
	if _, err := NewWithScopeAndCompatibility("human:test", ScopeDefaults{
		Home: t.TempDir(), HomeByDefault: true,
	}, CompatibilityOptions{ProductVersion: "0.4.8", RequestedCompatLayer: compat.LegacyLayer}); !errors.Is(err, compat.ErrLayerScopeMismatch) {
		t.Fatalf("Carbon v1 constructor = %v, want ErrLayerScopeMismatch", err)
	}
	if _, err := NewWithScopeAndCompatibility("human:test", ScopeDefaults{LegacyRoot: t.TempDir()}, CompatibilityOptions{
		ProductVersion: "0.4.8", RequestedCompatLayer: compat.StableLayer,
	}); !errors.Is(err, compat.ErrLayerScopeMismatch) {
		t.Fatalf("legacy v2 constructor = %v, want ErrLayerScopeMismatch", err)
	}

	// A legacy process pinned to v1 cannot be used to tunnel into a Carbon home
	// through the Streamable HTTP query parameters.
	homeRoot := t.TempDir()
	if _, err := os.Stat(homeRoot); err != nil {
		t.Fatal(err)
	}
	api, err := NewWithScopeAndCompatibility("human:test", ScopeDefaults{LegacyRoot: t.TempDir()}, CompatibilityOptions{
		ProductVersion: "0.4.8", RequestedCompatLayer: compat.LegacyLayer,
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "/mcp?actor=agent%3Atest&home=" + url.QueryEscape(homeRoot)
	if code, body := raw(api.Handler(), http.MethodPost, endpoint, `{}`); code != http.StatusBadRequest || body == "" {
		t.Fatalf("legacy v1 -> Carbon MCP = %d %q, want compatibility 400", code, body)
	}
}
