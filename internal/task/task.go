// Package task is the heart of Carbon: the Task type plus the pure gate logic that
// decides what transitions are legal. It has no side effects and no dependency on
// config, store, or check — MCP verbs and the future UI both call these functions so
// the rules physically cannot diverge (SPEC §0, §9).
package task

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Check is a single gate-closing verification on a task (SPEC §5, §6).
// A Check with no Cmd is manual: its Result is set by attestation, not execution.
// Result is engine-managed and is one of: pending | pass | fail.
type Check struct {
	Desc    string
	Cmd     string
	Type    string // "manual" for a check with no Cmd; otherwise empty
	Result  string // pending | pass | fail
	Cwd     string // relative to repo root; defaults to repo root
	Timeout int    // seconds; falls back to config check_timeout_default
}

// Evidence is a durable, user-visible proof attached to a task. The engine owns the
// creation audit fields: adapters may supply Kind/Value/Label/URL, while the service
// stamps ID, CreatedAt, and CreatedBy for new entries. Existing entries retain their
// original audit data when edited.
type Evidence struct {
	ID        string `yaml:"id,omitempty" json:"id,omitempty"`
	Kind      string `yaml:"kind" json:"kind"`
	Value     string `yaml:"value" json:"value"`
	Label     string `yaml:"label,omitempty" json:"label,omitempty"`
	URL       string `yaml:"url,omitempty" json:"url,omitempty"`
	CreatedAt string `yaml:"created_at,omitempty" json:"createdAt,omitempty"`
	CreatedBy string `yaml:"created_by,omitempty" json:"createdBy,omitempty"`
}

const MaxEvidence = 64

// MaxBlockerReason bounds a free-text blocked-state explanation while leaving enough
// room for a concise diagnostic, reproduction detail, or handoff note.
const MaxBlockerReason = 4096

var evidenceKindRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// EvidenceKinds are the built-in proof kinds. ValidEvidenceKind also accepts a small,
// machine-safe extension key so integrations can add a stable kind without requiring a
// core release.
var EvidenceKinds = []string{"git_commit", "git_url", "artifact", "test_run", "other"}

// ValidEvidenceKind reports whether kind is one of the built-ins or a safe extension
// key. Free-form labels belong in Evidence.Label, not in the machine-facing kind.
func ValidEvidenceKind(kind string) bool {
	return evidenceKindRE.MatchString(kind)
}

// ValidateBlockerReason accepts normal Unicode text plus tabs and line breaks, while
// rejecting invalid UTF-8 and control bytes that could poison a YAML/JSON/log boundary.
// Empty is valid and means "clear the optional reason".
func ValidateBlockerReason(reason string) error {
	if !utf8.ValidString(reason) {
		return fmt.Errorf("%w: reason must be valid UTF-8", ErrInvalidBlockerReason)
	}
	if utf8.RuneCountInString(reason) > MaxBlockerReason {
		return fmt.Errorf("%w: reason exceeds %d characters", ErrInvalidBlockerReason, MaxBlockerReason)
	}
	for _, r := range reason {
		switch r {
		case '\n', '\r', '\t':
			continue
		}
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: reason contains a control character", ErrInvalidBlockerReason)
		}
	}
	return nil
}

// ValidateEvidence validates the caller-owned evidence payload. ID and creation audit
// fields are intentionally not constrained here: they are generated/preserved by the
// service, and this leaf package must remain usable for lossless reads of older files.
func ValidateEvidence(evidence []Evidence) error {
	if len(evidence) > MaxEvidence {
		return fmt.Errorf("%w: at most %d entries", ErrInvalidEvidence, MaxEvidence)
	}
	seen := make(map[string]struct{}, len(evidence))
	for i, item := range evidence {
		if !ValidEvidenceKind(item.Kind) {
			return fmt.Errorf("%w: entry %d has invalid kind %q", ErrInvalidEvidence, i, item.Kind)
		}
		if strings.TrimSpace(item.Value) == "" {
			return fmt.Errorf("%w: entry %d value is required", ErrInvalidEvidence, i)
		}
		if utf8.RuneCountInString(item.Value) > 2048 {
			return fmt.Errorf("%w: entry %d value exceeds 2048 characters", ErrInvalidEvidence, i)
		}
		if utf8.RuneCountInString(item.Label) > 256 {
			return fmt.Errorf("%w: entry %d label exceeds 256 characters", ErrInvalidEvidence, i)
		}
		if item.URL != "" {
			u, err := url.ParseRequestURI(item.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("%w: entry %d url must be an http or https URL", ErrInvalidEvidence, i)
			}
		}
		if item.ID != "" {
			if _, ok := seen[item.ID]; ok {
				return fmt.Errorf("%w: duplicate evidence id %q", ErrInvalidEvidence, item.ID)
			}
			seen[item.ID] = struct{}{}
		}
	}
	return nil
}

// Passed reports whether this check has succeeded.
func (c Check) Passed() bool { return c.Result == "pass" }

// Task is the gate-relevant view of a task file. The store parses task files into
// this struct for reads; lossless writes operate on the raw YAML node, not this type
// (SPEC §8), so unknown frontmatter keys are preserved outside of here.
type Task struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Status   string   `json:"status"`
	Assignee string   `json:"assignee,omitempty"`
	Deps     []string `json:"deps,omitempty"`
	Checks   []Check  `json:"checks,omitempty"`
	// ProjectID is a stable project scope. It is deliberately independent from the
	// task id/prefix: a task can be moved between projects without minting a new id.
	// An empty value is retained for legacy and cluster-wide tasks.
	ProjectID string `json:"project_id,omitempty"`
	// Type and Importance are independent workflow dimensions. Importance MUST NOT be
	// used as a substitute for Priority: priority remains the existing urgency/order
	// field below.
	Type       string `json:"type,omitempty"`
	Importance string `json:"importance,omitempty"`
	// Version is an opaque, read-only content fingerprint populated by the store. It
	// is never serialized into YAML and can be supplied back as an optimistic token.
	Version string `json:"version,omitempty"`
	// Optional, caller-owned organization fields. None gate transitions (only deps do);
	// Parent is grouping/rollup (epics & sub-tasks).
	Labels   []string `json:"labels,omitempty"`
	Priority string   `json:"priority,omitempty"` // "" | low | medium | high | urgent
	Parent   string   `json:"parent,omitempty"`   // id of the parent task, or ""
	Rank     float64  `json:"rank,omitempty"`     // manual board ordering; 0 = unset (falls back to id order)
	// ActiveAttempt identifies the session attempt currently eligible for review.
	ActiveAttempt string `json:"active_attempt,omitempty"`
	// Lease is the current ownership lease. It is optional so old task files stay
	// valid; an expired lease is treated as unowned by the lease manager.
	Lease *Lease `json:"lease,omitempty"`
	// PendingClaims are explicit approval requests made when an active assignment or
	// lease conflicts with a claimant. Keeping them on the task makes the conflict
	// visible and auditable rather than silently discarding it.
	PendingClaims []ClaimRequest `json:"pending_claims,omitempty"`
	// Trash is set only while the task lives in .carbon/trash. The normal task store
	// ignores it, but retaining it in the typed model lets trash/search/audit callers
	// inspect lifecycle metadata without reparsing YAML.
	Trash *TrashInfo `json:"trash,omitempty"`
	// BlockerReason is a caller-owned note associated with a blocked task state. It
	// deliberately does not gate transitions: leaving blocked retains the historical
	// explanation until a caller explicitly clears or updates it.
	BlockerReason string     `yaml:"blocker_reason,omitempty" json:"blockerReason,omitempty"`
	Evidence      []Evidence `yaml:"evidence,omitempty" json:"evidence,omitempty"`
}

// Lease is a durable, renewable ownership grant. All timestamps use RFC3339 UTC strings
// because task YAML is intentionally human-editable and lossless at the store boundary.
type Lease struct {
	ID         string `yaml:"id" json:"id"`
	Holder     string `yaml:"holder" json:"holder"`
	AcquiredAt string `yaml:"acquired_at" json:"acquired_at"`
	ExpiresAt  string `yaml:"expires_at" json:"expires_at"`
	RenewedAt  string `yaml:"renewed_at,omitempty" json:"renewed_at,omitempty"`
}

// ClaimRequest records an assignment conflict awaiting an explicit approver decision.
// Requests are append-safe and may be removed by a successful claim or reassignment.
type ClaimRequest struct {
	RequestID       string `yaml:"request_id" json:"request_id"`
	Actor           string `yaml:"actor" json:"actor"`
	RequestedAt     string `yaml:"requested_at" json:"requested_at"`
	LeaseTTLSeconds int    `yaml:"lease_ttl_seconds,omitempty" json:"lease_ttl_seconds,omitempty"`
	Reason          string `yaml:"reason,omitempty" json:"reason,omitempty"`
}

// TrashInfo is attached before a task file is moved into the managed trash directory.
// OriginalProjectID keeps restoration deterministic even after a project rename/move.
type TrashInfo struct {
	TrashedAt         string `yaml:"trashed_at" json:"trashed_at"`
	TrashedBy         string `yaml:"trashed_by" json:"trashed_by"`
	Reason            string `yaml:"reason,omitempty" json:"reason,omitempty"`
	OriginalProjectID string `yaml:"original_project_id,omitempty" json:"original_project_id,omitempty"`
}

// Rules are the config-derived inputs the gate logic needs. They are passed in (rather
// than importing the config package) so task stays a leaf in the dependency graph
// (SPEC §9). The mcp/store layer builds a Rules from config.Config.
type Rules struct {
	Initial string   // the state new tasks start in
	Closed  []string // states considered "closed" (subset of States)
	States  []string // all valid states; empty disables target-state validation
	Review  string   // the review/handoff state, or ""; entry is gated on command checks
}

// IsClosed reports whether status is one of the closed states.
func (r Rules) IsClosed(status string) bool {
	return slices.Contains(r.Closed, status)
}

// IsState reports whether status is a valid state. When States is empty, validation is
// opt-out and every status is accepted.
func (r Rules) IsState(status string) bool {
	if len(r.States) == 0 {
		return true
	}
	return slices.Contains(r.States, status)
}

// Gate sentinels. CanTransition and ValidateDeps wrap these with offending detail via
// %w, so callers match with errors.Is — e.g. the mcp transition verb auto-runs checks
// when it sees ErrChecksNotPassed (SPEC §5, §7).
var (
	ErrUnknownState         = errors.New("unknown target state")
	ErrDepsNotClosed        = errors.New("dependencies not closed")
	ErrChecksNotPassed      = errors.New("checks not passed")
	ErrDanglingDep          = errors.New("dangling dependency")
	ErrCycle                = errors.New("dependency cycle")
	ErrParentMissing        = errors.New("parent not found")
	ErrParentCycle          = errors.New("parent cycle")
	ErrInvalidPriority      = errors.New("invalid priority")
	ErrInvalidImportance    = errors.New("invalid importance")
	ErrInvalidType          = errors.New("invalid task type")
	ErrHasChildren          = errors.New("task has child tasks")
	ErrHasDependents        = errors.New("task has dependents")
	ErrInvalidBlockerReason = errors.New("invalid blocker reason")
	ErrInvalidEvidence      = errors.New("invalid task evidence")
)

// Priorities are the allowed non-empty priority values (highest first).
var Priorities = []string{"urgent", "high", "medium", "low"}

// Importances are intentionally a separate, stable classification axis from
// Priorities. Their order is semantic/documentary, not a scheduling precedence.
var Importances = []string{"core", "important", "normal", "optional", "experimental"}

// DefaultTypes are the workflow primitives available in every repository. Custom task
// types are additive and are supplied by config/types; they never replace these keys.
var DefaultTypes = []string{"foundation", "library", "patch", "extension", "plugin"}

// ValidPriority reports whether p is "" (none) or one of Priorities.
func ValidPriority(p string) bool { return p == "" || slices.Contains(Priorities, p) }

// ValidImportance reports whether i is empty (legacy/unset) or one of the stable keys.
// New explicit creation primitives require a non-empty value; this permissive helper keeps
// old YAML fully compatible.
func ValidImportance(i string) bool { return i == "" || slices.Contains(Importances, i) }

// ValidImportanceKey reports whether i is one of the non-empty built-in keys.
func ValidImportanceKey(i string) bool { return slices.Contains(Importances, i) }

// ValidType reports whether kind is empty (legacy/unset) or appears in the supplied
// catalog. The built-in type list is always considered valid even if a caller passes nil.
func ValidType(kind string, allowed []string) bool {
	if kind == "" {
		return true
	}
	if slices.Contains(DefaultTypes, kind) {
		return true
	}
	return slices.Contains(allowed, kind)
}

// Closed reports whether t is currently in a closed state.
func Closed(t Task, r Rules) bool { return r.IsClosed(t.Status) }

// DepResolver looks up a single task by id. ok is false when no such task exists. It lets
// the deps gate fetch only a task's listed dependencies instead of loading the whole board.
type DepResolver func(id string) (Task, bool)

// ReadyFunc reports whether every dep of t resolves to a closed task, fetching each dep on
// demand via resolve. Readiness is derived, never stored (SPEC §4). A dep that does not
// resolve yields false defensively; the real dangling-dep error is raised once, at load, by
// ValidateDeps.
func ReadyFunc(t Task, resolve DepResolver, r Rules) bool {
	for _, id := range t.Deps {
		dep, ok := resolve(id)
		if !ok || !r.IsClosed(dep.Status) {
			return false
		}
	}
	return true
}

// Ready is ReadyFunc backed by a fully-loaded task map.
func Ready(t Task, all map[string]Task, r Rules) bool {
	return ReadyFunc(t, mapResolver(all), r)
}

// mapResolver adapts a loaded task map to a DepResolver.
func mapResolver(all map[string]Task) DepResolver {
	return func(id string) (Task, bool) { t, ok := all[id]; return t, ok }
}

// CanTransition returns nil if moving t to state `to` is allowed, otherwise a wrapped
// sentinel. The two gates (SPEC §5):
//
//  1. Deps gate  — cannot LEAVE the initial state unless all deps are closed.
//  2. Checks gate — cannot ENTER a closed state unless ALL checks pass; cannot ENTER the
//     review state unless all COMMAND checks pass (manual checks are attested during
//     review, per SPEC §6, so they're exempt at review entry). Zero checks passes
//     vacuously.
//
// Gating review entry is what makes handoff un-skippable: an agent cannot finish a session
// into review while its command checks are still pending or failing.
//
// All other transitions are free, including reopening a closed task. Both gates can
// apply at once (e.g. backlog -> done directly); the deps gate is reported first.
func CanTransition(t Task, to string, all map[string]Task, r Rules) error {
	return CanTransitionFunc(t, to, mapResolver(all), r)
}

// CanTransitionFunc is CanTransition with the deps gate resolving dependencies on demand via
// resolve, so a caller need not load the whole board to move one task.
func CanTransitionFunc(t Task, to string, resolve DepResolver, r Rules) error {
	if !r.IsState(to) {
		return fmt.Errorf("%w: %q", ErrUnknownState, to)
	}

	// Deps gate: triggered only when actually leaving the initial state.
	if t.Status == r.Initial && to != r.Initial && !ReadyFunc(t, resolve, r) {
		return fmt.Errorf("%w: %s cannot leave %q with unclosed deps", ErrDepsNotClosed, t.ID, r.Initial)
	}

	// Checks gate: closed entry requires every check; review entry requires every command
	// check (manual checks are attested later, during review).
	gatedClosed := r.IsClosed(to)
	gatedReview := r.Review != "" && to == r.Review && !gatedClosed
	if gatedClosed || gatedReview {
		for i, c := range t.Checks {
			if gatedReview && c.Cmd == "" {
				continue // manual check: attested during review, not before entry
			}
			if !c.Passed() {
				return fmt.Errorf("%w: check %d (%q) is %q", ErrChecksNotPassed, i, c.Desc, c.Result)
			}
		}
	}

	return nil
}

// ValidateParents checks the parent chain of every task: each parent must exist and the
// chain must not cycle (a task can't be its own ancestor). Parents are grouping only and
// never gate transitions.
func ValidateParents(all map[string]Task) error {
	for id, t := range all {
		seen := map[string]bool{id: true}
		for cur := t.Parent; cur != ""; {
			p, ok := all[cur]
			if !ok {
				return fmt.Errorf("%w: %s -> %s", ErrParentMissing, id, cur)
			}
			if seen[cur] {
				return fmt.Errorf("%w: %s", ErrParentCycle, id)
			}
			seen[cur] = true
			cur = p.Parent
		}
	}
	return nil
}

// ValidateDeletable reports whether the task id may be deleted: it refuses when any other
// task names it as a parent (children) or lists it in deps (dependents), since deleting would
// orphan the graph. Offending ids are sorted and listed in the error for a clear message.
func ValidateDeletable(id string, all map[string]Task) error {
	var children, dependents []string
	for tid, t := range all {
		if tid == id {
			continue
		}
		if t.Parent == id {
			children = append(children, tid)
		}
		if slices.Contains(t.Deps, id) {
			dependents = append(dependents, tid)
		}
	}
	if len(children) > 0 {
		slices.Sort(children)
		return fmt.Errorf("%w: %s blocked by %s", ErrHasChildren, id, strings.Join(children, ", "))
	}
	if len(dependents) > 0 {
		slices.Sort(dependents)
		return fmt.Errorf("%w: %s blocked by %s", ErrHasDependents, id, strings.Join(dependents, ", "))
	}
	return nil
}

// ValidateDeps checks the whole task set as a graph, for the store to call on load
// (SPEC §4): any dep id absent from all is a dangling-dep error; any cycle (including a
// self-loop) is a cycle error. Both are loud, non-recoverable load failures.
func ValidateDeps(all map[string]Task) error {
	// Dangling deps first: a missing node also can't be walked for cycle detection.
	for id, t := range all {
		for _, dep := range t.Deps {
			if _, ok := all[dep]; !ok {
				return fmt.Errorf("%w: %s -> %s", ErrDanglingDep, id, dep)
			}
		}
	}

	// Cycle detection via DFS with three colors (white=unseen, gray=on-stack, black=done).
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(all))

	var visit func(id string) error
	visit = func(id string) error {
		color[id] = gray
		for _, dep := range all[id].Deps {
			switch color[dep] {
			case gray:
				return fmt.Errorf("%w: %s -> %s", ErrCycle, id, dep)
			case white:
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		color[id] = black
		return nil
	}

	for id := range all {
		if color[id] == white {
			if err := visit(id); err != nil {
				return err
			}
		}
	}
	return nil
}
