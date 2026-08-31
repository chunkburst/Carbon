package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"carbon/internal/incident"
	"carbon/internal/mcp"
)

type incidentCreateRequest struct {
	Kind           incident.Kind     `json:"kind"`
	RelatedTaskIDs []string          `json:"relatedTaskIds"`
	Title          string            `json:"title"`
	Body           string            `json:"body"`
	Severity       incident.Severity `json:"severity"`
}

type incidentUpdateRequest struct {
	Status incident.Status `json:"status"`
}

type incidentReplyRequest struct {
	Body string `json:"body"`
}

func (s *Server) handleListIncidents(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	items, err := svc.ListIncidents()
	if err != nil {
		writeIncidentErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Incidents []incident.Incident `json:"incidents"`
	}{Incidents: items})
}

func (s *Server) handleCreateIncident(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req incidentCreateRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	item, err := svc.CreateIncident(r.Context(), incident.CreateInput{Kind: req.Kind, RelatedTaskIDs: append([]string(nil), req.RelatedTaskIDs...), Title: req.Title, Body: req.Body, Severity: req.Severity})
	if err != nil {
		writeIncidentErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) handleGetIncident(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	item, err := svc.GetIncident(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeIncidentErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleUpdateIncident(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req incidentUpdateRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	item, err := svc.UpdateIncidentLifecycle(r.Context(), strings.TrimSpace(r.PathValue("id")), incident.UpdateInput{Status: req.Status})
	if err != nil {
		writeIncidentErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleReplyIncident(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req incidentReplyRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	reply, err := svc.ReplyIncident(r.Context(), strings.TrimSpace(r.PathValue("id")), req.Body)
	if err != nil {
		writeIncidentErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, reply)
}

func writeIncidentErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, incident.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errBody(err))
	case errors.Is(err, mcp.ErrIncidentScopeRequired):
		writeJSON(w, http.StatusBadRequest, errBody(err))
	case errors.Is(err, mcp.ErrIncidentProjectRequired):
		writeJSON(w, http.StatusConflict, errBody(err))
	case errors.Is(err, mcp.ErrIncidentTaskScope):
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
	case errors.Is(err, incident.ErrInvalidIncident):
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
	default:
		writeErr(w, err)
	}
}

// decodeStrictJSON is deliberately local to new management APIs. The older generic
// API decoder is compatibility-tolerant; identity/event/review writes are discovery
// surfaces and should fail closed on misspelled fields rather than silently dropping
// a requested lifecycle or role value.
func decodeStrictJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	if r.Body == nil {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("JSON body is required")))
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errBody(fmt.Errorf("JSON body exceeds %d bytes", maxJSONBodyBytes)))
		} else {
			writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("invalid JSON: %w", err)))
		}
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("invalid JSON: multiple values")))
		return false
	}
	return true
}
