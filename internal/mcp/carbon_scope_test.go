package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"carbon/internal/repo"
	"carbon/internal/store"
)

func TestCarbonGenericAssigneeIsRejectedAndBulkVersionsAreRequired(t *testing.T) {
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "CAR"); err != nil {
		t.Fatal(err)
	}
	svc := NewScopedService(store.New(root), "human:test", Scope{
		Home: "home_test", ClusterID: "cluster_test", ProjectID: "project_test",
	}, nil)
	doc, err := svc.CreateContext(context.Background(), store.Draft{Title: "owned", Type: "patch", Importance: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	assignee := "human:other"
	if _, err := svc.UpdateWithVersion(doc.Task.ID, UpdateFields{Assignee: &assignee}, doc.Version()); !errors.Is(err, ErrAssigneeLeaseRequired) {
		t.Fatalf("generic Carbon assignee update = %v, want ErrAssigneeLeaseRequired", err)
	}
	if _, err := svc.BulkUpdate(context.Background(), store.BulkUpdate{
		IDs: []string{doc.Task.ID}, ExpectedVersions: map[string]string{doc.Task.ID: doc.Version()}, Assignee: &assignee,
	}); !errors.Is(err, ErrAssigneeLeaseRequired) {
		t.Fatalf("unassigned Carbon bulk assignee update = %v, want ErrAssigneeLeaseRequired", err)
	}
	if reloaded, err := svc.store.Get(doc.Task.ID); err != nil || reloaded.Task.Assignee != "" {
		t.Fatalf("unassigned Carbon bulk assignee mutated task = %#v, %v", reloaded, err)
	}
	occupied, err := svc.store.Get(doc.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	occupied.SetAssignee("human:incumbent")
	if err := svc.store.Save(occupied); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BulkUpdate(context.Background(), store.BulkUpdate{
		IDs: []string{occupied.Task.ID}, ExpectedVersions: map[string]string{occupied.Task.ID: occupied.Version()}, Assignee: &assignee, Force: true, Reason: "bypass",
	}); !errors.Is(err, ErrAssigneeLeaseRequired) {
		t.Fatalf("occupied Carbon bulk assignee update = %v, want ErrAssigneeLeaseRequired", err)
	}
	if reloaded, err := svc.store.Get(occupied.Task.ID); err != nil || reloaded.Task.Assignee != "human:incumbent" {
		t.Fatalf("occupied Carbon bulk assignee mutated task = %#v, %v", reloaded, err)
	}
	doc, err = svc.store.Get(doc.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	labels := []string{"batched"}
	if _, err := svc.BulkUpdate(context.Background(), store.BulkUpdate{IDs: []string{doc.Task.ID}, Labels: &labels}); !errors.Is(err, ErrExpectedVersionsRequired) {
		t.Fatalf("Carbon bulk without versions = %v, want ErrExpectedVersionsRequired", err)
	}
	changed, err := svc.BulkUpdate(context.Background(), store.BulkUpdate{
		IDs: []string{doc.Task.ID}, Labels: &labels, ExpectedVersions: map[string]string{doc.Task.ID: doc.Version()},
	})
	if err != nil || len(changed) != 1 || len(changed[0].Task.Labels) != 1 {
		t.Fatalf("Carbon bulk with version = %#v, %v", changed, err)
	}
}

func TestCarbonTaskCreationRequiresProjectOrExplicitClusterScope(t *testing.T) {
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "CAR"); err != nil {
		t.Fatal(err)
	}
	clusterImplicit := NewScopedService(store.New(root), "human:test", Scope{
		Home: "home", ClusterID: "cluster",
	}, nil)
	if _, err := clusterImplicit.CreateContext(context.Background(), store.Draft{
		Title: "implicit shared", ProjectIDSet: true, Type: "patch", Importance: "normal",
	}); !errors.Is(err, ErrProjectBindingRequired) {
		t.Fatalf("implicit cluster-wide create = %v, want ErrProjectBindingRequired", err)
	}
	if _, err := clusterImplicit.CreateContext(context.Background(), store.Draft{
		Title: "omitted project", Type: "patch", Importance: "normal",
	}); !errors.Is(err, ErrProjectBindingRequired) {
		t.Fatalf("cluster create without explicit empty project = %v, want ErrProjectBindingRequired", err)
	}
	explicitCluster := NewScopedService(store.New(root), "human:test", Scope{
		Home: "home", ClusterID: "cluster", ClusterScope: true,
	}, nil)
	shared, err := explicitCluster.CreateContext(context.Background(), store.Draft{
		Title: "explicit shared", ProjectIDSet: true, Type: "patch", Importance: "normal",
	})
	if err != nil || shared.Task.ProjectID != "" {
		t.Fatalf("explicit cluster-wide create = %#v, %v", shared, err)
	}
	bound := NewScopedService(store.New(root), "human:test", Scope{
		Home: "home", ClusterID: "cluster", ProjectID: "project-one",
	}, nil)
	owned, err := bound.CreateContext(context.Background(), store.Draft{Title: "bound", Type: "patch", Importance: "normal"})
	if err != nil || owned.Task.ProjectID != "project-one" {
		t.Fatalf("bound default project create = %#v, %v", owned, err)
	}
	if _, err := bound.CreateContext(context.Background(), store.Draft{
		Title: "bound cannot silently share", ProjectIDSet: true, Type: "patch", Importance: "normal",
	}); !errors.Is(err, ErrProjectBindingRequired) {
		t.Fatalf("project-bound cluster-wide create = %v, want ErrProjectBindingRequired", err)
	}
	legacy := NewService(store.New(root), "human:test", nil)
	if _, err := legacy.CreateContext(context.Background(), store.Draft{Title: "legacy omits project"}); err != nil {
		t.Fatalf("legacy --repo compatible create = %v", err)
	}
}

func TestStandaloneProjectScopeRejectsClusterExpansionAndSharedTasks(t *testing.T) {
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "SOLO"); err != nil {
		t.Fatal(err)
	}
	svc := NewScopedService(store.New(root), "human:test", Scope{
		Home: "home", ProjectID: "project-standalone", Standalone: true,
	}, nil)
	created, err := svc.CreateContext(context.Background(), store.Draft{
		Title: "private task", Type: "patch", Importance: "normal",
	})
	if err != nil || created.Task.ProjectID != "project-standalone" {
		t.Fatalf("standalone create = %#v, %v", created, err)
	}
	if _, err := svc.ListScoped("", "", nil, "", true); !errors.Is(err, ErrStandaloneClusterScope) {
		t.Fatalf("standalone list include_cluster = %v, want ErrStandaloneClusterScope", err)
	}
	if _, err := svc.GetScoped(created.Task.ID, true); !errors.Is(err, ErrStandaloneClusterScope) {
		t.Fatalf("standalone get include_cluster = %v, want ErrStandaloneClusterScope", err)
	}
	if _, err := svc.CreateContext(context.Background(), store.Draft{
		Title: "shared task", ProjectIDSet: true, Type: "patch", Importance: "normal",
	}); !errors.Is(err, ErrProjectBindingRequired) {
		t.Fatalf("standalone shared create = %v, want ErrProjectBindingRequired", err)
	}
	if _, err := svc.CreateContext(context.Background(), store.Draft{
		Title: "foreign task", ProjectID: "project-other", ProjectIDSet: true, Type: "patch", Importance: "normal",
	}); !errors.Is(err, ErrProjectWriteScope) {
		t.Fatalf("standalone foreign create = %v, want ErrProjectWriteScope", err)
	}
	if _, err := svc.BulkMoveWithAuthorization(context.Background(), store.BulkMove{
		IDs: []string{created.Task.ID}, ExpectedVersions: map[string]string{created.Task.ID: created.Version()}, ClusterWide: true,
	}, true); !errors.Is(err, ErrProjectWriteScope) {
		t.Fatalf("standalone project-to-shared move = %v, want ErrProjectWriteScope", err)
	}
}

func TestLegacyBulkUpdateRetainsAssigneeBehavior(t *testing.T) {
	svc := service(t, "human:test")
	doc, err := svc.Create(store.Draft{Title: "legacy assignment"})
	if err != nil {
		t.Fatal(err)
	}
	assignee := "human:other"
	changed, err := svc.BulkUpdate(context.Background(), store.BulkUpdate{IDs: []string{doc.Task.ID}, Assignee: &assignee})
	if err != nil || len(changed) != 1 || changed[0].Task.Assignee != assignee {
		t.Fatalf("legacy bulk assignee update = %#v, %v", changed, err)
	}
}

func TestCarbonProjectBoundWritesRejectForeignTasksAndRequireExplicitMove(t *testing.T) {
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "CAR"); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	resolver := func(projectID string) (string, error) {
		if projectID != "project-one" && projectID != "project-two" {
			return "", errors.New("unknown project")
		}
		return source, nil
	}
	svc := NewScopedServiceWithClientAndResolver(store.New(root), "human:one", "", Scope{
		Home: "home", ClusterID: "cluster", ProjectID: "project-one",
	}, resolver, nil)
	foreign, err := store.New(root).Create(store.Draft{Title: "foreign", ProjectID: "project-two", ProjectIDSet: true}, "human:two", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Note(foreign.Task.ID, "should fail"); !errors.Is(err, ErrProjectWriteScope) {
		t.Fatalf("foreign note = %v, want ErrProjectWriteScope", err)
	}
	shared, err := store.New(root).Create(store.Draft{Title: "shared", ProjectIDSet: true}, "human:one", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	project := "project-two"
	if _, err := svc.UpdateWithVersion(shared.Task.ID, UpdateFields{ProjectID: &project}, shared.Version()); !errors.Is(err, ErrProjectMoveRequired) {
		t.Fatalf("generic project update = %v, want ErrProjectMoveRequired", err)
	}
	move := store.BulkMove{IDs: []string{shared.Task.ID}, ExpectedVersions: map[string]string{shared.Task.ID: shared.Version()}, ProjectID: "project-two", Reason: "handoff"}
	if _, err := svc.BulkMoveWithAuthorization(context.Background(), move, false); !errors.Is(err, ErrProjectWriteScope) {
		t.Fatalf("unforced cross-project move = %v, want ErrProjectWriteScope", err)
	}
	if _, err := svc.BulkMoveWithAuthorization(context.Background(), move, true); err != nil {
		t.Fatalf("forced explicit move = %v", err)
	}
}

func TestCarbonListTrashDefaultsToConcreteBoundProject(t *testing.T) {
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "CAR"); err != nil {
		t.Fatal(err)
	}
	st := store.New(root)
	svc := NewScopedService(st, "human:one", Scope{Home: "home", ClusterID: "cluster", ProjectID: "project-one"}, nil)
	owned, err := st.Create(store.Draft{Title: "owned", ProjectID: "project-one", ProjectIDSet: true}, "human:one", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := st.Create(store.Draft{Title: "foreign", ProjectID: "project-two", ProjectIDSet: true}, "human:two", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	shared, err := st.Create(store.Draft{Title: "shared", ProjectIDSet: true}, "human:one", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.TrashTasks(context.Background(), "human:one", []string{owned.Task.ID, foreign.Task.ID, shared.Task.ID}, "test", nil, time.Now()); err != nil {
		t.Fatal(err)
	}

	entries, err := svc.ListTrash(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != owned.Task.ID {
		t.Fatalf("default project trash = %#v, want only %s", entries, owned.Task.ID)
	}
	entries, err = svc.ListTrash(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("include_cluster trash count = %d, want 3", len(entries))
	}
}

func TestCarbonProjectMovesRequireForceAndReasonForEveryActualChange(t *testing.T) {
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "CAR"); err != nil {
		t.Fatal(err)
	}
	st := store.New(root)
	resolver := func(projectID string) (string, error) {
		if projectID != "project-one" && projectID != "project-two" {
			return "", errors.New("unknown project")
		}
		return root, nil
	}
	// A cluster-bound scope still needs an explicit audit acknowledgement for any
	// source->target project change.
	svc := NewScopedServiceWithClientAndResolver(st, "human:one", "", Scope{Home: "home", ClusterID: "cluster"}, resolver, nil)
	fromOne, err := st.Create(store.Draft{Title: "from one", ProjectID: "project-one", ProjectIDSet: true}, "human:one", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fromTwo, err := st.Create(store.Draft{Title: "from two", ProjectID: "project-two", ProjectIDSet: true}, "human:one", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	target := "project-two"
	versions := map[string]string{fromOne.Task.ID: fromOne.Version(), fromTwo.Task.ID: fromTwo.Version()}
	if _, err := svc.BulkUpdate(context.Background(), store.BulkUpdate{IDs: []string{fromOne.Task.ID, fromTwo.Task.ID}, ExpectedVersions: versions, ProjectID: &target}); !errors.Is(err, ErrProjectWriteScope) {
		t.Fatalf("mixed project move without force/reason = %v, want ErrProjectWriteScope", err)
	}
	if _, err := svc.BulkUpdate(context.Background(), store.BulkUpdate{IDs: []string{fromOne.Task.ID}, ExpectedVersions: map[string]string{fromOne.Task.ID: fromOne.Version()}, ProjectID: &target, Force: true}); !errors.Is(err, ErrProjectWriteScope) {
		t.Fatalf("project move without reason = %v, want ErrProjectWriteScope", err)
	}
	changed, err := svc.BulkUpdate(context.Background(), store.BulkUpdate{IDs: []string{fromOne.Task.ID}, ExpectedVersions: map[string]string{fromOne.Task.ID: fromOne.Version()}, ProjectID: &target, Force: true, Reason: "handoff"})
	if err != nil || len(changed) != 1 || changed[0].Task.ProjectID != target {
		t.Fatalf("forced project update = %#v, %v", changed, err)
	}
	// An unchanged target is a no-op and therefore needs no force acknowledgement.
	if _, err := svc.BulkUpdate(context.Background(), store.BulkUpdate{IDs: []string{fromTwo.Task.ID}, ExpectedVersions: map[string]string{fromTwo.Task.ID: fromTwo.Version()}, ProjectID: &target}); err != nil {
		t.Fatalf("same-project bulk update = %v", err)
	}

	move, err := st.Create(store.Draft{Title: "to shared", ProjectID: "project-two", ProjectIDSet: true}, "human:one", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	baseMove := store.BulkMove{IDs: []string{move.Task.ID}, ExpectedVersions: map[string]string{move.Task.ID: move.Version()}, ClusterWide: true}
	if _, err := svc.BulkMoveWithAuthorization(context.Background(), baseMove, false); !errors.Is(err, ErrProjectWriteScope) {
		t.Fatalf("project-to-shared move without force/reason = %v, want ErrProjectWriteScope", err)
	}
	baseMove.Reason = "shared triage"
	changed, err = svc.BulkMoveWithAuthorization(context.Background(), baseMove, true)
	if err != nil || len(changed) != 1 || changed[0].Task.ProjectID != "" {
		t.Fatalf("forced project-to-shared move = %#v, %v", changed, err)
	}
	if _, err := svc.BulkMoveWithAuthorization(context.Background(), store.BulkMove{IDs: []string{move.Task.ID}, ExpectedVersions: map[string]string{move.Task.ID: changed[0].Version()}}, true); !errors.Is(err, ErrProjectWriteScope) {
		t.Fatalf("empty target without cluster_wide = %v, want ErrProjectWriteScope", err)
	}
}

func TestCarbonSessionUsesProjectResolverRatherThanSharedDataRoot(t *testing.T) {
	dataRoot := t.TempDir()
	if err := repo.InitDataRoot(dataRoot, "CAR"); err != nil {
		t.Fatal(err)
	}
	sourceRoot := t.TempDir()
	resolverCalls := 0
	svc := NewScopedServiceWithClientAndResolver(store.New(dataRoot), "agent:codex", "codex", Scope{
		Home: "home_test", ClusterID: "cluster_test", ProjectID: "project_test",
	}, func(projectID string) (string, error) {
		resolverCalls++
		if projectID != "project_test" {
			return "", errors.New("unexpected project")
		}
		return sourceRoot, nil
	}, nil)
	doc, err := svc.CreateContext(context.Background(), store.Draft{Title: "session", Type: "patch", Importance: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	// Supplying the physical Carbon data root as worktree must fail. If BeginSession
	// ever fell back to Store.Root this would incorrectly succeed.
	if _, err := svc.BeginSession(context.Background(), BeginSessionInput{TaskID: doc.Task.ID, ExpectedActor: "agent:codex", Client: "codex", Worktree: dataRoot, IdempotencyKey: "outside"}); !errors.Is(err, ErrExecutionProjectRequired) {
		t.Fatalf("shared data root worktree = %v, want ErrExecutionProjectRequired", err)
	}
	if resolverCalls == 0 {
		t.Fatal("Carbon session did not resolve its project source")
	}
	if sessions, err := svc.store.ListSessions(); err != nil || len(sessions) != 0 {
		t.Fatalf("rejected Carbon worktree created session: %d, %v", len(sessions), err)
	}
	if _, err := svc.BeginSession(context.Background(), BeginSessionInput{TaskID: doc.Task.ID, ExpectedActor: "agent:codex", Client: "codex", Worktree: sourceRoot, IdempotencyKey: "inside"}); err != nil {
		t.Fatalf("project source worktree rejected: %v", err)
	}

	offlineRoot := t.TempDir()
	if err := repo.InitDataRoot(offlineRoot, "OFF"); err != nil {
		t.Fatal(err)
	}
	offlineSvc := NewScopedServiceWithClientAndResolver(store.New(offlineRoot), "agent:codex", "codex", Scope{
		Home: "home_test", ClusterID: "cluster_test", ProjectID: "project_test",
	}, func(string) (string, error) { return "", errors.New("source identity mismatch") }, nil)
	offlineTask, err := offlineSvc.CreateContext(context.Background(), store.Draft{Title: "offline", Type: "patch", Importance: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := offlineSvc.BeginSession(context.Background(), BeginSessionInput{TaskID: offlineTask.Task.ID, ExpectedActor: "agent:codex", Client: "codex", IdempotencyKey: "offline"}); !errors.Is(err, ErrExecutionProjectRequired) {
		t.Fatalf("offline source session = %v, want ErrExecutionProjectRequired", err)
	}
}
