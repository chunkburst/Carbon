package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"carbon/internal/backup"
	"carbon/internal/home"
)

func TestBackupConfigAndLocalSnapshotsNeverTouchRemote(t *testing.T) {
	root := t.TempDir()
	homeHandle, err := home.Ensure(root)
	if err != nil {
		t.Fatal(err)
	}
	remote := newBackupS3Server(t)
	defer remote.Close()
	setBackupUploadEnvironment(t)

	server := NewWithScope("human:test", ScopeDefaults{Home: root, HomeByDefault: true})
	handler := server.Handler()
	profile := testBackupProfile(remote.URL, true)
	putBackupProfile(t, handler, profile, http.StatusOK)
	if calls := remote.Calls(); calls != 0 {
		t.Fatalf("config PUT made %d remote calls", calls)
	}
	if code, body := raw(handler, http.MethodGet, "/api/backup/config", ""); code != http.StatusOK || strings.Contains(body, "lastUpload") {
		t.Fatalf("config GET = %d %s", code, body)
	}
	if code, body := raw(handler, http.MethodGet, "/api/backup/status", ""); code != http.StatusOK || !strings.Contains(body, `"operational":false`) {
		t.Fatalf("status = %d %s", code, body)
	}
	if calls := remote.Calls(); calls != 0 {
		t.Fatalf("config/status made %d remote calls", calls)
	}

	code, body := raw(handler, http.MethodPost, "/api/backup/snapshots", "")
	if code != http.StatusCreated {
		t.Fatalf("local snapshot = %d %s", code, body)
	}
	var snapshot backup.Snapshot
	if err := json.Unmarshal([]byte(body), &snapshot); err != nil {
		t.Fatal(err)
	}
	if calls := remote.Calls(); calls != 0 {
		t.Fatalf("local snapshot made %d remote calls", calls)
	}

	repository, err := localBackupRepository(homeHandle)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := repository.LoadManifest(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range manifest.Files {
		if file.Path == "backup.json" || file.Path == ".carbon/backup.json" {
			t.Fatalf("backup config leaked into manifest: %+v", manifest.Files)
		}
	}

	code, body = raw(handler, http.MethodGet, "/api/backup/snapshots", "")
	if code != http.StatusOK || !strings.Contains(body, `"snapshot"`) || !strings.Contains(body, `"manifest"`) || strings.Contains(body, `"Snapshot"`) || strings.Contains(body, `"Manifest"`) {
		t.Fatalf("snapshot list DTO = %d %s", code, body)
	}
	if calls := remote.Calls(); calls != 0 {
		t.Fatalf("local snapshot list made %d remote calls", calls)
	}
}

func TestBackupConfigRejectsSecretsUnknownFieldsAndForgedLastUpload(t *testing.T) {
	root := t.TempDir()
	if _, err := home.Ensure(root); err != nil {
		t.Fatal(err)
	}
	server := NewWithScope("human:test", ScopeDefaults{Home: root, HomeByDefault: true})
	handler := server.Handler()
	profile := testBackupProfile("http://127.0.0.1:1", false)
	encoded, err := json.Marshal(map[string]any{"profile": profile})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"accessKey", "secretKey", "token"} {
		var request map[string]any
		if err := json.Unmarshal(encoded, &request); err != nil {
			t.Fatal(err)
		}
		request["profile"].(map[string]any)[field] = "raw-secret"
		body, _ := json.Marshal(request)
		if code, got := raw(handler, http.MethodPut, "/api/backup/config", string(body)); code != http.StatusBadRequest || !strings.Contains(got, "unknown field") {
			t.Fatalf("raw %s accepted = %d %s", field, code, got)
		}
	}
	forged := `{"profile":{"backend":"s3","enabled":false},"lastUpload":"2099-01-01T00:00:00Z"}`
	if code, body := raw(handler, http.MethodPut, "/api/backup/config", forged); code != http.StatusBadRequest || !strings.Contains(body, "unknown field") {
		t.Fatalf("forged lastUpload accepted = %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(root, ".carbon", backupConfigFilename)); !os.IsNotExist(err) {
		t.Fatalf("rejected config wrote backup.json: %v", err)
	}
}

func TestBackupV2LocalRunAuthorizationAndStatusStayOffline(t *testing.T) {
	root := t.TempDir()
	if _, err := home.Ensure(root); err != nil {
		t.Fatal(err)
	}
	remote := newBackupS3Server(t)
	defer remote.Close()
	server := NewWithScope("human:test", ScopeDefaults{Home: root, HomeByDefault: true})
	handler := server.Handler()
	putBackupProfile(t, handler, testBackupProfile(remote.URL, true), http.StatusOK)
	if calls := remote.Calls(); calls != 0 {
		t.Fatalf("v2 config save made %d remote calls", calls)
	}

	code, body := raw(handler, http.MethodGet, "/api/backup/config", "")
	if code != http.StatusOK || !strings.Contains(body, `"intervalHours":6`) || !strings.Contains(body, `"keepLast":30`) || !strings.Contains(body, `"keepDays":30`) {
		t.Fatalf("v2 config defaults = %d %s", code, body)
	}
	if calls := remote.Calls(); calls != 0 {
		t.Fatalf("v2 config GET made %d remote calls", calls)
	}

	// The generic config endpoint may not grant continuous authorization, even
	// when its JSON shape includes the field for status display.
	profile := testBackupProfile(remote.URL, true)
	profile.ContinuousAuthorization = true
	encoded, err := json.Marshal(map[string]any{"profile": profile})
	if err != nil {
		t.Fatal(err)
	}
	if code, body := raw(handler, http.MethodPut, "/api/backup/config", string(encoded)); code != http.StatusUnprocessableEntity || !strings.Contains(body, "explicit confirmation") {
		t.Fatalf("implicit continuous authorization = %d %s", code, body)
	}

	// The run-now handler is intentionally not route-wired in this unit: it
	// proves the handler path itself remains local even with a valid enabled
	// remote profile whose endpoint would record any provider request.
	runRequest := httptest.NewRequest(http.MethodPost, "/api/backup/run-now", nil)
	runRecorder := httptest.NewRecorder()
	server.handleBackupRunNow(runRecorder, runRequest)
	if runRecorder.Code != http.StatusOK {
		t.Fatalf("run-now = %d %s", runRecorder.Code, runRecorder.Body.String())
	}
	var run backup.LocalRunResult
	if err := json.Unmarshal(runRecorder.Body.Bytes(), &run); err != nil || !run.Created || run.Snapshot.ID == "" {
		t.Fatalf("run-now result = %+v, %v", run, err)
	}
	if calls := remote.Calls(); calls != 0 {
		t.Fatalf("run-now made %d remote calls", calls)
	}

	authorizationRequest := httptest.NewRequest(http.MethodPost, "/api/backup/continuous-authorization", strings.NewReader(`{"confirm":true,"enabled":true}`))
	authorizationRecorder := httptest.NewRecorder()
	server.handleBackupContinuousAuthorization(authorizationRecorder, authorizationRequest)
	if authorizationRecorder.Code != http.StatusOK || !strings.Contains(authorizationRecorder.Body.String(), `"continuousAuthorization":true`) {
		t.Fatalf("continuous authorization = %d %s", authorizationRecorder.Code, authorizationRecorder.Body.String())
	}
	if calls := remote.Calls(); calls != 0 {
		t.Fatalf("continuous authorization made %d remote calls", calls)
	}

	code, body = raw(handler, http.MethodGet, "/api/backup/status", "")
	if code != http.StatusOK || !strings.Contains(body, `"continuousAuthorization":true`) || !strings.Contains(body, `"lastSnapshotId"`) {
		t.Fatalf("v2 status = %d %s", code, body)
	}
	if calls := remote.Calls(); calls != 0 {
		t.Fatalf("v2 status made %d remote calls", calls)
	}
}

func TestScheduledBackupSyncRequiresAuthorizationSkipsUnchangedAndPreservesLocalFailure(t *testing.T) {
	root := t.TempDir()
	homeHandle, err := home.Ensure(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := homeHandle.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	remote := newBackupS3Server(t)
	defer remote.Close()
	setBackupUploadEnvironment(t)
	server := NewWithScope("human:test", ScopeDefaults{Home: root, HomeByDefault: true})
	handler := server.Handler()
	putBackupProfile(t, handler, testBackupProfile(remote.URL, true), http.StatusOK)

	scheduler, err := localBackupScheduler(homeHandle, manifest)
	if err != nil {
		t.Fatal(err)
	}
	// The scheduler has an installed provider callback, but a default profile
	// still has no continuous authorization and must make zero provider calls.
	first, err := scheduler.RunScheduled(context.Background())
	if err != nil || first.Snapshot.ID == "" {
		t.Fatalf("default scheduled run = %+v, %v", first, err)
	}
	if calls := remote.Calls(); calls != 0 {
		t.Fatalf("default scheduled run made %d provider calls", calls)
	}

	if code, body := raw(handler, http.MethodPost, "/api/backup/continuous-authorization", `{"confirm":true,"enabled":true}`); code != http.StatusOK || !strings.Contains(body, `"continuousAuthorization":true`) {
		t.Fatalf("authorize scheduled sync = %d %s", code, body)
	}
	if calls := remote.Calls(); calls != 0 {
		t.Fatalf("authorization made %d provider calls", calls)
	}

	beforeSync := remote.Calls()
	synced, err := scheduler.RunScheduled(context.Background())
	if err != nil || synced.Snapshot.ID != first.Snapshot.ID || synced.Created {
		t.Fatalf("authorized unchanged scheduled run = %+v, %v", synced, err)
	}
	if calls := remote.Calls(); calls <= beforeSync {
		t.Fatalf("authorized scheduled run made %d provider calls, want upload", calls)
	}
	for key, object := range remote.Objects() {
		if !bytes.HasPrefix(object.data, []byte("CBEN")) {
			t.Fatalf("scheduled remote object %q is not encrypted: %x", key, object.data[:minBackupTest(len(object.data), 8)])
		}
	}
	state, err := backup.LoadRuntimeState(backup.RuntimeStatePath(homeHandle.CarbonRoot))
	if err != nil || state.LastRemoteSnapshotID != first.Snapshot.ID || state.LastRemoteSnapshotAt == "" {
		t.Fatalf("successful scheduled remote state = %+v, %v", state, err)
	}
	config, err := backup.LoadProfileConfigFile(backupConfigPath(homeHandle))
	if err != nil || config.LastUpload == "" {
		t.Fatalf("scheduled upload did not record lastUpload: %+v, %v", config, err)
	}

	afterSync := remote.Calls()
	if _, err := scheduler.RunScheduled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := remote.Calls(); calls != afterSync {
		t.Fatalf("unchanged destination/content made %d extra provider calls", calls-afterSync)
	}
	previousDestination := state.LastRemoteDestination
	config.Profile.Prefix = "carbon/changed-destination"
	if err := backup.SaveProfileConfigFile(backupConfigPath(homeHandle), config); err != nil {
		t.Fatal(err)
	}
	beforeDestinationChange := remote.Calls()
	if _, err := scheduler.RunScheduled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := remote.Calls(); calls <= beforeDestinationChange {
		t.Fatalf("changed destination made %d provider calls, want a new encrypted sync", calls-beforeDestinationChange)
	}
	state, err = backup.LoadRuntimeState(backup.RuntimeStatePath(homeHandle.CarbonRoot))
	if err != nil || state.LastRemoteDestination == previousDestination {
		t.Fatalf("changed destination state = %+v, %v", state, err)
	}

	if err := os.MkdirAll(filepath.Join(homeHandle.CarbonRoot, "tasks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeHandle.CarbonRoot, "tasks", "remote-failure.md"), []byte("must remain locally recoverable"), 0o600); err != nil {
		t.Fatal(err)
	}
	remote.FailNextRequest()
	beforeFailure := remote.Calls()
	failedRemote, err := scheduler.RunScheduled(context.Background())
	if err != nil || !failedRemote.Created || failedRemote.Snapshot.ID == "" {
		t.Fatalf("remote failure must retain local success = %+v, %v", failedRemote, err)
	}
	if calls := remote.Calls(); calls <= beforeFailure {
		t.Fatalf("failure path made %d provider calls, want one failed upload attempt", calls-beforeFailure)
	}
	repository, err := localBackupRepository(homeHandle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Verify(context.Background(), failedRemote.Snapshot); err != nil {
		t.Fatalf("remote failure damaged local snapshot: %v", err)
	}
	state, err = backup.LoadRuntimeState(backup.RuntimeStatePath(homeHandle.CarbonRoot))
	if err != nil || state.LastSnapshotID != failedRemote.Snapshot.ID || state.LastSuccessAt == "" || state.LastRemoteFailureAt == "" || state.LastRemoteError != backup.RedactedRemoteSyncError {
		t.Fatalf("remote failure state = %+v, %v", state, err)
	}
	if state.LastRemoteSnapshotID != first.Snapshot.ID {
		t.Fatalf("failed sync replaced last verified remote snapshot: %+v", state)
	}

	afterFailure := remote.Calls()
	if _, err := scheduler.RunScheduled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := remote.Calls(); calls != afterFailure {
		t.Fatalf("remote failure retried before one local interval elapsed: %d extra calls", calls-afterFailure)
	}

	if code, body := raw(handler, http.MethodPost, "/api/backup/continuous-authorization", `{"confirm":true,"enabled":false}`); code != http.StatusOK || !strings.Contains(body, `"continuousAuthorization":false`) {
		t.Fatalf("revoke scheduled sync = %d %s", code, body)
	}
	if err := os.WriteFile(filepath.Join(homeHandle.CarbonRoot, "tasks", "revoked.md"), []byte("no provider after revocation"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeRevokeRun := remote.Calls()
	if _, err := scheduler.RunScheduled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := remote.Calls(); calls != beforeRevokeRun {
		t.Fatalf("revoked authorization made %d provider calls", calls-beforeRevokeRun)
	}

	if code, body := raw(handler, http.MethodGet, "/api/backup/status", ""); code != http.StatusOK || !strings.Contains(body, `"lastRemoteSnapshotAt"`) || !strings.Contains(body, `"lastRemoteFailureAt"`) || strings.Contains(body, remote.URL) {
		t.Fatalf("remote scheduler status = %d %s", code, body)
	}
}

func TestScheduledBackupRevocationCancelsInFlightRemoteSync(t *testing.T) {
	root := t.TempDir()
	homeHandle, err := home.Ensure(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := homeHandle.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	remote := newBackupS3Server(t)
	defer remote.Close()
	setBackupUploadEnvironment(t)
	server := NewWithScope("human:test", ScopeDefaults{Home: root, HomeByDefault: true})
	handler := server.Handler()
	putBackupProfile(t, handler, testBackupProfile(remote.URL, true), http.StatusOK)
	if code, body := raw(handler, http.MethodPost, "/api/backup/continuous-authorization", `{"confirm":true,"enabled":true}`); code != http.StatusOK {
		t.Fatalf("authorize scheduled sync = %d %s", code, body)
	}
	scheduler, err := localBackupScheduler(homeHandle, manifest)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := scheduler.RunScheduled(context.Background())
	if err != nil || baseline.Snapshot.ID == "" {
		t.Fatalf("baseline scheduled sync = %+v, %v", baseline, err)
	}
	baselineState, err := backup.LoadRuntimeState(backup.RuntimeStatePath(homeHandle.CarbonRoot))
	if err != nil || baselineState.LastRemoteSnapshotID != baseline.Snapshot.ID {
		t.Fatalf("baseline remote state = %+v, %v", baselineState, err)
	}
	baselineConfig, err := backup.LoadProfileConfigFile(backupConfigPath(homeHandle))
	if err != nil || baselineConfig.LastUpload == "" {
		t.Fatalf("baseline scheduled upload state = %+v, %v", baselineConfig, err)
	}
	if err := os.MkdirAll(filepath.Join(homeHandle.CarbonRoot, "tasks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeHandle.CarbonRoot, "tasks", "cancel-in-flight.md"), []byte("new local snapshot before cancellation"), 0o600); err != nil {
		t.Fatal(err)
	}

	block := remote.BlockNextRequest()
	type scheduledResult struct {
		result backup.LocalRunResult
		err    error
	}
	runDone := make(chan scheduledResult, 1)
	go func() {
		result, runErr := scheduler.RunScheduled(context.Background())
		runDone <- scheduledResult{result: result, err: runErr}
	}()
	select {
	case <-block.entered:
	case <-time.After(time.Second):
		t.Fatal("scheduled provider request did not reach blocking fake")
	}
	callsAtBlock := remote.Calls()

	type revokeResult struct {
		code int
		body string
	}
	revokeDone := make(chan revokeResult, 1)
	go func() {
		code, body := raw(handler, http.MethodPost, "/api/backup/continuous-authorization", `{"confirm":true,"enabled":false}`)
		revokeDone <- revokeResult{code: code, body: body}
	}()
	select {
	case revoked := <-revokeDone:
		if revoked.code != http.StatusOK || !strings.Contains(revoked.body, `"continuousAuthorization":false`) {
			block.Release()
			t.Fatalf("revoke in-flight sync = %d %s", revoked.code, revoked.body)
		}
	case <-time.After(500 * time.Millisecond):
		block.Release()
		t.Fatal("revoke waited for the blocked provider request")
	}
	select {
	case <-block.canceled:
	case <-time.After(time.Second):
		block.Release()
		t.Fatal("revocation did not cancel the provider request context")
	}
	select {
	case completed := <-runDone:
		if completed.err != nil || !completed.result.Created || completed.result.Snapshot.ID == "" {
			t.Fatalf("canceled remote sync did not retain local run = %+v, %v", completed.result, completed.err)
		}
	case <-time.After(time.Second):
		block.Release()
		t.Fatal("scheduled run did not finish after provider context cancellation")
	}

	config, err := backup.LoadProfileConfigFile(backupConfigPath(homeHandle))
	if err != nil || config.Profile.ContinuousAuthorization || config.LastUpload != baselineConfig.LastUpload {
		t.Fatalf("revoked profile = %+v, %v", config, err)
	}
	state, err := backup.LoadRuntimeState(backup.RuntimeStatePath(homeHandle.CarbonRoot))
	if err != nil || state.LastSnapshotID == baseline.Snapshot.ID || state.LastRemoteSnapshotID != baseline.Snapshot.ID || state.LastRemoteSnapshotAt != baselineState.LastRemoteSnapshotAt || state.LastRemoteFailureAt != "" || state.LastRemoteError != "" {
		t.Fatalf("canceled remote state overwrote revocation/local state = %+v, %v", state, err)
	}
	callsAfterRevoke := remote.Calls()
	time.Sleep(100 * time.Millisecond)
	if calls := remote.Calls(); calls != callsAfterRevoke || calls != callsAtBlock {
		t.Fatalf("provider calls grew after revoke: block=%d revoke=%d now=%d", callsAtBlock, callsAfterRevoke, calls)
	}
}

func TestBackupConfigReconfigurationCancelsInFlightScheduledRemoteSync(t *testing.T) {
	root := t.TempDir()
	homeHandle, err := home.Ensure(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := homeHandle.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	remote := newBackupS3Server(t)
	defer remote.Close()
	setBackupUploadEnvironment(t)
	server := NewWithScope("human:test", ScopeDefaults{Home: root, HomeByDefault: true})
	handler := server.Handler()
	putBackupProfile(t, handler, testBackupProfile(remote.URL, true), http.StatusOK)
	if code, body := raw(handler, http.MethodPost, "/api/backup/continuous-authorization", `{"confirm":true,"enabled":true}`); code != http.StatusOK {
		t.Fatalf("authorize scheduled sync = %d %s", code, body)
	}

	scheduler, err := localBackupScheduler(homeHandle, manifest)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := scheduler.RunScheduled(context.Background())
	if err != nil || baseline.Snapshot.ID == "" {
		t.Fatalf("baseline scheduled sync = %+v, %v", baseline, err)
	}
	baselineState, err := backup.LoadRuntimeState(backup.RuntimeStatePath(homeHandle.CarbonRoot))
	if err != nil || baselineState.LastRemoteSnapshotID != baseline.Snapshot.ID {
		t.Fatalf("baseline remote state = %+v, %v", baselineState, err)
	}
	baselineConfig, err := backup.LoadProfileConfigFile(backupConfigPath(homeHandle))
	if err != nil || baselineConfig.LastUpload == "" {
		t.Fatalf("baseline profile state = %+v, %v", baselineConfig, err)
	}

	if err := os.MkdirAll(filepath.Join(homeHandle.CarbonRoot, "tasks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeHandle.CarbonRoot, "tasks", "reconfigure-in-flight.md"), []byte("new local snapshot before target reconfiguration"), 0o600); err != nil {
		t.Fatal(err)
	}

	block := remote.BlockNextRequest()
	defer block.Release()
	type scheduledResult struct {
		result backup.LocalRunResult
		err    error
	}
	runDone := make(chan scheduledResult, 1)
	go func() {
		result, runErr := scheduler.RunScheduled(context.Background())
		runDone <- scheduledResult{result: result, err: runErr}
	}()
	select {
	case <-block.entered:
	case <-time.After(time.Second):
		t.Fatal("scheduled provider request did not reach blocking fake")
	}

	// A profile edit is persisted before the old generation is canceled. The
	// handler must not wait for the fake provider to be released, and the
	// in-flight callback must observe a canceled context rather than recording
	// success against this new prefix.
	replacement := testBackupProfile(remote.URL, true)
	replacement.Prefix = "carbon/reconfigured-target"
	start := time.Now()
	putBackupProfile(t, handler, replacement, http.StatusOK)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("backup reconfiguration waited %s for a blocked provider", elapsed)
	}
	select {
	case <-block.canceled:
	case <-time.After(time.Second):
		t.Fatal("reconfiguration did not cancel the old provider request context")
	}
	var canceledRun backup.LocalRunResult
	select {
	case completed := <-runDone:
		if completed.err != nil || !completed.result.Created || completed.result.Snapshot.ID == "" {
			t.Fatalf("canceled remote sync did not retain its local snapshot = %+v, %v", completed.result, completed.err)
		}
		canceledRun = completed.result
	case <-time.After(time.Second):
		t.Fatal("scheduled run did not finish after reconfiguration cancellation")
	}

	updatedConfig, err := backup.LoadProfileConfigFile(backupConfigPath(homeHandle))
	if err != nil || !updatedConfig.Profile.ContinuousAuthorization || updatedConfig.Profile.Prefix != replacement.Prefix || updatedConfig.LastUpload != baselineConfig.LastUpload {
		t.Fatalf("reconfiguration allowed stale scheduled upload bookkeeping = %+v, %v", updatedConfig, err)
	}
	updatedState, err := backup.LoadRuntimeState(backup.RuntimeStatePath(homeHandle.CarbonRoot))
	if err != nil || updatedState.LastSnapshotID != canceledRun.Snapshot.ID || updatedState.LastRemoteSnapshotID != baseline.Snapshot.ID || updatedState.LastRemoteDestination != baselineState.LastRemoteDestination || updatedState.LastRemoteSnapshotAt != baselineState.LastRemoteSnapshotAt || updatedState.LastRemoteFailureAt != "" || updatedState.LastRemoteError != "" {
		t.Fatalf("reconfiguration allowed stale scheduled runtime state = %+v, %v", updatedState, err)
	}

	// The replacement remains continuously authorized by the already-confirmed
	// local consent. It must be eligible for a fresh encrypted transfer rather
	// than being permanently suppressed by the canceled generation.
	beforeReplacementSync := remote.Calls()
	retransmitted, err := scheduler.RunScheduled(context.Background())
	if err != nil || retransmitted.Snapshot.ID != canceledRun.Snapshot.ID {
		t.Fatalf("replacement scheduled sync = %+v, %v", retransmitted, err)
	}
	if calls := remote.Calls(); calls <= beforeReplacementSync {
		t.Fatalf("replacement target made no provider calls: before=%d after=%d", beforeReplacementSync, calls)
	}
	expectedDestination, err := backup.RemoteDestinationFingerprint(updatedConfig.Profile)
	if err != nil {
		t.Fatal(err)
	}
	stateAfterReplacement, err := backup.LoadRuntimeState(backup.RuntimeStatePath(homeHandle.CarbonRoot))
	if err != nil || stateAfterReplacement.LastRemoteSnapshotID != canceledRun.Snapshot.ID || stateAfterReplacement.LastRemoteDestination != expectedDestination || stateAfterReplacement.LastRemoteSnapshotAt == "" {
		t.Fatalf("replacement sync did not record its own target = %+v, %v", stateAfterReplacement, err)
	}
	configAfterReplacement, err := backup.LoadProfileConfigFile(backupConfigPath(homeHandle))
	if err != nil || configAfterReplacement.LastUpload == "" || configAfterReplacement.LastUpload == baselineConfig.LastUpload {
		t.Fatalf("replacement sync did not record a new upload timestamp = %+v, %v", configAfterReplacement, err)
	}
}

func TestBackupRemoteSyncProfileChangedCoversEffectiveFields(t *testing.T) {
	base := testBackupProfile("http://127.0.0.1:9000", true)
	base.ContinuousAuthorization = true
	changes := map[string]func(*backup.RemoteProfile){
		"enabled":                  func(profile *backup.RemoteProfile) { profile.Enabled = false },
		"continuous authorization": func(profile *backup.RemoteProfile) { profile.ContinuousAuthorization = false },
		"backend":                  func(profile *backup.RemoteProfile) { profile.Backend = "cos" },
		"bucket":                   func(profile *backup.RemoteProfile) { profile.Bucket = "other-backup-bucket" },
		"prefix":                   func(profile *backup.RemoteProfile) { profile.Prefix = "carbon/other" },
		"region":                   func(profile *backup.RemoteProfile) { profile.Region = "us-west-2" },
		"endpoint":                 func(profile *backup.RemoteProfile) { profile.Endpoint = "http://127.0.0.1:9001" },
		"path style":               func(profile *backup.RemoteProfile) { profile.UsePathStyle = false },
		"insecure endpoint":        func(profile *backup.RemoteProfile) { profile.AllowInsecureEndpoint = false },
		"credential reference":     func(profile *backup.RemoteProfile) { profile.CredentialRef = "env://CARBON_OTHER_AWS" },
		"encryption":               func(profile *backup.RemoteProfile) { profile.Encryption = false },
		"encryption key reference": func(profile *backup.RemoteProfile) { profile.EncryptionKeyRef = "env://CARBON_OTHER_BACKUP_KEY" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			changed := base
			change(&changed)
			if !backupRemoteSyncProfileChanged(base, changed) {
				t.Fatalf("%s did not invalidate scheduled remote sync identity", name)
			}
		})
	}
	displayOnly := base
	displayOnly.Profile = "renamed-display-profile"
	if backupRemoteSyncProfileChanged(base, displayOnly) {
		t.Fatal("display-only profile name invalidated scheduled remote sync identity")
	}
}

func TestBackupUploadRequiresConfirmEnabledAndLoopbackServer(t *testing.T) {
	root := t.TempDir()
	if _, err := home.Ensure(root); err != nil {
		t.Fatal(err)
	}
	remote := newBackupS3Server(t)
	defer remote.Close()
	setBackupUploadEnvironment(t)
	server := NewWithScope("human:test", ScopeDefaults{Home: root, HomeByDefault: true})
	handler := server.Handler()

	disabled := testBackupProfile(remote.URL, false)
	putBackupProfile(t, handler, disabled, http.StatusOK)
	snapshot := createBackupSnapshot(t, handler)
	path := "/api/backup/snapshots/" + snapshot.ID + "/upload"
	if code, body := raw(handler, http.MethodPost, path, `{"confirm":false}`); code != http.StatusBadRequest {
		t.Fatalf("confirm=false = %d %s", code, body)
	}
	if code, body := raw(handler, http.MethodPost, path, `{"confirm":true}`); code != http.StatusForbidden {
		t.Fatalf("disabled upload = %d %s", code, body)
	}
	if calls := remote.Calls(); calls != 0 {
		t.Fatalf("confirm/disabled path made %d remote calls", calls)
	}

	putBackupProfile(t, handler, testBackupProfile(remote.URL, true), http.StatusOK)
	server.allowRemote = true
	if code, body := raw(handler, http.MethodPost, path, `{"confirm":true}`); code != http.StatusForbidden {
		t.Fatalf("allow-remote upload = %d %s", code, body)
	}
	if calls := remote.Calls(); calls != 0 {
		t.Fatalf("allow-remote upload made %d remote calls", calls)
	}
}

func TestBackupUploadEncryptsRemoteObjectsVerifiesAndOwnsLastUpload(t *testing.T) {
	root := t.TempDir()
	if _, err := home.Ensure(root); err != nil {
		t.Fatal(err)
	}
	remote := newBackupS3Server(t)
	defer remote.Close()
	setBackupUploadEnvironment(t)
	server := NewWithScope("human:test", ScopeDefaults{Home: root, HomeByDefault: true})
	handler := server.Handler()
	putBackupProfile(t, handler, testBackupProfile(remote.URL, true), http.StatusOK)
	snapshot := createBackupSnapshot(t, handler)

	path := "/api/backup/snapshots/" + snapshot.ID + "/upload"
	code, body := raw(handler, http.MethodPost, path, `{"confirm":true}`)
	if code != http.StatusOK || !strings.Contains(body, `"verified":true`) || !strings.Contains(body, `"lastUpload"`) {
		t.Fatalf("upload = %d %s", code, body)
	}
	objects := remote.Objects()
	if len(objects) == 0 {
		t.Fatal("upload made no provider objects")
	}
	for key, object := range objects {
		if !bytes.HasPrefix(object.data, []byte("CBEN")) {
			t.Fatalf("remote %q is not an encrypted CBEN object: %x", key, object.data[:minBackupTest(len(object.data), 8)])
		}
		if bytes.Contains(object.data, []byte("backup-bucket")) || bytes.Contains(object.data, []byte("credentialRef")) {
			t.Fatalf("remote %q leaked plaintext configuration", key)
		}
	}
	if code, body := raw(handler, http.MethodGet, "/api/backup/config", ""); code != http.StatusOK || strings.Contains(body, "lastUpload") {
		t.Fatalf("config leaked server lastUpload = %d %s", code, body)
	}
	if code, body := raw(handler, http.MethodGet, "/api/backup/status", ""); code != http.StatusOK || !strings.Contains(body, `"lastUpload"`) {
		t.Fatalf("status lacks server lastUpload = %d %s", code, body)
	}
	config, err := backup.LoadProfileConfigFile(filepath.Join(root, ".carbon", backupConfigFilename))
	if err != nil || config.LastUpload == "" {
		t.Fatalf("stored server lastUpload = %+v, %v", config, err)
	}
}

func TestBackupRejectsProjectAndClusterScopeWithoutWrites(t *testing.T) {
	root := t.TempDir()
	h, err := home.Ensure(root)
	if err != nil {
		t.Fatal(err)
	}
	cluster, err := h.CreateCluster(home.CreateClusterRequest{Name: "Backup scope", Prefix: "BKP"})
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	project, err := home.AddProject(root, cluster.ID, home.AddProjectRequest{Name: "Project", Kind: home.ProjectGeneric, SourcePath: source})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithScope("human:test", ScopeDefaults{Home: root, HomeByDefault: true})
	handler := server.Handler()
	base := "/api/backup/status?home=" + url.QueryEscape(root) + "&cluster=" + url.QueryEscape(cluster.ID)
	if code, body := raw(handler, http.MethodGet, base, ""); code != http.StatusUnprocessableEntity || !strings.Contains(body, "home-only") {
		t.Fatalf("cluster scope = %d %s", code, body)
	}
	projectURL := base + "&project=" + url.QueryEscape(project.ID)
	if code, body := raw(handler, http.MethodPost, strings.Replace(projectURL, "/status", "/snapshots", 1), ""); code != http.StatusUnprocessableEntity || !strings.Contains(body, "home-only") {
		t.Fatalf("project scope = %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(root, ".carbon", backupConfigFilename)); !os.IsNotExist(err) {
		t.Fatalf("scoped endpoint wrote backup config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".carbon", "backups")); !os.IsNotExist(err) {
		t.Fatalf("scoped endpoint created local snapshots: %v", err)
	}
}

func putBackupProfile(t *testing.T, handler http.Handler, profile backup.RemoteProfile, wantStatus int) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"profile": profile})
	if err != nil {
		t.Fatal(err)
	}
	if code, response := raw(handler, http.MethodPut, "/api/backup/config", string(body)); code != wantStatus {
		t.Fatalf("PUT backup profile = %d %s", code, response)
	}
}

func createBackupSnapshot(t *testing.T, handler http.Handler) backup.Snapshot {
	t.Helper()
	code, body := raw(handler, http.MethodPost, "/api/backup/snapshots", "")
	if code != http.StatusCreated {
		t.Fatalf("create snapshot = %d %s", code, body)
	}
	var snapshot backup.Snapshot
	if err := json.Unmarshal([]byte(body), &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func testBackupProfile(endpoint string, enabled bool) backup.RemoteProfile {
	return backup.RemoteProfile{
		Backend:               "s3",
		Enabled:               enabled,
		Bucket:                "backup-bucket",
		Prefix:                "carbon/test",
		Region:                "us-east-1",
		Endpoint:              endpoint,
		UsePathStyle:          true,
		AllowInsecureEndpoint: true,
		CredentialRef:         "env://CARBON_TEST_AWS",
		Encryption:            true,
		EncryptionKeyRef:      "env://CARBON_TEST_BACKUP_KEY",
	}
}

func setBackupUploadEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("CARBON_TEST_AWS_ACCESS_KEY_ID", "test-access")
	t.Setenv("CARBON_TEST_AWS_SECRET_ACCESS_KEY", "test-secret")
	t.Setenv("CARBON_TEST_BACKUP_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x4a}, 32)))
}

type backupS3Object struct {
	data     []byte
	checksum string
}

type backupS3Server struct {
	mu        sync.Mutex
	calls     int
	objects   map[string]backupS3Object
	failNext  bool
	blockNext *backupS3Block
}

type backupS3Block struct {
	entered  chan struct{}
	release  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func newBackupS3Block() *backupS3Block {
	return &backupS3Block{
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
}

func (block *backupS3Block) Release() {
	block.once.Do(func() { close(block.release) })
}

type backupS3Harness struct {
	*httptest.Server
	fake *backupS3Server
}

func newBackupS3Server(t *testing.T) *backupS3Harness {
	t.Helper()
	fake := &backupS3Server{objects: make(map[string]backupS3Object)}
	server := httptest.NewServer(fake)
	return &backupS3Harness{Server: server, fake: fake}
}

func (s *backupS3Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.calls++
	failNext := s.failNext
	s.failNext = false
	block := s.blockNext
	s.blockNext = nil
	s.mu.Unlock()
	if failNext {
		http.Error(w, "forced provider failure", http.StatusForbidden)
		return
	}
	if block != nil {
		close(block.entered)
		select {
		case <-block.release:
		case <-r.Context().Done():
			close(block.canceled)
			return
		}
	}
	key := strings.TrimPrefix(r.URL.EscapedPath(), "/backup-bucket/")
	key = strings.TrimPrefix(key, "/")
	if key == "" || key == r.URL.EscapedPath() {
		http.Error(w, "invalid fake bucket path", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		if _, exists := s.objects[key]; exists && r.Header.Get("If-None-Match") == "*" {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		s.objects[key] = backupS3Object{data: bytes.Clone(data), checksum: r.Header.Get("X-Amz-Meta-Carbon-Sha256")}
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		object, exists := s.objects[key]
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", stringInt(len(object.data)))
		w.Header().Set("X-Amz-Meta-Carbon-Sha256", object.checksum)
		_, _ = w.Write(object.data)
	case http.MethodHead:
		object, exists := s.objects[key]
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", stringInt(len(object.data)))
		w.Header().Set("X-Amz-Meta-Carbon-Sha256", object.checksum)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "unsupported", http.StatusMethodNotAllowed)
	}
}

func (s *backupS3Server) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *backupS3Server) Objects() map[string]backupS3Object {
	s.mu.Lock()
	defer s.mu.Unlock()
	objects := make(map[string]backupS3Object, len(s.objects))
	for key, object := range s.objects {
		objects[key] = backupS3Object{data: bytes.Clone(object.data), checksum: object.checksum}
	}
	return objects
}

func (s *backupS3Server) FailNextRequest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext = true
}

func (s *backupS3Server) BlockNextRequest() *backupS3Block {
	s.mu.Lock()
	defer s.mu.Unlock()
	block := newBackupS3Block()
	s.blockNext = block
	return block
}

func stringInt(value int) string {
	return strconv.Itoa(value)
}

func (h *backupS3Harness) Calls() int { return h.fake.Calls() }

func (h *backupS3Harness) Objects() map[string]backupS3Object { return h.fake.Objects() }

func (h *backupS3Harness) FailNextRequest() { h.fake.FailNextRequest() }

func (h *backupS3Harness) BlockNextRequest() *backupS3Block { return h.fake.BlockNextRequest() }

func minBackupTest(left, right int) int {
	if left < right {
		return left
	}
	return right
}
