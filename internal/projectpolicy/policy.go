// Package projectpolicy owns the small, project-addressed identity policy
// sidecar. A Carbon cluster shares task/config storage, but Identity Mode and
// no-trace are intentionally not cluster policy: two member projects can make
// different coordination choices without leaking that choice to one another.
package projectpolicy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"carbon/internal/store"

	"gopkg.in/yaml.v3"
)

const (
	dataDir  = "project-policies"
	version  = 1
	maxBytes = 64 << 10
)

var ErrInvalidPolicy = errors.New("invalid project policy")

// Policy is deliberately narrow. Missing files resolve to false/false, which
// preserves existing projects until a human explicitly opts in.
type Policy struct {
	Version      int    `yaml:"version" json:"version"`
	ProjectID    string `yaml:"project_id" json:"projectId"`
	IdentityMode bool   `yaml:"identity_mode,omitempty" json:"identityMode"`
	NoTraceMode  bool   `yaml:"no_trace_mode,omitempty" json:"noTraceMode"`
}

type Manager struct{ Store *store.Store }

func New(s *store.Store) *Manager { return &Manager{Store: s} }

// Default returns the policy represented by an absent sidecar.
func Default(projectID string) (Policy, error) {
	if err := ValidateProjectID(projectID); err != nil {
		return Policy{}, err
	}
	return Policy{Version: version, ProjectID: projectID}, nil
}

func (m *Manager) Get(projectID string) (Policy, error) {
	if m == nil || m.Store == nil {
		return Policy{}, errors.New("project policy manager has no store")
	}
	if _, err := Default(projectID); err != nil {
		return Policy{}, err
	}
	data, err := m.Store.ReadData(dataDir, fileName(projectID))
	if errors.Is(err, os.ErrNotExist) {
		return Default(projectID)
	}
	if err != nil {
		return Policy{}, err
	}
	return decode(projectID, data)
}

// GetTx reads the matching project sidecar under an existing Store.Write lock.
func (m *Manager) GetTx(tx *store.WriteTx, projectID string) (Policy, error) {
	if m == nil || m.Store == nil || tx == nil {
		return Policy{}, errors.New("project policy manager has no store transaction")
	}
	if _, err := Default(projectID); err != nil {
		return Policy{}, err
	}
	data, err := tx.ReadData(dataDir, fileName(projectID))
	if errors.Is(err, os.ErrNotExist) {
		return Default(projectID)
	}
	if err != nil {
		return Policy{}, err
	}
	return decode(projectID, data)
}

// Save persists one fully validated policy in an ordinary Store.Write. Service
// code that needs policy and another record atomically should call SaveTx.
func (m *Manager) Save(ctx context.Context, actor string, value Policy) (Policy, error) {
	if m == nil || m.Store == nil {
		return Policy{}, errors.New("project policy manager has no store")
	}
	if strings.TrimSpace(actor) == "" {
		return Policy{}, fmt.Errorf("%w: actor", ErrInvalidPolicy)
	}
	var out Policy
	err := m.Store.Write(ctx, actor, "update project identity policy", func(tx *store.WriteTx) error {
		var err error
		out, err = m.SaveTx(tx, value)
		return err
	})
	return out, err
}

// SaveTx is intentionally the only write primitive used by the identity service.
func (m *Manager) SaveTx(tx *store.WriteTx, value Policy) (Policy, error) {
	if m == nil || m.Store == nil || tx == nil {
		return Policy{}, errors.New("project policy manager has no store transaction")
	}
	if err := Validate(value); err != nil {
		return Policy{}, err
	}
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return Policy{}, fmt.Errorf("%w: encode: %v", ErrInvalidPolicy, err)
	}
	if err := tx.WriteData(dataDir, fileName(value.ProjectID), encoded); err != nil {
		return Policy{}, err
	}
	return value, nil
}

func Validate(value Policy) error {
	if value.Version != version {
		return fmt.Errorf("%w: unsupported version", ErrInvalidPolicy)
	}
	return ValidateProjectID(value.ProjectID)
}

// ValidateProjectID matches Carbon's manifest-safe stable project-id grammar. It
// is exported so project-bound consumers can reject a missing/unsafe scope before
// constructing filenames or reading a policy sidecar.
func ValidateProjectID(projectID string) error {
	if projectID == "" || !utf8.ValidString(projectID) || strings.TrimSpace(projectID) != projectID || utf8.RuneCountInString(projectID) > 128 {
		return fmt.Errorf("%w: project id", ErrInvalidPolicy)
	}
	for _, r := range projectID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("%w: project id", ErrInvalidPolicy)
	}
	return nil
}

func fileName(projectID string) string { return projectID + ".yaml" }

func decode(projectID string, data []byte) (Policy, error) {
	if len(data) == 0 || len(data) > maxBytes || !utf8.Valid(data) {
		return Policy{}, fmt.Errorf("%w: encoded state", ErrInvalidPolicy)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var value Policy
	if err := decoder.Decode(&value); err != nil {
		return Policy{}, fmt.Errorf("%w: parse: %v", ErrInvalidPolicy, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Policy{}, fmt.Errorf("%w: multiple documents", ErrInvalidPolicy)
		}
		return Policy{}, fmt.Errorf("%w: parse: %v", ErrInvalidPolicy, err)
	}
	if err := Validate(value); err != nil {
		return Policy{}, err
	}
	if value.ProjectID != projectID {
		return Policy{}, fmt.Errorf("%w: project id does not match sidecar name", ErrInvalidPolicy)
	}
	return value, nil
}
