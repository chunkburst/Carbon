package server

import (
	"net/http"

	"carbon/internal/mcp"
	"carbon/internal/repo"
)

type beginSessionReq struct {
	ExpectedActor  string `json:"expectedActor"`
	Client         string `json:"client"`
	Model          string `json:"model"`
	Worktree       string `json:"worktree"`
	Branch         string `json:"branch"`
	Head           string `json:"head"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type heartbeatReq struct {
	Progress string `json:"progress"`
}

type finishSessionReq struct {
	Summary string `json:"summary"`
	Head    string `json:"head"`
}

type cancelSessionReq struct {
	Reason string `json:"reason"`
}

func (s *Server) handleIdentity(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, svc.Identity())
}

func (s *Server) handleBeginSession(w http.ResponseWriter, r *http.Request) {
	svc, scope, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req beginSessionReq
	if !decode(w, r, &req) {
		return
	}
	// Validate the task's write scope before creating .carbon/sessions or .carbon/live.
	// This prevents a rejected cross-project begin from leaving observable filesystem
	// side effects in the shared Carbon data root.
	if err := svc.AuthorizeTaskWrite(r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	// Carbon's Root is a private metadata store, not a source checkout. The legacy
	// helper also edits .gitignore, so use the directory-only primitive for Carbon.
	var ensureErr error
	if scope.Legacy {
		ensureErr = repo.EnsureSessionDirs(scope.Root)
	} else {
		_, ensureErr = repo.EnsureCarbonDirs(scope.Root, "sessions", "live")
	}
	if ensureErr != nil {
		writeErr(w, ensureErr)
		return
	}
	view, err := svc.BeginSession(r.Context(), mcp.BeginSessionInput{
		TaskID: r.PathValue("id"), ExpectedActor: req.ExpectedActor, Client: req.Client,
		Model: req.Model, Worktree: req.Worktree, Branch: req.Branch, Head: req.Head,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleListTaskSessions(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	views, err := svc.ListSessionsScoped(r.PathValue("id"), "", "", "", includeCluster(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": views})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	views, err := svc.ListSessionsScoped(q.Get("task"), q.Get("actor"), q.Get("status"), q.Get("health"), includeCluster(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": views})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	view, err := svc.GetSessionScoped(r.PathValue("session"), includeCluster(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req heartbeatReq
	if !decode(w, r, &req) {
		return
	}
	view, err := svc.Heartbeat(r.Context(), mcp.HeartbeatInput{
		SessionID: r.PathValue("session"), Progress: req.Progress,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleFinishSession(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req finishSessionReq
	if !decode(w, r, &req) {
		return
	}
	view, err := svc.FinishSession(r.Context(), mcp.FinishSessionInput{
		SessionID: r.PathValue("session"), Summary: req.Summary, Head: req.Head,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleCancelSession(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req cancelSessionReq
	if !decode(w, r, &req) {
		return
	}
	view, err := svc.CancelSession(r.Context(), mcp.CancelSessionInput{
		SessionID: r.PathValue("session"), Reason: req.Reason,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
