package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"carbon/internal/home"
)

// projectClearRequest intentionally accepts only the typed confirmation value. Home
// selection comes from the local server/query scope, never from a JSON field that could
// silently override a desktop's selected Home.
type projectClearRequest struct {
	Name string `json:"name"`
}

type projectClearResponse struct {
	ProjectID       string `json:"projectId"`
	ProjectName     string `json:"projectName"`
	ClusterID       string `json:"clusterId,omitempty"`
	Standalone      bool   `json:"standalone"`
	TasksDeleted    int    `json:"tasksDeleted"`
	TrashDeleted    int    `json:"trashDeleted"`
	SessionsDeleted int    `json:"sessionsDeleted"`
	LiveDeleted     int    `json:"liveDeleted"`
	RunsDeleted     int    `json:"runsDeleted"`
	ReceiptID       string `json:"receiptId"`
	ClearedAt       string `json:"clearedAt"`
	BackupRetained  bool   `json:"backupRetained,omitempty"`
}

// handleClearHomeProjectTaskData is deliberately not an MCP/service operation. It is
// a local human-only catalog administration action guarded by an exact typed project
// name; agents retain ordinary task lifecycle tools but cannot erase a project history.
func (s *Server) handleClearHomeProjectTaskData(w http.ResponseWriter, r *http.Request) {
	actor := s.actorFor(r)
	if !strings.HasPrefix(actor, "human:") {
		writeJSON(w, http.StatusForbidden, errBody(errors.New("project task-data clear requires a human local UI actor")))
		return
	}
	var req projectClearRequest
	if !decodeStrictProjectClear(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("project name confirmation is required")))
		return
	}
	root, err := s.projectClearHomeRoot(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	result, err := home.ClearProjectTaskData(r.Context(), root, home.ClearProjectTaskDataRequest{
		ProjectID: r.PathValue("project"), ConfirmationName: req.Name, Actor: actor,
	})
	if err != nil {
		writeProjectClearErr(w, err)
		return
	}
	data := result.Data
	writeJSON(w, http.StatusOK, projectClearResponse{
		ProjectID: result.Project.ID, ProjectName: result.Project.Name, ClusterID: result.ClusterID, Standalone: result.Standalone,
		TasksDeleted: data.TasksDeleted, TrashDeleted: data.TrashDeleted, SessionsDeleted: data.SessionsDeleted,
		LiveDeleted: data.LiveDeleted, RunsDeleted: data.RunsDeleted, ReceiptID: data.ReceiptID, ClearedAt: data.ClearedAt,
		BackupRetained: data.BackupRetained,
	})
}

// projectClearHomeRoot intentionally allows a server launched with a default selected
// project: the UI uses this top-level stable-id route to administer that selected
// project. Explicit request cluster/project selectors are rejected, as are all legacy
// path aliases, so the target is always resolved through the current Home manifest.
func (s *Server) projectClearHomeRoot(r *http.Request) (string, error) {
	if scopeValue(r, "cluster", "X-Carbon-Cluster") != "" || scopeValue(r, "project", "X-Carbon-Project") != "" {
		return "", errors.New("project task-data clear requires a home-only request")
	}
	if strings.TrimSpace(r.URL.Query().Get("path")) != "" || strings.TrimSpace(r.URL.Query().Get("repo")) != "" {
		return "", errors.New("project task-data clear cannot use a legacy path/repo scope")
	}
	raw := scopeValue(r, "home", "X-Carbon-Home")
	if raw == "" {
		raw = s.defaultHome
	}
	if raw == "" {
		return "", errors.New("home path is required")
	}
	return s.resolveRoot(raw)
}

func decodeStrictProjectClear(w http.ResponseWriter, r *http.Request, target *projectClearRequest) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("invalid project clear request: %w", err)))
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			writeJSON(w, http.StatusBadRequest, errBody(errors.New("invalid project clear request: multiple JSON values")))
		} else {
			writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("invalid project clear request: %w", err)))
		}
		return false
	}
	return true
}

func writeProjectClearErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, home.ErrNotInitialized), errors.Is(err, home.ErrProjectNotFound):
		writeJSON(w, http.StatusNotFound, errBody(err))
	case errors.Is(err, home.ErrProjectClearNameConfirmation):
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
	case errors.Is(err, home.ErrAmbiguousProject), errors.Is(err, home.ErrAmbiguousProjectReference), errors.Is(err, home.ErrLockTimeout):
		writeJSON(w, http.StatusConflict, errBody(err))
	default:
		// Store corruption, a lock timeout, a foreign cross-project reference, and an
		// incomplete filesystem rollback are all fail-closed conflicts. This route
		// never reports a destructive action as an opaque 500 after it has started.
		writeJSON(w, http.StatusConflict, errBody(err))
	}
}
