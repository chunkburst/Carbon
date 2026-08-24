// Package identity owns Carbon's durable Worker identity registry. The registry is
// store-local (one shared-cluster or one standalone-project data root), so a Worker
// can deliberately claim a different role in different projects without a Home-wide
// global identity leaking across task pools.
package identity

import (
	"bytes"
	"context"
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
	tasktypes "carbon/internal/types"

	"gopkg.in/yaml.v3"
)

const (
	dataDir      = "identities"
	registryName = "registry.yaml"
	// RegistryVersion makes later on-disk migrations explicit instead of silently
	// interpreting an unknown shape as today's policy.
	RegistryVersion = 1

	maxRegistryBytes = 256 << 10
	maxRecords       = 2048
	maxActorRunes    = 128
	maxRoleRunes     = 96
	maxReasonRunes   = 1024
	maxTypes         = 16
)

var (
	ErrNotFound             = errors.New("worker identity not found")
	ErrInvalidIdentity      = errors.New("invalid worker identity")
	ErrChangeReasonRequired = errors.New("identity changes require a reason")
)

// Record is one Worker-owned assignment of a durable role and the task types it is
// allowed to take when IdentityMode is enabled. ClaimedAt never changes; UpdatedAt,
// ChangedBy, and Reason provide the audit trail for later role/type changes.
type Record struct {
	Actor     string   `yaml:"actor" json:"actor"`
	Role      string   `yaml:"role" json:"role"`
	Types     []string `yaml:"types" json:"types"`
	ClaimedAt string   `yaml:"claimed_at" json:"claimedAt"`
	UpdatedAt string   `yaml:"updated_at" json:"updatedAt"`
	ChangedBy string   `yaml:"changed_by" json:"changedBy"`
	Reason    string   `yaml:"reason,omitempty" json:"reason,omitempty"`
}

// Registry is the versioned store-local persisted envelope. Records are sorted by
// actor before every write for stable diffs and deterministic API output.
type Registry struct {
	Version int      `yaml:"version" json:"version"`
	Records []Record `yaml:"records" json:"records"`
}

// ClaimInput is deliberately actor-explicit so a human HTTP manager can set a chosen
// Worker while MCP's self-service method always fills Actor from its fixed connection.
type ClaimInput struct {
	Actor  string
	Role   string
	Types  []string
	Reason string
}

// Manager persists identities through Store.Write, sharing the same lock as task and
// lease mutations. It has no actor-authorization policy: callers decide who may set
// which Actor, while this package guarantees a valid, atomic registry transition.
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

// List returns a defensive, actor-sorted snapshot. Missing registries are a normal
// migration-free empty state for projects created before identity mode existed.
func (m *Manager) List() ([]Record, error) {
	registry, err := m.load()
	if err != nil {
		return nil, err
	}
	return cloneRecords(registry.Records), nil
}

// Get returns one exact canonical actor record.
func (m *Manager) Get(actor string) (Record, error) {
	if err := ValidateActor(actor); err != nil {
		return Record{}, err
	}
	registry, err := m.load()
	if err != nil {
		return Record{}, err
	}
	for _, record := range registry.Records {
		if record.Actor == actor {
			return cloneRecord(record), nil
		}
	}
	return Record{}, fmt.Errorf("%w: %s", ErrNotFound, actor)
}

// GetTx is the transaction-safe counterpart used by Service authorization hooks.
// Callers must already hold Store.Write; it never opens a nested lock.
func (m *Manager) GetTx(tx *store.WriteTx, actor string) (Record, error) {
	if err := ValidateActor(actor); err != nil {
		return Record{}, err
	}
	registry, err := m.loadTx(tx)
	if err != nil {
		return Record{}, err
	}
	for _, record := range registry.Records {
		if record.Actor == actor {
			return cloneRecord(record), nil
		}
	}
	return Record{}, fmt.Errorf("%w: %s", ErrNotFound, actor)
}

// ClaimOrChange creates a first identity or changes its role/type assignment. A first
// claim may omit Reason; every material role/type change must carry a non-empty reason.
// An exact idempotent retry returns the existing record without changing timestamps.
func (m *Manager) ClaimOrChange(ctx context.Context, changedBy string, input ClaimInput) (Record, error) {
	if m == nil || m.Store == nil {
		return Record{}, errors.New("identity manager has no store")
	}
	if err := ValidateActor(changedBy); err != nil {
		return Record{}, err
	}
	if err := validateClaimInput(input); err != nil {
		return Record{}, err
	}

	var out Record
	err := m.Store.Write(ctx, changedBy, "claim worker identity", func(tx *store.WriteTx) error {
		registry, err := m.loadTx(tx)
		if err != nil {
			return err
		}
		at := m.now().Format(time.RFC3339Nano)
		for index := range registry.Records {
			current := registry.Records[index]
			if current.Actor != input.Actor {
				continue
			}
			if current.Role == input.Role && slices.Equal(current.Types, input.Types) {
				out = cloneRecord(current)
				return nil
			}
			if strings.TrimSpace(input.Reason) == "" {
				return ErrChangeReasonRequired
			}
			current.Role = input.Role
			current.Types = slices.Clone(input.Types)
			current.UpdatedAt = at
			current.ChangedBy = changedBy
			current.Reason = input.Reason
			registry.Records[index] = current
			return m.writeTx(tx, registry, &out, current)
		}

		record := Record{
			Actor: input.Actor, Role: input.Role, Types: slices.Clone(input.Types),
			ClaimedAt: at, UpdatedAt: at, ChangedBy: changedBy, Reason: input.Reason,
		}
		registry.Records = append(registry.Records, record)
		return m.writeTx(tx, registry, &out, record)
	})
	if err != nil {
		return Record{}, err
	}
	return out, nil
}

func (m *Manager) writeTx(tx *store.WriteTx, registry Registry, out *Record, record Record) error {
	sort.Slice(registry.Records, func(i, j int) bool { return registry.Records[i].Actor < registry.Records[j].Actor })
	if err := validateRegistry(registry); err != nil {
		return err
	}
	encoded, err := yaml.Marshal(registry)
	if err != nil {
		return fmt.Errorf("%w: encode registry: %v", ErrInvalidIdentity, err)
	}
	if err := tx.WriteData(dataDir, registryName, encoded); err != nil {
		return err
	}
	*out = cloneRecord(record)
	return nil
}

func (m *Manager) load() (Registry, error) {
	if m == nil || m.Store == nil {
		return Registry{}, errors.New("identity manager has no store")
	}
	data, err := m.Store.ReadData(dataDir, registryName)
	if errors.Is(err, os.ErrNotExist) {
		return Registry{Version: RegistryVersion}, nil
	}
	if err != nil {
		return Registry{}, err
	}
	return decode(data)
}

func (m *Manager) loadTx(tx *store.WriteTx) (Registry, error) {
	if m == nil || m.Store == nil || tx == nil {
		return Registry{}, errors.New("identity manager has no store transaction")
	}
	data, err := tx.ReadData(dataDir, registryName)
	if errors.Is(err, os.ErrNotExist) {
		return Registry{Version: RegistryVersion}, nil
	}
	if err != nil {
		return Registry{}, err
	}
	return decode(data)
}

func decode(data []byte) (Registry, error) {
	if len(data) == 0 || len(data) > maxRegistryBytes || !utf8.Valid(data) {
		return Registry{}, fmt.Errorf("%w: invalid encoded registry", ErrInvalidIdentity)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var registry Registry
	if err := decoder.Decode(&registry); err != nil {
		return Registry{}, fmt.Errorf("%w: parse registry: %v", ErrInvalidIdentity, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Registry{}, fmt.Errorf("%w: multiple YAML documents", ErrInvalidIdentity)
		}
		return Registry{}, fmt.Errorf("%w: parse registry: %v", ErrInvalidIdentity, err)
	}
	if err := validateRegistry(registry); err != nil {
		return Registry{}, err
	}
	return registry, nil
}

func validateRegistry(registry Registry) error {
	if registry.Version != RegistryVersion {
		return fmt.Errorf("%w: unsupported registry version %d", ErrInvalidIdentity, registry.Version)
	}
	if len(registry.Records) > maxRecords {
		return fmt.Errorf("%w: too many records", ErrInvalidIdentity)
	}
	seen := make(map[string]struct{}, len(registry.Records))
	for _, record := range registry.Records {
		if err := validateRecord(record); err != nil {
			return err
		}
		if _, exists := seen[record.Actor]; exists {
			return fmt.Errorf("%w: duplicate actor %q", ErrInvalidIdentity, record.Actor)
		}
		seen[record.Actor] = struct{}{}
	}
	return nil
}

func validateRecord(record Record) error {
	if err := ValidateActor(record.Actor); err != nil {
		return err
	}
	if err := ValidateRole(record.Role); err != nil {
		return err
	}
	if err := ValidateTypes(record.Types); err != nil {
		return err
	}
	if err := validateTimestamp(record.ClaimedAt); err != nil {
		return err
	}
	if err := validateTimestamp(record.UpdatedAt); err != nil {
		return err
	}
	claimed, _ := time.Parse(time.RFC3339Nano, record.ClaimedAt)
	updated, _ := time.Parse(time.RFC3339Nano, record.UpdatedAt)
	if updated.Before(claimed) {
		return fmt.Errorf("%w: updated_at precedes claimed_at", ErrInvalidIdentity)
	}
	if err := ValidateActor(record.ChangedBy); err != nil {
		return fmt.Errorf("%w: changed_by: %v", ErrInvalidIdentity, err)
	}
	return ValidateReason(record.Reason)
}

func validateClaimInput(input ClaimInput) error {
	if err := ValidateActor(input.Actor); err != nil {
		return err
	}
	if err := ValidateRole(input.Role); err != nil {
		return err
	}
	if err := ValidateTypes(input.Types); err != nil {
		return err
	}
	return ValidateReason(input.Reason)
}

// ValidateActor accepts Carbon's canonical principal grammar. Worker identities are
// normally agent:* records, but retaining human/system grammar here keeps audit fields
// valid and lets the service apply its stricter Worker policy at one boundary.
func ValidateActor(actor string) error {
	if actor == "" || !utf8.ValidString(actor) || strings.TrimSpace(actor) != actor || utf8.RuneCountInString(actor) > maxActorRunes {
		return fmt.Errorf("%w: actor", ErrInvalidIdentity)
	}
	kind, name, ok := strings.Cut(actor, ":")
	if !ok || name == "" || strings.Contains(name, ":") || (kind != "agent" && kind != "human" && kind != "system") {
		return fmt.Errorf("%w: actor %q", ErrInvalidIdentity, actor)
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("%w: actor %q", ErrInvalidIdentity, actor)
	}
	return nil
}

// IsAgent is intentionally strict: malformed values never receive the agent-only
// identity guard or management privilege through a loose prefix comparison.
func IsAgent(actor string) bool {
	return ValidateActor(actor) == nil && strings.HasPrefix(actor, "agent:")
}

func IsHuman(actor string) bool {
	return ValidateActor(actor) == nil && strings.HasPrefix(actor, "human:")
}

// IsSystem recognizes the canonical system principal form. System actors are not
// Workers themselves, but Service authorization can treat them as administrators
// without relying on a loose prefix check.
func IsSystem(actor string) bool {
	return ValidateActor(actor) == nil && strings.HasPrefix(actor, "system:")
}

func ValidateRole(role string) error {
	if role == "" || !utf8.ValidString(role) || strings.TrimSpace(role) != role || utf8.RuneCountInString(role) > maxRoleRunes {
		return fmt.Errorf("%w: role", ErrInvalidIdentity)
	}
	for _, r := range role {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: role", ErrInvalidIdentity)
		}
	}
	return nil
}

// ValidateTypes requires at least one task type, rejects duplicates, and shares the
// task catalog's stable key grammar. The service additionally checks that each key is
// currently enabled in the selected project's config catalog.
func ValidateTypes(types []string) error {
	if len(types) == 0 || len(types) > maxTypes {
		return fmt.Errorf("%w: types must contain 1..%d values", ErrInvalidIdentity, maxTypes)
	}
	seen := make(map[string]struct{}, len(types))
	for _, typ := range types {
		if err := tasktypes.ValidateKey(typ); err != nil {
			return fmt.Errorf("%w: type %q: %v", ErrInvalidIdentity, typ, err)
		}
		if _, exists := seen[typ]; exists {
			return fmt.Errorf("%w: duplicate type %q", ErrInvalidIdentity, typ)
		}
		seen[typ] = struct{}{}
	}
	return nil
}

func ValidateReason(reason string) error {
	if !utf8.ValidString(reason) || strings.TrimSpace(reason) != reason || utf8.RuneCountInString(reason) > maxReasonRunes {
		return fmt.Errorf("%w: reason", ErrInvalidIdentity)
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: reason", ErrInvalidIdentity)
		}
	}
	return nil
}

func validateTimestamp(value string) error {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return fmt.Errorf("%w: timestamp", ErrInvalidIdentity)
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return fmt.Errorf("%w: timestamp", ErrInvalidIdentity)
	}
	return nil
}

func cloneRecord(record Record) Record {
	record.Types = slices.Clone(record.Types)
	return record
}

func cloneRecords(records []Record) []Record {
	out := make([]Record, len(records))
	for i, record := range records {
		out[i] = cloneRecord(record)
	}
	return out
}
