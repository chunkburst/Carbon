package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"carbon/internal/home"
	"carbon/internal/store"
)

func carbonClaimContractFixture(t *testing.T) (http.Handler, *store.Store, *store.Doc) {
	t.Helper()
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	cluster, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Lease contract", Prefix: "LCT"})
	if err != nil {
		t.Fatal(err)
	}
	dataRoot, err := home.ClusterDataRoot(homeRoot, cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := initCarbonDataRoot(dataRoot, ""); err != nil {
		t.Fatal(err)
	}
	st := store.New(dataRoot)
	doc, err := st.Create(store.Draft{Title: "claim target", ProjectIDSet: true}, "human:seed", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	api := NewWithScope("human:test", ScopeDefaults{Home: homeRoot, ClusterID: cluster.ID, HomeByDefault: true})
	return api.Handler(), st, doc
}

func assertLeaseClaimUnchanged(t *testing.T, st *store.Store, id, version string) {
	t.Helper()
	doc, err := st.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Version() != version {
		t.Fatalf("validation failure changed task version: got %q, want %q", doc.Version(), version)
	}
	if doc.Task.Assignee != "" || doc.Task.Lease != nil || len(doc.Task.PendingClaims) != 0 {
		t.Fatalf("validation failure changed lease state: %+v", doc.Task)
	}
}

func TestCarbonLeaseClaimRequiresReasonAndExpectedVersionBeforeMutation(t *testing.T) {
	h, st, doc := carbonClaimContractFixture(t)
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "blank reason",
			body: fmt.Sprintf(`{"reason":"   ","expectedVersion":%q}`, doc.Version()),
			want: "reason is required",
		},
		{
			name: "blank expected version",
			body: `{"reason":"claim for regression coverage","expectedVersion":"   "}`,
			want: "expectedVersion is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := raw(h, http.MethodPost, "/api/tasks/"+doc.Task.ID+"/lease/claim", tc.body)
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("lease claim = %d %s, want 422", code, body)
			}
			if !strings.Contains(body, tc.want) {
				t.Fatalf("lease claim error = %s, want %q", body, tc.want)
			}
			assertLeaseClaimUnchanged(t, st, doc.Task.ID, doc.Version())
		})
	}
}

func TestCarbonLeaseClaimKeepsPendingAcceptedResponse(t *testing.T) {
	h, st, doc := carbonClaimContractFixture(t)
	doc.SetAssignee("agent:holder")
	if err := st.Save(doc); err != nil {
		t.Fatal(err)
	}
	code, body := raw(h, http.MethodPost, "/api/tasks/"+doc.Task.ID+"/lease/claim", fmt.Sprintf(`{"reason":"request handoff","expectedVersion":%q}`, doc.Version()))
	if code != http.StatusAccepted {
		t.Fatalf("conflicting lease claim = %d %s, want 202", code, body)
	}
	var response struct {
		Pending bool    `json:"pending"`
		Task    taskDTO `json:"task"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Pending || response.Task.Assignee != "agent:holder" {
		t.Fatalf("pending lease claim response = %+v", response)
	}
	reloaded, err := st.Get(doc.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Task.PendingClaims) != 1 || reloaded.Task.PendingClaims[0].Actor != "human:test" {
		t.Fatalf("pending lease claim was not retained: %+v", reloaded.Task.PendingClaims)
	}
}

func TestLegacyClaimRouteIsLimitedToStableLegacyScope(t *testing.T) {
	carbonHandler, carbonStore, carbonTask := carbonClaimContractFixture(t)
	code, body := raw(carbonHandler, http.MethodPost, "/api/tasks/"+carbonTask.Task.ID+"/claim", "")
	if code != http.StatusGone {
		t.Fatalf("Carbon legacy claim = %d %s, want 410", code, body)
	}
	if !strings.Contains(body, "/lease/claim") {
		t.Fatalf("Carbon legacy claim response = %s, want lease route guidance", body)
	}
	assertLeaseClaimUnchanged(t, carbonStore, carbonTask.Task.ID, carbonTask.Version())

	_, legacyHandler := newServer(t)
	var initialized statusResp
	call(t, legacyHandler, http.MethodPost, "/api/init", `{"prefix":"LEG"}`, &initialized)
	var created taskDTO
	call(t, legacyHandler, http.MethodPost, "/api/tasks", `{"title":"legacy claim remains supported"}`, &created)
	var claimed taskDTO
	call(t, legacyHandler, http.MethodPost, "/api/tasks/"+created.ID+"/claim", "", &claimed)
	if claimed.Assignee != "human:test" {
		t.Fatalf("legacy claim assignee = %q, want human:test", claimed.Assignee)
	}
}
