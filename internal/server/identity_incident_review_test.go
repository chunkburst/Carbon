package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"carbon/internal/home"
	"carbon/internal/incident"
	"carbon/internal/mcp"
	"carbon/internal/review"
	"carbon/internal/store"
	"carbon/internal/task"
)

func TestIdentityIncidentReviewHTTPVerticalSliceAndProjectIsolation(t *testing.T) {
	fixture := newWorkLogHTTPFixture(t)
	identityPath := fixture.scopedPath("/api/worker-identities/agent:reviewer", fixture.cluster1, fixture.project1.ID)
	managed := workLogHTTPCall(t, fixture.handler, http.MethodPut, identityPath,
		`{"roles":["reviewer","backend"],"types":["patch"]}`, "human:lead", nil)
	if managed.Code != http.StatusOK {
		t.Fatalf("human identity management while disabled = %d %s", managed.Code, managed.Body.String())
	}
	var identityResult mcp.WorkerIdentityResult
	if err := json.Unmarshal(managed.Body.Bytes(), &identityResult); err != nil || identityResult.ModeEnabled || len(identityResult.Record.Roles) != 2 {
		t.Fatalf("identity HTTP response = %#v err=%v", identityResult, err)
	}
	if bad := workLogHTTPCall(t, fixture.handler, http.MethodPut, identityPath,
		`{"roles":["reviewer"],"role":"reviewer","types":["patch"]}`, "human:lead", nil); bad.Code != http.StatusBadRequest {
		t.Fatalf("roles/role dual input = %d %s, want 400", bad.Code, bad.Body.String())
	}
	if bad := workLogHTTPCall(t, fixture.handler, http.MethodPut, identityPath,
		`{"roles":["reviewer"],"types":["patch"],"unknown":true}`, "human:lead", nil); bad.Code != http.StatusBadRequest {
		t.Fatalf("identity strict unknown field = %d %s, want 400", bad.Code, bad.Body.String())
	}
	auditPath := fixture.scopedPath("/api/worker-identities/audit", fixture.cluster1, fixture.project1.ID)
	auditResponse := workLogHTTPCall(t, fixture.handler, http.MethodGet, auditPath, "", "human:lead", nil)
	if auditResponse.Code != http.StatusOK {
		t.Fatalf("identity audit HTTP = %d %s", auditResponse.Code, auditResponse.Body.String())
	}
	var audits mcp.WorkerIdentityAuditSnapshot
	if err := json.Unmarshal(auditResponse.Body.Bytes(), &audits); err != nil || len(audits.Audits) != 1 || audits.Audits[0].RelatedIncidentID == "" {
		t.Fatalf("identity audit response = %#v err=%v", audits, err)
	}
	autoPath := fixture.scopedPath("/api/incidents/"+audits.Audits[0].RelatedIncidentID, fixture.cluster1, fixture.project1.ID)
	if response := workLogHTTPCall(t, fixture.handler, http.MethodPatch, autoPath, `{"status":"investigating"}`, "agent:reviewer", nil); response.Code != http.StatusOK {
		t.Fatalf("automatic identity incident investigating = %d %s", response.Code, response.Body.String())
	} else {
		var updated incident.Incident
		if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil || updated.Status != incident.StatusInvestigating {
			t.Fatalf("automatic identity lifecycle = %#v err=%v", updated, err)
		}
	}

	incidentsPath := fixture.scopedPath("/api/incidents", fixture.cluster1, fixture.project1.ID)
	created := workLogHTTPCall(t, fixture.handler, http.MethodPost, incidentsPath,
		`{"kind":"investigation","title":"429 排查","body":"先把过程留下","severity":"high"}`, "agent:reviewer", nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("incident create = %d %s", created.Code, created.Body.String())
	}
	var manual incident.Incident
	if err := json.Unmarshal(created.Body.Bytes(), &manual); err != nil || manual.Origin != incident.OriginManual || manual.Kind != incident.KindInvestigation {
		t.Fatalf("manual incident response = %#v err=%v", manual, err)
	}
	if bad := workLogHTTPCall(t, fixture.handler, http.MethodPost, incidentsPath,
		`{"title":"strict","unknownField":true}`, "agent:reviewer", nil); bad.Code != http.StatusBadRequest {
		t.Fatalf("strict incident decoder = %d %s, want 400", bad.Code, bad.Body.String())
	}
	replyPath := fixture.scopedPath("/api/incidents/"+manual.ID+"/reply", fixture.cluster1, fixture.project1.ID)
	if replied := workLogHTTPCall(t, fixture.handler, http.MethodPost, replyPath, `{"body":"保留观测，稍后继续。"}`, "agent:reviewer", nil); replied.Code != http.StatusCreated {
		t.Fatalf("incident reply = %d %s", replied.Code, replied.Body.String())
	}
	otherPath := fixture.scopedPath("/api/incidents", fixture.cluster1, fixture.project2.ID)
	otherList := workLogHTTPCall(t, fixture.handler, http.MethodGet, otherPath, "", "human:lead", nil)
	if otherList.Code != http.StatusOK {
		t.Fatalf("other project incident list = %d %s", otherList.Code, otherList.Body.String())
	}
	var other struct {
		Incidents []incident.Incident `json:"incidents"`
	}
	if err := json.Unmarshal(otherList.Body.Bytes(), &other); err != nil || len(other.Incidents) != 0 {
		t.Fatalf("incidents crossed projects = %#v err=%v", other, err)
	}

	dataRoot, err := home.ClusterDataRoot(fixture.homeRoot, fixture.cluster1.ID)
	if err != nil {
		t.Fatal(err)
	}
	projectService := mcp.NewScopedService(store.New(dataRoot), "human:lead", mcp.Scope{Home: fixture.homeRoot, ClusterID: fixture.cluster1.ID, ProjectID: fixture.project1.ID}, nil)
	planTask, err := projectService.CreateContext(context.Background(), store.Draft{Title: "plan review task", Type: "patch", Importance: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	manualTask, err := projectService.CreateContext(context.Background(), store.Draft{Title: "manual review task", Type: "patch", Importance: "normal", Checks: []task.Check{{Desc: "human sign-off", Type: "manual"}}})
	if err != nil {
		t.Fatal(err)
	}
	reviewsPath := fixture.scopedPath("/api/reviews", fixture.cluster1, fixture.project1.ID)
	if bad := workLogHTTPCall(t, fixture.handler, http.MethodPost, reviewsPath,
		`{"targetKind":"plan","targetId":"not-the-task","taskId":"`+planTask.Task.ID+`","reviewerActor":"human:lead"}`, "human:lead", nil); bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("review bad plan target = %d %s, want 422", bad.Code, bad.Body.String())
	}
	createdReview := workLogHTTPCall(t, fixture.handler, http.MethodPost, reviewsPath,
		`{"targetKind":"plan","targetId":"`+planTask.Task.ID+`","taskId":"`+planTask.Task.ID+`","reviewerActor":"human:lead"}`, "human:lead", nil)
	if createdReview.Code != http.StatusCreated {
		t.Fatalf("review create = %d %s", createdReview.Code, createdReview.Body.String())
	}
	var target review.Target
	if err := json.Unmarshal(createdReview.Body.Bytes(), &target); err != nil || target.Status != review.StatusPending {
		t.Fatalf("review target response = %#v err=%v", target, err)
	}
	decisionPath := fixture.scopedPath("/api/reviews/"+target.ID+"/decide", fixture.cluster1, fixture.project1.ID)
	decided := workLogHTTPCall(t, fixture.handler, http.MethodPost, decisionPath,
		`{"status":"approved","decision":"计划边界清楚。"}`, "human:lead", nil)
	if decided.Code != http.StatusOK {
		t.Fatalf("review decide = %d %s", decided.Code, decided.Body.String())
	}
	if manual := workLogHTTPCall(t, fixture.handler, http.MethodPost, reviewsPath,
		`{"targetKind":"manual_check","targetId":"`+manualTask.Task.ID+`#check:0","taskId":"`+manualTask.Task.ID+`","checkId":"0","reviewerActor":"human:lead"}`, "human:lead", nil); manual.Code != http.StatusCreated {
		t.Fatalf("review manual check = %d %s", manual.Code, manual.Body.String())
	}
	if bad := workLogHTTPCall(t, fixture.handler, http.MethodPost, reviewsPath,
		`{"targetKind":"manual_check","targetId":"`+manualTask.Task.ID+`#check:1","taskId":"`+manualTask.Task.ID+`","checkId":"1","reviewerActor":"human:lead"}`, "human:lead", nil); bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("review bad manual check = %d %s, want 422", bad.Code, bad.Body.String())
	}

	configPath := fixture.scopedPath("/api/config", fixture.cluster1, fixture.project1.ID)
	configResponse := workLogHTTPCall(t, fixture.handler, http.MethodPost, configPath, `{"noTraceMode":true}`, "human:lead", nil)
	if configResponse.Code != http.StatusOK {
		t.Fatalf("no trace config HTTP = %d %s", configResponse.Code, configResponse.Body.String())
	}
	var cfg configResp
	if err := json.Unmarshal(configResponse.Body.Bytes(), &cfg); err != nil || !cfg.NoTraceMode {
		t.Fatalf("no trace config response = %#v err=%v", cfg, err)
	}
}

func TestProjectIdentityPolicyAndJournalAreIsolatedWithinOneCluster(t *testing.T) {
	fixture := newWorkLogHTTPFixture(t)
	projectA := fixture.project1.ID
	projectB := fixture.project2.ID
	identityA := fixture.scopedPath("/api/worker-identities/agent:shared", fixture.cluster1, projectA)
	identityB := fixture.scopedPath("/api/worker-identities", fixture.cluster1, projectB)
	auditA := fixture.scopedPath("/api/worker-identities/audit", fixture.cluster1, projectA)
	auditB := fixture.scopedPath("/api/worker-identities/audit", fixture.cluster1, projectB)
	incidentsA := fixture.scopedPath("/api/incidents", fixture.cluster1, projectA)
	incidentsB := fixture.scopedPath("/api/incidents", fixture.cluster1, projectB)
	configA := fixture.scopedPath("/api/config", fixture.cluster1, projectA)
	clusterConfig := fixture.scopedPath("/api/config", fixture.cluster1, "")

	if response := workLogHTTPCall(t, fixture.handler, http.MethodPost, configA, `{"identityMode":true}`, "human:lead", nil); response.Code != http.StatusOK {
		t.Fatalf("enable project A identity mode = %d %s", response.Code, response.Body.String())
	}
	if response := workLogHTTPCall(t, fixture.handler, http.MethodPut, identityA,
		`{"roles":["backend"],"types":["patch"]}`, "human:lead", nil); response.Code != http.StatusOK {
		t.Fatalf("project A identity write = %d %s", response.Code, response.Body.String())
	}
	var audits struct {
		Audits []struct {
			ID                string `json:"id"`
			RelatedIncidentID string `json:"relatedIncidentId"`
		} `json:"audits"`
	}
	if response := workLogHTTPCall(t, fixture.handler, http.MethodGet, auditA, "", "human:lead", nil); response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &audits) != nil || len(audits.Audits) != 1 || audits.Audits[0].RelatedIncidentID == "" {
		t.Fatalf("project A audit = %d %s %#v", response.Code, response.Body.String(), audits)
	}
	if response := workLogHTTPCall(t, fixture.handler, http.MethodGet, identityB, "", "human:lead", nil); response.Code != http.StatusOK {
		t.Fatalf("project B identity list = %d %s", response.Code, response.Body.String())
	} else {
		var records mcp.WorkerIdentitySnapshot
		if err := json.Unmarshal(response.Body.Bytes(), &records); err != nil || len(records.Records) != 0 || records.ModeEnabled {
			t.Fatalf("project B inherited identity state = %#v err=%v", records, err)
		}
	}
	if response := workLogHTTPCall(t, fixture.handler, http.MethodGet, auditB, "", "human:lead", nil); response.Code != http.StatusOK {
		t.Fatalf("project B audit list = %d %s", response.Code, response.Body.String())
	} else {
		var value mcp.WorkerIdentityAuditSnapshot
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil || len(value.Audits) != 0 {
			t.Fatalf("project B inherited audits = %#v err=%v", value, err)
		}
	}
	if response := workLogHTTPCall(t, fixture.handler, http.MethodGet, incidentsB, "", "human:lead", nil); response.Code != http.StatusOK {
		t.Fatalf("project B incident list = %d %s", response.Code, response.Body.String())
	} else {
		var value struct {
			Incidents []incident.Incident `json:"incidents"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil || len(value.Incidents) != 0 {
			t.Fatalf("project B inherited automatic incident = %#v err=%v", value, err)
		}
	}
	// The fixture server carries project A as its launch-time default. Exercise an
	// actual cluster-only request through a server with no default project so the
	// policy route cannot silently inherit A's project scope.
	clusterOnly := NewWithScope("human:lead", ScopeDefaults{Home: fixture.homeRoot, ClusterID: fixture.cluster1.ID, HomeByDefault: true}).Handler()
	if response := workLogHTTPCall(t, clusterOnly, http.MethodPost, clusterConfig, `{"identityMode":true}`, "human:lead", nil); response.Code != http.StatusConflict {
		t.Fatalf("cluster-only identity policy write = %d %s, want 409", response.Code, response.Body.String())
	}
	if response := workLogHTTPCall(t, clusterOnly, http.MethodGet, fixture.scopedPath("/api/worker-identities", fixture.cluster1, ""), "", "human:lead", nil); response.Code != http.StatusConflict {
		t.Fatalf("cluster-only identities = %d %s, want 409", response.Code, response.Body.String())
	}

	if response := workLogHTTPCall(t, fixture.handler, http.MethodPost, configA, `{"noTraceMode":true}`, "human:lead", nil); response.Code != http.StatusOK {
		t.Fatalf("enable project A noTrace = %d %s", response.Code, response.Body.String())
	}
	if response := workLogHTTPCall(t, fixture.handler, http.MethodPut, identityA,
		`{"roles":["backend","reviewer"],"types":["patch"],"reason":"add review capability"}`, "human:lead", nil); response.Code != http.StatusOK {
		t.Fatalf("project A noTrace identity change = %d %s", response.Code, response.Body.String())
	}
	if response := workLogHTTPCall(t, fixture.handler, http.MethodGet, auditA, "", "human:lead", nil); response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &audits) != nil || len(audits.Audits) != 2 || audits.Audits[1].RelatedIncidentID != "" {
		t.Fatalf("project A noTrace audit = %d %s %#v", response.Code, response.Body.String(), audits)
	}
	if response := workLogHTTPCall(t, fixture.handler, http.MethodGet, incidentsA, "", "human:lead", nil); response.Code != http.StatusOK {
		t.Fatalf("project A incident list = %d %s", response.Code, response.Body.String())
	} else {
		var value struct {
			Incidents []incident.Incident `json:"incidents"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil || len(value.Incidents) != 1 || value.Incidents[0].ID != audits.Audits[0].RelatedIncidentID {
			t.Fatalf("noTrace added automatic incident = %#v err=%v", value, err)
		}
	}

	// A fresh Server uses the same managed home/store, proving project-local
	// policy, audit and automatic Incident survive process restart.
	restarted := NewWithScope("human:lead", ScopeDefaults{Home: fixture.homeRoot, ClusterID: fixture.cluster1.ID, ProjectID: projectA, HomeByDefault: true}).Handler()
	if response := workLogHTTPCall(t, restarted, http.MethodGet, auditA, "", "human:lead", nil); response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &audits) != nil || len(audits.Audits) != 2 {
		t.Fatalf("restart project A audit = %d %s %#v", response.Code, response.Body.String(), audits)
	}
}
