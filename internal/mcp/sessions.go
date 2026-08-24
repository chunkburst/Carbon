package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"carbon/internal/compat"
	"carbon/internal/gitctx"
	"carbon/internal/lease"
	"carbon/internal/session"
	"carbon/internal/store"
	"carbon/internal/task"
)

// ServiceVersion is stamped by release/desktop builds alongside main.version. Keeping the
// development fallback explicit prevents a stale hard-coded API identity from disagreeing
// with the packaged desktop version.
var ServiceVersion = "dev"

const (
	ExecutionActive         = "active"
	ExecutionStalled        = "stalled"
	ExecutionAwaitingReview = "awaiting_review"
)

var (
	ErrIdentityMismatch    = errors.New("session actor does not match the bound connection actor")
	ErrClientMismatch      = errors.New("session client does not match the bound connection client")
	ErrIdempotencyRequired = errors.New("session idempotency key is required")
	ErrSessionActor        = errors.New("session belongs to another actor")
	ErrTaskClosed          = errors.New("cannot begin a session on a closed task")
)

// Identity describes the actor/client stamped on session writes.
type Identity struct {
	Actor              string          `json:"actor"`
	Client             string          `json:"client,omitempty"`
	Version            string          `json:"version"`
	Scope              ScopeMetadata   `json:"scope"`
	Compatibility      compat.Contract `json:"compatibility"`
	CompatibilityError string          `json:"compatibilityError,omitempty"`
	// BindingMode is empty for established fixed Service connections. Project
	// Session servers set it to "session" so an agent knows that select_project
	// may change Scope during this same MCP connection.
	BindingMode string `json:"bindingMode,omitempty"`
	// SelectionVersion is zero before a Project Session selects a project and
	// increments after every successful selection. Fixed connections leave it
	// omitted for wire compatibility.
	SelectionVersion uint64 `json:"selectionVersion,omitempty"`
}

// SessionView combines durable session fields with ephemeral health.
type SessionView struct {
	session.Session
	Health session.Health `json:"health"`
	Live   *session.Live  `json:"live,omitempty"`
	// HeartbeatIntervalSeconds is the cadence at which the agent should heartbeat to keep
	// the session from going stale. Derived from config so clients need not guess.
	HeartbeatIntervalSeconds int `json:"heartbeatIntervalSeconds,omitempty"`
}

type BeginSessionInput struct {
	TaskID         string
	ExpectedActor  string
	Client         string
	Model          string
	Worktree       string
	Branch         string
	Head           string
	IdempotencyKey string
}

type HeartbeatInput struct {
	SessionID string
	Progress  string
}

type FinishSessionInput struct {
	SessionID string
	Summary   string
	Head      string
}

type CancelSessionInput struct {
	SessionID string
	Reason    string
}

// Identity returns the connection identity without mutating state.
func (svc *Service) Identity() Identity {
	contract, err := svc.Compatibility()
	identity := Identity{
		Actor: svc.actor, Client: svc.client, Version: ServiceVersion,
		Scope: svc.scope.Metadata(), Compatibility: contract,
	}
	if err != nil {
		identity.CompatibilityError = err.Error()
	}
	return identity
}

// Compatibility returns the active legacy/stable envelope selected for this
// connection. It delegates all policy to internal/compat, so a Carbon product build
// cannot change the frozen v1 legacy or approved v2 stable layer by changing ServiceVersion.
func (svc *Service) Compatibility() (compat.Contract, error) {
	mode := compat.ModeLegacy
	if svc.scope.IsCarbonHome() || svc.scope.IsCarbon() {
		mode = compat.ModeCarbon
	}
	return compat.Resolve(ServiceVersion, svc.scope.CompatLayer, mode)
}

// BeginSession atomically claims/starts a task and creates its observable session.
func (svc *Service) BeginSession(ctx context.Context, in BeginSessionInput) (SessionView, error) {
	if in.ExpectedActor != svc.actor {
		return SessionView{}, fmt.Errorf("%w: expected %q, bound as %q", ErrIdentityMismatch, in.ExpectedActor, svc.actor)
	}
	if svc.client != "" && in.Client != "" && in.Client != svc.client {
		return SessionView{}, fmt.Errorf("%w: expected %q, bound as %q", ErrClientMismatch, in.Client, svc.client)
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return SessionView{}, ErrIdempotencyRequired
	}
	// Resolve against the task's source project before opening the write transaction.
	// In Carbon mode Store.Root is a shared metadata directory, never an executable
	// checkout; using it here would let a command/session operate in .carbon data.
	taskDoc, err := svc.store.Get(in.TaskID)
	if err != nil {
		return SessionView{}, err
	}
	if err := svc.writeAllowed(taskDoc.Task); err != nil {
		return SessionView{}, err
	}
	if err := svc.authorizeWorkerTask(taskDoc.Task, svc.actor); err != nil {
		return SessionView{}, err
	}
	worktree, err := svc.resolveTaskWorktree(taskDoc.Task, in.Worktree)
	if err != nil {
		return SessionView{}, err
	}
	in.Worktree = worktree

	startedAt := svc.now().UTC()
	if in.Branch == "" || in.Head == "" {
		if ref, err := gitctx.Current(ctx, worktree); err == nil {
			if in.Branch == "" {
				in.Branch = ref.Branch
			}
			if in.Head == "" {
				in.Head = ref.Head
			}
		}
	}
	sessionID := stableSessionID("ses_", in.TaskID, svc.actor, in.IdempotencyKey)
	attemptID := stableSessionID("att_", in.TaskID, svc.actor, in.IdempotencyKey)
	var result *store.SessionDoc

	err = svc.store.Write(ctx, svc.actor, "begin session", func(tx *store.WriteTx) error {
		doc, err := tx.GetTask(in.TaskID)
		if err != nil {
			return err
		}
		if err := svc.writeAllowed(doc.Task); err != nil {
			return err
		}
		if err := svc.authorizeWorkerTaskTx(tx, doc.Task, svc.actor); err != nil {
			return err
		}
		existing, err := tx.FindSessionByIdempotency(in.TaskID, in.IdempotencyKey)
		if err != nil {
			return err
		}

		// Idempotent retry: a previously-successful begin returns its original session
		// regardless of any later human action (closing or reclaiming the task). An
		// active retry also re-establishes its actor's lease, which lets a reconnect
		// repair a session created before durable leases were introduced.
		if existing != nil {
			result = existing
			if existing.Session.Status == session.StatusActive {
				cfg, err := tx.Config()
				if err != nil {
					return err
				}
				changed, err := svc.maintainSessionLease(doc, cfg.SessionStaleDuration(), startedAt, true)
				if err != nil {
					return err
				}
				if changed {
					if err := tx.SaveTask(doc); err != nil {
						return err
					}
				}
			}
			if live, err := svc.readLiveForTask(existing.Session.ID, doc.Task); err != nil {
				return err
			} else if live == nil {
				return svc.writeLiveForTask(tx, session.Live{SessionID: existing.Session.ID, HeartbeatAt: startedAt, Worktree: in.Worktree}, doc.Task)
			}
			return nil
		}

		cfg, err := tx.Config()
		if err != nil {
			return err
		}
		if slices.Contains(cfg.Closed, doc.Task.Status) {
			return fmt.Errorf("%w: %s", ErrTaskClosed, in.TaskID)
		}
		live, err := tx.LiveSession(in.TaskID)
		if err != nil {
			return err
		}
		if live != nil {
			return fmt.Errorf("%w: %s", store.ErrLiveSession, live.Session.ID)
		}

		taskChanged, err := svc.maintainSessionLease(doc, cfg.SessionStaleDuration(), startedAt, true)
		if err != nil {
			return err
		}
		if doc.Task.Status == cfg.Initial {
			all, err := tx.Tasks()
			if err != nil {
				return err
			}
			if err := task.CanTransition(doc.Task, cfg.Working(), all, rulesOf(cfg)); err != nil {
				return err
			}
			doc.SetStatus(cfg.Working())
			taskChanged = true
		}
		if doc.Task.ActiveAttempt != attemptID {
			doc.SetActiveAttempt(attemptID)
			taskChanged = true
		}
		if taskChanged {
			doc.AppendProvenance(svc.actor, "began session "+sessionID, "", startedAt)
			if err := tx.SaveTask(doc); err != nil {
				return err
			}
		}

		value := session.Session{
			ID:             sessionID,
			TaskID:         in.TaskID,
			AttemptID:      attemptID,
			Actor:          svc.actor,
			Client:         firstNonEmpty(in.Client, svc.client),
			Model:          in.Model,
			Status:         session.StatusActive,
			IdempotencyKey: in.IdempotencyKey,
			StartedAt:      startedAt,
			Branch:         in.Branch,
			HeadStarted:    in.Head,
		}
		created, err := tx.CreateSession(value)
		if err != nil {
			return err
		}
		if err := svc.writeLiveForTask(tx, session.Live{SessionID: sessionID, HeartbeatAt: startedAt, Worktree: in.Worktree}, doc.Task); err != nil {
			return err
		}
		result = created
		return nil
	})
	if err != nil {
		return SessionView{}, err
	}
	return svc.sessionView(result)
}

// resolveTaskWorktree canonicalizes an optional worktree inside the task's executable
// source root. It mirrors Store.ResolveWorktree's containment guarantees without ever
// treating a Carbon metadata root as source code.
func (svc *Service) resolveTaskWorktree(t task.Task, raw string) (string, error) {
	if !svc.scope.IsCarbon() {
		return svc.store.ResolveWorktree(raw)
	}
	root, err := svc.sourceRoot(t)
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(raw)
	if path == "" {
		path = root
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%w: resolve worktree %q: %v", ErrExecutionProjectRequired, raw, err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: worktree %q is not a directory", ErrExecutionProjectRequired, raw)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("%w: resolve project source: %v", ErrExecutionProjectRequired, err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: worktree escapes task project", ErrExecutionProjectRequired)
	}
	return filepath.Clean(resolved), nil
}

// readLiveForTask keeps legacy live-state validation rooted at Store.Root, while Carbon
// validates every persisted worktree against the concrete source root of the session's
// owning task. This is deliberately repeated at read time to reject tampered live JSON.
func (svc *Service) readLiveForTask(sessionID string, t task.Task) (*session.Live, error) {
	if !svc.scope.IsCarbon() {
		return svc.store.ReadLive(sessionID)
	}
	root, err := svc.sourceRoot(t)
	if err != nil {
		return nil, err
	}
	return svc.store.ReadLiveWithin(sessionID, root)
}

func (svc *Service) writeLiveForTask(tx *store.WriteTx, live session.Live, t task.Task) error {
	if !svc.scope.IsCarbon() {
		return tx.WriteLive(live)
	}
	root, err := svc.sourceRoot(t)
	if err != nil {
		return err
	}
	return tx.WriteLiveWithin(live, root)
}

// maintainSessionLease gives an active session durable ownership without opening a
// nested Store transaction. The caller already holds Store.Write's transaction lock,
// so doing this beside the session/live updates keeps the two lifecycles coherent.
//
// The lease lifetime is never shorter than the session stale window. This matters when
// a deployment deliberately uses a stale threshold longer than the normal 15-minute
// lease: a healthy worker must not lose ownership before the UI would mark its session
// stalled.
func (svc *Service) maintainSessionLease(doc *store.Doc, stale time.Duration, now time.Time, audit bool) (bool, error) {
	now = now.UTC()
	ttl := lease.DefaultTTL
	if stale > ttl {
		ttl = stale
	}

	changed := false
	active := doc.Task.Lease
	if active != nil {
		expires, err := time.Parse(time.RFC3339, active.ExpiresAt)
		if err != nil {
			return false, fmt.Errorf("%w: lease %s expires_at %q", lease.ErrInvalidLease, active.ID, active.ExpiresAt)
		}
		if !now.Before(expires) {
			expired := *active
			doc.SetLease(nil)
			if doc.Task.Assignee == expired.Holder {
				doc.SetAssignee("")
			}
			if audit {
				doc.AppendProvenance("system:lease", "lease auto-released", "holder="+expired.Holder+"; expired_at="+expired.ExpiresAt, now)
			}
			active = nil
			changed = true
		}
	}

	if active != nil {
		if active.Holder != svc.actor {
			return changed, fmt.Errorf("%w: held by %s", ErrAlreadyClaimed, active.Holder)
		}
		next := *active
		next.ExpiresAt = now.Add(ttl).Format(time.RFC3339)
		next.RenewedAt = now.Format(time.RFC3339)
		doc.SetLease(&next)
		if doc.Task.Assignee != svc.actor {
			doc.SetAssignee(svc.actor)
		}
		if audit {
			doc.AppendProvenance(svc.actor, "session lease renewed", "lease_id="+next.ID, now)
		}
		return true, nil
	}

	if doc.Task.Assignee != "" && doc.Task.Assignee != svc.actor {
		return changed, fmt.Errorf("%w: held by %s", ErrAlreadyClaimed, doc.Task.Assignee)
	}
	id, err := mintSessionLeaseID()
	if err != nil {
		return changed, err
	}
	claimed := task.Lease{
		ID:         id,
		Holder:     svc.actor,
		AcquiredAt: now.Format(time.RFC3339),
		ExpiresAt:  now.Add(ttl).Format(time.RFC3339),
	}
	doc.SetAssignee(svc.actor)
	doc.SetLease(&claimed)
	if audit {
		doc.AppendProvenance(svc.actor, "session lease claimed", "lease_id="+claimed.ID, now)
	}
	return true, nil
}

// releaseSessionLease relinquishes only this session actor's ownership. A manual
// reassignment made while a session was ending must never be overwritten by its
// finishing/canceling client.
func (svc *Service) releaseSessionLease(doc *store.Doc) bool {
	if doc.Task.Lease == nil || doc.Task.Lease.Holder != svc.actor {
		return false
	}
	doc.SetLease(nil)
	return true
}

func mintSessionLeaseID() (string, error) {
	var bytes [10]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("mint session lease id: %w", err)
	}
	return "lease_" + hex.EncodeToString(bytes[:]), nil
}

// Heartbeat refreshes ephemeral progress for an active session.
func (svc *Service) Heartbeat(ctx context.Context, in HeartbeatInput) (SessionView, error) {
	var result *store.SessionDoc
	err := svc.store.Write(ctx, svc.actor, "heartbeat session", func(tx *store.WriteTx) error {
		d, err := tx.GetSession(in.SessionID)
		if err != nil {
			return err
		}
		if err := svc.ownsSession(d.Session); err != nil {
			return err
		}
		if d.Session.Status != session.StatusActive {
			return fmt.Errorf("%w: %s is %s", session.ErrTerminal, d.Session.ID, d.Session.Status)
		}
		taskDoc, err := tx.GetTask(d.Session.TaskID)
		if err != nil {
			return err
		}
		if err := svc.writeAllowed(taskDoc.Task); err != nil {
			return err
		}
		cfg, err := tx.Config()
		if err != nil {
			return err
		}
		// A live session is a stronger liveness signal than the old standalone
		// assignment field. Keep its durable ownership lease beyond the configured
		// stale window on every heartbeat, so a long-running session cannot be
		// claimed by another actor merely because the default lease elapsed.
		changed, err := svc.maintainSessionLease(taskDoc, cfg.SessionStaleDuration(), svc.now().UTC(), false)
		if err != nil {
			return err
		}
		if changed {
			if err := tx.SaveTask(taskDoc); err != nil {
				return err
			}
		}
		live, err := svc.readLiveForTask(in.SessionID, taskDoc.Task)
		if err != nil {
			return err
		}
		if live == nil {
			live = &session.Live{SessionID: in.SessionID}
		}
		live.HeartbeatAt = svc.now().UTC()
		if strings.TrimSpace(in.Progress) != "" {
			live.Progress = strings.TrimSpace(in.Progress)
		}
		if err := svc.writeLiveForTask(tx, *live, taskDoc.Task); err != nil {
			return err
		}
		result = d
		return nil
	})
	if err != nil {
		return SessionView{}, err
	}
	return svc.sessionView(result)
}

// FinishSession records a final summary and moves the task into review when configured.
func (svc *Service) FinishSession(ctx context.Context, in FinishSessionInput) (SessionView, error) {
	var result *store.SessionDoc
	if in.Head == "" {
		if durable, err := svc.store.GetSession(in.SessionID); err == nil {
			if taskDoc, err := svc.store.Get(durable.Session.TaskID); err == nil {
				if scopeErr := svc.writeAllowed(taskDoc.Task); scopeErr == nil {
					if root, err := svc.sourceRoot(taskDoc.Task); err == nil {
						if ref, err := gitctx.Current(ctx, root); err == nil {
							in.Head = ref.Head
						}
					}
				}
			}
		}
	}
	err := svc.store.Write(ctx, svc.actor, "finish session", func(tx *store.WriteTx) error {
		d, err := tx.GetSession(in.SessionID)
		if err != nil {
			return err
		}
		if err := svc.ownsSession(d.Session); err != nil {
			return err
		}
		taskDoc, err := tx.GetTask(d.Session.TaskID)
		if err != nil {
			return err
		}
		if err := svc.writeAllowed(taskDoc.Task); err != nil {
			return err
		}
		cfg, err := tx.Config()
		if err != nil {
			return err
		}
		review := cfg.Review()
		movingToReview := review != "" && !slices.Contains(cfg.Closed, taskDoc.Task.Status) && taskDoc.Task.Status != review

		// Enforce the review checks gate UP FRONT, before ending the session: a handoff into
		// review requires every COMMAND check to be recorded `pass` (manual checks are exempt
		// — they're attested during review). If they're pending or failing, refuse the whole
		// finish so the session stays active; the agent runs `run_checks` (which executes
		// outside this write lock) and retries. This makes "run checks before handoff" a hard
		// engine gate, not a documented suggestion — while keeping the build off the lock.
		if movingToReview {
			all, err := tx.Tasks()
			if err != nil {
				return err
			}
			if gateErr := task.CanTransition(taskDoc.Task, review, all, rulesOf(cfg)); gateErr != nil {
				return gateErr
			}
		}

		finished := d.Session
		justFinished := false
		if d.Session.Status != session.StatusFinished {
			finished, err = session.Finish(d.Session, strings.TrimSpace(in.Summary), in.Head, svc.now())
			if err != nil {
				return err
			}
			d.Replace(finished)
			justFinished = true
			if err := tx.SaveSession(d); err != nil {
				return err
			}
		}
		if err := tx.DeleteLive(d.Session.ID); err != nil {
			return err
		}

		taskChanged := false
		if movingToReview {
			taskDoc.SetStatus(review)
			taskChanged = true
		}
		// A completed session no longer needs exclusive execution ownership. Keep
		// the assignee as an audit/reviewer attribution, but release this actor's
		// durable lease so a follow-up can be explicitly claimed.
		if justFinished && svc.releaseSessionLease(taskDoc) {
			taskChanged = true
		}
		if taskChanged {
			taskDoc.AppendProvenance(svc.actor, "finished session "+d.Session.ID, finished.Summary, svc.now())
			if err := tx.SaveTask(taskDoc); err != nil {
				return err
			}
		}
		result = d
		return nil
	})
	if err != nil {
		return SessionView{}, err
	}
	return svc.sessionView(result)
}

// CancelSession abandons a live attempt and releases its task assignment.
func (svc *Service) CancelSession(ctx context.Context, in CancelSessionInput) (SessionView, error) {
	var result *store.SessionDoc
	err := svc.store.Write(ctx, svc.actor, "cancel session", func(tx *store.WriteTx) error {
		d, err := tx.GetSession(in.SessionID)
		if err != nil {
			return err
		}
		if err := svc.ownsSession(d.Session); err != nil {
			return err
		}
		taskDoc, err := tx.GetTask(d.Session.TaskID)
		if err != nil {
			return err
		}
		if err := svc.writeAllowed(taskDoc.Task); err != nil {
			return err
		}
		canceled := d.Session
		if d.Session.Status != session.StatusCanceled {
			canceled, err = session.Cancel(d.Session, strings.TrimSpace(in.Reason), svc.now())
			if err != nil {
				return err
			}
			d.Replace(canceled)
			if err := tx.SaveSession(d); err != nil {
				return err
			}
		}
		if err := tx.DeleteLive(d.Session.ID); err != nil {
			return err
		}

		taskChanged := false
		if svc.releaseSessionLease(taskDoc) {
			taskChanged = true
		}
		if taskDoc.Task.Assignee == svc.actor && taskDoc.Task.ActiveAttempt == d.Session.AttemptID {
			taskDoc.SetAssignee("")
			// An abandoned attempt must no longer be the task's active attempt, so the task
			// derives no execution state (finish, by contrast, retains it for awaiting_review).
			taskDoc.SetActiveAttempt("")
			taskChanged = true
		}
		if taskChanged {
			taskDoc.AppendProvenance(svc.actor, "canceled session "+d.Session.ID, canceled.CancelReason, svc.now())
			if err := tx.SaveTask(taskDoc); err != nil {
				return err
			}
		}
		result = d
		return nil
	})
	if err != nil {
		return SessionView{}, err
	}
	return svc.sessionView(result)
}

// GetSession returns one session with current live health.
func (svc *Service) GetSession(id string) (SessionView, error) {
	return svc.GetSessionScoped(id, false)
}

// GetSessionScoped exposes a foreign-project session only after an explicit same-cluster
// read opt-in. It never affects write authorization for heartbeat/finish/cancel.
func (svc *Service) GetSessionScoped(id string, includeCluster bool) (SessionView, error) {
	if err := svc.validateIncludeCluster(includeCluster); err != nil {
		return SessionView{}, err
	}
	d, err := svc.store.GetSession(id)
	if err != nil {
		return SessionView{}, err
	}
	taskDoc, err := svc.store.Get(d.Session.TaskID)
	if err != nil {
		return SessionView{}, err
	}
	if !svc.readAllowed(taskDoc.Task, includeCluster) {
		return SessionView{}, fmt.Errorf("%w: %s", ErrProjectScope, d.Session.TaskID)
	}
	return svc.sessionView(d)
}

// ListSessions returns newest-first sessions filtered by durable or derived fields.
func (svc *Service) ListSessions(taskID, actor, status, health string) ([]SessionView, error) {
	return svc.ListSessionsScoped(taskID, actor, status, health, false)
}

// ListSessionsScoped mirrors GetSessionScoped for list queries. includeCluster is a
// read-only opt-in and never widens the task ownership used by session mutations.
func (svc *Service) ListSessionsScoped(taskID, actor, status, health string, includeCluster bool) ([]SessionView, error) {
	if err := svc.validateIncludeCluster(includeCluster); err != nil {
		return nil, err
	}
	if taskID != "" {
		if _, err := svc.GetScoped(taskID, includeCluster); err != nil {
			return nil, err
		}
	}
	docs, err := svc.store.ListSessions()
	if err != nil {
		return nil, err
	}
	out := make([]SessionView, 0, len(docs))
	for _, d := range docs {
		if taskID != "" && d.Session.TaskID != taskID {
			continue
		}
		if actor != "" && d.Session.Actor != actor {
			continue
		}
		if status != "" && string(d.Session.Status) != status {
			continue
		}
		taskDoc, err := svc.store.Get(d.Session.TaskID)
		if err != nil {
			return nil, err
		}
		if !svc.readAllowed(taskDoc.Task, includeCluster) {
			continue
		}
		view, err := svc.sessionView(d)
		if err != nil {
			return nil, err
		}
		if health != "" && string(view.Health) != health {
			continue
		}
		out = append(out, view)
	}
	return out, nil
}

func (svc *Service) sessionView(d *store.SessionDoc) (SessionView, error) {
	taskDoc, err := svc.store.Get(d.Session.TaskID)
	if err != nil {
		return SessionView{}, err
	}
	live, err := svc.readLiveForTask(d.Session.ID, taskDoc.Task)
	if err != nil {
		return SessionView{}, err
	}
	cfg, err := svc.store.Config()
	if err != nil {
		return SessionView{}, err
	}
	return SessionView{
		Session:                  d.Session,
		Live:                     live,
		Health:                   session.DeriveHealth(d.Session, live, svc.now(), cfg.SessionStaleDuration()),
		HeartbeatIntervalSeconds: int(cfg.SessionHeartbeatDuration().Seconds()),
	}, nil
}

func (svc *Service) executionForTask(t task.Task) (string, string) {
	docs, err := svc.store.TaskSessions(t.ID)
	if err != nil {
		// A read failure (e.g. a corrupt session file) must not masquerade as "no
		// session" — surface it so the degraded state is visible, then degrade gracefully.
		log.Printf("mcp: execution state for %s: read sessions: %v", t.ID, err)
		return "", ""
	}
	if len(docs) == 0 {
		return "", ""
	}
	cfg, err := svc.store.Config()
	if err != nil {
		log.Printf("mcp: execution state for %s: read config: %v", t.ID, err)
		return "", ""
	}
	state, sessionID, err := svc.executionFor(t, docs[0], cfg)
	if err != nil {
		log.Printf("mcp: execution state for %s: derive: %v", t.ID, err)
		return "", ""
	}
	return state, sessionID
}

// ExecutionOf returns a task's derived supervision state and latest relevant session.
func (svc *Service) ExecutionOf(t task.Task) (string, string) {
	return svc.executionForTask(t)
}

func (svc *Service) ownsSession(s session.Session) error {
	if s.Actor != svc.actor {
		return fmt.Errorf("%w: %s is owned by %s", ErrSessionActor, s.ID, s.Actor)
	}
	return nil
}

func stableSessionID(prefix, taskID, actor, key string) string {
	sum := sha256.Sum256([]byte(taskID + "\x00" + actor + "\x00" + key))
	return prefix + hex.EncodeToString(sum[:12])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
