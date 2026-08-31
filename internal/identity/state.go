package identity

// This file owns the v2 canonical identity journal. registry.yaml deliberately
// remains the v1 compatibility projection: older Carbon binaries decode it with
// yaml.KnownFields(true), so placing `roles` in that file would make a downgrade
// fail loudly. The journal is authoritative for new Carbon versions and holds the
// identity change, immutable audit, and optional automatic process Incident in one
// atomic metadata file.

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

	"carbon/internal/store"

	"gopkg.in/yaml.v3"
)

const (
	// legacyStateName is the short-lived v2-alpha global canonical journal. New
	// Carbon project managers never read it in a shared cluster: it has no project
	// discriminator, so importing it there would recreate the exact A/B leakage
	// this split prevents.
	legacyStateName       = "state.yaml"
	projectStateDir       = "identity-projects"
	stateVersion          = 1
	maxStateBytes         = 1024 << 10
	maxAudits             = 8192
	maxAutoIncidents      = 4096
	maxRoles              = 16
	maxProjectIDRunes     = 128
	maxIncidentTitleRunes = 240
	maxIncidentBodyRunes  = 4096
)

// canonicalRecord is never written to registry.yaml. It carries the actual
// composable role assignment while Record.Role is retained only as a legacy alias.
type canonicalRecord struct {
	Actor     string   `yaml:"actor"`
	Roles     []string `yaml:"roles"`
	Role      string   `yaml:"role"`
	Types     []string `yaml:"types"`
	ClaimedAt string   `yaml:"claimed_at"`
	UpdatedAt string   `yaml:"updated_at"`
	ChangedBy string   `yaml:"changed_by"`
	Reason    string   `yaml:"reason,omitempty"`
}

type state struct {
	Version int `yaml:"version"`
	// ProjectID is required in every new project-scoped canonical file. It makes
	// a copied/misnamed sidecar fail closed rather than exposing another project's
	// Worker records or automatic identity Incidents.
	ProjectID string `yaml:"project_id,omitempty"`
	// LegacyRegistryFingerprint is a content hash baseline, not an mtime. It is
	// populated only for legacy/standalone projection files and makes an old binary
	// edit visible as a conflict instead of silently overwriting it.
	LegacyRegistryFingerprint string            `yaml:"legacy_registry_fingerprint,omitempty"`
	Records                   []canonicalRecord `yaml:"records"`
	Audits                    []Audit           `yaml:"audits,omitempty"`
	AutoIncidents             []AutoIncident    `yaml:"auto_incidents,omitempty"`
}

func (m *Manager) loadState() (state, error) {
	if m == nil || m.Store == nil {
		return state{}, errors.New("identity manager has no store")
	}
	dir, name, err := m.stateLocation()
	if err != nil {
		return state{}, err
	}
	data, err := m.Store.ReadData(dir, name)
	if errors.Is(err, os.ErrNotExist) {
		return m.initialState()
	}
	if err != nil {
		return state{}, err
	}
	value, err := decodeState(data)
	if err != nil {
		return state{}, err
	}
	if err := m.validateLoadedState(value); err != nil {
		return state{}, err
	}
	return value, nil
}

func (m *Manager) loadStateTx(tx *store.WriteTx) (state, error) {
	if m == nil || m.Store == nil || tx == nil {
		return state{}, errors.New("identity manager has no store transaction")
	}
	dir, name, err := m.stateLocation()
	if err != nil {
		return state{}, err
	}
	data, err := tx.ReadData(dir, name)
	if errors.Is(err, os.ErrNotExist) {
		return m.initialStateTx(tx)
	}
	if err != nil {
		return state{}, err
	}
	value, err := decodeState(data)
	if err != nil {
		return state{}, err
	}
	if err := m.validateLoadedStateTx(tx, value); err != nil {
		return state{}, err
	}
	return value, nil
}

func (m *Manager) stateLocation() (string, string, error) {
	if m == nil {
		return "", "", errors.New("identity manager is nil")
	}
	if m.ProjectID == "" {
		return dataDir, legacyStateName, nil
	}
	if err := validateProjectID(m.ProjectID); err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrProjectRequired, err)
	}
	return projectStateDir, m.ProjectID + ".yaml", nil
}

func (m *Manager) initialState() (state, error) {
	if m.ProjectID == "" {
		return m.projectLegacyRegistry()
	}
	if !m.LegacyProjection {
		return state{Version: stateVersion, ProjectID: m.ProjectID}, nil
	}
	return m.projectLegacyRegistry()
}

func (m *Manager) initialStateTx(tx *store.WriteTx) (state, error) {
	if m.ProjectID == "" || m.LegacyProjection {
		return m.projectLegacyRegistryTx(tx)
	}
	return state{Version: stateVersion, ProjectID: m.ProjectID}, nil
}

func (m *Manager) projectLegacyRegistry() (state, error) {
	registry, fingerprint, err := m.readLegacyRegistry()
	if err != nil {
		return state{}, err
	}
	return m.stateFromRegistry(registry, fingerprint)
}

func (m *Manager) projectLegacyRegistryTx(tx *store.WriteTx) (state, error) {
	registry, fingerprint, err := m.readLegacyRegistryTx(tx)
	if err != nil {
		return state{}, err
	}
	return m.stateFromRegistry(registry, fingerprint)
}

func (m *Manager) stateFromRegistry(registry Registry, fingerprint string) (state, error) {
	out := state{Version: stateVersion, ProjectID: m.ProjectID, Records: make([]canonicalRecord, 0, len(registry.Records))}
	if m.ProjectID != "" && m.LegacyProjection {
		out.LegacyRegistryFingerprint = fingerprint
	}
	for _, legacy := range registry.Records {
		roles, primary, err := normalizeRoles(nil, legacy.Role)
		if err != nil {
			return state{}, err
		}
		out.Records = append(out.Records, canonicalRecord{
			Actor: legacy.Actor, Roles: roles, Role: primary, Types: slices.Clone(legacy.Types),
			ClaimedAt: legacy.ClaimedAt, UpdatedAt: legacy.UpdatedAt, ChangedBy: legacy.ChangedBy, Reason: legacy.Reason,
		})
	}
	if err := m.validateStateForWrite(out); err != nil {
		return state{}, err
	}
	return out, nil
}

func (m *Manager) readLegacyRegistry() (Registry, string, error) {
	if m == nil || m.Store == nil {
		return Registry{}, "", errors.New("identity manager has no store")
	}
	raw, err := m.Store.ReadData(dataDir, registryName)
	if errors.Is(err, os.ErrNotExist) {
		return Registry{Version: RegistryVersion}, legacyRegistryFingerprint(nil, false), nil
	}
	if err != nil {
		return Registry{}, "", err
	}
	registry, err := decode(raw)
	if err != nil {
		return Registry{}, "", err
	}
	return registry, legacyRegistryFingerprint(raw, true), nil
}

func (m *Manager) readLegacyRegistryTx(tx *store.WriteTx) (Registry, string, error) {
	if m == nil || m.Store == nil || tx == nil {
		return Registry{}, "", errors.New("identity manager has no store transaction")
	}
	raw, err := tx.ReadData(dataDir, registryName)
	if errors.Is(err, os.ErrNotExist) {
		return Registry{Version: RegistryVersion}, legacyRegistryFingerprint(nil, false), nil
	}
	if err != nil {
		return Registry{}, "", err
	}
	registry, err := decode(raw)
	if err != nil {
		return Registry{}, "", err
	}
	return registry, legacyRegistryFingerprint(raw, true), nil
}

func legacyRegistryFingerprint(raw []byte, present bool) string {
	hash := sha256.New()
	if present {
		_, _ = hash.Write([]byte("present\\x00"))
		_, _ = hash.Write(raw)
	} else {
		_, _ = hash.Write([]byte("missing\\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func decodeState(data []byte) (state, error) {
	if len(data) == 0 || len(data) > maxStateBytes || !utf8.Valid(data) {
		return state{}, fmt.Errorf("%w: invalid encoded identity state", ErrInvalidIdentity)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var out state
	if err := decoder.Decode(&out); err != nil {
		return state{}, fmt.Errorf("%w: parse identity state: %v", ErrInvalidIdentity, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return state{}, fmt.Errorf("%w: multiple identity state documents", ErrInvalidIdentity)
		}
		return state{}, fmt.Errorf("%w: parse identity state: %v", ErrInvalidIdentity, err)
	}
	if err := validateState(out); err != nil {
		return state{}, err
	}
	return out, nil
}

func (m *Manager) validateLoadedState(value state) error {
	if err := m.validateStateForWrite(value); err != nil {
		return err
	}
	if m.ProjectID != "" && m.LegacyProjection {
		_, fingerprint, err := m.readLegacyRegistry()
		if err != nil {
			return err
		}
		if fingerprint != value.LegacyRegistryFingerprint {
			return fmt.Errorf("%w: registry.yaml changed outside canonical identity state", ErrLegacyProjectionConflict)
		}
	}
	return nil
}

func (m *Manager) validateLoadedStateTx(tx *store.WriteTx, value state) error {
	if err := m.validateStateForWrite(value); err != nil {
		return err
	}
	if m.ProjectID != "" && m.LegacyProjection {
		_, fingerprint, err := m.readLegacyRegistryTx(tx)
		if err != nil {
			return err
		}
		if fingerprint != value.LegacyRegistryFingerprint {
			return fmt.Errorf("%w: registry.yaml changed outside canonical identity state", ErrLegacyProjectionConflict)
		}
	}
	return nil
}

// validateStateForWrite checks the manager boundary without consulting the live
// legacy file. The live fingerprint comparison happens only when state is loaded;
// a mutation intentionally updates its stored baseline before its best-effort
// projection write so a failed projection cannot be silently overwritten later.
func (m *Manager) validateStateForWrite(value state) error {
	if err := validateState(value); err != nil {
		return err
	}
	if m == nil {
		return errors.New("identity manager is nil")
	}
	if m.ProjectID == "" {
		if value.ProjectID != "" || value.LegacyRegistryFingerprint != "" {
			return fmt.Errorf("%w: legacy state cannot carry project scope", ErrInvalidIdentity)
		}
		return nil
	}
	if value.ProjectID != m.ProjectID {
		return fmt.Errorf("%w: state belongs to project %q, manager is %q", ErrInvalidIdentity, value.ProjectID, m.ProjectID)
	}
	if m.LegacyProjection {
		if !validFingerprint(value.LegacyRegistryFingerprint) {
			return fmt.Errorf("%w: missing legacy projection fingerprint", ErrInvalidIdentity)
		}
	} else if value.LegacyRegistryFingerprint != "" {
		return fmt.Errorf("%w: shared project must not carry a legacy projection", ErrInvalidIdentity)
	}
	for _, incident := range value.AutoIncidents {
		if incident.ProjectID != m.ProjectID {
			return fmt.Errorf("%w: automatic incident crossed project scope", ErrInvalidIdentity)
		}
	}
	return nil
}

func validFingerprint(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateState(value state) error {
	if value.Version != stateVersion {
		return fmt.Errorf("%w: unsupported identity state version %d", ErrInvalidIdentity, value.Version)
	}
	if len(value.Records) > maxRecords || len(value.Audits) > maxAudits || len(value.AutoIncidents) > maxAutoIncidents {
		return fmt.Errorf("%w: too many identity state records", ErrInvalidIdentity)
	}
	if value.ProjectID != "" {
		if err := validateProjectID(value.ProjectID); err != nil {
			return err
		}
	}
	if value.LegacyRegistryFingerprint != "" && !validFingerprint(value.LegacyRegistryFingerprint) {
		return fmt.Errorf("%w: legacy projection fingerprint", ErrInvalidIdentity)
	}
	actors := make(map[string]struct{}, len(value.Records))
	for _, record := range value.Records {
		if err := validateCanonicalRecord(record); err != nil {
			return err
		}
		if _, found := actors[record.Actor]; found {
			return fmt.Errorf("%w: duplicate actor %q", ErrInvalidIdentity, record.Actor)
		}
		actors[record.Actor] = struct{}{}
	}
	audits := make(map[string]struct{}, len(value.Audits))
	for _, audit := range value.Audits {
		if err := validateAudit(audit); err != nil {
			return err
		}
		if _, found := audits[audit.ID]; found {
			return fmt.Errorf("%w: duplicate identity audit %q", ErrInvalidIdentity, audit.ID)
		}
		audits[audit.ID] = struct{}{}
	}
	incidents := make(map[string]struct{}, len(value.AutoIncidents))
	for _, incident := range value.AutoIncidents {
		if err := validateAutoIncident(incident); err != nil {
			return err
		}
		if _, found := incidents[incident.ID]; found {
			return fmt.Errorf("%w: duplicate automatic identity incident %q", ErrInvalidIdentity, incident.ID)
		}
		if _, found := audits[incident.RelatedAuditID]; !found {
			return fmt.Errorf("%w: automatic incident %q has unknown audit", ErrInvalidIdentity, incident.ID)
		}
		incidents[incident.ID] = struct{}{}
	}
	return nil
}

func validateCanonicalRecord(record canonicalRecord) error {
	if err := ValidateActor(record.Actor); err != nil {
		return err
	}
	roles, primary, err := normalizeRoles(record.Roles, record.Role)
	if err != nil || !slices.Equal(roles, record.Roles) || primary != record.Role {
		return fmt.Errorf("%w: roles", ErrInvalidIdentity)
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

func validateAudit(audit Audit) error {
	if err := validateOpaqueID(audit.ID, "wia_"); err != nil {
		return err
	}
	if err := ValidateActor(audit.Actor); err != nil {
		return err
	}
	if audit.Operation != "claimed" && audit.Operation != "changed" {
		return fmt.Errorf("%w: audit operation", ErrInvalidIdentity)
	}
	if err := validateAuditRoles(audit.AfterRoles); err != nil {
		return err
	}
	if len(audit.BeforeRoles) > 0 {
		if err := validateAuditRoles(audit.BeforeRoles); err != nil {
			return err
		}
	}
	if err := ValidateTypes(audit.AfterTypes); err != nil {
		return err
	}
	if len(audit.BeforeTypes) > 0 {
		if err := ValidateTypes(audit.BeforeTypes); err != nil {
			return err
		}
	}
	if err := ValidateActor(audit.ChangedBy); err != nil {
		return err
	}
	if err := ValidateReason(audit.Reason); err != nil {
		return err
	}
	if err := validateTimestamp(audit.At); err != nil {
		return err
	}
	if audit.RelatedIncidentID != "" {
		return validateOpaqueID(audit.RelatedIncidentID, "inc_")
	}
	return nil
}

func validateAutoIncident(incident AutoIncident) error {
	if err := validateOpaqueID(incident.ID, "inc_"); err != nil {
		return err
	}
	if err := validateProjectID(incident.ProjectID); err != nil {
		return err
	}
	if err := validateText(incident.Title, maxIncidentTitleRunes, false); err != nil {
		return err
	}
	if err := validateText(incident.Body, maxIncidentBodyRunes, true); err != nil {
		return err
	}
	if incident.Severity != "info" || !validAutoIncidentStatus(incident.Status) || incident.Origin != "identity_change" || incident.Kind != "identity_change" || len(incident.RelatedTaskIDs) != 0 {
		return fmt.Errorf("%w: automatic incident shape", ErrInvalidIdentity)
	}
	if err := ValidateActor(incident.CreatedBy); err != nil {
		return err
	}
	if err := validateTimestamp(incident.CreatedAt); err != nil {
		return err
	}
	if err := validateTimestamp(incident.UpdatedAt); err != nil {
		return err
	}
	if incident.UpdatedAt < incident.CreatedAt {
		return fmt.Errorf("%w: automatic incident time", ErrInvalidIdentity)
	}
	return validateOpaqueID(incident.RelatedAuditID, "wia_")
}

func validAutoIncidentStatus(status string) bool {
	return status == "open" || status == "investigating" || status == "resolved" || status == "closed"
}

func validateOpaqueID(id, prefix string) error {
	if !strings.HasPrefix(id, prefix) || len(id) != len(prefix)+24 {
		return fmt.Errorf("%w: id", ErrInvalidIdentity)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(id, prefix)); err != nil {
		return fmt.Errorf("%w: id", ErrInvalidIdentity)
	}
	return nil
}

func validateProjectID(projectID string) error {
	if projectID == "" || !utf8.ValidString(projectID) || strings.TrimSpace(projectID) != projectID || utf8.RuneCountInString(projectID) > maxProjectIDRunes {
		return fmt.Errorf("%w: project id", ErrInvalidIdentity)
	}
	for _, r := range projectID {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("%w: project id", ErrInvalidIdentity)
	}
	return nil
}

func validateText(value string, maxRunes int, allowEmpty bool) error {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value || (!allowEmpty && value == "") || utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%w: text", ErrInvalidIdentity)
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' {
			return fmt.Errorf("%w: text", ErrInvalidIdentity)
		}
	}
	return nil
}

// ClaimOrChangeWithOptions is the durable one-record public mutation. Direct
// callers default to no automatic process Incident for backward compatibility;
// Service uses ClaimOrChangeTx so configuration is read under the same write lock.
func (m *Manager) ClaimOrChangeWithOptions(ctx context.Context, changedBy string, input ClaimInput, options ChangeOptions) (Record, error) {
	if m == nil || m.Store == nil {
		return Record{}, errors.New("identity manager has no store")
	}
	if err := ValidateActor(changedBy); err != nil {
		return Record{}, err
	}
	var out Record
	err := m.Store.Write(ctx, changedBy, "claim worker identity", func(tx *store.WriteTx) error {
		var err error
		out, _, err = m.ClaimOrChangeTx(tx, changedBy, input, options)
		return err
	})
	return out, err
}

// ClaimOrChangeTx is the Service-facing atomic primitive. The canonical state file
// contains record/audit/automatic-Incident together. registry.yaml is written only
// as a compatibility projection and a projection write failure is deliberately not
// surfaced after the canonical commit. Its pinned fingerprint then makes every later
// identity read or mutation fail closed until an explicit repair/migration reconciles
// the old projection; no retry ever rebuilds or silently overwrites it.
func (m *Manager) ClaimOrChangeTx(tx *store.WriteTx, changedBy string, input ClaimInput, options ChangeOptions) (Record, bool, error) {
	if m == nil || m.Store == nil || tx == nil {
		return Record{}, false, errors.New("identity manager has no store transaction")
	}
	if err := ValidateActor(changedBy); err != nil {
		return Record{}, false, err
	}
	if err := validateClaimInput(input); err != nil {
		return Record{}, false, err
	}
	roles, primary, err := normalizeClaimRoles(input.Roles, input.Role)
	if err != nil {
		return Record{}, false, err
	}
	input.Roles, input.Role = roles, primary
	if m.ProjectID != "" {
		if options.ProjectID != m.ProjectID {
			return Record{}, false, fmt.Errorf("%w: operation project %q does not match manager project %q", ErrInvalidIdentity, options.ProjectID, m.ProjectID)
		}
	} else if !options.NoTraceMode {
		return Record{}, false, ErrProjectRequired
	}

	value, err := m.loadStateTx(tx)
	if err != nil {
		return Record{}, false, err
	}
	for index := range value.Records {
		current := value.Records[index]
		if current.Actor != input.Actor {
			continue
		}
		if slices.Equal(current.Roles, input.Roles) && slices.Equal(current.Types, input.Types) {
			out := recordFromCanonical(current)
			return out, false, nil
		}
		if strings.TrimSpace(input.Reason) == "" {
			return Record{}, false, ErrChangeReasonRequired
		}
		at := m.now().Format(time.RFC3339Nano)
		next := current
		next.Roles, next.Role, next.Types = slices.Clone(input.Roles), input.Role, slices.Clone(input.Types)
		next.UpdatedAt, next.ChangedBy, next.Reason = at, changedBy, input.Reason
		return m.appendChangeTx(tx, value, index, &current, next, changedBy, input, options, "changed", at)
	}

	at := m.now().Format(time.RFC3339Nano)
	next := canonicalRecord{Actor: input.Actor, Roles: slices.Clone(input.Roles), Role: input.Role, Types: slices.Clone(input.Types), ClaimedAt: at, UpdatedAt: at, ChangedBy: changedBy, Reason: input.Reason}
	value.Records = append(value.Records, next)
	return m.appendChangeTx(tx, value, len(value.Records)-1, nil, next, changedBy, input, options, "claimed", at)
}

func (m *Manager) appendChangeTx(tx *store.WriteTx, value state, index int, before *canonicalRecord, next canonicalRecord, changedBy string, input ClaimInput, options ChangeOptions, operation, at string) (Record, bool, error) {
	if index < 0 || index >= len(value.Records) {
		return Record{}, false, fmt.Errorf("%w: identity state index", ErrInvalidIdentity)
	}
	value.Records[index] = next
	auditID, err := mintIdentityID("wia_")
	if err != nil {
		return Record{}, false, err
	}
	audit := Audit{ID: auditID, Actor: input.Actor, Operation: operation, AfterRoles: slices.Clone(next.Roles), AfterTypes: slices.Clone(next.Types), ChangedBy: changedBy, Reason: input.Reason, At: at}
	if before != nil {
		audit.BeforeRoles, audit.BeforeTypes = slices.Clone(before.Roles), slices.Clone(before.Types)
	}
	if !options.NoTraceMode {
		incidentID, err := mintIdentityID("inc_")
		if err != nil {
			return Record{}, false, err
		}
		audit.RelatedIncidentID = incidentID
		value.AutoIncidents = append(value.AutoIncidents, AutoIncident{
			ID: incidentID, ProjectID: options.ProjectID,
			Title: "Worker 身份已" + map[string]string{"claimed": "认领", "changed": "变更"}[operation],
			Body:  fmt.Sprintf("%s 的身份由 %s %s。", input.Actor, changedBy, map[string]string{"claimed": "认领", "changed": "调整"}[operation]),
			Kind:  "identity_change", Severity: "info", Status: "open", CreatedBy: changedBy, CreatedAt: at, UpdatedAt: at,
			Origin: "identity_change", RelatedAuditID: auditID,
		})
	}
	value.Audits = append(value.Audits, audit)
	var projection []byte
	if m.LegacyProjection {
		// The old registry is a standalone/legacy-only projection. Build and
		// validate it before the canonical write, then pin the exact content
		// fingerprint in canonical state. If the subsequent projection write is
		// interrupted, later calls fail closed instead of overwriting old-binary
		// changes or duplicating this audit on retry.
		projection, err = legacyProjectionBytes(value)
		if err != nil {
			return Record{}, false, err
		}
		if m.ProjectID != "" {
			value.LegacyRegistryFingerprint = legacyRegistryFingerprint(projection, true)
		}
	}
	if err := m.writeCanonicalStateTx(tx, value); err != nil {
		return Record{}, false, err
	}
	if m.LegacyProjection {
		// Canonical state is the retry-safe authority. Deliberately do not return a
		// post-commit projection error; the pinned hash forces a visible conflict on
		// the next operation rather than a hidden overwrite.
		_ = tx.WriteData(dataDir, registryName, projection)
	}
	return recordFromCanonical(next), true, nil
}

func (m *Manager) writeCanonicalStateTx(tx *store.WriteTx, value state) error {
	if err := m.validateStateForWrite(value); err != nil {
		return err
	}
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: encode identity state: %v", ErrInvalidIdentity, err)
	}
	dir, name, err := m.stateLocation()
	if err != nil {
		return err
	}
	return tx.WriteData(dir, name, encoded)
}

func legacyProjectionBytes(value state) ([]byte, error) {
	registry := Registry{Version: RegistryVersion, Records: make([]Record, 0, len(value.Records))}
	for _, item := range value.Records {
		registry.Records = append(registry.Records, Record{Actor: item.Actor, Role: item.Role, Types: slices.Clone(item.Types), ClaimedAt: item.ClaimedAt, UpdatedAt: item.UpdatedAt, ChangedBy: item.ChangedBy, Reason: item.Reason})
	}
	sort.Slice(registry.Records, func(i, j int) bool { return registry.Records[i].Actor < registry.Records[j].Actor })
	if err := validateRegistry(registry); err != nil {
		return nil, err
	}
	encoded, err := yaml.Marshal(registry)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func mintIdentityID(prefix string) (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("identity random id: %w", err)
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}

func recordFromCanonical(value canonicalRecord) Record {
	return Record{Actor: value.Actor, Roles: slices.Clone(value.Roles), Role: value.Role, Types: slices.Clone(value.Types), ClaimedAt: value.ClaimedAt, UpdatedAt: value.UpdatedAt, ChangedBy: value.ChangedBy, Reason: value.Reason}
}

func canonicalFromRecord(value Record) canonicalRecord {
	roles, primary, _ := normalizeRoles(value.Roles, value.Role)
	return canonicalRecord{Actor: value.Actor, Roles: roles, Role: primary, Types: slices.Clone(value.Types), ClaimedAt: value.ClaimedAt, UpdatedAt: value.UpdatedAt, ChangedBy: value.ChangedBy, Reason: value.Reason}
}

// ListAudit returns a defensive append-only snapshot. Legacy registries naturally
// return no audits until their next material identity operation creates state.yaml.
func (m *Manager) ListAudit() ([]Audit, error) {
	value, err := m.loadState()
	if err != nil {
		return nil, err
	}
	out := make([]Audit, len(value.Audits))
	for i, item := range value.Audits {
		out[i] = cloneAudit(item)
	}
	return out, nil
}

func (m *Manager) ListAutoIncidents(projectID string) ([]AutoIncident, error) {
	if err := validateProjectID(projectID); err != nil {
		return nil, err
	}
	if m.ProjectID != "" && projectID != m.ProjectID {
		return nil, fmt.Errorf("%w: automatic incidents belong to project %s", ErrNotFound, m.ProjectID)
	}
	value, err := m.loadState()
	if err != nil {
		return nil, err
	}
	out := make([]AutoIncident, 0)
	for _, item := range value.AutoIncidents {
		if item.ProjectID == projectID {
			out = append(out, item)
		}
	}
	return out, nil
}

func (m *Manager) GetAutoIncident(id string) (AutoIncident, error) {
	if err := validateOpaqueID(id, "inc_"); err != nil {
		return AutoIncident{}, err
	}
	value, err := m.loadState()
	if err != nil {
		return AutoIncident{}, err
	}
	for _, item := range value.AutoIncidents {
		if item.ID == id && (m.ProjectID == "" || item.ProjectID == m.ProjectID) {
			return item, nil
		}
	}
	return AutoIncident{}, fmt.Errorf("%w: automatic incident %s", ErrNotFound, id)
}

// UpdateAutoIncident only changes its lifecycle fields. It is intentionally exposed
// for the incident manager so the process model can manage auto-created Incidents
// without coupling identity to the incident package.
func (m *Manager) UpdateAutoIncident(ctx context.Context, actor, id, status string) (AutoIncident, error) {
	var out AutoIncident
	err := m.Store.Write(ctx, actor, "update automatic identity incident", func(tx *store.WriteTx) error {
		var err error
		out, _, err = m.UpdateAutoIncidentTx(tx, actor, id, status)
		return err
	})
	return out, err
}

// UpdateAutoIncidentTx is the transaction-safe lifecycle primitive used by the
// Incident adapter when an automatic identity Incident also needs a recoverable
// project event-ledger record. changed reports whether source state changed.
func (m *Manager) UpdateAutoIncidentTx(tx *store.WriteTx, actor, id, status string) (out AutoIncident, changed bool, err error) {
	return m.UpdateAutoIncidentTxWithBeforeWrite(tx, actor, id, status, nil)
}

// UpdateAutoIncidentTxWithBeforeWrite exposes the smallest ordering hook needed
// by the project event ledger: persist its recovery intent after the new source
// values are known but before this canonical identity journal is written.
func (m *Manager) UpdateAutoIncidentTxWithBeforeWrite(tx *store.WriteTx, actor, id, status string, beforeWrite func(AutoIncident) error) (out AutoIncident, changed bool, err error) {
	if m == nil || m.Store == nil || tx == nil {
		return AutoIncident{}, false, errors.New("identity manager has no store transaction")
	}
	if err := ValidateActor(actor); err != nil {
		return AutoIncident{}, false, err
	}
	if err := validateOpaqueID(id, "inc_"); err != nil {
		return AutoIncident{}, false, err
	}
	if !validAutoIncidentStatus(status) {
		return AutoIncident{}, false, fmt.Errorf("%w: incident status", ErrInvalidIdentity)
	}
	value, err := m.loadStateTx(tx)
	if err != nil {
		return AutoIncident{}, false, err
	}
	for i := range value.AutoIncidents {
		if value.AutoIncidents[i].ID != id {
			continue
		}
		if value.AutoIncidents[i].Status == status {
			return value.AutoIncidents[i], false, nil
		}
		value.AutoIncidents[i].Status = status
		value.AutoIncidents[i].UpdatedAt = m.now().Format(time.RFC3339Nano)
		if beforeWrite != nil {
			if err := beforeWrite(value.AutoIncidents[i]); err != nil {
				return AutoIncident{}, false, err
			}
		}
		if err := m.writeCanonicalStateTx(tx, value); err != nil {
			return AutoIncident{}, false, err
		}
		return value.AutoIncidents[i], true, nil
	}
	return AutoIncident{}, false, fmt.Errorf("%w: automatic incident %s", ErrNotFound, id)
}

func cloneAudit(value Audit) Audit {
	value.BeforeRoles = slices.Clone(value.BeforeRoles)
	value.BeforeTypes = slices.Clone(value.BeforeTypes)
	value.AfterRoles = slices.Clone(value.AfterRoles)
	value.AfterTypes = slices.Clone(value.AfterTypes)
	return value
}

var legacyRoleKeys = map[string]string{
	"架构师": "architect", "任务发布者": "task_publisher", "前端": "frontend", "后端": "backend", "审核者": "reviewer", "研究者": "researcher", "规划者": "planner",
}

// normalizeClaimRoles is deliberately stricter than the canonical-storage
// normalizer below. New API requests must choose roles[] or the deprecated role
// alias, never both; canonical records necessarily carry both because Role is the
// derived first-role projection for legacy clients.
func normalizeClaimRoles(roles []string, legacyRole string) ([]string, string, error) {
	// A non-nil empty slice means the transport explicitly supplied `roles: []`.
	// Treat that as choosing the new field too, so callers can never smuggle a
	// deprecated `role` through a deliberately empty roles array.
	if roles != nil && strings.TrimSpace(legacyRole) != "" {
		return nil, "", fmt.Errorf("%w: roles and role are mutually exclusive", ErrInvalidIdentity)
	}
	return normalizeRoles(roles, legacyRole)
}

func normalizeRoles(roles []string, legacyRole string) ([]string, string, error) {
	if len(roles) == 0 {
		if err := ValidateRole(legacyRole); err != nil {
			return nil, "", err
		}
		key := legacyRoleKeys[legacyRole]
		if key == "" {
			key = legacyRole
		}
		return []string{key}, key, nil
	}
	if len(roles) > maxRoles {
		return nil, "", fmt.Errorf("%w: roles", ErrInvalidIdentity)
	}
	out := make([]string, 0, len(roles))
	seen := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		if mapped := legacyRoleKeys[role]; mapped != "" {
			role = mapped
		}
		if err := validateRoleKey(role); err != nil {
			// An old registry could contain a pre-v2 display role that has no
			// machine-key mapping. Preserve it as a legacy canonical value so a
			// downgrade/upgrade never destroys the record; new Roles input remains
			// machine-key validated when no legacy primary alias is supplied.
			if strings.TrimSpace(legacyRole) == "" || role != legacyRole || ValidateRole(role) != nil {
				return nil, "", err
			}
		}
		if _, found := seen[role]; found {
			return nil, "", fmt.Errorf("%w: duplicate role %q", ErrInvalidIdentity, role)
		}
		seen[role] = struct{}{}
		out = append(out, role)
	}
	primary := out[0]
	if legacyRole != "" {
		if err := ValidateRole(legacyRole); err != nil {
			return nil, "", err
		}
		alias := legacyRoleKeys[legacyRole]
		if alias == "" {
			alias = legacyRole
		}
		if alias != primary {
			return nil, "", fmt.Errorf("%w: legacy role alias must match roles[0]", ErrInvalidIdentity)
		}
	}
	return out, primary, nil
}

func validateRoleKey(role string) error {
	if role == "" || !utf8.ValidString(role) || strings.TrimSpace(role) != role || utf8.RuneCountInString(role) > maxRoleRunes {
		return fmt.Errorf("%w: role key", ErrInvalidIdentity)
	}
	for index, r := range role {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9' && index > 0) || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("%w: role key", ErrInvalidIdentity)
	}
	return nil
}

// validateAuditRoles accepts the historic display-role escape hatch only for
// migrated records. New `roles[]` requests still go through validateRoleKey above;
// this keeps an old custom role auditable instead of making its first v2 change
// impossible to persist.
func validateAuditRoles(roles []string) error {
	if len(roles) == 0 || len(roles) > maxRoles {
		return fmt.Errorf("%w: audit roles", ErrInvalidIdentity)
	}
	seen := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		if err := validateRoleKey(role); err != nil && ValidateRole(role) != nil {
			return err
		}
		if _, found := seen[role]; found {
			return fmt.Errorf("%w: duplicate audit role", ErrInvalidIdentity)
		}
		seen[role] = struct{}{}
	}
	return nil
}
