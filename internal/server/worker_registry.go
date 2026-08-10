package server

import (
	"errors"
	"net/http"
	"strings"

	"carbon/internal/home"
)

// workerRegistryActorReq intentionally carries only the immutable actor identity.
// The selected home always comes from a home-only request scope, never from JSON.
type workerRegistryActorReq struct {
	Actor string `json:"actor"`
}

type workerRegistryWorkerResp struct {
	Actor string `json:"actor"`
	home.WorkerRecord
}

type workerRegistryResp struct {
	Worker workerRegistryWorkerResp `json:"worker"`
}

// handleResetWorker clears the selected Worker's derived metric history. It is a
// human-only home administration operation and never resolves a cluster/project store.
func (s *Server) handleResetWorker(w http.ResponseWriter, r *http.Request) {
	if !s.requireHumanWorkerAdmin(w, r) {
		return
	}
	var req workerRegistryActorReq
	if !decode(w, r, &req) {
		return
	}
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	h, err := home.Open(root)
	if err != nil {
		writeWorkerRegistryErr(w, err)
		return
	}
	record, err := h.ResetWorker(req.Actor)
	if err != nil {
		writeWorkerRegistryErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workerRegistryResp{Worker: workerRegistryWorkerResp{Actor: req.Actor, WorkerRecord: record}})
}

// handleDeleteWorker records a Worker tombstone and removes its presentation alias.
// It intentionally does not edit task, provenance, lease, session, or run data. A
// worker can reappear later only through durable task activity after the tombstone.
func (s *Server) handleDeleteWorker(w http.ResponseWriter, r *http.Request) {
	if !s.requireHumanWorkerAdmin(w, r) {
		return
	}
	actor := r.PathValue("actor")
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	h, err := home.Open(root)
	if err != nil {
		writeWorkerRegistryErr(w, err)
		return
	}
	// Validate the independent alias sidecar before adding the tombstone. A corrupt
	// alias document must fail closed instead of producing a half-finished delete.
	if _, err := h.ListWorkerAliases(); err != nil {
		writeWorkerRegistryErr(w, err)
		return
	}
	record, err := h.DeleteWorker(actor)
	if err != nil {
		writeWorkerRegistryErr(w, err)
		return
	}
	if _, err := h.SetWorkerAlias(actor, ""); err != nil {
		writeWorkerRegistryErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workerRegistryResp{Worker: workerRegistryWorkerResp{Actor: actor, WorkerRecord: record}})
}

func (s *Server) requireHumanWorkerAdmin(w http.ResponseWriter, r *http.Request) bool {
	if strings.HasPrefix(s.actorFor(r), "human:") {
		return true
	}
	writeJSON(w, http.StatusForbidden, errBody(errors.New("Worker registry administration requires a human actor")))
	return false
}

func writeWorkerRegistryErr(w http.ResponseWriter, err error) {
	if errors.Is(err, home.ErrInvalidWorkerRegistryActor) || errors.Is(err, home.ErrInvalidWorkerAlias) {
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
		return
	}
	writeHomeErr(w, err)
}
