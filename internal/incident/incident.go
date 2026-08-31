// Package incident owns Carbon's project-scoped process record. An Incident is
// intentionally not a task, Work Log, or SSE Event: it captures an unresolved or
// exploratory situation and may contain a chronological discussion without implying
// that a deliverable task must be completed.
package incident

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"carbon/internal/subscription"

	"gopkg.in/yaml.v3"
)

const (
	dataDir         = "incidents"
	stateName       = "state.yaml"
	repliesName     = "replies.yaml"
	stateVersion    = 1
	maxBytes        = 1024 << 10
	maxRecords      = 4096
	maxReplies      = 16384
	maxProject      = 128
	maxTitle        = 240
	maxBody         = 64 << 10
	maxReplyBody    = 16 << 10
	maxKind         = 64
	maxRelatedTasks = 32
)

var (
	ErrNotFound        = errors.New("incident not found")
	ErrInvalidIncident = errors.New("invalid incident")
)

type Severity string

const (
	SeverityInfo   Severity = "info"
	SeverityLow    Severity = "low"
	SeverityNormal Severity = "normal"
	SeverityHigh   Severity = "high"
	SeverityUrgent Severity = "urgent"
)

type Status string

const (
	StatusOpen          Status = "open"
	StatusInvestigating Status = "investigating"
	StatusResolved      Status = "resolved"
	StatusClosed        Status = "closed"
)

type Origin string

const (
	OriginManual         Origin = "manual"
	OriginIdentityChange Origin = "identity_change"
)

// Kind describes the process shape without changing an Incident into a task.
// The listed values are built-in UX keys; a future project may supply another
// valid machine key without a storage migration.
type Kind string

const (
	KindSudden         Kind = "sudden"
	KindLongRunning    Kind = "long_running"
	KindInvestigation  Kind = "investigation"
	KindIdentityChange Kind = "identity_change"
	KindOther          Kind = "other"
)

// Incident is the combined read model. Replies are stored independently so an
// identity-origin Incident can be atomically created with its audit journal while
// later discussion remains a normal Incident concern.
type Incident struct {
	ID             string   `yaml:"id" json:"id"`
	ProjectID      string   `yaml:"project_id" json:"projectId"`
	Kind           Kind     `yaml:"kind" json:"kind"`
	RelatedTaskIDs []string `yaml:"related_task_ids,omitempty" json:"relatedTaskIds,omitempty"`
	Title          string   `yaml:"title" json:"title"`
	Body           string   `yaml:"body,omitempty" json:"body,omitempty"`
	Severity       Severity `yaml:"severity" json:"severity"`
	Status         Status   `yaml:"status" json:"status"`
	CreatedBy      string   `yaml:"created_by" json:"createdBy"`
	CreatedAt      string   `yaml:"created_at" json:"createdAt"`
	UpdatedAt      string   `yaml:"updated_at" json:"updatedAt"`
	Origin         Origin   `yaml:"origin" json:"origin"`
	RelatedAuditID string   `yaml:"related_audit_id,omitempty" json:"relatedAuditId,omitempty"`
	Replies        []Reply  `yaml:"-" json:"replies,omitempty"`
}

type Reply struct {
	ID         string `yaml:"id" json:"id"`
	IncidentID string `yaml:"incident_id" json:"incidentId"`
	Author     string `yaml:"author" json:"author"`
	Body       string `yaml:"body" json:"body"`
	CreatedAt  string `yaml:"created_at" json:"createdAt"`
}

type CreateInput struct {
	ProjectID      string
	Kind           Kind
	RelatedTaskIDs []string
	Title          string
	Body           string
	Severity       Severity
}

type UpdateInput struct {
	Status Status
}

type manualState struct {
	Version   int        `yaml:"version"`
	Incidents []Incident `yaml:"incidents"`
}

type replyState struct {
	Version int     `yaml:"version"`
	Replies []Reply `yaml:"replies"`
}

type Manager struct {
	Store *store.Store
	Now   func() time.Time
	// Events is optional for direct package callers. The scoped MCP service sets
	// it for stable v2 projects, where Incident mutations participate in the
	// recoverable project event ledger without becoming task activity.
	Events *subscription.Manager
	// LegacyProjection is set by the project-scoped adapter for an isolated
	// standalone project. Shared clusters always keep it false so automatic
	// identity Incidents cannot revive a shared registry projection.
	LegacyProjection bool
}

func New(s *store.Store, now func() time.Time) *Manager {
	return NewScoped(s, now, false)
}

func NewScoped(s *store.Store, now func() time.Time, legacyProjection bool) *Manager {
	return NewScopedWithEvents(s, now, legacyProjection, nil)
}

func NewScopedWithEvents(s *store.Store, now func() time.Time, legacyProjection bool, events *subscription.Manager) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{Store: s, Now: now, Events: events, LegacyProjection: legacyProjection}
}

func (m *Manager) identityManager(projectID string) (*identity.Manager, error) {
	if m == nil || m.Store == nil {
		return nil, errors.New("incident manager has no store")
	}
	return identity.NewProject(m.Store, m.Now, projectID, m.LegacyProjection)
}

func (m *Manager) now() time.Time {
	if m == nil || m.Now == nil {
		return time.Now().UTC()
	}
	return m.Now().UTC()
}

func (m *Manager) prepareIncidentEventTx(tx *store.WriteTx, actor string, item Incident, eventKind string, source subscription.SourceRef) (*subscription.PreparedEvent, error) {
	if m == nil || m.Events == nil {
		return nil, nil
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, item.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("%w: event source time", ErrInvalidIncident)
	}
	prepared, err := m.Events.PrepareTx(tx, subscription.EventInput{
		ProjectID: item.ProjectID, OccurredAt: occurredAt, Module: subscription.ModuleIncidents,
		Kind: eventKind, ResourceID: item.ID, Actor: actor, Status: string(item.Status),
		Severity: string(item.Severity), IncidentKind: string(item.Kind),
	}, source)
	if err != nil {
		return nil, err
	}
	return &prepared, nil
}

func (m *Manager) commitPreparedEventTx(tx *store.WriteTx, prepared *subscription.PreparedEvent) error {
	if prepared == nil || m == nil || m.Events == nil {
		return nil
	}
	_, err := m.Events.CommitTx(tx, *prepared)
	return err
}

func (m *Manager) Create(ctx context.Context, actor string, input CreateInput) (Incident, error) {
	if err := validateActor(actor); err != nil {
		return Incident{}, err
	}
	if err := validateCreate(input); err != nil {
		return Incident{}, err
	}
	id, err := mintID("inc_")
	if err != nil {
		return Incident{}, err
	}
	now := m.now().Format(time.RFC3339Nano)
	created := Incident{ID: id, ProjectID: input.ProjectID, Kind: normalizeKind(input.Kind), RelatedTaskIDs: slices.Clone(input.RelatedTaskIDs), Title: input.Title, Body: input.Body, Severity: normalizeSeverity(input.Severity), Status: StatusOpen, CreatedBy: actor, CreatedAt: now, UpdatedAt: now, Origin: OriginManual}
	var out Incident
	err = m.Store.Write(ctx, actor, "create incident", func(tx *store.WriteTx) error {
		state, err := readManualTx(tx)
		if err != nil {
			return err
		}
		if len(state.Incidents) >= maxRecords {
			return fmt.Errorf("%w: too many incidents", ErrInvalidIncident)
		}
		state.Incidents = append(state.Incidents, created)
		prepared, err := m.prepareIncidentEventTx(tx, actor, created, "created", subscription.SourceRef{
			Kind: subscription.SourceIncident, ResourceID: created.ID, MutationID: created.ID,
		})
		if err != nil {
			return err
		}
		if err := writeManualTx(tx, state); err != nil {
			return err
		}
		if err := m.commitPreparedEventTx(tx, prepared); err != nil {
			return err
		}
		out = cloneIncident(created)
		return nil
	})
	return out, err
}

func (m *Manager) List(projectID string) ([]Incident, error) {
	if err := validateProjectID(projectID); err != nil {
		return nil, err
	}
	manual, err := m.readManual()
	if err != nil {
		return nil, err
	}
	replies, err := m.readReplies()
	if err != nil {
		return nil, err
	}
	out := make([]Incident, 0)
	for _, item := range manual.Incidents {
		if item.ProjectID == projectID {
			out = append(out, joinReplies(item, replies.Replies))
		}
	}
	identityManager, err := m.identityManager(projectID)
	if err != nil {
		return nil, err
	}
	auto, err := identityManager.ListAutoIncidents(projectID)
	if err != nil {
		return nil, err
	}
	for _, item := range auto {
		out = append(out, joinReplies(fromAuto(item), replies.Replies))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

func (m *Manager) Get(projectID, id string) (Incident, error) {
	if err := validateProjectID(projectID); err != nil {
		return Incident{}, err
	}
	if err := validateID(id, "inc_"); err != nil {
		return Incident{}, err
	}
	manual, err := m.readManual()
	if err != nil {
		return Incident{}, err
	}
	replies, err := m.readReplies()
	if err != nil {
		return Incident{}, err
	}
	for _, item := range manual.Incidents {
		if item.ID == id && item.ProjectID == projectID {
			return joinReplies(item, replies.Replies), nil
		}
	}
	identityManager, managerErr := m.identityManager(projectID)
	if managerErr != nil {
		return Incident{}, managerErr
	}
	auto, err := identityManager.GetAutoIncident(id)
	if err == nil && auto.ProjectID == projectID {
		return joinReplies(fromAuto(auto), replies.Replies), nil
	}
	if err != nil && !errors.Is(err, identity.ErrNotFound) {
		return Incident{}, err
	}
	return Incident{}, fmt.Errorf("%w: %s", ErrNotFound, id)
}

func (m *Manager) UpdateLifecycle(ctx context.Context, actor, projectID, id string, input UpdateInput) (Incident, error) {
	if err := validateActor(actor); err != nil {
		return Incident{}, err
	}
	if err := validateProjectID(projectID); err != nil {
		return Incident{}, err
	}
	if err := validateID(id, "inc_"); err != nil {
		return Incident{}, err
	}
	if !validStatus(input.Status) {
		return Incident{}, fmt.Errorf("%w: status", ErrInvalidIncident)
	}
	// Identity-origin Incidents remain stored with the identity journal so their
	// audit link stays physically atomic. Their lifecycle is still a normal Incident
	// action and replies are merged from this package's reply store.
	identityManager, managerErr := m.identityManager(projectID)
	if managerErr != nil {
		return Incident{}, managerErr
	}
	auto, autoErr := identityManager.GetAutoIncident(id)
	if autoErr == nil {
		if auto.ProjectID != projectID {
			return Incident{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		var updated identity.AutoIncident
		err := m.Store.Write(ctx, actor, "update automatic identity incident", func(tx *store.WriteTx) error {
			var prepared *subscription.PreparedEvent
			var changed bool
			var err error
			updated, changed, err = identityManager.UpdateAutoIncidentTxWithBeforeWrite(tx, actor, id, string(input.Status), func(next identity.AutoIncident) error {
				mutationID, err := mintID("imu_")
				if err != nil {
					return err
				}
				prepared, err = m.prepareIncidentEventTx(tx, actor, fromAuto(next), "status_changed", subscription.SourceRef{
					Kind: subscription.SourceIncidentStatus, ResourceID: next.ID, MutationID: mutationID,
					ExpectedStatus: next.Status, ExpectedUpdatedAt: next.UpdatedAt,
				})
				return err
			})
			if err != nil {
				return err
			}
			if !changed {
				return nil
			}
			return m.commitPreparedEventTx(tx, prepared)
		})
		if err != nil {
			return Incident{}, err
		}
		replies, err := m.readReplies()
		if err != nil {
			return Incident{}, err
		}
		return joinReplies(fromAuto(updated), replies.Replies), nil
	}
	if autoErr != nil && !errors.Is(autoErr, identity.ErrNotFound) {
		return Incident{}, autoErr
	}

	var out Incident
	err := m.Store.Write(ctx, actor, "update incident lifecycle", func(tx *store.WriteTx) error {
		state, err := readManualTx(tx)
		if err != nil {
			return err
		}
		for i := range state.Incidents {
			current := &state.Incidents[i]
			if current.ID != id || current.ProjectID != projectID {
				continue
			}
			if current.Status != input.Status {
				current.Status = input.Status
				current.UpdatedAt = m.now().Format(time.RFC3339Nano)
				mutationID, err := mintID("imu_")
				if err != nil {
					return err
				}
				prepared, err := m.prepareIncidentEventTx(tx, actor, *current, "status_changed", subscription.SourceRef{
					Kind: subscription.SourceIncidentStatus, ResourceID: current.ID, MutationID: mutationID,
					ExpectedStatus: string(current.Status), ExpectedUpdatedAt: current.UpdatedAt,
				})
				if err != nil {
					return err
				}
				if err := writeManualTx(tx, state); err != nil {
					return err
				}
				if err := m.commitPreparedEventTx(tx, prepared); err != nil {
					return err
				}
			}
			out = cloneIncident(*current)
			return nil
		}
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	})
	if err != nil {
		return Incident{}, err
	}
	replies, err := m.readReplies()
	if err != nil {
		return Incident{}, err
	}
	return joinReplies(out, replies.Replies), nil
}

// Reply is deliberately append-only. The same Agent can reply repeatedly, including
// self-questions/self-answers, because an Incident captures investigation process.
func (m *Manager) Reply(ctx context.Context, actor, projectID, id, body string) (Reply, error) {
	if err := validateActor(actor); err != nil {
		return Reply{}, err
	}
	if err := validateProjectID(projectID); err != nil {
		return Reply{}, err
	}
	if err := validateID(id, "inc_"); err != nil {
		return Reply{}, err
	}
	if err := validateBody(body, maxReplyBody, false); err != nil {
		return Reply{}, err
	}
	// Validate scope before persisting a reply. The target itself has no delete API,
	// and this is deliberately a normal process record rather than task provenance.
	target, err := m.Get(projectID, id)
	if err != nil {
		return Reply{}, err
	}
	replyID, err := mintID("rep_")
	if err != nil {
		return Reply{}, err
	}
	created := Reply{ID: replyID, IncidentID: id, Author: actor, Body: body, CreatedAt: m.now().Format(time.RFC3339Nano)}
	var out Reply
	err = m.Store.Write(ctx, actor, "reply to incident", func(tx *store.WriteTx) error {
		state, err := readRepliesTx(tx)
		if err != nil {
			return err
		}
		if len(state.Replies) >= maxReplies {
			return fmt.Errorf("%w: too many incident replies", ErrInvalidIncident)
		}
		state.Replies = append(state.Replies, created)
		eventTarget := cloneIncident(target)
		eventTarget.UpdatedAt = created.CreatedAt
		prepared, err := m.prepareIncidentEventTx(tx, actor, eventTarget, "reply_added", subscription.SourceRef{
			Kind: subscription.SourceIncidentReply, ResourceID: id, MutationID: created.ID,
		})
		if err != nil {
			return err
		}
		if err := writeRepliesTx(tx, state); err != nil {
			return err
		}
		if err := m.commitPreparedEventTx(tx, prepared); err != nil {
			return err
		}
		out = created
		return nil
	})
	return out, err
}

func (m *Manager) readManual() (manualState, error) {
	if m == nil || m.Store == nil {
		return manualState{}, errors.New("incident manager has no store")
	}
	data, err := m.Store.ReadData(dataDir, stateName)
	if errors.Is(err, os.ErrNotExist) {
		return manualState{Version: stateVersion}, nil
	}
	if err != nil {
		return manualState{}, err
	}
	return decodeManual(data)
}

func readManualTx(tx *store.WriteTx) (manualState, error) {
	data, err := tx.ReadData(dataDir, stateName)
	if errors.Is(err, os.ErrNotExist) {
		return manualState{Version: stateVersion}, nil
	}
	if err != nil {
		return manualState{}, err
	}
	return decodeManual(data)
}

func writeManualTx(tx *store.WriteTx, state manualState) error {
	if err := validateManual(state); err != nil {
		return err
	}
	data, err := yaml.Marshal(state)
	if err != nil {
		return fmt.Errorf("%w: encode", ErrInvalidIncident)
	}
	return tx.WriteData(dataDir, stateName, data)
}

func (m *Manager) readReplies() (replyState, error) {
	if m == nil || m.Store == nil {
		return replyState{}, errors.New("incident manager has no store")
	}
	data, err := m.Store.ReadData(dataDir, repliesName)
	if errors.Is(err, os.ErrNotExist) {
		return replyState{Version: stateVersion}, nil
	}
	if err != nil {
		return replyState{}, err
	}
	return decodeReplies(data)
}

func readRepliesTx(tx *store.WriteTx) (replyState, error) {
	data, err := tx.ReadData(dataDir, repliesName)
	if errors.Is(err, os.ErrNotExist) {
		return replyState{Version: stateVersion}, nil
	}
	if err != nil {
		return replyState{}, err
	}
	return decodeReplies(data)
}

func writeRepliesTx(tx *store.WriteTx, state replyState) error {
	if err := validateReplies(state); err != nil {
		return err
	}
	data, err := yaml.Marshal(state)
	if err != nil {
		return fmt.Errorf("%w: encode replies", ErrInvalidIncident)
	}
	return tx.WriteData(dataDir, repliesName, data)
}

func decodeManual(data []byte) (manualState, error) {
	if len(data) == 0 || len(data) > maxBytes || !utf8.Valid(data) {
		return manualState{}, fmt.Errorf("%w: encoded state", ErrInvalidIncident)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var out manualState
	if err := decoder.Decode(&out); err != nil {
		return manualState{}, fmt.Errorf("%w: parse: %v", ErrInvalidIncident, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return manualState{}, fmt.Errorf("%w: multiple documents", ErrInvalidIncident)
	}
	return out, validateManual(out)
}

func decodeReplies(data []byte) (replyState, error) {
	if len(data) == 0 || len(data) > maxBytes || !utf8.Valid(data) {
		return replyState{}, fmt.Errorf("%w: encoded replies", ErrInvalidIncident)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var out replyState
	if err := decoder.Decode(&out); err != nil {
		return replyState{}, fmt.Errorf("%w: parse replies: %v", ErrInvalidIncident, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return replyState{}, fmt.Errorf("%w: multiple reply documents", ErrInvalidIncident)
	}
	return out, validateReplies(out)
}

func validateManual(state manualState) error {
	if state.Version != stateVersion || len(state.Incidents) > maxRecords {
		return fmt.Errorf("%w: state", ErrInvalidIncident)
	}
	seen := make(map[string]struct{}, len(state.Incidents))
	for _, item := range state.Incidents {
		if err := validateIncident(item); err != nil {
			return err
		}
		if item.Origin != OriginManual || item.RelatedAuditID != "" {
			return fmt.Errorf("%w: manual origin", ErrInvalidIncident)
		}
		if _, found := seen[item.ID]; found {
			return fmt.Errorf("%w: duplicate incident", ErrInvalidIncident)
		}
		seen[item.ID] = struct{}{}
	}
	return nil
}

func validateReplies(state replyState) error {
	if state.Version != stateVersion || len(state.Replies) > maxReplies {
		return fmt.Errorf("%w: replies", ErrInvalidIncident)
	}
	seen := make(map[string]struct{}, len(state.Replies))
	for _, reply := range state.Replies {
		if err := validateReply(reply); err != nil {
			return err
		}
		if _, found := seen[reply.ID]; found {
			return fmt.Errorf("%w: duplicate reply", ErrInvalidIncident)
		}
		seen[reply.ID] = struct{}{}
	}
	return nil
}

func validateCreate(input CreateInput) error {
	if err := validateProjectID(input.ProjectID); err != nil {
		return err
	}
	if !validKind(normalizeKind(input.Kind)) {
		return fmt.Errorf("%w: kind", ErrInvalidIncident)
	}
	if len(input.RelatedTaskIDs) > maxRelatedTasks {
		return fmt.Errorf("%w: related task ids", ErrInvalidIncident)
	}
	seen := make(map[string]struct{}, len(input.RelatedTaskIDs))
	for _, id := range input.RelatedTaskIDs {
		if err := store.ValidateTaskID(id); err != nil {
			return fmt.Errorf("%w: related task id", ErrInvalidIncident)
		}
		if _, found := seen[id]; found {
			return fmt.Errorf("%w: duplicate related task id", ErrInvalidIncident)
		}
		seen[id] = struct{}{}
	}
	if err := validateBody(input.Title, maxTitle, false); err != nil {
		return err
	}
	if err := validateBody(input.Body, maxBody, true); err != nil {
		return err
	}
	if !validSeverity(normalizeSeverity(input.Severity)) {
		return fmt.Errorf("%w: severity", ErrInvalidIncident)
	}
	return nil
}

func validateIncident(item Incident) error {
	if err := validateID(item.ID, "inc_"); err != nil {
		return err
	}
	if err := validateCreate(CreateInput{ProjectID: item.ProjectID, Kind: item.Kind, RelatedTaskIDs: item.RelatedTaskIDs, Title: item.Title, Body: item.Body, Severity: item.Severity}); err != nil {
		return err
	}
	if !validStatus(item.Status) || !validOrigin(item.Origin) || !isActor(item.CreatedBy) {
		return fmt.Errorf("%w: incident fields", ErrInvalidIncident)
	}
	if !validTime(item.CreatedAt) || !validTime(item.UpdatedAt) || item.UpdatedAt < item.CreatedAt {
		return fmt.Errorf("%w: incident time", ErrInvalidIncident)
	}
	if item.Origin == OriginIdentityChange && item.RelatedAuditID == "" {
		return fmt.Errorf("%w: identity incident audit", ErrInvalidIncident)
	}
	return nil
}

func validateReply(reply Reply) error {
	if err := validateID(reply.ID, "rep_"); err != nil {
		return err
	}
	if err := validateID(reply.IncidentID, "inc_"); err != nil || !isActor(reply.Author) || !validTime(reply.CreatedAt) {
		return fmt.Errorf("%w: reply fields", ErrInvalidIncident)
	}
	return validateBody(reply.Body, maxReplyBody, false)
}

func fromAuto(item identity.AutoIncident) Incident {
	return Incident{ID: item.ID, ProjectID: item.ProjectID, Kind: Kind(item.Kind), RelatedTaskIDs: slices.Clone(item.RelatedTaskIDs), Title: item.Title, Body: item.Body, Severity: Severity(item.Severity), Status: Status(item.Status), CreatedBy: item.CreatedBy, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, Origin: Origin(item.Origin), RelatedAuditID: item.RelatedAuditID}
}

func joinReplies(item Incident, replies []Reply) Incident {
	item.Replies = nil
	for _, reply := range replies {
		if reply.IncidentID == item.ID {
			item.Replies = append(item.Replies, reply)
		}
	}
	sort.Slice(item.Replies, func(i, j int) bool {
		if item.Replies[i].CreatedAt == item.Replies[j].CreatedAt {
			return item.Replies[i].ID < item.Replies[j].ID
		}
		return item.Replies[i].CreatedAt < item.Replies[j].CreatedAt
	})
	return item
}

func cloneIncident(item Incident) Incident {
	item.RelatedTaskIDs = slices.Clone(item.RelatedTaskIDs)
	item.Replies = slices.Clone(item.Replies)
	return item
}

func normalizeSeverity(value Severity) Severity {
	if value == "" {
		return SeverityNormal
	}
	return value
}

func normalizeKind(value Kind) Kind {
	if value == "" {
		return KindOther
	}
	return value
}

func validSeverity(value Severity) bool {
	return value == SeverityInfo || value == SeverityLow || value == SeverityNormal || value == SeverityHigh || value == SeverityUrgent
}

func validKind(value Kind) bool {
	if value == "" || !utf8.ValidString(string(value)) || strings.TrimSpace(string(value)) != string(value) || utf8.RuneCountInString(string(value)) > maxKind {
		return false
	}
	for index, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9' && index > 0) || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}
func validStatus(value Status) bool {
	return value == StatusOpen || value == StatusInvestigating || value == StatusResolved || value == StatusClosed
}
func validOrigin(value Origin) bool { return value == OriginManual || value == OriginIdentityChange }

func validateActor(actor string) error {
	if !isActor(actor) {
		return fmt.Errorf("%w: actor", ErrInvalidIncident)
	}
	return nil
}

func isActor(actor string) bool { return identity.ValidateActor(actor) == nil }

func validateProjectID(value string) error {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value || utf8.RuneCountInString(value) > maxProject {
		return fmt.Errorf("%w: project id", ErrInvalidIncident)
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("%w: project id", ErrInvalidIncident)
	}
	return nil
}

func validateBody(value string, limit int, allowEmpty bool) error {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value || (!allowEmpty && value == "") || utf8.RuneCountInString(value) > limit {
		return fmt.Errorf("%w: text", ErrInvalidIncident)
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' {
			return fmt.Errorf("%w: text", ErrInvalidIncident)
		}
	}
	return nil
}

func validateID(value, prefix string) error {
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+24 {
		return fmt.Errorf("%w: id", ErrInvalidIncident)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(value, prefix)); err != nil {
		return fmt.Errorf("%w: id", ErrInvalidIncident)
	}
	return nil
}

func validTime(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func mintID(prefix string) (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("incident random id: %w", err)
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}
