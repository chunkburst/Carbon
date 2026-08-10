package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"carbon/internal/connect"
	"carbon/internal/home"
)

type connectSessionFixture struct {
	homeRoot string
	project  home.Project
	source   string
	handler  http.Handler
}

func newConnectSessionFixture(t *testing.T) connectSessionFixture {
	t.Helper()
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	project, err := home.AddStandaloneProject(homeRoot, home.AddProjectRequest{
		Name: "pronovel", SourcePath: source,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithScope("human:test", ScopeDefaults{Home: homeRoot, HomeByDefault: true})
	return connectSessionFixture{homeRoot: homeRoot, project: project, source: source, handler: server.Handler()}
}

func cursorCarbonArgs(t *testing.T, source string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(source, ".cursor", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MCPServers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	entry, ok := config.MCPServers["carbon"]
	if !ok {
		return nil
	}
	return entry.Args
}

func integrationStatusForID(t *testing.T, body, id string) connect.AgentStatus {
	t.Helper()
	var response struct {
		Agents []connect.AgentStatus `json:"agents"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatal(err)
	}
	for _, agent := range response.Agents {
		if agent.ID == id {
			return agent
		}
	}
	t.Fatalf("agent %q missing from integrations response: %s", id, body)
	return connect.AgentStatus{}
}

func TestProjectSessionConnectUsesHomeOnlyProcessBoundary(t *testing.T) {
	fixture := newConnectSessionFixture(t)
	connectPath := "/api/connect/cursor?routing=session"
	request := `{"actor":"agent:pronovel","configProjectId":"` + fixture.project.ID + `"}`
	if code, body := raw(fixture.handler, http.MethodPost, connectPath, request); code != http.StatusOK {
		t.Fatalf("project-session connect = %d %s", code, body)
	}
	wantArgs := []string{
		"serve", "--actor", "agent:pronovel",
		"--home", fixture.homeRoot,
		"--project-session",
		"--compat-layer", connect.CarbonCompatLayer,
	}
	if got := cursorCarbonArgs(t, fixture.source); !reflect.DeepEqual(got, wantArgs) {
		t.Fatalf("project-session args = %#v, want %#v", got, wantArgs)
	}

	listPath := "/api/connect?routing=session&configProjectId=" + url.QueryEscape(fixture.project.ID)
	if code, body := raw(fixture.handler, http.MethodGet, listPath, ""); code != http.StatusOK {
		t.Fatalf("project-session detect = %d %s", code, body)
	} else if !integrationStatusForID(t, body, "cursor").Connected {
		t.Fatalf("project-session connection was not detected: %s", body)
	}

	// An id is resolved only within the selected Home. Passing the same project id
	// under a different home must never select its original source directory.
	otherHome := t.TempDir()
	if _, err := home.Ensure(otherHome); err != nil {
		t.Fatal(err)
	}
	wrongHomePath := "/api/connect?home=" + url.QueryEscape(otherHome) +
		"&routing=session&configProjectId=" + url.QueryEscape(fixture.project.ID)
	if code, body := raw(fixture.handler, http.MethodGet, wrongHomePath, ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("project-session wrong home = %d %s, want 422", code, body)
	}

	if code, body := raw(fixture.handler, http.MethodDelete, "/api/connect/cursor?routing=session&configProjectId="+url.QueryEscape(fixture.project.ID), ""); code != http.StatusOK {
		t.Fatalf("project-session disconnect = %d %s", code, body)
	}
	if got := cursorCarbonArgs(t, fixture.source); len(got) != 0 {
		t.Fatalf("project-session disconnect left Carbon args: %#v", got)
	}
}

func TestProjectSessionConnectRejectsOfflineConfigProject(t *testing.T) {
	fixture := newConnectSessionFixture(t)
	if err := os.RemoveAll(fixture.source); err != nil {
		t.Fatal(err)
	}
	request := `{"configProjectId":"` + fixture.project.ID + `"}`
	if code, body := raw(fixture.handler, http.MethodPost, "/api/connect/cursor?routing=session", request); code != http.StatusUnprocessableEntity {
		t.Fatalf("offline project-session connect = %d %s, want 422", code, body)
	}
}

func TestProjectSessionRejectsExplicitEmptyScopeParameters(t *testing.T) {
	fixture := newConnectSessionFixture(t)
	for _, parameter := range []string{"path", "repo", "cluster", "project"} {
		t.Run(parameter, func(t *testing.T) {
			path := "/api/connect?routing=session&" + parameter + "="
			if code, body := raw(fixture.handler, http.MethodGet, path, ""); code != http.StatusBadRequest {
				t.Fatalf("project-session %s= = %d %s, want 400", parameter, code, body)
			}
		})
	}
}

func TestSessionConnectScopeRejectsExplicitEmptyScopeHeaders(t *testing.T) {
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	server := NewWithScope("human:test", ScopeDefaults{Home: homeRoot, HomeByDefault: true})
	for _, header := range []string{"X-Carbon-Cluster", "X-Carbon-Project"} {
		t.Run(header, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/connect?routing=session", nil)
			request.Header.Add(header, "")
			if _, err := server.sessionConnectScope(request, ""); err == nil {
				t.Fatalf("project-session accepted explicitly empty %s header", header)
			}
		})
	}
}

func TestProjectSessionWithoutConfigProjectProvidesGuideButCannotWrite(t *testing.T) {
	fixture := newConnectSessionFixture(t)
	if code, body := raw(fixture.handler, http.MethodGet, "/api/connect?routing=session", ""); code != http.StatusOK {
		t.Fatalf("project-session manual list = %d %s", code, body)
	} else {
		var response struct {
			Agents []connect.AgentStatus `json:"agents"`
			Manual bool                  `json:"manual"`
			Reason string                `json:"reason"`
		}
		if err := json.Unmarshal([]byte(body), &response); err != nil {
			t.Fatal(err)
		}
		if !response.Manual || len(response.Agents) != 0 || !strings.Contains(response.Reason, "configProjectId") {
			t.Fatalf("project-session manual list = %s", body)
		}
	}

	if code, body := raw(fixture.handler, http.MethodGet, "/api/connect/cursor/manual?routing=session", ""); code != http.StatusOK {
		t.Fatalf("project-session manual guide = %d %s", code, body)
	} else {
		var guide connect.Guide
		if err := json.Unmarshal([]byte(body), &guide); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(guide.Config, "--project-session") || strings.Contains(guide.Config, `"--cluster"`) || strings.Contains(guide.Config, `"--project"`) {
			t.Fatalf("project-session guide did not use the Home-only contract: %s", guide.Config)
		}
	}

	if code, body := raw(fixture.handler, http.MethodPost, "/api/connect/cursor?routing=session", `{}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("project-session connect without config project = %d %s, want 422", code, body)
	}
	if code, body := raw(fixture.handler, http.MethodDelete, "/api/connect/cursor?routing=session", ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("project-session disconnect without config project = %d %s, want 422", code, body)
	}
}

func TestConnectWithoutSessionRoutingRemainsProjectPinned(t *testing.T) {
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	cluster, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Pinned", Prefix: "PIN"})
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	project, err := home.AddProject(homeRoot, cluster.ID, home.AddProjectRequest{Name: "legacy selection", SourcePath: source})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithScope("human:test", ScopeDefaults{
		Home: homeRoot, ClusterID: cluster.ID, ProjectID: project.ID, HomeByDefault: true,
	})
	handler := server.Handler()
	if code, body := raw(handler, http.MethodPost, "/api/connect/cursor", `{}`); code != http.StatusOK {
		t.Fatalf("pinned connect = %d %s", code, body)
	}
	wantArgs := []string{
		"serve", "--actor", "agent:cursor",
		"--home", homeRoot,
		"--cluster", cluster.ID,
		"--project", project.ID,
		"--compat-layer", connect.CarbonCompatLayer,
	}
	if got := cursorCarbonArgs(t, source); !reflect.DeepEqual(got, wantArgs) {
		t.Fatalf("pinned args changed = %#v, want %#v", got, wantArgs)
	}
	if code, body := raw(handler, http.MethodGet, "/api/connect", ""); code != http.StatusOK {
		t.Fatalf("pinned detect = %d %s", code, body)
	} else if !integrationStatusForID(t, body, "cursor").Connected {
		t.Fatalf("pinned connection was not detected: %s", body)
	}
}
