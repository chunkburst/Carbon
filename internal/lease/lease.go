// Package lease provides durable assignment leases and explicit conflict/approval flow.
// It composes store transactions instead of owning a second persistence mechanism, so
// lease changes share task optimistic versions and provenance with every other mutation.
package lease

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"carbon/internal/store"
	"carbon/internal/task"
)

const DefaultTTL = 15 * time.Minute

var (
	ErrLeaseHeld       = errors.New("task lease is held")
	ErrLeaseNotFound   = errors.New("task has no active lease")
	ErrLeaseOwner      = errors.New("lease is owned by another actor")
	ErrApprovalPending = errors.New("claim is pending approval")
	ErrForceRequired   = errors.New("force is required to change an existing assignee")
	ErrReasonRequired  = errors.New("reason is required")
	ErrInvalidTTL      = errors.New("invalid lease ttl")
	ErrInvalidActor    = errors.New("invalid lease actor")
	ErrInvalidLease    = errors.New("invalid task lease")
	ErrRequestNotFound = errors.New("claim request not found")
)

// Manager's fields are public so a host can inject a deterministic clock. New is the
// preferred constructor and fills all safe defaults.
type Manager struct {
	Store      *store.Store
	Now        func() time.Time
	DefaultTTL time.Duration
	// Authorize is an optional Service-layer policy hook. Lease remains the single
	// durable ownership primitive, while callers can enforce a task/actor policy
	// inside the same Store.Write transaction before a new holder is recorded.
	Authorize TaskAuthorizer
}

// TaskAuthorizer receives the current task while Manager already holds Store.Write.
// It must not open a nested Store.Write transaction. Nil preserves legacy behavior.
type TaskAuthorizer func(tx *store.WriteTx, current task.Task, actor string) error

func New(s *store.Store, now func() time.Time, defaultTTL time.Duration) *Manager {
	if now == nil {
		now = time.Now
	}
	if defaultTTL <= 0 {
		defaultTTL = DefaultTTL
	}
	return &Manager{Store: s, Now: now, DefaultTTL: defaultTTL}
}

func (m *Manager) now() time.Time {
	if m.Now == nil {
		return time.Now().UTC()
	}
	return m.Now().UTC()
}

func (m *Manager) authorize(tx *store.WriteTx, current task.Task, actor string) error {
	if m == nil || m.Authorize == nil {
		return nil
	}
	return m.Authorize(tx, current, actor)
}

func (m *Manager) ttl(value time.Duration) (time.Duration, error) {
	if value == 0 {
		value = m.DefaultTTL
		if value <= 0 {
			value = DefaultTTL
		}
	}
	if value <= 0 || value > 24*time.Hour {
		return 0, fmt.Errorf("%w: %s", ErrInvalidTTL, value)
	}
	return value, nil
}

// ClaimInput is intentionally explicit about actor, ttl, reason, and optimistic token.
// ExpectedVersion accepts the raw store version or quoted ETag; empty keeps compatibility.
type ClaimInput struct {
	TaskID string
	Actor  string
	TTL    time.Duration
	// RequestID is optional on a first request. Supplying the id returned from a prior
	// conflict makes a retry idempotent instead of creating another pending request.
	RequestID       string
	Reason          string
	ExpectedVersion string
}

type ClaimResult struct {
	Doc     *store.Doc
	Lease   *task.Lease
	Pending bool
	Request *task.ClaimRequest
}

type RenewInput struct {
	TaskID          string
	Actor           string
	LeaseID         string
	TTL             time.Duration
	ExpectedVersion string
}

type ReleaseInput struct {
	TaskID  string
	Actor   string
	LeaseID string
	// KeepAssignee ends execution ownership while retaining the visible reviewer/worker
	// attribution. Normal release/cancel leaves it false and clears the actor assignment.
	KeepAssignee    bool
	Reason          string
	ExpectedVersion string
}

// ReassignInput changes a task's assignee manually. Replacing a non-empty assignee or
// lease holder requires Force=true and a non-empty Reason, leaving an audit explanation.
type ReassignInput struct {
	TaskID          string
	Actor           string
	Assignee        string
	Force           bool
	Reason          string
	ExpectedVersion string
}

// ApproveInput records an explicit approval or rejection for one stable pending request.
// Approving grants a fresh lease to the requester and clears the remaining queue because
// the assignment conflict has been decided. A decision reason is always required.
type ApproveInput struct {
	TaskID          string
	Approver        string
	RequestID       string
	Approve         bool
	Reason          string
	ExpectedVersion string
}

type Expired struct {
	TaskID string
	Holder string
	Lease  task.Lease
}

// Claim grants a new lease. A competing owner is never silently overwritten: the request
// is persisted in pending_claims and ErrApprovalPending is returned together with the
// updated Doc/ClaimResult for callers that want to render a 202/approval state.
func (m *Manager) Claim(ctx context.Context, input ClaimInput) (ClaimResult, error) {
	if m.Store == nil {
		return ClaimResult{}, errors.New("lease manager has no store")
	}
	if strings.TrimSpace(input.Actor) == "" {
		return ClaimResult{}, ErrInvalidActor
	}
	ttl, err := m.ttl(input.TTL)
	if err != nil {
		return ClaimResult{}, err
	}
	input.TTL = ttl
	now := m.now()
	var out ClaimResult
	err = m.Store.Write(ctx, input.Actor, "claim task lease", func(tx *store.WriteTx) error {
		doc, err := tx.GetTask(input.TaskID)
		if err != nil {
			return err
		}
		if err := m.authorize(tx, doc.Task, input.Actor); err != nil {
			return err
		}
		if err := doc.MatchVersion(input.ExpectedVersion); err != nil {
			return err
		}
		if expired, err := releaseIfExpired(doc, now); err != nil {
			return err
		} else if expired != nil {
			doc.AppendProvenance("system:lease", "lease auto-released", "holder="+expired.Holder+"; expired_at="+expired.ExpiresAt, now)
		}

		if active := doc.Task.Lease; active != nil {
			if active.Holder == input.Actor {
				renewed := *active
				renewed.ExpiresAt = now.Add(ttl).Format(time.RFC3339)
				renewed.RenewedAt = now.Format(time.RFC3339)
				doc.SetLease(&renewed)
				doc.AppendProvenance(input.Actor, "lease renewed", "lease_id="+renewed.ID, now)
				if err := tx.SaveTask(doc); err != nil {
					return err
				}
				out = ClaimResult{Doc: doc, Lease: cloneLease(&renewed)}
				return nil
			}
			return m.pendingConflict(tx, doc, input, active.Holder, now, &out)
		}
		if doc.Task.Assignee != "" && doc.Task.Assignee != input.Actor {
			return m.pendingConflict(tx, doc, input, doc.Task.Assignee, now, &out)
		}

		id, err := mintLeaseID()
		if err != nil {
			return err
		}
		lease := task.Lease{
			ID: id, Holder: input.Actor, AcquiredAt: now.Format(time.RFC3339),
			ExpiresAt: now.Add(ttl).Format(time.RFC3339),
		}
		doc.SetAssignee(input.Actor)
		doc.SetLease(&lease)
		doc.SetPendingClaims(removeActor(doc.Task.PendingClaims, input.Actor))
		doc.AppendProvenance(input.Actor, "lease claimed", auditText("lease_id="+lease.ID, input.Reason), now)
		if err := tx.SaveTask(doc); err != nil {
			return err
		}
		out = ClaimResult{Doc: doc, Lease: cloneLease(&lease)}
		return nil
	})
	return out, err
}

func (m *Manager) pendingConflict(tx *store.WriteTx, doc *store.Doc, input ClaimInput, holder string, now time.Time, out *ClaimResult) error {
	for _, existing := range doc.Task.PendingClaims {
		// A retry by the same actor without a request id must reuse its outstanding
		// request. Otherwise every network retry would create more approval work.
		if existing.Actor == input.Actor && (input.RequestID == "" || existing.RequestID == input.RequestID) {
			*out = ClaimResult{Doc: doc, Lease: cloneLease(doc.Task.Lease), Pending: true, Request: cloneRequest(&existing)}
			return fmt.Errorf("%w: held by %s", ErrApprovalPending, holder)
		}
		if input.RequestID != "" && existing.RequestID == input.RequestID {
			return fmt.Errorf("%w: request id belongs to %s", ErrApprovalPending, existing.Actor)
		}
	}
	requestID := input.RequestID
	if requestID == "" {
		var err error
		requestID, err = mintRequestID()
		if err != nil {
			return err
		}
	}
	request := task.ClaimRequest{RequestID: requestID, Actor: input.Actor, RequestedAt: now.Format(time.RFC3339), LeaseTTLSeconds: int(input.TTL / time.Second), Reason: input.Reason}
	claims := removeActor(doc.Task.PendingClaims, input.Actor)
	claims = append(claims, request)
	doc.SetPendingClaims(claims)
	doc.AppendProvenance(input.Actor, "claim approval requested", auditText("request_id="+request.RequestID, "holder="+holder, "ttl_seconds="+fmt.Sprint(request.LeaseTTLSeconds), input.Reason), now)
	if err := tx.SaveTask(doc); err != nil {
		return err
	}
	*out = ClaimResult{Doc: doc, Lease: cloneLease(doc.Task.Lease), Pending: true, Request: &request}
	return fmt.Errorf("%w: held by %s", ErrApprovalPending, holder)
}

// Renew extends an existing actor-owned lease. Expiration is checked before ownership so a
// stale worker cannot revive a timed-out claim.
func (m *Manager) Renew(ctx context.Context, input RenewInput) (*store.Doc, error) {
	if m.Store == nil {
		return nil, errors.New("lease manager has no store")
	}
	if strings.TrimSpace(input.Actor) == "" {
		return nil, ErrInvalidActor
	}
	ttl, err := m.ttl(input.TTL)
	if err != nil {
		return nil, err
	}
	now := m.now()
	var out *store.Doc
	err = m.Store.Write(ctx, input.Actor, "renew task lease", func(tx *store.WriteTx) error {
		doc, err := tx.GetTask(input.TaskID)
		if err != nil {
			return err
		}
		if err := doc.MatchVersion(input.ExpectedVersion); err != nil {
			return err
		}
		if expired, err := releaseIfExpired(doc, now); err != nil {
			return err
		} else if expired != nil {
			doc.AppendProvenance("system:lease", "lease auto-released", "holder="+expired.Holder+"; expired_at="+expired.ExpiresAt, now)
			if err := tx.SaveTask(doc); err != nil {
				return err
			}
			return fmt.Errorf("%w: %s", ErrLeaseNotFound, input.TaskID)
		}
		active := doc.Task.Lease
		if active == nil {
			return fmt.Errorf("%w: %s", ErrLeaseNotFound, input.TaskID)
		}
		if active.Holder != input.Actor || (input.LeaseID != "" && active.ID != input.LeaseID) {
			return fmt.Errorf("%w: held by %s", ErrLeaseOwner, active.Holder)
		}
		next := *active
		next.ExpiresAt = now.Add(ttl).Format(time.RFC3339)
		next.RenewedAt = now.Format(time.RFC3339)
		doc.SetLease(&next)
		doc.AppendProvenance(input.Actor, "lease renewed", "lease_id="+next.ID, now)
		if err := tx.SaveTask(doc); err != nil {
			return err
		}
		out = doc
		return nil
	})
	return out, err
}

// Release relinquishes an actor-owned active lease. A reason is mandatory so releases
// are useful audit events instead of unexplained assignment churn.
func (m *Manager) Release(ctx context.Context, input ReleaseInput) (*store.Doc, error) {
	if m.Store == nil {
		return nil, errors.New("lease manager has no store")
	}
	if strings.TrimSpace(input.Actor) == "" {
		return nil, ErrInvalidActor
	}
	if strings.TrimSpace(input.Reason) == "" {
		return nil, ErrReasonRequired
	}
	now := m.now()
	var out *store.Doc
	err := m.Store.Write(ctx, input.Actor, "release task lease", func(tx *store.WriteTx) error {
		doc, err := tx.GetTask(input.TaskID)
		if err != nil {
			return err
		}
		if err := doc.MatchVersion(input.ExpectedVersion); err != nil {
			return err
		}
		if expired, err := releaseIfExpired(doc, now); err != nil {
			return err
		} else if expired != nil {
			doc.AppendProvenance("system:lease", "lease auto-released", "holder="+expired.Holder+"; expired_at="+expired.ExpiresAt, now)
			if err := tx.SaveTask(doc); err != nil {
				return err
			}
			return fmt.Errorf("%w: %s", ErrLeaseNotFound, input.TaskID)
		}
		active := doc.Task.Lease
		if active == nil {
			return fmt.Errorf("%w: %s", ErrLeaseNotFound, input.TaskID)
		}
		if active.Holder != input.Actor || (input.LeaseID != "" && active.ID != input.LeaseID) {
			return fmt.Errorf("%w: held by %s", ErrLeaseOwner, active.Holder)
		}
		doc.SetLease(nil)
		if doc.Task.Assignee == input.Actor && !input.KeepAssignee {
			doc.SetAssignee("")
		}
		doc.AppendProvenance(input.Actor, "lease released", auditText("lease_id="+active.ID, "keep_assignee="+fmt.Sprint(input.KeepAssignee), input.Reason), now)
		if err := tx.SaveTask(doc); err != nil {
			return err
		}
		out = doc
		return nil
	})
	return out, err
}

// Reassign performs the auditable human override. It clears an active lease because a
// different owner cannot inherit another actor's authority, and clears pending approvals
// because the requested owner has now been decided explicitly.
func (m *Manager) Reassign(ctx context.Context, input ReassignInput) (*store.Doc, error) {
	if m.Store == nil {
		return nil, errors.New("lease manager has no store")
	}
	if strings.TrimSpace(input.Actor) == "" {
		return nil, ErrInvalidActor
	}
	now := m.now()
	var out *store.Doc
	err := m.Store.Write(ctx, input.Actor, "reassign task", func(tx *store.WriteTx) error {
		doc, err := tx.GetTask(input.TaskID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(input.Assignee) != "" {
			if err := m.authorize(tx, doc.Task, input.Assignee); err != nil {
				return err
			}
		}
		if err := doc.MatchVersion(input.ExpectedVersion); err != nil {
			return err
		}
		if expired, err := releaseIfExpired(doc, now); err != nil {
			return err
		} else if expired != nil {
			doc.AppendProvenance("system:lease", "lease auto-released", "holder="+expired.Holder+"; expired_at="+expired.ExpiresAt, now)
		}
		current := doc.Task.Assignee
		if doc.Task.Lease != nil {
			current = doc.Task.Lease.Holder
		}
		// Reassign only changes ownership. When the visible assignee already matches,
		// it is a true no-op: in particular it must not silently clear an active lease
		// or pending approval requests merely because the names happen to match.
		if current == input.Assignee {
			out = doc
			return nil
		}
		if current != "" && current != input.Assignee {
			if !input.Force {
				return fmt.Errorf("%w: current assignee %s", ErrForceRequired, current)
			}
			if strings.TrimSpace(input.Reason) == "" {
				return ErrReasonRequired
			}
		}
		if input.Force && strings.TrimSpace(input.Reason) == "" {
			return ErrReasonRequired
		}
		doc.SetLease(nil)
		doc.SetAssignee(input.Assignee)
		doc.SetPendingClaims(nil)
		doc.AppendProvenance(input.Actor, "assignee reassigned", auditText("from="+current, "to="+input.Assignee, "force="+fmt.Sprint(input.Force), input.Reason), now)
		if err := tx.SaveTask(doc); err != nil {
			return err
		}
		out = doc
		return nil
	})
	return out, err
}

// Approve resolves exactly one pending claim request. Approval is the explicit authority
// required to replace an active owner; rejection only removes that request. Both outcomes
// leave a structured provenance entry with request id, requester, approver, and reason.
func (m *Manager) Approve(ctx context.Context, input ApproveInput) (*store.Doc, error) {
	if m.Store == nil {
		return nil, errors.New("lease manager has no store")
	}
	if strings.TrimSpace(input.Approver) == "" {
		return nil, ErrInvalidActor
	}
	if strings.TrimSpace(input.RequestID) == "" {
		return nil, fmt.Errorf("%w: request id is required", ErrRequestNotFound)
	}
	if strings.TrimSpace(input.Reason) == "" {
		return nil, ErrReasonRequired
	}
	now := m.now()
	var out *store.Doc
	err := m.Store.Write(ctx, input.Approver, "decide claim approval", func(tx *store.WriteTx) error {
		doc, err := tx.GetTask(input.TaskID)
		if err != nil {
			return err
		}
		if err := doc.MatchVersion(input.ExpectedVersion); err != nil {
			return err
		}
		request, found := findRequest(doc.Task.PendingClaims, input.RequestID)
		if !found {
			return fmt.Errorf("%w: %s", ErrRequestNotFound, input.RequestID)
		}
		if input.Approve {
			if err := m.authorize(tx, doc.Task, request.Actor); err != nil {
				return err
			}
		}
		if !input.Approve {
			doc.SetPendingClaims(removeRequest(doc.Task.PendingClaims, input.RequestID))
			doc.AppendProvenance(input.Approver, "claim approval rejected", auditText("request_id="+request.RequestID, "requester="+request.Actor, input.Reason), now)
			if err := tx.SaveTask(doc); err != nil {
				return err
			}
			out = doc
			return nil
		}

		if expired, err := releaseIfExpired(doc, now); err != nil {
			return err
		} else if expired != nil {
			doc.AppendProvenance("system:lease", "lease auto-released", "holder="+expired.Holder+"; expired_at="+expired.ExpiresAt, now)
		}
		ttl := time.Duration(request.LeaseTTLSeconds) * time.Second
		if ttl <= 0 || ttl > 24*time.Hour {
			ttl, err = m.ttl(0)
			if err != nil {
				return err
			}
		}
		id, err := mintLeaseID()
		if err != nil {
			return err
		}
		previous := doc.Task.Assignee
		if doc.Task.Lease != nil {
			previous = doc.Task.Lease.Holder
		}
		lease := task.Lease{ID: id, Holder: request.Actor, AcquiredAt: now.Format(time.RFC3339), ExpiresAt: now.Add(ttl).Format(time.RFC3339)}
		doc.SetLease(&lease)
		doc.SetAssignee(request.Actor)
		doc.SetPendingClaims(nil)
		doc.AppendProvenance(input.Approver, "claim approval approved", auditText("request_id="+request.RequestID, "requester="+request.Actor, "from="+previous, "lease_id="+lease.ID, input.Reason), now)
		if err := tx.SaveTask(doc); err != nil {
			return err
		}
		out = doc
		return nil
	})
	return out, err
}

// Expire sweeps every task and automatically releases expired leases. There is no daemon
// hidden in the store; hosts can call this at a cadence, while Claim/Renew/Release also
// enforce expiration opportunistically for correctness on every ownership write.
func (m *Manager) Expire(ctx context.Context) ([]Expired, error) {
	if m.Store == nil {
		return nil, errors.New("lease manager has no store")
	}
	now := m.now()
	var expired []Expired
	err := m.Store.Write(ctx, "system:lease", "expire task leases", func(tx *store.WriteTx) error {
		docs, err := m.Store.ListDocs()
		if err != nil {
			return err
		}
		for _, doc := range docs {
			before := cloneLease(doc.Task.Lease)
			changed, err := releaseIfExpired(doc, now)
			if err != nil {
				return err
			}
			if changed == nil {
				continue
			}
			doc.AppendProvenance("system:lease", "lease auto-released", "holder="+changed.Holder+"; expired_at="+changed.ExpiresAt, now)
			if err := tx.SaveTask(doc); err != nil {
				return err
			}
			if before != nil {
				expired = append(expired, Expired{TaskID: doc.Task.ID, Holder: before.Holder, Lease: *before})
			}
		}
		return nil
	})
	return expired, err
}

func releaseIfExpired(doc *store.Doc, now time.Time) (*task.Lease, error) {
	active := doc.Task.Lease
	if active == nil {
		return nil, nil
	}
	expires, err := time.Parse(time.RFC3339, active.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("%w: lease %s expires_at %q", ErrInvalidLease, active.ID, active.ExpiresAt)
	}
	if now.Before(expires) {
		return nil, nil
	}
	old := cloneLease(active)
	doc.SetLease(nil)
	if doc.Task.Assignee == active.Holder {
		doc.SetAssignee("")
	}
	return old, nil
}

func removeActor(claims []task.ClaimRequest, actor string) []task.ClaimRequest {
	out := make([]task.ClaimRequest, 0, len(claims))
	for _, claim := range claims {
		if claim.Actor != actor {
			out = append(out, claim)
		}
	}
	return out
}

func findRequest(claims []task.ClaimRequest, id string) (task.ClaimRequest, bool) {
	for _, claim := range claims {
		if claim.RequestID == id {
			return claim, true
		}
	}
	return task.ClaimRequest{}, false
}

func removeRequest(claims []task.ClaimRequest, id string) []task.ClaimRequest {
	out := make([]task.ClaimRequest, 0, len(claims))
	for _, claim := range claims {
		if claim.RequestID != id {
			out = append(out, claim)
		}
	}
	return out
}

func cloneLease(in *task.Lease) *task.Lease {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneRequest(in *task.ClaimRequest) *task.ClaimRequest {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func mintLeaseID() (string, error) {
	var bytes [10]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("lease: random id: %w", err)
	}
	return "lease_" + hex.EncodeToString(bytes[:]), nil
}

func mintRequestID() (string, error) {
	var bytes [10]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("lease: random request id: %w", err)
	}
	return "claim_" + hex.EncodeToString(bytes[:]), nil
}

func auditText(parts ...string) string {
	nonempty := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			nonempty = append(nonempty, part)
		}
	}
	return strings.Join(nonempty, "; ")
}

// PendingFor returns a deterministic copy of pending requests for a task without
// changing state. It is useful for adapters that want a dedicated approval endpoint.
func PendingFor(t task.Task) []task.ClaimRequest { return slices.Clone(t.PendingClaims) }
