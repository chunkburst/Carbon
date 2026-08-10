package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"carbon/internal/home"
	"carbon/internal/store"
)

func (f projectClearHTTPFixture) deletePath(projectID string) string {
	query := url.Values{"home": {f.homeRoot}}
	return "/api/home/projects/" + url.PathEscape(projectID) + "/delete?" + query.Encode()
}

func homeManifestHasProject(manifest home.Manifest, projectID string) bool {
	for _, project := range manifest.Projects {
		if project.ID == projectID {
			return true
		}
	}
	for _, cluster := range manifest.Clusters {
		for _, project := range cluster.Projects {
			if project.ID == projectID {
				return true
			}
		}
	}
	return false
}

func TestDeleteHomeProjectHTTPRemovesCatalogAndOptionallyData(t *testing.T) {
	t.Run("grouped project clears only selected data", func(t *testing.T) {
		f := newProjectClearHTTPFixture(t)
		owned := f.createTask(t, f.groupedStore, f.grouped.ID, "owned")
		peer := f.createTask(t, f.groupedStore, f.sibling.ID, "peer")
		code, body := projectClearRaw(f.handler, f.deletePath(f.grouped.ID), `{"name":"东京 Team Alpha","deleteData":true}`, "")
		if code != http.StatusOK || !strings.Contains(body, `"deleteData":true`) {
			t.Fatalf("grouped delete = %d %s", code, body)
		}
		if _, err := f.groupedStore.Get(owned.Task.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("grouped selected task remains: %v", err)
		}
		if _, err := f.groupedStore.Get(peer.Task.ID); err != nil {
			t.Fatalf("grouped peer task was removed: %v", err)
		}
		h, err := home.Open(f.homeRoot)
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := h.Manifest()
		if err != nil {
			t.Fatal(err)
		}
		if homeManifestHasProject(manifest, f.grouped.ID) || !homeManifestHasProject(manifest, f.sibling.ID) {
			t.Fatalf("unexpected grouped manifest after delete: %#v", manifest)
		}
		if _, err := os.Stat(f.grouped.Source.Path); err != nil {
			t.Fatalf("grouped source was deleted: %v", err)
		}
	})

	t.Run("standalone catalog-only keeps task data and source", func(t *testing.T) {
		f := newProjectClearHTTPFixture(t)
		owned := f.createTask(t, f.standaloneStore, f.standalone.ID, "owned")
		code, body := projectClearRaw(f.handler, f.deletePath(f.standalone.ID), `{"name":"Private Omega","deleteData":false}`, "")
		if code != http.StatusOK || !strings.Contains(body, `"deleteData":false`) {
			t.Fatalf("standalone catalog-only delete = %d %s", code, body)
		}
		if _, err := f.standaloneStore.Get(owned.Task.ID); err != nil {
			t.Fatalf("catalog-only standalone delete cleared task: %v", err)
		}
		h, err := home.Open(f.homeRoot)
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := h.Manifest()
		if err != nil {
			t.Fatal(err)
		}
		if homeManifestHasProject(manifest, f.standalone.ID) {
			t.Fatal("standalone entry remains after delete")
		}
		if _, err := os.Stat(f.standalone.Source.Path); err != nil {
			t.Fatalf("standalone source was deleted: %v", err)
		}
	})
}

func TestDeleteHomeProjectHTTPRejectsUnauthorizedStrictNameAndScopeWithoutMutation(t *testing.T) {
	f := newProjectClearHTTPFixture(t)
	owned := f.createTask(t, f.groupedStore, f.grouped.ID, "owned")
	for _, tc := range []struct {
		name  string
		path  string
		body  string
		actor string
		code  int
	}{
		{name: "unknown JSON field", path: f.deletePath(f.grouped.ID), body: `{"name":"东京 Team Alpha","deleteData":false,"unexpected":true}`, code: http.StatusBadRequest},
		{name: "duplicate confirmation name", path: f.deletePath(f.grouped.ID), body: `{"name":"东京 Team Alpha","name":"other","deleteData":false}`, code: http.StatusBadRequest},
		{name: "multiple JSON values", path: f.deletePath(f.grouped.ID), body: `{"name":"东京 Team Alpha","deleteData":false} {}`, code: http.StatusBadRequest},
		{name: "missing deleteData", path: f.deletePath(f.grouped.ID), body: `{"name":"东京 Team Alpha"}`, code: http.StatusBadRequest},
		{name: "null deleteData", path: f.deletePath(f.grouped.ID), body: `{"name":"东京 Team Alpha","deleteData":null}`, code: http.StatusBadRequest},
		{name: "nonhuman actor", path: f.deletePath(f.grouped.ID), body: `{"name":"东京 Team Alpha","deleteData":false}`, actor: "agent:codex", code: http.StatusForbidden},
		{name: "empty human actor", path: f.deletePath(f.grouped.ID), body: `{"name":"东京 Team Alpha","deleteData":false}`, actor: "human:", code: http.StatusForbidden},
		{name: "exact name mismatch", path: f.deletePath(f.grouped.ID), body: `{"name":"东京 team Alpha","deleteData":false}`, code: http.StatusUnprocessableEntity},
		{name: "explicit project scope", path: f.deletePath(f.grouped.ID) + "&project=" + url.QueryEscape(f.grouped.ID), body: `{"name":"东京 Team Alpha","deleteData":false}`, code: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := projectClearRaw(f.handler, tc.path, tc.body, tc.actor)
			if code != tc.code {
				t.Fatalf("rejected delete = %d %s, want %d", code, body, tc.code)
			}
			if _, err := f.groupedStore.Get(owned.Task.ID); err != nil {
				t.Fatalf("rejected delete mutated task: %v", err)
			}
			h, err := home.Open(f.homeRoot)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := h.Manifest()
			if err != nil {
				t.Fatal(err)
			}
			if !homeManifestHasProject(manifest, f.grouped.ID) {
				t.Fatal("rejected delete removed manifest entry")
			}
		})
	}
	if code, body := projectClearRaw(f.handler, f.deletePath("project_missing"), `{"name":"东京 Team Alpha","deleteData":false}`, ""); code != http.StatusNotFound {
		t.Fatalf("unknown stable id = %d %s, want 404", code, body)
	}
}

func TestDeleteHomeProjectHTTPMapsSurvivingPeerReferenceToConflict(t *testing.T) {
	f := newProjectClearHTTPFixture(t)
	owned := f.createTask(t, f.groupedStore, f.grouped.ID, "owned")
	peer, err := f.groupedStore.Create(store.Draft{
		Title: "peer depends on target", ProjectID: f.sibling.ID, ProjectIDSet: true, Deps: []string{owned.Task.ID},
	}, "human:test", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	code, body := projectClearRaw(f.handler, f.deletePath(f.grouped.ID), `{"name":"东京 Team Alpha","deleteData":true}`, "")
	if code != http.StatusConflict {
		t.Fatalf("peer reference delete = %d %s, want conflict", code, body)
	}
	if _, err := f.groupedStore.Get(owned.Task.ID); err != nil {
		t.Fatalf("conflict delete removed target: %v", err)
	}
	if _, err := f.groupedStore.Get(peer.Task.ID); err != nil {
		t.Fatalf("conflict delete removed peer: %v", err)
	}
}

func TestProjectDeleteErrorMappingFailsClosedForSafetyReferencesAndConcurrency(t *testing.T) {
	for _, err := range []error{
		home.ErrUnsafePath,
		home.ErrLockTimeout,
		store.ErrLockTimeout,
		store.ErrProjectTaskDataChanged,
		store.ErrProjectTaskDataReferenced,
		home.ErrProjectDeleteRecovery,
	} {
		recorder := httptest.NewRecorder()
		writeProjectDeleteErr(recorder, err)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("delete error %v = %d, want conflict", err, recorder.Code)
		}
	}
}
