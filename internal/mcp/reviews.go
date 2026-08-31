package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"carbon/internal/identity"
	"carbon/internal/review"
)

var (
	ErrReviewScopeRequired     = errors.New("review targets require a Carbon project scope")
	ErrReviewProjectRequired   = errors.New("review targets require a selected project")
	ErrReviewerIneligible      = errors.New("assigned reviewer does not hold the reviewer identity role")
	ErrReviewDecisionForbidden = errors.New("only the assigned reviewer may decide this review target")
	ErrReviewTaskScope         = errors.New("review target task is outside the selected project")
)

func (svc *Service) reviewManager() (*review.Manager, string, error) {
	if svc == nil || svc.store == nil || !svc.scope.IsCarbon() {
		return nil, "", ErrReviewScopeRequired
	}
	projectID := strings.TrimSpace(svc.scope.ProjectID)
	if projectID == "" {
		return nil, "", ErrReviewProjectRequired
	}
	return review.New(svc.store, svc.now), projectID, nil
}

// CreateReviewTarget makes an explicit review assignment. It is independent from
// leases and pending claim approval; tasks remain normal work ownership records.
func (svc *Service) CreateReviewTarget(ctx context.Context, input review.CreateInput) (review.Target, error) {
	manager, projectID, err := svc.reviewManager()
	if err != nil {
		return review.Target{}, err
	}
	input.ProjectID = projectID
	if err := svc.validateReviewTarget(input); err != nil {
		return review.Target{}, err
	}
	if err := svc.requireReviewerRole(input.ReviewerActor); err != nil {
		return review.Target{}, err
	}
	return manager.Create(ctx, svc.actor, input)
}

func (svc *Service) ListReviewTargets() ([]review.Target, error) {
	manager, projectID, err := svc.reviewManager()
	if err != nil {
		return nil, err
	}
	return manager.List(projectID)
}

func (svc *Service) GetReviewTarget(id string) (review.Target, error) {
	manager, projectID, err := svc.reviewManager()
	if err != nil {
		return review.Target{}, err
	}
	return manager.Get(projectID, id)
}

func (svc *Service) DecideReviewTarget(ctx context.Context, id string, input review.DecideInput) (review.Target, error) {
	manager, projectID, err := svc.reviewManager()
	if err != nil {
		return review.Target{}, err
	}
	target, err := manager.Get(projectID, id)
	if err != nil {
		return review.Target{}, err
	}
	if svc.actor != target.ReviewerActor && !identity.IsHuman(svc.actor) && !identity.IsSystem(svc.actor) {
		return review.Target{}, ErrReviewDecisionForbidden
	}
	// Role is checked again at decision time so a human withdrawing reviewer from an
	// Agent identity has an immediate effect. Humans/system retain the management
	// override explicitly promised by the local control-plane model.
	if identity.IsAgent(svc.actor) {
		if err := svc.requireReviewerRole(target.ReviewerActor); err != nil {
			return review.Target{}, err
		}
	}
	return manager.Decide(ctx, svc.actor, projectID, id, input)
}

func (svc *Service) validateReviewTarget(input review.CreateInput) error {
	if strings.TrimSpace(input.TaskID) == "" {
		return fmt.Errorf("%w: taskId is required", ErrReviewTaskScope)
	}
	doc, err := svc.GetScoped(input.TaskID, false)
	if err != nil || doc == nil || doc.Task.ProjectID != svc.scope.ProjectID {
		return fmt.Errorf("%w: %s", ErrReviewTaskScope, input.TaskID)
	}
	switch input.TargetKind {
	case review.TargetPlan:
		if input.TargetID != input.TaskID || input.CheckID != "" {
			return fmt.Errorf("%w: plan targetId must equal taskId and checkId must be empty", review.ErrInvalidReview)
		}
	case review.TargetManualCheck:
		index, err := parseManualCheckIndex(input.CheckID)
		if err != nil || index >= len(doc.Task.Checks) || doc.Task.Checks[index].Cmd != "" {
			return fmt.Errorf("%w: manual_check must address an existing no-cmd task check", review.ErrInvalidReview)
		}
		if input.TargetID != input.TaskID+"#check:"+input.CheckID {
			return fmt.Errorf("%w: manual_check targetId does not match taskId/checkId", review.ErrInvalidReview)
		}
	default:
		return fmt.Errorf("%w: target kind", review.ErrInvalidReview)
	}
	return nil
}

func parseManualCheckIndex(value string) (int, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, errors.New("invalid check index")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, errors.New("invalid check index")
		}
	}
	index, err := strconv.Atoi(value)
	if err != nil || index < 0 {
		return 0, errors.New("invalid check index")
	}
	return index, nil
}

func (svc *Service) requireReviewerRole(actor string) error {
	if !identity.IsAgent(actor) {
		return nil // Human/system reviewers are explicit control-plane assignments.
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
		return fmt.Errorf("%w: %s", ErrReviewerIneligible, actor)
	}
	if err != nil {
		return err
	}
	if !slices.Contains(record.Roles, "reviewer") {
		return fmt.Errorf("%w: %s", ErrReviewerIneligible, actor)
	}
	return nil
}
