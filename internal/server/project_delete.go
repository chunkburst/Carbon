package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"carbon/internal/home"
)

// projectDeleteRequest intentionally has pointer fields so an omitted or null
// deleteData cannot silently become catalog-only deletion. The route accepts exactly the
// explicit human confirmation pair: {"name":"…","deleteData":true|false}.
type projectDeleteRequest struct {
	Name       *string `json:"name"`
	DeleteData *bool   `json:"deleteData"`
}

type projectDeleteResponse struct {
	ProjectID       string `json:"projectId"`
	ProjectName     string `json:"projectName"`
	ClusterID       string `json:"clusterId,omitempty"`
	Standalone      bool   `json:"standalone"`
	DeleteData      bool   `json:"deleteData"`
	TasksDeleted    int    `json:"tasksDeleted,omitempty"`
	TrashDeleted    int    `json:"trashDeleted,omitempty"`
	SessionsDeleted int    `json:"sessionsDeleted,omitempty"`
	LiveDeleted     int    `json:"liveDeleted,omitempty"`
	RunsDeleted     int    `json:"runsDeleted,omitempty"`
	ReceiptID       string `json:"receiptId,omitempty"`
	ClearedAt       string `json:"clearedAt,omitempty"`
}

// handleDeleteHomeProject is a local-human Home administration route. It is deliberately
// separate from MCP and from task lifecycle adapters: deleting a catalog entry is not an
// agent tool, and the source checkout is never a transport-controlled deletion target.
func (s *Server) handleDeleteHomeProject(w http.ResponseWriter, r *http.Request) {
	actor := s.actorFor(r)
	if !isHumanProjectDeleteActor(actor) {
		writeJSON(w, http.StatusForbidden, errBody(errors.New("project deletion requires a human local UI actor")))
		return
	}
	var req projectDeleteRequest
	if !decodeStrictProjectDelete(w, r, &req) {
		return
	}
	if req.Name == nil || req.DeleteData == nil || *req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("project name confirmation and deleteData are required")))
		return
	}
	// Unlike the historical clear endpoint, deletion requires a genuinely Home-only
	// server/request. A selected cluster/project default must not make a local UI route
	// capable of deleting a catalog entry through an inherited task scope.
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	result, err := home.DeleteProject(r.Context(), root, home.DeleteProjectRequest{
		ProjectID: r.PathValue("project"), ConfirmationName: *req.Name, DeleteData: *req.DeleteData, Actor: actor,
	})
	if err != nil {
		writeProjectDeleteErr(w, err)
		return
	}
	response := projectDeleteResponse{
		ProjectID: result.Project.ID, ProjectName: result.Project.Name, ClusterID: result.ClusterID,
		Standalone: result.Standalone, DeleteData: result.DeleteData,
	}
	if result.Data != nil {
		response.TasksDeleted = result.Data.TasksDeleted
		response.TrashDeleted = result.Data.TrashDeleted
		response.SessionsDeleted = result.Data.SessionsDeleted
		response.LiveDeleted = result.Data.LiveDeleted
		response.RunsDeleted = result.Data.RunsDeleted
		response.ReceiptID = result.Data.ReceiptID
		response.ClearedAt = result.Data.ClearedAt
	}
	writeJSON(w, http.StatusOK, response)
}

func isHumanProjectDeleteActor(actor string) bool {
	return strings.HasPrefix(actor, "human:") && strings.TrimPrefix(actor, "human:") != ""
}

func decodeStrictProjectDelete(w http.ResponseWriter, r *http.Request, target *projectDeleteRequest) bool {
	if r.Body == nil {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("project delete request body is required")))
		return false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("invalid project delete request: %w", err)))
		return false
	}
	if len(bytes.TrimSpace(data)) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("project delete request body is required")))
		return false
	}
	if err := rejectDuplicateProjectDeleteJSONKeys(data); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("invalid project delete request: %w", err)))
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("invalid project delete request: %w", err)))
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			writeJSON(w, http.StatusBadRequest, errBody(errors.New("invalid project delete request: multiple JSON values")))
		} else {
			writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("invalid project delete request: %w", err)))
		}
		return false
	}
	return true
}

// rejectDuplicateProjectDeleteJSONKeys rejects duplicate top-level keys before the
// typed decoder applies last-key-wins semantics. Both accepted fields are primitives,
// so nested objects are invalid later and need no independent duplicate-key surface.
func rejectDuplicateProjectDeleteJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return errors.New("project delete request must be a JSON object")
	}
	seen := make(map[string]struct{}, 2)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("project delete request has a non-string object key")
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate JSON key %q", key)
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	token, err = decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok = token.(json.Delim)
	if !ok || delimiter != '}' {
		return errors.New("project delete request has an unterminated JSON object")
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeProjectDeleteErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, home.ErrNotInitialized), errors.Is(err, home.ErrProjectNotFound):
		writeJSON(w, http.StatusNotFound, errBody(err))
	case errors.Is(err, home.ErrProjectDeleteNameConfirmation):
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
	default:
		// Store references/concurrency, unsafe metadata/data paths, Home locks, and a
		// retained recovery receipt are all fail-closed conflicts. The UI may retry only
		// after surfacing the exact local error; this route never reports a started
		// destructive action as an opaque 500.
		writeJSON(w, http.StatusConflict, errBody(err))
	}
}
