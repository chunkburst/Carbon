package connect

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"

	"carbon/internal/compat"
)

func TestMcpServersJSONUpsertPreservesAndIsIdempotent(t *testing.T) {
	existing := []byte(`{
  "mcpServers": { "other": { "command": "x" } },
  "extra": true
}`)
	cfg := serverConfig{Name: "carbon", Bin: "/abs/carbon", Args: []string{"serve", "--repo", "/r"}}

	out, err := (mcpServersJSON{}).upsert(existing, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	if root["extra"] != true {
		t.Error("top-level key not preserved")
	}
	servers := root["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Error("sibling server not preserved")
	}
	carbon := servers["carbon"].(map[string]any)
	if carbon["command"] != "/abs/carbon" {
		t.Errorf("command = %v", carbon["command"])
	}
	if !(mcpServersJSON{}).connected(out, "/r") {
		t.Error("connected should be true after upsert")
	}

	// Idempotent: a second upsert yields identical bytes.
	out2, err := (mcpServersJSON{}).upsert(out, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(out2) {
		t.Error("upsert not idempotent")
	}
}

func TestMcpServersJSONUpsertEmpty(t *testing.T) {
	out, err := (mcpServersJSON{}).upsert(nil, serverConfig{Name: "carbon", Bin: "/c", Args: []string{"serve", "--repo", "/r"}})
	if err != nil {
		t.Fatal(err)
	}
	if !(mcpServersJSON{}).connected(out, "/r") {
		t.Errorf("fresh file should be connected:\n%s", out)
	}
}

func TestFormatsReadLegacyCairnKeyAndMigrateToCanonicalCarbonKey(t *testing.T) {
	cfg := serverConfig{Name: legacyServerName, Bin: "/abs/carbon", Args: []string{"serve", "--repo", "/r"}}
	cases := []struct {
		name      string
		format    format
		legacy    []byte
		canonical func(t *testing.T, data []byte)
	}{
		{
			name:   "mcpServers JSON",
			format: mcpServersJSON{},
			legacy: []byte(`{"mcpServers":{"cairn":{"command":"/old/carbon","args":["serve","--repo","/r"]},"other":{"command":"keep"}}}`),
			canonical: func(t *testing.T, data []byte) {
				t.Helper()
				var root map[string]any
				if err := json.Unmarshal(data, &root); err != nil {
					t.Fatal(err)
				}
				servers := root["mcpServers"].(map[string]any)
				if _, ok := servers[legacyServerName]; ok {
					t.Fatalf("legacy Cairn key remained: %s", data)
				}
				if _, ok := servers[serverName]; !ok {
					t.Fatalf("canonical Carbon key missing: %s", data)
				}
				if _, ok := servers["other"]; !ok {
					t.Fatalf("sibling entry was removed: %s", data)
				}
			},
		},
		{
			name:   "OpenCode JSON",
			format: openCodeJSON{},
			legacy: []byte(`{"mcp":{"cairn":{"type":"local","command":["/old/carbon","serve","--repo","/r"]},"other":{"type":"local","command":["keep"]}}}`),
			canonical: func(t *testing.T, data []byte) {
				t.Helper()
				var root map[string]any
				if err := json.Unmarshal(data, &root); err != nil {
					t.Fatal(err)
				}
				servers := root["mcp"].(map[string]any)
				if _, ok := servers[legacyServerName]; ok {
					t.Fatalf("legacy Cairn key remained: %s", data)
				}
				if _, ok := servers[serverName]; !ok {
					t.Fatalf("canonical Carbon key missing: %s", data)
				}
				if _, ok := servers["other"]; !ok {
					t.Fatalf("sibling entry was removed: %s", data)
				}
			},
		},
		{
			name:   "Codex TOML",
			format: codexTOML{},
			legacy: []byte("[mcp_servers.cairn]\ncommand = \"/old/carbon\"\nargs = [\"serve\", \"--repo\", \"/r\"]\n\n[mcp_servers.other]\ncommand = \"keep\"\n"),
			canonical: func(t *testing.T, data []byte) {
				t.Helper()
				var root map[string]any
				if err := toml.Unmarshal(data, &root); err != nil {
					t.Fatal(err)
				}
				servers := root["mcp_servers"].(map[string]any)
				if _, ok := servers[legacyServerName]; ok {
					t.Fatalf("legacy Cairn key remained: %s", data)
				}
				if _, ok := servers[serverName]; !ok {
					t.Fatalf("canonical Carbon key missing: %s", data)
				}
				if _, ok := servers["other"]; !ok {
					t.Fatalf("sibling entry was removed: %s", data)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.format.connected(tc.legacy, "/r") {
				t.Fatalf("legacy Cairn config was not read as a compatibility fallback: %s", tc.legacy)
			}
			out, err := tc.format.upsert(tc.legacy, cfg)
			if err != nil {
				t.Fatal(err)
			}
			tc.canonical(t, out)
		})
	}
}

func TestConnectMigratesLegacyCairnKeyWithoutDuplicate(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"mcpServers":{"cairn":{"command":"/old/carbon","args":["serve","--repo","/old"]},"other":{"command":"keep"}}}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := connectWith(stubSys(t.TempDir()), "/abs/carbon", "cursor", repo, "agent:test"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(got, &root); err != nil {
		t.Fatal(err)
	}
	servers := root["mcpServers"].(map[string]any)
	if _, ok := servers[legacyServerName]; ok {
		t.Fatalf("legacy Cairn key remained after Connect: %s", got)
	}
	if _, ok := servers[serverName]; !ok {
		t.Fatalf("canonical Carbon key missing after Connect: %s", got)
	}
	if _, err := os.ReadFile(path + ".bak"); err != nil {
		t.Fatalf("legacy config backup missing: %v", err)
	}
}

func TestCanonicalCarbonConnectKeyTakesPrecedenceOverLegacyCairnKey(t *testing.T) {
	config := []byte(`{"mcpServers":{"carbon":{"command":"/canonical/carbon","args":["serve","--repo","/canonical"]},"cairn":{"command":"/legacy/cairn","args":["serve","--repo","/legacy"]}}}`)
	format := mcpServersJSON{}
	if !format.connected(config, "/canonical") {
		t.Fatal("canonical Carbon key was not recognized")
	}
	if format.connected(config, "/legacy") {
		t.Fatal("legacy Cairn key overrode an existing canonical Carbon key")
	}
	out, err := format.upsert(config, newServerConfig("/new/carbon", "/canonical", "agent:test"))
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	servers := root["mcpServers"].(map[string]any)
	if _, exists := servers[legacyServerName]; exists {
		t.Fatalf("legacy key remained after canonical rewrite: %s", out)
	}
	if _, exists := servers[serverName]; !exists {
		t.Fatalf("canonical key was lost after rewrite: %s", out)
	}
}

func TestOpenCodeJSONUpsert(t *testing.T) {
	cfg := serverConfig{Name: "carbon", Bin: "/abs/carbon", Args: []string{"serve", "--repo", "/r"}}
	out, err := (openCodeJSON{}).upsert([]byte(`{"mcp":{"keep":{"type":"local"}}}`), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	if root["$schema"] != "https://opencode.ai/config.json" {
		t.Errorf("schema = %v", root["$schema"])
	}
	mcp := root["mcp"].(map[string]any)
	if _, ok := mcp["keep"]; !ok {
		t.Error("sibling not preserved")
	}
	carbon := mcp["carbon"].(map[string]any)
	if carbon["type"] != "local" || carbon["enabled"] != true {
		t.Errorf("carbon entry = %v", carbon)
	}
	cmd := carbon["command"].([]any)
	if len(cmd) != 4 || cmd[0] != "/abs/carbon" {
		t.Errorf("command argv = %v", cmd)
	}
	if !(openCodeJSON{}).connected(out, "/r") {
		t.Error("connected should be true")
	}
}

func TestCodexTOMLUpsertPreserves(t *testing.T) {
	existing := []byte("model = \"o1\"\n\n[mcp_servers.other]\ncommand = \"x\"\n")
	cfg := serverConfig{Name: "carbon", Bin: "/abs/carbon", Args: []string{"serve", "--repo", "/r"}}
	out, err := (codexTOML{}).upsert(existing, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := toml.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	if root["model"] != "o1" {
		t.Error("top-level key not preserved")
	}
	servers := root["mcp_servers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Error("sibling table not preserved")
	}
	carbon := servers["carbon"].(map[string]any)
	if carbon["command"] != "/abs/carbon" {
		t.Errorf("command = %v", carbon["command"])
	}
	if !(codexTOML{}).connected(out, "/r") {
		t.Error("connected should be true")
	}
}

func TestFormatsConnectedOnlyForBoundRepo(t *testing.T) {
	repo := t.TempDir()
	otherRepo := t.TempDir()
	// An old desktop sidecar can have an arbitrary absolute path. Detection must inspect
	// its serialized arguments without trying to execute or resolve that binary.
	cfg := serverConfig{
		Name: serverName,
		Bin:  filepath.Join(t.TempDir(), "old-carbon-sidecar"),
		Args: []string{"serve", "--actor", "agent:codex-2", "--client", "codex", "--repo", repo},
	}
	cases := []struct {
		name   string
		format format
	}{
		{name: "mcpServers JSON", format: mcpServersJSON{}},
		{name: "Codex TOML", format: codexTOML{}},
		{name: "OpenCode JSON", format: openCodeJSON{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.format.upsert(nil, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.format.connected(out, repo) {
				t.Errorf("custom actor and --client should still match %s:\n%s", repo, out)
			}
			if tc.format.connected(out, otherRepo) {
				t.Errorf("entry for %s must not match %s:\n%s", repo, otherRepo, out)
			}
		})
	}
}

func TestFormatsConnectedRejectsMalformedOrRemoteEntries(t *testing.T) {
	repo := t.TempDir()
	missingRepo := serverConfig{Name: serverName, Bin: "/abs/carbon", Args: []string{"serve", "--actor", "agent:codex-2"}}
	missingRepoValue := serverConfig{Name: serverName, Bin: "/abs/carbon", Args: []string{"serve", "--repo"}}

	for _, tc := range []struct {
		name   string
		format format
		config []byte
	}{
		{
			name:   "mcpServers missing --repo",
			format: mcpServersJSON{},
			config: mustUpsert(t, mcpServersJSON{}, missingRepo),
		},
		{
			name:   "Codex missing --repo",
			format: codexTOML{},
			config: mustUpsert(t, codexTOML{}, missingRepo),
		},
		{
			name:   "OpenCode missing --repo",
			format: openCodeJSON{},
			config: mustUpsert(t, openCodeJSON{}, missingRepo),
		},
		{
			name:   "mcpServers missing --repo value",
			format: mcpServersJSON{},
			config: mustUpsert(t, mcpServersJSON{}, missingRepoValue),
		},
		{
			name:   "mcpServers HTTP",
			format: mcpServersJSON{},
			config: []byte(`{"mcpServers":{"carbon":{"url":"http://127.0.0.1:2525/mcp"}}}`),
		},
		{
			name:   "OpenCode remote",
			format: openCodeJSON{},
			config: []byte(`{"mcp":{"carbon":{"type":"remote","url":"http://127.0.0.1:2525/mcp"}}}`),
		},
		{
			name:   "unrelated carbon key",
			format: mcpServersJSON{},
			config: []byte(`{"mcpServers":{"carbon":{"command":"other","args":["start"]}}}`),
		},
		{
			name:   "malformed JSON",
			format: mcpServersJSON{},
			config: []byte(`{"mcpServers":`),
		},
		{
			name:   "malformed TOML",
			format: codexTOML{},
			config: []byte(`[mcp_servers.carbon`),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.format.connected(tc.config, repo) {
				t.Errorf("invalid entry reported connected:\n%s", tc.config)
			}
		})
	}
}

func TestDetectRequiresEntryBoundToCurrentRepo(t *testing.T) {
	repo := t.TempDir()
	otherRepo := t.TempDir()
	path := filepath.Join(repo, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	wrong, err := (mcpServersJSON{}).upsert(nil, serverConfig{
		Name: serverName,
		Bin:  filepath.Join(t.TempDir(), "old-carbon-sidecar"),
		Args: []string{"serve", "--actor", "agent:codex-2", "--repo", otherRepo},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, wrong, 0o600); err != nil {
		t.Fatal(err)
	}
	if statusForID(t, detectWith(stubSys(t.TempDir()), repo), "cursor").Connected {
		t.Error("a carbon entry for another repo must not be reported connected")
	}

	right, err := (mcpServersJSON{}).upsert(nil, serverConfig{
		Name: serverName,
		Bin:  filepath.Join(t.TempDir(), "old-carbon-sidecar"),
		Args: []string{"serve", "--actor", "agent:codex-2", "--repo", repo},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, right, 0o600); err != nil {
		t.Fatal(err)
	}
	if !statusForID(t, detectWith(stubSys(t.TempDir()), repo), "cursor").Connected {
		t.Error("a local carbon entry for the current repo should be connected")
	}
}

func TestSameRepoResolvesSymlinksWhenAvailable(t *testing.T) {
	repo := t.TempDir()
	link := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(repo, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating test symlink is not permitted: %v", err)
		}
		t.Fatal(err)
	}
	if !sameRepo(link, repo) {
		t.Errorf("symlinked repo %s should match %s", link, repo)
	}
}

func TestConnectedMatchesWindowsRepoCase(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows paths are case-insensitive only on Windows")
	}
	// Keep the final component absent so EvalSymlinks cannot normalize the casing for us.
	repo := filepath.Join(t.TempDir(), "not-created")
	out := mustUpsert(t, mcpServersJSON{}, serverConfig{
		Name: serverName,
		Bin:  `C:\Tools\carbon.exe`,
		Args: []string{"serve", "--repo", strings.ToUpper(repo)},
	})
	if !(mcpServersJSON{}).connected(out, repo) {
		t.Errorf("case-insensitive Windows repo paths should match:\n%s", out)
	}
}

func mustUpsert(t *testing.T, f format, cfg serverConfig) []byte {
	t.Helper()
	out, err := f.upsert(nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func statusForID(t *testing.T, statuses []AgentStatus, id string) AgentStatus {
	t.Helper()
	for _, status := range statuses {
		if status.ID == id {
			return status
		}
	}
	t.Fatalf("agent %q not found", id)
	return AgentStatus{}
}

// stubSys builds a sys rooted at a temp HOME with the given binaries "on PATH".
func stubSys(home string, onPath ...string) sys {
	set := map[string]bool{}
	for _, b := range onPath {
		set[b] = true
	}
	return sys{home: home, lookPath: func(b string) (string, error) {
		if set[b] {
			return "/usr/bin/" + b, nil
		}
		return "", os.ErrNotExist
	}}
}

func TestConnectWritesBackupAndVerifies(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	s := stubSys(home)

	// First connect: creates .cursor/mcp.json, no backup (nothing pre-existed).
	path, err := connectWith(s, "/abs/carbon", "cursor", repo, "agent:test")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(repo, ".cursor", "mcp.json"); path != want {
		t.Errorf("path = %s want %s", path, want)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Error("backup should not exist on first write")
	}
	b, _ := os.ReadFile(path)
	if !(mcpServersJSON{}).connected(b, repo) {
		t.Errorf("config not connected:\n%s", b)
	}
	var root map[string]any
	json.Unmarshal(b, &root)
	args := root["mcpServers"].(map[string]any)["carbon"].(map[string]any)["args"].([]any)
	if args[len(args)-1] != repo {
		t.Errorf("--repo arg = %v want %s", args[len(args)-1], repo)
	}

	// Second connect: overwrites, so a .bak of the prior content is kept.
	if _, err := connectWith(s, "/abs/carbon", "cursor", repo, "agent:test2"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Errorf("backup missing after overwrite: %v", err)
	}
}

func TestWriteFileAtomicBackupDoesNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	backup := path + ".bak"
	external := filepath.Join(t.TempDir(), "outside.json")
	old := []byte(`{"mcpServers":{"other":{"command":"old"}}}`)
	next := []byte(`{"mcpServers":{"carbon":{"command":"new"}}}`)
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(external, []byte("do not overwrite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, backup); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating test symlink is not permitted: %v", err)
		}
		t.Fatal(err)
	}

	if err := writeFileAtomic(dir, path, old, next); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(external); err != nil {
		t.Fatal(err)
	} else if string(got) != "do not overwrite" {
		t.Errorf("external file was overwritten through backup symlink: %q", got)
	}
	if info, err := os.Lstat(backup); err != nil {
		t.Fatal(err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		t.Error("backup symlink was not replaced")
	}
	if got, err := os.ReadFile(backup); err != nil {
		t.Fatal(err)
	} else if string(got) != string(old) {
		t.Errorf("backup = %q, want %q", got, old)
	}
	if got, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	} else if string(got) != string(next) {
		t.Errorf("config = %q, want %q", got, next)
	}
}

func TestConnectRejectsSymlinkedConfigParent(t *testing.T) {
	repo := t.TempDir()
	external := t.TempDir()
	linkConfigParentOrSkip(t, external, filepath.Join(repo, ".cursor"))

	_, err := connectWith(stubSys(t.TempDir()), "/abs/carbon", "cursor", repo, "agent:test")
	if !errors.Is(err, ErrUnsafeConfigPath) {
		t.Fatalf("connect through symlinked config parent = %v, want ErrUnsafeConfigPath", err)
	}
	assertDirEmpty(t, external)
}

func TestConnectCarbonRejectsSymlinkedConfigParent(t *testing.T) {
	repo := t.TempDir()
	external := t.TempDir()
	linkConfigParentOrSkip(t, external, filepath.Join(repo, ".kilocode"))
	cfg, err := carbonServerConfig("/abs/carbon", "kilo", "agent:test", CarbonScope{Home: "/carbon/home", ClusterID: "cluster-a", ScopeMode: CarbonScopeModeCluster})
	if err != nil {
		t.Fatal(err)
	}

	_, err = connectWithConfig(stubSys(t.TempDir()), "kilo", repo, cfg)
	if !errors.Is(err, ErrUnsafeConfigPath) {
		t.Fatalf("ConnectCarbon path through symlinked config parent = %v, want ErrUnsafeConfigPath", err)
	}
	assertDirEmpty(t, external)
}

func TestDetectAndDisconnectRejectSymlinkedConfigParent(t *testing.T) {
	repo := t.TempDir()
	external := t.TempDir()
	linkConfigParentOrSkip(t, external, filepath.Join(repo, ".cursor"))
	path := filepath.Join(external, "mcp.json")
	seed := mustUpsert(t, mcpServersJSON{}, newServerConfig("/abs/carbon", repo, "agent:test"))
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatal(err)
	}

	if got := statusForID(t, detectWith(stubSys(t.TempDir()), repo), "cursor"); got.Connected {
		t.Fatal("Detect followed a symlinked config parent outside the selected repo")
	}
	if _, err := Disconnect("cursor", repo); !errors.Is(err, ErrUnsafeConfigPath) {
		t.Fatalf("Disconnect through symlinked config parent = %v, want ErrUnsafeConfigPath", err)
	}
	if got, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	} else if string(got) != string(seed) {
		t.Fatalf("external config changed through Disconnect: %q", got)
	}
}

func linkConfigParentOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	} else if runtime.GOOS != "windows" {
		t.Fatalf("create config parent symlink: %v", err)
	}
	// On Windows, directory symlinks may require Developer Mode or elevation. A junction
	// exercises the same reparse-point defense without that privilege when available.
	command := "mklink /J \"" + link + "\" \"" + target + "\""
	if err := exec.Command("cmd.exe", "/d", "/c", command).Run(); err != nil {
		t.Skipf("creating a directory symlink/junction is not permitted: %v", err)
	}
}

func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("external config directory was modified: %v", entries)
	}
}

func TestConnectAutoAgentsWriteExpectedPaths(t *testing.T) {
	cases := map[string]string{
		"kilo": ".kilocode/mcp.json", // project-scoped, standard mcpServers
		"pi":   ".mcp.json",          // Pi's preferred project config (shared with Claude)
	}
	for agent, rel := range cases {
		repo := t.TempDir()
		path, err := connectWith(stubSys(t.TempDir()), "/abs/carbon", agent, repo, "agent:test")
		if err != nil {
			t.Fatalf("%s: %v", agent, err)
		}
		if want := filepath.Join(repo, rel); path != want {
			t.Errorf("%s path = %s want %s", agent, path, want)
		}
		b, _ := os.ReadFile(path)
		if !(mcpServersJSON{}).connected(b, repo) {
			t.Errorf("%s config not connected:\n%s", agent, b)
		}
	}
}

func TestConnectDefaultsToPerAgentIdentity(t *testing.T) {
	repo := t.TempDir()
	// Empty actor 鈫?the agent gets its own identity (agent:cursor), not a human/default name.
	path, err := connectWith(stubSys(t.TempDir()), "/abs/carbon", "cursor", repo, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var root map[string]any
	json.Unmarshal(b, &root)
	args := root["mcpServers"].(map[string]any)["carbon"].(map[string]any)["args"].([]any)
	// args = [serve --actor agent:cursor --repo <repo>]
	if args[2] != "agent:cursor" {
		t.Errorf("default actor = %v, want agent:cursor", args[2])
	}
}

func TestDisconnectRemovesOnlyCarbonAndKeepsSiblings(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// A config with carbon plus another server.
	seed := []byte(`{"mcpServers":{"carbon":{"command":"/c"},"other":{"command":"x"}}}`)
	if err := os.WriteFile(path, seed, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Disconnect("cursor", repo)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Errorf("path = %s want %s", got, path)
	}
	b, _ := os.ReadFile(path)
	if (mcpServersJSON{}).has(b) {
		t.Errorf("carbon entry should be gone:\n%s", b)
	}
	var root map[string]any
	json.Unmarshal(b, &root)
	if _, ok := root["mcpServers"].(map[string]any)["other"]; !ok {
		t.Errorf("sibling server should be preserved:\n%s", b)
	}
	// Idempotent: disconnecting again is a no-op (not an error).
	if _, err := Disconnect("cursor", repo); err != nil {
		t.Errorf("second disconnect errored: %v", err)
	}
}

func TestDisconnectMissingFileIsNoop(t *testing.T) {
	if _, err := Disconnect("cursor", t.TempDir()); err != nil {
		t.Errorf("disconnect with no config should be a no-op, got %v", err)
	}
}

func TestConnectRejectsManualAgent(t *testing.T) {
	if _, err := connectWith(stubSys(t.TempDir()), "/c", "antigravity", t.TempDir(), ""); err == nil {
		t.Error("expected error connecting a manual-only agent")
	}
}

func TestDetectMarksInstalledAndSortsFirst(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	// Cursor "installed" via ~/.cursor; others not.
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := detectWith(stubSys(home), repo)
	if len(out) == 0 {
		t.Fatal("no agents")
	}
	if !out[0].Installed {
		t.Errorf("first agent should be installed, got %+v", out[0])
	}
	byID := map[string]AgentStatus{}
	for _, a := range out {
		byID[a.ID] = a
	}
	if !byID["cursor"].Installed {
		t.Error("cursor should be detected as installed")
	}
	if byID["claude"].Installed {
		t.Error("claude should not be installed in this stub")
	}
}

func TestManualGuideRendersSnippet(t *testing.T) {
	g, err := ManualGuide("codex", "/my/repo", "agent:x")
	if err != nil {
		t.Fatal(err)
	}
	if g.Lang != "toml" {
		t.Errorf("lang = %s", g.Lang)
	}
	if !(codexTOML{}).connected([]byte(g.Config), "/my/repo") {
		t.Errorf("guide snippet missing carbon entry:\n%s", g.Config)
	}
}

func TestCarbonServerConfigRequiresProjectByDefault(t *testing.T) {
	_, err := carbonServerConfig("/abs/carbon", "codex", "", CarbonScope{Home: t.TempDir(), ClusterID: "cluster-a"})
	if err == nil || !strings.Contains(err.Error(), "requires a project id") {
		t.Fatalf("empty Carbon project scope = %v, want explicit-scope rejection", err)
	}
}

func TestCarbonServerConfigAllowsExplicitClusterScope(t *testing.T) {
	home := t.TempDir()
	if CarbonCompatLayer != compat.StableLayer {
		t.Fatalf("Carbon generated compatibility layer = %q, want compat.StableLayer %q", CarbonCompatLayer, compat.StableLayer)
	}
	if CarbonCompatLayer != "v2" {
		t.Fatalf("Carbon generated compatibility layer = %q, want approved stable v2", CarbonCompatLayer)
	}
	cluster, err := carbonServerConfig("/abs/carbon", "codex", "", CarbonScope{
		Home: home, ClusterID: "cluster-a", ScopeMode: CarbonScopeModeCluster,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range cluster.Args {
		if arg == "--project" {
			t.Fatalf("cluster-scoped config unexpectedly pinned a project: %#v", cluster.Args)
		}
	}
	if !strings.Contains(strings.Join(cluster.Args, " "), "--home "+home+" --cluster cluster-a") {
		t.Fatalf("cluster args = %#v", cluster.Args)
	}
	if !strings.Contains(strings.Join(cluster.Args, " "), "--compat-layer "+CarbonCompatLayer) {
		t.Fatalf("Carbon compat layer missing: %#v", cluster.Args)
	}
	if _, err := carbonServerConfig("/abs/carbon", "codex", "", CarbonScope{
		Home: home, ClusterID: "cluster-a", AllowClusterScope: true,
	}); err != nil {
		t.Fatalf("explicit AllowClusterScope compatibility opt-in = %v", err)
	}

	project, err := carbonServerConfig("/abs/carbon", "codex", "", CarbonScope{
		Home: home, ClusterID: "cluster-a", ProjectID: "project-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(project.Args, " "), "--project project-a") {
		t.Fatalf("project args = %#v", project.Args)
	}
	for _, arg := range newServerConfig("/abs/carbon", t.TempDir(), "agent:legacy").Args {
		if arg == "--compat-layer" {
			t.Fatalf("legacy --repo config unexpectedly gained Carbon compatibility flag")
		}
	}
}

func TestCarbonServerConfigAllowsStandaloneProjectWithoutCluster(t *testing.T) {
	home := t.TempDir()
	scope := CarbonScope{Home: home, ProjectID: "project-standalone"}
	cfg, err := carbonServerConfig("/abs/carbon", "codex", "", scope)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(cfg.Args, " ")
	if strings.Contains(args, "--cluster") || !strings.Contains(args, "--home "+home+" --project project-standalone") {
		t.Fatalf("standalone args = %#v", cfg.Args)
	}
	for _, format := range []format{mcpServersJSON{}, codexTOML{}, openCodeJSON{}} {
		out := mustUpsert(t, format, cfg)
		if !format.connectedCarbon(out, scope) {
			t.Fatalf("standalone config was not exact-match connected:\n%s", out)
		}
		if format.connectedCarbon(out, CarbonScope{Home: home, ClusterID: "cluster-a", ProjectID: scope.ProjectID}) {
			t.Fatalf("standalone config matched a shared-cluster scope:\n%s", out)
		}
	}
	if _, err := carbonServerConfig("/abs/carbon", "codex", "", CarbonScope{Home: home, ScopeMode: CarbonScopeModeStandalone}); err == nil {
		t.Fatal("standalone mode without project unexpectedly succeeded")
	}
}

func TestCarbonServerConfigAllowsProjectSessionScope(t *testing.T) {
	home := t.TempDir()
	scope := CarbonScope{Home: home, ScopeMode: CarbonScopeModeSession}
	cfg, err := carbonServerConfig("/abs/carbon", "codex", "agent:session", scope)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{
		"serve", "--actor", "agent:session",
		"--home", home,
		"--project-session",
		"--compat-layer", CarbonCompatLayer,
	}
	if !reflect.DeepEqual(cfg.Args, wantArgs) {
		t.Fatalf("project-session args = %#v, want %#v", cfg.Args, wantArgs)
	}
	for _, arg := range cfg.Args {
		if arg == "--cluster" || arg == "--project" {
			t.Fatalf("project-session config serialized a pinned scope: %#v", cfg.Args)
		}
	}
	for _, format := range []format{mcpServersJSON{}, codexTOML{}, openCodeJSON{}} {
		out := mustUpsert(t, format, cfg)
		if !format.connectedCarbon(out, scope) {
			t.Fatalf("project-session config was not detected:\n%s", out)
		}
		if format.connectedCarbon(out, CarbonScope{Home: t.TempDir(), ScopeMode: CarbonScopeModeSession}) {
			t.Fatalf("project-session config matched another Carbon home:\n%s", out)
		}
		if format.connectedCarbon(out, CarbonScope{Home: home, ClusterID: "cluster-a", ProjectID: "project-a"}) {
			t.Fatalf("project-session config matched a pinned project scope:\n%s", out)
		}
		badSwitch := cfg
		badSwitch.Args = append([]string(nil), cfg.Args...)
		for i, arg := range badSwitch.Args {
			if arg == "--project-session" {
				badSwitch.Args[i] = "--project-session=true"
			}
		}
		if format.connectedCarbon(mustUpsert(t, format, badSwitch), scope) {
			t.Fatalf("non-canonical project-session switch was detected:\n%s", out)
		}
	}
	if _, err := carbonServerConfig("/abs/carbon", "codex", "", CarbonScope{
		Home: home, ClusterID: "cluster-a", ScopeMode: CarbonScopeModeSession,
	}); err == nil {
		t.Fatal("project-session scope with cluster id unexpectedly succeeded")
	}
	if _, err := carbonServerConfig("/abs/carbon", "codex", "", CarbonScope{
		Home: home, ProjectID: "project-a", ScopeMode: CarbonScopeModeSession,
	}); err == nil {
		t.Fatal("project-session scope with project id unexpectedly succeeded")
	}
}

func TestCarbonProjectSessionDetectAndDisconnectAreScopeBound(t *testing.T) {
	sourceRoot := t.TempDir()
	home := t.TempDir()
	scope := CarbonScope{Home: home, ScopeMode: CarbonScopeModeSession}
	path := filepath.Join(sourceRoot, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	s := stubSys(t.TempDir())
	if _, err := connectCarbonWith(s, "/abs/carbon", "cursor", sourceRoot, "agent:session", scope); err != nil {
		t.Fatal(err)
	}
	if !statusForID(t, detectCarbonWith(s, sourceRoot, scope), "cursor").Connected {
		t.Fatal("project-session connection was not detected")
	}
	pinned := CarbonScope{Home: home, ClusterID: "cluster-a", ProjectID: "project-a"}
	if statusForID(t, detectCarbonWith(s, sourceRoot, pinned), "cursor").Connected {
		t.Fatal("project-session connection matched a pinned project")
	}
	seed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disconnectCarbonWith(s, "cursor", sourceRoot, pinned); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	} else if string(got) != string(seed) {
		t.Fatalf("pinned disconnect changed a project-session config:\n got: %s\nwant: %s", got, seed)
	}
	if _, err := disconnectCarbonWith(s, "cursor", sourceRoot, scope); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	} else if (mcpServersJSON{}).has(got) {
		t.Fatalf("project-session disconnect left Carbon config:\n%s", got)
	}
}

func TestCarbonFormatsMatchExactScope(t *testing.T) {
	home := t.TempDir()
	scope := CarbonScope{Home: home, ClusterID: "cluster-a", ProjectID: "project-a"}
	cfg, err := carbonServerConfig("/abs/carbon", "codex", "", scope)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		format format
	}{
		{name: "mcpServers JSON", format: mcpServersJSON{}},
		{name: "Codex TOML", format: codexTOML{}},
		{name: "OpenCode JSON", format: openCodeJSON{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := mustUpsert(t, tc.format, cfg)
			if !tc.format.connectedCarbon(out, scope) {
				t.Fatalf("exact Carbon config was not connected:\n%s", out)
			}
			for _, wrong := range []struct {
				name  string
				scope CarbonScope
			}{
				{name: "other home", scope: CarbonScope{Home: t.TempDir(), ClusterID: scope.ClusterID, ProjectID: scope.ProjectID}},
				{name: "other cluster", scope: CarbonScope{Home: scope.Home, ClusterID: "cluster-b", ProjectID: scope.ProjectID}},
				{name: "other project", scope: CarbonScope{Home: scope.Home, ClusterID: scope.ClusterID, ProjectID: "project-b"}},
				{name: "explicit cluster", scope: CarbonScope{Home: scope.Home, ClusterID: scope.ClusterID, ScopeMode: CarbonScopeModeCluster}},
			} {
				t.Run(wrong.name, func(t *testing.T) {
					if tc.format.connectedCarbon(out, wrong.scope) {
						t.Fatalf("config for %#v incorrectly matched %#v", scope, wrong.scope)
					}
				})
			}

			for _, layer := range []string{"v1", "0.3", "0.4"} {
				badCompat := cfg
				badCompat.Args = append([]string(nil), cfg.Args...)
				for i, arg := range badCompat.Args {
					if arg == "--compat-layer" && i+1 < len(badCompat.Args) {
						badCompat.Args[i+1] = layer
					}
				}
				if tc.format.connectedCarbon(mustUpsert(t, tc.format, badCompat), scope) {
					t.Fatalf("Carbon config with non-canonical compatibility layer %q was reported connected", layer)
				}
			}
		})
	}
}

func TestCarbonDetectConnectAndDisconnectAreScopeBound(t *testing.T) {
	sourceRoot := t.TempDir()
	home := t.TempDir()
	scope := CarbonScope{Home: home, ClusterID: "cluster-a", ProjectID: "project-a"}
	path := filepath.Join(sourceRoot, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := []byte(`{"mcpServers":{"other":{"command":"other","args":["serve"]}},"keep":true}`)
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	s := stubSys(t.TempDir())
	gotPath, err := connectCarbonWith(s, "/abs/carbon", "cursor", sourceRoot, "agent:test", scope)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != path {
		t.Fatalf("connect path = %s, want %s", gotPath, path)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("Carbon connect did not preserve a backup: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !(mcpServersJSON{}).connectedCarbon(b, scope) {
		t.Fatalf("written config is not connected to the requested Carbon scope:\n%s", b)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	if _, ok := root["mcpServers"].(map[string]any)["other"]; !ok || root["keep"] != true {
		t.Fatalf("Carbon connect did not preserve sibling config: %s", b)
	}
	if !statusForID(t, detectCarbonWith(s, sourceRoot, scope), "cursor").Connected {
		t.Fatal("Carbon detect did not recognize the exact scope")
	}
	otherScope := scope
	otherScope.ClusterID = "cluster-b"
	if statusForID(t, detectCarbonWith(s, sourceRoot, otherScope), "cursor").Connected {
		t.Fatal("Carbon detect matched a config for another cluster")
	}
	if _, err := disconnectCarbonWith(s, "cursor", sourceRoot, scope); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if (mcpServersJSON{}).has(b) {
		t.Fatalf("Carbon disconnect left the matching carbon entry:\n%s", b)
	}
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	if _, ok := root["mcpServers"].(map[string]any)["other"]; !ok || root["keep"] != true {
		t.Fatalf("Carbon disconnect did not preserve sibling config: %s", b)
	}
}

func TestCarbonDisconnectLeavesOtherScopeEntryUntouched(t *testing.T) {
	sourceRoot := t.TempDir()
	home := t.TempDir()
	bound := CarbonScope{Home: home, ClusterID: "cluster-a", ProjectID: "project-a"}
	other := bound
	other.ProjectID = "project-b"
	cfg, err := carbonServerConfig("/abs/carbon", "cursor", "agent:test", other)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sourceRoot, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := mustUpsert(t, mcpServersJSON{}, cfg)
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := disconnectCarbonWith(stubSys(t.TempDir()), "cursor", sourceRoot, bound); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(seed) {
		t.Fatalf("disconnect for another scope changed config:\n got: %s\nwant: %s", got, seed)
	}
}

func TestCarbonConnectedMatchesWindowsHomeCase(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows paths are case-insensitive only on Windows")
	}
	home := filepath.Join(t.TempDir(), "not-created")
	scope := CarbonScope{Home: home, ClusterID: "cluster-a", ProjectID: "project-a"}
	cfg, err := carbonServerConfig(`C:\Tools\carbon.exe`, "codex", "", CarbonScope{
		Home: strings.ToUpper(home), ClusterID: scope.ClusterID, ProjectID: scope.ProjectID,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := mustUpsert(t, mcpServersJSON{}, cfg)
	if !(mcpServersJSON{}).connectedCarbon(out, scope) {
		t.Fatalf("case-insensitive Windows Carbon home paths should match:\n%s", out)
	}
}
