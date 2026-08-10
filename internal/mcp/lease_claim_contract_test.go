package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"carbon/internal/compat"
	"carbon/internal/repo"
	"carbon/internal/store"
)

func carbonLeaseClaimSession(t *testing.T, svc *Service) (*mcpsdk.ClientSession, context.Context) {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := NewServer(svc).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect lease claim server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "lease-contract-test", Version: "0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect lease claim client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession, ctx
}

func mcpToolByName(t *testing.T, session *mcpsdk.ClientSession, ctx context.Context, name string) *mcpsdk.Tool {
	t.Helper()
	for cursor := ""; ; {
		result, err := session.ListTools(ctx, &mcpsdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		for _, tool := range result.Tools {
			if tool.Name == name {
				return tool
			}
		}
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}
	t.Fatalf("tools/list omitted %q", name)
	return nil
}

func assertMCPLeaseClaimUnchanged(t *testing.T, st *store.Store, id, version string) {
	t.Helper()
	doc, err := st.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Version() != version {
		t.Fatalf("failed MCP lease claim changed version: got %q, want %q", doc.Version(), version)
	}
	if doc.Task.Assignee != "" || doc.Task.Lease != nil || len(doc.Task.PendingClaims) != 0 {
		t.Fatalf("failed MCP lease claim changed lease state: %+v", doc.Task)
	}
}

func TestCarbonMCPLeaseClaimRequiresContractFields(t *testing.T) {
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "MCP"); err != nil {
		t.Fatal(err)
	}
	st := store.New(root)
	doc, err := st.Create(store.Draft{Title: "MCP claim target", ProjectIDSet: true}, "human:seed", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewScopedService(st, "agent:lease", Scope{
		Home: t.TempDir(), ClusterID: "cluster-lease", ClusterScope: true, CompatLayer: compat.StableLayer,
	}, nil)
	session, ctx := carbonLeaseClaimSession(t, svc)
	tool := mcpToolByName(t, session, ctx, "lease_claim")
	encodedSchema, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(encodedSchema, &schema); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"reason", "expected_version"} {
		found := false
		for _, required := range schema.Required {
			if required == field {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("lease_claim schema required = %v, missing %q", schema.Required, field)
		}
	}

	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "blank reason",
			args: map[string]any{"id": doc.Task.ID, "reason": "   ", "expected_version": doc.Version()},
			want: "reason is required",
		},
		{
			name: "blank expected version",
			args: map[string]any{"id": doc.Task.ID, "reason": "claim regression coverage", "expected_version": "   "},
			want: "expected_version",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "lease_claim", Arguments: tc.args})
			if err != nil {
				t.Fatalf("lease_claim call: %v", err)
			}
			if !result.IsError {
				t.Fatalf("lease_claim accepted invalid input: %+v", result)
			}
			content, err := json.Marshal(result.Content)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(content), tc.want) {
				t.Fatalf("lease_claim error = %s, want %q", content, tc.want)
			}
			assertMCPLeaseClaimUnchanged(t, st, doc.Task.ID, doc.Version())
		})
	}
}
