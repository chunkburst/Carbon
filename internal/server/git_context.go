package server

import (
	"fmt"
	"net/http"
	"sync"

	"carbon/internal/gitctx"
	"carbon/internal/home"
	"carbon/internal/mcp"
	"carbon/internal/session"
	"carbon/internal/store"
)

type sessionGitContextDTO struct {
	Session mcp.SessionView `json:"session"`
	Context gitctx.Context  `json:"context"`
}

func (s *Server) handleTaskGitContext(w http.ResponseWriter, r *http.Request) {
	svc, scope, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	taskID := r.PathValue("id")
	views, err := svc.ListSessions(taskID, "", "", "")
	if err != nil {
		writeErr(w, err)
		return
	}
	sourceRoot, err := s.executionSourceRoot(scope, taskID)
	if err != nil {
		writeErr(w, err)
		return
	}
	latestHead := latestRunHead(scope.Root, taskID)
	// Each session triggers several git subprocesses; fan out so a task with many
	// sessions does not serialize their (timeout-bounded) latencies.
	out := make([]sessionGitContextDTO, len(views))
	var wg sync.WaitGroup
	for i, view := range views {
		wg.Add(1)
		go func(i int, view mcp.SessionView) {
			defer wg.Done()
			repo := sourceRoot
			if view.Live != nil && view.Live.Worktree != "" {
				repo = view.Live.Worktree
			}
			out[i] = sessionGitContextDTO{
				Session: view,
				Context: gitctx.Session(
					r.Context(),
					repo,
					view.HeadStarted,
					view.HeadFinished,
					view.Branch,
					latestHead,
					view.Status == session.StatusActive,
				),
			}
		}(i, view)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (s *Server) handleSessionGitContext(w http.ResponseWriter, r *http.Request) {
	svc, scope, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	view, err := svc.GetSession(r.PathValue("session"))
	if err != nil {
		writeErr(w, err)
		return
	}
	repo, err := s.executionSourceRoot(scope, view.TaskID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if view.Live != nil && view.Live.Worktree != "" {
		repo = view.Live.Worktree
	}
	ctx := gitctx.Session(
		r.Context(),
		repo,
		view.HeadStarted,
		view.HeadFinished,
		view.Branch,
		latestRunHead(scope.Root, view.TaskID),
		view.Status == session.StatusActive,
	)
	writeJSON(w, http.StatusOK, map[string]any{"session": view, "context": ctx})
}

// executionSourceRoot resolves a task's project source independently of the shared
// cluster metadata root. A missing project is valid for a cluster-wide task but cannot
// be used for Git/session/check execution until a concrete project is selected.
func (s *Server) executionSourceRoot(scope requestScope, taskID string) (string, error) {
	if scope.Legacy {
		return scope.Root, nil
	}
	doc, err := store.New(scope.Root).Get(taskID)
	if err != nil {
		return "", err
	}
	projectID := doc.Task.ProjectID
	if projectID == "" {
		projectID = scope.ProjectID
	}
	if projectID == "" {
		return "", mcp.ErrExecutionProjectRequired
	}
	resolution, err := home.ResolveProject(scope.Home, home.ResolveProjectRequest{
		ClusterID: scope.ClusterID, ProjectID: projectID,
	})
	if err != nil {
		return "", err
	}
	if resolution.Offline {
		return "", fmt.Errorf("%w: Carbon project %s is offline or its source fingerprint no longer matches", mcp.ErrExecutionProjectRequired, projectID)
	}
	return resolution.SourcePath, nil
}
