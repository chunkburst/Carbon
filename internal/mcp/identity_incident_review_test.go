package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	mcpSDK "github.com/modelcontextprotocol/go-sdk/mcp"

	"carbon/internal/incident"
	"carbon/internal/projectpolicy"
	"carbon/internal/repo"
	"carbon/internal/review"
	"carbon/internal/store"
	taskpkg "carbon/internal/task"
)

func newIdentityIncidentReviewServices(t *testing.T) (*store.Store, *Service, *Service, *Service) {
	t.Helper()
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "IIR"); err != nil {
		t.Fatal(err)
	}
	data := store.New(root)
	if _, err := projectpolicy.New(data).Save(context.Background(), "human:lead", projectpolicy.Policy{Version: 1, ProjectID: "project_one"}); err != nil {
		t.Fatal(err)
	}
	scope := Scope{Home: "home", ClusterID: "cluster", ProjectID: "project_one", SourcePath: t.TempDir()}
	now := func() time.Time { return time.Date(2026, 8, 30, 14, 0, 0, 0, time.UTC) }
	return data, NewScopedService(data, "human:lead", scope, now), NewScopedService(data, "agent:reviewer", scope, now), NewScopedService(data, "agent:other", scope, now)
}

func setIdentityPolicy(t *testing.T, data *store.Store, identityMode, noTraceMode bool) {
	t.Helper()
	if _, err := projectpolicy.New(data).Save(context.Background(), "human:lead", projectpolicy.Policy{Version: 1, ProjectID: "project_one", IdentityMode: identityMode, NoTraceMode: noTraceMode}); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityAuditNoTraceIncidentAndReviewVerticalSlice(t *testing.T) {
	data, human, reviewer, other := newIdentityIncidentReviewServices(t)
	ctx := context.Background()

	// Human administration is deliberately available before IdentityMode turns on.
	identityResult, err := human.ManageWorkerIdentity(ctx, "agent:reviewer", WorkerIdentityClaimInput{Roles: []string{"reviewer", "backend"}, Types: []string{"patch"}})
	if err != nil {
		t.Fatal(err)
	}
	if identityResult.ModeEnabled || len(identityResult.Record.Roles) != 2 || identityResult.Record.Roles[0] != "reviewer" {
		t.Fatalf("human preconfigured identity = %#v", identityResult)
	}
	audits, err := human.ListWorkerIdentityAudit()
	if err != nil || len(audits.Audits) != 1 || audits.Audits[0].RelatedIncidentID == "" {
		t.Fatalf("identity audit = %#v err=%v", audits, err)
	}
	auto, err := human.GetIncident(audits.Audits[0].RelatedIncidentID)
	if err != nil || auto.Origin != incident.OriginIdentityChange || auto.Kind != incident.KindIdentityChange || auto.RelatedAuditID != audits.Audits[0].ID {
		t.Fatalf("automatic identity incident = %#v err=%v", auto, err)
	}
	if _, err := reviewer.ReplyIncident(ctx, auto.ID, "我已接收审核职责。"); err != nil {
		t.Fatal(err)
	}

	// NoTrace suppresses only the optional process Incident; permanent audit still grows.
	setIdentityPolicy(t, data, false, true)
	if _, err := human.ManageWorkerIdentity(ctx, "agent:reviewer", WorkerIdentityClaimInput{Roles: []string{"reviewer", "backend", "architect"}, Types: []string{"patch"}, Reason: "加入架构协作"}); err != nil {
		t.Fatal(err)
	}
	audits, err = human.ListWorkerIdentityAudit()
	if err != nil || len(audits.Audits) != 2 || audits.Audits[1].RelatedIncidentID != "" {
		t.Fatalf("no trace audit = %#v err=%v", audits, err)
	}
	incidents, err := human.ListIncidents()
	if err != nil || len(incidents) != 1 {
		t.Fatalf("no trace incident list = %#v err=%v", incidents, err)
	}

	// Turn on enforcement: Agent self-service is now allowed, and reviewer role is
	// required for a review target assigned to an Agent.
	setIdentityPolicy(t, data, true, true)
	if _, err := other.ClaimWorkerIdentity(ctx, WorkerIdentityClaimInput{Roles: []string{"backend"}, Types: []string{"patch"}}); err != nil {
		t.Fatalf("Agent self claim after mode enabled = %v", err)
	}
	if _, err := human.ManageWorkerIdentity(ctx, "agent:other", WorkerIdentityClaimInput{Roles: []string{"backend"}, Types: []string{"patch"}}); err != nil {
		t.Fatal(err)
	}
	task, err := human.CreateContext(ctx, store.Draft{Title: "linked task", Type: "patch", Importance: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	manual, err := human.CreateIncident(ctx, incident.CreateInput{Kind: incident.KindInvestigation, RelatedTaskIDs: []string{task.Task.ID}, Title: "429 incident", Body: "先绕开，再研究", Severity: incident.SeverityHigh})
	if err != nil || len(manual.RelatedTaskIDs) != 1 {
		t.Fatalf("manual incident = %#v err=%v", manual, err)
	}
	if _, err := reviewer.ReplyIncident(ctx, manual.ID, "先记录当前重试窗口。"); err != nil {
		t.Fatal(err)
	}
	createdReview, err := human.CreateReviewTarget(ctx, review.CreateInput{TargetKind: review.TargetPlan, TargetID: task.Task.ID, TaskID: task.Task.ID, ReviewerActor: "agent:reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.DecideReviewTarget(ctx, createdReview.ID, review.DecideInput{Status: review.StatusApproved, Decision: "越权"}); !errors.Is(err, ErrReviewDecisionForbidden) {
		t.Fatalf("peer review decision = %v, want forbidden", err)
	}
	decided, err := reviewer.DecideReviewTarget(ctx, createdReview.ID, review.DecideInput{Status: review.StatusApproved, Decision: "计划可以进入实现。"})
	if err != nil || decided.Status != review.StatusApproved || decided.ResolvedBy != "agent:reviewer" {
		t.Fatalf("review decision = %#v err=%v", decided, err)
	}
	manualTask, err := human.CreateContext(ctx, store.Draft{Title: "manual review", Type: "patch", Importance: "normal", Checks: []taskpkg.Check{{Desc: "human sign-off", Type: "manual"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := human.CreateReviewTarget(ctx, review.CreateInput{TargetKind: review.TargetManualCheck, TargetID: manualTask.Task.ID + "#check:0", TaskID: manualTask.Task.ID, CheckID: "0", ReviewerActor: "agent:reviewer"}); err != nil {
		t.Fatalf("manual review target = %v", err)
	}
	if _, err := human.CreateReviewTarget(ctx, review.CreateInput{TargetKind: review.TargetManualCheck, TargetID: manualTask.Task.ID + "#check:1", TaskID: manualTask.Task.ID, CheckID: "1", ReviewerActor: "agent:reviewer"}); !errors.Is(err, review.ErrInvalidReview) {
		t.Fatalf("invalid manual review target = %v, want invalid review", err)
	}

	// Task provenance count is unchanged by Incident discussion; Incidents are not
	// market/task activity and therefore cannot fake freshness.
	got, err := human.GetScoped(task.Task.ID, false)
	if err != nil || len(got.Provenance) != 1 {
		t.Fatalf("task provenance after incident work = %#v err=%v", got, err)
	}

}

func TestNewServerRegistersAndCallsIdentityIncidentReviewTools(t *testing.T) {
	_, human, _, _ := newIdentityIncidentReviewServices(t)
	names := listedWorkLogToolNames(t, NewServer(human))
	for _, name := range []string{
		"worker_identity_audit_list",
		"incident_list", "incident_get", "incident_create", "incident_reply", "incident_update",
		"review_list", "review_get", "review_create", "review_decide",
	} {
		if !names[name] {
			t.Errorf("stable Carbon tool catalog omitted %q", name)
		}
	}

	ctx := context.Background()
	server := NewServer(human)
	clientTransport, serverTransport := mcpSDK.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcpSDK.NewClient(&mcpSDK.Implementation{Name: "identity-incident-review-test", Version: "0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	result, err := clientSession.CallTool(ctx, &mcpSDK.CallToolParams{Name: "incident_create", Arguments: map[string]any{
		"kind": "investigation", "title": "MCP process record", "body": "工具直连验证", "severity": "normal",
	}})
	if err != nil || result.IsError {
		t.Fatalf("incident_create tool = %#v err=%v", result, err)
	}
	var created incident.Incident
	data, _ := json.Marshal(result.StructuredContent)
	if err := json.Unmarshal(data, &created); err != nil || created.ID == "" || created.Kind != incident.KindInvestigation {
		t.Fatalf("decode incident_create = %#v err=%v", created, err)
	}
	result, err = clientSession.CallTool(ctx, &mcpSDK.CallToolParams{Name: "incident_reply", Arguments: map[string]any{"id": created.ID, "body": "MCP 回复已持久化。"}})
	if err != nil || result.IsError {
		t.Fatalf("incident_reply tool = %#v err=%v", result, err)
	}
}
