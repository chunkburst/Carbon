package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"carbon/internal/home"
	"carbon/internal/repo"
	"carbon/internal/store"
)

func TestWorkerRegistryHandlersOnlyChangeHomeMetadataAndDeleteAlias(t *testing.T) {
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	cluster, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Worker registry", Prefix: "WRK"})
	if err != nil {
		t.Fatal(err)
	}
	dataRoot, err := home.ClusterDataRoot(homeRoot, cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(dataRoot)
	doc, err := st.Create(store.Draft{Title: "do not mutate"}, "agent:codex", time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(dataRoot, repo.CarbonDirName, "tasks", doc.Task.ID+".md")
	before, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}

	s := NewWithScope("human:test", ScopeDefaults{Home: homeRoot, HomeByDefault: true})
	handler := s.Handler()
	resetRequest := httptest.NewRequest(http.MethodPost, "/api/home/workers/reset", bytes.NewBufferString(`{"actor":"agent:codex"}`))
	resetResponse := httptest.NewRecorder()
	handler.ServeHTTP(resetResponse, resetRequest)
	if resetResponse.Code != http.StatusOK {
		t.Fatalf("reset = %d %s", resetResponse.Code, resetResponse.Body.String())
	}
	afterReset, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, afterReset) {
		t.Fatalf("reset changed task bytes\nbefore=%q\nafter=%q", before, afterReset)
	}

	h, err := home.Open(homeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.SetWorkerAlias("agent:codex", "codex1"); err != nil {
		t.Fatal(err)
	}
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/home/workers/agent:codex", nil)
	deleteResponse := httptest.NewRecorder()
	handler.ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	var deleted workerRegistryResp
	if err := json.Unmarshal(deleteResponse.Body.Bytes(), &deleted); err != nil {
		t.Fatal(err)
	}
	if deleted.Worker.Actor != "agent:codex" || deleted.Worker.DeletedAt == "" {
		t.Fatalf("delete response = %+v", deleted)
	}
	afterDelete, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, afterDelete) {
		t.Fatalf("delete changed task bytes\nbefore=%q\nafter=%q", before, afterDelete)
	}
	aliases, err := h.ListWorkerAliases()
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 0 {
		t.Fatalf("delete did not clear alias: %#v", aliases)
	}
	registry, err := h.ListWorkerRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if registry["agent:codex"].DeletedAt == "" {
		t.Fatalf("delete did not write tombstone: %#v", registry)
	}
}

func TestDeletedWorkerAutoReappearsOnlyAfterLaterTaskActivity(t *testing.T) {
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	cluster, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Reappearance", Prefix: "RVR"})
	if err != nil {
		t.Fatal(err)
	}
	dataRoot, err := home.ClusterDataRoot(homeRoot, cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(dataRoot)
	doc, err := st.Create(store.Draft{Title: "old task"}, "agent:returning", time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	h, err := home.Open(homeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.DeleteWorker("agent:returning"); err != nil {
		t.Fatal(err)
	}

	s := NewWithScope("human:test", ScopeDefaults{Home: homeRoot, HomeByDefault: true})
	first, err := s.homeWorkerStats(context.Background(), homeRoot, cluster.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Workers) != 0 {
		t.Fatalf("tombstoned worker remained visible before new activity: %+v", first.Workers)
	}

	doc, err = st.Get(doc.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The later note is durable provenance for this actor. Its timestamp is strictly
	// later than the tombstone, which is the only condition that can revive it.
	if err := doc.AppendProvenance("agent:returning", "note", "back online", time.Now().UTC().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(doc); err != nil {
		t.Fatal(err)
	}
	revived, err := s.homeWorkerStats(context.Background(), homeRoot, cluster.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(revived.Workers) != 1 || revived.Workers[0].Actor != "agent:returning" || len(revived.Workers[0].RecentWork) != 1 {
		t.Fatalf("worker did not revive from later activity: %+v", revived.Workers)
	}
	registry, err := h.ListWorkerRegistry()
	if err != nil {
		t.Fatal(err)
	}
	record := registry["agent:returning"]
	if record.DeletedAt != "" || record.ResetAt == "" {
		t.Fatalf("revived registry record = %+v", record)
	}
}

func TestWorkerRegistryHandlersRequireHumanHomeOnlyScope(t *testing.T) {
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	cluster, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Scope", Prefix: "SCP"})
	if err != nil {
		t.Fatal(err)
	}
	projectScoped := NewWithScope("human:test", ScopeDefaults{Home: homeRoot, ClusterID: cluster.ID, HomeByDefault: true})
	request := httptest.NewRequest(http.MethodPost, "/api/home/workers/reset", bytes.NewBufferString(`{"actor":"agent:codex"}`))
	response := httptest.NewRecorder()
	projectScoped.handleResetWorker(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("project scope reset = %d %s, want 400", response.Code, response.Body.String())
	}

	homeOnly := NewWithScope("human:test", ScopeDefaults{Home: homeRoot, HomeByDefault: true})
	legacy := httptest.NewRequest(http.MethodPost, "/api/home/workers/reset?path="+url.QueryEscape(homeRoot), bytes.NewBufferString(`{"actor":"agent:codex"}`))
	legacyResponse := httptest.NewRecorder()
	homeOnly.handleResetWorker(legacyResponse, legacy)
	if legacyResponse.Code != http.StatusBadRequest {
		t.Fatalf("legacy reset = %d %s, want 400", legacyResponse.Code, legacyResponse.Body.String())
	}

	agent := NewWithScope("agent:codex", ScopeDefaults{Home: homeRoot, HomeByDefault: true})
	agentResponse := httptest.NewRecorder()
	agent.handleResetWorker(agentResponse, httptest.NewRequest(http.MethodPost, "/api/home/workers/reset", bytes.NewBufferString(`{"actor":"agent:codex"}`)))
	if agentResponse.Code != http.StatusForbidden {
		t.Fatalf("agent reset = %d %s, want 403", agentResponse.Code, agentResponse.Body.String())
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/home/workers/agent:codex?path="+url.QueryEscape(homeRoot), nil)
	deleteRequest.SetPathValue("actor", "agent:codex")
	deleteResponse := httptest.NewRecorder()
	homeOnly.handleDeleteWorker(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusBadRequest {
		t.Fatalf("legacy delete = %d %s, want 400", deleteResponse.Code, deleteResponse.Body.String())
	}
}

func TestHomeWorkerStatsMergesAggregateAndWorkerCyclesBySampleCount(t *testing.T) {
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	first, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "One", Prefix: "ONE"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Two", Prefix: "TWO"})
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	createCompletedStatsTask(t, homeRoot, first.ID, "agent:weighted", created, created.Add(2*time.Hour))
	createCompletedStatsTask(t, homeRoot, second.ID, "agent:weighted", created, created.Add(4*time.Hour))

	s := NewWithScope("human:test", ScopeDefaults{Home: homeRoot, HomeByDefault: true})
	report, err := s.homeWorkerStats(context.Background(), homeRoot, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if report.Aggregate.TaskCount != 2 || report.Aggregate.Completed != 2 || report.Aggregate.CycleSamples != 2 || report.Aggregate.AverageCycleSeconds != 3*60*60 {
		t.Fatalf("aggregate merge = %+v", report.Aggregate)
	}
	if len(report.Workers) != 1 || report.Workers[0].AverageCycleSeconds != 3*60*60 || report.Workers[0].CycleSamples != 2 {
		t.Fatalf("worker merge = %+v", report.Workers)
	}
}

func createCompletedStatsTask(t *testing.T, homeRoot, clusterID, actor string, created, completed time.Time) {
	t.Helper()
	dataRoot, err := home.ClusterDataRoot(homeRoot, clusterID)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(dataRoot)
	doc, err := st.Create(store.Draft{Title: actor}, actor, created)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.SetStatus("done"); err != nil {
		t.Fatal(err)
	}
	if err := doc.AppendProvenance(actor, "transitioned to done", "", completed); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(doc); err != nil {
		t.Fatal(err)
	}
}
