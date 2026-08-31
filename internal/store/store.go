// Package store is Carbon's read-fresh file layer (SPEC §8). It parses task files
// losslessly, mutates frontmatter at the yaml.Node level (so unknown keys, ordering,
// and comments survive every write), saves atomically via temp+rename, scans the tasks
// directory, validates the dep graph on load, and serializes all writes behind one
// repository-wide advisory lock.
//
// The split is deliberate: reads decode into the typed task.Task convenience view, but
// writes operate on the raw node — never a struct round-trip, which would drop unknown
// keys and reorder fields.
package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"

	"carbon/internal/config"
	repopkg "carbon/internal/repo"
	"carbon/internal/task"
)

const carbonStoreDir = repopkg.CarbonDirName

// ErrNotFound is returned when a task id has no file.
var ErrNotFound = errors.New("task not found")

// ErrInvalidID is returned when a caller supplies a task id that cannot be mapped safely
// to a file in .carbon/tasks.
var ErrInvalidID = errors.New("invalid task id")

// ErrConflict is returned when a Doc read by Get is saved after its file changed underneath
// it (another process/actor wrote first). The caller should re-read and retry rather than
// clobber the newer state (optimistic concurrency, SPEC §8).
var ErrConflict = errors.New("task changed since it was read")

// ErrVersionMismatch is returned when a caller supplies an optimistic Version/ETag
// token that is no longer current. It wraps neither transport semantics nor a revision
// number: versions are opaque content hashes and must be treated as such.
var ErrVersionMismatch = errors.New("task version does not match")

// ErrNotEditable is returned when addressing a non-note provenance entry for edit/delete:
// only note entries (Did=="note") are mutable.
var ErrNotEditable = errors.New("only note provenance entries can be edited or deleted")

// ErrNoteNotFound is returned when the addressed note entry does not exist.
var ErrNoteNotFound = errors.New("note not found")

// ErrPathOutsideRoot is returned when a managed Carbon path resolves outside the
// repository root. Managed paths are checked through symlinks/reparse points before
// any store read, write, or deletion.
var ErrPathOutsideRoot = errors.New("store path escapes repository root")

// ErrTrashNotFound and ErrAlreadyTrashed identify managed soft-delete lifecycle errors.
var (
	ErrTrashNotFound   = errors.New("trashed task not found")
	ErrAlreadyTrashed  = errors.New("task is already in trash")
	ErrInvalidDataPath = errors.New("invalid managed data path")
)

// Store is rooted at a repo containing .carbon/. The mutex serializes goroutines sharing
// this instance; Write also takes a repository-wide OS lock for other Carbon processes.
// Reads stay lock-free because atomic rename prevents half-written files.
type Store struct {
	root string
	mu   sync.Mutex

	// Test seams only. Production keeps these nil and uses the durable helpers below.
	// Keeping them per-Store rather than package globals avoids cross-test interference
	// and makes rollback failure handling reproducible.
	atomicWriteFn func(path string, data []byte) error
	renameFn      func(oldPath, newPath string) error
}

// New returns a Store rooted at the given repo directory.
func New(root string) *Store { return &Store{root: root} }

func (s *Store) tasksDir() string   { return filepath.Join(s.root, carbonStoreDir, "tasks") }
func (s *Store) configPath() string { return filepath.Join(s.root, carbonStoreDir, "config.yaml") }
func (s *Store) lockPath() string   { return filepath.Join(s.root, carbonStoreDir, "write.lock") }
func (s *Store) taskPath(id string) string {
	return filepath.Join(s.tasksDir(), id+".md")
}

func validateFileID(kind error, id string) error {
	if id == "" || id == "." || id == ".." || strings.TrimSpace(id) != id {
		return fmt.Errorf("%w: %q", kind, id)
	}
	for _, r := range id {
		switch {
		case r == '-' || r == '_':
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			continue
		default:
			return fmt.Errorf("%w: %q", kind, id)
		}
	}
	return nil
}

func validateTaskID(id string) error {
	return validateFileID(ErrInvalidID, id)
}

// ValidateTaskID verifies that id is safe to map to a task filename. HTTP adapters that
// need to construct a filename pattern use this before touching the filesystem.
func ValidateTaskID(id string) error { return validateTaskID(id) }

// Root returns the repo root the store is bound to.
func (s *Store) Root() string { return s.root }

// RunsDir is the gitignored directory for check-run logs (SPEC §1, §6).
func (s *Store) RunsDir() string { return filepath.Join(s.root, carbonStoreDir, "runs") }

// Config loads config.yaml fresh (read-fresh, SPEC §8).
func (s *Store) Config() (config.Config, error) {
	path, err := s.configFilePath(false, true)
	if err != nil {
		return config.Config{}, fmt.Errorf("store: resolve config: %w", err)
	}
	return config.Load(path)
}

// SaveConfig validates and writes config.yaml. Engine-owned and small, so a struct
// round-trip is fine (unlike task files, which need node-level writes).
func (s *Store) SaveConfig(c config.Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	path, err := s.configFilePath(true, false)
	if err != nil {
		return fmt.Errorf("store: resolve config: %w", err)
	}
	return config.Save(path, c)
}

// Provenance is one audit entry (SPEC §2, §7). System entries (created/transitioned/...)
// are append-only; note entries (Did=="note") carry a stable ID so they can be edited or
// deleted in place, and EditedAt records the last edit.
type Provenance struct {
	ID       string `yaml:"id,omitempty"` // stable id, present on note entries only
	Who      string `yaml:"who"`
	At       string `yaml:"at"`
	Did      string `yaml:"did"`
	Text     string `yaml:"text,omitempty"`
	EditedAt string `yaml:"editedAt,omitempty"` // set when a note is edited in place
}

// Doc is a parsed task file: the typed view for reads plus the raw frontmatter node and
// body needed for lossless writes.
type Doc struct {
	Task       task.Task
	Provenance []Provenance
	Body       string // markdown after the frontmatter, preserved byte-for-byte

	node    yaml.Node // document node of the frontmatter
	version string    // content hash of the file at read time; "" for a never-saved Doc
}

// docFields is the read-side decode target. Unknown keys are intentionally absent here —
// they live only in the node and are preserved through it.
type docFields struct {
	ID            string              `yaml:"id"`
	Title         string              `yaml:"title"`
	Status        string              `yaml:"status"`
	BlockerReason string              `yaml:"blocker_reason"`
	Evidence      []task.Evidence     `yaml:"evidence"`
	Assignee      string              `yaml:"assignee"`
	ProjectID     string              `yaml:"project_id"`
	Type          string              `yaml:"type"`
	Importance    string              `yaml:"importance"`
	Deps          []string            `yaml:"deps"`
	Labels        []string            `yaml:"labels"`
	Priority      string              `yaml:"priority"`
	Parent        string              `yaml:"parent"`
	Rank          float64             `yaml:"rank"`
	ActiveAttempt string              `yaml:"active_attempt"`
	Lease         *task.Lease         `yaml:"lease"`
	PendingClaims []task.ClaimRequest `yaml:"pending_claims"`
	Trash         *task.TrashInfo     `yaml:"trash"`
	Checks        []checkFields       `yaml:"checks"`
	Provenance    []Provenance        `yaml:"provenance"`
}

type checkFields struct {
	Desc    string `yaml:"desc"`
	Cmd     string `yaml:"cmd"`
	Type    string `yaml:"type"`
	Result  string `yaml:"result"`
	Cwd     string `yaml:"cwd"`
	Timeout int    `yaml:"timeout"`
}

func (c checkFields) toTask() task.Check {
	return task.Check{Desc: c.Desc, Cmd: c.Cmd, Type: c.Type, Result: c.Result, Cwd: c.Cwd, Timeout: c.Timeout}
}

// Get reads and parses one task file.
func (s *Store) Get(id string) (*Doc, error) {
	if err := s.projectClearReadBarrier(); err != nil {
		return nil, err
	}
	if err := validateTaskID(id); err != nil {
		return nil, err
	}
	path, err := s.taskFilePath(id, false, true, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("store: resolve %s: %w", id, err)
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("store: read %s: %w", id, err)
	}
	d, err := parse(b)
	if err != nil {
		return nil, err
	}
	if d.Task.ID != id {
		return nil, fmt.Errorf("%w: task file %s declares %q", ErrInvalidID, id, d.Task.ID)
	}
	d.version = hashBytes(b) // baseline for optimistic-concurrency check on Save
	d.Task.Version = d.version
	return d, nil
}

// hashBytes is the content fingerprint used to detect a concurrent write.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Version returns the opaque content fingerprint captured when this document was read or
// most recently saved. It is suitable for optimistic concurrency but is not a sequence
// number and callers must not derive ordering from it.
func (d *Doc) Version() string { return d.version }

// ETag returns the HTTP-safe quoted representation of Version. It is exported here (not
// in an adapter) so MCP, HTTP, and local callers all use the exact same token semantics.
func (d *Doc) ETag() string {
	if d.version == "" {
		return ""
	}
	return `"` + d.version + `"`
}

// MatchVersion checks an opaque Version or quoted ETag against this document. Empty
// expected values intentionally mean "no precondition" for legacy callers.
func (d *Doc) MatchVersion(expected string) error {
	if expected == "" {
		return nil
	}
	expected = strings.TrimSpace(expected)
	if len(expected) >= 2 && expected[0] == '"' && expected[len(expected)-1] == '"' {
		expected = expected[1 : len(expected)-1]
	}
	if expected != d.version {
		return fmt.Errorf("%w: expected %q, got %q", ErrVersionMismatch, expected, d.version)
	}
	return nil
}

// ETag returns the current task ETag without exposing a mutable Doc to callers.
func (s *Store) ETag(id string) (string, error) {
	d, err := s.Get(id)
	if err != nil {
		return "", err
	}
	return d.ETag(), nil
}

// ListDocs scans and parses all task files (read-fresh) and validates the dep graph. It
// returns full Docs (including provenance) so callers can derive things like last-activity.
// A dangling dep or a cycle is a loud load failure (SPEC §4).
func (s *Store) ListDocs() ([]*Doc, error) {
	if err := s.projectClearReadBarrier(); err != nil {
		return nil, err
	}
	dir, err := s.managedDir(false, carbonStoreDir, "tasks")
	if err != nil {
		return nil, fmt.Errorf("store: scan tasks: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("store: scan tasks: %w", err)
	}
	var docs []*Doc
	all := make(map[string]task.Task)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		fileID := strings.TrimSuffix(e.Name(), ".md")
		if err := validateTaskID(fileID); err != nil {
			return nil, fmt.Errorf("store: invalid task filename %s: %w", e.Name(), err)
		}
		path, err := s.managedFile(dir, e.Name(), true, false)
		if err != nil {
			return nil, fmt.Errorf("store: resolve %s: %w", e.Name(), err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("store: read %s: %w", e.Name(), err)
		}
		d, err := parse(b)
		if err != nil {
			return nil, fmt.Errorf("store: parse %s: %w", e.Name(), err)
		}
		if d.Task.ID != fileID {
			return nil, fmt.Errorf("%w: task file %s declares %q", ErrInvalidID, e.Name(), d.Task.ID)
		}
		docs = append(docs, d)
		d.version = hashBytes(b)
		d.Task.Version = d.version
		all[d.Task.ID] = d.Task
	}
	if err := task.ValidateDeps(all); err != nil {
		return nil, err
	}
	if err := task.ValidateParents(all); err != nil {
		return nil, err
	}
	return docs, nil
}

// List scans the tasks directory (read-fresh) and validates the dep graph, returning the
// typed task views keyed by id.
func (s *Store) List() (map[string]task.Task, error) {
	docs, err := s.ListDocs()
	if err != nil {
		return nil, err
	}
	all := make(map[string]task.Task, len(docs))
	for _, d := range docs {
		all[d.Task.ID] = d.Task
	}
	return all, nil
}

// parse splits frontmatter from body, decodes both the typed view and the raw node.
func parse(b []byte) (*Doc, error) {
	fm, body, err := splitFrontmatter(b)
	if err != nil {
		return nil, err
	}
	d := &Doc{Body: string(body)}
	if err := yaml.Unmarshal(fm, &d.node); err != nil {
		return nil, fmt.Errorf("store: parse frontmatter: %w", err)
	}
	var f docFields
	if err := d.node.Decode(&f); err != nil {
		return nil, fmt.Errorf("store: decode frontmatter: %w", err)
	}
	d.Task = task.Task{ID: f.ID, Title: f.Title, Status: f.Status, BlockerReason: f.BlockerReason, Evidence: cloneEvidence(f.Evidence), Assignee: f.Assignee,
		ProjectID: f.ProjectID, Type: f.Type, Importance: f.Importance, Deps: f.Deps,
		Labels: f.Labels, Priority: f.Priority, Parent: f.Parent, Rank: f.Rank,
		ActiveAttempt: f.ActiveAttempt, Lease: cloneLease(f.Lease),
		PendingClaims: slices.Clone(f.PendingClaims), Trash: cloneTrashInfo(f.Trash)}
	for _, c := range f.Checks {
		d.Task.Checks = append(d.Task.Checks, c.toTask())
	}
	d.Provenance = f.Provenance
	if err := validateTaskID(d.Task.ID); err != nil {
		return nil, err
	}
	if err := task.ValidateBlockerReason(d.Task.BlockerReason); err != nil {
		return nil, err
	}
	if err := task.ValidateEvidence(d.Task.Evidence); err != nil {
		return nil, err
	}
	return d, nil
}

func cloneEvidence(in []task.Evidence) []task.Evidence {
	return slices.Clone(in)
}

func cloneLease(in *task.Lease) *task.Lease {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneTrashInfo(in *task.TrashInfo) *task.TrashInfo {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

// splitFrontmatter separates the leading `---`-fenced YAML from the markdown body. It
// recognizes both LF and CRLF fences while preserving the original bytes in the YAML and
// body, so a Windows checkout does not silently normalize task files on read.
func splitFrontmatter(b []byte) (fm, body []byte, err error) {
	opening := []byte("---\n")
	newline := []byte("\n")
	closing := []byte("\n---\n")
	terminal := []byte("\n---")
	if bytes.HasPrefix(b, []byte("---\r\n")) {
		opening = []byte("---\r\n")
		newline = []byte("\r\n")
		closing = []byte("\r\n---\r\n")
		terminal = []byte("\r\n---")
	}
	if !bytes.HasPrefix(b, opening) {
		return nil, nil, errors.New("store: missing frontmatter '---' fence")
	}
	rest := b[len(opening):]
	if i := bytes.Index(rest, closing); i >= 0 {
		return rest[:i+len(newline)], rest[i+len(closing):], nil
	}
	if bytes.HasSuffix(rest, terminal) {
		return rest[:len(rest)-len("---")], nil, nil
	}
	return nil, nil, errors.New("store: missing closing '---' fence")
}

// mapping returns the frontmatter's top-level mapping node.
func (d *Doc) mapping() *yaml.Node {
	if d.node.Kind == yaml.DocumentNode && len(d.node.Content) > 0 {
		return d.node.Content[0]
	}
	return &d.node
}

// SetStatus surgically updates the status value node (creating the key if absent) and
// the typed mirror. Engine-owned per SPEC §2.
func (d *Doc) SetStatus(status string) error {
	setScalar(d.mapping(), "status", strNode(status))
	d.Task.Status = status
	return nil
}

// SetPriority/SetParent/SetLabels surgically update the optional organization fields,
// removing the key entirely when cleared so frontmatter stays clean.
func (d *Doc) SetPriority(p string) error {
	if p == "" {
		removeKey(d.mapping(), "priority")
	} else {
		setScalar(d.mapping(), "priority", strNode(p))
	}
	d.Task.Priority = p
	return nil
}

// SetBlockerReason replaces the optional blocked-state explanation. An empty value
// removes the frontmatter key; it does not alter status or participate in transition
// gates.
func (d *Doc) SetBlockerReason(reason string) error {
	if err := task.ValidateBlockerReason(reason); err != nil {
		return err
	}
	if reason == "" {
		removeKey(d.mapping(), "blocker_reason")
	} else {
		setScalar(d.mapping(), "blocker_reason", strNode(reason))
	}
	d.Task.BlockerReason = reason
	return nil
}

// SetEvidence replaces the task's durable proof list. Empty evidence removes the key
// entirely so older task files stay compact. Validation lives in task so HTTP, MCP,
// and direct store callers share the same limits and URL/kind rules.
func (d *Doc) SetEvidence(evidence []task.Evidence) error {
	if err := task.ValidateEvidence(evidence); err != nil {
		return err
	}
	if len(evidence) == 0 {
		removeKey(d.mapping(), "evidence")
	} else {
		setScalar(d.mapping(), "evidence", evidenceNode(evidence))
	}
	d.Task.Evidence = cloneEvidence(evidence)
	return nil
}

func (d *Doc) SetParent(parent string) error {
	if parent == "" {
		removeKey(d.mapping(), "parent")
	} else {
		setScalar(d.mapping(), "parent", strNode(parent))
	}
	d.Task.Parent = parent
	return nil
}

// SetDeps replaces the dependency list for controlled graph/migration workflows. It does
// not validate existence/cycles itself; callers that alter several documents should use a
// transaction and task.ValidateDeps on the resulting graph before saving.
func (d *Doc) SetDeps(deps []string) error {
	if len(deps) == 0 {
		removeKey(d.mapping(), "deps")
	} else {
		setScalar(d.mapping(), "deps", strSeq(deps))
	}
	d.Task.Deps = slices.Clone(deps)
	return nil
}

func (d *Doc) SetLabels(labels []string) error {
	if len(labels) == 0 {
		removeKey(d.mapping(), "labels")
	} else {
		setScalar(d.mapping(), "labels", strSeq(labels))
	}
	d.Task.Labels = labels
	return nil
}

// SetRank sets the manual board ordering value (0 clears it).
func (d *Doc) SetRank(rank float64) error {
	if rank == 0 {
		removeKey(d.mapping(), "rank")
	} else {
		setScalar(d.mapping(), "rank", floatNode(rank))
	}
	d.Task.Rank = rank
	return nil
}

// SetAssignee sets the assignee (used by claim, SPEC §7).
func (d *Doc) SetAssignee(who string) error {
	if who == "" {
		removeKey(d.mapping(), "assignee")
	} else {
		setScalar(d.mapping(), "assignee", strNode(who))
	}
	d.Task.Assignee = who
	return nil
}

// SetProjectID changes the stable project scope without changing the task id. Empty is
// meaningful for cluster-wide tasks and removes the YAML key to preserve old-file shape.
func (d *Doc) SetProjectID(projectID string) error {
	if projectID == "" {
		removeKey(d.mapping(), "project_id")
	} else {
		setScalar(d.mapping(), "project_id", strNode(projectID))
	}
	d.Task.ProjectID = projectID
	return nil
}

// SetType updates the independent task-type classification. Validation against built-ins
// and repository custom types belongs to the caller/store creation primitive because a
// Doc deliberately does not import config.
func (d *Doc) SetType(kind string) error {
	if kind == "" {
		removeKey(d.mapping(), "type")
	} else {
		setScalar(d.mapping(), "type", strNode(kind))
	}
	d.Task.Type = kind
	return nil
}

// SetImportance updates the semantic importance classification. It intentionally leaves
// priority untouched; callers must use SetPriority for scheduling urgency.
func (d *Doc) SetImportance(importance string) error {
	if importance == "" {
		removeKey(d.mapping(), "importance")
	} else {
		setScalar(d.mapping(), "importance", strNode(importance))
	}
	d.Task.Importance = importance
	return nil
}

// SetLease replaces the current durable lease. Passing nil clears it.
func (d *Doc) SetLease(lease *task.Lease) error {
	if lease == nil {
		removeKey(d.mapping(), "lease")
	} else {
		setScalar(d.mapping(), "lease", leaseNode(*lease))
	}
	d.Task.Lease = cloneLease(lease)
	return nil
}

// SetPendingClaims replaces the visible approval queue. It copies the input so a caller
// cannot mutate the typed mirror after this method returns.
func (d *Doc) SetPendingClaims(claims []task.ClaimRequest) error {
	if len(claims) == 0 {
		removeKey(d.mapping(), "pending_claims")
	} else {
		setScalar(d.mapping(), "pending_claims", claimRequestsNode(claims))
	}
	d.Task.PendingClaims = slices.Clone(claims)
	return nil
}

// SetTrashInfo records or clears soft-delete metadata. It does not move a file; use the
// Store trash primitives so movement stays serialized and graph-safe.
func (d *Doc) SetTrashInfo(info *task.TrashInfo) error {
	if info == nil {
		removeKey(d.mapping(), "trash")
	} else {
		setScalar(d.mapping(), "trash", trashInfoNode(*info))
	}
	d.Task.Trash = cloneTrashInfo(info)
	return nil
}

// SetIDForMigration changes the frontmatter id only. It is intentionally explicit and
// does not rename a file; callers must use Store.ReassignTaskID/ImportTask so filename,
// optimistic version, and graph validation remain coordinated.
func (d *Doc) SetIDForMigration(id string) error {
	if err := validateTaskID(id); err != nil {
		return err
	}
	setScalar(d.mapping(), "id", strNode(id))
	d.Task.ID = id
	return nil
}

// SetActiveAttempt records the session attempt currently eligible for review.
func (d *Doc) SetActiveAttempt(id string) error {
	if id == "" {
		removeKey(d.mapping(), "active_attempt")
	} else {
		setScalar(d.mapping(), "active_attempt", strNode(id))
	}
	d.Task.ActiveAttempt = id
	return nil
}

// SetTitle updates the title value node and the typed mirror.
func (d *Doc) SetTitle(title string) error {
	setScalar(d.mapping(), "title", strNode(title))
	d.Task.Title = title
	return nil
}

// SetBody replaces the markdown body. The body lives outside the frontmatter node and is
// written byte-for-byte by save, so this is a plain field assignment; the caller owns any
// normalization.
func (d *Doc) SetBody(body string) error {
	d.Body = body
	return nil
}

// SetChecks replaces the entire checks sequence node and the typed mirror. Callers carry
// forward each retained check's Result; checksNode defaults an empty Result to "pending",
// so newly-added checks come out pending. Clearing to empty removes the key.
func (d *Doc) SetChecks(checks []task.Check) error {
	m := d.mapping()
	if len(checks) == 0 {
		removeKey(m, "checks")
	} else {
		setScalar(m, "checks", checksNode(checks))
	}
	d.Task.Checks = checks
	return nil
}

// SetCheckResult writes the result of the check at index i (engine-managed, SPEC §6).
func (d *Doc) SetCheckResult(i int, result string) error {
	checks, ok := mapGet(d.mapping(), "checks")
	if !ok || checks.Kind != yaml.SequenceNode || i < 0 || i >= len(checks.Content) {
		return fmt.Errorf("store: check index %d out of range", i)
	}
	setScalar(checks.Content[i], "result", strNode(result))
	if i < len(d.Task.Checks) {
		d.Task.Checks[i].Result = result
	}
	return nil
}

// AppendProvenance appends one audit entry to the provenance sequence, creating it if
// absent (SPEC §7: every write appends one). Entries are flow-style for clean diffs. Note
// entries get a stable id (so they can be edited/deleted later); ordinary system entries
// remain byte-identical to before this capability was added.
func (d *Doc) AppendProvenance(who, did, text string, at time.Time) error {
	m := d.mapping()
	seq, ok := mapGet(m, "provenance")
	if !ok {
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		m.Content = append(m.Content, strNode("provenance"), seq)
	}
	var id string
	entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
	if did == "note" {
		var err error
		if id, err = mintNoteID(); err != nil {
			return err
		}
		entry.Content = append(entry.Content, strNode("id"), strNode(id))
	}
	entry.Content = append(entry.Content,
		strNode("who"), strNode(who), strNode("at"), tsNode(at), strNode("did"), strNode(did))
	if text != "" {
		entry.Content = append(entry.Content, strNode("text"), strNode(text))
	}
	seq.Content = append(seq.Content, entry)
	d.Provenance = append(d.Provenance, Provenance{ID: id, Who: who, At: at.UTC().Format(time.RFC3339), Did: did, Text: text})
	return nil
}

// EnsureLastProvenanceID gives the newest source mutation a durable identity without
// changing historical system entries. Carbon's event-ledger recovery uses this narrow
// opt-in after a service has decided that a task mutation is externally subscribable.
// It must be called before the document is saved.
func (d *Doc) EnsureLastProvenanceID() (string, error) {
	if d == nil || len(d.Provenance) == 0 {
		return "", fmt.Errorf("store: no provenance entry to identify")
	}
	last := len(d.Provenance) - 1
	if id := d.Provenance[last].ID; id != "" {
		return id, nil
	}
	seq, ok := mapGet(d.mapping(), "provenance")
	if !ok || seq.Kind != yaml.SequenceNode || last >= len(seq.Content) || seq.Content[last].Kind != yaml.MappingNode {
		return "", fmt.Errorf("store: provenance node is unavailable")
	}
	id, err := mintNoteID()
	if err != nil {
		return "", err
	}
	entry := seq.Content[last]
	entry.Content = append([]*yaml.Node{strNode("id"), strNode(id)}, entry.Content...)
	d.Provenance[last].ID = id
	return id, nil
}

// findProvenance locates the provenance sequence node and the index of the target entry,
// addressing by id first (notes minted with one) and falling back to position (legacy notes
// with no id; pass id=="" and a 0-based index). seq.Content[i] aligns 1:1 with d.Provenance[i]
// because parse and AppendProvenance build both lists in lockstep.
func (d *Doc) findProvenance(id string, index int) (seq *yaml.Node, entry *yaml.Node, i int, err error) {
	m := d.mapping()
	seq, ok := mapGet(m, "provenance")
	if !ok || seq.Kind != yaml.SequenceNode {
		return nil, nil, -1, ErrNoteNotFound
	}
	if id != "" {
		for j, n := range seq.Content {
			if v, ok := mapGet(n, "id"); ok && v.Value == id {
				return seq, n, j, nil
			}
		}
		return nil, nil, -1, fmt.Errorf("%w: %s", ErrNoteNotFound, id)
	}
	if index < 0 || index >= len(seq.Content) {
		return nil, nil, -1, fmt.Errorf("%w: index %d", ErrNoteNotFound, index)
	}
	return seq, seq.Content[index], index, nil
}

// isNoteNode reports whether a provenance entry node is a free-text note (Did=="note").
func isNoteNode(entry *yaml.Node) bool {
	v, ok := mapGet(entry, "did")
	return ok && v.Value == "note"
}

// EditNote rewrites a note's text in place and stamps editedAt. Only note entries are
// editable; system entries are refused with ErrNotEditable. Address by id (preferred) or,
// for a legacy note without one, by 0-based provenance index (id=="").
func (d *Doc) EditNote(id string, index int, text string, at time.Time) error {
	_, entry, i, err := d.findProvenance(id, index)
	if err != nil {
		return err
	}
	if !isNoteNode(entry) {
		return ErrNotEditable
	}
	setScalar(entry, "text", strNode(text))
	setScalar(entry, "editedAt", tsNode(at))
	d.Provenance[i].Text = text
	d.Provenance[i].EditedAt = at.UTC().Format(time.RFC3339)
	return nil
}

// DeleteNote splices a note entry out of the provenance sequence and the typed mirror. Only
// note entries are deletable; system entries are refused with ErrNotEditable.
func (d *Doc) DeleteNote(id string, index int) error {
	seq, entry, i, err := d.findProvenance(id, index)
	if err != nil {
		return err
	}
	if !isNoteNode(entry) {
		return ErrNotEditable
	}
	seq.Content = append(seq.Content[:i], seq.Content[i+1:]...)
	d.Provenance = append(d.Provenance[:i], d.Provenance[i+1:]...)
	return nil
}

// DeleteTask hard-deletes a task file. It refuses if any task names this id as its parent
// (children) or lists it in deps (dependents), since deleting would orphan the graph
// (ValidateParents/ValidateDeps would then fail on load). The scan and unlink happen under
// the repository write lock so a child created concurrently can't slip through.
func (s *Store) DeleteTask(id, actor string) error {
	if err := validateTaskID(id); err != nil {
		return err
	}
	return s.Write(context.Background(), actor, "delete task", func(tx *WriteTx) error {
		path, err := s.taskFilePath(id, false, true, true)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		if err != nil {
			return fmt.Errorf("store: resolve delete %s: %w", id, err)
		}
		all, err := tx.Tasks() // validated board snapshot under the lock
		if err != nil {
			return err
		}
		if err := task.ValidateDeletable(id, all); err != nil {
			return err
		}
		// Resolve again immediately before unlinking so a changed final path cannot be
		// followed after graph validation.
		path, err = s.taskFilePath(id, false, true, true)
		if err != nil {
			return fmt.Errorf("store: resolve delete %s: %w", id, err)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("store: delete %s: %w", id, err)
		}
		return nil
	})
}

// Save renders the Doc and writes it atomically under the repository write lock (SPEC §8).
func (s *Store) Save(d *Doc) error {
	return s.Write(context.Background(), "", "save task", func(tx *WriteTx) error {
		return tx.SaveTask(d)
	})
}

// SaveIfVersion is Save with an optional optimistic precondition. expected accepts either
// a raw Version or the quoted ETag returned by Doc.ETag. An empty token preserves the
// legacy unconditional Save behaviour.
func (s *Store) SaveIfVersion(d *Doc, expected string) error {
	return s.Write(context.Background(), "", "save task with version", func(tx *WriteTx) error {
		if err := d.MatchVersion(expected); err != nil {
			return err
		}
		return tx.SaveTask(d)
	})
}

// save renders frontmatter from the node and recomposes the file. Callers must hold the
// repository write transaction.
// A Doc carrying a version (i.e. read via Get) must still match the on-disk file, else a
// concurrent writer changed it and we refuse with ErrConflict rather than clobber.
func (s *Store) save(d *Doc) error {
	if err := validateTaskID(d.Task.ID); err != nil {
		return err
	}
	path, err := s.taskFilePath(d.Task.ID, true, false, true)
	if err != nil {
		return fmt.Errorf("store: resolve %s: %w", d.Task.ID, err)
	}
	return s.saveToPath(d, path, true)
}

// saveToPath renders a Doc to a managed task-shaped file. It is shared by normal task
// saves and the soft-delete move/restore path; callers must already hold the write lock.
// checkVersion controls whether the Doc's read fingerprint is compared against path.
func (s *Store) saveToPath(d *Doc, path string, checkVersion bool) error {
	if checkVersion && d.version != "" {
		if err := s.checkVersion(d, path); err != nil {
			return err
		}
	}

	var fm bytes.Buffer
	enc := yaml.NewEncoder(&fm)
	enc.SetIndent(2) // match the 2-space convention so diffs stay clean
	if err := enc.Encode(d.mapping()); err != nil {
		return fmt.Errorf("store: encode frontmatter: %w", err)
	}
	enc.Close()

	var out bytes.Buffer
	out.WriteString("---\n")
	out.Write(fm.Bytes())
	out.WriteString("---\n")
	out.WriteString(d.Body)
	data := out.Bytes()
	if err := s.writeAtomic(path, data); err != nil {
		return err
	}
	d.version = hashBytes(data) // advance so the same Doc can be saved again
	d.Task.Version = d.version
	return nil
}

// checkVersion compares the on-disk file against the Doc's read-time fingerprint.
func (s *Store) checkVersion(d *Doc, path string) error {
	cur, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s was deleted", ErrConflict, d.Task.ID)
	}
	if err != nil {
		return fmt.Errorf("store: reread %s: %w", d.Task.ID, err)
	}
	if hashBytes(cur) != d.version {
		return fmt.Errorf("%w: %s", ErrConflict, d.Task.ID)
	}
	return nil
}

// Create mints a time-ordered, collision-resistant id under the repository lock and writes a
// new task file in the initial state with a `created` provenance entry (SPEC §3, §7). No
// shared counter is touched, so concurrent creators in separate clones never collide.
// Draft is the caller-supplied content for a new task. Id and status are engine-assigned.
type Draft struct {
	Title         string
	Body          string
	BlockerReason string
	Evidence      []task.Evidence
	Deps          []string
	Checks        []task.Check
	Labels        []string
	Priority      string
	Parent        string
	Rank          float64
	ProjectID     string
	// ProjectIDSet distinguishes deliberate cluster-wide empty from legacy omission.
	ProjectIDSet bool
	Type         string
	Importance   string
}

// ExplicitDraft is the Carbon creation primitive. Unlike the legacy Draft/Create pair,
// it requires a deliberate Type and Importance choice. ProjectID remains allowed to be
// empty for cluster-wide tasks; when omitted it falls back to config.ProjectID.
type ExplicitDraft struct {
	Title         string
	Body          string
	BlockerReason string
	Evidence      []task.Evidence
	Deps          []string
	Checks        []task.Check
	Labels        []string
	Priority      string
	Parent        string
	Rank          float64
	ProjectID     string
	// ClusterWide explicitly preserves an empty project_id rather than falling back to
	// config.ProjectID. It is meaningful only when ProjectID itself is empty.
	ClusterWide bool
	Type        string
	Importance  string
}

func (d ExplicitDraft) legacy() Draft {
	return Draft{
		Title: d.Title, Body: d.Body, BlockerReason: d.BlockerReason, Evidence: d.Evidence, Deps: d.Deps, Checks: d.Checks, Labels: d.Labels,
		Priority: d.Priority, Parent: d.Parent, Rank: d.Rank, ProjectID: d.ProjectID,
		ProjectIDSet: d.ClusterWide || d.ProjectID != "",
		Type:         d.Type, Importance: d.Importance,
	}
}

func (s *Store) Create(draft Draft, actor string, at time.Time) (*Doc, error) {
	return s.create(context.Background(), draft, actor, at, false, nil, nil)
}

// CreateExplicit creates through the strict Carbon primitive. Type and Importance must
// be supplied, which prevents an adapter from accidentally smuggling scheduling priority
// into importance. It uses the wall clock because explicit API callers do not inject a
// testing clock; the legacy Create remains available for deterministic tests.
func (s *Store) CreateExplicit(ctx context.Context, actor string, draft ExplicitDraft) (*Doc, error) {
	return s.create(ctx, draft.legacy(), actor, time.Now(), true, nil, nil)
}

// CreateExplicit creates a strict Carbon task while the caller already holds this
// store's write transaction. It lets compound workflows validate their input and
// create a task beneath one repository lock instead of opening a second transaction.
func (tx *WriteTx) CreateExplicit(actor string, draft ExplicitDraft, at time.Time) (*Doc, error) {
	return tx.store.create(context.Background(), draft.legacy(), actor, at, true, tx, nil)
}

// CreateExplicitWithBeforeSave is the strict creation primitive for one bounded
// compound workflow. The hook runs under the caller's WriteTx after the initial
// provenance record exists but before the task itself is persisted.
func (tx *WriteTx) CreateExplicitWithBeforeSave(actor string, draft ExplicitDraft, at time.Time, beforeSave func(*Doc) error) (*Doc, error) {
	if beforeSave == nil {
		return tx.CreateExplicit(actor, draft, at)
	}
	return tx.store.create(context.Background(), draft.legacy(), actor, at, true, tx, beforeSave)
}

func (s *Store) create(ctx context.Context, draft Draft, actor string, at time.Time, strict bool, existing *WriteTx, beforeSave func(*Doc) error) (*Doc, error) {
	var created *Doc
	create := func(tx *WriteTx) error {
		cfg, err := tx.Config()
		if err != nil {
			return err
		}
		if strict && draft.Type == "" {
			return fmt.Errorf("%w: explicit creation requires type", task.ErrInvalidType)
		}
		if strict && !task.ValidImportanceKey(draft.Importance) {
			return fmt.Errorf("%w: explicit creation requires one of %s", task.ErrInvalidImportance, strings.Join(task.Importances, ", "))
		}
		if !task.ValidPriority(draft.Priority) {
			return fmt.Errorf("%w: %q", task.ErrInvalidPriority, draft.Priority)
		}
		if err := task.ValidateBlockerReason(draft.BlockerReason); err != nil {
			return err
		}
		if err := task.ValidateEvidence(draft.Evidence); err != nil {
			return err
		}
		if !task.ValidImportance(draft.Importance) {
			return fmt.Errorf("%w: %q", task.ErrInvalidImportance, draft.Importance)
		}
		kind := draft.Type
		if kind == "" {
			kind = task.DefaultTypes[0]
		}
		if !cfg.TypeCatalog().Allowed(kind) {
			return fmt.Errorf("%w: %q", task.ErrInvalidType, kind)
		}
		importance := draft.Importance
		if importance == "" {
			importance = "normal"
		}
		projectID := draft.ProjectID
		if projectID == "" && !draft.ProjectIDSet {
			projectID = cfg.ProjectID
		}
		// Mint a time-ordered, collision-resistant id (SPEC §3). No counter is read or
		// written, so concurrent creates in separate clones never collide. The existence
		// check is a belt-and-suspenders guard against the astronomically unlikely case of
		// a same-millisecond, same-random clash within this repo.
		var id string
		for range 5 {
			candidate, err := mintTaskID(cfg.Prefix, at)
			if err != nil {
				return err
			}
			candidatePath, pathErr := s.taskFilePath(candidate, true, false, true)
			if pathErr != nil {
				return fmt.Errorf("store: resolve %s: %w", candidate, pathErr)
			}
			if _, statErr := os.Lstat(candidatePath); os.IsNotExist(statErr) {
				id = candidate
				break
			} else if statErr != nil {
				return fmt.Errorf("store: stat %s: %w", candidate, statErr)
			}
		}
		if id == "" {
			return fmt.Errorf("store: could not mint a unique task id after 5 attempts")
		}

		d := &Doc{Body: draft.Body}
		d.node = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
		m := d.mapping()
		m.Content = append(m.Content, strNode("id"), strNode(id))
		m.Content = append(m.Content, strNode("title"), strNode(draft.Title))
		m.Content = append(m.Content, strNode("status"), strNode(cfg.Initial))
		if draft.BlockerReason != "" {
			m.Content = append(m.Content, strNode("blocker_reason"), strNode(draft.BlockerReason))
		}
		if len(draft.Evidence) > 0 {
			m.Content = append(m.Content, strNode("evidence"), evidenceNode(draft.Evidence))
		}
		if projectID != "" {
			m.Content = append(m.Content, strNode("project_id"), strNode(projectID))
		}
		m.Content = append(m.Content, strNode("type"), strNode(kind))
		m.Content = append(m.Content, strNode("importance"), strNode(importance))
		if draft.Priority != "" {
			m.Content = append(m.Content, strNode("priority"), strNode(draft.Priority))
		}
		if len(draft.Labels) > 0 {
			m.Content = append(m.Content, strNode("labels"), strSeq(draft.Labels))
		}
		if draft.Parent != "" {
			m.Content = append(m.Content, strNode("parent"), strNode(draft.Parent))
		}
		if draft.Rank != 0 {
			m.Content = append(m.Content, strNode("rank"), floatNode(draft.Rank))
		}
		if len(draft.Deps) > 0 {
			m.Content = append(m.Content, strNode("deps"), strSeq(draft.Deps))
		}
		if len(draft.Checks) > 0 {
			m.Content = append(m.Content, strNode("checks"), checksNode(draft.Checks))
		}
		d.Task = task.Task{ID: id, Title: draft.Title, Status: cfg.Initial, BlockerReason: draft.BlockerReason, Evidence: cloneEvidence(draft.Evidence), ProjectID: projectID,
			Type: kind, Importance: importance, Deps: draft.Deps, Checks: draft.Checks,
			Labels: draft.Labels, Priority: draft.Priority, Parent: draft.Parent, Rank: draft.Rank}

		if err := d.AppendProvenance(actor, "created", "", at); err != nil {
			return err
		}
		if beforeSave != nil {
			if err := beforeSave(d); err != nil {
				return err
			}
		}
		if err := tx.SaveTask(d); err != nil {
			return err
		}
		created = d
		return nil
	}
	var err error
	if existing != nil {
		err = create(existing)
	} else {
		err = s.Write(ctx, actor, "create task", create)
	}
	return created, err
}

// --- yaml.Node helpers ---

func strNode(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }
func intNode(i int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(i)}
}
func floatNode(f float64) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(f, 'f', -1, 64)}
}
func tsNode(t time.Time) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!timestamp", Value: t.UTC().Format(time.RFC3339)}
}

func strSeq(items []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, it := range items {
		n.Content = append(n.Content, strNode(it))
	}
	return n
}

func checksNode(checks []task.Check) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, c := range checks {
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		m.Content = append(m.Content, strNode("desc"), strNode(c.Desc))
		if c.Cmd != "" {
			m.Content = append(m.Content, strNode("cmd"), strNode(c.Cmd))
		}
		if c.Type != "" {
			m.Content = append(m.Content, strNode("type"), strNode(c.Type))
		}
		if c.Cwd != "" {
			m.Content = append(m.Content, strNode("cwd"), strNode(c.Cwd))
		}
		if c.Timeout != 0 {
			m.Content = append(m.Content, strNode("timeout"), intNode(c.Timeout))
		}
		result := c.Result
		if result == "" {
			result = "pending"
		}
		m.Content = append(m.Content, strNode("result"), strNode(result))
		seq.Content = append(seq.Content, m)
	}
	return seq
}

func evidenceNode(evidence []task.Evidence) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, item := range evidence {
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if item.ID != "" {
			m.Content = append(m.Content, strNode("id"), strNode(item.ID))
		}
		m.Content = append(m.Content, strNode("kind"), strNode(item.Kind), strNode("value"), strNode(item.Value))
		if item.Label != "" {
			m.Content = append(m.Content, strNode("label"), strNode(item.Label))
		}
		if item.URL != "" {
			m.Content = append(m.Content, strNode("url"), strNode(item.URL))
		}
		if item.CreatedAt != "" {
			m.Content = append(m.Content, strNode("created_at"), strNode(item.CreatedAt))
		}
		if item.CreatedBy != "" {
			m.Content = append(m.Content, strNode("created_by"), strNode(item.CreatedBy))
		}
		seq.Content = append(seq.Content, m)
	}
	return seq
}

func leaseNode(lease task.Lease) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content,
		strNode("id"), strNode(lease.ID),
		strNode("holder"), strNode(lease.Holder),
		strNode("acquired_at"), strNode(lease.AcquiredAt),
		strNode("expires_at"), strNode(lease.ExpiresAt),
	)
	if lease.RenewedAt != "" {
		m.Content = append(m.Content, strNode("renewed_at"), strNode(lease.RenewedAt))
	}
	return m
}

func claimRequestsNode(claims []task.ClaimRequest) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, claim := range claims {
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if claim.RequestID != "" {
			m.Content = append(m.Content, strNode("request_id"), strNode(claim.RequestID))
		}
		m.Content = append(m.Content,
			strNode("actor"), strNode(claim.Actor),
			strNode("requested_at"), strNode(claim.RequestedAt),
		)
		if claim.LeaseTTLSeconds > 0 {
			m.Content = append(m.Content, strNode("lease_ttl_seconds"), intNode(claim.LeaseTTLSeconds))
		}
		if claim.Reason != "" {
			m.Content = append(m.Content, strNode("reason"), strNode(claim.Reason))
		}
		seq.Content = append(seq.Content, m)
	}
	return seq
}

func trashInfoNode(info task.TrashInfo) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content,
		strNode("trashed_at"), strNode(info.TrashedAt),
		strNode("trashed_by"), strNode(info.TrashedBy),
	)
	if info.Reason != "" {
		m.Content = append(m.Content, strNode("reason"), strNode(info.Reason))
	}
	if info.OriginalProjectID != "" {
		m.Content = append(m.Content, strNode("original_project_id"), strNode(info.OriginalProjectID))
	}
	return m
}

// mapGet returns the value node for key in a mapping node.
func mapGet(m *yaml.Node, key string) (*yaml.Node, bool) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1], true
		}
	}
	return nil, false
}

// removeKey deletes a key and its value node from a mapping, if present.
func removeKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// setScalar replaces the value node for key, or appends key+val if the key is absent.
func setScalar(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content, strNode(key), val)
}
