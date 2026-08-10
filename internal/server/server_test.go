package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"carbon/internal/compat"
	"carbon/internal/home"
	"carbon/internal/mcp"
	"carbon/internal/repo"
	"carbon/internal/session"
)

// webIDRe matches the time-ordered ids minted by the store (prefix + 16 base32 chars).
var webIDRe = regexp.MustCompile(`^WEB-[0-9a-z]{10}$`)

type runResp struct {
	Runs []struct {
		File     string `json:"file"`
		At       string `json:"at"`
		Cmd      string `json:"cmd"`
		Cwd      string `json:"cwd"`
		Head     string `json:"head"`
		Exit     int    `json:"exit"`
		TimedOut bool   `json:"timedout"`
		Duration string `json:"duration"`
		Output   string `json:"output"`
	} `json:"runs"`
}

func TestValidateLoopbackAddr(t *testing.T) {
	for _, addr := range []string{"localhost:2525", "127.0.0.1:2525", "[::1]:2525"} {
		if err := ValidateLoopbackAddr(addr); err != nil {
			t.Errorf("ValidateLoopbackAddr(%q) = %v, want nil", addr, err)
		}
	}
	for _, addr := range []string{":2525", "0.0.0.0:2525", "[::]:2525", "192.0.2.1:2525", "localhost.localdomain:2525", "LOCALHOST:2525", "127.0.0.1:"} {
		err := ValidateLoopbackAddr(addr)
		if !errors.Is(err, ErrNonLoopbackAddr) {
			t.Errorf("ValidateLoopbackAddr(%q) = %v, want ErrNonLoopbackAddr", addr, err)
		}
	}
}

func TestRunRejectsNonLoopbackAddressBeforeServing(t *testing.T) {
	s, _ := newServer(t)
	if err := s.Run(":0"); !errors.Is(err, ErrNonLoopbackAddr) {
		t.Fatalf("Run(:0) = %v, want ErrNonLoopbackAddr", err)
	}
}

func TestRunsEndpointParsesLogsNewestFirst(t *testing.T) {
	s, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	created := createRunTask(t, h)

	runsDir := filepath.Join(s.defaultRoot, repo.CarbonDirName, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(runsDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(created.ID+"-20260621-185736.523.log",
		"cmd: echo old\ncwd: /repo\nexit: 1  timedout: false  duration: 500ms\n----\nold output\n")
	write(created.ID+"-20260621-190000.000.log",
		"cmd: echo new\ncwd: /repo\nhead: abc123\nexit: 0  timedout: false  duration: 1.2s\n----\nnew output\n")
	// A different task's log must not leak into WEB-001's runs.
	write("WEB-002-20260621-190001.000.log",
		"cmd: nope\ncwd: /repo\nexit: 0  timedout: false  duration: 1s\n----\n")

	var resp runResp
	call(t, h, "GET", "/api/tasks/"+created.ID+"/runs", "", &resp)
	if len(resp.Runs) != 2 {
		t.Fatalf("want 2 runs, got %d: %+v", len(resp.Runs), resp.Runs)
	}
	newest := resp.Runs[0]
	if newest.Cmd != "echo new" || newest.Exit != 0 || newest.Output != "new output\n" {
		t.Fatalf("newest run: %+v", newest)
	}
	if newest.Head != "abc123" {
		t.Fatalf("newest Head: %q", newest.Head)
	}
	if newest.At != "2026-06-21T19:00:00Z" {
		t.Fatalf("newest At: %q", newest.At)
	}
	older := resp.Runs[1]
	if older.Cmd != "echo old" || older.Exit != 1 || older.TimedOut {
		t.Fatalf("older run: %+v", older)
	}
}

func TestParseRunParsesCollisionSuffixTimestamp(t *testing.T) {
	run := parseRun("WEB-001", "WEB-001-20260621-190000.000-001.log", "----\n")
	if run.At != "2026-06-21T19:00:00Z" {
		t.Fatalf("collision-suffixed timestamp = %q", run.At)
	}
}

func TestRunsEndpointEmptyWhenNoRuns(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	created := createRunTask(t, h)
	var resp runResp
	call(t, h, "GET", "/api/tasks/"+created.ID+"/runs", "", &resp)
	if len(resp.Runs) != 0 {
		t.Fatalf("want 0 runs, got %d", len(resp.Runs))
	}
}

func TestRunsEndpointSkipsSymlinkedLog(t *testing.T) {
	s, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	created := createRunTask(t, h)

	runsDir := filepath.Join(s.defaultRoot, repo.CarbonDirName, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(secret, []byte("head: outside-secret\n----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(runsDir, created.ID+"-20260621-190000.000.log")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	code, body := raw(h, "GET", "/api/tasks/"+created.ID+"/runs", "")
	if code != http.StatusOK {
		t.Fatalf("runs status = %d: %s", code, body)
	}
	if strings.Contains(body, "outside-secret") {
		t.Fatalf("runs API followed a log symlink: %s", body)
	}
	var resp runResp
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Runs) != 0 {
		t.Fatalf("want no readable runs, got %+v", resp.Runs)
	}
	if got := latestRunHead(s.defaultRoot, created.ID); got != "" {
		t.Fatalf("latestRunHead followed a log symlink: %q", got)
	}
}

func TestRunsEndpointRejectsSymlinkedRunsDirectory(t *testing.T) {
	s, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	created := createRunTask(t, h)

	runsDir := filepath.Join(s.defaultRoot, repo.CarbonDirName, "runs")
	if err := os.Remove(runsDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, created.ID+"-20260621-190000.000.log"), []byte("head: outside-secret\n----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, runsDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	code, body := raw(h, "GET", "/api/tasks/"+created.ID+"/runs", "")
	if code != http.StatusOK {
		t.Fatalf("runs status = %d: %s", code, body)
	}
	if strings.Contains(body, "outside-secret") {
		t.Fatalf("runs API followed the runs-directory symlink: %s", body)
	}
	if got := latestRunHead(s.defaultRoot, created.ID); got != "" {
		t.Fatalf("latestRunHead followed the runs-directory symlink: %q", got)
	}
}

func TestRunsEndpointRejectsUnsafeTaskID(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)

	// Brackets are meaningful to filepath.Glob. The router decodes the escaped path
	// value before handleRuns sees it, so this proves validation happens before Glob.
	if code, body := raw(h, "GET", "/api/tasks/WEB%5B1%5D/runs", ""); code != http.StatusUnprocessableEntity || !strings.Contains(body, "invalid") {
		t.Fatalf("unsafe task id = %d %s, want 422 invalid id", code, body)
	}
}

func createRunTask(t *testing.T, h http.Handler) taskDTO {
	t.Helper()
	var created taskDTO
	call(t, h, "POST", "/api/tasks", `{"title":"run task"}`, &created)
	return created
}

func newServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	s := New(t.TempDir(), "human:test")
	return s, s.Handler()
}

func TestStatusThenInit(t *testing.T) {
	s, h := newServer(t)

	var st statusResp
	call(t, h, "GET", "/api/status", "", &st)
	if st.Initialized {
		t.Fatal("should not be initialized")
	}
	if st.SuggestedPrefix == "" {
		t.Fatal("expected a suggested prefix")
	}

	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	if !st.Initialized || st.Prefix != "WEB" {
		t.Fatalf("after init: %+v", st)
	}
	if !repo.IsInitialized(s.defaultRoot) {
		t.Fatal("workspace not created on disk")
	}
	// Status now carries the config states for the board.
	call(t, h, "GET", "/api/status", "", &st)
	if len(st.States) == 0 || st.Initial != "backlog" {
		t.Fatalf("status missing config: %+v", st)
	}
}

func TestTaskLifecycleOverHTTP(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)

	// Create with a passing check.
	var created taskDTO
	call(t, h, "POST", "/api/tasks", `{"title":"ship it","checks":[{"desc":"t","cmd":"exit 0"}]}`, &created)
	if !webIDRe.MatchString(created.ID) || created.Status != "backlog" {
		t.Fatalf("created: %+v", created)
	}

	// List shows it.
	var list struct {
		Tasks []taskDTO `json:"tasks"`
	}
	call(t, h, "GET", "/api/tasks", "", &list)
	if len(list.Tasks) != 1 {
		t.Fatalf("want 1 task, got %d", len(list.Tasks))
	}

	// Claim.
	var claimed taskDTO
	call(t, h, "POST", "/api/tasks/"+created.ID+"/claim", "", &claimed)
	if claimed.Assignee != "human:test" {
		t.Fatalf("assignee: %q", claimed.Assignee)
	}

	// Transition to done auto-runs the passing check and closes.
	var done taskDTO
	call(t, h, "POST", "/api/tasks/"+created.ID+"/transition", `{"to":"done"}`, &done)
	if done.Status != "done" || done.Checks[0].Result != "pass" {
		t.Fatalf("transition: %+v", done)
	}
}

func TestCreateRejectsBadJSONAndBlankTitle(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)

	if code, body := raw(h, "POST", "/api/tasks", `{"title":`); code != http.StatusBadRequest || !strings.Contains(body, "invalid JSON") {
		t.Fatalf("bad JSON = %d %s, want 400 invalid JSON", code, body)
	}
	if code, body := raw(h, "POST", "/api/tasks", `{"title":"  "}`); code != http.StatusUnprocessableEntity || !strings.Contains(body, "title") {
		t.Fatalf("blank title = %d %s, want 422 title error", code, body)
	}
}

func TestLegacyCreateRetainsDirectAssignee(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	var created taskDTO
	call(t, h, "POST", "/api/tasks", `{"title":"legacy assignment","assignee":"human:other"}`, &created)
	if created.Assignee != "human:other" {
		t.Fatalf("legacy create assignee = %q, want human:other", created.Assignee)
	}
}

func TestSessionLifecycleOverHTTP(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	var created taskDTO
	call(t, h, "POST", "/api/tasks", `{"title":"observable"}`, &created)

	var identity mcp.Identity
	call(t, h, "GET", "/api/identity", "", &identity)
	if identity.Actor != "human:test" {
		t.Fatalf("identity = %+v", identity)
	}

	var begun mcp.SessionView
	call(t, h, "POST", "/api/tasks/"+created.ID+"/sessions/begin",
		`{"expectedActor":"human:test","client":"test","idempotencyKey":"begin-1"}`, &begun)
	if begun.TaskID != created.ID || begun.Status != session.StatusActive {
		t.Fatalf("begun = %+v", begun)
	}

	var heartbeat mcp.SessionView
	call(t, h, "POST", "/api/sessions/"+begun.ID+"/heartbeat", `{"progress":"running tests"}`, &heartbeat)
	if heartbeat.Live == nil || heartbeat.Live.Progress != "running tests" {
		t.Fatalf("heartbeat = %+v", heartbeat)
	}

	var finished mcp.SessionView
	call(t, h, "POST", "/api/sessions/"+begun.ID+"/finish", `{"summary":"implemented"}`, &finished)
	if finished.Status != session.StatusFinished {
		t.Fatalf("finished = %+v", finished)
	}

	var sessions struct {
		Sessions []mcp.SessionView `json:"sessions"`
	}
	call(t, h, "GET", "/api/tasks/"+created.ID+"/sessions", "", &sessions)
	if len(sessions.Sessions) != 1 || sessions.Sessions[0].ID != begun.ID {
		t.Fatalf("sessions = %+v", sessions.Sessions)
	}

	var list struct {
		Tasks []taskDTO `json:"tasks"`
	}
	call(t, h, "GET", "/api/tasks?execution=awaiting_review", "", &list)
	if len(list.Tasks) != 1 || list.Tasks[0].SessionID != begun.ID || list.Tasks[0].ExecutionState != mcp.ExecutionAwaitingReview {
		t.Fatalf("awaiting-review tasks = %+v", list.Tasks)
	}
}

func TestSessionIdentityMismatchIs422(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	var created taskDTO
	call(t, h, "POST", "/api/tasks", `{"title":"observable"}`, &created)

	code, body := raw(h, "POST", "/api/tasks/"+created.ID+"/sessions/begin",
		`{"expectedActor":"agent:codex","idempotencyKey":"begin-1"}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "bound") {
		t.Fatalf("identity mismatch = %d %s", code, body)
	}
}

func TestBeginSessionRejectsOutsideWorktreeBeforeMutation(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	var created taskDTO
	call(t, h, "POST", "/api/tasks", `{"title":"observable"}`, &created)

	payload, err := json.Marshal(beginSessionReq{
		ExpectedActor:  "human:test",
		IdempotencyKey: "begin-outside",
		Worktree:       t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, body := raw(h, "POST", "/api/tasks/"+created.ID+"/sessions/begin", string(payload)); code != http.StatusUnprocessableEntity || !strings.Contains(body, "worktree") {
		t.Fatalf("outside worktree = %d %s, want 422 worktree error", code, body)
	}
	var reloaded taskDTO
	call(t, h, "GET", "/api/tasks/"+created.ID, "", &reloaded)
	if reloaded.Status != "backlog" || reloaded.Assignee != "" {
		t.Fatalf("task mutated after rejected worktree: %+v", reloaded)
	}
}

func TestTransitionRefusalIs422(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	var created taskDTO
	call(t, h, "POST", "/api/tasks", `{"title":"x","checks":[{"desc":"bad","cmd":"exit 1"}]}`, &created)

	code, body := raw(h, "POST", "/api/tasks/"+created.ID+"/transition", `{"to":"done"}`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", code, body)
	}
	if !strings.Contains(body, "checks") {
		t.Fatalf("expected gate reason in body, got %s", body)
	}
}

func TestGetMissingIs404(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", "", &st)
	if code, _ := raw(h, "GET", "/api/tasks/NOPE-999", ""); code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
}

func TestAttestEndpointUnblocksClose(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)

	var created taskDTO
	call(t, h, "POST", "/api/tasks", `{"title":"review me","checks":[{"desc":"human review","type":"manual"}]}`, &created)

	// Closing is refused while the manual check is pending.
	if code, _ := raw(h, "POST", "/api/tasks/"+created.ID+"/transition", `{"to":"done"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("close before attest = %d, want 422", code)
	}

	var attested taskDTO
	call(t, h, "POST", "/api/tasks/"+created.ID+"/attest", `{"index":0,"pass":true}`, &attested)
	if attested.Checks[0].Result != "pass" {
		t.Fatalf("after attest: %+v", attested.Checks[0])
	}

	var done taskDTO
	call(t, h, "POST", "/api/tasks/"+created.ID+"/transition", `{"to":"done"}`, &done)
	if done.Status != "done" {
		t.Fatalf("close after attest: %+v", done)
	}
}

func TestListIncludesUpdatedAt(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	var created taskDTO
	call(t, h, "POST", "/api/tasks", `{"title":"x"}`, &created)

	var list struct {
		Tasks []taskDTO `json:"tasks"`
	}
	call(t, h, "GET", "/api/tasks", "", &list)
	if len(list.Tasks) != 1 || list.Tasks[0].UpdatedAt == "" {
		t.Fatalf("expected updatedAt on list, got %+v", list.Tasks)
	}
	first := list.Tasks[0].UpdatedAt

	// A note appends a newer provenance entry; updatedAt must not go backwards.
	call(t, h, "POST", "/api/tasks/"+created.ID+"/note", `{"text":"hi"}`, &created)
	call(t, h, "GET", "/api/tasks", "", &list)
	if list.Tasks[0].UpdatedAt < first {
		t.Fatalf("updatedAt regressed: %q < %q", list.Tasks[0].UpdatedAt, first)
	}
}

func TestHealthz(t *testing.T) {
	_, h := newServer(t)
	code, body := raw(h, "GET", "/healthz", "")
	if code != http.StatusOK || strings.TrimSpace(body) != "ok" {
		t.Fatalf("healthz = %d %q, want 200 ok", code, body)
	}
}

func TestCompatibilityEnvelopeKeepsFrozenLegacyAndStableCarbonAcrossBuilds(t *testing.T) {
	root := t.TempDir()
	if err := repo.Init(root, ""); err != nil {
		t.Fatal(err)
	}
	s, err := NewWithScopeAndCompatibility("human:test", ScopeDefaults{LegacyRoot: root}, CompatibilityOptions{
		ProductVersion: "19.4.7+portable",
	})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	var status statusResp
	call(t, h, http.MethodGet, "/api/status", "", &status)
	if status.ProductVersion != "19.4.7+portable" || status.APIVersion != compat.APIVersion {
		t.Fatalf("status version contract = %+v", status.Contract)
	}
	if status.RequestedCompatLayer != compat.LegacyLayer || status.StableCompatLayer != compat.StableLayer {
		t.Fatalf("legacy status compatibility = %+v, want requested v1/stable v2", status.Contract)
	}
	if status.CarbonVersion != "0.3" {
		t.Fatalf("legacy compatibility alias = %q, want 0.3", status.CarbonVersion)
	}
	if len(status.SupportedCompatLayers) != 2 || status.SupportedCompatLayers[0] != compat.LegacyLayer || status.SupportedCompatLayers[1] != compat.StableLayer {
		t.Fatalf("supported layers = %v", status.SupportedCompatLayers)
	}

	var versionInfo versionResp
	call(t, h, http.MethodGet, "/api/version", "", &versionInfo)
	if versionInfo.ProductVersion != status.ProductVersion || versionInfo.RequestedCompatLayer != compat.LegacyLayer || versionInfo.StableCompatLayer != compat.StableLayer || versionInfo.CarbonVersion != "0.3" {
		t.Fatalf("/api/version = %+v, want legacy v1 contract", versionInfo)
	}

	var identity struct {
		Actor         string          `json:"actor"`
		Compatibility compat.Contract `json:"compatibility"`
		compat.Contract
	}
	call(t, h, http.MethodGet, "/api/identity", "", &identity)
	if identity.Actor != "human:test" || identity.ProductVersion != status.ProductVersion || identity.RequestedCompatLayer != compat.LegacyLayer || identity.Compatibility.ProductVersion != status.ProductVersion || identity.Compatibility.RequestedCompatLayer != compat.LegacyLayer {
		t.Fatalf("/api/identity = %+v", identity)
	}

	plainReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	plainW := httptest.NewRecorder()
	h.ServeHTTP(plainW, plainReq)
	if plainW.Code != http.StatusOK || strings.TrimSpace(plainW.Body.String()) != "ok" {
		t.Fatalf("plain health = %d %q, want 200 ok", plainW.Code, plainW.Body.String())
	}
	if got := plainW.Header().Get("X-Carbon-Product-Version"); got != status.ProductVersion {
		t.Fatalf("health product header = %q, want %q", got, status.ProductVersion)
	}
	if got := plainW.Header().Get("X-Carbon-Requested-Compat-Layer"); got != compat.LegacyLayer {
		t.Fatalf("health requested layer = %q, want v1", got)
	}
	if got := plainW.Header().Get("X-Carbon-Stable-Compat-Layer"); got != compat.StableLayer {
		t.Fatalf("health stable layer = %q, want v2", got)
	}

	jsonReq := httptest.NewRequest(http.MethodGet, "/healthz?format=json", nil)
	jsonW := httptest.NewRecorder()
	h.ServeHTTP(jsonW, jsonReq)
	var health healthResp
	if err := json.Unmarshal(jsonW.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.Status != "ok" || health.RequestedCompatLayer != compat.LegacyLayer || health.StableCompatLayer != compat.StableLayer || health.APIVersion != compat.APIVersion {
		t.Fatalf("JSON health = %+v", health)
	}
}

func TestCarbonScopeDefaultsToApprovedStableV2(t *testing.T) {
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	cluster, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "Stable", Prefix: "STB"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewWithScopeAndCompatibility("human:test", ScopeDefaults{
		Home: homeRoot, ClusterID: cluster.ID, HomeByDefault: true,
	}, CompatibilityOptions{ProductVersion: "0.4.99"})
	if err != nil {
		t.Fatal(err)
	}
	var status statusResp
	call(t, s.Handler(), http.MethodGet, "/api/status", "", &status)
	if status.Scope.Mode != "carbon" || status.RequestedCompatLayer != compat.StableLayer {
		t.Fatalf("Carbon status = %+v", status)
	}
	if status.StableCompatLayer != compat.StableLayer {
		t.Fatalf("Carbon stable layer = %q, want %q", status.StableCompatLayer, compat.StableLayer)
	}
	if !slices.Contains(status.Capabilities, "carbon-0.4") {
		t.Fatalf("stable v2 capabilities = %v", status.Capabilities)
	}
	if status.CarbonVersion != "0.4" {
		t.Fatalf("Carbon product alias = %q, want 0.4", status.CarbonVersion)
	}
	healthW := httptest.NewRecorder()
	s.Handler().ServeHTTP(healthW, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if got := healthW.Header().Get("X-Carbon-Requested-Compat-Layer"); got != compat.StableLayer {
		t.Fatalf("Carbon health requested layer = %q, want stable v2", got)
	}
	if got := healthW.Header().Get("X-Carbon-Stable-Compat-Layer"); got != compat.StableLayer {
		t.Fatalf("Carbon health stable layer = %q, want stable v2", got)
	}
}

func TestServerRejectsUnknownCompatibilityLayer(t *testing.T) {
	_, err := NewWithScopeAndCompatibility("human:test", ScopeDefaults{LegacyRoot: t.TempDir()}, CompatibilityOptions{
		ProductVersion: "0.4.0", RequestedCompatLayer: "v3",
	})
	if !errors.Is(err, compat.ErrUnsupportedLayer) {
		t.Fatalf("constructor error = %v, want ErrUnsupportedLayer", err)
	}
}

func TestScopedMCPScopeCarriesCanonicalLayerAndExplicitCluster(t *testing.T) {
	homeRoot := t.TempDir()
	if _, err := home.Ensure(homeRoot); err != nil {
		t.Fatal(err)
	}
	cluster, err := home.CreateCluster(homeRoot, home.CreateClusterRequest{Name: "MCP", Prefix: "MCP"})
	if err != nil {
		t.Fatal(err)
	}
	carbon, err := NewWithScopeAndCompatibility("human:test", ScopeDefaults{
		Home: homeRoot, ClusterID: cluster.ID, HomeByDefault: true,
	}, CompatibilityOptions{ProductVersion: "0.4.0", RequestedCompatLayer: "0.4"})
	if err != nil {
		t.Fatal(err)
	}
	carbonScope, err := carbon.resolveScope(httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if err != nil {
		t.Fatal(err)
	}
	carbonMCP := carbon.scopedMCPScope(carbonScope)
	if carbonMCP.CompatLayer != compat.StableLayer || !carbonMCP.ClusterScope || carbonMCP.Legacy {
		t.Fatalf("Carbon MCP scope = %+v, want canonical v2 and explicit cluster", carbonMCP)
	}

	legacyRoot := t.TempDir()
	legacy, err := NewWithScopeAndCompatibility("human:test", ScopeDefaults{LegacyRoot: legacyRoot}, CompatibilityOptions{ProductVersion: "0.4.0"})
	if err != nil {
		t.Fatal(err)
	}
	legacyScope, err := legacy.resolveScope(httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if err != nil {
		t.Fatal(err)
	}
	legacyMCP := legacy.scopedMCPScope(legacyScope)
	if legacyMCP.CompatLayer != compat.LegacyLayer || legacyMCP.ClusterScope || !legacyMCP.Legacy {
		t.Fatalf("legacy MCP scope = %+v, want canonical v1 legacy scope", legacyMCP)
	}
}

func TestHandlerSetsSafeResponseHeaders(t *testing.T) {
	_, h := newServer(t)
	r := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	csp := w.Header().Get("Content-Security-Policy")
	for _, directive := range []string{
		"default-src 'self'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
		"object-src 'none'",
		"script-src 'self'",
		"connect-src 'self' ipc: http://ipc.localhost http://127.0.0.1:*",
	} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP %q missing %q", csp, directive)
		}
	}
}

func TestWriteOriginProtection(t *testing.T) {
	_, h := newServer(t)

	evil := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:2525/api/init", strings.NewReader(`{"prefix":"WEB"}`))
	evil.Header.Set("Origin", "https://evil.example")
	evilW := httptest.NewRecorder()
	h.ServeHTTP(evilW, evil)
	if evilW.Code != http.StatusForbidden {
		t.Fatalf("cross-origin write status = %d, want 403", evilW.Code)
	}
	rebinding := httptest.NewRequest(http.MethodPost, "http://evil.example:2525/api/init", strings.NewReader(`{"prefix":"WEB"}`))
	rebinding.Header.Set("Origin", "http://evil.example:2525")
	rebindingW := httptest.NewRecorder()
	h.ServeHTTP(rebindingW, rebinding)
	if rebindingW.Code != http.StatusForbidden {
		t.Fatalf("DNS-rebinding write status = %d, want 403", rebindingW.Code)
	}
	missingOrigin := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:2525/api/init", strings.NewReader(`{"prefix":"WEB"}`))
	missingOrigin.Header.Set("Sec-Fetch-Site", "cross-site")
	missingOriginW := httptest.NewRecorder()
	h.ServeHTTP(missingOriginW, missingOrigin)
	if missingOriginW.Code != http.StatusForbidden {
		t.Fatalf("cross-site write without Origin status = %d, want 403", missingOriginW.Code)
	}

	sameOrigin := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:2525/api/init", strings.NewReader(`{"prefix":"WEB"}`))
	sameOrigin.Header.Set("Origin", "http://127.0.0.1:2525")
	sameOriginW := httptest.NewRecorder()
	h.ServeHTTP(sameOriginW, sameOrigin)
	if sameOriginW.Code != http.StatusOK {
		t.Fatalf("same-origin write status = %d, want 200; body=%s", sameOriginW.Code, sameOriginW.Body.String())
	}

	dev := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:2525/api/init", strings.NewReader(`{"prefix":"WEB"}`))
	dev.Header.Set("Origin", "http://localhost:5173")
	devW := httptest.NewRecorder()
	h.ServeHTTP(devW, dev)
	if devW.Code == http.StatusForbidden {
		t.Fatalf("loopback Vite origin was rejected: %s", devW.Body.String())
	}

	remoteReq := httptest.NewRequest(http.MethodPost, "http://carbon.lan:2525/api/init", nil)
	if !allowedWriteOrigin(remoteReq, "http://carbon.lan:2525", true) {
		t.Fatal("explicit remote mode rejected a same-origin remote host")
	}
}

func TestJSONBodyLimit(t *testing.T) {
	_, h := newServer(t)
	body := `{"prefix":"` + strings.Repeat("x", int(maxJSONBodyBytes)) + `"}`
	if code, response := raw(h, "POST", "/api/init", body); code != http.StatusRequestEntityTooLarge || !strings.Contains(response, "exceeds") {
		t.Fatalf("oversized JSON = %d %s, want 413 body limit", code, response)
	}
}

func TestRequestBodyLimitAlsoProtectsMCP(t *testing.T) {
	s, h := newServer(t)
	body := strings.Repeat("x", int(maxJSONBodyBytes)+1)
	path := "/mcp?actor=agent:test&repo=" + url.QueryEscape(s.defaultRoot)
	if code, response := raw(h, "POST", path, body); code != http.StatusRequestEntityTooLarge || !strings.Contains(response, "exceeds") {
		t.Fatalf("oversized MCP body = %d %s, want 413 body limit", code, response)
	}
}

func TestResolveRootCanonicalizesExplicitProjectSymlink(t *testing.T) {
	s, _ := newServer(t)
	target := t.TempDir()
	link := filepath.Join(s.defaultRoot, "escape")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	got, err := s.resolveRoot(link)
	if err != nil {
		t.Fatalf("resolveRoot rejected an explicitly selected project: %v", err)
	}
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolveRoot = %q, want canonical target %q", got, want)
	}
}

func TestMCPEndpointValidation(t *testing.T) {
	s, h := newServer(t)
	// Missing ?actor -> 400 with a helpful message.
	if code, body := raw(h, "POST", "/mcp?repo="+s.defaultRoot, `{}`); code != http.StatusBadRequest || !strings.Contains(body, "actor") {
		t.Fatalf("missing actor = %d %s, want 400 mentioning actor", code, body)
	}
	if code, body := raw(h, "POST", "/mcp?actor=%0A%0D&repo="+url.QueryEscape(s.defaultRoot), `{}`); code != http.StatusBadRequest || !strings.Contains(body, "actor") {
		t.Fatalf("invalid actor = %d %s, want 400 mentioning actor", code, body)
	}
	// Unknown ?repo -> 400.
	if code, _ := raw(h, "POST", "/mcp?actor=agent:x&repo=/no/such/dir", `{}`); code != http.StatusBadRequest {
		t.Fatalf("bad repo, want 400")
	}
}

func raw(h http.Handler, method, path, body string) (int, string) {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func call(t *testing.T, h http.Handler, method, path, body string, out any) {
	t.Helper()
	code, b := raw(h, method, path, body)
	if code != http.StatusOK {
		t.Fatalf("%s %s -> %d: %s", method, path, code, b)
	}
	if err := json.Unmarshal([]byte(b), out); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestUpdateFieldsAndStatusActor(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	if st.Actor != "human:test" {
		t.Fatalf("status actor = %q, want human:test", st.Actor)
	}
	var created taskDTO
	call(t, h, "POST", "/api/tasks", `{"title":"x"}`, &created)
	var updated taskDTO
	call(t, h, "POST", "/api/tasks/"+created.ID+"/update", `{"priority":"high","labels":["backend"]}`, &updated)
	if updated.Priority != "high" || len(updated.Labels) != 1 || updated.Labels[0] != "backend" {
		t.Fatalf("update not applied: %+v", updated)
	}
}

func TestUpdateValidationAndGetUpdatedAt(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	var created taskDTO
	call(t, h, "POST", "/api/tasks", `{"title":"x"}`, &created)

	// single-task GET carries updatedAt (regression: dtoFromDoc must set it)
	var got taskDTO
	call(t, h, "GET", "/api/tasks/"+created.ID, "", &got)
	if got.UpdatedAt == "" {
		t.Fatal("GET task missing updatedAt")
	}

	// create with a missing parent -> 422, not 500
	if code, body := raw(h, "POST", "/api/tasks", `{"title":"y","parent":"NOPE-1"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("missing parent status = %d, want 422; body=%s", code, body)
	}
	// invalid priority -> 422
	if code, _ := raw(h, "POST", "/api/tasks/"+created.ID+"/update", `{"priority":"ASAP"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid priority status = %d, want 422", code)
	}
}

func TestEditTaskTitleBodyChecks(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	var created taskDTO
	call(t, h, "POST", "/api/tasks", `{"title":"x","body":"old\n"}`, &created)

	var updated taskDTO
	call(t, h, "POST", "/api/tasks/"+created.ID+"/update",
		`{"title":"renamed","body":"new\n","checks":[{"desc":"tests","cmd":"go test ./..."}]}`, &updated)
	if updated.Title != "renamed" || updated.Body != "new\n" || len(updated.Checks) != 1 {
		t.Fatalf("edit not applied: %+v", updated)
	}

	// empty title -> 422
	if code, _ := raw(h, "POST", "/api/tasks/"+created.ID+"/update", `{"title":"  "}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("empty title status = %d, want 422", code)
	}
}

func TestHTTPTaskBlockerEvidenceAuditAndVersionConflict(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	var created taskDTO
	call(t, h, "POST", "/api/tasks", `{
		"title":"proof",
		"blockerReason":"waiting for credentials",
		"evidence":[{
			"kind":"git_commit","value":"abc123","label":"initial",
			"createdAt":"1999-01-01T00:00:00Z","createdBy":"agent:forged"
		}]
	}`, &created)
	if created.BlockerReason != "waiting for credentials" || len(created.Evidence) != 1 {
		t.Fatalf("created blocker/evidence = %+v", created)
	}
	first := created.Evidence[0]
	if first.ID == "" || first.CreatedAt == "1999-01-01T00:00:00Z" || first.CreatedBy != "human:test" {
		t.Fatalf("HTTP create allowed forged evidence audit: %+v", first)
	}

	var updated taskDTO
	updateBody := `{"expectedVersion":"` + created.Version + `","evidence":[{
		"id":"` + first.ID + `","kind":"git_commit","value":"def456","label":"edited",
		"createdAt":"1998-01-01T00:00:00Z","createdBy":"agent:forged"
	}]}`
	call(t, h, "POST", "/api/tasks/"+created.ID+"/update", updateBody, &updated)
	if len(updated.Evidence) != 1 || updated.Evidence[0].Value != "def456" || updated.Evidence[0].CreatedAt != first.CreatedAt || updated.Evidence[0].CreatedBy != first.CreatedBy {
		t.Fatalf("HTTP update changed existing evidence audit: %+v", updated.Evidence)
	}

	if code, body := raw(h, "POST", "/api/tasks/"+created.ID+"/update", `{"expectedVersion":"`+created.Version+`","blockerReason":"stale"}`); code != http.StatusConflict {
		t.Fatalf("stale evidence-adjacent update = %d, want 409: %s", code, body)
	}

	var cleared taskDTO
	call(t, h, "POST", "/api/tasks/"+created.ID+"/update", `{"expectedVersion":"`+updated.Version+`","blockerReason":"","evidence":[]}`, &cleared)
	if cleared.BlockerReason != "" || len(cleared.Evidence) != 0 {
		t.Fatalf("HTTP clear blocker/evidence = %+v", cleared)
	}
	if code, body := raw(h, "POST", "/api/tasks/"+created.ID+"/update", `{"expectedVersion":"`+cleared.Version+`","blockerReason":"bad\u0000reason"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid blocker reason = %d, want 422: %s", code, body)
	}
}

func TestDeleteTaskEndpoint(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	var parent, child taskDTO
	call(t, h, "POST", "/api/tasks", `{"title":"parent"}`, &parent)
	call(t, h, "POST", "/api/tasks", `{"title":"child","parent":"`+parent.ID+`"}`, &child)

	// parent blocked by child -> 422
	if code, _ := raw(h, "DELETE", "/api/tasks/"+parent.ID, ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("delete blocked status = %d, want 422", code)
	}
	// child deletes -> 200, then it's gone -> 404
	if code, b := raw(h, "DELETE", "/api/tasks/"+child.ID, ""); code != http.StatusOK {
		t.Fatalf("delete child = %d: %s", code, b)
	}
	if code, _ := raw(h, "GET", "/api/tasks/"+child.ID, ""); code != http.StatusNotFound {
		t.Fatalf("get deleted = %d, want 404", code)
	}
	// deleting a missing task -> 404
	if code, _ := raw(h, "DELETE", "/api/tasks/NOPE-1", ""); code != http.StatusNotFound {
		t.Fatalf("delete missing = %d, want 404", code)
	}
}

func TestEditAndDeleteNoteEndpoint(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	var created taskDTO
	call(t, h, "POST", "/api/tasks", `{"title":"x"}`, &created)
	var noted taskDTO
	call(t, h, "POST", "/api/tasks/"+created.ID+"/note", `{"text":"first"}`, &noted)
	noteID := noted.Provenance[len(noted.Provenance)-1].ID
	if noteID == "" {
		t.Fatal("note missing id")
	}

	var edited taskDTO
	call(t, h, "PATCH", "/api/tasks/"+created.ID+"/notes/"+noteID, `{"text":"edited"}`, &edited)
	last := edited.Provenance[len(edited.Provenance)-1]
	if last.Text != "edited" || last.EditedAt == "" {
		t.Fatalf("note not edited: %+v", last)
	}

	// editing a system entry (the created entry, index 0) by index -> 422
	if code, _ := raw(h, "PATCH", "/api/tasks/"+created.ID+"/notes/-?index=0", `{"text":"nope"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("edit system entry status = %d, want 422", code)
	}

	var deleted taskDTO
	call(t, h, "DELETE", "/api/tasks/"+created.ID+"/notes/"+noteID, "", &deleted)
	for _, p := range deleted.Provenance {
		if p.ID == noteID {
			t.Fatal("note not deleted")
		}
	}
	// deleting a missing note -> 404
	if code, _ := raw(h, "DELETE", "/api/tasks/"+created.ID+"/notes/n_missing", ""); code != http.StatusNotFound {
		t.Fatalf("delete missing note = %d, want 404", code)
	}
}

func TestReorderEndpoint(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	var created taskDTO
	call(t, h, "POST", "/api/tasks", `{"title":"x"}`, &created)
	var got taskDTO
	call(t, h, "POST", "/api/tasks/"+created.ID+"/reorder", `{"rank":2048}`, &got)
	if got.Rank != 2048 {
		t.Fatalf("rank = %v, want 2048", got.Rank)
	}
}

func TestPerRequestActor(t *testing.T) {
	_, h := newServer(t)
	var st statusResp
	call(t, h, "POST", "/api/init", `{"prefix":"WEB"}`, &st)
	if st.SuggestedActor == "" || !strings.HasPrefix(st.SuggestedActor, "human:") {
		t.Fatalf("suggestedActor = %q, want human:*", st.SuggestedActor)
	}

	// A write carrying the canonical X-Carbon-Actor is attributed to that actor.
	r := httptest.NewRequest("POST", "/api/tasks", strings.NewReader(`{"title":"x"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Carbon-Actor", "human:ali")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created taskDTO
	_ = json.Unmarshal(w.Body.Bytes(), &created)

	var got taskDTO
	call(t, h, "GET", "/api/tasks/"+created.ID, "", &got)
	if got.Provenance[0].Who != "human:ali" {
		t.Fatalf("provenance who = %q, want human:ali", got.Provenance[0].Who)
	}

	// No header -> falls back to the server default (human:test from newServer).
	var created2 taskDTO
	call(t, h, "POST", "/api/tasks", `{"title":"y"}`, &created2)
	var got2 taskDTO
	call(t, h, "GET", "/api/tasks/"+created2.ID, "", &got2)
	if got2.Provenance[0].Who != "human:test" {
		t.Fatalf("fallback who = %q, want human:test", got2.Provenance[0].Who)
	}
}

func TestActorForReadsLegacyCairnHeaderOnlyAsFallback(t *testing.T) {
	s, _ := newServer(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Cairn-Actor", "human:legacy")
	if got := s.actorFor(r); got != "human:legacy" {
		t.Fatalf("legacy header actor = %q, want human:legacy", got)
	}
	r.Header.Set("X-Carbon-Actor", "human:canonical")
	if got := s.actorFor(r); got != "human:canonical" {
		t.Fatalf("canonical header did not take precedence: %q", got)
	}
}

func TestSanitizeActor(t *testing.T) {
	if got := sanitizeActor("  human:shahram  "); got != "human:shahram" {
		t.Fatalf("trim = %q", got)
	}
	if got := sanitizeActor("human:ali\ninjected: true"); strings.ContainsAny(got, "\n\r") {
		t.Fatalf("newline not stripped: %q", got)
	}
	if got := sanitizeActor("   "); got != "" {
		t.Fatalf("empty = %q, want \"\"", got)
	}
}
