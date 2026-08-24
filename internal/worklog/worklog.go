// Package worklog stores durable Worker-authored activity logs under
// .carbon/worklogs. It deliberately owns persistence and optimistic concurrency only;
// visibility and cross-Worker authorization are enforced by transport/scope layers.
package worklog

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"carbon/internal/identity"
	"carbon/internal/store"

	"gopkg.in/yaml.v3"
)

const (
	dataDir = "worklogs"

	// MaxListLimit caps one read so a corrupted or unusually large repository cannot
	// turn a Worker-log request into an unbounded response.
	MaxListLimit = 200

	maxEncodedBytes = 256 << 10
	maxActorBytes   = 256
	maxIDBytes      = 256
	maxTitleBytes   = 240
	maxBodyBytes    = 64 << 10
	maxTagBytes     = 64
	maxTags         = 32
	maxRecipients   = 16
	maxThreadBytes  = 48
)

var (
	// ErrNotFound identifies a requested Work Log that has no durable record.
	ErrNotFound = errors.New("work log not found")
	// ErrInvalidWorkLog covers invalid caller input as well as malformed durable YAML.
	// Callers should not attempt to repair malformed data implicitly.
	ErrInvalidWorkLog = errors.New("invalid work log")
	// ErrInvalidFilter identifies unsupported Work Log list constraints.
	ErrInvalidFilter = errors.New("invalid work log filter")
	// ErrCoordinationImmutable keeps a server-created collaboration envelope
	// append-only even if an internal caller reaches the persistence manager directly.
	ErrCoordinationImmutable = errors.New("work log coordination is immutable")
)

// Visibility controls which scope is eligible to read a Work Log. This package persists
// the declaration but deliberately does not decide whether a caller is in that scope.
type Visibility string

const (
	WorkerPrivate Visibility = "worker_private"
	ProjectPublic Visibility = "project_public"
	GlobalPublic  Visibility = "global_public"

	// Longer aliases make call sites self-documenting without creating a second wire form.
	VisibilityWorkerPrivate = WorkerPrivate
	VisibilityProjectPublic = ProjectPublic
	VisibilityGlobalPublic  = GlobalPublic
)

// CoordinationVersion identifies the first server-owned collaboration envelope.
// Missing Coordination remains a normal, fully compatible historical record.
const CoordinationVersion = 1

// Coordination is server-owned metadata attached only by the dedicated
// worklog_draft_send Service primitive. Tags remain useful presentation/search hints,
// but authorization must use this durable, versioned envelope rather than a
// user-controlled tag that older records might already contain.
type Coordination struct {
	Version    int      `yaml:"version" json:"version"`
	Recipients []string `yaml:"recipients,omitempty" json:"recipients,omitempty"`
	Thread     string   `yaml:"thread,omitempty" json:"thread,omitempty"`
}

// Log is the complete durable Work Log record. Version is an opaque SHA-256 fingerprint
// calculated from the serialized YAML; it is returned to clients but never persisted.
type Log struct {
	ID         string     `yaml:"id" json:"id"`
	Worker     string     `yaml:"worker" json:"worker"`
	Visibility Visibility `yaml:"visibility" json:"visibility"`
	// Standalone marks a log stored in a project-owned private Carbon root. Such
	// records deliberately have no ClusterID and must retain their ProjectID.
	Standalone bool     `yaml:"standalone,omitempty" json:"standalone,omitempty"`
	ClusterID  string   `yaml:"cluster_id" json:"cluster_id"`
	ProjectID  string   `yaml:"project_id,omitempty" json:"project_id,omitempty"`
	TaskID     string   `yaml:"task_id,omitempty" json:"task_id,omitempty"`
	Title      string   `yaml:"title" json:"title"`
	Body       string   `yaml:"body,omitempty" json:"body,omitempty"`
	Tags       []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	// Coordination is server-owned and optional for backward compatibility. It is
	// exposed to clients so they can render collaboration routing, but generic write
	// surfaces must never be able to create, replace, or remove it.
	Coordination *Coordination `yaml:"coordination,omitempty" json:"coordination,omitempty"`
	CreatedAt    string        `yaml:"created_at" json:"created_at"`
	CreatedBy    string        `yaml:"created_by" json:"created_by"`
	UpdatedAt    string        `yaml:"updated_at" json:"updated_at"`
	UpdatedBy    string        `yaml:"updated_by" json:"updated_by"`
	Version      string        `yaml:"-" json:"version,omitempty"`
}

// WorkLog is retained as an explicit name for integrations that prefer the product term.
type WorkLog = Log

// ETag returns Version in standard If-Match form. It is empty for caller-owned drafts.
func (l Log) ETag() string {
	if l.Version == "" {
		return ""
	}
	return `"` + l.Version + `"`
}

// Filter narrows a deterministic Work Log listing. Every non-empty field is conjunctive.
// Limit is required and must be in [1, MaxListLimit].
type Filter struct {
	Worker     string
	Visibility Visibility
	ProjectID  string
	TaskID     string
	Limit      int
}

// Manager binds Work Log persistence to one cluster's Store. It intentionally has no
// authorization policy: upper layers decide whether an actor may see or mutate a log.
type Manager struct {
	Store *store.Store
	Now   func() time.Time
}

func New(s *store.Store, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{Store: s, Now: now}
}

func (m *Manager) now() time.Time {
	if m.Now == nil {
		return time.Now().UTC()
	}
	return m.Now().UTC()
}

// Create persists a new Work Log. If ID is absent a cryptographically random log_ id is
// minted. Actor and Worker are validated independently: matching identities are an
// authorization decision for the caller, not a storage policy.
func (m *Manager) Create(ctx context.Context, actor string, log Log) (Log, error) {
	if m == nil || m.Store == nil {
		return Log{}, errors.New("worklog manager has no store")
	}
	if err := validateActor(actor); err != nil {
		return Log{}, err
	}
	if log.ID == "" {
		id, err := mintID()
		if err != nil {
			return Log{}, err
		}
		log.ID = id
	}
	if err := validateWritable(log); err != nil {
		return Log{}, err
	}

	now := m.now().Format(time.RFC3339Nano)
	log.CreatedAt, log.CreatedBy = now, actor
	log.UpdatedAt, log.UpdatedBy = now, actor
	log.Version = ""

	var out Log
	err := m.Store.Write(ctx, actor, "create work log", func(tx *store.WriteTx) error {
		name := filename(log.ID)
		if _, err := tx.ReadData(dataDir, name); err == nil {
			return fmt.Errorf("%w: duplicate id %q", ErrInvalidWorkLog, log.ID)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		data, err := yaml.Marshal(log)
		if err != nil {
			return fmt.Errorf("%w: encode: %v", ErrInvalidWorkLog, err)
		}
		if err := tx.WriteData(dataDir, name, data); err != nil {
			return err
		}
		log.Version = fingerprint(data)
		out = clone(log)
		return nil
	})
	return out, err
}

// Update replaces the caller-owned fields of a Work Log. Created metadata remains
// immutable; Updated metadata is stamped by this manager. expectedVersion accepts a raw
// Version or a quoted ETag and is optional for compatibility with existing Store APIs.
func (m *Manager) Update(ctx context.Context, actor string, log Log, expectedVersion string) (Log, error) {
	if m == nil || m.Store == nil {
		return Log{}, errors.New("worklog manager has no store")
	}
	if err := validateActor(actor); err != nil {
		return Log{}, err
	}
	if err := validateWritable(log); err != nil {
		return Log{}, err
	}

	var out Log
	err := m.Store.Write(ctx, actor, "update work log", func(tx *store.WriteTx) error {
		current, raw, err := readTx(tx, log.ID)
		if err != nil {
			return err
		}
		if err := matchVersion(fingerprint(raw), expectedVersion); err != nil {
			return err
		}
		if !equalCoordination(log.Coordination, current.Coordination) {
			return ErrCoordinationImmutable
		}

		log.CreatedAt, log.CreatedBy = current.CreatedAt, current.CreatedBy
		log.UpdatedAt, log.UpdatedBy = m.now().Format(time.RFC3339Nano), actor
		log.Version = ""
		data, err := yaml.Marshal(log)
		if err != nil {
			return fmt.Errorf("%w: encode: %v", ErrInvalidWorkLog, err)
		}
		if err := tx.WriteData(dataDir, filename(log.ID), data); err != nil {
			return err
		}
		log.Version = fingerprint(data)
		out = clone(log)
		return nil
	})
	return out, err
}

// Save is a compatibility alias for Update.
func (m *Manager) Save(ctx context.Context, actor string, log Log, expectedVersion string) (Log, error) {
	return m.Update(ctx, actor, log, expectedVersion)
}

// Get loads and strictly validates one Work Log.
func (m *Manager) Get(id string) (Log, error) {
	if m == nil || m.Store == nil {
		return Log{}, errors.New("worklog manager has no store")
	}
	if err := validateID(id); err != nil {
		return Log{}, err
	}
	data, err := m.Store.ReadData(dataDir, filename(id))
	if errors.Is(err, os.ErrNotExist) {
		return Log{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Log{}, err
	}
	log, err := decode(data)
	if err != nil {
		return Log{}, err
	}
	if log.ID != id {
		return Log{}, fmt.Errorf("%w: filename/id mismatch for %s", ErrInvalidWorkLog, id)
	}
	log.Version = fingerprint(data)
	return clone(log), nil
}

// List loads matching Work Logs in descending UpdatedAt order, with ID as a stable
// tie-breaker. A malformed YAML record is returned as an error rather than hidden.
func (m *Manager) List(filter Filter) ([]Log, error) {
	if m == nil || m.Store == nil {
		return nil, errors.New("worklog manager has no store")
	}
	if err := validateFilter(filter); err != nil {
		return nil, err
	}
	names, err := m.Store.ListData(dataDir)
	if err != nil {
		return nil, err
	}
	out := make([]Log, 0, min(len(names), filter.Limit))
	for _, name := range names {
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		id := strings.TrimSuffix(name, ".yaml")
		log, err := m.Get(id)
		if err != nil {
			return nil, err
		}
		if matches(log, filter) {
			out = append(out, log)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339Nano, out[i].UpdatedAt)
		right, _ := time.Parse(time.RFC3339Nano, out[j].UpdatedAt)
		if !left.Equal(right) {
			return left.After(right)
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

// Delete removes exactly one Work Log after an optional optimistic-version check.
func (m *Manager) Delete(ctx context.Context, actor, id, expectedVersion string) error {
	if m == nil || m.Store == nil {
		return errors.New("worklog manager has no store")
	}
	if err := validateActor(actor); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}
	return m.Store.Write(ctx, actor, "delete work log", func(tx *store.WriteTx) error {
		_, raw, err := readTx(tx, id)
		if err != nil {
			return err
		}
		if err := matchVersion(fingerprint(raw), expectedVersion); err != nil {
			return err
		}
		return tx.DeleteData(dataDir, filename(id))
	})
}

func readTx(tx *store.WriteTx, id string) (Log, []byte, error) {
	data, err := tx.ReadData(dataDir, filename(id))
	if errors.Is(err, os.ErrNotExist) {
		return Log{}, nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Log{}, nil, err
	}
	log, err := decode(data)
	if err != nil {
		return Log{}, nil, err
	}
	if log.ID != id {
		return Log{}, nil, fmt.Errorf("%w: filename/id mismatch for %s", ErrInvalidWorkLog, id)
	}
	log.Version = fingerprint(data)
	return log, data, nil
}

func decode(data []byte) (Log, error) {
	if len(data) == 0 || len(data) > maxEncodedBytes || !utf8.Valid(data) {
		return Log{}, fmt.Errorf("%w: invalid encoded YAML", ErrInvalidWorkLog)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var log Log
	if err := decoder.Decode(&log); err != nil {
		return Log{}, fmt.Errorf("%w: parse YAML: %v", ErrInvalidWorkLog, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Log{}, fmt.Errorf("%w: multiple YAML documents", ErrInvalidWorkLog)
		}
		return Log{}, fmt.Errorf("%w: parse YAML: %v", ErrInvalidWorkLog, err)
	}
	if err := validateStored(log); err != nil {
		return Log{}, err
	}
	return log, nil
}

func filename(id string) string { return id + ".yaml" }

func validateWritable(log Log) error {
	if err := validateID(log.ID); err != nil {
		return err
	}
	if err := validateWorker(log.Worker); err != nil {
		return err
	}
	if !validVisibility(log.Visibility) {
		return fmt.Errorf("%w: visibility %q", ErrInvalidWorkLog, log.Visibility)
	}
	if log.Standalone {
		if log.ClusterID != "" {
			return fmt.Errorf("%w: standalone work log cannot declare cluster_id", ErrInvalidWorkLog)
		}
		if err := validateRequiredOpaqueID("project_id", log.ProjectID); err != nil {
			return err
		}
	} else if err := validateRequiredOpaqueID("cluster_id", log.ClusterID); err != nil {
		return err
	}
	if log.ProjectID != "" {
		if err := validateOptionalOpaqueID("project_id", log.ProjectID); err != nil {
			return err
		}
	}
	if log.Visibility == ProjectPublic && log.ProjectID == "" {
		return fmt.Errorf("%w: project_public requires project_id", ErrInvalidWorkLog)
	}
	if log.TaskID != "" {
		if err := validateOptionalOpaqueID("task_id", log.TaskID); err != nil {
			return err
		}
	}
	if err := validateTitle(log.Title); err != nil {
		return err
	}
	if err := validateBody(log.Body); err != nil {
		return err
	}
	if err := validateTags(log.Tags); err != nil {
		return err
	}
	if err := validateCoordination(log); err != nil {
		return err
	}
	return nil
}

func validateCoordination(log Log) error {
	coordination := log.Coordination
	if coordination == nil {
		return nil
	}
	if coordination.Version != CoordinationVersion {
		return fmt.Errorf("%w: unsupported coordination version %d", ErrInvalidWorkLog, coordination.Version)
	}
	if log.Visibility != WorkerPrivate || log.ProjectID == "" || !identity.IsAgent(log.Worker) {
		return fmt.Errorf("%w: coordination requires an agent-owned project worker_private log", ErrInvalidWorkLog)
	}
	if len(coordination.Recipients) > maxRecipients {
		return fmt.Errorf("%w: coordination has more than %d recipients", ErrInvalidWorkLog, maxRecipients)
	}
	seen := make(map[string]struct{}, len(coordination.Recipients))
	for _, recipient := range coordination.Recipients {
		if !identity.IsAgent(recipient) {
			return fmt.Errorf("%w: coordination recipient %q is not a canonical agent", ErrInvalidWorkLog, recipient)
		}
		if _, exists := seen[recipient]; exists {
			return fmt.Errorf("%w: duplicate coordination recipient %q", ErrInvalidWorkLog, recipient)
		}
		seen[recipient] = struct{}{}
	}
	if coordination.Thread == "" {
		return nil
	}
	if len(coordination.Thread) > maxThreadBytes || !utf8.ValidString(coordination.Thread) || strings.TrimSpace(coordination.Thread) != coordination.Thread {
		return fmt.Errorf("%w: invalid coordination thread", ErrInvalidWorkLog)
	}
	for _, r := range coordination.Thread {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("%w: invalid coordination thread", ErrInvalidWorkLog)
	}
	return nil
}

func validateStored(log Log) error {
	if err := validateWritable(log); err != nil {
		return err
	}
	if !validTimestamp(log.CreatedAt) || !validTimestamp(log.UpdatedAt) {
		return fmt.Errorf("%w: invalid created_at or updated_at", ErrInvalidWorkLog)
	}
	createdAt, _ := time.Parse(time.RFC3339Nano, log.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339Nano, log.UpdatedAt)
	if updatedAt.Before(createdAt) {
		return fmt.Errorf("%w: updated_at precedes created_at", ErrInvalidWorkLog)
	}
	if err := validateActor(log.CreatedBy); err != nil {
		return fmt.Errorf("%w: created_by: %v", ErrInvalidWorkLog, err)
	}
	if err := validateActor(log.UpdatedBy); err != nil {
		return fmt.Errorf("%w: updated_by: %v", ErrInvalidWorkLog, err)
	}
	return nil
}

func validateFilter(filter Filter) error {
	if filter.Limit < 1 || filter.Limit > MaxListLimit {
		return fmt.Errorf("%w: limit must be 1..%d", ErrInvalidFilter, MaxListLimit)
	}
	if filter.Worker != "" {
		if err := validateWorker(filter.Worker); err != nil {
			return fmt.Errorf("%w: worker: %v", ErrInvalidFilter, err)
		}
	}
	if filter.Visibility != "" && !validVisibility(filter.Visibility) {
		return fmt.Errorf("%w: visibility %q", ErrInvalidFilter, filter.Visibility)
	}
	if filter.ProjectID != "" {
		if err := validateOptionalOpaqueID("project_id", filter.ProjectID); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidFilter, err)
		}
	}
	if filter.TaskID != "" {
		if err := validateOptionalOpaqueID("task_id", filter.TaskID); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidFilter, err)
		}
	}
	return nil
}

func matches(log Log, filter Filter) bool {
	return (filter.Worker == "" || log.Worker == filter.Worker) &&
		(filter.Visibility == "" || log.Visibility == filter.Visibility) &&
		(filter.ProjectID == "" || log.ProjectID == filter.ProjectID) &&
		(filter.TaskID == "" || log.TaskID == filter.TaskID)
}

func validVisibility(value Visibility) bool {
	return value == WorkerPrivate || value == ProjectPublic || value == GlobalPublic
}

func validateID(id string) error {
	if !strings.HasPrefix(id, "log_") || len(id) != len("log_")+32 {
		return fmt.Errorf("%w: id %q", ErrInvalidWorkLog, id)
	}
	for _, r := range id[len("log_"):] {
		if !((r >= 'a' && r <= 'f') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("%w: id %q", ErrInvalidWorkLog, id)
		}
	}
	return nil
}

func mintID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("%w: generate id: %v", ErrInvalidWorkLog, err)
	}
	return "log_" + hex.EncodeToString(bytes[:]), nil
}

func validateWorker(worker string) error {
	if err := validateActor(worker); err != nil {
		return fmt.Errorf("%w: worker must be a canonical actor: %v", ErrInvalidWorkLog, err)
	}
	return nil
}

// validateActor intentionally does not canonicalize or authorize the actor. Canonical
// identity is exact, so changing case or trimming would silently create a new Worker.
func validateActor(actor string) error {
	if actor == "" || len(actor) > maxActorBytes || !utf8.ValidString(actor) || strings.TrimSpace(actor) != actor {
		return fmt.Errorf("%w: actor must be a non-empty canonical identifier", ErrInvalidWorkLog)
	}
	for _, r := range actor {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: actor contains a control character", ErrInvalidWorkLog)
		}
	}
	return nil
}

func validateRequiredOpaqueID(field, value string) error {
	if value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidWorkLog, field)
	}
	return validateOptionalOpaqueID(field, value)
}

func validateOptionalOpaqueID(field, value string) error {
	if len(value) > maxIDBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: invalid %s", ErrInvalidWorkLog, field)
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("%w: invalid %s", ErrInvalidWorkLog, field)
	}
	return nil
}

func validateTitle(title string) error {
	if title == "" || len(title) > maxTitleBytes || !utf8.ValidString(title) || strings.TrimSpace(title) != title {
		return fmt.Errorf("%w: title must be non-empty and at most %d bytes", ErrInvalidWorkLog, maxTitleBytes)
	}
	for _, r := range title {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: title contains a control character", ErrInvalidWorkLog)
		}
	}
	return nil
}

func validateBody(body string) error {
	if len(body) > maxBodyBytes || !utf8.ValidString(body) {
		return fmt.Errorf("%w: body must be valid UTF-8 and at most %d bytes", ErrInvalidWorkLog, maxBodyBytes)
	}
	for _, r := range body {
		// Markdown logs legitimately contain line endings and indented code. Every other
		// control code is rejected to keep YAML, JSON, and terminal clients unambiguous.
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return fmt.Errorf("%w: body contains a control character", ErrInvalidWorkLog)
		}
	}
	return nil
}

func validateTags(tags []string) error {
	if len(tags) > maxTags {
		return fmt.Errorf("%w: at most %d tags", ErrInvalidWorkLog, maxTags)
	}
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if tag == "" || len(tag) > maxTagBytes || !utf8.ValidString(tag) || strings.TrimSpace(tag) != tag {
			return fmt.Errorf("%w: invalid tag", ErrInvalidWorkLog)
		}
		for _, r := range tag {
			if unicode.IsControl(r) {
				return fmt.Errorf("%w: tag contains a control character", ErrInvalidWorkLog)
			}
		}
		key := strings.ToLower(tag)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: duplicate tag %q", ErrInvalidWorkLog, tag)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validTimestamp(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func fingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func matchVersion(current, expected string) error {
	if expected == "" {
		return nil
	}
	expected = strings.TrimSpace(expected)
	if len(expected) >= 2 && expected[0] == '"' && expected[len(expected)-1] == '"' {
		expected = expected[1 : len(expected)-1]
	}
	if current != expected {
		return fmt.Errorf("%w: expected %q, got %q", store.ErrVersionMismatch, expected, current)
	}
	return nil
}

func clone(log Log) Log {
	log.Tags = slices.Clone(log.Tags)
	log.Coordination = cloneCoordination(log.Coordination)
	return log
}

func cloneCoordination(value *Coordination) *Coordination {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Recipients = slices.Clone(value.Recipients)
	return &copy
}

func equalCoordination(left, right *Coordination) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Version == right.Version && left.Thread == right.Thread && slices.Equal(left.Recipients, right.Recipients)
}
