package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"carbon/internal/config"
	"carbon/internal/task"
)

const (
	defaultLockTimeout = 5 * time.Second
	lockRetryInterval  = 10 * time.Millisecond
)

// ErrLockTimeout is returned when another Carbon process holds the repository write lock
// beyond the caller's deadline.
var ErrLockTimeout = errors.New("repository write lock timeout")

// errWouldBlock signals that the exclusive lock is currently held by another process. The
// platform-specific lockExclusiveNB returns it (wrapping the OS code) so acquireLock can
// retry. Real faults are returned as-is.
var errWouldBlock = errors.New("store: write lock held")

// WriteTx is a short, repository-exclusive mutation scope. Long-running work such as
// command execution must happen before entering a transaction.
type WriteTx struct {
	store *Store
}

// Write serializes a short mutation across every Carbon process using this repository.
func (s *Store) Write(ctx context.Context, actor, operation string, fn func(*WriteTx) error) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultLockTimeout)
		defer cancel()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	lockPath, err := s.lockFilePath()
	if err != nil {
		return fmt.Errorf("store: resolve write lock: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("store: open write lock: %w", err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return fmt.Errorf("store: stat write lock: %w", err)
	} else if !fi.Mode().IsRegular() {
		return fmt.Errorf("store: write lock is not a regular file: %s", lockPath)
	}

	if err := acquireLock(ctx, f); err != nil {
		return err
	}
	defer unlock(f) //nolint:errcheck // closing the fd also releases the lock

	// A project task-data clear swaps whole managed collections through a same-store
	// quarantine. Recover an interrupted prepared swap before any later mutation can
	// observe or extend a mixed collection layout. The helper is deliberately
	// lock-free itself: this Write transaction already owns the repository lock.
	if err := s.recoverPendingProjectTaskDataClears(); err != nil {
		return err
	}

	if err := writeLockDiagnostic(f, actor, operation); err != nil {
		return err
	}
	return fn(&WriteTx{store: s})
}

func acquireLock(ctx context.Context, f *os.File) error {
	for {
		err := lockExclusiveNB(f)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errWouldBlock) {
			return fmt.Errorf("store: acquire write lock: %w", err)
		}

		timer := time.NewTimer(lockRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w: %w", ErrLockTimeout, ctx.Err())
		case <-timer.C:
		}
	}
}

func writeLockDiagnostic(f *os.File, actor, operation string) error {
	diagnostic := struct {
		PID       int    `json:"pid"`
		Actor     string `json:"actor,omitempty"`
		Operation string `json:"operation,omitempty"`
		Acquired  string `json:"acquiredAt"`
	}{
		PID:       os.Getpid(),
		Actor:     actor,
		Operation: operation,
		Acquired:  time.Now().UTC().Format(time.RFC3339Nano),
	}
	b, err := json.Marshal(diagnostic)
	if err != nil {
		return fmt.Errorf("store: marshal write-lock diagnostic: %w", err)
	}
	b = append(b, '\n')
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("store: truncate write lock: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return fmt.Errorf("store: seek write lock: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		return fmt.Errorf("store: write lock diagnostic: %w", err)
	}
	return nil
}

// SaveTask writes a task inside an existing repository transaction.
func (tx *WriteTx) SaveTask(d *Doc) error {
	return tx.store.save(d)
}

// GetTask reads a task inside an existing transaction.
func (tx *WriteTx) GetTask(id string) (*Doc, error) { return tx.store.Get(id) }

// Tasks reads the validated task graph inside an existing transaction.
func (tx *WriteTx) Tasks() (map[string]task.Task, error) { return tx.store.List() }

// Config reads repository configuration inside an existing transaction.
func (tx *WriteTx) Config() (config.Config, error) { return tx.store.Config() }

// SaveConfig persists configuration while the repository write lock is held. It is used
// by workflow primitives such as custom task-type creation so a concurrent process cannot
// race the quota/rate-limit state in config.yaml.
func (tx *WriteTx) SaveConfig(c config.Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	path, err := tx.store.configFilePath(true, false)
	if err != nil {
		return fmt.Errorf("store: resolve config: %w", err)
	}
	return config.Save(path, c)
}
