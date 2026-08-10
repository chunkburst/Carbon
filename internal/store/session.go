package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"carbon/internal/session"
)

var (
	// ErrSessionNotFound is returned when a session id has no durable file.
	ErrSessionNotFound = errors.New("session not found")
	// ErrSessionConflict is returned when a stale session document would overwrite a newer one.
	ErrSessionConflict = errors.New("session changed since it was read")
	// ErrLiveSession is returned when a task already has a live session.
	ErrLiveSession = errors.New("task already has a live session")
	// ErrInvalidSessionID is returned when a caller supplies a session/live id that cannot
	// be mapped safely to a file in .carbon/sessions or .carbon/live.
	ErrInvalidSessionID = errors.New("invalid session id")
	// ErrWorktreeOutsideRoot is returned when session metadata points Git at a directory
	// outside this store's repository root.
	ErrWorktreeOutsideRoot = errors.New("session worktree must be within repository root")
)

// SessionDoc is a typed session plus its lossless YAML representation.
type SessionDoc struct {
	Session session.Session

	node    yaml.Node
	version string
}

func (s *Store) sessionsDir() string { return filepath.Join(s.root, carbonStoreDir, "sessions") }
func (s *Store) liveDir() string     { return filepath.Join(s.root, carbonStoreDir, "live") }
func (s *Store) sessionPath(id string) string {
	return filepath.Join(s.sessionsDir(), id+".yaml")
}
func (s *Store) livePath(id string) string {
	return filepath.Join(s.liveDir(), id+".json")
}

func validateSessionID(id string) error {
	return validateFileID(ErrInvalidSessionID, id)
}

// ResolveWorktree resolves worktree through symlinks and returns its canonical path only
// when it remains below this store's root. Empty worktrees mean the repository root;
// relative paths are interpreted below that root, never relative to the server process.
func (s *Store) ResolveWorktree(worktree string) (string, error) {
	return ResolveWorktreeWithin(s.root, worktree)
}

// ResolveWorktreeWithin applies the same canonical containment check as ResolveWorktree,
// but against an explicit allowed source root. Carbon stores durable session metadata in a
// shared cluster data root while the actual worktree belongs to the task's project source;
// callers must therefore supply that already-resolved project root instead of relying on a
// global trusted-path bypass.
func ResolveWorktreeWithin(allowedRoot, worktree string) (string, error) {
	root, err := resolveExistingStoreDir(allowedRoot)
	if err != nil {
		return "", fmt.Errorf("store: resolve allowed worktree root: %w", err)
	}
	path := worktree
	if path == "" {
		path = root
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	resolved, err := resolveExistingStoreDir(path)
	if err != nil {
		return "", fmt.Errorf("store: resolve session worktree %q: %w", worktree, err)
	}
	if !storePathWithin(root, resolved) {
		return "", fmt.Errorf("%w: %s", ErrWorktreeOutsideRoot, worktree)
	}
	return resolved, nil
}

func resolveExistingStoreDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("not a directory: %s", path)
	}
	return resolved, nil
}

func storePathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// GetSession reads one durable session.
func (s *Store) GetSession(id string) (*SessionDoc, error) {
	if err := s.projectClearReadBarrier(); err != nil {
		return nil, err
	}
	if err := validateSessionID(id); err != nil {
		return nil, err
	}
	path, err := s.sessionFilePath(id, false, true, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("store: resolve session %s: %w", id, err)
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("store: read session %s: %w", id, err)
	}
	d, err := parseSession(b)
	if err != nil {
		return nil, err
	}
	if d.Session.ID != id {
		return nil, fmt.Errorf("%w: session file %s declares %q", ErrInvalidSessionID, id, d.Session.ID)
	}
	d.version = hashBytes(b)
	return d, nil
}

// ListSessions reads every durable session, newest first.
func (s *Store) ListSessions() ([]*SessionDoc, error) {
	return s.listSessions()
}

func (s *Store) listSessions() ([]*SessionDoc, error) {
	if err := s.projectClearReadBarrier(); err != nil {
		return nil, err
	}
	dir, err := s.managedDir(false, carbonStoreDir, "sessions")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: scan sessions: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: scan sessions: %w", err)
	}
	docs := make([]*SessionDoc, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		fileID := strings.TrimSuffix(entry.Name(), ".yaml")
		if err := validateSessionID(fileID); err != nil {
			return nil, fmt.Errorf("store: invalid session filename %s: %w", entry.Name(), err)
		}
		path, err := s.managedFile(dir, entry.Name(), true, false)
		if err != nil {
			return nil, fmt.Errorf("store: resolve session %s: %w", entry.Name(), err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("store: read session %s: %w", entry.Name(), err)
		}
		d, err := parseSession(b)
		if err != nil {
			return nil, fmt.Errorf("store: parse session %s: %w", entry.Name(), err)
		}
		if d.Session.ID != fileID {
			return nil, fmt.Errorf("%w: session file %s declares %q", ErrInvalidSessionID, entry.Name(), d.Session.ID)
		}
		d.version = hashBytes(b)
		docs = append(docs, d)
	}
	sort.Slice(docs, func(i, j int) bool {
		return docs[i].Session.StartedAt.After(docs[j].Session.StartedAt)
	})
	return docs, nil
}

// TaskSessions returns durable sessions for one task, newest first.
func (s *Store) TaskSessions(taskID string) ([]*SessionDoc, error) {
	docs, err := s.listSessions()
	if err != nil {
		return nil, err
	}
	out := make([]*SessionDoc, 0, len(docs))
	for _, d := range docs {
		if d.Session.TaskID == taskID {
			out = append(out, d)
		}
	}
	return out, nil
}

// FindSessionByIdempotency returns the matching task operation, if any.
func (s *Store) FindSessionByIdempotency(taskID, key string) (*SessionDoc, error) {
	if key == "" {
		return nil, nil
	}
	docs, err := s.TaskSessions(taskID)
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		if d.Session.IdempotencyKey == key {
			return d, nil
		}
	}
	return nil, nil
}

// FindSessionByIdempotency reads a retry key inside an existing transaction.
func (tx *WriteTx) FindSessionByIdempotency(taskID, key string) (*SessionDoc, error) {
	return tx.store.FindSessionByIdempotency(taskID, key)
}

// LiveSession returns the active durable session for taskID, if one exists.
func (s *Store) LiveSession(taskID string) (*SessionDoc, error) {
	docs, err := s.TaskSessions(taskID)
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		if d.Session.Status == session.StatusActive {
			return d, nil
		}
	}
	return nil, nil
}

// LiveSession reads the task's current live session inside an existing transaction.
func (tx *WriteTx) LiveSession(taskID string) (*SessionDoc, error) {
	return tx.store.LiveSession(taskID)
}

// GetSession reads one durable session inside an existing transaction.
func (tx *WriteTx) GetSession(id string) (*SessionDoc, error) { return tx.store.GetSession(id) }

// ReadLive reads ephemeral session state inside an existing transaction.
func (tx *WriteTx) ReadLive(id string) (*session.Live, error) { return tx.store.ReadLive(id) }

// CreateSession persists a new durable session and optional live state.
func (s *Store) CreateSession(ctx context.Context, actor string, value session.Session, live *session.Live) (*SessionDoc, error) {
	if err := validateSessionID(value.ID); err != nil {
		return nil, err
	}
	if live != nil {
		if err := validateSessionID(live.SessionID); err != nil {
			return nil, err
		}
		if live.SessionID != value.ID {
			return nil, fmt.Errorf("%w: live session %q does not match durable session %q", ErrInvalidSessionID, live.SessionID, value.ID)
		}
		worktree, err := s.ResolveWorktree(live.Worktree)
		if err != nil {
			return nil, err
		}
		copy := *live
		copy.Worktree = worktree
		live = &copy
	}
	var created *SessionDoc
	err := s.Write(ctx, actor, "create session", func(tx *WriteTx) error {
		if current, err := tx.store.LiveSession(value.TaskID); err != nil {
			return err
		} else if current != nil {
			return fmt.Errorf("%w: %s", ErrLiveSession, current.Session.ID)
		}
		d, err := tx.CreateSession(value)
		if err != nil {
			return err
		}
		if live != nil {
			if err := tx.WriteLive(*live); err != nil {
				return err
			}
		}
		created = d
		return nil
	})
	return created, err
}

// SaveSession atomically updates a durable session under the repository lock.
func (s *Store) SaveSession(ctx context.Context, actor string, d *SessionDoc) error {
	if err := validateSessionID(d.Session.ID); err != nil {
		return err
	}
	return s.Write(ctx, actor, "save session", func(tx *WriteTx) error {
		return tx.SaveSession(d)
	})
}

// CreateSession writes a durable session inside an existing transaction.
func (tx *WriteTx) CreateSession(value session.Session) (*SessionDoc, error) {
	if err := validateSessionID(value.ID); err != nil {
		return nil, err
	}
	path, err := tx.store.sessionFilePath(value.ID, true, false, true)
	if err != nil {
		return nil, fmt.Errorf("store: resolve session %s: %w", value.ID, err)
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("store: session already exists: %s", value.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("store: stat session %s: %w", value.ID, err)
	}
	d, err := newSessionDoc(value)
	if err != nil {
		return nil, err
	}
	if err := tx.SaveSession(d); err != nil {
		return nil, err
	}
	return d, nil
}

// SaveSession writes a durable session inside an existing transaction.
func (tx *WriteTx) SaveSession(d *SessionDoc) error {
	if err := validateSessionID(d.Session.ID); err != nil {
		return err
	}
	path, err := tx.store.sessionFilePath(d.Session.ID, true, false, true)
	if err != nil {
		return fmt.Errorf("store: resolve session %s: %w", d.Session.ID, err)
	}
	if d.version != "" {
		current, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s was deleted", ErrSessionConflict, d.Session.ID)
		}
		if err != nil {
			return fmt.Errorf("store: reread session %s: %w", d.Session.ID, err)
		}
		if hashBytes(current) != d.version {
			return fmt.Errorf("%w: %s", ErrSessionConflict, d.Session.ID)
		}
	}
	b, err := renderSession(d)
	if err != nil {
		return err
	}
	if err := tx.store.writeAtomic(path, b); err != nil {
		return err
	}
	d.version = hashBytes(b)
	return nil
}

// ReadLive reads ephemeral state. A missing file means no heartbeat has been recorded.
func (s *Store) ReadLive(sessionID string) (*session.Live, error) {
	return s.ReadLiveWithin(sessionID, s.root)
}

// ReadLiveWithin reads live state only when its worktree canonically remains inside
// allowedRoot. It keeps Carbon's project-source validation at read time as well as write
// time, so a tampered live JSON file cannot redirect Git/session consumers elsewhere.
func (s *Store) ReadLiveWithin(sessionID, allowedRoot string) (*session.Live, error) {
	if err := s.projectClearReadBarrier(); err != nil {
		return nil, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	path, err := s.liveFilePath(sessionID, false, true, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: resolve live session %s: %w", sessionID, err)
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read live session %s: %w", sessionID, err)
	}
	var live session.Live
	if err := json.Unmarshal(b, &live); err != nil {
		return nil, fmt.Errorf("store: parse live session %s: %w", sessionID, err)
	}
	if err := validateSessionID(live.SessionID); err != nil {
		return nil, err
	}
	if live.SessionID != sessionID {
		return nil, fmt.Errorf("%w: live file %s declares %q", ErrInvalidSessionID, sessionID, live.SessionID)
	}
	worktree, err := ResolveWorktreeWithin(allowedRoot, live.Worktree)
	if err != nil {
		return nil, err
	}
	live.Worktree = worktree
	return &live, nil
}

// WriteLive atomically replaces ephemeral state inside an existing transaction.
func (tx *WriteTx) WriteLive(live session.Live) error {
	return tx.WriteLiveWithin(live, tx.store.root)
}

// WriteLiveWithin atomically writes live state after validating its worktree against an
// explicit allowed source root. Legacy callers keep WriteLive, which delegates to the
// store root; Carbon adapters use this only after resolving the owning task's project.
func (tx *WriteTx) WriteLiveWithin(live session.Live, allowedRoot string) error {
	if err := validateSessionID(live.SessionID); err != nil {
		return err
	}
	worktree, err := ResolveWorktreeWithin(allowedRoot, live.Worktree)
	if err != nil {
		return err
	}
	live.Worktree = worktree
	path, err := tx.store.liveFilePath(live.SessionID, true, false, true)
	if err != nil {
		return fmt.Errorf("store: resolve live session %s: %w", live.SessionID, err)
	}
	b, err := json.MarshalIndent(live, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal live session: %w", err)
	}
	b = append(b, '\n')
	return tx.store.writeAtomic(path, b)
}

// DeleteLive removes ephemeral state inside an existing transaction.
func (tx *WriteTx) DeleteLive(sessionID string) error {
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	path, err := tx.store.liveFilePath(sessionID, false, true, true)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: resolve live session %s: %w", sessionID, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store: remove live session %s: %w", sessionID, err)
	}
	return nil
}

// Replace updates the known session fields while preserving unknown YAML keys.
func (d *SessionDoc) Replace(next session.Session) {
	m := d.mapping()
	setScalar(m, "status", strNode(string(next.Status)))
	if next.EndedAt == nil {
		removeKey(m, "ended_at")
	} else {
		setScalar(m, "ended_at", tsNode(*next.EndedAt))
	}
	setOptionalScalar(m, "head_finished", next.HeadFinished)
	setOptionalScalar(m, "summary", next.Summary)
	setOptionalScalar(m, "cancel_reason", next.CancelReason)
	removeKey(m, "usage") // deprecated: usage tracking removed; purge the key on rewrite
	d.Session = next
}

func newSessionDoc(value session.Session) (*SessionDoc, error) {
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return nil, fmt.Errorf("store: encode session: %w", err)
	}
	return &SessionDoc{Session: value, node: node}, nil
}

func parseSession(b []byte) (*SessionDoc, error) {
	d := &SessionDoc{}
	if err := yaml.Unmarshal(b, &d.node); err != nil {
		return nil, fmt.Errorf("store: parse session: %w", err)
	}
	if err := d.node.Decode(&d.Session); err != nil {
		return nil, fmt.Errorf("store: decode session: %w", err)
	}
	if err := validateSessionID(d.Session.ID); err != nil {
		return nil, err
	}
	return d, nil
}

func renderSession(d *SessionDoc) ([]byte, error) {
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(d.mapping()); err != nil {
		return nil, fmt.Errorf("store: encode session: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("store: close session encoder: %w", err)
	}
	return out.Bytes(), nil
}

func (d *SessionDoc) mapping() *yaml.Node {
	if d.node.Kind == yaml.DocumentNode && len(d.node.Content) > 0 {
		return d.node.Content[0]
	}
	return &d.node
}

func setOptionalScalar(m *yaml.Node, key, value string) {
	if value == "" {
		removeKey(m, key)
		return
	}
	setScalar(m, key, strNode(value))
}
