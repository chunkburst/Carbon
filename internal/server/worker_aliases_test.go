package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"carbon/internal/home"
)

func TestWorkerAliasesHTTPRoundTripUsesHomeGlobalDocument(t *testing.T) {
	root := t.TempDir()
	if _, err := home.Ensure(root); err != nil {
		t.Fatal(err)
	}
	service := NewWithScope("human:test", ScopeDefaults{Home: root, HomeByDefault: true})
	handler := service.Handler()

	var initial workerAliasesResp
	call(t, handler, http.MethodGet, "/api/home/workers/aliases", "", &initial)
	if len(initial.Aliases) != 0 {
		t.Fatalf("initial aliases = %#v, want empty", initial.Aliases)
	}

	var updated workerAliasesResp
	call(t, handler, http.MethodPatch, "/api/home/workers/aliases", `{"actor":"agent:Codex","alias":"  codex1  "}`, &updated)
	if got := updated.Aliases["agent:Codex"]; got != "codex1" {
		t.Fatalf("updated alias = %q, want codex1", got)
	}
	// The actor map key stays byte-for-byte as supplied. Alias case-insensitive
	// comparisons must not imply that actor identities are lower-cased.
	if _, exists := updated.Aliases["agent:codex"]; exists {
		t.Fatalf("actor was canonicalized unexpectedly: %#v", updated.Aliases)
	}

	var listed workerAliasesResp
	call(t, handler, http.MethodGet, "/api/home/workers/aliases", "", &listed)
	if got := listed.Aliases["agent:Codex"]; got != "codex1" {
		t.Fatalf("listed alias = %q, want codex1", got)
	}

	var deleted workerAliasesResp
	call(t, handler, http.MethodPatch, "/api/home/workers/aliases", `{"actor":"agent:Codex","alias":""}`, &deleted)
	if len(deleted.Aliases) != 0 {
		t.Fatalf("deleted aliases = %#v, want empty", deleted.Aliases)
	}

	data, err := os.ReadFile(filepath.Join(root, home.CarbonDirName, home.WorkerAliasesFilename))
	if err != nil {
		t.Fatal(err)
	}
	var file home.WorkerAliasesFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if file.Version != home.WorkerAliasesVersion || len(file.Aliases) != 0 {
		t.Fatalf("alias file = %#v", file)
	}
}

func TestWorkerAliasesHTTPRejectsInvalidAliasesAndLeavesExistingAlias(t *testing.T) {
	root := t.TempDir()
	if _, err := home.Ensure(root); err != nil {
		t.Fatal(err)
	}
	handler := NewWithScope("human:test", ScopeDefaults{Home: root, HomeByDefault: true}).Handler()
	if code, body := raw(handler, http.MethodPatch, "/api/home/workers/aliases", `{"actor":"agent:codex","alias":"codex1"}`); code != http.StatusOK {
		t.Fatalf("initial alias = %d %s", code, body)
	}
	for _, body := range []string{
		`{"actor":" agent:codex","alias":"other"}`,
		`{"actor":"agent:other","alias":"CODEX1"}`,
		`{"actor":"agent:other","alias":"bad\nname"}`,
	} {
		if code, response := raw(handler, http.MethodPatch, "/api/home/workers/aliases", body); code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid alias %s = %d %s, want 422", body, code, response)
		}
	}
	var listed workerAliasesResp
	call(t, handler, http.MethodGet, "/api/home/workers/aliases", "", &listed)
	if len(listed.Aliases) != 1 || listed.Aliases["agent:codex"] != "codex1" {
		t.Fatalf("invalid requests mutated aliases: %#v", listed.Aliases)
	}
}

func TestWorkerAliasesHTTPRequiresHomeOnlyScope(t *testing.T) {
	f := newProjectScopeFixture(t)
	for _, test := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/home/workers/aliases", ""},
		{http.MethodPatch, "/api/home/workers/aliases", `{"actor":"agent:codex","alias":"codex1"}`},
	} {
		if code, body := raw(f.handler, test.method, test.path, test.body); code != http.StatusBadRequest {
			t.Fatalf("selected %s = %d %s, want 400", test.method, code, body)
		}
	}

	homeOnly := NewWithScope("human:test", ScopeDefaults{Home: f.homeRoot, HomeByDefault: true}).Handler()
	if code, body := raw(homeOnly, http.MethodGet, "/api/home/workers/aliases?cluster="+url.QueryEscape(f.project1.ID), ""); code != http.StatusBadRequest {
		t.Fatalf("query cluster scope = %d %s, want 400", code, body)
	}
	legacyPath := "/api/home/workers/aliases?path=" + url.QueryEscape(f.homeRoot)
	if code, body := raw(homeOnly, http.MethodGet, legacyPath, ""); code != http.StatusBadRequest {
		t.Fatalf("legacy path scope = %d %s, want 400", code, body)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/home/workers/aliases", nil)
	req.Header.Set("X-Carbon-Project", f.project1.ID)
	resp := httptest.NewRecorder()
	homeOnly.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("header project scope = %d %s, want 400", resp.Code, resp.Body.String())
	}
}

func TestWorkerAliasesHTTPFailsClosedForMalformedAliasFile(t *testing.T) {
	root := t.TempDir()
	if _, err := home.Ensure(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, home.CarbonDirName, home.WorkerAliasesFilename)
	bad := []byte(`{"version":1,"aliases":{"agent:one":"same","agent:two":"SAME"}}`)
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	handler := NewWithScope("human:test", ScopeDefaults{Home: root, HomeByDefault: true}).Handler()
	if code, body := raw(handler, http.MethodGet, "/api/home/workers/aliases", ""); code != http.StatusBadRequest {
		t.Fatalf("malformed aliases GET = %d %s, want 400", code, body)
	}
	if code, body := raw(handler, http.MethodPatch, "/api/home/workers/aliases", `{"actor":"agent:new","alias":"new"}`); code != http.StatusBadRequest {
		t.Fatalf("malformed aliases PATCH = %d %s, want 400", code, body)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(bad) {
		t.Fatalf("malformed file was changed\nbefore=%q\nafter=%q", bad, after)
	}
}
