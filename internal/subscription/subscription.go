// Package subscription owns Carbon's project-scoped Agent event subscriptions.
//
// A subscription is intentionally not project configuration: it belongs to one
// fixed Agent and project, while the append-only ledger is a separate project
// record. The first delivery implementation is durable polling only. It never
// pretends that a long poll, UI SSE stream, or local notification can wake an
// Agent through MCP.
package subscription

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

	"carbon/internal/store"

	"gopkg.in/yaml.v3"
)

const (
	subscriptionDir = "event-subscriptions"
	ledgerDir       = "event-ledgers"
	pendingDir      = "event-ledger-pending"
	stateVersion    = 1
	maxStateBytes   = 1024 << 10
	// A fully retained safe-summary ledger is intentionally larger than the
	// small subscription and one-marker records. 32768 bounded entries do not
	// fit in the 1 MiB control-plane sidecar limit, so keep a separate hard cap
	// rather than advertising a retention capacity the decoder cannot reopen.
	maxLedgerStateBytes = 16 << 20
	maxSubscriptions    = 512
	maxReceipts         = 32
	maxLedgerEvents     = 32768
	maxFilters          = 32
	maxCursorWait       = 30 * time.Second
	defaultPollLimit    = 50
	maxPollLimit        = 200
	pollRetryInterval   = 100 * time.Millisecond
)

var (
	ErrInvalidSubscription  = errors.New("invalid event subscription")
	ErrSubscriptionNotFound = errors.New("event subscription not found")
	ErrIdempotencyConflict  = errors.New("subscription idempotency key conflicts with a different request")
	ErrExpectedVersion      = errors.New("subscription update requires the current expected version")
	ErrVersionConflict      = errors.New("subscription version conflict")
	ErrInvalidCursor        = errors.New("invalid event subscription cursor")
	ErrCursorExpired        = errors.New("event subscription cursor expired; reinitialize with a new idempotency key and expected version")
	ErrCursorRegression     = errors.New("event subscription cursor cannot move backwards")
)

type Mode string

const (
	ModePassive Mode = "passive"
	ModeMixed   Mode = "mixed"
	ModeActive  Mode = "active"
)

type Module string

const (
	ModuleTasks     Module = "tasks"
	ModuleIncidents Module = "incidents"
)

// TaskFilter applies only to task events. Empty slices deliberately mean no
// filter, so a caller can subscribe to all task mutations without inventing a
// sentinel status/type/importance value.
type TaskFilter struct {
	Statuses    []string `yaml:"statuses,omitempty" json:"statuses,omitempty"`
	Types       []string `yaml:"types,omitempty" json:"types,omitempty"`
	Importances []string `yaml:"importances,omitempty" json:"importances,omitempty"`
}

// IncidentFilter applies only to Incident events.
type IncidentFilter struct {
	Statuses   []string `yaml:"statuses,omitempty" json:"statuses,omitempty"`
	Severities []string `yaml:"severities,omitempty" json:"severities,omitempty"`
	Kinds      []string `yaml:"kinds,omitempty" json:"kinds,omitempty"`
}

type InitializeInput struct {
	SubscriptionID  string
	IdempotencyKey  string
	ExpectedVersion *uint64
	Mode            Mode
	Modules         []Module
	Tasks           TaskFilter
	Incidents       IncidentFilter
}

// Subscription is the safe public record. Cursor secret/high-water and
// idempotency receipts stay durable but are intentionally not returned through
// MCP or HTTP.
type Subscription struct {
	ProjectID string         `yaml:"project_id" json:"projectId"`
	Actor     string         `yaml:"actor" json:"actor"`
	ID        string         `yaml:"id" json:"id"`
	Version   uint64         `yaml:"version" json:"version"`
	Mode      Mode           `yaml:"mode" json:"mode"`
	Modules   []Module       `yaml:"modules" json:"modules"`
	Tasks     TaskFilter     `yaml:"task_filters,omitempty" json:"taskFilters,omitempty"`
	Incidents IncidentFilter `yaml:"incident_filters,omitempty" json:"incidentFilters,omitempty"`
	CreatedAt string         `yaml:"created_at" json:"createdAt"`
	UpdatedAt string         `yaml:"updated_at" json:"updatedAt"`

	CursorSecret    string               `yaml:"cursor_secret" json:"-"`
	CursorFloor     uint64               `yaml:"cursor_floor,omitempty" json:"-"`
	CursorHighWater uint64               `yaml:"cursor_high_water,omitempty" json:"-"`
	Receipts        []idempotencyReceipt `yaml:"receipts,omitempty" json:"-"`
}

type idempotencyReceipt struct {
	Key  string `yaml:"key"`
	Hash string `yaml:"hash"`
}

type Delivery struct {
	RequestedMode     Mode   `json:"requestedMode"`
	EffectiveDelivery string `json:"effectiveDelivery"`
	PushSupported     bool   `json:"pushSupported"`
	PushReason        string `json:"pushReason,omitempty"`
}

type InitializeResult struct {
	Subscription Subscription `json:"subscription"`
	Cursor       string       `json:"cursor"`
	Delivery     Delivery     `json:"delivery"`
}

// Event contains only safe routing/summary metadata. In particular it excludes
// task bodies, Incident bodies, Work Log text, and Incident reply text.
type Event struct {
	ProjectID    string `yaml:"project_id" json:"projectId"`
	Seq          uint64 `yaml:"seq" json:"seq"`
	ID           string `yaml:"id" json:"id"`
	OccurredAt   string `yaml:"occurred_at" json:"occurredAt"`
	Module       Module `yaml:"module" json:"module"`
	Kind         string `yaml:"kind" json:"kind"`
	ResourceID   string `yaml:"resource_id" json:"resourceId"`
	Actor        string `yaml:"actor" json:"actor"`
	Status       string `yaml:"status,omitempty" json:"status,omitempty"`
	Type         string `yaml:"type,omitempty" json:"type,omitempty"`
	Importance   string `yaml:"importance,omitempty" json:"importance,omitempty"`
	Severity     string `yaml:"severity,omitempty" json:"severity,omitempty"`
	IncidentKind string `yaml:"incident_kind,omitempty" json:"incidentKind,omitempty"`
}

type EventInput struct {
	ProjectID    string
	OccurredAt   time.Time
	Module       Module
	Kind         string
	ResourceID   string
	Actor        string
	Status       string
	Type         string
	Importance   string
	Severity     string
	IncidentKind string
}

// SourceRef is deliberately narrow: it is a recovery proof for one task or
// Incident mutation, not a general purpose outbox. A pending record is visible
// only after this exact source record can be verified after a crash.
type SourceRef struct {
	Kind              SourceKind `yaml:"kind" json:"-"`
	ResourceID        string     `yaml:"resource_id" json:"-"`
	MutationID        string     `yaml:"mutation_id" json:"-"`
	ExpectedStatus    string     `yaml:"expected_status,omitempty" json:"-"`
	ExpectedUpdatedAt string     `yaml:"expected_updated_at,omitempty" json:"-"`
}

type SourceKind string

const (
	SourceTaskProvenance SourceKind = "task_provenance"
	SourceIncident       SourceKind = "incident"
	SourceIncidentReply  SourceKind = "incident_reply"
	SourceIncidentStatus SourceKind = "incident_status"
)

// PreparedEvent is written before the source mutation. If the process stops
// between source persistence and ledger persistence, Recover verifies Source
// and publishes it once; if the source never landed, Recover drops it silently.
type PreparedEvent struct {
	Event  Event     `yaml:"event" json:"-"`
	Source SourceRef `yaml:"source" json:"-"`
}

type SourceVerifier func(SourceRef) (bool, error)

type PollInput struct {
	SubscriptionID string
	Cursor         string
	Limit          int
	Wait           time.Duration
}

type PollResult struct {
	Events   []Event  `json:"events"`
	Cursor   string   `json:"cursor"`
	Delivery Delivery `json:"delivery"`
}

type Capability struct {
	EffectiveDelivery string `json:"effectiveDelivery"`
	PushSupported     bool   `json:"pushSupported"`
	PushReason        string `json:"pushReason"`
}

type subscriptionState struct {
	Version       int            `yaml:"version"`
	ProjectID     string         `yaml:"project_id"`
	Subscriptions []Subscription `yaml:"subscriptions"`
}

type ledgerState struct {
	Version   int     `yaml:"version"`
	ProjectID string  `yaml:"project_id"`
	BaseSeq   uint64  `yaml:"base_seq,omitempty"`
	NextSeq   uint64  `yaml:"next_seq"`
	Events    []Event `yaml:"events"`
}

// pendingRecord is deliberately one file per prepared mutation. A backlog of
// undelivered notifications must never hit the ledger's bounded retention
// limit and begin rejecting unrelated task or Incident source writes. The
// record is still small and individually validated; a real disk failure is
// surfaced before the source mutation so recovery never has to guess.
type pendingRecord struct {
	Version   int           `yaml:"version"`
	ProjectID string        `yaml:"project_id"`
	Prepared  PreparedEvent `yaml:"prepared"`
}

// legacyPendingState is read-only migration support for the short-lived v1
// single-file pending shape written by earlier development builds. New writes
// always use pendingRecord files and Recovery removes the old projection after
// it has verified each source.
type legacyPendingState struct {
	Version   int             `yaml:"version"`
	ProjectID string          `yaml:"project_id"`
	Pending   []PreparedEvent `yaml:"pending"`
}

type pendingItem struct {
	Prepared PreparedEvent
	FileName string
}

type cursorPayload struct {
	Version        int    `json:"v"`
	ProjectID      string `json:"p"`
	Actor          string `json:"a"`
	SubscriptionID string `json:"s"`
	Seq            uint64 `json:"q"`
}

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
	if m == nil || m.Now == nil {
		return time.Now().UTC()
	}
	return m.Now().UTC()
}

func CapabilitySnapshot() Capability {
	return Capability{
		EffectiveDelivery: "poll",
		PushSupported:     false,
		PushReason:        "current MCP session has no verified resource-subscription delivery; durable events_poll is authoritative",
	}
}

func delivery(mode Mode) Delivery {
	capability := CapabilitySnapshot()
	return Delivery{RequestedMode: mode, EffectiveDelivery: capability.EffectiveDelivery, PushSupported: capability.PushSupported, PushReason: capability.PushReason}
}

func (m *Manager) Initialize(ctx context.Context, projectID, actor string, input InitializeInput) (InitializeResult, error) {
	if m == nil || m.Store == nil {
		return InitializeResult{}, errors.New("event subscription manager has no store")
	}
	input, fingerprint, err := normalizeInitialize(input)
	if err != nil {
		return InitializeResult{}, err
	}
	if err := validateProjectID(projectID); err != nil {
		return InitializeResult{}, err
	}
	if err := validateActor(actor); err != nil {
		return InitializeResult{}, err
	}
	var out Subscription
	var cursor string
	err = m.Store.Write(ctx, actor, "initialize event subscription", func(tx *store.WriteTx) error {
		state, err := readSubscriptionsTx(tx, projectID)
		if err != nil {
			return err
		}
		ledger, err := readLedgerTx(tx, projectID)
		if err != nil {
			return err
		}
		for index := range state.Subscriptions {
			current := &state.Subscriptions[index]
			if current.Actor != actor || current.ID != input.SubscriptionID {
				continue
			}
			if receipt, found := findReceipt(current.Receipts, input.IdempotencyKey); found {
				if receipt.Hash != fingerprint {
					return ErrIdempotencyConflict
				}
				out = cloneSubscription(*current)
				cursor, err = makeCursor(*current, current.CursorHighWater)
				return err
			}
			if input.ExpectedVersion == nil {
				return ErrExpectedVersion
			}
			if *input.ExpectedVersion != current.Version {
				return ErrVersionConflict
			}
			current.Version++
			current.Mode, current.Modules, current.Tasks, current.Incidents = input.Mode, slices.Clone(input.Modules), cloneTaskFilter(input.Tasks), cloneIncidentFilter(input.Incidents)
			current.UpdatedAt = m.now().Format(time.RFC3339Nano)
			// A deliberate update with a new key is also the explicit resync path
			// after a slow cursor expires. It begins at the current ledger tail;
			// it never silently replays an arbitrarily old filtered history.
			current.CursorFloor, current.CursorHighWater = ledger.BaseSeq, ledger.NextSeq
			current.Receipts = appendReceipt(current.Receipts, idempotencyReceipt{Key: input.IdempotencyKey, Hash: fingerprint})
			if err := validateSubscription(*current); err != nil {
				return err
			}
			if err := writeSubscriptionsTx(tx, state); err != nil {
				return err
			}
			out = cloneSubscription(*current)
			cursor, err = makeCursor(*current, current.CursorHighWater)
			return err
		}

		if input.ExpectedVersion != nil {
			return fmt.Errorf("%w: expectedVersion is only valid for an existing subscription", ErrInvalidSubscription)
		}
		if len(state.Subscriptions) >= maxSubscriptions {
			return fmt.Errorf("%w: too many subscriptions", ErrInvalidSubscription)
		}
		secret, err := mintSecret()
		if err != nil {
			return err
		}
		now := m.now().Format(time.RFC3339Nano)
		created := Subscription{
			ProjectID: projectID, Actor: actor, ID: input.SubscriptionID, Version: 1,
			Mode: input.Mode, Modules: slices.Clone(input.Modules), Tasks: cloneTaskFilter(input.Tasks), Incidents: cloneIncidentFilter(input.Incidents),
			CreatedAt: now, UpdatedAt: now, CursorSecret: secret, CursorFloor: ledger.BaseSeq, CursorHighWater: ledger.NextSeq,
			Receipts: []idempotencyReceipt{{Key: input.IdempotencyKey, Hash: fingerprint}},
		}
		if err := validateSubscription(created); err != nil {
			return err
		}
		state.Subscriptions = append(state.Subscriptions, created)
		if err := writeSubscriptionsTx(tx, state); err != nil {
			return err
		}
		out = cloneSubscription(created)
		cursor, err = makeCursor(created, ledger.NextSeq)
		return err
	})
	if err != nil {
		return InitializeResult{}, err
	}
	return InitializeResult{Subscription: out, Cursor: cursor, Delivery: delivery(out.Mode)}, nil
}

func (m *Manager) List(projectID string) ([]Subscription, error) {
	if m == nil || m.Store == nil {
		return nil, errors.New("event subscription manager has no store")
	}
	if err := validateProjectID(projectID); err != nil {
		return nil, err
	}
	state, err := readSubscriptions(m.Store, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]Subscription, len(state.Subscriptions))
	for i, item := range state.Subscriptions {
		out[i] = cloneSubscription(item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Actor == out[j].Actor {
			return out[i].ID < out[j].ID
		}
		return out[i].Actor < out[j].Actor
	})
	return out, nil
}

// PrepareTx records a single, narrowly-scoped delivery intent before the task
// or Incident source is written. CommitTx must run in the same Store.Write
// callback after the source write. This is not a generic outbox: its only job
// is making the project ledger recoverable across a multi-file crash boundary.
func (m *Manager) PrepareTx(tx *store.WriteTx, input EventInput, source SourceRef) (PreparedEvent, error) {
	if m == nil || m.Store == nil || tx == nil {
		return PreparedEvent{}, errors.New("event subscription manager has no store transaction")
	}
	if err := validateEventInput(input); err != nil {
		return PreparedEvent{}, err
	}
	if err := validateSourceRef(source); err != nil {
		return PreparedEvent{}, err
	}
	// New subscriptions begin at the current ledger tail, so retaining events
	// before any current recipient is both wasteful and misleading. The same is
	// true when no existing subscription's filters can receive this event.
	subscriptions, err := readSubscriptionsTx(tx, input.ProjectID)
	if err != nil {
		return PreparedEvent{}, err
	}
	probe := Event{ProjectID: input.ProjectID, OccurredAt: input.OccurredAt.UTC().Format(time.RFC3339Nano), Module: input.Module, Kind: input.Kind, ResourceID: input.ResourceID, Actor: input.Actor, Status: input.Status, Type: input.Type, Importance: input.Importance, Severity: input.Severity, IncidentKind: input.IncidentKind}
	interested := false
	for _, item := range subscriptions.Subscriptions {
		if matches(item, probe) {
			interested = true
			break
		}
	}
	if !interested {
		return PreparedEvent{}, nil
	}
	id, err := mintEventID()
	if err != nil {
		return PreparedEvent{}, err
	}
	prepared := PreparedEvent{
		Event: Event{
			ProjectID: input.ProjectID, ID: id, OccurredAt: input.OccurredAt.UTC().Format(time.RFC3339Nano),
			Module: input.Module, Kind: input.Kind, ResourceID: input.ResourceID, Actor: input.Actor,
			Status: input.Status, Type: input.Type, Importance: input.Importance, Severity: input.Severity, IncidentKind: input.IncidentKind,
		},
		Source: source,
	}
	if err := writePendingRecordTx(tx, prepared); err != nil {
		return PreparedEvent{}, err
	}
	return clonePrepared(prepared), nil
}

// CommitTx publishes a prepared event after its source has been written. An
// interruption after ledger persistence but before pending-record cleanup is
// safe: Recover de-duplicates by the durable Event.ID and only removes the
// already-committed intent.
func (m *Manager) CommitTx(tx *store.WriteTx, prepared PreparedEvent) (Event, error) {
	if m == nil || m.Store == nil || tx == nil {
		return Event{}, errors.New("event subscription manager has no store transaction")
	}
	if prepared.Event.ID == "" {
		return Event{}, nil
	}
	if err := validatePrepared(prepared); err != nil {
		return Event{}, err
	}
	if _, err := readPendingRecordTx(tx, prepared.Event.ProjectID, prepared.Event.ID); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Event{}, fmt.Errorf("%w: prepared event", ErrInvalidSubscription)
		}
		return Event{}, err
	}
	ledger, err := readLedgerTx(tx, prepared.Event.ProjectID)
	if err != nil {
		return Event{}, err
	}
	subscriptions, err := readSubscriptionsTx(tx, prepared.Event.ProjectID)
	if err != nil {
		return Event{}, err
	}
	stored, changed, err := appendPreparedToLedger(&ledger, subscriptions.Subscriptions, prepared)
	if err != nil {
		return Event{}, err
	}
	if changed {
		if err := writeLedgerTx(tx, ledger); err != nil {
			return Event{}, err
		}
	}
	if err := deletePendingRecordTx(tx, prepared.Event.ProjectID, prepared.Event.ID); err != nil {
		return Event{}, err
	}
	return stored, nil
}

// Recover reconciles only prepared intents for one project. The caller supplies
// the source-specific verifier; this keeps task/Incident ownership out of the
// subscription package and makes a source-less pending event impossible to
// surface to an Agent.
func (m *Manager) Recover(ctx context.Context, projectID, actor string, verify SourceVerifier) error {
	if m == nil || m.Store == nil {
		return errors.New("event subscription manager has no store")
	}
	if err := validateProjectID(projectID); err != nil {
		return err
	}
	if err := validateActor(actor); err != nil {
		return err
	}
	if verify == nil {
		return fmt.Errorf("%w: source verifier", ErrInvalidSubscription)
	}
	return m.Store.Write(ctx, actor, "recover event ledger", func(tx *store.WriteTx) error {
		pending, legacyPending, err := readPendingRecordsTx(tx, projectID)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}
		present := make([]bool, len(pending))
		for i, item := range pending {
			if err := validatePrepared(item.Prepared); err != nil {
				return err
			}
			present[i], err = verify(item.Prepared.Source)
			if err != nil {
				return err
			}
		}
		ledger, err := readLedgerTx(tx, projectID)
		if err != nil {
			return err
		}
		subscriptions, err := readSubscriptionsTx(tx, projectID)
		if err != nil {
			return err
		}
		ledgerChanged := false
		for i, item := range pending {
			if !present[i] {
				continue
			}
			if _, changed, err := appendPreparedToLedger(&ledger, subscriptions.Subscriptions, item.Prepared); err != nil {
				return err
			} else if changed {
				ledgerChanged = true
			}
		}
		if ledgerChanged {
			if err := writeLedgerTx(tx, ledger); err != nil {
				return err
			}
		}
		// Every item was now either confirmed and published or confirmed absent;
		// neither should survive to a later poll. Per-event removal deliberately
		// cannot rewrite or lose a concurrently prepared marker.
		for _, item := range pending {
			if item.FileName == "" {
				continue
			}
			if err := tx.DeleteData(pendingDir, item.FileName); err != nil {
				return err
			}
		}
		if legacyPending {
			return tx.DeleteData(pendingDir, projectFileName(projectID))
		}
		return nil
	})
}

func (m *Manager) Poll(ctx context.Context, projectID, actor string, input PollInput) (PollResult, error) {
	if m == nil || m.Store == nil {
		return PollResult{}, errors.New("event subscription manager has no store")
	}
	if err := validateProjectID(projectID); err != nil {
		return PollResult{}, err
	}
	if err := validateActor(actor); err != nil {
		return PollResult{}, err
	}
	if err := validatePollInput(input); err != nil {
		return PollResult{}, err
	}
	input = m.withPollDefaults(input)
	deadline := time.NewTimer(input.Wait)
	defer deadline.Stop()
	for {
		out, found, err := m.pollOnce(ctx, projectID, actor, input)
		if err != nil {
			return PollResult{}, err
		}
		if found || input.Wait == 0 {
			return out, nil
		}
		ticker := time.NewTimer(pollRetryInterval)
		select {
		case <-ctx.Done():
			ticker.Stop()
			return PollResult{}, ctx.Err()
		case <-deadline.C:
			ticker.Stop()
			return out, nil
		case <-ticker.C:
		}
	}
}

func (m *Manager) pollOnce(ctx context.Context, projectID, actor string, input PollInput) (PollResult, bool, error) {
	var out PollResult
	var found bool
	err := m.Store.Write(ctx, actor, "poll event subscription", func(tx *store.WriteTx) error {
		state, err := readSubscriptionsTx(tx, projectID)
		if err != nil {
			return err
		}
		ledger, err := readLedgerTx(tx, projectID)
		if err != nil {
			return err
		}
		index := findSubscriptionIndex(state.Subscriptions, actor, input.SubscriptionID)
		if index < 0 {
			return ErrSubscriptionNotFound
		}
		current := &state.Subscriptions[index]
		if current.CursorHighWater < ledger.BaseSeq {
			return ErrCursorExpired
		}
		// CursorHighWater is an acknowledged client cursor, not an optimistic
		// delivery cursor. events_poll may be interrupted after it writes a
		// response but before the Agent persists that response, so we only move
		// the durable high-water mark when the next request explicitly returns a
		// signed cursor. This deliberately permits safe redelivery after a client
		// crash and keeps retention compaction tied to confirmed consumption.
		after := current.CursorHighWater
		acknowledged := false
		if input.Cursor != "" {
			seq, err := parseCursor(input.Cursor, *current, projectID, actor)
			if err != nil {
				return err
			}
			if seq < ledger.BaseSeq {
				return ErrCursorExpired
			}
			if seq < current.CursorHighWater {
				return ErrCursorRegression
			}
			if seq > ledger.NextSeq {
				return ErrInvalidCursor
			}
			after = seq
			acknowledged = seq > current.CursorHighWater
		}
		next := after
		events := make([]Event, 0, input.Limit)
		for _, event := range ledger.Events {
			if event.Seq <= after {
				continue
			}
			next = event.Seq
			if !matches(*current, event) {
				continue
			}
			events = append(events, cloneEvent(event))
			if len(events) >= input.Limit {
				break
			}
		}
		if acknowledged {
			current.CursorHighWater = after
			if err := writeSubscriptionsTx(tx, state); err != nil {
				return err
			}
		}
		cursor, err := makeCursor(*current, next)
		if err != nil {
			return err
		}
		out = PollResult{Events: events, Cursor: cursor, Delivery: delivery(current.Mode)}
		found = len(events) != 0
		return nil
	})
	return out, found, err
}

func normalizeInitialize(input InitializeInput) (InitializeInput, string, error) {
	input.SubscriptionID = strings.TrimSpace(input.SubscriptionID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if err := validateID(input.SubscriptionID, "subscription id"); err != nil {
		return InitializeInput{}, "", err
	}
	if err := validateID(input.IdempotencyKey, "idempotency key"); err != nil {
		return InitializeInput{}, "", err
	}
	if input.Mode != ModePassive && input.Mode != ModeMixed && input.Mode != ModeActive {
		return InitializeInput{}, "", fmt.Errorf("%w: mode", ErrInvalidSubscription)
	}
	modules, err := normalizeModules(input.Modules)
	if err != nil {
		return InitializeInput{}, "", err
	}
	input.Modules = modules
	if input.Tasks, err = normalizeTaskFilter(input.Tasks); err != nil {
		return InitializeInput{}, "", err
	}
	if input.Incidents, err = normalizeIncidentFilter(input.Incidents); err != nil {
		return InitializeInput{}, "", err
	}
	type fingerprintInput struct {
		SubscriptionID  string         `json:"subscriptionId"`
		ExpectedVersion *uint64        `json:"expectedVersion,omitempty"`
		Mode            Mode           `json:"mode"`
		Modules         []Module       `json:"modules"`
		Tasks           TaskFilter     `json:"tasks"`
		Incidents       IncidentFilter `json:"incidents"`
	}
	encoded, err := json.Marshal(fingerprintInput{SubscriptionID: input.SubscriptionID, ExpectedVersion: input.ExpectedVersion, Mode: input.Mode, Modules: input.Modules, Tasks: input.Tasks, Incidents: input.Incidents})
	if err != nil {
		return InitializeInput{}, "", fmt.Errorf("%w: request fingerprint", ErrInvalidSubscription)
	}
	digest := sha256.Sum256(encoded)
	return input, hex.EncodeToString(digest[:]), nil
}

func normalizeModules(items []Module) ([]Module, error) {
	if len(items) == 0 || len(items) > 2 {
		return nil, fmt.Errorf("%w: modules", ErrInvalidSubscription)
	}
	seen := map[Module]struct{}{}
	out := make([]Module, 0, len(items))
	for _, item := range items {
		if item != ModuleTasks && item != ModuleIncidents {
			return nil, fmt.Errorf("%w: module", ErrInvalidSubscription)
		}
		if _, found := seen[item]; found {
			return nil, fmt.Errorf("%w: duplicate module", ErrInvalidSubscription)
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func normalizeTaskFilter(value TaskFilter) (TaskFilter, error) {
	var err error
	if value.Statuses, err = normalizeTokens(value.Statuses); err != nil {
		return TaskFilter{}, err
	}
	if value.Types, err = normalizeTokens(value.Types); err != nil {
		return TaskFilter{}, err
	}
	if value.Importances, err = normalizeTokens(value.Importances); err != nil {
		return TaskFilter{}, err
	}
	return value, nil
}

func normalizeIncidentFilter(value IncidentFilter) (IncidentFilter, error) {
	var err error
	if value.Statuses, err = normalizeTokens(value.Statuses); err != nil {
		return IncidentFilter{}, err
	}
	if value.Severities, err = normalizeTokens(value.Severities); err != nil {
		return IncidentFilter{}, err
	}
	if value.Kinds, err = normalizeTokens(value.Kinds); err != nil {
		return IncidentFilter{}, err
	}
	return value, nil
}

func normalizeTokens(items []string) ([]string, error) {
	if len(items) > maxFilters {
		return nil, fmt.Errorf("%w: too many filters", ErrInvalidSubscription)
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if err := validateToken(item); err != nil {
			return nil, err
		}
		if _, found := seen[item]; found {
			return nil, fmt.Errorf("%w: duplicate filter", ErrInvalidSubscription)
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out, nil
}

func validatePollInput(input PollInput) error {
	if err := validateID(strings.TrimSpace(input.SubscriptionID), "subscription id"); err != nil {
		return err
	}
	if input.Limit < 0 || input.Limit > maxPollLimit {
		return fmt.Errorf("%w: limit must be between 0 and %d", ErrInvalidSubscription, maxPollLimit)
	}
	if input.Wait < 0 || input.Wait > maxCursorWait {
		return fmt.Errorf("%w: waitMs must be between 0 and %d", ErrInvalidSubscription, maxCursorWait.Milliseconds())
	}
	return nil
}

func (m *Manager) withPollDefaults(input PollInput) PollInput {
	if input.Limit == 0 {
		input.Limit = defaultPollLimit
	}
	return input
}

func validateEventInput(input EventInput) error {
	if err := validateProjectID(input.ProjectID); err != nil {
		return err
	}
	if input.Module != ModuleTasks && input.Module != ModuleIncidents {
		return fmt.Errorf("%w: event module", ErrInvalidSubscription)
	}
	if err := validateToken(input.Kind); err != nil {
		return err
	}
	if err := validateID(input.ResourceID, "resource id"); err != nil {
		return err
	}
	if err := validateActor(input.Actor); err != nil {
		return err
	}
	if input.OccurredAt.IsZero() {
		return fmt.Errorf("%w: event time", ErrInvalidSubscription)
	}
	for _, value := range []string{input.Status, input.Type, input.Importance, input.Severity, input.IncidentKind} {
		if value != "" {
			if err := validateToken(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func matches(subscription Subscription, event Event) bool {
	if !slices.Contains(subscription.Modules, event.Module) {
		return false
	}
	switch event.Module {
	case ModuleTasks:
		return matchesStringFilter(subscription.Tasks.Statuses, event.Status) && matchesStringFilter(subscription.Tasks.Types, event.Type) && matchesStringFilter(subscription.Tasks.Importances, event.Importance)
	case ModuleIncidents:
		return matchesStringFilter(subscription.Incidents.Statuses, event.Status) && matchesStringFilter(subscription.Incidents.Severities, event.Severity) && matchesStringFilter(subscription.Incidents.Kinds, event.IncidentKind)
	default:
		return false
	}
}

func matchesStringFilter(items []string, value string) bool {
	return len(items) == 0 || slices.Contains(items, value)
}

func readSubscriptions(s *store.Store, projectID string) (subscriptionState, error) {
	data, err := s.ReadData(subscriptionDir, projectFileName(projectID))
	if errors.Is(err, os.ErrNotExist) {
		return subscriptionState{Version: stateVersion, ProjectID: projectID}, nil
	}
	if err != nil {
		return subscriptionState{}, err
	}
	return decodeSubscriptions(projectID, data)
}

func readSubscriptionsTx(tx *store.WriteTx, projectID string) (subscriptionState, error) {
	data, err := tx.ReadData(subscriptionDir, projectFileName(projectID))
	if errors.Is(err, os.ErrNotExist) {
		return subscriptionState{Version: stateVersion, ProjectID: projectID}, nil
	}
	if err != nil {
		return subscriptionState{}, err
	}
	return decodeSubscriptions(projectID, data)
}

func writeSubscriptionsTx(tx *store.WriteTx, value subscriptionState) error {
	if err := validateSubscriptionState(value); err != nil {
		return err
	}
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: encode subscriptions", ErrInvalidSubscription)
	}
	return tx.WriteData(subscriptionDir, projectFileName(value.ProjectID), encoded)
}

func readLedgerTx(tx *store.WriteTx, projectID string) (ledgerState, error) {
	data, err := tx.ReadData(ledgerDir, projectFileName(projectID))
	if errors.Is(err, os.ErrNotExist) {
		return ledgerState{Version: stateVersion, ProjectID: projectID}, nil
	}
	if err != nil {
		return ledgerState{}, err
	}
	return decodeLedger(projectID, data)
}

func pendingFileName(projectID, eventID string) string {
	return projectID + "--" + eventID + ".yaml"
}

func readPendingRecordTx(tx *store.WriteTx, projectID, eventID string) (pendingRecord, error) {
	data, err := tx.ReadData(pendingDir, pendingFileName(projectID, eventID))
	if err != nil {
		return pendingRecord{}, err
	}
	return decodePendingRecord(projectID, data)
}

// readPendingRecordsTx enumerates only the current project's marker files.
// The legacy single-file record is retained as a read-only migration input and
// is deleted by Recover after its entries were either verified or discarded.
func readPendingRecordsTx(tx *store.WriteTx, projectID string) ([]pendingItem, bool, error) {
	names, err := tx.ListData(pendingDir)
	if err != nil {
		return nil, false, err
	}
	prefix := projectID + "--"
	items := make([]pendingItem, 0)
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		data, err := tx.ReadData(pendingDir, name)
		if err != nil {
			return nil, false, err
		}
		record, err := decodePendingRecord(projectID, data)
		if err != nil {
			return nil, false, err
		}
		if name != pendingFileName(projectID, record.Prepared.Event.ID) {
			return nil, false, fmt.Errorf("%w: pending event filename", ErrInvalidSubscription)
		}
		items = append(items, pendingItem{Prepared: record.Prepared, FileName: name})
	}
	legacyData, err := tx.ReadData(pendingDir, projectFileName(projectID))
	if errors.Is(err, os.ErrNotExist) {
		return items, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	legacy, err := decodeLegacyPending(projectID, legacyData)
	if err != nil {
		return nil, false, err
	}
	for _, item := range legacy.Pending {
		items = append(items, pendingItem{Prepared: item})
	}
	return items, true, nil
}

func writeLedgerTx(tx *store.WriteTx, value ledgerState) error {
	if err := validateLedgerState(value); err != nil {
		return err
	}
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: encode event ledger", ErrInvalidSubscription)
	}
	return tx.WriteData(ledgerDir, projectFileName(value.ProjectID), encoded)
}

func writePendingRecordTx(tx *store.WriteTx, value PreparedEvent) error {
	record := pendingRecord{Version: stateVersion, ProjectID: value.Event.ProjectID, Prepared: value}
	if err := validatePendingRecord(record); err != nil {
		return err
	}
	encoded, err := yaml.Marshal(record)
	if err != nil {
		return fmt.Errorf("%w: encode pending ledger event", ErrInvalidSubscription)
	}
	return tx.WriteData(pendingDir, pendingFileName(record.ProjectID, record.Prepared.Event.ID), encoded)
}

func deletePendingRecordTx(tx *store.WriteTx, projectID, eventID string) error {
	return tx.DeleteData(pendingDir, pendingFileName(projectID, eventID))
}

func decodeSubscriptions(projectID string, data []byte) (subscriptionState, error) {
	if len(data) == 0 || len(data) > maxStateBytes || !utf8.Valid(data) {
		return subscriptionState{}, fmt.Errorf("%w: subscription state bytes", ErrInvalidSubscription)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var value subscriptionState
	if err := decoder.Decode(&value); err != nil {
		return subscriptionState{}, fmt.Errorf("%w: subscription state parse: %v", ErrInvalidSubscription, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return subscriptionState{}, fmt.Errorf("%w: subscription state multiple documents", ErrInvalidSubscription)
		}
		return subscriptionState{}, fmt.Errorf("%w: subscription state parse: %v", ErrInvalidSubscription, err)
	}
	if value.ProjectID != projectID {
		return subscriptionState{}, fmt.Errorf("%w: subscription state project id", ErrInvalidSubscription)
	}
	if err := validateSubscriptionState(value); err != nil {
		return subscriptionState{}, err
	}
	return value, nil
}

func decodeLedger(projectID string, data []byte) (ledgerState, error) {
	if len(data) == 0 || len(data) > maxLedgerStateBytes || !utf8.Valid(data) {
		return ledgerState{}, fmt.Errorf("%w: ledger bytes", ErrInvalidSubscription)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var value ledgerState
	if err := decoder.Decode(&value); err != nil {
		return ledgerState{}, fmt.Errorf("%w: ledger parse: %v", ErrInvalidSubscription, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return ledgerState{}, fmt.Errorf("%w: ledger multiple documents", ErrInvalidSubscription)
		}
		return ledgerState{}, fmt.Errorf("%w: ledger parse: %v", ErrInvalidSubscription, err)
	}
	if value.ProjectID != projectID {
		return ledgerState{}, fmt.Errorf("%w: ledger project id", ErrInvalidSubscription)
	}
	if err := validateLedgerState(value); err != nil {
		return ledgerState{}, err
	}
	return value, nil
}

func decodePendingRecord(projectID string, data []byte) (pendingRecord, error) {
	if len(data) == 0 || len(data) > maxStateBytes || !utf8.Valid(data) {
		return pendingRecord{}, fmt.Errorf("%w: pending ledger bytes", ErrInvalidSubscription)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var value pendingRecord
	if err := decoder.Decode(&value); err != nil {
		return pendingRecord{}, fmt.Errorf("%w: pending ledger parse: %v", ErrInvalidSubscription, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return pendingRecord{}, fmt.Errorf("%w: pending ledger multiple documents", ErrInvalidSubscription)
		}
		return pendingRecord{}, fmt.Errorf("%w: pending ledger parse: %v", ErrInvalidSubscription, err)
	}
	if value.ProjectID != projectID {
		return pendingRecord{}, fmt.Errorf("%w: pending ledger project id", ErrInvalidSubscription)
	}
	if err := validatePendingRecord(value); err != nil {
		return pendingRecord{}, err
	}
	return value, nil
}

func decodeLegacyPending(projectID string, data []byte) (legacyPendingState, error) {
	if len(data) == 0 || len(data) > maxStateBytes || !utf8.Valid(data) {
		return legacyPendingState{}, fmt.Errorf("%w: pending ledger bytes", ErrInvalidSubscription)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var value legacyPendingState
	if err := decoder.Decode(&value); err != nil {
		return legacyPendingState{}, fmt.Errorf("%w: pending ledger parse: %v", ErrInvalidSubscription, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return legacyPendingState{}, fmt.Errorf("%w: pending ledger multiple documents", ErrInvalidSubscription)
		}
		return legacyPendingState{}, fmt.Errorf("%w: pending ledger parse: %v", ErrInvalidSubscription, err)
	}
	if value.ProjectID != projectID {
		return legacyPendingState{}, fmt.Errorf("%w: pending ledger project id", ErrInvalidSubscription)
	}
	if err := validateLegacyPending(value); err != nil {
		return legacyPendingState{}, err
	}
	return value, nil
}

func validateSubscriptionState(value subscriptionState) error {
	if value.Version != stateVersion || validateProjectID(value.ProjectID) != nil || len(value.Subscriptions) > maxSubscriptions {
		return fmt.Errorf("%w: subscription state", ErrInvalidSubscription)
	}
	seen := map[string]struct{}{}
	for _, item := range value.Subscriptions {
		if err := validateSubscription(item); err != nil {
			return err
		}
		if item.ProjectID != value.ProjectID {
			return fmt.Errorf("%w: subscription project", ErrInvalidSubscription)
		}
		key := item.Actor + "\x00" + item.ID
		if _, found := seen[key]; found {
			return fmt.Errorf("%w: duplicate subscription", ErrInvalidSubscription)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateSubscription(value Subscription) error {
	if err := validateProjectID(value.ProjectID); err != nil {
		return err
	}
	if err := validateActor(value.Actor); err != nil {
		return err
	}
	if err := validateID(value.ID, "subscription id"); err != nil || value.Version == 0 {
		return fmt.Errorf("%w: subscription identity", ErrInvalidSubscription)
	}
	if value.Mode != ModePassive && value.Mode != ModeMixed && value.Mode != ModeActive {
		return fmt.Errorf("%w: subscription mode", ErrInvalidSubscription)
	}
	if _, err := normalizeModules(value.Modules); err != nil {
		return err
	}
	if _, err := normalizeTaskFilter(value.Tasks); err != nil {
		return err
	}
	if _, err := normalizeIncidentFilter(value.Incidents); err != nil {
		return err
	}
	if len(value.CursorSecret) != 64 {
		return fmt.Errorf("%w: cursor secret", ErrInvalidSubscription)
	}
	if _, err := hex.DecodeString(value.CursorSecret); err != nil {
		return fmt.Errorf("%w: cursor secret", ErrInvalidSubscription)
	}
	if value.CursorFloor > value.CursorHighWater {
		return fmt.Errorf("%w: cursor range", ErrInvalidSubscription)
	}
	if _, err := time.Parse(time.RFC3339Nano, value.CreatedAt); err != nil {
		return fmt.Errorf("%w: subscription created at", ErrInvalidSubscription)
	}
	if _, err := time.Parse(time.RFC3339Nano, value.UpdatedAt); err != nil {
		return fmt.Errorf("%w: subscription updated at", ErrInvalidSubscription)
	}
	if len(value.Receipts) == 0 || len(value.Receipts) > maxReceipts {
		return fmt.Errorf("%w: idempotency receipts", ErrInvalidSubscription)
	}
	seen := map[string]struct{}{}
	for _, receipt := range value.Receipts {
		if err := validateID(receipt.Key, "idempotency key"); err != nil || len(receipt.Hash) != 64 {
			return fmt.Errorf("%w: idempotency receipt", ErrInvalidSubscription)
		}
		if _, err := hex.DecodeString(receipt.Hash); err != nil {
			return fmt.Errorf("%w: idempotency receipt", ErrInvalidSubscription)
		}
		if _, found := seen[receipt.Key]; found {
			return fmt.Errorf("%w: duplicate idempotency key", ErrInvalidSubscription)
		}
		seen[receipt.Key] = struct{}{}
	}
	return nil
}

func validateLedgerState(value ledgerState) error {
	if value.Version != stateVersion || validateProjectID(value.ProjectID) != nil || len(value.Events) > maxLedgerEvents {
		return fmt.Errorf("%w: ledger state", ErrInvalidSubscription)
	}
	if value.BaseSeq > value.NextSeq {
		return fmt.Errorf("%w: ledger base sequence", ErrInvalidSubscription)
	}
	last := value.BaseSeq
	seen := make(map[string]struct{}, len(value.Events))
	for _, event := range value.Events {
		if err := validateEvent(event); err != nil {
			return err
		}
		if event.ProjectID != value.ProjectID || event.Seq != last+1 {
			return fmt.Errorf("%w: ledger sequence", ErrInvalidSubscription)
		}
		if _, found := seen[event.ID]; found {
			return fmt.Errorf("%w: duplicate ledger event", ErrInvalidSubscription)
		}
		seen[event.ID] = struct{}{}
		last = event.Seq
	}
	if value.NextSeq != last {
		return fmt.Errorf("%w: ledger next sequence", ErrInvalidSubscription)
	}
	return nil
}

func validatePendingRecord(value pendingRecord) error {
	if value.Version != stateVersion || validateProjectID(value.ProjectID) != nil {
		return fmt.Errorf("%w: pending ledger state", ErrInvalidSubscription)
	}
	if err := validatePrepared(value.Prepared); err != nil {
		return err
	}
	if value.Prepared.Event.ProjectID != value.ProjectID || value.Prepared.Event.Seq != 0 {
		return fmt.Errorf("%w: pending ledger project", ErrInvalidSubscription)
	}
	return nil
}

func validateLegacyPending(value legacyPendingState) error {
	if value.Version != stateVersion || validateProjectID(value.ProjectID) != nil || len(value.Pending) > maxLedgerEvents {
		return fmt.Errorf("%w: legacy pending ledger state", ErrInvalidSubscription)
	}
	seen := make(map[string]struct{}, len(value.Pending))
	for _, item := range value.Pending {
		if err := validatePrepared(item); err != nil {
			return err
		}
		if item.Event.ProjectID != value.ProjectID || item.Event.Seq != 0 {
			return fmt.Errorf("%w: legacy pending ledger project", ErrInvalidSubscription)
		}
		if _, found := seen[item.Event.ID]; found {
			return fmt.Errorf("%w: duplicate legacy pending ledger event", ErrInvalidSubscription)
		}
		seen[item.Event.ID] = struct{}{}
	}
	return nil
}

func validateEvent(value Event) error {
	if err := validateProjectID(value.ProjectID); err != nil {
		return err
	}
	if value.Seq == 0 || !validEventID(value.ID) {
		return fmt.Errorf("%w: event id", ErrInvalidSubscription)
	}
	if _, err := time.Parse(time.RFC3339Nano, value.OccurredAt); err != nil {
		return fmt.Errorf("%w: event occurred at", ErrInvalidSubscription)
	}
	if value.Module != ModuleTasks && value.Module != ModuleIncidents {
		return fmt.Errorf("%w: event module", ErrInvalidSubscription)
	}
	if err := validateToken(value.Kind); err != nil {
		return err
	}
	if err := validateID(value.ResourceID, "resource id"); err != nil {
		return err
	}
	if err := validateActor(value.Actor); err != nil {
		return err
	}
	for _, item := range []string{value.Status, value.Type, value.Importance, value.Severity, value.IncidentKind} {
		if item != "" {
			if err := validateToken(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func validatePrepared(value PreparedEvent) error {
	if value.Event.Seq != 0 {
		return fmt.Errorf("%w: prepared event sequence", ErrInvalidSubscription)
	}
	if err := validateUnsequencedEvent(value.Event); err != nil {
		return err
	}
	return validateSourceRef(value.Source)
}

func validateUnsequencedEvent(value Event) error {
	if err := validateProjectID(value.ProjectID); err != nil {
		return err
	}
	if !validEventID(value.ID) {
		return fmt.Errorf("%w: prepared event id", ErrInvalidSubscription)
	}
	if _, err := time.Parse(time.RFC3339Nano, value.OccurredAt); err != nil {
		return fmt.Errorf("%w: event occurred at", ErrInvalidSubscription)
	}
	if value.Module != ModuleTasks && value.Module != ModuleIncidents {
		return fmt.Errorf("%w: event module", ErrInvalidSubscription)
	}
	if err := validateToken(value.Kind); err != nil {
		return err
	}
	if err := validateID(value.ResourceID, "resource id"); err != nil {
		return err
	}
	if err := validateActor(value.Actor); err != nil {
		return err
	}
	for _, item := range []string{value.Status, value.Type, value.Importance, value.Severity, value.IncidentKind} {
		if item != "" {
			if err := validateToken(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSourceRef(value SourceRef) error {
	if err := validateID(value.ResourceID, "source resource id"); err != nil {
		return err
	}
	if err := validateID(value.MutationID, "source mutation id"); err != nil {
		return err
	}
	switch value.Kind {
	case SourceTaskProvenance, SourceIncident, SourceIncidentReply:
		if value.ExpectedStatus != "" || value.ExpectedUpdatedAt != "" {
			return fmt.Errorf("%w: source expectation", ErrInvalidSubscription)
		}
	case SourceIncidentStatus:
		if err := validateToken(value.ExpectedStatus); err != nil {
			return err
		}
		if _, err := time.Parse(time.RFC3339Nano, value.ExpectedUpdatedAt); err != nil {
			return fmt.Errorf("%w: source updated time", ErrInvalidSubscription)
		}
	default:
		return fmt.Errorf("%w: source kind", ErrInvalidSubscription)
	}
	return nil
}

func validEventID(value string) bool {
	if !strings.HasPrefix(value, "evt_") || len(value) != len("evt_")+24 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "evt_"))
	return err == nil
}

func appendPreparedToLedger(state *ledgerState, subscriptions []Subscription, prepared PreparedEvent) (Event, bool, error) {
	if state == nil {
		return Event{}, false, fmt.Errorf("%w: ledger state", ErrInvalidSubscription)
	}
	if err := validatePrepared(prepared); err != nil {
		return Event{}, false, err
	}
	for _, item := range state.Events {
		if item.ID == prepared.Event.ID {
			return cloneEvent(item), false, nil
		}
	}
	if len(state.Events) >= maxLedgerEvents {
		compactLedger(state, subscriptions)
	}
	if len(state.Events) >= maxLedgerEvents {
		return Event{}, false, fmt.Errorf("%w: compaction did not free a ledger slot", ErrInvalidSubscription)
	}
	state.NextSeq++
	stored := cloneEvent(prepared.Event)
	stored.Seq = state.NextSeq
	state.Events = append(state.Events, stored)
	return cloneEvent(stored), true, nil
}

func compactLedger(state *ledgerState, subscriptions []Subscription) {
	if state == nil || len(state.Events) == 0 {
		return
	}
	minHighWater := state.NextSeq
	for _, item := range subscriptions {
		if item.CursorHighWater < minHighWater {
			minHighWater = item.CursorHighWater
		}
	}
	cut := 0
	for cut < len(state.Events) && state.Events[cut].Seq <= minHighWater {
		cut++
	}
	if cut > 0 {
		state.BaseSeq = state.Events[cut-1].Seq
		state.Events = slices.Clone(state.Events[cut:])
	}
	if len(state.Events) < maxLedgerEvents {
		return
	}
	// A non-polling subscriber never blocks a source task or Incident write.
	// Its old signed cursor is rejected with ErrCursorExpired on the next poll.
	state.BaseSeq = state.Events[0].Seq
	state.Events = slices.Clone(state.Events[1:])
}

func makeCursor(subscription Subscription, seq uint64) (string, error) {
	secret, err := hex.DecodeString(subscription.CursorSecret)
	if err != nil {
		return "", fmt.Errorf("%w: cursor secret", ErrInvalidSubscription)
	}
	payload := cursorPayload{Version: 1, ProjectID: subscription.ProjectID, Actor: subscription.Actor, SubscriptionID: subscription.ID, Seq: seq}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + hex.EncodeToString(mac.Sum(nil)), nil
}

func parseCursor(value string, subscription Subscription, projectID, actor string) (uint64, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" || len(parts[1]) != 64 {
		return 0, ErrInvalidCursor
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(data) == 0 || len(data) > 1024 {
		return 0, ErrInvalidCursor
	}
	signature, err := hex.DecodeString(parts[1])
	if err != nil {
		return 0, ErrInvalidCursor
	}
	secret, err := hex.DecodeString(subscription.CursorSecret)
	if err != nil {
		return 0, ErrInvalidCursor
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(data)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return 0, ErrInvalidCursor
	}
	var payload cursorPayload
	if err := json.Unmarshal(data, &payload); err != nil || payload.Version != 1 || payload.ProjectID != projectID || payload.Actor != actor || payload.SubscriptionID != subscription.ID {
		return 0, ErrInvalidCursor
	}
	return payload.Seq, nil
}

func findSubscriptionIndex(items []Subscription, actor, id string) int {
	for index := range items {
		if items[index].Actor == actor && items[index].ID == id {
			return index
		}
	}
	return -1
}

func findReceipt(items []idempotencyReceipt, key string) (idempotencyReceipt, bool) {
	for _, item := range items {
		if item.Key == key {
			return item, true
		}
	}
	return idempotencyReceipt{}, false
}

func appendReceipt(items []idempotencyReceipt, value idempotencyReceipt) []idempotencyReceipt {
	items = append(items, value)
	if len(items) > maxReceipts {
		items = slices.Clone(items[len(items)-maxReceipts:])
	}
	return items
}

func cloneSubscription(value Subscription) Subscription {
	value.Modules = slices.Clone(value.Modules)
	value.Tasks = cloneTaskFilter(value.Tasks)
	value.Incidents = cloneIncidentFilter(value.Incidents)
	value.Receipts = slices.Clone(value.Receipts)
	return value
}

func cloneTaskFilter(value TaskFilter) TaskFilter {
	value.Statuses = slices.Clone(value.Statuses)
	value.Types = slices.Clone(value.Types)
	value.Importances = slices.Clone(value.Importances)
	return value
}

func cloneIncidentFilter(value IncidentFilter) IncidentFilter {
	value.Statuses = slices.Clone(value.Statuses)
	value.Severities = slices.Clone(value.Severities)
	value.Kinds = slices.Clone(value.Kinds)
	return value
}

func cloneEvent(value Event) Event { return value }

func clonePrepared(value PreparedEvent) PreparedEvent {
	value.Event = cloneEvent(value.Event)
	return value
}

func projectFileName(projectID string) string { return projectID + ".yaml" }

func mintSecret() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("event subscription random secret: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func mintEventID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("event subscription random id: %w", err)
	}
	return "evt_" + hex.EncodeToString(raw[:]), nil
}

func validateProjectID(value string) error { return validateID(value, "project id") }

func validateActor(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 {
		return fmt.Errorf("%w: actor", ErrInvalidSubscription)
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ':' || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("%w: actor", ErrInvalidSubscription)
	}
	return nil
}

func validateID(value, field string) error {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value || utf8.RuneCountInString(value) > 128 {
		return fmt.Errorf("%w: %s", ErrInvalidSubscription, field)
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("%w: %s", ErrInvalidSubscription, field)
	}
	return nil
}

func validateToken(value string) error {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value || utf8.RuneCountInString(value) > 96 {
		return fmt.Errorf("%w: filter token", ErrInvalidSubscription)
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' || r == ':' {
			continue
		}
		return fmt.Errorf("%w: filter token", ErrInvalidSubscription)
	}
	return nil
}
