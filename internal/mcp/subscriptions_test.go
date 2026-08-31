package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	mcpSDK "github.com/modelcontextprotocol/go-sdk/mcp"

	"carbon/internal/compat"
	"carbon/internal/incident"
	"carbon/internal/projectpolicy"
	"carbon/internal/repo"
	"carbon/internal/store"
	"carbon/internal/subscription"
)

func newSubscriptionServices(t *testing.T) (*store.Store, *Service, *Service) {
	t.Helper()
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "EVT"); err != nil {
		t.Fatal(err)
	}
	data := store.New(root)
	for _, projectID := range []string{"project_a", "project_b"} {
		if _, err := projectpolicy.New(data).Save(context.Background(), "human:lead", projectpolicy.Policy{Version: 1, ProjectID: projectID}); err != nil {
			t.Fatal(err)
		}
	}
	now := func() time.Time { return time.Date(2026, 8, 30, 15, 0, 0, 0, time.UTC) }
	newService := func(projectID string) *Service {
		return NewScopedService(data, "agent:alice", Scope{
			Home: "home", ClusterID: "cluster", ProjectID: projectID, SourcePath: t.TempDir(), CompatLayer: compat.StableLayer,
		}, now)
	}
	return data, newService("project_a"), newService("project_b")
}

func connectSubscriptionMCP(t *testing.T, svc *Service) *mcpSDK.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := mcpSDK.NewInMemoryTransports()
	serverSession, err := NewServer(svc).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcpSDK.NewClient(&mcpSDK.Implementation{Name: "subscription-vertical-test", Version: "0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func callSubscriptionTool(t *testing.T, client *mcpSDK.ClientSession, name string, arguments map[string]any, out any) {
	t.Helper()
	result, err := client.CallTool(context.Background(), &mcpSDK.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s transport: %v", name, err)
	}
	if result.IsError {
		t.Fatalf("%s tool error: %#v", name, result.Content)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal %s result: %v", name, err)
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		t.Fatalf("decode %s result: %v\n%s", name, err, encoded)
	}
}

func initializeSubscriptionOverMCP(t *testing.T, client *mcpSDK.ClientSession, id, key string, modules []string, taskFilters map[string]any, incidentFilters map[string]any) subscription.InitializeResult {
	t.Helper()
	var out subscription.InitializeResult
	args := map[string]any{
		"subscription_id": id,
		"idempotency_key": key,
		"mode":            "active",
		"modules":         modules,
	}
	if taskFilters != nil {
		args["task_filters"] = taskFilters
	}
	if incidentFilters != nil {
		args["incident_filters"] = incidentFilters
	}
	callSubscriptionTool(t, client, "subscription_initialize", args, &out)
	return out
}

func TestMCPEventSubscriptionVerticalSlice(t *testing.T) {
	data, serviceA, serviceB := newSubscriptionServices(t)
	clientA := connectSubscriptionMCP(t, serviceA)

	// This is a real registered MCP tool path, not a direct Manager test: it
	// establishes the recipient before mutations and confirms active is honestly
	// downgraded to durable polling.
	initial := initializeSubscriptionOverMCP(t, clientA, "main", "main-init", []string{"tasks", "incidents"}, nil, nil)
	if initial.Delivery.RequestedMode != subscription.ModeActive || initial.Delivery.EffectiveDelivery != "poll" || initial.Delivery.PushSupported {
		t.Fatalf("active delivery claim = %#v, want honest poll fallback", initial.Delivery)
	}
	if initial.Cursor == "" || initial.Subscription.Version != 1 {
		t.Fatalf("initialize result = %#v", initial)
	}
	// Identical idempotent initialization returns the same durable subscription,
	// not a second record or a second cursor secret.
	repeated := initializeSubscriptionOverMCP(t, clientA, "main", "main-init", []string{"tasks", "incidents"}, nil, nil)
	if repeated.Subscription.ID != initial.Subscription.ID || repeated.Subscription.Version != initial.Subscription.Version || repeated.Cursor != initial.Cursor {
		t.Fatalf("idempotent initialize changed result: first=%#v repeat=%#v", initial, repeated)
	}
	if _, err := serviceA.InitializeEventSubscription(context.Background(), subscription.InitializeInput{
		SubscriptionID: "main", IdempotencyKey: "main-init", Mode: subscription.ModePassive, Modules: []subscription.Module{subscription.ModuleTasks},
	}); !errors.Is(err, subscription.ErrIdempotencyConflict) {
		t.Fatalf("same idempotency key with different request = %v, want conflict", err)
	}
	strictResult, err := clientA.CallTool(context.Background(), &mcpSDK.CallToolParams{Name: "events_poll", Arguments: map[string]any{
		"subscription_id": "main", "unexpected": true,
	}})
	if err == nil && !strictResult.IsError {
		t.Fatalf("events_poll accepted an unknown argument: %#v", strictResult)
	}

	var created taskOut
	callSubscriptionTool(t, clientA, "create", map[string]any{"title": "subscription task", "type": "patch", "importance": "normal"}, &created)
	var updated taskOut
	callSubscriptionTool(t, clientA, "update", map[string]any{"id": created.ID, "title": "subscription task updated", "expected_version": created.Version}, &updated)
	var blocked taskOut
	callSubscriptionTool(t, clientA, "set_blocker", map[string]any{"id": created.ID, "blocker_reason": "waiting for fixture", "expected_version": updated.Version}, &blocked)
	var unblocked taskOut
	callSubscriptionTool(t, clientA, "set_blocker", map[string]any{"id": created.ID, "blocker_reason": "", "expected_version": blocked.Version}, &unblocked)
	var claimed leaseClaimOut
	callSubscriptionTool(t, clientA, "lease_claim", map[string]any{"id": created.ID, "reason": "start investigation", "expected_version": unblocked.Version}, &claimed)
	if claimed.Pending || claimed.Task.Lease == nil || claimed.Task.Lease.Holder != "agent:alice" {
		t.Fatalf("lease claim = %#v", claimed)
	}
	var transitioned taskOut
	callSubscriptionTool(t, clientA, "transition", map[string]any{"id": created.ID, "to": "in_progress"}, &transitioned)

	var createdIncident incident.Incident
	callSubscriptionTool(t, clientA, "incident_create", map[string]any{"kind": "investigation", "title": "retry behavior", "body": "private body must not be in ledger", "severity": "high"}, &createdIncident)
	var reply incident.Reply
	callSubscriptionTool(t, clientA, "incident_reply", map[string]any{"id": createdIncident.ID, "body": "private reply must not be in ledger"}, &reply)
	var changedIncident incident.Incident
	callSubscriptionTool(t, clientA, "incident_update", map[string]any{"id": createdIncident.ID, "status": "investigating"}, &changedIncident)

	var polled subscription.PollResult
	callSubscriptionTool(t, clientA, "events_poll", map[string]any{"subscription_id": "main", "limit": 50}, &polled)
	if len(polled.Events) < 9 { // create/update/blocked/unblocked/lease/status + incident create/reply/status
		t.Fatalf("safe event count = %d, events=%#v", len(polled.Events), polled.Events)
	}
	wantKinds := map[string]bool{"created": false, "updated": false, "blocked": false, "unblocked": false, "lease_claimed": false, "status_changed": false, "reply_added": false}
	for _, event := range polled.Events {
		if event.ProjectID != "project_a" || event.ResourceID == "" || event.OccurredAt == "" {
			t.Fatalf("unsafe/incomplete event %#v", event)
		}
		if _, ok := wantKinds[event.Kind]; ok {
			wantKinds[event.Kind] = true
		}
		if event.Module == subscription.ModuleIncidents && (event.Severity == "" || event.IncidentKind == "") {
			t.Fatalf("incident event omitted routing fields %#v", event)
		}
	}
	for kind, seen := range wantKinds {
		if !seen {
			t.Errorf("event ledger omitted %q: %#v", kind, polled.Events)
		}
	}
	if polled.Cursor == "" {
		t.Fatal("poll did not return a durable cursor")
	}

	// The filtered subscription receives only an eligible process event. The
	// main subscription still keeps normal events in the ledger, proving filters
	// are delivery filters rather than source-write blockers.
	filtered := initializeSubscriptionOverMCP(t, clientA, "high-incidents", "high-init", []string{"incidents"}, nil, map[string]any{"severities": []string{"urgent"}})
	var normalIncident incident.Incident
	callSubscriptionTool(t, clientA, "incident_create", map[string]any{"kind": "sudden", "title": "normal event", "severity": "normal"}, &normalIncident)
	var filteredPoll subscription.PollResult
	callSubscriptionTool(t, clientA, "events_poll", map[string]any{"subscription_id": "high-incidents", "cursor": filtered.Cursor, "limit": 10}, &filteredPoll)
	if len(filteredPoll.Events) != 0 {
		t.Fatalf("urgent filter received normal Incident: %#v", filteredPoll.Events)
	}
	var urgentIncident incident.Incident
	callSubscriptionTool(t, clientA, "incident_create", map[string]any{"kind": "sudden", "title": "urgent event", "severity": "urgent"}, &urgentIncident)
	callSubscriptionTool(t, clientA, "events_poll", map[string]any{"subscription_id": "high-incidents", "cursor": filteredPoll.Cursor, "limit": 10}, &filteredPoll)
	if len(filteredPoll.Events) != 1 || filteredPoll.Events[0].ResourceID != urgentIncident.ID {
		t.Fatalf("urgent filter result = %#v", filteredPoll.Events)
	}

	// A new Service over the same managed Store is the restart boundary. Its old
	// cursor continues and sees only the new source event, never a duplicate.
	restarted := NewScopedService(data, "agent:alice", serviceA.Scope(), func() time.Time { return time.Date(2026, 8, 30, 15, 1, 0, 0, time.UTC) })
	priority := "high"
	if _, err := restarted.UpdateWithVersion(created.ID, UpdateFields{Priority: &priority}, transitioned.Version); err != nil {
		t.Fatalf("restart source mutation: %v", err)
	}
	continued, err := restarted.PollEventSubscription(context.Background(), subscription.PollInput{SubscriptionID: "main", Cursor: polled.Cursor, Limit: 10})
	continuedTask := false
	for _, event := range continued.Events {
		if event.ResourceID == created.ID && event.Kind == "updated" {
			continuedTask = true
		}
	}
	if err != nil || !continuedTask {
		t.Fatalf("restart poll = %#v err=%v", continued, err)
	}

	// Same cluster, different project: each project owns its own subscriptions,
	// ledger, and Incident store. A cursor signed for A cannot cross to B.
	clientB := connectSubscriptionMCP(t, serviceB)
	initialB := initializeSubscriptionOverMCP(t, clientB, "main", "b-init", []string{"tasks", "incidents"}, nil, nil)
	var taskB taskOut
	callSubscriptionTool(t, clientB, "create", map[string]any{"title": "project B task", "type": "patch", "importance": "normal"}, &taskB)
	var incidentB incident.Incident
	callSubscriptionTool(t, clientB, "incident_create", map[string]any{"kind": "sudden", "title": "project B incident", "severity": "normal"}, &incidentB)
	if listed, err := serviceB.ListIncidents(); err != nil || len(listed) != 1 || listed[0].ID != incidentB.ID {
		t.Fatalf("project B incidents = %#v err=%v", listed, err)
	}
	if listed, err := serviceA.ListIncidents(); err != nil || len(listed) < 3 {
		t.Fatalf("project A incidents = %#v err=%v", listed, err)
	}
	if _, err := serviceB.PollEventSubscription(context.Background(), subscription.PollInput{SubscriptionID: "main", Cursor: continued.Cursor, Limit: 10}); !errors.Is(err, subscription.ErrInvalidCursor) {
		t.Fatalf("cross-project cursor = %v, want invalid cursor", err)
	}
	var polledB subscription.PollResult
	callSubscriptionTool(t, clientB, "events_poll", map[string]any{"subscription_id": "main", "cursor": initialB.Cursor, "limit": 10}, &polledB)
	if len(polledB.Events) != 2 {
		t.Fatalf("project B events = %#v", polledB.Events)
	}
}

func TestEventRecoveryUsesActualTaskSourceAndNeverSurfacesAbsentSource(t *testing.T) {
	data, serviceA, _ := newSubscriptionServices(t)
	if _, err := serviceA.InitializeEventSubscription(context.Background(), subscription.InitializeInput{
		SubscriptionID: "recovery", IdempotencyKey: "recovery-init", Mode: subscription.ModePassive, Modules: []subscription.Module{subscription.ModuleTasks},
	}); err != nil {
		t.Fatal(err)
	}
	task, err := serviceA.CreateContext(context.Background(), store.Draft{Title: "recovery task", Type: "patch", Importance: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	manager, projectID, err := serviceA.eventSubscriptionManager()
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after an actual task source save and before ledger commit.
	doc, err := data.Get(task.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	doc.AppendProvenance("agent:alice", "recovery source", "", time.Date(2026, 8, 30, 15, 2, 0, 0, time.UTC))
	err = data.Write(context.Background(), "agent:alice", "simulate source-only task mutation", func(tx *store.WriteTx) error {
		if _, err := serviceA.prepareTaskEventTx(tx, manager, projectID, doc, "updated"); err != nil {
			return err
		}
		return tx.SaveTask(doc)
	})
	if err != nil {
		t.Fatal(err)
	}
	polled, err := serviceA.PollEventSubscription(context.Background(), subscription.PollInput{SubscriptionID: "recovery", Limit: 10})
	if err != nil || len(polled.Events) != 2 { // initial task create + recovered source
		t.Fatalf("recovered real task source = %#v err=%v", polled, err)
	}

	// A marker whose task provenance did not land is removed by the service's
	// source verifier and cannot leak through events_poll.
	err = data.Write(context.Background(), "agent:alice", "simulate absent task source", func(tx *store.WriteTx) error {
		prepared, err := manager.PrepareTx(tx, subscription.EventInput{
			ProjectID: projectID, OccurredAt: time.Date(2026, 8, 30, 15, 3, 0, 0, time.UTC), Module: subscription.ModuleTasks,
			Kind: "updated", ResourceID: task.Task.ID, Actor: "agent:alice", Status: task.Task.Status, Type: task.Task.Type, Importance: task.Task.Importance,
		}, subscription.SourceRef{Kind: subscription.SourceTaskProvenance, ResourceID: task.Task.ID, MutationID: "missing-source"})
		if err != nil {
			return err
		}
		if prepared.Event.ID == "" {
			return errors.New("expected absent-source marker")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	continued, err := serviceA.PollEventSubscription(context.Background(), subscription.PollInput{SubscriptionID: "recovery", Cursor: polled.Cursor, Limit: 10})
	if err != nil || len(continued.Events) != 0 {
		t.Fatalf("absent source escaped recovery = %#v err=%v", continued, err)
	}
}

func TestProjectSessionSwitchRejectsPriorSubscriptionCursor(t *testing.T) {
	main := t.TempDir()
	binding, err := NewProjectSession(store.New(main), "agent:session", "codex", main, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, client := connectProjectSessionServer(t, NewProjectSessionServer(binding))
	first := createProjectThroughSession(t, client, ctx, "Subscription A", t.TempDir())
	var initialized subscription.InitializeResult
	callProjectSessionTool(t, client, ctx, "subscription_initialize", map[string]any{
		"subscription_id": "session-main", "idempotency_key": "a-init", "mode": "passive", "modules": []string{"tasks"},
	}, &initialized)
	var taskA taskOut
	callProjectSessionTool(t, client, ctx, "create", map[string]any{"title": "session task", "type": "patch", "importance": "normal"}, &taskA)
	var pollA subscription.PollResult
	callProjectSessionTool(t, client, ctx, "events_poll", map[string]any{"subscription_id": "session-main", "cursor": initialized.Cursor, "limit": 10}, &pollA)
	if len(pollA.Events) != 1 || pollA.Events[0].ProjectID != first.Project.CanonicalID {
		t.Fatalf("project A session events = %#v", pollA.Events)
	}

	second := createProjectThroughSession(t, client, ctx, "Subscription B", t.TempDir())
	if second.Project.CanonicalID == first.Project.CanonicalID {
		t.Fatal("project session did not switch to a distinct project")
	}
	var initializedB subscription.InitializeResult
	callProjectSessionTool(t, client, ctx, "subscription_initialize", map[string]any{
		"subscription_id": "session-main", "idempotency_key": "b-init", "mode": "passive", "modules": []string{"tasks"},
	}, &initializedB)
	result, err := client.CallTool(ctx, &mcpSDK.CallToolParams{Name: "events_poll", Arguments: map[string]any{
		"subscription_id": "session-main", "cursor": pollA.Cursor, "limit": 10,
	}})
	if err != nil {
		t.Fatalf("switched events_poll transport: %v", err)
	}
	if !result.IsError {
		t.Fatalf("project-switched cursor was accepted: %#v", result.StructuredContent)
	}
	if initializedB.Cursor == pollA.Cursor {
		t.Fatal("different selected project unexpectedly reused the cursor")
	}
}
