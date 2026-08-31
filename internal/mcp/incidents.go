package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"carbon/internal/incident"
	"carbon/internal/subscription"
)

var (
	ErrIncidentScopeRequired   = errors.New("incidents require a Carbon project scope")
	ErrIncidentProjectRequired = errors.New("incidents require a selected project")
	ErrIncidentTaskScope       = errors.New("incident related task is outside the selected project")
)

func (svc *Service) incidentManager() (*incident.Manager, string, error) {
	if svc == nil || svc.store == nil || !svc.scope.IsCarbon() {
		return nil, "", ErrIncidentScopeRequired
	}
	projectID := strings.TrimSpace(svc.scope.ProjectID)
	if projectID == "" {
		return nil, "", ErrIncidentProjectRequired
	}
	var events *subscription.Manager
	if manager, _, err := svc.eventSubscriptionManager(); err == nil {
		events = manager
	}
	return incident.NewScopedWithEvents(svc.store, svc.now, svc.scope.IsStandalone(), events), projectID, nil
}

// CreateIncident records a process-shaped situation. It never writes a task,
// provenance entry, Work Log, or SSE task Event, so opening an Incident cannot make
// a stagnant task look fresh or influence the task-market timeline.
func (svc *Service) CreateIncident(ctx context.Context, input incident.CreateInput) (incident.Incident, error) {
	manager, projectID, err := svc.incidentManager()
	if err != nil {
		return incident.Incident{}, err
	}
	input.ProjectID = projectID
	if err := svc.validateIncidentRelatedTasks(input.RelatedTaskIDs); err != nil {
		return incident.Incident{}, err
	}
	return manager.Create(ctx, svc.actor, input)
}

func (svc *Service) ListIncidents() ([]incident.Incident, error) {
	manager, projectID, err := svc.incidentManager()
	if err != nil {
		return nil, err
	}
	return manager.List(projectID)
}

func (svc *Service) GetIncident(id string) (incident.Incident, error) {
	manager, projectID, err := svc.incidentManager()
	if err != nil {
		return incident.Incident{}, err
	}
	return manager.Get(projectID, id)
}

func (svc *Service) UpdateIncidentLifecycle(ctx context.Context, id string, input incident.UpdateInput) (incident.Incident, error) {
	manager, projectID, err := svc.incidentManager()
	if err != nil {
		return incident.Incident{}, err
	}
	return manager.UpdateLifecycle(ctx, svc.actor, projectID, id, input)
}

func (svc *Service) ReplyIncident(ctx context.Context, id, body string) (incident.Reply, error) {
	manager, projectID, err := svc.incidentManager()
	if err != nil {
		return incident.Reply{}, err
	}
	return manager.Reply(ctx, svc.actor, projectID, id, body)
}

func (svc *Service) validateIncidentRelatedTasks(ids []string) error {
	for _, id := range ids {
		doc, err := svc.GetScoped(id, false)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrIncidentTaskScope, err)
		}
		if doc == nil || doc.Task.ProjectID != svc.scope.ProjectID {
			return fmt.Errorf("%w: %s", ErrIncidentTaskScope, id)
		}
	}
	return nil
}
