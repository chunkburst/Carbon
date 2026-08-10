package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RuntimeStateVersion is the private local scheduler state schema.
const RuntimeStateVersion = 1

// RuntimeState records scheduler observability only. It intentionally contains
// no remote credentials, provider diagnostics, or source paths.
type RuntimeState struct {
	Version                      int    `json:"version"`
	LastRunAt                    string `json:"lastRunAt,omitempty"`
	LastSuccessAt                string `json:"lastSuccessAt,omitempty"`
	LastSnapshotID               string `json:"lastSnapshotId,omitempty"`
	LastSnapshotAt               string `json:"lastSnapshotAt,omitempty"`
	LastPruneAt                  string `json:"lastPruneAt,omitempty"`
	LastPruned                   int    `json:"lastPruned,omitempty"`
	LastRemoteSnapshotID         string `json:"lastRemoteSnapshotId,omitempty"`
	LastRemoteSnapshotAt         string `json:"lastRemoteSnapshotAt,omitempty"`
	LastRemoteDestination        string `json:"lastRemoteDestination,omitempty"`
	LastRemoteFailureAt          string `json:"lastRemoteFailureAt,omitempty"`
	LastRemoteFailureDestination string `json:"lastRemoteFailureDestination,omitempty"`
	LastRemoteError              string `json:"lastRemoteError,omitempty"`
}

// RedactedRemoteSyncError is the only remote failure detail persisted in
// runtime state or surfaced through status. Provider errors may contain paths
// or vendor diagnostics and must remain local to the failed call.
const RedactedRemoteSyncError = "encrypted remote sync failed"

func DefaultRuntimeState() RuntimeState { return RuntimeState{Version: RuntimeStateVersion} }

// RuntimeStatePath is the fixed private state location under a Carbon metadata
// root (`<home>/.carbon/backups/state.json`). It is excluded from snapshots.
func RuntimeStatePath(carbonRoot string) string {
	return filepath.Join(carbonRoot, "backups", "state.json")
}

// LoadRuntimeState is strict and side-effect free. A malformed state file is a
// fail-closed scheduler error; automatic work must not guess past corruption.
func LoadRuntimeState(filename string) (RuntimeState, error) {
	data, err := readBackupPrivateFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultRuntimeState(), nil
	}
	if err != nil {
		return RuntimeState{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state RuntimeState
	if err := decoder.Decode(&state); err != nil {
		return RuntimeState{}, fmt.Errorf("parse backup runtime state: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return RuntimeState{}, errors.New("parse backup runtime state: trailing data")
	}
	if err := normalizeRuntimeState(&state); err != nil {
		return RuntimeState{}, err
	}
	return state, nil
}

// SaveRuntimeState atomically writes a current-user-only state document. Its
// parent directory is created through the same no-reparse local path checks as
// the blob store.
func SaveRuntimeState(filename string, state RuntimeState) error {
	state.Version = RuntimeStateVersion
	if err := normalizeRuntimeState(&state); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeBackupPrivateFile(filename, append(data, '\n'), true)
}

func normalizeRuntimeState(state *RuntimeState) error {
	if state == nil || state.Version != RuntimeStateVersion {
		return errors.New("unsupported backup runtime state version")
	}
	for name, value := range map[string]string{
		"lastRunAt":            state.LastRunAt,
		"lastSuccessAt":        state.LastSuccessAt,
		"lastSnapshotAt":       state.LastSnapshotAt,
		"lastPruneAt":          state.LastPruneAt,
		"lastRemoteSnapshotAt": state.LastRemoteSnapshotAt,
		"lastRemoteFailureAt":  state.LastRemoteFailureAt,
	} {
		value = strings.TrimSpace(value)
		switch name {
		case "lastRunAt":
			state.LastRunAt = value
		case "lastSuccessAt":
			state.LastSuccessAt = value
		case "lastSnapshotAt":
			state.LastSnapshotAt = value
		case "lastPruneAt":
			state.LastPruneAt = value
		case "lastRemoteSnapshotAt":
			state.LastRemoteSnapshotAt = value
		case "lastRemoteFailureAt":
			state.LastRemoteFailureAt = value
		}
		if value != "" {
			if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
				return fmt.Errorf("backup runtime state %s is invalid", name)
			}
		}
	}
	state.LastSnapshotID = strings.TrimSpace(state.LastSnapshotID)
	if state.LastSnapshotID != "" {
		if err := validateSHA256(state.LastSnapshotID); err != nil {
			return errors.New("backup runtime state lastSnapshotId is invalid")
		}
	}
	for name, value := range map[string]string{
		"lastRemoteSnapshotId":         state.LastRemoteSnapshotID,
		"lastRemoteDestination":        state.LastRemoteDestination,
		"lastRemoteFailureDestination": state.LastRemoteFailureDestination,
	} {
		value = strings.TrimSpace(value)
		switch name {
		case "lastRemoteSnapshotId":
			state.LastRemoteSnapshotID = value
		case "lastRemoteDestination":
			state.LastRemoteDestination = value
		case "lastRemoteFailureDestination":
			state.LastRemoteFailureDestination = value
		}
		if value != "" {
			if err := validateSHA256(value); err != nil {
				return fmt.Errorf("backup runtime state %s is invalid", name)
			}
		}
	}
	if (state.LastRemoteSnapshotID == "") != (state.LastRemoteSnapshotAt == "") ||
		(state.LastRemoteSnapshotID == "") != (state.LastRemoteDestination == "") {
		return errors.New("backup runtime state remote success is incomplete")
	}
	state.LastRemoteError = strings.TrimSpace(state.LastRemoteError)
	if state.LastRemoteFailureAt == "" {
		if state.LastRemoteFailureDestination != "" || state.LastRemoteError != "" {
			return errors.New("backup runtime state remote failure is incomplete")
		}
	} else if state.LastRemoteFailureDestination == "" || state.LastRemoteError != RedactedRemoteSyncError {
		return errors.New("backup runtime state remote failure is invalid")
	}
	if state.LastPruned < 0 {
		return errors.New("backup runtime state lastPruned is invalid")
	}
	return nil
}
