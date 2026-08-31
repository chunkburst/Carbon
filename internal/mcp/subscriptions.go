package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"carbon/internal/compat"
	"carbon/internal/incident"
	"carbon/internal/store"
	"carbon/internal/subscription"
)

var (
	ErrEventSubscriptionScopeRequired   = errors.New("event subscriptions require a stable Carbon project scope")
	ErrEventSubscriptionProjectRequired = errors.New("event subscriptions require a selected project")
)

// eventSubscriptionManager is deliberately project-only. A legacy repository or
// a shared-cluster-only connection has no safe recipient boundary, so neither can
// initialize, list, recover, or poll subscriptions.
func (svc *Service) eventSubscriptionManager() (*subscription.Manager, string, error) {
	if svc == nil || svc.store == nil || !svc.scope.IsCarbon() {
		return nil, "", ErrEventSubscriptionScopeRequired
	}
	projectID := strings.TrimSpace(svc.scope.ProjectID)
	if projectID == "" {
		return nil, "", ErrEventSubscriptionProjectRequired
	}
	contract, err := svc.Compatibility()
	if err != nil || contract.RequestedCompatLayer != compat.StableLayer {
		if err != nil {
			return nil, "", err
		}
		return nil, "", ErrEventSubscriptionScopeRequired
	}
	return subscription.New(svc.store, svc.now), projectID, nil
}

func (svc *Service) EventSubscriptionCapability() (subscription.Capability, error) {
	if _, _, err := svc.eventSubscriptionManager(); err != nil {
		return subscription.Capability{}, err
	}
	return subscription.CapabilitySnapshot(), nil
}

func (svc *Service) ListEventSubscriptions() ([]subscription.Subscription, error) {
	manager, projectID, err := svc.eventSubscriptionManager()
	if err != nil {
		return nil, err
	}
	return manager.List(projectID)
}

func (svc *Service) InitializeEventSubscription(ctx context.Context, input subscription.InitializeInput) (subscription.InitializeResult, error) {
	manager, projectID, err := svc.eventSubscriptionManager()
	if err != nil {
		return subscription.InitializeResult{}, err
	}
	if err := svc.recoverEventLedger(ctx, manager, projectID); err != nil {
		return subscription.InitializeResult{}, err
	}
	return manager.Initialize(ctx, projectID, svc.actor, input)
}

func (svc *Service) PollEventSubscription(ctx context.Context, input subscription.PollInput) (subscription.PollResult, error) {
	manager, projectID, err := svc.eventSubscriptionManager()
	if err != nil {
		return subscription.PollResult{}, err
	}
	if err := svc.recoverEventLedger(ctx, manager, projectID); err != nil {
		return subscription.PollResult{}, err
	}
	return manager.Poll(ctx, projectID, svc.actor, input)
}

// recoverEventLedger is the only recovery call site. It verifies the original
// task/Incident source record under the same Store.Write lock before publishing a
// pending safe summary. No watcher, UI event, or best-effort post-write path is
// involved.
func (svc *Service) recoverEventLedger(ctx context.Context, manager *subscription.Manager, projectID string) error {
	return manager.Recover(ctx, projectID, svc.actor, func(source subscription.SourceRef) (bool, error) {
		switch source.Kind {
		case subscription.SourceTaskProvenance:
			doc, err := svc.store.Get(source.ResourceID)
			if errors.Is(err, store.ErrNotFound) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if doc.Task.ProjectID != projectID {
				return false, nil
			}
			for _, item := range doc.Provenance {
				if item.ID == source.MutationID {
					return true, nil
				}
			}
			return false, nil
		case subscription.SourceIncident, subscription.SourceIncidentReply, subscription.SourceIncidentStatus:
			incidents := incident.NewScopedWithEvents(svc.store, svc.now, svc.scope.IsStandalone(), nil)
			item, err := incidents.Get(projectID, source.ResourceID)
			if errors.Is(err, incident.ErrNotFound) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			switch source.Kind {
			case subscription.SourceIncident:
				return item.ID == source.MutationID, nil
			case subscription.SourceIncidentReply:
				for _, reply := range item.Replies {
					if reply.ID == source.MutationID {
						return true, nil
					}
				}
				return false, nil
			case subscription.SourceIncidentStatus:
				return string(item.Status) == source.ExpectedStatus && item.UpdatedAt == source.ExpectedUpdatedAt, nil
			}
		}
		return false, fmt.Errorf("%w: unknown recovery source", subscription.ErrInvalidSubscription)
	})
}

// optionalEventSubscriptionManager keeps frozen legacy and deliberately
// cluster-only task flows behaviorally unchanged. A selected stable v2 project
// gets the ledger; a malformed Carbon compatibility selection still fails closed.
func (svc *Service) optionalEventSubscriptionManager() (*subscription.Manager, string, bool, error) {
	if svc == nil || !svc.scope.IsCarbon() || strings.TrimSpace(svc.scope.ProjectID) == "" {
		return nil, "", false, nil
	}
	manager, projectID, err := svc.eventSubscriptionManager()
	if err != nil {
		return nil, "", false, err
	}
	return manager, projectID, true, nil
}

func (svc *Service) prepareTaskEventTx(tx *store.WriteTx, manager *subscription.Manager, projectID string, doc *store.Doc, eventKind string) (*subscription.PreparedEvent, error) {
	if manager == nil {
		return nil, nil
	}
	if doc == nil || doc.Task.ProjectID != projectID || len(doc.Provenance) == 0 {
		return nil, fmt.Errorf("%w: task event source", subscription.ErrInvalidSubscription)
	}
	provenanceID, err := doc.EnsureLastProvenanceID()
	if err != nil {
		return nil, err
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, doc.Provenance[len(doc.Provenance)-1].At)
	if err != nil {
		return nil, fmt.Errorf("%w: task provenance time", subscription.ErrInvalidSubscription)
	}
	prepared, err := manager.PrepareTx(tx, subscription.EventInput{
		ProjectID: projectID, OccurredAt: occurredAt, Module: subscription.ModuleTasks,
		Kind: eventKind, ResourceID: doc.Task.ID, Actor: svc.actor, Status: doc.Task.Status,
		Type: doc.Task.Type, Importance: doc.Task.Importance,
	}, subscription.SourceRef{Kind: subscription.SourceTaskProvenance, ResourceID: doc.Task.ID, MutationID: provenanceID})
	if err != nil {
		return nil, err
	}
	return &prepared, nil
}

func commitPreparedTaskEventTx(tx *store.WriteTx, manager *subscription.Manager, prepared *subscription.PreparedEvent) error {
	if manager == nil || prepared == nil {
		return nil
	}
	_, err := manager.CommitTx(tx, *prepared)
	return err
}
