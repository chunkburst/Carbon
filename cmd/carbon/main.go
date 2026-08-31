// Command carbon is the single Carbon binary. It serves the repo's task graph to two
// front-ends over the same rule-set: agents via MCP (stdio) and the web UI via HTTP.
//
// Usage:
//
//	carbon init  [--prefix PROJ] [--repo .]          # scaffold .carbon/ in a project
//	carbon serve [--actor agent:claude-1] [--client claude] [--repo .] # MCP server over stdio
//	carbon web   [--addr 127.0.0.1:2525] [--allow-remote (disabled)] [--repo .]  # HTTP server for the web UI
//
// Identity for MCP writes is injected once via --actor and stamped onto every write as
// provenance (SPEC §7); it is never passed per call.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"carbon/internal/backup"
	"carbon/internal/compat"
	"carbon/internal/home"
	"carbon/internal/mcp"
	"carbon/internal/repo"
	"carbon/internal/server"
	"carbon/internal/store"
)

// version is injected at release time via -ldflags "-X main.version=...".
var version = "dev"

const defaultWebAddr = "127.0.0.1:2525"

// listenWeb is replaceable by tests so rejected command-line options can prove they
// never open a network listener.
var listenWeb = listenWithFallback

// runMCPStdio is replaceable by focused CLI tests. Production always uses the
// standard StdioTransport; keeping this seam at the final transport boundary lets
// tests verify the selected server surface without borrowing process stdin/stdout.
var runMCPStdio = func(ctx context.Context, srv *mcpsdk.Server) error {
	return srv.Run(ctx, &mcpsdk.StdioTransport{})
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Carbon:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	mcp.ServiceVersion = version
	if len(args) == 0 {
		return usage()
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "init":
		return runInit(rest)
	case "home":
		return runHome(rest)
	case "snapshot":
		return runSnapshot(rest)
	case "serve":
		return runServe(rest)
	case "web":
		return runWeb(rest)
	case "version", "--version", "-v":
		fmt.Println("Carbon", version)
		return nil
	case "-h", "--help", "help":
		return usage()
	default:
		return fmt.Errorf("unknown command %q (want init, home, snapshot, serve, web, or version)", cmd)
	}
}

const usageText = `Carbon V1.1.2 — local task coordination

  carbon init  [--prefix PROJ] [--repo .]            scaffold .carbon/ in a project
  carbon home init [--home PATH]                     initialize a Carbon home
  carbon home import --home PATH --legacy-cluster H [--plan FILE | --apply --expected-digest SHA256]
  carbon home doctor [--home PATH] [--repair]        inspect/repair Carbon home metadata
  carbon snapshot create --home PATH                 snapshot <home>/.carbon locally
  carbon snapshot verify --home PATH --id SHA256     verify a local snapshot
  carbon snapshot upload --home PATH --id SHA256 --confirm
                                                    explicitly upload an encrypted snapshot
  carbon serve [--actor agent:x] [--client codex] [--compat-layer v1|v2] [--home PATH [--cluster ID [--project ID] | --project ID] | --repo PATH]
	                                            MCP server over stdio; v1=frozen legacy, v2=approved Carbon stable
  carbon serve --project-session --home PATH [--actor agent:x] [--client codex] [--compat-layer v2]
	                                            Carbon v2 session routing: begin in the home catalog, then select or create one active project; the same MCP connection may switch projects explicitly
  carbon web   [--addr 127.0.0.1:2525] [--compat-layer v1|v2] [--allow-remote (disabled)] [--home PATH [--cluster ID] [--project ID] | --repo PATH]
	                                            v1=frozen legacy, v2=approved Carbon stable; loopback-only; use an SSH/VPN tunnel to 127.0.0.1 for remote access
  carbon version                                     print the version and exit
`

func usage() error {
	fmt.Fprint(os.Stderr, usageText)
	return nil
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	prefix := fs.String("prefix", "", "id prefix (default: derived from the project folder name)")
	repoRoot := fs.String("repo", ".", "repo root to initialize")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := repo.Init(*repoRoot, *prefix); err != nil {
		return err
	}
	fmt.Printf("initialized Carbon workspace in %s (prefix %s)\n", *repoRoot, repo.CurrentPrefix(*repoRoot))
	return nil
}

// defaultHomePath keeps a released portable binary self-contained: its Carbon home is
// the executable directory unless a caller explicitly passes --home. Development builds
// use the current directory so `go run` and tests do not write beside a temporary test
// executable. The desktop shell always passes --home explicitly.
func defaultHomePath() string {
	if version != "dev" {
		if executable, err := os.Executable(); err == nil {
			return filepath.Dir(executable)
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

func runHome(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return homeUsage()
	}
	subcommand, rest := args[0], args[1:]
	switch subcommand {
	case "init":
		fs := flag.NewFlagSet("home init", flag.ContinueOnError)
		homePath := fs.String("home", defaultHomePath(), "Carbon home directory")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		h, err := home.Ensure(*homePath)
		if err != nil {
			return err
		}
		manifest, err := h.Manifest()
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(manifest)
	case "doctor":
		fs := flag.NewFlagSet("home doctor", flag.ContinueOnError)
		homePath := fs.String("home", defaultHomePath(), "Carbon home directory")
		repair := fs.Bool("repair", false, "apply deterministic metadata repairs")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		report, err := home.Doctor(*homePath, home.DoctorOptions{Apply: *repair})
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	case "import":
		fs := flag.NewFlagSet("home import", flag.ContinueOnError)
		homePath := fs.String("home", defaultHomePath(), "target Carbon home directory")
		legacyRoot := fs.String("legacy-cluster", "", "legacy .cairn-cluster.json root")
		planPath := fs.String("plan", "", "write a reviewed dry-run plan JSON to this path")
		apply := fs.Bool("apply", false, "apply a freshly generated, reviewed import plan")
		expectedDigest := fs.String("expected-digest", "", "required reviewed plan reviewDigest for --apply")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if strings.TrimSpace(*legacyRoot) == "" {
			return fmt.Errorf("home import requires --legacy-cluster")
		}
		if *apply && *planPath != "" {
			return fmt.Errorf("--plan and --apply are mutually exclusive")
		}
		plan, err := home.PlanLegacyImport(*homePath, *legacyRoot)
		if err != nil {
			return err
		}
		if *apply {
			if strings.TrimSpace(*expectedDigest) == "" {
				return fmt.Errorf("home import --apply requires --expected-digest from the reviewed plan")
			}
			if plan.ReviewDigest != strings.TrimSpace(*expectedDigest) {
				return fmt.Errorf("legacy import review changed: expected %s, found %s", strings.TrimSpace(*expectedDigest), plan.ReviewDigest)
			}
			// A fresh target cannot be initialized before Apply: doing so changes the
			// reviewed HomeExists state and intentionally invalidates its digest. Take
			// a verified pre-import snapshot only for an existing home; otherwise take
			// the verified post-import baseline immediately after the atomic apply.
			preExisting := plan.BaseHomeDigest != ""
			var snapshot backup.Snapshot
			if preExisting {
				snapshot, err = createVerifiedHomeSnapshotCLI(*homePath)
				if err != nil {
					return err
				}
			}
			// The apply request deliberately contains only a source root plus the
			// reviewed digest. Home re-plans under its lock; a serialized plan is never
			// trusted as a write instruction.
			result, err := home.ApplyLegacyImportRequest(*homePath, home.LegacyImportApplyRequest{
				LegacyRoot: *legacyRoot, ExpectedDigest: strings.TrimSpace(*expectedDigest), ConfigPolicy: plan.ConfigPolicy,
			})
			if err != nil {
				return err
			}
			if !preExisting {
				snapshot, err = createVerifiedHomeSnapshotCLI(*homePath)
				if err != nil {
					return fmt.Errorf("legacy import applied but post-import snapshot failed: %w", err)
				}
			}
			return json.NewEncoder(os.Stdout).Encode(map[string]any{
				"result": result, "snapshot": snapshot, "snapshotId": snapshot.ID,
				"snapshotTiming": map[bool]string{true: "pre-import", false: "post-import"}[preExisting],
			})
		}
		data, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}
		if *planPath != "" {
			if err := os.WriteFile(*planPath, append(data, '\n'), 0o600); err != nil {
				return err
			}
			return nil
		}
		_, err = os.Stdout.Write(append(data, '\n'))
		return err
	default:
		return fmt.Errorf("unknown home command %q (want init, import, or doctor)", subcommand)
	}
}

func runSnapshot(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return snapshotUsage()
	}
	subcommand, rest := args[0], args[1:]
	fs := flag.NewFlagSet("snapshot "+subcommand, flag.ContinueOnError)
	homePath := fs.String("home", defaultHomePath(), "Carbon home directory")
	id := fs.String("id", "", "snapshot manifest SHA-256 id")
	confirm := fs.Bool("confirm", false, "explicitly authorize a remote upload")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	repository, manifest, err := localHomeBackupRepository(*homePath)
	if err != nil {
		return err
	}
	switch subcommand {
	case "create":
		snapshot, err := repository.Create(context.Background(), backup.CreateOptions{
			SourceDir: manifest.CarbonRoot, SourceID: manifest.ID, AppVersion: version,
		})
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(snapshot)
	case "verify":
		if strings.TrimSpace(*id) == "" {
			return fmt.Errorf("snapshot verify requires --id")
		}
		verified, err := repository.Verify(context.Background(), backup.Snapshot{ID: *id})
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(verified)
	case "upload":
		if strings.TrimSpace(*id) == "" {
			return fmt.Errorf("snapshot upload requires --id")
		}
		if !*confirm {
			return fmt.Errorf("snapshot upload requires --confirm")
		}
		snapshot := backup.Snapshot{ID: *id}
		// Verify all local bytes before reading a profile or resolving any
		// credential/key reference. This preserves the no-network failure path
		// for malformed IDs and corrupt snapshots.
		if _, err := repository.Verify(context.Background(), snapshot); err != nil {
			return err
		}
		config, err := backup.LoadProfileConfigFile(filepath.Join(manifest.CarbonRoot, "backup.json"))
		if err != nil {
			return err
		}
		if !config.Profile.Enabled {
			return backup.ErrRemoteDisabled
		}
		remote, err := backup.NewEncryptedRemoteBlobStore(context.Background(), config.Profile)
		if err != nil {
			return err
		}
		if err := repository.Upload(context.Background(), snapshot, remote, backup.UploadOptions{Enabled: true}); err != nil {
			return fmt.Errorf("snapshot upload failed")
		}
		updated, err := backup.MarkProfileUpload(filepath.Join(manifest.CarbonRoot, "backup.json"), time.Now())
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"snapshot": snapshot, "uploaded": true, "verified": true, "lastUpload": updated.LastUpload,
		})
	default:
		return fmt.Errorf("unknown snapshot command %q (want create, verify, or upload)", subcommand)
	}
}

func homeUsage() error {
	fmt.Fprint(os.Stderr, "Carbon home commands\n\n"+
		"  carbon home init [--home PATH]\n"+
		"  carbon home import --home PATH --legacy-cluster PATH [--plan FILE | --apply --expected-digest REVIEW_DIGEST]\n"+
		"  carbon home doctor [--home PATH] [--repair]\n")
	return nil
}

func snapshotUsage() error {
	fmt.Fprint(os.Stderr, "Carbon snapshot commands\n\n"+
		"  carbon snapshot create [--home PATH]\n"+
		"  carbon snapshot verify [--home PATH] --id SNAPSHOT_ID\n"+
		"  carbon snapshot upload [--home PATH] --id SNAPSHOT_ID --confirm\n")
	return nil
}

// backupHomeHandle derives every backup path from an opened Carbon home. It deliberately
// does not accept a source/staging directory argument, preventing CLI callers from
// snapshotting arbitrary folders through Carbon's backup feature.
type backupHomeHandle struct {
	CarbonRoot string
	ID         string
}

func localHomeBackupRepository(homePath string) (*backup.Repository, backupHomeHandle, error) {
	h, err := home.Open(homePath)
	if err != nil {
		return nil, backupHomeHandle{}, err
	}
	manifest, err := h.Manifest()
	if err != nil {
		return nil, backupHomeHandle{}, err
	}
	local, err := backup.NewLocalBlobStore(filepath.Join(h.CarbonRoot, "backups", "local"))
	if err != nil {
		return nil, backupHomeHandle{}, err
	}
	repository, err := backup.NewRepository(local, version)
	if err != nil {
		return nil, backupHomeHandle{}, err
	}
	return repository, backupHomeHandle{CarbonRoot: h.CarbonRoot, ID: manifest.ID}, nil
}

// createVerifiedHomeSnapshotCLI is deliberately private to the home import command.
// It ensures the target home exists, snapshots its private .carbon tree, and verifies
// that snapshot before import proceeds. No user-controlled source or staging path is
// accepted by this flow.
func createVerifiedHomeSnapshotCLI(homePath string) (backup.Snapshot, error) {
	if _, err := home.Ensure(homePath); err != nil {
		return backup.Snapshot{}, err
	}
	repository, handle, err := localHomeBackupRepository(homePath)
	if err != nil {
		return backup.Snapshot{}, err
	}
	snapshot, err := repository.Create(context.Background(), backup.CreateOptions{
		SourceDir: handle.CarbonRoot, SourceID: handle.ID, AppVersion: version,
	})
	if err != nil {
		return backup.Snapshot{}, err
	}
	if _, err := repository.Verify(context.Background(), snapshot); err != nil {
		return backup.Snapshot{}, fmt.Errorf("verify pre-migration snapshot %s: %w", snapshot.ID, err)
	}
	return snapshot, nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	actor := fs.String("actor", "", "identity for writes, e.g. agent:claude-1 or human:shah")
	client := fs.String("client", "", "agent client identity, e.g. codex or claude")
	compatLayer := fs.String("compat-layer", "", "compatibility layer: v1 (frozen legacy Cairn 0.3) or v2 (approved Carbon stable contract)")
	repoRoot := fs.String("repo", "", "legacy repo root containing .cairn/ (frozen v1 compatibility mode)")
	homePath := fs.String("home", defaultHomePath(), "Carbon home directory")
	clusterID := fs.String("cluster", "", "Carbon cluster id")
	projectID := fs.String("project", "", "default Carbon project id")
	projectSession := fs.Bool("project-session", false, "Carbon v2 MCP session routing: select one active project per connection")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *actor == "" {
		return fmt.Errorf("--actor is required (e.g. agent:claude-1)")
	}
	legacy := strings.TrimSpace(*repoRoot) != ""
	if *projectSession {
		// Session routing is deliberately an opt-in Carbon v2 transport mode. Do
		// not treat empty values as harmless here: accepting --project "" or
		// --repo "" would make a generated integration config look session-aware
		// while silently using a different binding contract.
		if flagWasSet(fs, "repo") || flagWasSet(fs, "cluster") || flagWasSet(fs, "project") {
			return fmt.Errorf("--project-session cannot be combined with --repo, --cluster, or --project")
		}
		if !flagWasSet(fs, "home") {
			return fmt.Errorf("--project-session requires an explicit --home")
		}
	}
	if legacy && (*clusterID != "" || *projectID != "" || flagWasSet(fs, "home")) {
		return fmt.Errorf("--repo legacy mode cannot be combined with --home, --cluster, or --project")
	}
	// --home + --project is an isolated standalone Carbon project. A cluster remains
	// optional and is only needed for the intentionally shared task-pool mode.
	homeOnly := !legacy && !*projectSession && strings.TrimSpace(*clusterID) == "" && strings.TrimSpace(*projectID) == ""
	if homeOnly && !flagWasSet(fs, "home") {
		return fmt.Errorf("Carbon MCP serve requires --cluster or an explicit --home catalog scope (or use explicit --repo for legacy mode)")
	}
	contract, err := commandCompatibility(*compatLayer, legacy)
	if err != nil {
		return err
	}

	var (
		svc     *mcp.Service
		srv     *mcpsdk.Server
		binding *mcp.ProjectSession
	)
	if *projectSession {
		// NewProjectSession starts as an inert Carbon home catalog. It resolves and
		// validates each selected project afresh rather than capturing a task root
		// at process startup, so an explicit select_project is the only way a
		// connection can cross a project boundary.
		resolvedHome, err := resolveCarbonCatalogHomeCLI(*homePath)
		if err != nil {
			return err
		}
		binding, err = mcp.NewProjectSession(store.New(resolvedHome), *actor, *client, resolvedHome, nil)
		if err != nil {
			return err
		}
		srv = mcp.NewProjectSessionServer(binding)
	} else if legacy {
		// Auto-init so a freshly opened legacy project just works.
		if err := repo.Init(*repoRoot, ""); err != nil {
			return err
		}
		svc = mcp.NewServiceWithClient(store.New(*repoRoot), *actor, *client, nil)
	} else if homeOnly {
		// A home-only stdio server is deliberately catalog/identity-only. It may
		// address an existing, not-yet-initialized directory so create_cluster can
		// perform its own explicit allow_create/reason-gated home initialization.
		// No cluster data root is initialized here.
		resolvedHome, err := resolveCarbonCatalogHomeCLI(*homePath)
		if err != nil {
			return err
		}
		svc = mcp.NewScopedServiceWithClientAndResolver(store.New(resolvedHome), *actor, *client,
			mcp.Scope{Home: resolvedHome, CompatLayer: contract.RequestedCompatLayer}, nil, nil)
	} else {
		scope, err := resolveCarbonCLIScope(*homePath, *clusterID, *projectID, true)
		if err != nil {
			return err
		}
		svc = mcp.NewScopedServiceWithClientAndResolver(store.New(scope.Root), *actor, *client,
			mcp.Scope{Home: scope.Home, ClusterID: scope.ClusterID, ProjectID: scope.ProjectID, SourcePath: scope.SourcePath,
				Standalone: scope.Standalone, ClusterScope: scope.ClusterID != "" && scope.ProjectID == "", CompatLayer: contract.RequestedCompatLayer},
			carbonProjectRootResolver(scope.Home, scope.ClusterID, scope.Standalone, scope.ProjectID), nil)
	}
	if srv == nil {
		srv = mcp.NewServer(svc)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if binding != nil {
		go sweepMCPProjectSessionLeases(ctx, binding)
	} else if !homeOnly {
		go sweepMCPLeases(ctx, svc)
	}

	// A client closing the pipe (EOF) or a Ctrl-C is a normal shutdown, not a failure.
	err = runMCPStdio(ctx, srv)
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func runWeb(args []string) error {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	addr := fs.String("addr", defaultWebAddr, "loopback address to listen on (a busy port falls back to the next free one)")
	allowRemote := fs.Bool("allow-remote", false, "deprecated compatibility flag; disabled because the local API is unauthenticated")
	compatLayer := fs.String("compat-layer", "", "compatibility layer: v1 (frozen legacy Cairn 0.3) or v2 (approved Carbon stable contract)")
	repoRoot := fs.String("repo", "", "legacy default repo root (frozen v1 compatibility mode)")
	homePath := fs.String("home", defaultHomePath(), "Carbon home directory")
	clusterID := fs.String("cluster", "", "default Carbon cluster id")
	projectID := fs.String("project", "", "default Carbon project id")
	actor := fs.String("actor", "human:web", "identity stamped on web-driven writes")
	parentWatch := fs.Bool("parent-watch", false, "shut down when stdin closes (set by the desktop shell)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Origin is not authentication. Keep the REST and streamable MCP listeners local
	// until an authenticated remote transport exists; a tunnel can still expose the
	// loopback listener under an operator-controlled security boundary.
	if *allowRemote {
		return fmt.Errorf("--allow-remote is disabled: use an SSH/VPN tunnel to 127.0.0.1 instead")
	}
	if err := validateWebAddr(*addr); err != nil {
		return err
	}
	legacy := strings.TrimSpace(*repoRoot) != ""
	if legacy && (*clusterID != "" || *projectID != "" || flagWasSet(fs, "home")) {
		return fmt.Errorf("--repo legacy mode cannot be combined with --home, --cluster, or --project")
	}
	if _, err := commandCompatibility(*compatLayer, legacy); err != nil {
		return err
	}

	defaults := server.ScopeDefaults{}
	if legacy {
		defaults.LegacyRoot = *repoRoot
	} else {
		defaults = server.ScopeDefaults{
			Home: *homePath, ClusterID: *clusterID, ProjectID: *projectID, HomeByDefault: true,
		}
	}
	// Build the API before opening a listener. A malformed/future compatibility layer
	// therefore cannot leave an unauthenticated server accepting connections.
	api, err := server.NewWithScopeAndCompatibility(*actor, defaults, server.CompatibilityOptions{
		ProductVersion: version, RequestedCompatLayer: *compatLayer,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// As a Tauri sidecar the desktop app holds our stdin open; when it exits the
	// pipe closes (EOF) and we shut down, so the server never outlives the app.
	// Gated so terminal/CI runs (stdin may be /dev/null → instant EOF) are unaffected.
	if *parentWatch || parentWatchEnabled() {
		go func() {
			io.Copy(io.Discard, os.Stdin)
			cancel()
		}()
	}

	if !legacy {
		// Carbon's default main path is a real Home, even when the user keeps the
		// portable defaults. Starting the local scheduler may create/verify local
		// snapshots and apply local retention only; it never resolves a remote
		// credential or opens a cloud connection.
		if _, err := home.Ensure(*homePath); err != nil {
			return fmt.Errorf("initialize Carbon home for local backups: %w", err)
		}
		backupScheduler, err := server.StartLocalBackupScheduler(ctx, *homePath)
		if err != nil {
			return fmt.Errorf("start local backup scheduler: %w", err)
		}
		defer backupScheduler.Stop()
	}

	ln, err := listenWeb(*addr)
	if err != nil {
		return err
	}

	// One machine-parseable line on stdout for the desktop shell to read (the port
	// may differ from the request after fallback); the human line stays on stderr.
	url := listenerURL(ln.Addr())
	fmt.Printf("CARBON_WEB_URL=%s\n", url)
	os.Stdout.Sync()
	if legacy {
		fmt.Fprintf(os.Stderr, "Carbon web listening on %s (legacy repo %s)\n", url, *repoRoot)
	} else {
		fmt.Fprintf(os.Stderr, "Carbon web listening on %s (Carbon home %s)\n", url, *homePath)
	}

	api.StartLeaseSweep(ctx)
	defer api.StopLeaseSweep()
	srv := &http.Server{
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		<-ctx.Done()
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// parentWatchEnabled reads the canonical desktop-sidecar switch first. The historical
// spelling remains a read-only compatibility alias so an already-installed sidecar can
// still shut down cleanly during an upgrade; all new launchers emit CARBON_PARENT_WATCH.
func parentWatchEnabled() bool {
	return os.Getenv("CARBON_PARENT_WATCH") != "" || os.Getenv("CAIRN_PARENT_WATCH") != ""
}

// commandCompatibility keeps CLI parsing fail-closed without coupling the command
// package to a particular HTTP server constructor. `--repo` intentionally defaults
// to frozen legacy v1 (Cairn 0.3 semantics); Carbon home scope intentionally defaults
// to the approved stable v2 contract. Product build versions remain
// descriptive and do not alter either compatibility layer.
func commandCompatibility(requested string, legacy bool) (compat.Contract, error) {
	mode := compat.ModeCarbon
	if legacy {
		mode = compat.ModeLegacy
	}
	return compat.Resolve(version, requested, mode)
}

// resolveCarbonCatalogHomeCLI opens an initialized Carbon home normally. An empty
// but already-safe directory is also valid for a home-only catalog server: the
// catalog's create_* methods re-run home validation and initialize metadata only
// after their explicit allow_create/reason gate. We deliberately do not call
// home.Ensure here, because merely starting an MCP server must not create durable
// catalog state.
func resolveCarbonCatalogHomeCLI(homePath string) (string, error) {
	h, err := home.Open(homePath)
	if err == nil {
		return h.Root, nil
	}
	if !errors.Is(err, home.ErrNotInitialized) {
		return "", err
	}
	// home.Open reached ErrNotInitialized only after its strict local/root safety
	// validation. Canonicalize once for the inert Scope handle; every catalog
	// operation validates the root again before reading or mutating metadata.
	abs, absErr := filepath.Abs(homePath)
	if absErr != nil {
		return "", absErr
	}
	resolved, resolveErr := filepath.EvalSymlinks(abs)
	if resolveErr != nil {
		return "", resolveErr
	}
	info, statErr := os.Stat(resolved)
	if statErr != nil || !info.IsDir() {
		return "", fmt.Errorf("Carbon home is not an existing directory: %s", abs)
	}
	return filepath.Clean(resolved), nil
}

type carbonCLIScope struct {
	Home       string
	ClusterID  string
	ProjectID  string
	Root       string
	SourcePath string
	Standalone bool
}

// resolveCarbonCLIScope binds either a shared cluster data root or one isolated
// standalone project root separately from an optional executable source path. `Root`
// is never returned as a source directory.
func resolveCarbonCLIScope(homePath, clusterID, projectID string, initializeDataRoot bool) (carbonCLIScope, error) {
	clusterID = strings.TrimSpace(clusterID)
	projectID = strings.TrimSpace(projectID)
	if clusterID == "" && projectID == "" {
		return carbonCLIScope{}, fmt.Errorf("Carbon cluster id or standalone project id is required")
	}
	h, err := home.Open(homePath)
	if err != nil {
		return carbonCLIScope{}, err
	}
	if clusterID == "" {
		resolution, err := home.ResolveProject(h.Root, home.ResolveProjectRequest{ProjectID: projectID})
		if err != nil {
			return carbonCLIScope{}, err
		}
		if !resolution.Standalone {
			return carbonCLIScope{}, fmt.Errorf("Carbon project %s belongs to cluster %s; pass --cluster for shared-pool scope", projectID, resolution.Cluster.ID)
		}
		if initializeDataRoot {
			if err := validateStandaloneDataRootCLI(resolution.DataRoot, resolution.Project.ID); err != nil {
				return carbonCLIScope{}, err
			}
		}
		scope := carbonCLIScope{
			Home: h.Root, ProjectID: resolution.Project.ID, Root: resolution.DataRoot,
			Standalone: true,
		}
		if resolution.Offline {
			return carbonCLIScope{}, fmt.Errorf("Carbon standalone project %s is offline or its source fingerprint no longer matches", projectID)
		}
		scope.SourcePath = resolution.SourcePath
		return scope, nil
	}
	cluster, err := h.ResolveCluster(clusterID)
	if err != nil {
		return carbonCLIScope{}, err
	}
	clusterID = cluster.ID
	dataRoot, err := home.ClusterDataRoot(h.Root, clusterID)
	if err != nil {
		return carbonCLIScope{}, err
	}
	if initializeDataRoot {
		if err := initCarbonDataRootCLI(dataRoot, ""); err != nil {
			return carbonCLIScope{}, err
		}
	}
	scope := carbonCLIScope{Home: h.Root, ClusterID: clusterID, ProjectID: projectID, Root: dataRoot}
	if projectID == "" {
		return scope, nil
	}
	resolution, err := home.ResolveProject(h.Root, home.ResolveProjectRequest{ClusterID: clusterID, ProjectID: projectID})
	if err != nil {
		return carbonCLIScope{}, err
	}
	if resolution.Standalone {
		return carbonCLIScope{}, fmt.Errorf("standalone project %s cannot be selected through --cluster", projectID)
	}
	if resolution.Offline {
		return carbonCLIScope{}, fmt.Errorf("Carbon project %s is offline or its source fingerprint no longer matches", projectID)
	}
	scope.ProjectID = resolution.Project.ID
	scope.SourcePath = resolution.SourcePath
	return scope, nil
}

func carbonProjectRootResolver(homePath, clusterID string, standalone bool, boundProjectID string) mcp.ProjectRootResolver {
	return func(projectID string) (string, error) {
		if standalone && projectID != boundProjectID {
			return "", fmt.Errorf("standalone project %s cannot resolve sibling project %s", boundProjectID, projectID)
		}
		resolution, err := home.ResolveProject(homePath, home.ResolveProjectRequest{ClusterID: clusterID, ProjectID: projectID})
		if err != nil {
			return "", err
		}
		if resolution.Standalone != standalone {
			return "", fmt.Errorf("project %s resolved outside selected Carbon storage scope", projectID)
		}
		if resolution.Offline {
			return "", fmt.Errorf("Carbon project %s is offline or its source fingerprint no longer matches", projectID)
		}
		return resolution.SourcePath, nil
	}
}

func initCarbonDataRootCLI(dataRoot, prefix string) error {
	if err := repo.InitDataRoot(dataRoot, prefix); err != nil {
		return err
	}
	st := store.New(dataRoot)
	cfg, err := st.Config()
	if err != nil {
		return err
	}
	if cfg.ProjectID == "" {
		return nil
	}
	cfg.ProjectID = ""
	return st.SaveConfig(cfg)
}

// validateStandaloneDataRootCLI preserves Home's stable project binding. It is a
// read-only guard: Home is responsible for creating standalone private stores.
func validateStandaloneDataRootCLI(dataRoot, projectID string) error {
	cfg, err := store.New(dataRoot).Config()
	if err != nil {
		return err
	}
	if projectID == "" || cfg.ProjectID != projectID {
		return fmt.Errorf("standalone data root is not bound to project %s", projectID)
	}
	return nil
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(value *flag.Flag) {
		if value.Name == name {
			set = true
		}
	})
	return set
}

func sweepMCPLeases(ctx context.Context, svc *mcp.Service) {
	_, _ = svc.ExpireLeases(ctx)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = svc.ExpireLeases(ctx)
		}
	}
}

// sweepMCPProjectSessionLeases follows the active binding at every tick. A
// ProjectSession intentionally has no task store before select_project; its
// ExpireLeases method reports that state without ever sweeping the home catalog.
// Do not cache ActiveService here: an explicit project switch must take effect
// before the next maintenance operation.
func sweepMCPProjectSessionLeases(ctx context.Context, binding *mcp.ProjectSession) {
	_, _ = binding.ExpireLeases(ctx)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = binding.ExpireLeases(ctx)
		}
	}
}

func listenerURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://127.0.0.1"
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		// An unspecified listener accepts local loopback traffic too; advertise a URL
		// the local desktop shell/browser can actually open.
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// validateWebAddr keeps the unauthenticated REST and MCP API permanently local. A
// hostname other than exact localhost, wildcard, or non-loopback IP must be exposed
// through an authenticated operator-controlled tunnel rather than a direct listener.
func validateWebAddr(addr string) error {
	if err := server.ValidateLoopbackAddr(addr); err == nil {
		return nil
	}
	return fmt.Errorf("refusing non-loopback --addr %q: use an SSH/VPN tunnel to 127.0.0.1", addr)
}

func isLoopbackAddr(addr string) bool {
	return server.ValidateLoopbackAddr(addr) == nil
}

// listenWithFallback binds addr; if its port is taken it scans the next 20 ports,
// then falls back to an OS-assigned one — so the desktop app always comes up even
// when the preferred port (2525) is already in use.
func listenWithFallback(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return ln, nil
	}
	host, portStr, perr := net.SplitHostPort(addr)
	if perr != nil {
		return nil, err
	}
	base, aerr := strconv.Atoi(portStr)
	if aerr != nil || base == 0 {
		return nil, err
	}
	for p := base + 1; p <= base+20; p++ {
		if l, e := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p))); e == nil {
			return l, nil
		}
	}
	return net.Listen("tcp", net.JoinHostPort(host, "0"))
}
