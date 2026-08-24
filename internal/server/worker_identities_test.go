package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"carbon/internal/home"
	"carbon/internal/mcp"
	"carbon/internal/store"
)

func TestWorkerIdentityHTTPModeAndHumanManagement(t *testing.T) {
	fixture := newWorkLogHTTPFixture(t)
	path := fixture.scopedPath("/api/worker-identities", fixture.cluster1, fixture.project1.ID)

	initial := workLogHTTPCall(t, fixture.handler, http.MethodGet, path, "", "agent:owner", nil)
	if initial.Code != http.StatusOK {
		t.Fatalf("initial identity list = %d %s", initial.Code, initial.Body.String())
	}
	var snapshot mcp.WorkerIdentitySnapshot
	if err := json.Unmarshal(initial.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ModeEnabled || len(snapshot.Records) != 0 {
		t.Fatalf("identity default response = %#v", snapshot)
	}

	selfPath := fixture.scopedPath("/api/worker-identities/agent:owner", fixture.cluster1, fixture.project1.ID)
	if response := workLogHTTPCall(t, fixture.handler, http.MethodPut, selfPath,
		`{"role":"架构师","types":["patch"]}`, "agent:owner", nil); response.Code != http.StatusConflict {
		t.Fatalf("disabled identity claim = %d %s, want 409", response.Code, response.Body.String())
	}

	dataRoot, err := home.ClusterDataRoot(fixture.homeRoot, fixture.cluster1.ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := store.New(dataRoot).Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.IdentityMode = true
	if err := store.New(dataRoot).SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	created := workLogHTTPCall(t, fixture.handler, http.MethodPut, selfPath,
		`{"role":"架构师","types":["foundation","patch"]}`, "agent:owner", nil)
	if created.Code != http.StatusOK {
		t.Fatalf("agent self identity = %d %s", created.Code, created.Body.String())
	}
	var record mcp.WorkerIdentityResult
	if err := json.Unmarshal(created.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if !record.ModeEnabled || record.Record.Actor != "agent:owner" || record.Record.Role != "架构师" || len(record.Record.Types) != 2 || record.Record.ClaimedAt == "" {
		t.Fatalf("agent identity response = %#v", record)
	}

	peerPath := fixture.scopedPath("/api/worker-identities/agent:peer", fixture.cluster1, fixture.project1.ID)
	if response := workLogHTTPCall(t, fixture.handler, http.MethodPut, peerPath,
		`{"role":"前端","types":["extension"]}`, "agent:owner", nil); response.Code != http.StatusForbidden {
		t.Fatalf("agent peer identity mutation = %d %s, want 403", response.Code, response.Body.String())
	}
	managed := workLogHTTPCall(t, fixture.handler, http.MethodPut, peerPath,
		`{"role":"前端","types":["extension"]}`, "human:lead", nil)
	if managed.Code != http.StatusOK {
		t.Fatalf("human identity management = %d %s", managed.Code, managed.Body.String())
	}
	var managedRecord mcp.WorkerIdentityResult
	if err := json.Unmarshal(managed.Body.Bytes(), &managedRecord); err != nil {
		t.Fatal(err)
	}
	if managedRecord.Record.Actor != "agent:peer" || managedRecord.Record.ChangedBy != "human:lead" {
		t.Fatalf("human-managed record = %#v", managedRecord)
	}
	if response := workLogHTTPCall(t, fixture.handler, http.MethodPut, peerPath,
		`{"role":"后端","types":["patch"]}`, "human:lead", nil); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("identity change without reason = %d %s, want 422", response.Code, response.Body.String())
	}
}
