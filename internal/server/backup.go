package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"carbon/internal/backup"
	"carbon/internal/home"
)

const backupConfigFilename = "backup.json"

var (
	errBackupHomeOnly = errors.New("backup requires an explicit home-only scope")
	backupConfigMu    sync.Mutex
	backupRemoteSyncs backupRemoteSyncRegistry
)

// backupRemoteSyncRegistry owns cancellation generations for scheduled remote
// work. Config mutations use backupConfigMu first and then this registry; a
// remote operation never holds backupConfigMu while it talks to a provider.
// Keeping generations per Carbon home prevents a revocation in one home from
// affecting another local scheduler.
type backupRemoteSyncRegistry struct {
	mu    sync.Mutex
	homes map[string]*backupRemoteSyncHome
}

type backupRemoteSyncHome struct {
	generation uint64
	nextID     uint64
	inFlight   map[uint64]context.CancelFunc
}

// backupRemoteSyncProfileIdentity contains every persisted profile value that
// can change what a scheduled remote sync is authorized to do. It intentionally
// includes the credential reference even though RemoteDestinationFingerprint
// does not: a new credential authority must not let an older request complete
// and record success against the replacement configuration. Profile is only a
// local display name, so it is not part of the effective sync identity.
type backupRemoteSyncProfileIdentity struct {
	enabled                 bool
	continuousAuthorization bool
	backend                 string
	bucket                  string
	prefix                  string
	region                  string
	endpoint                string
	usePathStyle            bool
	allowInsecureEndpoint   bool
	credentialRef           string
	encryption              bool
	encryptionKeyRef        string
}

func backupRemoteSyncProfileIdentityFor(profile backup.RemoteProfile) backupRemoteSyncProfileIdentity {
	return backupRemoteSyncProfileIdentity{
		enabled:                 profile.Enabled,
		continuousAuthorization: profile.ContinuousAuthorization,
		backend:                 profile.Backend,
		bucket:                  profile.Bucket,
		prefix:                  profile.Prefix,
		region:                  profile.Region,
		endpoint:                profile.Endpoint,
		usePathStyle:            profile.UsePathStyle,
		allowInsecureEndpoint:   profile.AllowInsecureEndpoint,
		credentialRef:           profile.CredentialRef,
		encryption:              profile.Encryption,
		encryptionKeyRef:        profile.EncryptionKeyRef,
	}
}

func backupRemoteSyncProfileChanged(before, after backup.RemoteProfile) bool {
	return backupRemoteSyncProfileIdentityFor(before) != backupRemoteSyncProfileIdentityFor(after)
}

func (registry *backupRemoteSyncRegistry) begin(homeKey string, parent context.Context) (context.Context, uint64, func()) {
	registry.mu.Lock()
	if registry.homes == nil {
		registry.homes = make(map[string]*backupRemoteSyncHome)
	}
	state := registry.homes[homeKey]
	if state == nil {
		state = &backupRemoteSyncHome{inFlight: make(map[uint64]context.CancelFunc)}
		registry.homes[homeKey] = state
	}
	state.nextID++
	id := state.nextID
	generation := state.generation
	ctx, cancel := context.WithCancel(parent)
	state.inFlight[id] = cancel
	registry.mu.Unlock()

	var once sync.Once
	return ctx, generation, func() {
		once.Do(func() {
			cancel()
			registry.mu.Lock()
			if current := registry.homes[homeKey]; current != nil {
				delete(current.inFlight, id)
			}
			registry.mu.Unlock()
		})
	}
}

func (registry *backupRemoteSyncRegistry) current(homeKey string, generation uint64) bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	state := registry.homes[homeKey]
	return state != nil && state.generation == generation
}

// invalidate advances a home generation and returns all old sync cancels. The
// caller must invoke them only after releasing backupConfigMu so a revoke never
// waits for provider I/O or an HTTP client to observe its canceled context.
func (registry *backupRemoteSyncRegistry) invalidate(homeKey string) []context.CancelFunc {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.homes == nil {
		registry.homes = make(map[string]*backupRemoteSyncHome)
	}
	state := registry.homes[homeKey]
	if state == nil {
		state = &backupRemoteSyncHome{inFlight: make(map[uint64]context.CancelFunc)}
		registry.homes[homeKey] = state
	}
	state.generation++
	cancels := make([]context.CancelFunc, 0, len(state.inFlight))
	for _, cancel := range state.inFlight {
		cancels = append(cancels, cancel)
	}
	// Existing callbacks retain their own cancel; removing them here means a
	// later reauthorization starts with a clean generation and no stale entry.
	state.inFlight = make(map[uint64]context.CancelFunc)
	return cancels
}

// backupConfigDTO is the entire public configuration surface. In particular it
// deliberately has no version or lastUpload field: clients can only submit a
// credential-free profile, while lastUpload stays server-owned on disk.
type backupConfigDTO struct {
	Profile backup.RemoteProfile `json:"profile"`
	Local   backup.LocalSchedule `json:"local"`
}

type backupConfigPutDTO struct {
	Profile *backup.RemoteProfile `json:"profile"`
	Local   *backup.LocalSchedule `json:"local"`
}

type backupUploadRequest struct {
	Confirm bool `json:"confirm"`
}

type backupContinuousAuthorizationRequest struct {
	Confirm bool `json:"confirm"`
	Enabled bool `json:"enabled"`
}

type backupLocalStatusDTO struct {
	Configured     bool   `json:"configured"`
	Enabled        bool   `json:"enabled"`
	Operational    bool   `json:"operational"`
	IntervalHours  int    `json:"intervalHours"`
	OnStart        bool   `json:"onStart"`
	KeepLast       int    `json:"keepLast"`
	KeepDays       int    `json:"keepDays"`
	LastRunAt      string `json:"lastRunAt,omitempty"`
	LastSuccessAt  string `json:"lastSuccessAt,omitempty"`
	LastSnapshotID string `json:"lastSnapshotId,omitempty"`
	LastSnapshotAt string `json:"lastSnapshotAt,omitempty"`
	LastPruneAt    string `json:"lastPruneAt,omitempty"`
	LastPruned     int    `json:"lastPruned,omitempty"`
}

type backupRemoteStatusDTO struct {
	Configured              bool   `json:"configured"`
	Enabled                 bool   `json:"enabled"`
	ContinuousAuthorization bool   `json:"continuousAuthorization"`
	Operational             bool   `json:"operational"`
	LastUpload              string `json:"lastUpload,omitempty"`
	LastRemoteSnapshotID    string `json:"lastRemoteSnapshotId,omitempty"`
	LastRemoteSnapshotAt    string `json:"lastRemoteSnapshotAt,omitempty"`
	LastRemoteFailureAt     string `json:"lastRemoteFailureAt,omitempty"`
	LastRemoteError         string `json:"lastRemoteError,omitempty"`
}

type backupStatusDTO struct {
	SourceID string                `json:"sourceId"`
	Source   string                `json:"source"`
	Local    backupLocalStatusDTO  `json:"local"`
	Remote   backupRemoteStatusDTO `json:"remote"`
}

func defaultBackupConfig() backup.ProfileConfigFile { return backup.DefaultProfileConfig() }

// backupHome deliberately rejects project and cluster scope. A backup snapshots
// the full <home>/.carbon management plane, so allowing a project-bound request
// to reach it would let a project UI alter an unrelated home-level destination.
func (s *Server) backupHome(r *http.Request) (*home.Home, home.Manifest, error) {
	scope, err := s.resolveScope(r)
	if err != nil {
		return nil, home.Manifest{}, err
	}
	if scope.Mode != "carbon" || strings.TrimSpace(scope.Home) == "" || scope.ClusterID != "" || scope.ProjectID != "" {
		return nil, home.Manifest{}, errBackupHomeOnly
	}
	h, err := home.Open(scope.Home)
	if err != nil {
		return nil, home.Manifest{}, err
	}
	manifest, err := h.Manifest()
	if err != nil {
		return nil, home.Manifest{}, err
	}
	return h, manifest, nil
}

func backupConfigPath(h *home.Home) string { return filepath.Join(h.CarbonRoot, backupConfigFilename) }

func loadBackupConfig(h *home.Home) (backup.ProfileConfigFile, error) {
	return backup.LoadProfileConfigFile(backupConfigPath(h))
}

func saveBackupConfig(h *home.Home, config backup.ProfileConfigFile) error {
	return backup.SaveProfileConfigFile(backupConfigPath(h), config)
}

func localBackupRepository(h *home.Home) (*backup.Repository, error) {
	local, err := backup.NewLocalBlobStore(filepath.Join(h.CarbonRoot, "backups", "local"))
	if err != nil {
		return nil, err
	}
	return backup.NewRepository(local, backup.DefaultAppVersion)
}

// scheduledBackupRemoteSync is the only automatic-provider path. It uses the
// config mutex only to read/register an authorized generation and to commit a
// still-current successful upload. Provider I/O runs on a cancelable context
// without that mutex, so revocation can persist, invalidate, and cancel it
// immediately. Its caller has already created and verified the local snapshot.
func scheduledBackupRemoteSync(h *home.Home) backup.ScheduledRemoteSync {
	homeKey := filepath.Clean(h.CarbonRoot)
	return func(ctx context.Context, request backup.ScheduledRemoteSyncRequest) (backup.ScheduledRemoteSyncResult, error) {
		if request.Repository == nil {
			return backup.ScheduledRemoteSyncResult{}, errors.New("backup scheduled remote sync has no local repository")
		}
		backupConfigMu.Lock()
		config, err := loadBackupConfig(h)
		if err != nil {
			backupConfigMu.Unlock()
			return backup.ScheduledRemoteSyncResult{}, err
		}
		// Do not trust the profile captured by the scheduler before the local
		// create/verify operation. A user may have revoked authorization while
		// that work ran; this fresh read is still entirely local.
		if !config.Profile.ContinuousSyncEnabled() {
			backupConfigMu.Unlock()
			return backup.ScheduledRemoteSyncResult{Skipped: true}, nil
		}
		destination, err := backup.RemoteDestinationFingerprint(config.Profile)
		if err != nil {
			backupConfigMu.Unlock()
			return backup.ScheduledRemoteSyncResult{}, err
		}
		profile := config.Profile
		syncContext, generation, finish := backupRemoteSyncs.begin(homeKey, ctx)
		backupConfigMu.Unlock()
		defer finish()

		if syncContext.Err() != nil {
			return backup.ScheduledRemoteSyncResult{Skipped: true}, nil
		}
		remote, err := backup.NewEncryptedRemoteBlobStore(syncContext, profile)
		if err != nil {
			return scheduledBackupRemoteSyncError(h, homeKey, generation, destination, request.At, syncContext, err)
		}
		if err := request.Repository.Upload(syncContext, request.Snapshot, remote, backup.UploadOptions{Enabled: true}); err != nil {
			return scheduledBackupRemoteSyncError(h, homeKey, generation, destination, request.At, syncContext, err)
		}
		uploadedAt := request.At
		if uploadedAt.IsZero() {
			uploadedAt = time.Now()
		}
		backupConfigMu.Lock()
		if syncContext.Err() != nil {
			backupConfigMu.Unlock()
			return backup.ScheduledRemoteSyncResult{Skipped: true}, nil
		}
		current, currentErr := scheduledBackupRemoteSyncCurrent(h, homeKey, generation, destination)
		if currentErr != nil {
			backupConfigMu.Unlock()
			return backup.ScheduledRemoteSyncResult{}, currentErr
		}
		if !current {
			backupConfigMu.Unlock()
			return backup.ScheduledRemoteSyncResult{Skipped: true}, nil
		}
		if _, err := backup.MarkProfileUpload(backupConfigPath(h), uploadedAt); err != nil {
			backupConfigMu.Unlock()
			return backup.ScheduledRemoteSyncResult{}, err
		}
		if err := recordScheduledRemoteSuccess(h, request.Snapshot.ID, destination, uploadedAt); err != nil {
			backupConfigMu.Unlock()
			return backup.ScheduledRemoteSyncResult{}, err
		}
		backupConfigMu.Unlock()
		return backup.ScheduledRemoteSyncResult{
			DestinationFingerprint: destination,
			UploadedAt:             uploadedAt,
			RemoteStateRecorded:    true,
		}, nil
	}
}

// scheduledBackupRemoteSyncCurrent must run while backupConfigMu is held. It
// makes only local reads and ensures a completed/reconfigured authorization
// cannot receive success or failure bookkeeping from an older upload.
func scheduledBackupRemoteSyncCurrent(h *home.Home, homeKey string, generation uint64, destination string) (bool, error) {
	if !backupRemoteSyncs.current(homeKey, generation) {
		return false, nil
	}
	config, err := loadBackupConfig(h)
	if err != nil {
		return false, err
	}
	if !config.Profile.ContinuousSyncEnabled() {
		return false, nil
	}
	currentDestination, err := backup.RemoteDestinationFingerprint(config.Profile)
	if err != nil {
		return false, err
	}
	return currentDestination == destination, nil
}

func scheduledBackupRemoteSyncError(h *home.Home, homeKey string, generation uint64, destination string, attemptedAt time.Time, syncContext context.Context, remoteErr error) (backup.ScheduledRemoteSyncResult, error) {
	// A cancellation is normally a revocation. It is not a remote failure and
	// must not overwrite status after the authorization has been cleared.
	if syncContext.Err() != nil || errors.Is(remoteErr, context.Canceled) {
		return backup.ScheduledRemoteSyncResult{Skipped: true}, nil
	}
	backupConfigMu.Lock()
	defer backupConfigMu.Unlock()
	if syncContext.Err() != nil {
		return backup.ScheduledRemoteSyncResult{Skipped: true}, nil
	}
	current, err := scheduledBackupRemoteSyncCurrent(h, homeKey, generation, destination)
	if err != nil {
		return backup.ScheduledRemoteSyncResult{}, err
	}
	if !current {
		return backup.ScheduledRemoteSyncResult{Skipped: true}, nil
	}
	if err := recordScheduledRemoteFailure(h, destination, attemptedAt); err != nil {
		return backup.ScheduledRemoteSyncResult{}, err
	}
	return backup.ScheduledRemoteSyncResult{RemoteFailureRecorded: true}, nil
}

// recordScheduledRemoteSuccess and recordScheduledRemoteFailure run while the
// callback still holds backupConfigMu and has confirmed its generation. They
// make the remote portion of runtime state inseparable from that authorization
// decision, so a revoke cannot be followed by a stale scheduler state write.
func recordScheduledRemoteSuccess(h *home.Home, snapshotID, destination string, uploadedAt time.Time) error {
	state, err := backup.LoadRuntimeState(backup.RuntimeStatePath(h.CarbonRoot))
	if err != nil {
		return err
	}
	state.LastRemoteSnapshotID = snapshotID
	state.LastRemoteSnapshotAt = uploadedAt.UTC().Format(time.RFC3339Nano)
	state.LastRemoteDestination = destination
	return backup.SaveRuntimeState(backup.RuntimeStatePath(h.CarbonRoot), state)
}

func recordScheduledRemoteFailure(h *home.Home, destination string, attemptedAt time.Time) error {
	state, err := backup.LoadRuntimeState(backup.RuntimeStatePath(h.CarbonRoot))
	if err != nil {
		return err
	}
	if attemptedAt.IsZero() {
		attemptedAt = time.Now()
	}
	state.LastRemoteFailureAt = attemptedAt.UTC().Format(time.RFC3339Nano)
	state.LastRemoteFailureDestination = destination
	state.LastRemoteError = backup.RedactedRemoteSyncError
	return backup.SaveRuntimeState(backup.RuntimeStatePath(h.CarbonRoot), state)
}

func localBackupScheduler(h *home.Home, manifest home.Manifest) (*backup.LocalScheduler, error) {
	return backup.NewLocalScheduler(backup.LocalSchedulerOptions{
		CarbonRoot:          h.CarbonRoot,
		SourceDir:           h.CarbonRoot,
		SourceID:            manifest.ID,
		AppVersion:          backup.DefaultAppVersion,
		ScheduledRemoteSync: scheduledBackupRemoteSync(h),
	})
}

// StartLocalBackupScheduler is the server-host lifecycle hook. A caller that
// owns the web/CLI process should retain the returned scheduler and cancel the
// supplied context at shutdown. Construction never resolves a remote profile,
// credential reference, or provider. A scheduled run can publish only after a
// saved, enabled, encrypted profile has separately granted continuous consent.
func StartLocalBackupScheduler(ctx context.Context, homeRoot string) (*backup.LocalScheduler, error) {
	h, err := home.Open(homeRoot)
	if err != nil {
		return nil, err
	}
	manifest, err := h.Manifest()
	if err != nil {
		return nil, err
	}
	scheduler, err := localBackupScheduler(h, manifest)
	if err != nil {
		return nil, err
	}
	if err := scheduler.Start(ctx); err != nil {
		return nil, err
	}
	return scheduler, nil
}

// createVerifiedHomeSnapshot is the migration guardrail. It snapshots only the trusted
// Carbon metadata root, verifies the immutable local snapshot before any migration
// write, and returns the snapshot id for the migration receipt. A failed create or
// verify stops the caller before it can mutate the home. It never creates a remote
// client or resolves a credential reference.
func createVerifiedHomeSnapshot(ctx context.Context, homeRoot string) (backup.Snapshot, error) {
	h, err := home.Ensure(homeRoot)
	if err != nil {
		return backup.Snapshot{}, err
	}
	manifest, err := h.Manifest()
	if err != nil {
		return backup.Snapshot{}, err
	}
	repository, err := localBackupRepository(h)
	if err != nil {
		return backup.Snapshot{}, err
	}
	snapshot, err := repository.Create(ctx, backup.CreateOptions{
		SourceDir: h.CarbonRoot, SourceID: manifest.ID, AppVersion: backup.DefaultAppVersion,
	})
	if err != nil {
		return backup.Snapshot{}, err
	}
	if _, err := repository.Verify(ctx, snapshot); err != nil {
		return backup.Snapshot{}, fmt.Errorf("verify pre-migration snapshot %s: %w", snapshot.ID, err)
	}
	return snapshot, nil
}

func (s *Server) handleBackupConfigGet(w http.ResponseWriter, r *http.Request) {
	h, _, err := s.backupHome(r)
	if err != nil {
		writeBackupHomeErr(w, err)
		return
	}
	config, err := loadBackupConfig(h)
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, backupConfigDTO{Profile: config.Profile, Local: config.Local})
}

func (s *Server) handleBackupConfigPut(w http.ResponseWriter, r *http.Request) {
	h, _, err := s.backupHome(r)
	if err != nil {
		writeBackupHomeErr(w, err)
		return
	}
	var request backupConfigPutDTO
	if !decodeStrictBackupJSON(w, r, &request) {
		return
	}
	if request.Profile == nil {
		writeJSON(w, http.StatusUnprocessableEntity, errBody(errors.New("backup profile is required")))
		return
	}
	backupConfigMu.Lock()
	config, err := loadBackupConfig(h)
	if err != nil {
		backupConfigMu.Unlock()
		writeBackupConfigErr(w, err)
		return
	}
	previousProfile := config.Profile
	// Retain server-owned values. Continuous authorization has a separate
	// confirm-only endpoint, so an ordinary profile save cannot silently grant
	// it (or revoke it by accidentally saving a stale form).
	if request.Profile.ContinuousAuthorization && !config.Profile.ContinuousAuthorization {
		backupConfigMu.Unlock()
		writeJSON(w, http.StatusUnprocessableEntity, errBody(errors.New("continuous backup authorization requires explicit confirmation")))
		return
	}
	request.Profile.ContinuousAuthorization = config.Profile.ContinuousAuthorization
	config.Profile = *request.Profile
	if request.Local != nil {
		config.Local = *request.Local
	}
	if err := backup.NormalizeRemoteProfile(&config.Profile); err != nil {
		backupConfigMu.Unlock()
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
		return
	}
	if err := backup.NormalizeLocalSchedule(&config.Local); err != nil {
		backupConfigMu.Unlock()
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
		return
	}
	if err := saveBackupConfig(h, config); err != nil {
		backupConfigMu.Unlock()
		writeBackupConfigErr(w, err)
		return
	}
	// Persist the replacement before advancing the generation. A scheduled
	// callback beginning after this point can observe only the new profile; an
	// older callback is denied both success and failure bookkeeping when it
	// reaches its post-provider current-generation check. Cancels themselves
	// run after this lock is released so a blocked provider cannot delay the
	// configuration response.
	var cancels []context.CancelFunc
	if backupRemoteSyncProfileChanged(previousProfile, config.Profile) {
		cancels = backupRemoteSyncs.invalidate(filepath.Clean(h.CarbonRoot))
	}
	backupConfigMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	writeJSON(w, http.StatusOK, backupConfigDTO{Profile: config.Profile, Local: config.Local})
}

func (s *Server) handleBackupStatus(w http.ResponseWriter, r *http.Request) {
	h, manifest, err := s.backupHome(r)
	if err != nil {
		writeBackupHomeErr(w, err)
		return
	}
	config, err := loadBackupConfig(h)
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	state, err := backup.LoadRuntimeState(backup.RuntimeStatePath(h.CarbonRoot))
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	configured := config.Profile.Configured()
	writeJSON(w, http.StatusOK, backupStatusDTO{
		SourceID: manifest.ID,
		Source:   "<home>/.carbon",
		Local: backupLocalStatusDTO{
			Configured:     true,
			Enabled:        config.Local.Enabled,
			Operational:    true,
			IntervalHours:  config.Local.IntervalHours,
			OnStart:        config.Local.OnStart,
			KeepLast:       config.Local.KeepLast,
			KeepDays:       config.Local.KeepDays,
			LastRunAt:      state.LastRunAt,
			LastSuccessAt:  state.LastSuccessAt,
			LastSnapshotID: state.LastSnapshotID,
			LastSnapshotAt: state.LastSnapshotAt,
			LastPruneAt:    state.LastPruneAt,
			LastPruned:     state.LastPruned,
		},
		Remote: backupRemoteStatusDTO{
			Configured:              configured,
			Enabled:                 config.Profile.Enabled,
			ContinuousAuthorization: config.Profile.ContinuousAuthorization,
			// Status must be zero-network. It reports only locally persisted
			// scheduler state and never probes the provider.
			Operational:          false,
			LastUpload:           config.LastUpload,
			LastRemoteSnapshotID: state.LastRemoteSnapshotID,
			LastRemoteSnapshotAt: state.LastRemoteSnapshotAt,
			LastRemoteFailureAt:  state.LastRemoteFailureAt,
			LastRemoteError:      state.LastRemoteError,
		},
	})
}

func (s *Server) handleBackupList(w http.ResponseWriter, r *http.Request) {
	h, _, err := s.backupHome(r)
	if err != nil {
		writeBackupHomeErr(w, err)
		return
	}
	repository, err := localBackupRepository(h)
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	list, err := repository.ListSnapshots(r.Context())
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": list})
}

func (s *Server) handleBackupCreate(w http.ResponseWriter, r *http.Request) {
	h, manifest, err := s.backupHome(r)
	if err != nil {
		writeBackupHomeErr(w, err)
		return
	}
	scheduler, err := localBackupScheduler(h, manifest)
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	// SourceDir is fixed from a trusted Home handle. The client supplies no filesystem
	// path, so it cannot turn a Carbon snapshot request into arbitrary local exfiltration.
	result, err := scheduler.RunNow(r.Context())
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result.Snapshot)
}

// handleBackupRunNow is the richer run-now endpoint for the settings UI. The
// older /snapshots route above remains compatible and returns just Snapshot.
func (s *Server) handleBackupRunNow(w http.ResponseWriter, r *http.Request) {
	h, manifest, err := s.backupHome(r)
	if err != nil {
		writeBackupHomeErr(w, err)
		return
	}
	scheduler, err := localBackupScheduler(h, manifest)
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	result, err := scheduler.RunNow(r.Context())
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleBackupPrune runs only the local retention policy. It does not create a
// remote store, resolve credentials, or delete remote or shared local objects.
func (s *Server) handleBackupPrune(w http.ResponseWriter, r *http.Request) {
	h, manifest, err := s.backupHome(r)
	if err != nil {
		writeBackupHomeErr(w, err)
		return
	}
	scheduler, err := localBackupScheduler(h, manifest)
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	result, err := scheduler.PruneNow(r.Context())
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleBackupContinuousAuthorization is intentionally separate from profile
// save. It changes only local configuration and needs an explicit confirm=true
// acknowledgement before a future scheduled run may use the remote profile.
// Revocation also invalidates and cancels a currently running scheduled sync.
func (s *Server) handleBackupContinuousAuthorization(w http.ResponseWriter, r *http.Request) {
	var request backupContinuousAuthorizationRequest
	if !decodeStrictBackupJSON(w, r, &request) {
		return
	}
	if !request.Confirm {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("continuous backup authorization requires confirm=true")))
		return
	}
	if s.allowRemote {
		writeJSON(w, http.StatusForbidden, errBody(errors.New("continuous backup authorization is disabled when remote server access is enabled")))
		return
	}
	h, _, err := s.backupHome(r)
	if err != nil {
		writeBackupHomeErr(w, err)
		return
	}
	backupConfigMu.Lock()
	config, err := backup.SetContinuousAuthorization(backupConfigPath(h), request.Enabled)
	if err != nil {
		backupConfigMu.Unlock()
		writeBackupConfigErr(w, err)
		return
	}
	var cancels []context.CancelFunc
	if !request.Enabled {
		// Persist the revocation before invalidating the generation. New
		// scheduled callbacks cannot register as authorized after this point;
		// existing callbacks receive their cancellation after the lock is free.
		cancels = backupRemoteSyncs.invalidate(filepath.Clean(h.CarbonRoot))
	}
	backupConfigMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	writeJSON(w, http.StatusOK, backupConfigDTO{Profile: config.Profile, Local: config.Local})
}

func (s *Server) handleBackupUpload(w http.ResponseWriter, r *http.Request) {
	var request backupUploadRequest
	if !decodeStrictBackupJSON(w, r, &request) {
		return
	}
	if !request.Confirm {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("backup upload requires confirm=true")))
		return
	}
	// A server deliberately opened for remote browser access must never expose a
	// route that resolves cloud credentials or publishes a whole home snapshot.
	if s.allowRemote {
		writeJSON(w, http.StatusForbidden, errBody(errors.New("backup upload is disabled when remote server access is enabled")))
		return
	}
	h, _, err := s.backupHome(r)
	if err != nil {
		writeBackupHomeErr(w, err)
		return
	}

	backupConfigMu.Lock()
	defer backupConfigMu.Unlock()
	config, err := loadBackupConfig(h)
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	if !config.Profile.Enabled {
		writeJSON(w, http.StatusForbidden, errBody(backup.ErrRemoteDisabled))
		return
	}
	// Verify the local graph before resolving either credential/key reference.
	// This makes malformed IDs and corrupt local snapshots fail without any
	// remote client construction or network request.
	repository, err := localBackupRepository(h)
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	snapshot := backup.Snapshot{ID: r.PathValue("id")}
	if _, err := repository.Verify(r.Context(), snapshot); err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	remote, err := backup.NewEncryptedRemoteBlobStore(r.Context(), config.Profile)
	if err != nil {
		writeBackupUploadErr(w, err)
		return
	}
	if err := repository.Upload(r.Context(), snapshot, remote, backup.UploadOptions{Enabled: true}); err != nil {
		writeBackupUploadErr(w, err)
		return
	}
	// Repository.Upload performs a full remote read-back verification. Only now
	// may the private state document atomically gain a LastUpload timestamp.
	updated, err := backup.MarkProfileUpload(backupConfigPath(h), time.Now())
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"snapshot": snapshot, "uploaded": true, "verified": true, "lastUpload": updated.LastUpload,
	})
}

func (s *Server) handleBackupVerify(w http.ResponseWriter, r *http.Request) {
	h, _, err := s.backupHome(r)
	if err != nil {
		writeBackupHomeErr(w, err)
		return
	}
	repository, err := localBackupRepository(h)
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	snapshot := backup.Snapshot{ID: r.PathValue("id")}
	manifest, err := repository.Verify(r.Context(), snapshot)
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshot": snapshot, "manifest": manifest, "verified": true})
}

// Restore-plan deliberately verifies only. It never accepts a target or staging path and
// never writes a restore tree; a separate, explicitly reviewed recovery workflow owns
// any later staging/replacement action.
func (s *Server) handleBackupRestorePlan(w http.ResponseWriter, r *http.Request) {
	h, _, err := s.backupHome(r)
	if err != nil {
		writeBackupHomeErr(w, err)
		return
	}
	repository, err := localBackupRepository(h)
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	snapshot := backup.Snapshot{ID: r.PathValue("id")}
	manifest, err := repository.Verify(r.Context(), snapshot)
	if err != nil {
		writeBackupConfigErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"snapshot": snapshot, "verified": true, "files": len(manifest.Files),
		"restore": "no files written; restoration must use a new controlled staging directory",
	})
}

func decodeStrictBackupJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	if r.Body == nil {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("backup request body is required")))
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errBody(fmt.Errorf("JSON body exceeds %d bytes", maxJSONBodyBytes)))
		} else {
			writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("invalid backup JSON: %w", err)))
		}
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			writeJSON(w, http.StatusBadRequest, errBody(errors.New("invalid backup JSON: multiple values")))
		} else {
			writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("invalid backup JSON: %w", err)))
		}
		return false
	}
	return true
}

func writeBackupHomeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errBackupHomeOnly) {
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
		return
	}
	writeHomeErr(w, err)
}

func writeBackupConfigErr(w http.ResponseWriter, err error) {
	// Configuration validation errors are client-fixable; filesystem and local
	// snapshot errors retain the generic safe server response.
	if strings.HasPrefix(err.Error(), "backup ") || strings.HasPrefix(err.Error(), "configured backup") || strings.HasPrefix(err.Error(), "enabled backup") || strings.HasPrefix(err.Error(), "unsupported backup") {
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
		return
	}
	writeErr(w, err)
}

func writeBackupUploadErr(w http.ResponseWriter, err error) {
	if errors.Is(err, backup.ErrRemoteDisabled) {
		writeJSON(w, http.StatusForbidden, errBody(err))
		return
	}
	// Provider errors can contain an endpoint path or vendor diagnostic. Never
	// reflect those diagnostics through a local API response; they are not useful
	// to a caller and could accidentally expose contextual credential details.
	writeJSON(w, http.StatusBadGateway, errBody(errors.New("backup upload failed")))
}
