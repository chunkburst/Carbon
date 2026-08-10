package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"carbon/internal/backup"
	"carbon/internal/compat"
	"carbon/internal/home"
	"carbon/internal/repo"
)

func TestDefaultWebAddrIsLocalPort2525(t *testing.T) {
	if defaultWebAddr != "127.0.0.1:2525" {
		t.Fatalf("defaultWebAddr = %q, want 127.0.0.1:2525", defaultWebAddr)
	}
	if !isLoopbackAddr(defaultWebAddr) {
		t.Fatalf("defaultWebAddr %q is not loopback", defaultWebAddr)
	}
}

func TestParentWatchUsesCarbonEnvAndKeepsLegacyFallback(t *testing.T) {
	t.Setenv("CARBON_PARENT_WATCH", "")
	t.Setenv("CAIRN_PARENT_WATCH", "")
	if parentWatchEnabled() {
		t.Fatal("empty parent-watch environment unexpectedly enabled")
	}
	t.Setenv("CAIRN_PARENT_WATCH", "1")
	if !parentWatchEnabled() {
		t.Fatal("legacy parent-watch fallback was not read")
	}
	t.Setenv("CARBON_PARENT_WATCH", "1")
	if !parentWatchEnabled() {
		t.Fatal("canonical parent-watch environment was not read")
	}
}

func TestUsageMarksRemoteFlagDisabled(t *testing.T) {
	if !strings.Contains(usageText, "Carbon V1.0.0") {
		t.Fatalf("usage does not identify the V1.0.0 product release:\n%s", usageText)
	}
	if !strings.Contains(usageText, "--allow-remote (disabled)") {
		t.Fatalf("web usage does not mark --allow-remote disabled:\n%s", usageText)
	}
	if !strings.Contains(usageText, "SSH/VPN tunnel to 127.0.0.1") {
		t.Fatalf("web usage does not direct remote access through a tunnel:\n%s", usageText)
	}
	if !strings.Contains(usageText, "v1=frozen legacy, v2=approved Carbon stable") {
		t.Fatalf("usage does not state the stable compatibility contract:\n%s", usageText)
	}
	if !strings.Contains(usageText, "--project-session --home PATH") {
		t.Fatalf("usage does not document explicit project-session routing:\n%s", usageText)
	}
}

func TestRunServeProjectSessionRequiresExplicitCarbonV2HomeAndRejectsPinnedScopes(t *testing.T) {
	homeRoot := t.TempDir()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no home", args: []string{"--actor", "agent:test", "--project-session"}, want: "explicit --home"},
		{name: "legacy repo", args: []string{"--actor", "agent:test", "--project-session", "--home", homeRoot, "--repo", t.TempDir()}, want: "cannot be combined"},
		{name: "fixed project", args: []string{"--actor", "agent:test", "--project-session", "--home", homeRoot, "--project", "project-x"}, want: "cannot be combined"},
		{name: "fixed cluster", args: []string{"--actor", "agent:test", "--project-session", "--home", homeRoot, "--cluster", "cluster-x"}, want: "cannot be combined"},
		// Explicit empty fixed-scope values are also a mix. They are often emitted
		// by config generators, and accepting one would obscure which binding mode
		// the client actually requested.
		{name: "empty project flag", args: []string{"--actor", "agent:test", "--project-session", "--home", homeRoot, "--project", ""}, want: "cannot be combined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runServe(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runServe(%q) = %v, want %q", tc.args, err, tc.want)
			}
		})
	}

	err := runServe([]string{"--actor", "agent:test", "--project-session", "--home", homeRoot, "--compat-layer", compat.LegacyLayer})
	if !errors.Is(err, compat.ErrLayerScopeMismatch) {
		t.Fatalf("project-session Carbon v1 = %v, want ErrLayerScopeMismatch", err)
	}
}

func TestRunServeProjectSessionBuildsSelectableCarbonV2Server(t *testing.T) {
	homeRoot := t.TempDir()
	previous := runMCPStdio
	runMCPStdio = func(ctx context.Context, srv *mcpsdk.Server) error {
		clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
		serverSession, err := srv.Connect(ctx, serverTransport, nil)
		if err != nil {
			t.Fatalf("connect project-session server: %v", err)
		}
		defer serverSession.Close()
		client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "cli-project-session-test", Version: "0"}, nil)
		clientSession, err := client.Connect(ctx, clientTransport, nil)
		if err != nil {
			t.Fatalf("connect project-session client: %v", err)
		}
		defer clientSession.Close()
		tools, err := clientSession.ListTools(ctx, nil)
		if err != nil {
			t.Fatalf("list project-session tools: %v", err)
		}
		names := map[string]bool{}
		for _, tool := range tools.Tools {
			names[tool.Name] = true
		}
		if !names["select_project"] || !names["create_project"] || !names["create"] {
			t.Fatalf("project-session CLI server tools = %v, want select_project/create_project/create", names)
		}
		return io.EOF
	}
	t.Cleanup(func() { runMCPStdio = previous })

	if err := runServe([]string{"--actor", "agent:cli-session", "--client", "codex", "--project-session", "--home", homeRoot}); err != nil {
		t.Fatalf("runServe project-session = %v", err)
	}
}

func TestValidateWebAddrRejectsNonLoopback(t *testing.T) {
	for _, addr := range []string{":2525", "0.0.0.0:2525", "192.0.2.1:2525", "localhost.localdomain:2525"} {
		err := validateWebAddr(addr)
		if err == nil || !strings.Contains(err.Error(), "SSH/VPN tunnel") {
			t.Errorf("validateWebAddr(%q) = %v, want tunnel error", addr, err)
		}
	}
	for _, addr := range []string{"127.0.0.1:2525", "[::1]:2525", "localhost:2525"} {
		if err := validateWebAddr(addr); err != nil {
			t.Errorf("validateWebAddr(%q) = %v, want nil", addr, err)
		}
	}
}

func TestRunWebRejectsAllowRemoteBeforeListening(t *testing.T) {
	called := false
	previous := listenWeb
	listenWeb = func(string) (net.Listener, error) {
		called = true
		return nil, errors.New("listener must not be called")
	}
	t.Cleanup(func() { listenWeb = previous })

	err := runWeb([]string{"--allow-remote", "--addr", "127.0.0.1:0"})
	if err == nil || !strings.Contains(err.Error(), "disabled") || !strings.Contains(err.Error(), "SSH/VPN tunnel") {
		t.Fatalf("runWeb --allow-remote = %v, want disabled tunnel error", err)
	}
	if called {
		t.Fatal("runWeb --allow-remote opened a listener")
	}
}

func TestCommandCompatibilityDefaultsRespectFrozenLegacyAndStableCarbon(t *testing.T) {
	previous := version
	version = "17.3.9+portable"
	t.Cleanup(func() { version = previous })

	legacy, err := commandCompatibility("", true)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.ProductVersion != version || legacy.RequestedCompatLayer != compat.LegacyLayer || legacy.StableCompatLayer != compat.StableLayer {
		t.Fatalf("legacy contract = %+v, want build=%q, requested v1, stable v2", legacy, version)
	}

	carbon, err := commandCompatibility("", false)
	if err != nil {
		t.Fatal(err)
	}
	if carbon.RequestedCompatLayer != compat.StableLayer {
		t.Fatalf("Carbon default = %q, want %q", carbon.RequestedCompatLayer, compat.StableLayer)
	}
	if carbon.StableCompatLayer != compat.StableLayer {
		t.Fatalf("Carbon stable layer = %q, want %q", carbon.StableCompatLayer, compat.StableLayer)
	}
	legacyAlias, err := commandCompatibility("0.4", false)
	if err != nil || legacyAlias.RequestedCompatLayer != compat.StableLayer {
		t.Fatalf("legacy product alias 0.4 = %+v, %v; want canonical stable %q", legacyAlias, err, compat.StableLayer)
	}
}

func TestRunWebRejectsUnknownCompatLayerBeforeListening(t *testing.T) {
	called := false
	previous := listenWeb
	listenWeb = func(string) (net.Listener, error) {
		called = true
		return nil, errors.New("listener must not be called")
	}
	t.Cleanup(func() { listenWeb = previous })

	err := runWeb([]string{"--compat-layer", "v3", "--addr", "127.0.0.1:0"})
	if !errors.Is(err, compat.ErrUnsupportedLayer) {
		t.Fatalf("runWeb unknown compatibility layer = %v, want ErrUnsupportedLayer", err)
	}
	if called {
		t.Fatal("runWeb opened a listener for an unsupported compatibility layer")
	}
}

func TestRunServeLegacyRejectsUnknownCompatLayerBeforeAutoInit(t *testing.T) {
	root := t.TempDir()
	err := runServe([]string{"--actor", "agent:test", "--repo", root, "--compat-layer", "v3"})
	if !errors.Is(err, compat.ErrUnsupportedLayer) {
		t.Fatalf("runServe unknown compatibility layer = %v, want ErrUnsupportedLayer", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, repo.CarbonDirName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("legacy repository was initialized before compatibility rejection: %v", statErr)
	}
}

func TestRunServeRejectsCrossScopeCompatBeforeStoreInitialization(t *testing.T) {
	legacyRoot := t.TempDir()
	if err := runServe([]string{"--actor", "agent:test", "--repo", legacyRoot, "--compat-layer", compat.StableLayer}); !errors.Is(err, compat.ErrLayerScopeMismatch) {
		t.Fatalf("legacy v2 = %v, want ErrLayerScopeMismatch", err)
	}
	if _, err := os.Stat(filepath.Join(legacyRoot, repo.CarbonDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy repository was initialized before cross-scope rejection: %v", err)
	}

	// Resolve rejects before opening/initializing a Carbon data root, so this need
	// not be an initialized home to prove the fail-closed ordering.
	if err := runServe([]string{"--actor", "agent:test", "--home", t.TempDir(), "--cluster", "cluster-x", "--compat-layer", compat.LegacyLayer}); !errors.Is(err, compat.ErrLayerScopeMismatch) {
		t.Fatalf("Carbon v1 = %v, want ErrLayerScopeMismatch", err)
	}
}

func TestRunWebRejectsCrossScopeCompatBeforeListening(t *testing.T) {
	called := false
	previous := listenWeb
	listenWeb = func(string) (net.Listener, error) {
		called = true
		return nil, errors.New("listener must not be called")
	}
	t.Cleanup(func() { listenWeb = previous })

	if err := runWeb([]string{"--compat-layer", compat.LegacyLayer, "--addr", "127.0.0.1:0"}); !errors.Is(err, compat.ErrLayerScopeMismatch) {
		t.Fatalf("Carbon web v1 = %v, want ErrLayerScopeMismatch", err)
	}
	if err := runWeb([]string{"--repo", t.TempDir(), "--compat-layer", compat.StableLayer, "--addr", "127.0.0.1:0"}); !errors.Is(err, compat.ErrLayerScopeMismatch) {
		t.Fatalf("legacy web v2 = %v, want ErrLayerScopeMismatch", err)
	}
	if called {
		t.Fatal("runWeb opened a listener for a cross-scope compatibility layer")
	}
}

func TestResolveCarbonCatalogHomeCLIAllowsExplicitCreationTarget(t *testing.T) {
	root := t.TempDir()
	got, err := resolveCarbonCatalogHomeCLI(root)
	if err != nil {
		t.Fatalf("uninitialized catalog home: %v", err)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(got, want) {
		t.Fatalf("catalog home = %q, want canonical %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(root, ".carbon")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("catalog scope startup initialized home before allow_create: %v", err)
	}
}

func TestResolveCarbonCLIScopeAllowsStandaloneProjectWithoutCluster(t *testing.T) {
	root := t.TempDir()
	if _, err := home.Ensure(root); err != nil {
		t.Fatal(err)
	}
	standalone, err := home.AddStandaloneProject(root, home.AddProjectRequest{Name: "Desktop", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := resolveCarbonCLIScope(root, "", standalone.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot, err := home.ProjectDataRoot(root, standalone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !scope.Standalone || scope.ClusterID != "" || scope.ProjectID != standalone.ID || scope.Root != wantRoot || scope.SourcePath == "" {
		t.Fatalf("standalone CLI scope = %#v, want private project root %q", scope, wantRoot)
	}

	cluster, err := home.CreateCluster(root, home.CreateClusterRequest{Name: "Shared", Prefix: "SHR"})
	if err != nil {
		t.Fatal(err)
	}
	nested, err := home.AddProject(root, cluster.ID, home.AddProjectRequest{Name: "Nested", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveCarbonCLIScope(root, "", nested.ID, false); err == nil || !strings.Contains(err.Error(), "pass --cluster") {
		t.Fatalf("nested project without cluster = %v, want explicit cluster error", err)
	}
}

func TestListenerURLUsesBoundRemoteAddressAndLocalizesWildcard(t *testing.T) {
	for _, tc := range []struct {
		addr *net.TCPAddr
		want string
	}{
		{addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2525}, want: "http://127.0.0.1:2525"},
		{addr: &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 2525}, want: "http://192.0.2.10:2525"},
		{addr: &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 2525}, want: "http://127.0.0.1:2525"},
		{addr: &net.TCPAddr{IP: net.ParseIP("::1"), Port: 2525}, want: "http://[::1]:2525"},
	} {
		if got := listenerURL(tc.addr); got != tc.want {
			t.Errorf("listenerURL(%s) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

func TestSnapshotUploadRequiresConfirmBeforeReadingProfile(t *testing.T) {
	root := t.TempDir()
	h, err := home.Ensure(root)
	if err != nil {
		t.Fatal(err)
	}
	// This configuration would be rejected if the command reached profile
	// loading. Missing --confirm must stop before it can do so.
	if err := os.WriteFile(filepath.Join(h.CarbonRoot, "backup.json"), []byte(`{"accessKey":"must-not-be-read"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err = runSnapshot([]string{"upload", "--home", root, "--id", strings.Repeat("a", 64)})
	if err == nil || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("upload without confirm = %v, want --confirm error", err)
	}

	err = runSnapshot([]string{"upload", "--home", root, "--id", strings.Repeat("a", 64), "--confirm", "--access-key", "must-not-accept"})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("upload accepted raw credential argument: %v", err)
	}
}

func TestSnapshotUploadVerifiesLocalSnapshotBeforeReadingProfile(t *testing.T) {
	root := t.TempDir()
	h, err := home.Ensure(root)
	if err != nil {
		t.Fatal(err)
	}
	// A malformed profile must not mask a missing local snapshot: local
	// verification is the no-network gate before profile/key resolution.
	if err := os.WriteFile(filepath.Join(h.CarbonRoot, "backup.json"), []byte(`{"accessKey":"must-not-be-read"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err = runSnapshot([]string{"upload", "--home", root, "--id", strings.Repeat("a", 64), "--confirm"})
	if !errors.Is(err, backup.ErrNotFound) {
		t.Fatalf("upload with missing snapshot = %v, want local not-found before profile parse", err)
	}
}
