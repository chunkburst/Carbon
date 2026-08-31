package mcp

import (
	"context"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"carbon/internal/compat"
	"carbon/internal/repo"
	"carbon/internal/store"
)

func listedToolNames(t *testing.T, svc *Service) map[string]bool {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := NewServer(svc).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "compat-test", Version: "0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	names := make(map[string]bool)
	for cursor := ""; ; {
		result, err := clientSession.ListTools(ctx, &mcpsdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		for _, tool := range result.Tools {
			names[tool.Name] = true
		}
		if result.NextCursor == "" {
			return names
		}
		cursor = result.NextCursor
	}
}

func requireTools(t *testing.T, names map[string]bool, want ...string) {
	t.Helper()
	for _, name := range want {
		if !names[name] {
			t.Errorf("tools/list omitted %q; got %v", name, names)
		}
	}
}

func rejectTools(t *testing.T, names map[string]bool, forbidden ...string) {
	t.Helper()
	for _, name := range forbidden {
		if names[name] {
			t.Errorf("tools/list exposed incompatible tool %q; got %v", name, names)
		}
	}
}

func TestToolRegistrationUsesCompatibilityAndScopeBoundary(t *testing.T) {
	legacy := NewService(store.New(t.TempDir()), "agent:legacy", nil)
	legacyTools := listedToolNames(t, legacy)
	requireTools(t, legacyTools, "identity", "list", "create", "claim", "transition", "list_sessions")
	rejectTools(t, legacyTools,
		"list_clusters", "create_cluster", "list_projects", "create_project",
		"list_types", "create_type", "search", "lease_claim", "lease_status",
		"trash", "trash_many", "list_trash", "restore_trash", "bulk_update", "bulk_move",
		"list_views", "list_templates", "worker_stats", "set_blocker", "add_evidence", "remove_evidence",
		"worklog_create", "worklog_get", "worklog_list", "worklog_update", "worklog_delete", "worklog_draft_send",
		"worker_identity_list", "worker_identity_get", "worker_identity_claim",
		"subscription_initialize", "events_poll")

	carbonRoot := t.TempDir()
	if err := repo.InitDataRoot(carbonRoot, "CAR"); err != nil {
		t.Fatal(err)
	}
	carbon := NewScopedService(store.New(carbonRoot), "agent:carbon", Scope{
		Home: "home", ClusterID: "cluster", ProjectID: "project", CompatLayer: compat.StableLayer,
	}, nil)
	carbonTools := listedToolNames(t, carbon)
	requireTools(t, carbonTools,
		"lease_claim",
		"set_blocker", "add_evidence", "remove_evidence",
		"worklog_create", "worklog_get", "worklog_list", "worklog_update", "worklog_delete", "worklog_draft_send",
		"worker_identity_list", "worker_identity_get", "worker_identity_claim",
		"subscription_initialize", "events_poll")
	rejectTools(t, carbonTools, "claim")

	homeRoot := t.TempDir()
	homeOnly := NewScopedService(store.New(homeRoot), "agent:catalog", Scope{
		Home: homeRoot, CompatLayer: compat.StableLayer,
	}, nil)
	homeTools := listedToolNames(t, homeOnly)
	requireTools(t, homeTools,
		"identity", "list_clusters", "resolve_cluster", "create_cluster",
		"list_projects", "resolve_project", "create_project")
	rejectTools(t, homeTools,
		"list", "get", "create", "update", "claim", "transition", "begin", "list_sessions",
		"search", "lease_claim", "trash", "bulk_update", "bulk_move", "worker_stats",
		"worklog_create", "worklog_get", "worklog_list", "worklog_update", "worklog_delete", "worklog_draft_send",
		"worker_identity_list", "worker_identity_get", "worker_identity_claim")
}

func TestToolRegistrationFailsClosedForCrossScopeLayer(t *testing.T) {
	service := NewScopedService(store.New(t.TempDir()), "agent:bad", Scope{
		Home: t.TempDir(), CompatLayer: compat.LegacyLayer,
	}, nil)
	names := listedToolNames(t, service)
	requireTools(t, names, "identity")
	rejectTools(t, names, "list_clusters", "create_cluster", "list", "create", "lease_claim")
}

func TestCarbonBlockerEvidenceToolsRequireVersionAndReuseValidation(t *testing.T) {
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "CAR"); err != nil {
		t.Fatal(err)
	}
	svc := NewScopedService(store.New(root), "agent:carbon", Scope{
		Home: "home", ClusterID: "cluster", ProjectID: "project", CompatLayer: compat.StableLayer,
	}, nil)
	doc, err := svc.CreateContext(context.Background(), store.Draft{Title: "proof", Type: "patch", Importance: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := NewServer(svc).Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "evidence-contract-test", Version: "0"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	for name, args := range map[string]map[string]any{
		"set_blocker":     {"id": doc.Task.ID, "blocker_reason": "waiting"},
		"add_evidence":    {"id": doc.Task.ID, "kind": "artifact", "value": "build.zip"},
		"remove_evidence": {"id": doc.Task.ID, "evidence_id": "e_missing"},
	} {
		result, err := clientSession.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s transport error: %v", name, err)
		}
		if !result.IsError {
			t.Fatalf("%s accepted a missing expected_version: %+v", name, result)
		}
	}

	result, err := clientSession.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "set_blocker", Arguments: map[string]any{
		"id": doc.Task.ID, "blocker_reason": "bad\x00reason", "expected_version": doc.Version(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("set_blocker accepted an invalid control character: %+v", result)
	}
}
