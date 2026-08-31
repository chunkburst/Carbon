package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"carbon/internal/config"
	"carbon/internal/identity"
	"carbon/internal/lease"
	"carbon/internal/projectpolicy"
	"carbon/internal/store"
	"carbon/internal/subscription"
	"carbon/internal/task"
)

var (
	// ErrIdentityScopeRequired keeps the optional registry out of frozen legacy
	// workspaces. Carbon standalone and shared-cluster stores each get their own
	// durable registry.
	ErrIdentityScopeRequired   = errors.New("worker identities require a Carbon project or cluster scope")
	ErrIdentityModeDisabled    = errors.New("identity mode is disabled for this project")
	ErrIdentitySelfOnly        = errors.New("agents may only change their own worker identity")
	ErrIdentityAgentRequired   = errors.New("worker identity actor must be an agent")
	ErrIdentityTypeUnknown     = errors.New("worker identity references an unavailable task type")
	ErrIdentityRequired        = errors.New("agent must claim a worker identity before taking typed work")
	ErrIdentityTaskType        = errors.New("worker identity is not eligible for this task type")
	ErrIdentityProjectRequired = errors.New("worker identities require a selected Carbon project")
)

// WorkerIdentitySnapshot is the adapter-neutral registry response. Records remain
// visible while mode is disabled so a temporary opt-out never destroys an existing
// team's assignments; modeEnabled alone controls enforcement and mutations.
type WorkerIdentitySnapshot struct {
	ModeEnabled bool              `json:"modeEnabled"`
	Records     []identity.Record `json:"records"`
}

// WorkerIdentityResult is the one-record counterpart of WorkerIdentitySnapshot.
type WorkerIdentityResult struct {
	ModeEnabled bool            `json:"modeEnabled"`
	Record      identity.Record `json:"record"`
}

// WorkerIdentityAuditSnapshot is the append-only audit surface. Identity records
// stay readable while the optional enforcement mode is off; audits do too.
type WorkerIdentityAuditSnapshot struct {
	ModeEnabled bool             `json:"modeEnabled"`
	Audits      []identity.Audit `json:"audits"`
}

type WorkerIdentityClaimInput struct {
	Roles  []string
	Role   string
	Types  []string
	Reason string
}

func (svc *Service) identityManager() (*identity.Manager, error) {
	if svc == nil || svc.store == nil || !svc.scope.IsCarbon() {
		return nil, ErrIdentityScopeRequired
	}
	if strings.TrimSpace(svc.scope.ProjectID) == "" {
		return nil, ErrIdentityProjectRequired
	}
	manager, err := identity.NewProject(svc.store, svc.now, svc.scope.ProjectID, svc.scope.IsStandalone())
	if err != nil {
		return nil, err
	}
	return manager, nil
}

func (svc *Service) identityPolicy() (projectpolicy.Policy, error) {
	if _, err := svc.identityManager(); err != nil {
		return projectpolicy.Policy{}, err
	}
	return projectpolicy.New(svc.store).Get(svc.scope.ProjectID)
}

func (svc *Service) identityPolicyTx(tx *store.WriteTx) (projectpolicy.Policy, error) {
	if _, err := svc.identityManager(); err != nil {
		return projectpolicy.Policy{}, err
	}
	return projectpolicy.New(svc.store).GetTx(tx, svc.scope.ProjectID)
}

func (svc *Service) identityConfig() (enabled bool, err error) {
	policy, err := svc.identityPolicy()
	if err != nil {
		return false, err
	}
	return policy.IdentityMode, nil
}

// ListWorkerIdentities returns the current project/cluster's registry. Reading the
// registry is intentionally useful before identity mode is enabled, because a human
// can review existing records before re-enabling the guard.
func (svc *Service) ListWorkerIdentities() (WorkerIdentitySnapshot, error) {
	manager, err := svc.identityManager()
	if err != nil {
		return WorkerIdentitySnapshot{}, err
	}
	enabled, err := svc.identityConfig()
	if err != nil {
		return WorkerIdentitySnapshot{}, err
	}
	records, err := manager.List()
	if err != nil {
		return WorkerIdentitySnapshot{}, err
	}
	return WorkerIdentitySnapshot{ModeEnabled: enabled, Records: records}, nil
}

func (svc *Service) ListWorkerIdentityAudit() (WorkerIdentityAuditSnapshot, error) {
	manager, err := svc.identityManager()
	if err != nil {
		return WorkerIdentityAuditSnapshot{}, err
	}
	enabled, err := svc.identityConfig()
	if err != nil {
		return WorkerIdentityAuditSnapshot{}, err
	}
	audits, err := manager.ListAudit()
	if err != nil {
		return WorkerIdentityAuditSnapshot{}, err
	}
	return WorkerIdentityAuditSnapshot{ModeEnabled: enabled, Audits: audits}, nil
}

func (svc *Service) GetWorkerIdentity(actor string) (WorkerIdentityResult, error) {
	manager, err := svc.identityManager()
	if err != nil {
		return WorkerIdentityResult{}, err
	}
	enabled, err := svc.identityConfig()
	if err != nil {
		return WorkerIdentityResult{}, err
	}
	record, err := manager.Get(actor)
	if err != nil {
		return WorkerIdentityResult{}, err
	}
	return WorkerIdentityResult{ModeEnabled: enabled, Record: record}, nil
}

// ClaimWorkerIdentity is the MCP self-service primitive. A fixed Agent can only ever
// claim its own actor; human HTTP administration uses ManageWorkerIdentity instead.
func (svc *Service) ClaimWorkerIdentity(ctx context.Context, input WorkerIdentityClaimInput) (WorkerIdentityResult, error) {
	if !identity.IsAgent(svc.actor) {
		return WorkerIdentityResult{}, ErrIdentityAgentRequired
	}
	return svc.manageWorkerIdentity(ctx, svc.actor, input, false)
}

// ManageWorkerIdentity is the human-administration path used by HTTP. Agents still
// retain exactly the self-only rule even if an in-process caller supplies a target.
func (svc *Service) ManageWorkerIdentity(ctx context.Context, actor string, input WorkerIdentityClaimInput) (WorkerIdentityResult, error) {
	if !identity.IsAgent(actor) {
		return WorkerIdentityResult{}, ErrIdentityAgentRequired
	}
	if actor != svc.actor && !identity.IsHuman(svc.actor) && !identity.IsSystem(svc.actor) {
		return WorkerIdentityResult{}, ErrIdentitySelfOnly
	}
	// Human/system administrators can prepare or correct Worker assignments even
	// while IdentityMode is off. Mode only controls Agent self-service and task
	// eligibility enforcement, not the human management/audit path.
	return svc.manageWorkerIdentity(ctx, actor, input, identity.IsHuman(svc.actor) || identity.IsSystem(svc.actor))
}

func (svc *Service) manageWorkerIdentity(ctx context.Context, actor string, input WorkerIdentityClaimInput, administrative bool) (WorkerIdentityResult, error) {
	manager, err := svc.identityManager()
	if err != nil {
		return WorkerIdentityResult{}, err
	}
	var record identity.Record
	var modeEnabled bool
	err = svc.store.Write(ctx, svc.actor, "manage worker identity", func(tx *store.WriteTx) error {
		cfg, err := tx.Config()
		if err != nil {
			return err
		}
		policy, err := svc.identityPolicyTx(tx)
		if err != nil {
			return err
		}
		if !policy.IdentityMode && !administrative {
			return ErrIdentityModeDisabled
		}
		if err := validateWorkerIdentityTypes(cfg, input.Types); err != nil {
			return err
		}
		out, _, err := manager.ClaimOrChangeTx(tx, svc.actor, identity.ClaimInput{
			Actor: actor, Roles: slices.Clone(input.Roles), Role: input.Role, Types: slices.Clone(input.Types), Reason: input.Reason,
		}, identity.ChangeOptions{ProjectID: svc.scope.ProjectID, NoTraceMode: policy.NoTraceMode})
		if err != nil {
			return err
		}
		record, modeEnabled = out, policy.IdentityMode
		return nil
	})
	if err != nil {
		return WorkerIdentityResult{}, err
	}
	return WorkerIdentityResult{ModeEnabled: modeEnabled, Record: record}, nil
}

func validateWorkerIdentityTypes(cfg config.Config, types []string) error {
	if err := identity.ValidateTypes(types); err != nil {
		return err
	}
	catalog := cfg.TypeCatalog()
	for _, typ := range types {
		if !catalog.Allowed(typ) {
			return fmt.Errorf("%w: %s", ErrIdentityTypeUnknown, typ)
		}
	}
	return nil
}

// leaseManager is the one Service-layer construction point for ownership mutations.
// Its callback executes inside lease.Manager's transaction, preventing a direct MCP or
// HTTP route from bypassing IdentityMode with a time-of-check/time-of-use gap.
func (svc *Service) leaseManager() *lease.Manager {
	manager := lease.New(svc.store, svc.now, 0)
	manager.Authorize = svc.authorizeWorkerTaskTx
	var prepared *subscription.PreparedEvent
	manager.BeforeClaimSave = func(tx *store.WriteTx, doc *store.Doc) error {
		events, projectID, enabled, err := svc.optionalEventSubscriptionManager()
		if err != nil || !enabled {
			return err
		}
		prepared, err = svc.prepareTaskEventTx(tx, events, projectID, doc, "lease_claimed")
		return err
	}
	manager.AfterClaimSave = func(tx *store.WriteTx, _ *store.Doc) error {
		events, _, enabled, err := svc.optionalEventSubscriptionManager()
		if err != nil || !enabled {
			return err
		}
		return commitPreparedTaskEventTx(tx, events, prepared)
	}
	return manager
}

func (svc *Service) authorizeWorkerTask(t task.Task, actor string) error {
	if !svc.scope.IsCarbon() || !identity.IsAgent(actor) || strings.TrimSpace(t.Type) == "" {
		return nil // human/system actors and historic untyped tasks remain compatible.
	}
	manager, err := svc.identityManager()
	if err != nil {
		return err
	}
	policy, err := svc.identityPolicy()
	if err != nil {
		return err
	}
	if !policy.IdentityMode {
		return nil
	}
	record, err := manager.Get(actor)
	if errors.Is(err, identity.ErrNotFound) {
		return fmt.Errorf("%w: %s", ErrIdentityRequired, actor)
	}
	if err != nil {
		return err
	}
	if !slices.Contains(record.Types, t.Type) {
		return fmt.Errorf("%w: %s cannot take type %s", ErrIdentityTaskType, actor, t.Type)
	}
	return nil
}

func (svc *Service) authorizeWorkerTaskTx(tx *store.WriteTx, t task.Task, actor string) error {
	if !svc.scope.IsCarbon() || !identity.IsAgent(actor) || strings.TrimSpace(t.Type) == "" {
		return nil
	}
	if _, err := svc.identityManager(); err != nil {
		return err
	}
	policy, err := svc.identityPolicyTx(tx)
	if err != nil {
		return err
	}
	if !policy.IdentityMode {
		return nil
	}
	manager, err := svc.identityManager()
	if err != nil {
		return err
	}
	record, err := manager.GetTx(tx, actor)
	if errors.Is(err, identity.ErrNotFound) {
		return fmt.Errorf("%w: %s", ErrIdentityRequired, actor)
	}
	if err != nil {
		return err
	}
	if !slices.Contains(record.Types, t.Type) {
		return fmt.Errorf("%w: %s cannot take type %s", ErrIdentityTaskType, actor, t.Type)
	}
	return nil
}
