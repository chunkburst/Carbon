package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"carbon/internal/home"
	"carbon/internal/store"
	"carbon/internal/worklog"
)

type workLogFixture struct {
	homeRoot string
	cluster1 home.Cluster
	cluster2 home.Cluster
	project1 home.Project
	project2 home.Project
	project3 home.Project
	store1   *store.Store
	store2   *store.Store
}

func newWorkLogFixture(t *testing.T) workLogFixture {
	t.Helper()
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	cluster1, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "One", Prefix: "ONE"})
	if err != nil {
		t.Fatal(err)
	}
	cluster2, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Two", Prefix: "TWO"})
	if err != nil {
		t.Fatal(err)
	}
	project1, err := home.AddProject(homeRoot, cluster1.ID, home.AddProjectRequest{Name: "One A", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	project2, err := home.AddProject(homeRoot, cluster1.ID, home.AddProjectRequest{Name: "One B", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	project3, err := home.AddProject(homeRoot, cluster2.ID, home.AddProjectRequest{Name: "Two A", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	data1, err := home.ClusterDataRoot(homeRoot, cluster1.ID)
	if err != nil {
		t.Fatal(err)
	}
	data2, err := home.ClusterDataRoot(homeRoot, cluster2.ID)
	if err != nil {
		t.Fatal(err)
	}
	return workLogFixture{
		homeRoot: homeRoot,
		cluster1: cluster1,
		cluster2: cluster2,
		project1: project1,
		project2: project2,
		project3: project3,
		store1:   store.New(data1),
		store2:   store.New(data2),
	}
}

func (f workLogFixture) service(t *testing.T, actor string, cluster home.Cluster, projectID string) *Service {
	t.Helper()
	dataStore := f.store1
	if cluster.ID == f.cluster2.ID {
		dataStore = f.store2
	}
	return NewScopedService(dataStore, actor, Scope{
		Home:         f.homeRoot,
		ClusterID:    cluster.ID,
		ProjectID:    projectID,
		ClusterScope: projectID == "",
	}, func() time.Time { return time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC) })
}

func workLogDraft(visibility worklog.Visibility) worklog.Log {
	return worklog.Log{
		ID:         "log_00000000000000000000000000000000",
		Worker:     "agent:forged",
		ClusterID:  "cluster_forged",
		Visibility: visibility,
		Title:      "Durable work log",
		Body:       "Implemented and verified the change.",
		Tags:       []string{"backend"},
		CreatedBy:  "agent:forged",
		UpdatedBy:  "agent:forged",
		Version:    "forged",
	}
}

func TestWorkLogServiceStampsAndScopesVisibility(t *testing.T) {
	fixture := newWorkLogFixture(t)
	ownerP1 := fixture.service(t, "agent:owner", fixture.cluster1, fixture.project1.ID)
	peerP1 := fixture.service(t, "agent:peer", fixture.cluster1, fixture.project1.ID)
	peerP2 := fixture.service(t, "agent:peer", fixture.cluster1, fixture.project2.ID)
	ownerP2 := fixture.service(t, "agent:owner", fixture.cluster1, fixture.project2.ID)
	clusterPeer := fixture.service(t, "agent:peer", fixture.cluster1, "")
	peerOtherCluster := fixture.service(t, "agent:peer", fixture.cluster2, fixture.project3.ID)
	humanP2 := fixture.service(t, "human:web", fixture.cluster1, fixture.project2.ID)
	humanOtherCluster := fixture.service(t, "human:admin", fixture.cluster2, fixture.project3.ID)

	private, err := ownerP1.CreateWorkLog(context.Background(), workLogDraft(worklog.WorkerPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if private.ID == "log_00000000000000000000000000000000" || private.Worker != "agent:owner" || private.ClusterID != fixture.cluster1.ID || private.ProjectID != fixture.project1.ID || private.CreatedBy != "agent:owner" || private.Version == "" {
		t.Fatalf("service did not stamp immutable scope/actor: %#v", private)
	}
	if _, err := peerP1.GetWorkLog(private.ID); !errors.Is(err, ErrWorkLogNotVisible) {
		t.Fatalf("peer read private = %v, want ErrWorkLogNotVisible", err)
	}
	if _, err := ownerP2.GetWorkLog(private.ID); !errors.Is(err, ErrWorkLogNotVisible) {
		t.Fatalf("same Worker, foreign project private read = %v, want ErrWorkLogNotVisible", err)
	}
	if got, err := humanP2.GetWorkLog(private.ID); err != nil || got.ID != private.ID {
		t.Fatalf("human UI private read = %#v, %v", got, err)
	}
	if got, err := humanOtherCluster.GetWorkLog(private.ID); err != nil || got.ID != private.ID {
		t.Fatalf("human cross-cluster private read = %#v, %v", got, err)
	}
	if err := humanP2.DeleteWorkLog(context.Background(), private.ID, private.ETag()); !errors.Is(err, ErrWorkLogOwnerRequired) {
		t.Fatalf("human UI cannot rewrite another Worker's private audit log = %v, want ErrWorkLogOwnerRequired", err)
	}
	if logs, err := peerP1.ListWorkLogs(worklog.Filter{Limit: 10}); err != nil || len(logs) != 0 {
		t.Fatalf("peer private list = %#v, %v", logs, err)
	}

	project, err := ownerP1.CreateWorkLog(context.Background(), workLogDraft(worklog.ProjectPublic))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peerP1.GetWorkLog(project.ID); err != nil {
		t.Fatalf("same project public read = %v", err)
	}
	if _, err := peerP2.GetWorkLog(project.ID); !errors.Is(err, ErrWorkLogNotVisible) {
		t.Fatalf("foreign project public read = %v, want ErrWorkLogNotVisible", err)
	}
	if _, err := clusterPeer.GetWorkLog(project.ID); err != nil {
		t.Fatalf("explicit cluster scope project public read = %v", err)
	}

	global, err := ownerP1.CreateWorkLog(context.Background(), workLogDraft(worklog.GlobalPublic))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peerP2.GetWorkLog(global.ID); err != nil {
		t.Fatalf("global read in same cluster/home = %v", err)
	}
	if _, err := peerOtherCluster.GetWorkLog(global.ID); err != nil {
		t.Fatalf("same-Home cross-cluster global read = %v", err)
	}
	global.Title = "attempt a cross-project write"
	if _, err := ownerP2.UpdateWorkLog(context.Background(), global, global.ETag()); !errors.Is(err, ErrWorkLogProjectScope) {
		t.Fatalf("foreign project global write = %v, want ErrWorkLogProjectScope", err)
	}
	if _, err := peerP2.UpdateWorkLog(context.Background(), global, global.ETag()); !errors.Is(err, ErrWorkLogOwnerRequired) {
		t.Fatalf("non-owner global write = %v, want ErrWorkLogOwnerRequired", err)
	}
	if _, err := peerOtherCluster.UpdateWorkLog(context.Background(), global, global.ETag()); !errors.Is(err, ErrWorkLogClusterScope) {
		t.Fatalf("remote global write = %v, want ErrWorkLogClusterScope", err)
	}
}

func TestWorkLogServiceScopesProjectsTasksAndVersions(t *testing.T) {
	fixture := newWorkLogFixture(t)
	owner := fixture.service(t, "agent:owner", fixture.cluster1, fixture.project1.ID)
	clusterOwner := fixture.service(t, "agent:owner", fixture.cluster1, "")

	foreign := workLogDraft(worklog.ProjectPublic)
	foreign.ProjectID = fixture.project2.ID
	if _, err := owner.CreateWorkLog(context.Background(), foreign); !errors.Is(err, ErrWorkLogProjectScope) {
		t.Fatalf("create foreign project log = %v, want ErrWorkLogProjectScope", err)
	}
	unknown := workLogDraft(worklog.ProjectPublic)
	unknown.ProjectID = "project_missing"
	if _, err := clusterOwner.CreateWorkLog(context.Background(), unknown); !errors.Is(err, ErrWorkLogProjectScope) {
		t.Fatalf("create non-member project log = %v, want ErrWorkLogProjectScope", err)
	}

	foreignTask, err := fixture.store1.Create(store.Draft{
		Title: "foreign task", ProjectID: fixture.project2.ID, ProjectIDSet: true,
	}, "human:test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	log := workLogDraft(worklog.ProjectPublic)
	log.TaskID = foreignTask.Task.ID
	if _, err := owner.CreateWorkLog(context.Background(), log); !errors.Is(err, ErrWorkLogProjectScope) {
		t.Fatalf("foreign task work log = %v, want ErrWorkLogProjectScope", err)
	}
	clusterLog := workLogDraft(worklog.ProjectPublic)
	clusterLog.TaskID = foreignTask.Task.ID
	createdClusterLog, err := clusterOwner.CreateWorkLog(context.Background(), clusterLog)
	if err != nil {
		t.Fatalf("cluster-scope project task work log = %v", err)
	}
	if createdClusterLog.ProjectID != fixture.project2.ID {
		t.Fatalf("task did not bind its project: %#v", createdClusterLog)
	}

	created, err := owner.CreateWorkLog(context.Background(), workLogDraft(worklog.ProjectPublic))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.PatchWorkLog(context.Background(), created.ID, WorkLogPatch{Title: stringPointer("new evidence")}, ""); !errors.Is(err, ErrWorkLogExpectedVersionRequired) {
		t.Fatalf("missing patch version = %v, want ErrWorkLogExpectedVersionRequired", err)
	}
	if err := owner.DeleteWorkLog(context.Background(), created.ID, ""); !errors.Is(err, ErrWorkLogExpectedVersionRequired) {
		t.Fatalf("missing delete version = %v, want ErrWorkLogExpectedVersionRequired", err)
	}
	stale := created.ETag()
	if _, err := owner.PatchWorkLog(context.Background(), created.ID, WorkLogPatch{ProjectID: stringPointer("")}, stale); !errors.Is(err, ErrWorkLogProjectScope) {
		t.Fatalf("project-bound project clear = %v, want ErrWorkLogProjectScope", err)
	}
	updated, err := owner.PatchWorkLog(context.Background(), created.ID, WorkLogPatch{Title: stringPointer("new evidence")}, stale)
	if err != nil {
		t.Fatal(err)
	}
	if updated.UpdatedBy != "agent:owner" || updated.Title != "new evidence" || updated.Version == created.Version {
		t.Fatalf("patch stamping/version = %#v", updated)
	}
	if _, err := owner.PatchWorkLog(context.Background(), created.ID, WorkLogPatch{Body: stringPointer("stale")}, stale); !errors.Is(err, store.ErrVersionMismatch) {
		t.Fatalf("stale patch = %v, want ErrVersionMismatch", err)
	}
	if err := owner.DeleteWorkLog(context.Background(), updated.ID, stale); !errors.Is(err, store.ErrVersionMismatch) {
		t.Fatalf("stale delete = %v, want ErrVersionMismatch", err)
	}
	if err := owner.DeleteWorkLog(context.Background(), updated.ID, updated.ETag()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkLogServiceListFiltersAcrossHome(t *testing.T) {
	fixture := newWorkLogFixture(t)
	owner1 := fixture.service(t, "agent:owner", fixture.cluster1, fixture.project1.ID)
	owner2 := fixture.service(t, "agent:owner", fixture.cluster2, fixture.project3.ID)
	peer2 := fixture.service(t, "agent:peer", fixture.cluster2, fixture.project3.ID)
	human2 := fixture.service(t, "human:web", fixture.cluster2, fixture.project3.ID)

	private, err := owner1.CreateWorkLog(context.Background(), workLogDraft(worklog.WorkerPrivate))
	if err != nil {
		t.Fatal(err)
	}
	project, err := owner1.CreateWorkLog(context.Background(), workLogDraft(worklog.ProjectPublic))
	if err != nil {
		t.Fatal(err)
	}
	global1, err := owner1.CreateWorkLog(context.Background(), workLogDraft(worklog.GlobalPublic))
	if err != nil {
		t.Fatal(err)
	}
	global2, err := owner2.CreateWorkLog(context.Background(), workLogDraft(worklog.GlobalPublic))
	if err != nil {
		t.Fatal(err)
	}

	peerLogs, err := peer2.ListWorkLogs(worklog.Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(peerLogs) != 2 || !containsWorkLog(peerLogs, global1.ID) || !containsWorkLog(peerLogs, global2.ID) || containsWorkLog(peerLogs, private.ID) || containsWorkLog(peerLogs, project.ID) {
		t.Fatalf("peer cross-Home visibility list = %#v", peerLogs)
	}
	humanLogs, err := human2.ListWorkLogs(worklog.Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(humanLogs) != 4 || !containsWorkLog(humanLogs, private.ID) || !containsWorkLog(humanLogs, project.ID) {
		t.Fatalf("human all-cluster list = %#v", humanLogs)
	}
	if _, err := peer2.ListWorkLogs(worklog.Filter{Limit: 0}); !errors.Is(err, worklog.ErrInvalidFilter) {
		t.Fatalf("zero list limit = %v, want ErrInvalidFilter", err)
	}
	if _, err := peer2.ListWorkLogs(worklog.Filter{Limit: worklog.MaxListLimit + 1}); !errors.Is(err, worklog.ErrInvalidFilter) {
		t.Fatalf("oversize list limit = %v, want ErrInvalidFilter", err)
	}
	if _, err := NewService(fixture.store1, "agent:legacy", nil).ListWorkLogs(worklog.Filter{Limit: 1}); !errors.Is(err, ErrWorkLogScopeRequired) {
		t.Fatalf("legacy Work Log list = %v, want ErrWorkLogScopeRequired", err)
	}
}

func TestIdentityDraftsShareOnlyWithinTheirProjectAndStayAppendOnly(t *testing.T) {
	fixture := newWorkLogFixture(t)
	cfg, err := fixture.store1.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.IdentityMode = true
	if err := fixture.store1.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	owner := fixture.service(t, "agent:owner", fixture.cluster1, fixture.project1.ID)
	legacyOwner := fixture.service(t, "agent:legacy", fixture.cluster1, fixture.project1.ID)
	peerSameProject := fixture.service(t, "agent:peer", fixture.cluster1, fixture.project1.ID)
	bystanderSameProject := fixture.service(t, "agent:bystander", fixture.cluster1, fixture.project1.ID)
	peerOtherProject := fixture.service(t, "agent:peer", fixture.cluster1, fixture.project2.ID)
	systemSameProject := fixture.service(t, "system:auditor", fixture.cluster1, fixture.project1.ID)

	normalPrivate, err := owner.CreateWorkLog(context.Background(), workLogDraft(worklog.WorkerPrivate))
	if err != nil {
		t.Fatal(err)
	}
	// This record represents a historical generic private log whose author happened
	// to choose the future display tag. It must never acquire peer visibility merely
	// because the Identity Mode feature is later enabled.
	legacyTagged, err := worklog.New(fixture.store1, nil).Create(context.Background(), "agent:legacy", worklog.Log{
		Worker: "agent:legacy", Visibility: worklog.WorkerPrivate, ClusterID: fixture.cluster1.ID,
		ProjectID: fixture.project1.ID, Title: "old private tag", Tags: []string{identityDraftTag},
	})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := owner.CreateIdentityDraft(context.Background(), IdentityDraftInput{
		Title: "请评审接口", Body: "先看错误契约。", Recipients: []string{"agent:peer"}, Thread: "identity-flow",
	})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Visibility != worklog.WorkerPrivate || draft.ProjectID != fixture.project1.ID ||
		draft.Coordination == nil || draft.Coordination.Version != worklog.CoordinationVersion ||
		!containsString(draft.Coordination.Recipients, "agent:peer") || draft.Coordination.Thread != "identity-flow" ||
		!containsString(draft.Tags, identityDraftTag) || !containsString(draft.Tags, "to:agent:peer") || !containsString(draft.Tags, "thread:identity-flow") {
		t.Fatalf("identity draft shape = %#v", draft)
	}
	if _, err := peerSameProject.GetWorkLog(normalPrivate.ID); !errors.Is(err, ErrWorkLogNotVisible) {
		t.Fatalf("ordinary private leaked to peer: %v", err)
	}
	if _, err := peerSameProject.GetWorkLog(legacyTagged.ID); !errors.Is(err, ErrWorkLogNotVisible) {
		t.Fatalf("historical tagged private leaked to peer: %v", err)
	}
	if got, err := peerSameProject.GetWorkLog(draft.ID); err != nil || got.ID != draft.ID {
		t.Fatalf("same-project peer draft get = %#v err=%v", got, err)
	}
	if _, err := bystanderSameProject.GetWorkLog(draft.ID); !errors.Is(err, ErrWorkLogNotVisible) {
		t.Fatalf("unlisted same-project bystander read = %v, want private", err)
	}
	if got, err := systemSameProject.GetWorkLog(draft.ID); err != nil || got.ID != draft.ID {
		t.Fatalf("system manager draft get = %#v err=%v", got, err)
	}
	if listed, err := peerSameProject.ListWorkLogs(worklog.Filter{Limit: 20}); err != nil || !containsWorkLog(listed, draft.ID) || containsWorkLog(listed, normalPrivate.ID) || containsWorkLog(listed, legacyTagged.ID) {
		t.Fatalf("same-project peer draft list = %#v err=%v", listed, err)
	}
	if listed, err := bystanderSameProject.ListWorkLogs(worklog.Filter{Limit: 20}); err != nil || containsWorkLog(listed, draft.ID) {
		t.Fatalf("unlisted same-project bystander list = %#v err=%v", listed, err)
	}
	if _, err := peerOtherProject.GetWorkLog(draft.ID); !errors.Is(err, ErrWorkLogNotVisible) {
		t.Fatalf("foreign-project draft get = %v, want private", err)
	}
	if _, err := owner.PatchWorkLog(context.Background(), draft.ID, WorkLogPatch{Body: stringPointer("rewrite")}, draft.ETag()); !errors.Is(err, ErrIdentityDraftImmutable) {
		t.Fatalf("owner draft update = %v, want append-only", err)
	}
	if err := peerSameProject.DeleteWorkLog(context.Background(), draft.ID, draft.ETag()); !errors.Is(err, ErrIdentityDraftImmutable) {
		t.Fatalf("peer draft delete = %v, want append-only", err)
	}
	if _, err := owner.CreateWorkLog(context.Background(), worklog.Log{Visibility: worklog.WorkerPrivate, Title: "forged draft", Tags: []string{identityDraftTag}}); !errors.Is(err, ErrIdentityDraftReservedTag) {
		t.Fatalf("ordinary create accepted reserved tag: %v", err)
	}
	if _, err := owner.CreateWorkLog(context.Background(), worklog.Log{Visibility: worklog.WorkerPrivate, Title: "forged envelope", Coordination: &worklog.Coordination{Version: worklog.CoordinationVersion}}); !errors.Is(err, ErrIdentityDraftServerOwned) {
		t.Fatalf("ordinary create accepted coordination envelope: %v", err)
	}
	forgedUpdate := normalPrivate
	forgedUpdate.Coordination = &worklog.Coordination{Version: worklog.CoordinationVersion}
	if _, err := owner.UpdateWorkLog(context.Background(), forgedUpdate, normalPrivate.ETag()); !errors.Is(err, ErrIdentityDraftServerOwned) {
		t.Fatalf("ordinary update accepted coordination envelope: %v", err)
	}
	if _, err := legacyOwner.PatchWorkLog(context.Background(), legacyTagged.ID, WorkLogPatch{Title: stringPointer("old tag remains editable")}, legacyTagged.ETag()); err != nil {
		t.Fatalf("historical tag lost normal edit behavior: %v", err)
	}
	broadcast, err := owner.CreateIdentityDraft(context.Background(), IdentityDraftInput{Title: "项目广播"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := bystanderSameProject.GetWorkLog(broadcast.ID); err != nil || got.ID != broadcast.ID {
		t.Fatalf("empty-recipient broadcast = %#v err=%v", got, err)
	}

	cfg.IdentityMode = false
	if err := fixture.store1.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := peerSameProject.GetWorkLog(draft.ID); !errors.Is(err, ErrWorkLogNotVisible) {
		t.Fatalf("disabled identity mode still shared draft: %v", err)
	}
	if _, err := owner.CreateIdentityDraft(context.Background(), IdentityDraftInput{Title: "disabled"}); !errors.Is(err, ErrIdentityModeDisabled) {
		t.Fatalf("draft send while disabled = %v", err)
	}
}

func TestStandaloneWorkLogsStayPrivateAndAreDiscoveredAsGlobalFromCluster(t *testing.T) {
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	cluster, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Shared", Prefix: "SHR"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := home.AddStandaloneProject(homeRoot, home.AddProjectRequest{Name: "First", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	second, err := home.AddStandaloneProject(homeRoot, home.AddProjectRequest{Name: "Second", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	firstRoot, err := home.ProjectDataRoot(homeRoot, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondRoot, err := home.ProjectDataRoot(homeRoot, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	clusterRoot, err := home.ClusterDataRoot(homeRoot, cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC) }
	firstOwner := NewScopedService(store.New(firstRoot), "agent:owner", Scope{
		Home: homeRoot, ProjectID: first.ID, Standalone: true,
	}, now)
	secondPeer := NewScopedService(store.New(secondRoot), "agent:peer", Scope{
		Home: homeRoot, ProjectID: second.ID, Standalone: true,
	}, now)
	clusterPeer := NewScopedService(store.New(clusterRoot), "agent:peer", Scope{
		Home: homeRoot, ClusterID: cluster.ID, ClusterScope: true,
	}, now)

	created, err := firstOwner.CreateWorkLog(context.Background(), workLogDraft(worklog.GlobalPublic))
	if err != nil {
		t.Fatal(err)
	}
	if !created.Standalone || created.ClusterID != "" || created.ProjectID != first.ID {
		t.Fatalf("standalone log scope = %#v", created)
	}
	if _, err := secondPeer.GetWorkLog(created.ID); !errors.Is(err, worklog.ErrNotFound) {
		t.Fatalf("sibling standalone get = %v, want ErrNotFound", err)
	}
	if logs, err := secondPeer.ListWorkLogs(worklog.Filter{Limit: 10}); err != nil || len(logs) != 0 {
		t.Fatalf("sibling standalone list = %#v, %v", logs, err)
	}
	foreign := workLogDraft(worklog.ProjectPublic)
	foreign.ProjectID = second.ID
	if _, err := firstOwner.CreateWorkLog(context.Background(), foreign); !errors.Is(err, ErrWorkLogProjectScope) {
		t.Fatalf("standalone foreign create = %v, want ErrWorkLogProjectScope", err)
	}
	if got, err := clusterPeer.GetWorkLog(created.ID); err != nil || got.ID != created.ID {
		t.Fatalf("cluster global standalone discovery = %#v, %v", got, err)
	}
}

func TestRegisterWorkLogToolsExposesCRUDAndRequiresVersions(t *testing.T) {
	fixture := newWorkLogFixture(t)
	svc := fixture.service(t, "agent:owner", fixture.cluster1, fixture.project1.ID)
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "worklog-test", Version: "0"}, nil)
	registerWorkLogTools(srv, svc)
	ctx := context.Background()
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "worklog-client", Version: "0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(listed.Tools))
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	for _, name := range []string{"worklog_create", "worklog_get", "worklog_list", "worklog_update", "worklog_delete", "worklog_draft_send"} {
		if !names[name] {
			t.Errorf("worklog tool %q is not registered: %v", name, names)
		}
	}

	result, err := clientSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: "worklog_create", Arguments: map[string]any{
		"visibility": "project_public", "title": "Tool-created log",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("worklog_create = %+v", result)
	}
	var created workLogOut
	data, _ := json.Marshal(result.StructuredContent)
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatal(err)
	}
	if created.WorkLog.Worker != "agent:owner" || created.WorkLog.ClusterID != fixture.cluster1.ID || created.WorkLog.ProjectID != fixture.project1.ID {
		t.Fatalf("tool create did not stamp service scope: %#v", created.WorkLog)
	}
	result, err = clientSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: "worklog_update", Arguments: map[string]any{
		"id": created.WorkLog.ID, "title": "missing version",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("worklog_update accepted omitted expected_version: %+v", result)
	}
}

func TestNewServerRegistersWorkLogToolsOnlyForStableCarbonV2(t *testing.T) {
	fixture := newWorkLogFixture(t)
	carbonNames := listedWorkLogToolNames(t, NewServer(fixture.service(t, "agent:owner", fixture.cluster1, fixture.project1.ID)))
	for _, name := range []string{"worklog_create", "worklog_get", "worklog_list", "worklog_update", "worklog_delete", "worklog_draft_send"} {
		if !carbonNames[name] {
			t.Errorf("Carbon stable v2 tool catalog omitted %q: %v", name, carbonNames)
		}
	}
	legacyNames := listedWorkLogToolNames(t, NewServer(NewService(store.New(t.TempDir()), "agent:legacy", nil)))
	for _, name := range []string{"worklog_create", "worklog_get", "worklog_list", "worklog_update", "worklog_delete", "worklog_draft_send"} {
		if legacyNames[name] {
			t.Errorf("legacy tool catalog exposed %q: %v", name, legacyNames)
		}
	}
}

func listedWorkLogToolNames(t *testing.T, srv *mcpsdk.Server) map[string]bool {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "worklog-catalog-test", Version: "0"}, nil)
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

func containsWorkLog(items []worklog.Log, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func stringPointer(value string) *string { return &value }
