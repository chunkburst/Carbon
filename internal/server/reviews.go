package server

import (
	"errors"
	"net/http"
	"strings"

	"carbon/internal/mcp"
	"carbon/internal/review"
)

type reviewCreateRequest struct {
	TargetKind    review.TargetKind `json:"targetKind"`
	TargetID      string            `json:"targetId"`
	TaskID        string            `json:"taskId"`
	CheckID       string            `json:"checkId"`
	ReviewerActor string            `json:"reviewerActor"`
}

type reviewDecideRequest struct {
	Status   review.Status `json:"status"`
	Decision string        `json:"decision"`
}

func (s *Server) handleListReviews(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	items, err := svc.ListReviewTargets()
	if err != nil {
		writeReviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Reviews []review.Target `json:"reviews"`
	}{Reviews: items})
}

func (s *Server) handleCreateReview(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req reviewCreateRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	item, err := svc.CreateReviewTarget(r.Context(), review.CreateInput{TargetKind: req.TargetKind, TargetID: req.TargetID, TaskID: req.TaskID, CheckID: req.CheckID, ReviewerActor: req.ReviewerActor})
	if err != nil {
		writeReviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) handleGetReview(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	item, err := svc.GetReviewTarget(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeReviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleDecideReview(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req reviewDecideRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	item, err := svc.DecideReviewTarget(r.Context(), strings.TrimSpace(r.PathValue("id")), review.DecideInput{Status: req.Status, Decision: req.Decision})
	if err != nil {
		writeReviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func writeReviewErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, review.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errBody(err))
	case errors.Is(err, mcp.ErrReviewScopeRequired):
		writeJSON(w, http.StatusBadRequest, errBody(err))
	case errors.Is(err, mcp.ErrReviewProjectRequired):
		writeJSON(w, http.StatusConflict, errBody(err))
	case errors.Is(err, mcp.ErrReviewTaskScope):
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
	case errors.Is(err, mcp.ErrReviewerIneligible), errors.Is(err, mcp.ErrReviewDecisionForbidden):
		writeJSON(w, http.StatusForbidden, errBody(err))
	case errors.Is(err, review.ErrAlreadyDecided):
		writeJSON(w, http.StatusConflict, errBody(err))
	case errors.Is(err, review.ErrInvalidReview):
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
	default:
		writeErr(w, err)
	}
}
