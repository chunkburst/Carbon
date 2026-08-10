package home

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
	"unicode/utf8"
)

const (
	// WorkerRegistryFilename stores home-global Worker lifecycle metadata. It is
	// deliberately independent from home.json and task files: resetting or deleting
	// a Worker must never rewrite a task's provenance, lease, or assignment.
	WorkerRegistryFilename = "worker-registry.json"
	// WorkerRegistryVersion is the only Worker registry schema this binary can
	// safely read and rewrite.
	WorkerRegistryVersion = 1

	maxWorkerRegistryBytes = 1 << 20
	maxWorkerRegistryItems = 4096
)

var (
	// ErrInvalidWorkerRegistry means the durable registry is malformed or
	// semantically inconsistent. It is fail-closed: callers must not repair it
	// implicitly while processing a user mutation.
	ErrInvalidWorkerRegistry = errors.New("invalid Carbon worker registry")
	// ErrFutureWorkerRegistryVersion prevents an older Carbon binary from
	// overwriting data written by a newer schema.
	ErrFutureWorkerRegistryVersion = errors.New("unsupported future Carbon worker registry version")
	// ErrInvalidWorkerRegistryActor is reserved for untrusted reset/delete actor
	// input. It is distinct from a corrupt durable document so HTTP can return a
	// useful input error without hiding on-disk corruption.
	ErrInvalidWorkerRegistryActor = errors.New("invalid Carbon worker registry actor")
)

// WorkerRecord is the durable lifecycle record for one canonical actor. All
// timestamps are RFC3339/RFC3339Nano strings so the file remains readable and can
// be inspected without a migration tool. DeletedAt is present only while a Worker is
// tombstoned; when the actor produces later activity it is converted into ResetAt so
// historical metrics do not silently come back.
type WorkerRecord struct {
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	ResetAt   string `json:"resetAt,omitempty"`
	DeletedAt string `json:"deletedAt,omitempty"`
}

// WorkerRegistryFile is the durable v1 wire form.
type WorkerRegistryFile struct {
	Version int                     `json:"version"`
	Workers map[string]WorkerRecord `json:"workers"`
}

type workerRegistryWire struct {
	Version *int            `json:"version"`
	Workers json.RawMessage `json:"workers"`
}

// ListWorkerRegistry returns a defensive copy of home-global Worker lifecycle
// metadata. A missing file is the normal empty state and does not create metadata.
func ListWorkerRegistry(main string) (map[string]WorkerRecord, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return nil, err
	}
	carbonRoot, err := workerRegistryCarbonRoot(root)
	if err != nil {
		return nil, err
	}
	workers, err := readWorkerRegistry(carbonRoot)
	if err != nil {
		return nil, err
	}
	return cloneWorkerRegistry(workers), nil
}

// ListWorkerRegistry is the Home-handle equivalent of the package function.
func (h *Home) ListWorkerRegistry() (map[string]WorkerRecord, error) {
	if h == nil {
		return nil, ErrNotInitialized
	}
	return ListWorkerRegistry(h.Root)
}

// ResetWorker clears one Worker's derived metric history from this instant forward.
// It only changes worker-registry.json and leaves every task file byte-for-byte
// untouched. A tombstoned Worker remains tombstoned until it produces new activity.
func ResetWorker(main, actor string) (WorkerRecord, error) {
	return resetWorkerAt(main, actor, clock().UTC())
}

// ResetWorker is the Home-handle equivalent of ResetWorker.
func (h *Home) ResetWorker(actor string) (WorkerRecord, error) {
	if h == nil {
		return WorkerRecord{}, ErrNotInitialized
	}
	return ResetWorker(h.Root, actor)
}

func resetWorkerAt(main, actor string, now time.Time) (WorkerRecord, error) {
	if err := validateWorkerRegistryActor(actor); err != nil {
		return WorkerRecord{}, err
	}
	root, err := resolveRoot(main)
	if err != nil {
		return WorkerRecord{}, err
	}
	now = now.UTC()
	stamp := now.Format(time.RFC3339Nano)
	var result WorkerRecord
	err = withLock(root, func() error {
		carbonRoot, err := workerRegistryCarbonRoot(root)
		if err != nil {
			return err
		}
		workers, err := readWorkerRegistry(carbonRoot)
		if err != nil {
			return err
		}
		record, exists := workers[actor]
		if !exists {
			record.CreatedAt = stamp
		}
		record.UpdatedAt = stamp
		record.ResetAt = stamp
		workers[actor] = record
		if err := writeWorkerRegistry(carbonRoot, workers); err != nil {
			return err
		}
		result = record
		return nil
	})
	if err != nil {
		return WorkerRecord{}, err
	}
	return result, nil
}

// DeleteWorker writes a tombstone for one Worker. It intentionally does not delete,
// edit, reassign, or otherwise touch task data. A later ReconcileWorkerActivity call
// can revive the Worker only from activity strictly newer than this tombstone.
func DeleteWorker(main, actor string) (WorkerRecord, error) {
	return deleteWorkerAt(main, actor, clock().UTC())
}

// DeleteWorker is the Home-handle equivalent of DeleteWorker.
func (h *Home) DeleteWorker(actor string) (WorkerRecord, error) {
	if h == nil {
		return WorkerRecord{}, ErrNotInitialized
	}
	return DeleteWorker(h.Root, actor)
}

func deleteWorkerAt(main, actor string, now time.Time) (WorkerRecord, error) {
	if err := validateWorkerRegistryActor(actor); err != nil {
		return WorkerRecord{}, err
	}
	root, err := resolveRoot(main)
	if err != nil {
		return WorkerRecord{}, err
	}
	now = now.UTC()
	stamp := now.Format(time.RFC3339Nano)
	var result WorkerRecord
	err = withLock(root, func() error {
		carbonRoot, err := workerRegistryCarbonRoot(root)
		if err != nil {
			return err
		}
		workers, err := readWorkerRegistry(carbonRoot)
		if err != nil {
			return err
		}
		record, exists := workers[actor]
		if !exists {
			record.CreatedAt = stamp
		}
		record.UpdatedAt = stamp
		record.DeletedAt = stamp
		workers[actor] = record
		if err := writeWorkerRegistry(carbonRoot, workers); err != nil {
			return err
		}
		result = record
		return nil
	})
	if err != nil {
		return WorkerRecord{}, err
	}
	return result, nil
}

// ReconcileWorkerActivity records observed activity and revives tombstoned Workers
// only when the evidence is strictly newer than their DeletedAt timestamp. Deletion is
// converted into a reset cutoff on revival, ensuring the old worker history stays
// cleared. The returned map is always a defensive copy.
//
// The input is intentionally an actor -> latest-activity map rather than task docs so
// this package never reaches into task storage and cannot accidentally mutate it.
func ReconcileWorkerActivity(main string, activity map[string]time.Time) (map[string]WorkerRecord, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return nil, err
	}
	return reconcileWorkerActivity(root, activity)
}

// ReconcileWorkerActivity is the Home-handle equivalent of the package function.
func (h *Home) ReconcileWorkerActivity(activity map[string]time.Time) (map[string]WorkerRecord, error) {
	if h == nil {
		return nil, ErrNotInitialized
	}
	return ReconcileWorkerActivity(h.Root, activity)
}

func reconcileWorkerActivity(root string, activity map[string]time.Time) (map[string]WorkerRecord, error) {
	// Validate before taking the lock. Invalid activity must never cause a partial
	// registry update, and empty/zero observations are simply ignored.
	clean := make(map[string]time.Time, len(activity))
	for actor, at := range activity {
		if at.IsZero() {
			continue
		}
		if err := validateWorkerRegistryActor(actor); err != nil {
			return nil, err
		}
		at = at.UTC()
		if previous, exists := clean[actor]; !exists || at.After(previous) {
			clean[actor] = at
		}
	}

	var result map[string]WorkerRecord
	err := withLock(root, func() error {
		carbonRoot, err := workerRegistryCarbonRoot(root)
		if err != nil {
			return err
		}
		workers, err := readWorkerRegistry(carbonRoot)
		if err != nil {
			return err
		}
		changed := false
		actors := make([]string, 0, len(clean))
		for actor := range clean {
			actors = append(actors, actor)
		}
		sort.Strings(actors)
		for _, actor := range actors {
			at := clean[actor]
			record, exists := workers[actor]
			if !exists {
				stamp := at.Format(time.RFC3339Nano)
				workers[actor] = WorkerRecord{CreatedAt: stamp, UpdatedAt: stamp}
				changed = true
				continue
			}
			if record.DeletedAt != "" {
				deletedAt, _ := time.Parse(time.RFC3339, record.DeletedAt) // validated on read
				if !at.After(deletedAt) {
					continue
				}
				// A deletion is also a history boundary. Keep the later existing reset if
				// the user reset the tombstoned Worker after deleting it.
				if resetAt, ok := workerRecordTime(record.ResetAt); !ok || resetAt.Before(deletedAt) {
					record.ResetAt = record.DeletedAt
				}
				record.DeletedAt = ""
				changed = true
			}
			updatedAt, _ := time.Parse(time.RFC3339, record.UpdatedAt) // validated on read
			if at.After(updatedAt) {
				record.UpdatedAt = at.Format(time.RFC3339Nano)
				changed = true
			}
			workers[actor] = record
		}
		if changed {
			if err := writeWorkerRegistry(carbonRoot, workers); err != nil {
				return err
			}
		}
		result = cloneWorkerRegistry(workers)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func workerRegistryCarbonRoot(root string) (string, error) {
	carbonRoot, exists, err := carbonDir(root, false)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", ErrNotInitialized
	}
	if _, exists, err := readManifest(carbonRoot); err != nil {
		return "", err
	} else if !exists {
		return "", ErrNotInitialized
	}
	return carbonRoot, nil
}

func workerRegistryPath(carbonRoot string) string {
	return filepath.Join(carbonRoot, WorkerRegistryFilename)
}

func readWorkerRegistry(carbonRoot string) (map[string]WorkerRecord, error) {
	filename := workerRegistryPath(carbonRoot)
	if _, exists, err := safeRegularFile(carbonRoot, filename, true); err != nil {
		return nil, err
	} else if !exists {
		return map[string]WorkerRecord{}, nil
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("carbon: open worker registry: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxWorkerRegistryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("carbon: read worker registry: %w", err)
	}
	if int64(len(data)) > maxWorkerRegistryBytes {
		return nil, fmt.Errorf("%w: worker registry exceeds %d bytes", ErrInvalidWorkerRegistry, maxWorkerRegistryBytes)
	}
	return decodeWorkerRegistry(data)
}

func decodeWorkerRegistry(data []byte) (map[string]WorkerRecord, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: JSON is not valid UTF-8", ErrInvalidWorkerRegistry)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, fmt.Errorf("%w: ambiguous JSON: %v", ErrInvalidWorkerRegistry, err)
	}
	var wire workerRegistryWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("%w: parse JSON: %v", ErrInvalidWorkerRegistry, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%w: multiple JSON values", ErrInvalidWorkerRegistry)
		}
		return nil, fmt.Errorf("%w: parse JSON: %v", ErrInvalidWorkerRegistry, err)
	}
	if wire.Version == nil || len(wire.Workers) == 0 || bytes.Equal(bytes.TrimSpace(wire.Workers), []byte("null")) {
		return nil, fmt.Errorf("%w: version and workers are required", ErrInvalidWorkerRegistry)
	}
	if *wire.Version > WorkerRegistryVersion {
		return nil, fmt.Errorf("%w: version %d", ErrFutureWorkerRegistryVersion, *wire.Version)
	}
	if *wire.Version != WorkerRegistryVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrInvalidWorkerRegistry, *wire.Version)
	}
	var workers map[string]WorkerRecord
	workersDecoder := json.NewDecoder(bytes.NewReader(wire.Workers))
	workersDecoder.DisallowUnknownFields()
	if err := workersDecoder.Decode(&workers); err != nil {
		return nil, fmt.Errorf("%w: parse workers: %v", ErrInvalidWorkerRegistry, err)
	}
	if err := workersDecoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: parse workers: %v", ErrInvalidWorkerRegistry, err)
	}
	if workers == nil {
		return nil, fmt.Errorf("%w: workers must be an object", ErrInvalidWorkerRegistry)
	}
	if err := validateWorkerRegistry(workers); err != nil {
		return nil, err
	}
	return workers, nil
}

func writeWorkerRegistry(carbonRoot string, workers map[string]WorkerRecord) error {
	if err := validateWorkerRegistry(workers); err != nil {
		return err
	}
	data, err := json.MarshalIndent(WorkerRegistryFile{
		Version: WorkerRegistryVersion,
		Workers: cloneWorkerRegistry(workers),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("carbon: encode worker registry: %w", err)
	}
	data = append(data, '\n')
	return atomicWriteJSON(carbonRoot, WorkerRegistryFilename, data)
}

func validateWorkerRegistry(workers map[string]WorkerRecord) error {
	if workers == nil || len(workers) > maxWorkerRegistryItems {
		return fmt.Errorf("%w: workers must contain at most %d entries", ErrInvalidWorkerRegistry, maxWorkerRegistryItems)
	}
	for actor, record := range workers {
		if err := validateWorkerRegistryActorStored(actor); err != nil {
			return err
		}
		if err := validateWorkerRecord(record); err != nil {
			return fmt.Errorf("%w: actor %q: %v", ErrInvalidWorkerRegistry, actor, err)
		}
	}
	return nil
}

func validateWorkerRecord(record WorkerRecord) error {
	createdAt, ok := workerRecordTime(record.CreatedAt)
	if !ok {
		return errors.New("createdAt must be a valid RFC3339 timestamp")
	}
	updatedAt, ok := workerRecordTime(record.UpdatedAt)
	if !ok || updatedAt.Before(createdAt) {
		return errors.New("updatedAt must be a valid RFC3339 timestamp not before createdAt")
	}
	if record.ResetAt != "" {
		resetAt, ok := workerRecordTime(record.ResetAt)
		if !ok || resetAt.Before(createdAt) || resetAt.After(updatedAt) {
			return errors.New("resetAt must be a valid RFC3339 timestamp between createdAt and updatedAt")
		}
	}
	if record.DeletedAt != "" {
		deletedAt, ok := workerRecordTime(record.DeletedAt)
		if !ok || deletedAt.Before(createdAt) || deletedAt.After(updatedAt) {
			return errors.New("deletedAt must be a valid RFC3339 timestamp between createdAt and updatedAt")
		}
	}
	return nil
}

func workerRecordTime(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, value)
	return at, err == nil
}

func validateWorkerRegistryActor(actor string) error {
	if !validWorkerActor(actor) {
		return fmt.Errorf("%w: actor must be a non-empty canonical identifier", ErrInvalidWorkerRegistryActor)
	}
	return nil
}

func validateWorkerRegistryActorStored(actor string) error {
	if !validWorkerActor(actor) {
		return fmt.Errorf("%w: invalid canonical actor %q", ErrInvalidWorkerRegistry, actor)
	}
	return nil
}

func cloneWorkerRegistry(workers map[string]WorkerRecord) map[string]WorkerRecord {
	clone := make(map[string]WorkerRecord, len(workers))
	for actor, record := range workers {
		clone[actor] = record
	}
	return clone
}
