package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestProfileConfigV1MigratesToV2LocalDefaults(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "backup.json")
	if err := os.WriteFile(filename, []byte(`{"version":1,"profile":{"backend":"s3","enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadProfileConfigFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if config.Version != ProfileConfigVersion || config.Local != DefaultLocalSchedule() {
		t.Fatalf("v1 migration = %+v, want v%d local defaults %+v", config, ProfileConfigVersion, DefaultLocalSchedule())
	}
	if config.Profile.ContinuousAuthorization {
		t.Fatal("v1 migration granted continuous authorization")
	}
	if err := SaveProfileConfigFile(filename, config); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version": 2`) || !strings.Contains(string(data), `"local"`) {
		t.Fatalf("saved v2 configuration = %s", data)
	}
}

func TestRuntimeStateIsPrivateAtomicAndStrict(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "backups", "state.json")
	state := DefaultRuntimeState()
	state.LastRunAt = "2026-08-08T12:00:00Z"
	if err := SaveRuntimeState(filename, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRuntimeState(filename)
	if err != nil || loaded.LastRunAt != state.LastRunAt {
		t.Fatalf("load runtime state = %+v, %v", loaded, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filename)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("state permissions = %04o, want 0600", info.Mode().Perm())
		}
	}
	if err := os.WriteFile(filename, []byte(`{"version":1,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeState(filename); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("invalid runtime state was accepted: %v", err)
	}
}

func TestLocalSchedulerReusesUnchangedManifestAndNeverResolvesRemote(t *testing.T) {
	carbonRoot := filepath.Join(t.TempDir(), ".carbon")
	writeTestFile(t, filepath.Join(carbonRoot, "tasks", "snapshot.md"), []byte("first"), 0o600)
	config := DefaultProfileConfig()
	config.Profile = RemoteProfile{
		Backend:               "s3",
		Enabled:               true,
		Bucket:                "backup-bucket",
		Region:                "us-east-1",
		Endpoint:              "http://127.0.0.1:1",
		UsePathStyle:          true,
		AllowInsecureEndpoint: true,
		CredentialRef:         "env://MISSING_SCHEDULER_CREDENTIAL",
		Encryption:            true,
		EncryptionKeyRef:      "env://MISSING_SCHEDULER_KEY",
	}
	if err := SaveProfileConfigFile(filepath.Join(carbonRoot, "backup.json"), config); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	scheduler, err := NewLocalScheduler(LocalSchedulerOptions{
		CarbonRoot: carbonRoot,
		SourceID:   "home-test",
		AppVersion: "test",
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := scheduler.RunNow(context.Background())
	if err != nil || !first.Created || first.Snapshot.ID == "" {
		t.Fatalf("first local run = %+v, %v", first, err)
	}
	now = now.Add(7 * time.Hour)
	second, err := scheduler.RunNow(context.Background())
	if err != nil || second.Created || second.Snapshot.ID != first.Snapshot.ID {
		t.Fatalf("unchanged local run = %+v, %v; first=%+v", second, err, first)
	}
	store, err := NewLocalBlobStore(filepath.Join(carbonRoot, "backups", "local"))
	if err != nil {
		t.Fatal(err)
	}
	manifests, err := store.List(context.Background(), "manifests/sha256/")
	if err != nil || len(manifests) != 1 {
		t.Fatalf("local manifests = %+v, %v", manifests, err)
	}
	state, err := scheduler.State()
	if err != nil || state.LastSnapshotID != first.Snapshot.ID || state.LastSuccessAt == "" {
		t.Fatalf("scheduler state = %+v, %v", state, err)
	}
}

func TestPruneLocalRetainsSharedReachableObjectsAndFailsClosed(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalBlobStore(filepath.Join(root, "local"))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(store, "test")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	writeTestFile(t, filepath.Join(source, "shared.md"), []byte("shared"), 0o600)
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	repository.now = func() time.Time { return now }
	for _, content := range []string{"first", "second", "third"} {
		writeTestFile(t, filepath.Join(source, "changed.md"), []byte(content), 0o600)
		if _, err := repository.Create(context.Background(), CreateOptions{SourceDir: source, SourceID: "home", AppVersion: "test"}); err != nil {
			t.Fatal(err)
		}
		now = now.Add(24 * time.Hour)
	}
	result, err := repository.PruneLocalAt(context.Background(), RetentionPolicy{KeepLast: 1, KeepDays: 1}, now.Add(31*24*time.Hour))
	if err != nil || result.Pruned != 2 || result.Retained != 1 {
		t.Fatalf("prune result = %+v, %v", result, err)
	}
	if _, _, err := store.Get(context.Background(), ObjectKey(SHA256Hex([]byte("shared")))); err != nil {
		t.Fatalf("shared reachable object was not preserved: %v", err)
	}
	if _, _, err := store.Get(context.Background(), ObjectKey(SHA256Hex([]byte("first")))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unreachable object was not reclaimed: %v", err)
	}

	// A recognized manifest key with invalid JSON makes List (and therefore
	// retention) fail before it can delete any further valid manifest.
	bad := []byte("not a manifest")
	badID := SHA256Hex(bad)
	if _, _, err := store.PutIfAbsent(context.Background(), ManifestKey(badID), bad, PutOptions{SHA256: badID}); err != nil {
		t.Fatal(err)
	}
	before, err := store.List(context.Background(), "manifests/sha256/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PruneLocalAt(context.Background(), RetentionPolicy{KeepLast: 1, KeepDays: 1}, now.Add(60*24*time.Hour)); err == nil {
		t.Fatal("malformed manifest did not fail closed")
	}
	after, err := store.List(context.Background(), "manifests/sha256/")
	if err != nil || len(after) != len(before) {
		t.Fatalf("retention deleted after malformed manifest: before=%d after=%d err=%v", len(before), len(after), err)
	}
}

func TestLocalHomeLockSerializesSchedulers(t *testing.T) {
	root := t.TempDir()
	first, err := AcquireLocalHomeLock(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release() //nolint:errcheck // test cleanup
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	second, err := AcquireLocalHomeLock(ctx, root)
	if second != nil {
		_ = second.Release()
		t.Fatal("second local home lock unexpectedly acquired")
	}
	if !errors.Is(err, ErrHomeLockTimeout) {
		t.Fatalf("second local home lock = %v, want timeout", err)
	}
}

func TestLocalSchedulerStartRunsOnStart(t *testing.T) {
	carbonRoot := filepath.Join(t.TempDir(), ".carbon")
	writeTestFile(t, filepath.Join(carbonRoot, "tasks", "snapshot.md"), []byte("on-start"), 0o600)
	if err := SaveProfileConfigFile(filepath.Join(carbonRoot, "backup.json"), DefaultProfileConfig()); err != nil {
		t.Fatal(err)
	}
	contextForScheduler, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler, err := NewLocalScheduler(LocalSchedulerOptions{CarbonRoot: carbonRoot, SourceID: "home", AppVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Start(contextForScheduler); err != nil {
		t.Fatal(err)
	}
	defer scheduler.Stop()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, err := scheduler.State()
		if err == nil && state.LastSnapshotID != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("on-start scheduler did not create a local snapshot")
}

func TestContinuousAuthorizationRequiresConfiguredEnabledRemote(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "backup.json")
	if err := SaveProfileConfigFile(filename, DefaultProfileConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := SetContinuousAuthorization(filename, true); err == nil {
		t.Fatal("unconfigured remote was continuously authorized")
	}
	config := DefaultProfileConfig()
	config.Profile = RemoteProfile{
		Backend:               "s3",
		Enabled:               true,
		Bucket:                "backup-bucket",
		Region:                "us-east-1",
		Endpoint:              "http://127.0.0.1:1",
		UsePathStyle:          true,
		AllowInsecureEndpoint: true,
		CredentialRef:         "env://AUTHORIZATION_TEST",
		Encryption:            true,
		EncryptionKeyRef:      "env://AUTHORIZATION_KEY",
	}
	if err := SaveProfileConfigFile(filename, config); err != nil {
		t.Fatal(err)
	}
	updated, err := SetContinuousAuthorization(filename, true)
	if err != nil || !updated.Profile.ContinuousAuthorization {
		t.Fatalf("continuous authorization = %+v, %v", updated, err)
	}
}
