package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"carbon/internal/repo"
	"carbon/internal/store"
)

func TestServerInitializeInstructionsDescribeStableV2Contract(t *testing.T) {
	srv := NewServer(NewService(store.New(t.TempDir()), "agent:instructions", nil))
	ctx := context.Background()
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "instructions-test", Version: "0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	result := clientSession.InitializeResult()
	if result == nil {
		t.Fatal("initialize result is nil")
	}
	if result.ServerInfo == nil || result.ServerInfo.Name != "carbon" {
		t.Fatalf("MCP implementation = %+v, want canonical carbon", result.ServerInfo)
	}
	for _, want := range []string{
		"Use identity first", "fixed actor", "include_cluster=true", "expected_version",
		"set_blocker", "add_evidence", "remove_evidence", "worker_private", "project_public",
		"global_public", "Carbon v2 is the approved stable layer",
		"Identity Mode", "worklog_draft_send", "identity-draft",
		"historical claim tool is registered only for frozen legacy v1", "v1 is the frozen LegacyLayer",
	} {
		if !strings.Contains(result.Instructions, want) {
			t.Errorf("initialize instructions missing %q:\n%s", want, result.Instructions)
		}
	}
}

// TestServerEndToEnd drives the full MCP path: a real client calls the registered tools
// over an in-memory transport, exercising schema marshaling and the create -> claim ->
// run_checks -> transition lifecycle (SPEC §11.7 dogfood, in miniature).
func TestServerEndToEnd(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, repo.CarbonDirName, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "prefix: PROJ\ncounter: 0\nstates: [backlog, in_progress, done, canceled]\nclosed: [done, canceled]\ninitial: backlog\ncheck_timeout_default: 30\n"
	if err := os.WriteFile(filepath.Join(root, repo.CarbonDirName, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 6, 21, 9, 0, 0, 0, time.UTC)
	svc := NewServiceWithClient(store.New(root), "agent:claude-1", "claude", func() time.Time { return at })
	srv := NewServer(svc)

	ctx := context.Background()
	clientT, serverT := mcpsdk.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	callRaw := func(name string, args map[string]any, out any) {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s call: %v", name, err)
		}
		if res.IsError {
			t.Fatalf("%s returned tool error: %+v", name, res.Content)
		}
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s decode: %v", name, err)
		}
	}
	call := func(name string, args map[string]any) taskOut {
		t.Helper()
		var out taskOut
		callRaw(name, args, &out)
		return out
	}

	var identity Identity
	callRaw("identity", nil, &identity)
	if identity.Actor != "agent:claude-1" || identity.Client != "claude" {
		t.Fatalf("identity = %+v", identity)
	}

	created := call("create", map[string]any{
		"title":  "ship it",
		"checks": []map[string]any{{"desc": "tests", "cmd": "exit 0"}},
	})
	if !taskIDRe.MatchString(created.ID) {
		t.Fatalf("create id = %q, want match %s", created.ID, taskIDRe)
	}

	claimed := call("claim", map[string]any{"id": created.ID})
	if claimed.Assignee != "agent:claude-1" {
		t.Fatalf("assignee = %q", claimed.Assignee)
	}

	done := call("transition", map[string]any{"id": created.ID, "to": "done"})
	if done.Status != "done" {
		t.Fatalf("status = %q, want done", done.Status)
	}
	if done.Checks[0].Result != "pass" {
		t.Fatalf("check result = %q, want pass", done.Checks[0].Result)
	}

	sessionTask := call("create", map[string]any{"title": "observable"})
	var begun SessionView
	callRaw("begin", map[string]any{
		"id": sessionTask.ID, "expected_actor": "agent:claude-1", "client": "claude", "idempotency_key": "begin-observable",
	}, &begun)
	if begun.TaskID != sessionTask.ID || begun.Status != "active" {
		t.Fatalf("begun session = %+v", begun)
	}
	var heartbeat SessionView
	callRaw("heartbeat", map[string]any{"session": begun.ID, "progress": "testing"}, &heartbeat)
	if heartbeat.Live == nil || heartbeat.Live.Progress != "testing" {
		t.Fatalf("heartbeat = %+v", heartbeat)
	}
	var finished SessionView
	callRaw("finish", map[string]any{"session": begun.ID, "summary": "verified"}, &finished)
	if finished.Status != "finished" {
		t.Fatalf("finished session = %+v", finished)
	}
}
