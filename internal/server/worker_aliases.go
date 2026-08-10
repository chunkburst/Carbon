package server

import (
	"errors"
	"net/http"

	"carbon/internal/home"
)

// workerAliasReq intentionally contains only the immutable actor identity and its
// presentation alias. The selected home comes from the normal home-only request scope
// (default, ?home=, or X-Carbon-Home), never a JSON body path.
type workerAliasReq struct {
	Actor string `json:"actor"`
	Alias string `json:"alias"`
}

type workerAliasesResp struct {
	Aliases map[string]string `json:"aliases"`
}

func (s *Server) handleGetWorkerAliases(w http.ResponseWriter, r *http.Request) {
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	aliases, err := home.ListWorkerAliases(root)
	if err != nil {
		writeWorkerAliasesErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workerAliasesResp{Aliases: aliases})
}

func (s *Server) handlePatchWorkerAlias(w http.ResponseWriter, r *http.Request) {
	var req workerAliasReq
	if !decode(w, r, &req) {
		return
	}
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	aliases, err := home.SetWorkerAlias(root, req.Actor, req.Alias)
	if err != nil {
		writeWorkerAliasesErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workerAliasesResp{Aliases: aliases})
}

func writeWorkerAliasesErr(w http.ResponseWriter, err error) {
	if errors.Is(err, home.ErrInvalidWorkerAlias) {
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
		return
	}
	writeHomeErr(w, err)
}
