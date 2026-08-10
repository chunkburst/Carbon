package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"carbon/internal/store"
)

func TestUpdateToolExposesDepsPatchContract(t *testing.T) {
	svc := service(t, "agent:update")
	dep, err := svc.Create(store.Draft{Title: "dependency"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := svc.Create(store.Draft{Title: "target"})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := NewServer(svc).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "update-contract-test", Version: "0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	tool := mcpToolByName(t, clientSession, ctx, "update")
	encodedSchema, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(encodedSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Properties["deps"]; !ok {
		t.Fatalf("update schema omitted deps: %s", encodedSchema)
	}

	callUpdate := func(args map[string]any) taskOut {
		t.Helper()
		result, err := clientSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: "update", Arguments: args})
		if err != nil {
			t.Fatalf("update call: %v", err)
		}
		if result.IsError {
			t.Fatalf("update tool error: %+v", result.Content)
		}
		var out taskOut
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	updated := callUpdate(map[string]any{
		"id": target.Task.ID, "deps": []string{dep.Task.ID}, "expected_version": target.ETag(),
	})
	if len(updated.Deps) != 1 || updated.Deps[0] != dep.Task.ID {
		t.Fatalf("deps update = %+v", updated)
	}
	preserved := callUpdate(map[string]any{
		"id": target.Task.ID, "labels": []string{"without-deps"}, "expected_version": updated.Version,
	})
	if len(preserved.Deps) != 1 || preserved.Deps[0] != dep.Task.ID {
		t.Fatalf("omitted deps changed dependencies: %+v", preserved.Deps)
	}
	cleared := callUpdate(map[string]any{
		"id": target.Task.ID, "deps": []string{}, "expected_version": preserved.Version,
	})
	if len(cleared.Deps) != 0 {
		t.Fatalf("empty deps did not clear dependencies: %+v", cleared.Deps)
	}

	stale, err := clientSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: "update", Arguments: map[string]any{
		"id": target.Task.ID, "deps": []string{dep.Task.ID}, "expected_version": updated.Version,
	}})
	if err != nil {
		t.Fatalf("stale update call: %v", err)
	}
	if !stale.IsError {
		t.Fatalf("stale expected_version was accepted: %+v", stale)
	}
	content, err := json.Marshal(stale.Content)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "version") {
		t.Fatalf("stale update error = %s, want version conflict", content)
	}
}
