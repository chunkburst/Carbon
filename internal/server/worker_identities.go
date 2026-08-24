package server

import (
	"errors"
	"net/http"
	"strings"

	"carbon/internal/identity"
	"carbon/internal/mcp"
)

type workerIdentityPutRequest struct {
	Role   string   `json:"role"`
	Types  []string `json:"types"`
	Reason string   `json:"reason"`
}

// handleListWorkerIdentities exposes the selected standalone-project or shared-cluster
// registry. It deliberately goes through svcFor so the same resolved scope is used by
// the web UI, REST callers, and MCP rather than accepting a storage path in JSON.
func (s *Server) handleListWorkerIdentities(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	snapshot, err := svc.ListWorkerIdentities()
	if err != nil {
		writeWorkerIdentityErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleGetWorkerIdentity(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	result, err := svc.GetWorkerIdentity(strings.TrimSpace(r.PathValue("actor")))
	if err != nil {
		writeWorkerIdentityErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handlePutWorkerIdentity is intentionally the only non-self identity management
// adapter. Its Service call rechecks the actor class, so a future transport cannot
// accidentally grant an Agent authority to modify a peer just by reusing this request.
func (s *Server) handlePutWorkerIdentity(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req workerIdentityPutRequest
	if !decode(w, r, &req) {
		return
	}
	result, err := svc.ManageWorkerIdentity(r.Context(), strings.TrimSpace(r.PathValue("actor")), mcp.WorkerIdentityClaimInput{
		Role: req.Role, Types: append([]string(nil), req.Types...), Reason: req.Reason,
	})
	if err != nil {
		writeWorkerIdentityErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeWorkerIdentityErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errBody(err))
	case errors.Is(err, mcp.ErrIdentitySelfOnly):
		writeJSON(w, http.StatusForbidden, errBody(err))
	case errors.Is(err, mcp.ErrIdentityModeDisabled):
		writeJSON(w, http.StatusConflict, errBody(err))
	case errors.Is(err, mcp.ErrIdentityScopeRequired):
		writeJSON(w, http.StatusBadRequest, errBody(err))
	case errors.Is(err, mcp.ErrIdentityAgentRequired),
		errors.Is(err, mcp.ErrIdentityTypeUnknown),
		errors.Is(err, identity.ErrInvalidIdentity),
		errors.Is(err, identity.ErrChangeReasonRequired):
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
	default:
		writeErr(w, err)
	}
}
