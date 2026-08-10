package backup

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LocalSchedulerOptions binds a scheduler to one trusted Carbon metadata root
// (`<home>/.carbon`). Every default path remains inside that root. A remote
// callback is optional and is considered only by scheduled runs after a local
// snapshot has been verified and the persisted profile explicitly authorizes
// continuous sync.
type LocalSchedulerOptions struct {
	CarbonRoot string
	SourceDir  string
	SourceID   string
	AppVersion string

	ConfigPath string
	StoreRoot  string
	StatePath  string

	Now     func() time.Time
	OnError func(error)

	ScheduledRemoteSync ScheduledRemoteSync
}

// ScheduledRemoteSyncRequest gives a host callback a locally verified immutable
// snapshot. The callback must re-read the current profile before resolving any
// credential so a revoked authorization cannot race into a new provider call.
type ScheduledRemoteSyncRequest struct {
	Repository *Repository
	Snapshot   Snapshot
	At         time.Time
}

// ScheduledRemoteSyncResult is returned only after a remote upload and its
// encrypted read-back verification have succeeded. Skipped means the callback
// observed a revocation or other local eligibility change before contacting a
// provider. A trusted host callback may record success/failure state while it
// still owns its authorization generation; LocalScheduler then avoids a stale
// post-callback state write.
type ScheduledRemoteSyncResult struct {
	Skipped                bool
	DestinationFingerprint string
	UploadedAt             time.Time
	RemoteStateRecorded    bool
	RemoteFailureRecorded  bool
}

// ScheduledRemoteSync is a host-owned provider operation. Keeping it injected
// lets LocalScheduler retain a zero-network default while the trusted server
// lifecycle can perform the authorized encrypted S3/COS publication.
type ScheduledRemoteSync func(context.Context, ScheduledRemoteSyncRequest) (ScheduledRemoteSyncResult, error)

// LocalRunResult describes one manually requested or scheduled local run.
type LocalRunResult struct {
	Snapshot Snapshot    `json:"snapshot"`
	Created  bool        `json:"created"`
	Skipped  bool        `json:"skipped"`
	Prune    PruneResult `json:"prune"`
}

// LocalScheduler owns the lifecycle of an in-process local snapshot loop. A
// scheduled run may invoke its injected remote callback only after an explicit
// persisted authorization; all manual/local config/status paths remain offline.
// Snapshot and prune transactions acquire a per-home OS advisory lock so
// independently started Carbon processes do not create duplicate manifests or
// prune each other's current work.
type LocalScheduler struct {
	carbonRoot          string
	sourceDir           string
	sourceID            string
	appVersion          string
	configPath          string
	storeRoot           string
	statePath           string
	now                 func() time.Time
	onError             func(error)
	scheduledRemoteSync ScheduledRemoteSync

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewLocalScheduler validates local path confinement without creating a remote
// client. It does not begin background work; call Start with a host lifecycle
// context after construction.
func NewLocalScheduler(options LocalSchedulerOptions) (*LocalScheduler, error) {
	root, err := filepath.Abs(strings.TrimSpace(options.CarbonRoot))
	if err != nil || strings.TrimSpace(options.CarbonRoot) == "" {
		if err == nil {
			err = errors.New("backup Carbon root is required")
		}
		return nil, fmt.Errorf("backup scheduler root: %w", err)
	}
	if err := ensureTrustedLocalDirectoryChain(root, false); err != nil {
		return nil, fmt.Errorf("backup scheduler root is unsafe: %w", err)
	}
	sourceDir := options.SourceDir
	if strings.TrimSpace(sourceDir) == "" {
		sourceDir = root
	}
	sourceDir, err = filepath.Abs(sourceDir)
	if err != nil {
		return nil, err
	}
	if !backupPathWithin(root, sourceDir) {
		return nil, fmt.Errorf("%w: scheduler source escapes Carbon root", ErrUnsafePath)
	}
	if strings.TrimSpace(options.SourceID) == "" {
		return nil, errors.New("backup scheduler source ID is required")
	}
	configPath := options.ConfigPath
	if strings.TrimSpace(configPath) == "" {
		configPath = filepath.Join(root, "backup.json")
	}
	storeRoot := options.StoreRoot
	if strings.TrimSpace(storeRoot) == "" {
		storeRoot = filepath.Join(root, "backups", "local")
	}
	statePath := options.StatePath
	if strings.TrimSpace(statePath) == "" {
		statePath = RuntimeStatePath(root)
	}
	for name, value := range map[string]string{"config": configPath, "store": storeRoot, "state": statePath} {
		absolute, err := filepath.Abs(value)
		if err != nil {
			return nil, fmt.Errorf("backup scheduler %s path: %w", name, err)
		}
		if !backupPathWithin(root, absolute) {
			return nil, fmt.Errorf("%w: scheduler %s path escapes Carbon root", ErrUnsafePath, name)
		}
		switch name {
		case "config":
			configPath = absolute
		case "store":
			storeRoot = absolute
		case "state":
			statePath = absolute
		}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &LocalScheduler{
		carbonRoot:          root,
		sourceDir:           sourceDir,
		sourceID:            options.SourceID,
		appVersion:          options.AppVersion,
		configPath:          configPath,
		storeRoot:           storeRoot,
		statePath:           statePath,
		now:                 now,
		onError:             options.OnError,
		scheduledRemoteSync: options.ScheduledRemoteSync,
	}, nil
}

func backupPathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Start begins the background loop. It returns after registering the loop; an
// enabled OnStart run happens asynchronously. Start is intentionally one-shot:
// create a fresh scheduler when a host needs a new lifecycle instance.
func (scheduler *LocalScheduler) Start(ctx context.Context) error {
	if scheduler == nil {
		return errors.New("backup local scheduler is nil")
	}
	if ctx == nil {
		return errors.New("backup local scheduler context is nil")
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if scheduler.started {
		return errors.New("backup local scheduler already started")
	}
	runContext, cancel := context.WithCancel(ctx)
	scheduler.started = true
	scheduler.cancel = cancel
	scheduler.done = make(chan struct{})
	go scheduler.loop(runContext, scheduler.done)
	return nil
}

// Stop requests background shutdown and waits for the current scheduled operation.
// It is safe to call repeatedly.
func (scheduler *LocalScheduler) Stop() {
	if scheduler == nil {
		return
	}
	scheduler.mu.Lock()
	cancel := scheduler.cancel
	done := scheduler.done
	scheduler.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// State reads local runtime state without creating a remote client or probing a
// provider. It is suitable for status endpoints.
func (scheduler *LocalScheduler) State() (RuntimeState, error) {
	if scheduler == nil {
		return RuntimeState{}, errors.New("backup local scheduler is nil")
	}
	return LoadRuntimeState(scheduler.statePath)
}

// RunNow creates (or reuses) a local snapshot regardless of the schedule's
// Enabled setting. It is the safe user-initiated recovery action; it never
// observes RemoteProfile or starts a network operation.
func (scheduler *LocalScheduler) RunNow(ctx context.Context) (LocalRunResult, error) {
	return scheduler.run(ctx, true)
}

// RunScheduled performs a local run only when the saved local schedule is
// enabled. Hosts generally do not call it directly; Start owns periodic calls.
func (scheduler *LocalScheduler) RunScheduled(ctx context.Context) (LocalRunResult, error) {
	return scheduler.run(ctx, false)
}

func (scheduler *LocalScheduler) run(ctx context.Context, force bool) (LocalRunResult, error) {
	if scheduler == nil {
		return LocalRunResult{}, errors.New("backup local scheduler is nil")
	}
	if ctx == nil {
		return LocalRunResult{}, errors.New("backup local scheduler context is nil")
	}
	lock, err := AcquireLocalHomeLock(ctx, scheduler.carbonRoot)
	if err != nil {
		return LocalRunResult{}, err
	}
	defer lock.Release() //nolint:errcheck // operation errors are more useful than unlock errors

	config, err := LoadProfileConfigFile(scheduler.configPath)
	if err != nil {
		return LocalRunResult{}, err
	}
	state, err := LoadRuntimeState(scheduler.statePath)
	if err != nil {
		return LocalRunResult{}, err
	}
	now := scheduler.now().UTC()
	state.LastRunAt = now.Format(time.RFC3339Nano)
	if !force && !config.Local.Enabled {
		if err := SaveRuntimeState(scheduler.statePath, state); err != nil {
			return LocalRunResult{}, err
		}
		return LocalRunResult{Skipped: true}, nil
	}

	repository, err := scheduler.localRepository()
	if err != nil {
		return LocalRunResult{}, err
	}
	snapshot, created, err := repository.CreateIfChanged(ctx, CreateOptions{
		SourceDir: scheduler.sourceDir, SourceID: scheduler.sourceID, AppVersion: scheduler.appVersion,
	})
	if err != nil {
		return LocalRunResult{}, err
	}
	manifest, err := repository.Verify(ctx, snapshot)
	if err != nil {
		return LocalRunResult{}, fmt.Errorf("backup local snapshot verify: %w", err)
	}
	pruned, err := repository.PruneLocalAt(ctx, RetentionPolicy{KeepLast: config.Local.KeepLast, KeepDays: config.Local.KeepDays}, now)
	if err != nil {
		return LocalRunResult{}, err
	}
	state.LastSuccessAt = now.Format(time.RFC3339Nano)
	state.LastSnapshotID = snapshot.ID
	state.LastSnapshotAt = manifest.CreatedAt.UTC().Format(time.RFC3339Nano)
	state.LastPruneAt = now.Format(time.RFC3339Nano)
	state.LastPruned = pruned.Pruned
	if err := SaveRuntimeState(scheduler.statePath, state); err != nil {
		return LocalRunResult{}, err
	}
	// A forced run is deliberately local-only. Only the lifecycle-owned
	// scheduled path can use a separately authorized remote callback.
	if !force {
		if err := scheduler.syncScheduledRemote(ctx, config, &state, repository, snapshot, now); err != nil {
			return LocalRunResult{}, err
		}
	}
	return LocalRunResult{Snapshot: snapshot, Created: created, Prune: pruned}, nil
}

func (scheduler *LocalScheduler) syncScheduledRemote(ctx context.Context, config ProfileConfigFile, state *RuntimeState, repository *Repository, snapshot Snapshot, now time.Time) error {
	if scheduler.scheduledRemoteSync == nil || !config.Profile.ContinuousSyncEnabled() {
		return nil
	}
	destination, err := RemoteDestinationFingerprint(config.Profile)
	if err != nil {
		return err
	}
	if state.LastRemoteSnapshotID == snapshot.ID && state.LastRemoteDestination == destination {
		// The same immutable local content has already completed encrypted
		// upload/read-back verification to this destination.
		return nil
	}
	if scheduler.remoteFailureRateLimited(*state, destination, now, config.Local.IntervalHours) {
		return nil
	}
	result, err := scheduler.scheduledRemoteSync(ctx, ScheduledRemoteSyncRequest{
		Repository: repository,
		Snapshot:   snapshot,
		At:         now,
	})
	if err != nil {
		// The local snapshot state was persisted before this callback. A
		// provider failure can neither roll it back nor cause a tight retry
		// loop; retain only a redacted status detail.
		failureDestination := destination
		// A profile can be edited while the local snapshot was being built.
		// Record a freshly persisted authorized destination when available so
		// the interval limiter also covers the target that just failed.
		if current, loadErr := LoadProfileConfigFile(scheduler.configPath); loadErr == nil && current.Profile.ContinuousSyncEnabled() {
			if currentDestination, fingerprintErr := RemoteDestinationFingerprint(current.Profile); fingerprintErr == nil {
				failureDestination = currentDestination
			}
		}
		state.LastRemoteFailureAt = now.Format(time.RFC3339Nano)
		state.LastRemoteFailureDestination = failureDestination
		state.LastRemoteError = RedactedRemoteSyncError
		if saveErr := SaveRuntimeState(scheduler.statePath, *state); saveErr != nil {
			return saveErr
		}
		scheduler.reportError(errors.New(RedactedRemoteSyncError))
		return nil
	}
	if result.Skipped {
		return nil
	}
	if result.RemoteFailureRecorded {
		scheduler.reportError(errors.New(RedactedRemoteSyncError))
		return nil
	}
	if result.RemoteStateRecorded {
		return nil
	}
	actualDestination := strings.TrimSpace(result.DestinationFingerprint)
	if actualDestination == "" {
		actualDestination = destination
	}
	if err := validateSHA256(actualDestination); err != nil {
		return errors.New("backup scheduled remote sync returned an invalid destination")
	}
	uploadedAt := result.UploadedAt
	if uploadedAt.IsZero() {
		uploadedAt = now
	}
	state.LastRemoteSnapshotID = snapshot.ID
	state.LastRemoteSnapshotAt = uploadedAt.UTC().Format(time.RFC3339Nano)
	state.LastRemoteDestination = actualDestination
	if err := SaveRuntimeState(scheduler.statePath, *state); err != nil {
		return err
	}
	return nil
}

func (scheduler *LocalScheduler) remoteFailureRateLimited(state RuntimeState, destination string, now time.Time, intervalHours int) bool {
	if state.LastRemoteFailureAt == "" || state.LastRemoteFailureDestination != destination {
		return false
	}
	failedAt, err := time.Parse(time.RFC3339Nano, state.LastRemoteFailureAt)
	if err != nil {
		// LoadRuntimeState validates this before the scheduler reaches here; a
		// defensive failure is still safer than retrying in a tight loop.
		return true
	}
	if intervalHours < 1 {
		intervalHours = defaultLocalSnapshotIntervalHours
	}
	return now.Before(failedAt.Add(time.Duration(intervalHours) * time.Hour))
}

// PruneNow applies the current local retention policy without taking a new
// snapshot. It is local-only and first validates all manifests through
// Repository.PruneLocalAt.
func (scheduler *LocalScheduler) PruneNow(ctx context.Context) (PruneResult, error) {
	if scheduler == nil {
		return PruneResult{}, errors.New("backup local scheduler is nil")
	}
	if ctx == nil {
		return PruneResult{}, errors.New("backup local scheduler context is nil")
	}
	lock, err := AcquireLocalHomeLock(ctx, scheduler.carbonRoot)
	if err != nil {
		return PruneResult{}, err
	}
	defer lock.Release() //nolint:errcheck // operation errors take precedence
	config, err := LoadProfileConfigFile(scheduler.configPath)
	if err != nil {
		return PruneResult{}, err
	}
	repository, err := scheduler.localRepository()
	if err != nil {
		return PruneResult{}, err
	}
	now := scheduler.now().UTC()
	result, err := repository.PruneLocalAt(ctx, RetentionPolicy{KeepLast: config.Local.KeepLast, KeepDays: config.Local.KeepDays}, now)
	if err != nil {
		return PruneResult{}, err
	}
	state, err := LoadRuntimeState(scheduler.statePath)
	if err != nil {
		return PruneResult{}, err
	}
	state.LastPruneAt = now.Format(time.RFC3339Nano)
	state.LastPruned = result.Pruned
	if err := SaveRuntimeState(scheduler.statePath, state); err != nil {
		return PruneResult{}, err
	}
	return result, nil
}

func (scheduler *LocalScheduler) localRepository() (*Repository, error) {
	store, err := NewLocalBlobStore(scheduler.storeRoot)
	if err != nil {
		return nil, err
	}
	repository, err := NewRepository(store, scheduler.appVersion)
	if err != nil {
		return nil, err
	}
	repository.now = scheduler.now
	return repository, nil
}

func (scheduler *LocalScheduler) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	first := true
	for {
		config, err := LoadProfileConfigFile(scheduler.configPath)
		if err != nil {
			scheduler.reportError(err)
			config = DefaultProfileConfig()
		}
		if first && config.Local.OnStart {
			if _, err := scheduler.RunScheduled(ctx); err != nil && !errors.Is(err, context.Canceled) {
				scheduler.reportError(err)
			}
		}
		first = false
		wait := time.Duration(config.Local.IntervalHours) * time.Hour
		if wait <= 0 {
			wait = time.Duration(defaultLocalSnapshotIntervalHours) * time.Hour
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if _, err := scheduler.RunScheduled(ctx); err != nil && !errors.Is(err, context.Canceled) {
				scheduler.reportError(err)
			}
		}
	}
}

func (scheduler *LocalScheduler) reportError(err error) {
	if scheduler.onError != nil {
		scheduler.onError(err)
	}
}
