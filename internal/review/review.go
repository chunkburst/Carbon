// Package review owns explicit plan/manual-check review targets. These records do
// not reuse task leases or pending claim approvals: a Worker can be assigned to
// review a plan/check without becoming the execution lease holder for a task.
package review

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"carbon/internal/identity"
	"carbon/internal/store"

	"gopkg.in/yaml.v3"
)

const (
	dataDir     = "reviews"
	stateName   = "state.yaml"
	version     = 1
	maxBytes    = 1024 << 10
	maxRecords  = 4096
	maxProject  = 128
	maxTargetID = 256
	maxDecision = 8192
	maxCheckID  = 128
)

var (
	ErrNotFound       = errors.New("review target not found")
	ErrInvalidReview  = errors.New("invalid review target")
	ErrAlreadyDecided = errors.New("review target is already decided")
)

type TargetKind string

const (
	TargetPlan        TargetKind = "plan"
	TargetManualCheck TargetKind = "manual_check"
)

type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
)

type Target struct {
	ID            string     `yaml:"id" json:"id"`
	ProjectID     string     `yaml:"project_id" json:"projectId"`
	TargetKind    TargetKind `yaml:"target_kind" json:"targetKind"`
	TargetID      string     `yaml:"target_id" json:"targetId"`
	TaskID        string     `yaml:"task_id,omitempty" json:"taskId,omitempty"`
	CheckID       string     `yaml:"check_id,omitempty" json:"checkId,omitempty"`
	ReviewerActor string     `yaml:"reviewer_actor" json:"reviewerActor"`
	Status        Status     `yaml:"status" json:"status"`
	Decision      string     `yaml:"decision,omitempty" json:"decision,omitempty"`
	CreatedBy     string     `yaml:"created_by" json:"createdBy"`
	CreatedAt     string     `yaml:"created_at" json:"createdAt"`
	ResolvedBy    string     `yaml:"resolved_by,omitempty" json:"resolvedBy,omitempty"`
	ResolvedAt    string     `yaml:"resolved_at,omitempty" json:"resolvedAt,omitempty"`
}

type CreateInput struct {
	ProjectID     string
	TargetKind    TargetKind
	TargetID      string
	TaskID        string
	CheckID       string
	ReviewerActor string
}

type DecideInput struct {
	Status   Status
	Decision string
}

type state struct {
	Version int      `yaml:"version"`
	Targets []Target `yaml:"targets"`
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

func (m *Manager) Create(ctx context.Context, actor string, input CreateInput) (Target, error) {
	if err := validateActor(actor); err != nil {
		return Target{}, err
	}
	if err := validateCreate(input); err != nil {
		return Target{}, err
	}
	id, err := mintID()
	if err != nil {
		return Target{}, err
	}
	now := m.now().Format(time.RFC3339Nano)
	created := Target{ID: id, ProjectID: input.ProjectID, TargetKind: input.TargetKind, TargetID: input.TargetID, TaskID: input.TaskID, CheckID: input.CheckID, ReviewerActor: input.ReviewerActor, Status: StatusPending, CreatedBy: actor, CreatedAt: now}
	var out Target
	err = m.Store.Write(ctx, actor, "create review target", func(tx *store.WriteTx) error {
		state, err := readTx(tx)
		if err != nil {
			return err
		}
		if len(state.Targets) >= maxRecords {
			return fmt.Errorf("%w: too many targets", ErrInvalidReview)
		}
		state.Targets = append(state.Targets, created)
		if err := writeTx(tx, state); err != nil {
			return err
		}
		out = clone(created)
		return nil
	})
	return out, err
}

func (m *Manager) List(projectID string) ([]Target, error) {
	if err := validateProjectID(projectID); err != nil {
		return nil, err
	}
	state, err := m.read()
	if err != nil {
		return nil, err
	}
	out := make([]Target, 0)
	for _, target := range state.Targets {
		if target.ProjectID == projectID {
			out = append(out, clone(target))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

func (m *Manager) Get(projectID, id string) (Target, error) {
	if err := validateProjectID(projectID); err != nil {
		return Target{}, err
	}
	if err := validateID(id); err != nil {
		return Target{}, err
	}
	state, err := m.read()
	if err != nil {
		return Target{}, err
	}
	for _, target := range state.Targets {
		if target.ID == id && target.ProjectID == projectID {
			return clone(target), nil
		}
	}
	return Target{}, fmt.Errorf("%w: %s", ErrNotFound, id)
}

func (m *Manager) Decide(ctx context.Context, actor, projectID, id string, input DecideInput) (Target, error) {
	if err := validateActor(actor); err != nil {
		return Target{}, err
	}
	if err := validateProjectID(projectID); err != nil {
		return Target{}, err
	}
	if err := validateID(id); err != nil {
		return Target{}, err
	}
	if input.Status != StatusApproved && input.Status != StatusRejected {
		return Target{}, fmt.Errorf("%w: decision status", ErrInvalidReview)
	}
	if err := validateDecision(input.Decision, false); err != nil {
		return Target{}, err
	}
	var out Target
	err := m.Store.Write(ctx, actor, "decide review target", func(tx *store.WriteTx) error {
		state, err := readTx(tx)
		if err != nil {
			return err
		}
		for index := range state.Targets {
			current := &state.Targets[index]
			if current.ID != id || current.ProjectID != projectID {
				continue
			}
			if current.Status != StatusPending {
				if current.Status == input.Status && current.Decision == input.Decision {
					out = clone(*current)
					return nil
				}
				return ErrAlreadyDecided
			}
			current.Status, current.Decision, current.ResolvedBy, current.ResolvedAt = input.Status, input.Decision, actor, m.now().Format(time.RFC3339Nano)
			if err := writeTx(tx, state); err != nil {
				return err
			}
			out = clone(*current)
			return nil
		}
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	})
	return out, err
}

func (m *Manager) read() (state, error) {
	if m == nil || m.Store == nil {
		return state{}, errors.New("review manager has no store")
	}
	data, err := m.Store.ReadData(dataDir, stateName)
	if errors.Is(err, os.ErrNotExist) {
		return state{Version: version}, nil
	}
	if err != nil {
		return state{}, err
	}
	return decode(data)
}

func readTx(tx *store.WriteTx) (state, error) {
	data, err := tx.ReadData(dataDir, stateName)
	if errors.Is(err, os.ErrNotExist) {
		return state{Version: version}, nil
	}
	if err != nil {
		return state{}, err
	}
	return decode(data)
}

func writeTx(tx *store.WriteTx, value state) error {
	if err := validateState(value); err != nil {
		return err
	}
	data, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: encode", ErrInvalidReview)
	}
	return tx.WriteData(dataDir, stateName, data)
}

func decode(data []byte) (state, error) {
	if len(data) == 0 || len(data) > maxBytes || !utf8.Valid(data) {
		return state{}, fmt.Errorf("%w: state", ErrInvalidReview)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var out state
	if err := decoder.Decode(&out); err != nil {
		return state{}, fmt.Errorf("%w: parse: %v", ErrInvalidReview, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return state{}, fmt.Errorf("%w: multiple documents", ErrInvalidReview)
	}
	return out, validateState(out)
}

func validateState(value state) error {
	if value.Version != version || len(value.Targets) > maxRecords {
		return fmt.Errorf("%w: state", ErrInvalidReview)
	}
	seen := make(map[string]struct{}, len(value.Targets))
	for _, target := range value.Targets {
		if err := validateTarget(target); err != nil {
			return err
		}
		if _, found := seen[target.ID]; found {
			return fmt.Errorf("%w: duplicate target", ErrInvalidReview)
		}
		seen[target.ID] = struct{}{}
	}
	return nil
}

func validateCreate(input CreateInput) error {
	if err := validateProjectID(input.ProjectID); err != nil {
		return err
	}
	if input.TargetKind != TargetPlan && input.TargetKind != TargetManualCheck {
		return fmt.Errorf("%w: target kind", ErrInvalidReview)
	}
	if err := store.ValidateTaskID(input.TaskID); err != nil {
		return fmt.Errorf("%w: task id", ErrInvalidReview)
	}
	switch input.TargetKind {
	case TargetPlan:
		if input.TargetID != input.TaskID || input.CheckID != "" {
			return fmt.Errorf("%w: plan target metadata", ErrInvalidReview)
		}
	case TargetManualCheck:
		if !validDecimalCheckIndex(input.CheckID) || input.TargetID != input.TaskID+"#check:"+input.CheckID {
			return fmt.Errorf("%w: manual check target metadata", ErrInvalidReview)
		}
	}
	return validateActor(input.ReviewerActor)
}

func validDecimalCheckIndex(value string) bool {
	if value == "" || len(value) > maxCheckID || (len(value) > 1 && value[0] == '0') {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	_, err := strconv.Atoi(value)
	return err == nil
}

func validateTarget(target Target) error {
	if err := validateID(target.ID); err != nil {
		return err
	}
	if err := validateCreate(CreateInput{ProjectID: target.ProjectID, TargetKind: target.TargetKind, TargetID: target.TargetID, TaskID: target.TaskID, CheckID: target.CheckID, ReviewerActor: target.ReviewerActor}); err != nil {
		return err
	}
	if !validTime(target.CreatedAt) || !isActor(target.CreatedBy) {
		return fmt.Errorf("%w: created metadata", ErrInvalidReview)
	}
	switch target.Status {
	case StatusPending:
		if target.Decision != "" || target.ResolvedBy != "" || target.ResolvedAt != "" {
			return fmt.Errorf("%w: pending decision metadata", ErrInvalidReview)
		}
	case StatusApproved, StatusRejected:
		if err := validateDecision(target.Decision, false); err != nil || !isActor(target.ResolvedBy) || !validTime(target.ResolvedAt) {
			return fmt.Errorf("%w: decision metadata", ErrInvalidReview)
		}
	default:
		return fmt.Errorf("%w: status", ErrInvalidReview)
	}
	return nil
}

func validateActor(value string) error {
	if !isActor(value) {
		return fmt.Errorf("%w: actor", ErrInvalidReview)
	}
	return nil
}
func isActor(value string) bool { return identity.ValidateActor(value) == nil }

func validateProjectID(value string) error {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value || utf8.RuneCountInString(value) > maxProject {
		return fmt.Errorf("%w: project id", ErrInvalidReview)
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("%w: project id", ErrInvalidReview)
	}
	return nil
}

func validateOpaque(value string, max int) error {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value || utf8.RuneCountInString(value) > max {
		return fmt.Errorf("%w: target", ErrInvalidReview)
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return fmt.Errorf("%w: target", ErrInvalidReview)
		}
	}
	return nil
}

func validateDecision(value string, allowEmpty bool) error {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value || (!allowEmpty && value == "") || utf8.RuneCountInString(value) > maxDecision {
		return fmt.Errorf("%w: decision", ErrInvalidReview)
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' {
			return fmt.Errorf("%w: decision", ErrInvalidReview)
		}
	}
	return nil
}

func validateID(value string) error {
	if !strings.HasPrefix(value, "rev_") || len(value) != len("rev_")+24 {
		return fmt.Errorf("%w: id", ErrInvalidReview)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(value, "rev_")); err != nil {
		return fmt.Errorf("%w: id", ErrInvalidReview)
	}
	return nil
}

func validTime(value string) bool { _, err := time.Parse(time.RFC3339Nano, value); return err == nil }

func mintID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("review random id: %w", err)
	}
	return "rev_" + hex.EncodeToString(raw[:]), nil
}

func clone(target Target) Target { return target }
