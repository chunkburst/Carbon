package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"carbon/internal/mcp"
	"carbon/internal/store"
	"carbon/internal/worklog"
)

// workLogServiceFor resolves an explicit Carbon shared-cluster or standalone-project
// scope required by Work Log endpoints. Unlike svcFor it deliberately does not sweep
// task leases: reading or editing a Work Log must not mutate unrelated ownership state.
func (s *Server) workLogServiceFor(w http.ResponseWriter, r *http.Request) (*mcp.Service, requestScope, bool) {
	scope, err := s.resolveScope(r)
	if err != nil {
		writeJSON(w, scopeErrStatus(err), errBody(err))
		return nil, requestScope{}, false
	}
	if scope.Legacy || scope.Mode != "carbon" || scope.Home == "" || (!scope.Standalone && scope.ClusterID == "") || !scope.hasStore() {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("work log operation requires an explicit Carbon home and standalone project or cluster scope")))
		return nil, requestScope{}, false
	}
	if scope.Standalone && includeCluster(r) {
		writeJSON(w, http.StatusUnprocessableEntity, errBody(mcp.ErrStandaloneClusterScope))
		return nil, requestScope{}, false
	}
	return s.scopedService(scope, s.actorFor(r)), scope, true
}

type workLogDTO struct {
	ID         string             `json:"id"`
	Worker     string             `json:"worker"`
	Visibility worklog.Visibility `json:"visibility"`
	Standalone bool               `json:"standalone,omitempty"`
	ClusterID  string             `json:"clusterId"`
	ProjectID  string             `json:"projectId"`
	TaskID     string             `json:"taskId"`
	Title      string             `json:"title"`
	Body       string             `json:"body"`
	Tags       []string           `json:"tags"`
	CreatedAt  string             `json:"createdAt"`
	CreatedBy  string             `json:"createdBy"`
	UpdatedAt  string             `json:"updatedAt"`
	UpdatedBy  string             `json:"updatedBy"`
	Version    string             `json:"version"`
}

type workLogsResponse struct {
	WorkLogs []workLogDTO `json:"worklogs"`
}

type workLogCreateRequest struct {
	Visibility worklog.Visibility `json:"visibility"`
	ProjectID  string             `json:"projectId"`
	TaskID     string             `json:"taskId"`
	Title      string             `json:"title"`
	Body       string             `json:"body"`
	Tags       []string           `json:"tags"`
}

// workLogUpdateRequest deliberately contains only editable fields. Pointers make
// PUT partial: an omitted field is preserved, while an explicit empty string or []
// clears it. Worker, ClusterID, audit data, and Version are never accepted from HTTP.
type workLogUpdateRequest struct {
	Visibility      *worklog.Visibility `json:"visibility"`
	ProjectID       *string             `json:"projectId"`
	TaskID          *string             `json:"taskId"`
	Title           *string             `json:"title"`
	Body            *string             `json:"body"`
	Tags            *[]string           `json:"tags"`
	ExpectedVersion string              `json:"expectedVersion"`
}

func dtoFromWorkLog(item worklog.Log) workLogDTO {
	return workLogDTO{
		ID:         item.ID,
		Worker:     item.Worker,
		Visibility: item.Visibility,
		Standalone: item.Standalone,
		ClusterID:  item.ClusterID,
		ProjectID:  item.ProjectID,
		TaskID:     item.TaskID,
		Title:      item.Title,
		Body:       item.Body,
		Tags:       append([]string{}, item.Tags...),
		CreatedAt:  item.CreatedAt,
		CreatedBy:  item.CreatedBy,
		UpdatedAt:  item.UpdatedAt,
		UpdatedBy:  item.UpdatedBy,
		Version:    item.Version,
	}
}

func workLogDTOs(items []worklog.Log) []workLogDTO {
	out := make([]workLogDTO, 0, len(items))
	for _, item := range items {
		out = append(out, dtoFromWorkLog(item))
	}
	return out
}

// handleListWorkLogs serves GET /api/worklogs. `limit` is deliberately required to
// avoid an accidental unbounded worker-history query; it must be an integer in 1..200.
func (s *Server) handleListWorkLogs(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.workLogServiceFor(w, r)
	if !ok {
		return
	}
	limit, err := workLogListLimit(r)
	if err != nil {
		writeWorkLogErr(w, err)
		return
	}
	items, err := svc.ListWorkLogs(worklog.Filter{
		Worker:     strings.TrimSpace(r.URL.Query().Get("worker")),
		Visibility: worklog.Visibility(strings.TrimSpace(r.URL.Query().Get("visibility"))),
		ProjectID:  strings.TrimSpace(r.URL.Query().Get("projectId")),
		TaskID:     strings.TrimSpace(r.URL.Query().Get("taskId")),
		Limit:      limit,
	})
	if err != nil {
		writeWorkLogErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workLogsResponse{WorkLogs: workLogDTOs(items)})
}

// handleCreateWorkLog serves POST /api/worklogs. The request has no Worker, cluster,
// audit, ID, or Version fields; CreateWorkLog stamps all of those from the bound actor
// and Carbon scope.
func (s *Server) handleCreateWorkLog(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.workLogServiceFor(w, r)
	if !ok {
		return
	}
	var req workLogCreateRequest
	if !decodeWorkLogJSON(w, r, &req) {
		return
	}
	item, err := svc.CreateWorkLog(r.Context(), worklog.Log{
		Visibility: req.Visibility,
		ProjectID:  req.ProjectID,
		TaskID:     req.TaskID,
		Title:      req.Title,
		Body:       req.Body,
		Tags:       append([]string(nil), req.Tags...),
	})
	if err != nil {
		writeWorkLogErr(w, err)
		return
	}
	writeWorkLogJSON(w, http.StatusCreated, item)
}

// handleGetWorkLog serves GET /api/worklogs/{id}.
func (s *Server) handleGetWorkLog(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.workLogServiceFor(w, r)
	if !ok {
		return
	}
	item, err := svc.GetWorkLog(r.PathValue("id"))
	if err != nil {
		writeWorkLogErr(w, err)
		return
	}
	writeWorkLogJSON(w, http.StatusOK, item)
}

// handleUpdateWorkLog serves PUT /api/worklogs/{id}. It is a partial update, but every
// mutation requires a strong current Version/ETag. If-Match takes precedence over the
// JSON expectedVersion field for standard HTTP-client interoperability.
func (s *Server) handleUpdateWorkLog(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.workLogServiceFor(w, r)
	if !ok {
		return
	}
	var req workLogUpdateRequest
	if !decodeWorkLogJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	item, err := svc.PatchWorkLog(r.Context(), id, mcp.WorkLogPatch{
		Visibility: req.Visibility,
		ProjectID:  req.ProjectID,
		TaskID:     req.TaskID,
		Title:      req.Title,
		Body:       req.Body,
		Tags:       req.Tags,
	}, expectedVersion(r, req.ExpectedVersion))
	if errors.Is(err, store.ErrVersionMismatch) {
		writeWorkLogVersionConflict(w, svc, id, err)
		return
	}
	if err != nil {
		writeWorkLogErr(w, err)
		return
	}
	writeWorkLogJSON(w, http.StatusOK, item)
}

// handleDeleteWorkLog serves DELETE /api/worklogs/{id}. It accepts expectedVersion
// from If-Match first and then the (optional) JSON request body for clients that cannot
// set headers. The service rejects an omitted value in either place.
func (s *Server) handleDeleteWorkLog(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.workLogServiceFor(w, r)
	if !ok {
		return
	}
	var req struct {
		ExpectedVersion string `json:"expectedVersion"`
	}
	if r.Body != nil && r.ContentLength != 0 && !decodeWorkLogJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	if err := svc.DeleteWorkLog(r.Context(), id, expectedVersion(r, req.ExpectedVersion)); err != nil {
		if errors.Is(err, store.ErrVersionMismatch) {
			writeWorkLogVersionConflict(w, svc, id, err)
			return
		}
		writeWorkLogErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func workLogListLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 0, fmt.Errorf("%w: limit query parameter is required", worklog.ErrInvalidFilter)
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: limit must be an integer", worklog.ErrInvalidFilter)
	}
	if limit < 1 || limit > worklog.MaxListLimit {
		return 0, fmt.Errorf("%w: limit must be 1..%d", worklog.ErrInvalidFilter, worklog.MaxListLimit)
	}
	return limit, nil
}

// decodeWorkLogJSON rejects unknown fields rather than silently accepting an attempted
// Worker/cluster/audit forgery. It mirrors the API body-size and multiple-value guards
// used by decode while keeping this new contract strict from its first release.
func decodeWorkLogJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if r.Body == nil {
		return true
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil && !errors.Is(err, io.EOF) {
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
		if err == nil {
			writeJSON(w, http.StatusBadRequest, errBody(errors.New("invalid JSON: multiple values")))
			return false
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errBody(fmt.Errorf("JSON body exceeds %d bytes", maxJSONBodyBytes)))
		} else {
			writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("invalid JSON: %w", err)))
		}
		return false
	}
	return true
}

func writeWorkLogJSON(w http.ResponseWriter, code int, item worklog.Log) {
	if tag := item.ETag(); tag != "" {
		w.Header().Set("ETag", tag)
	}
	writeJSON(w, code, dtoFromWorkLog(item))
}

func writeWorkLogVersionConflict(w http.ResponseWriter, svc *mcp.Service, id string, err error) {
	currentVersion := ""
	currentETag := ""
	if version, etag, currentErr := svc.WorkLogVersionCurrent(id); currentErr == nil {
		currentVersion, currentETag = version, etag
	}
	if currentETag != "" {
		w.Header().Set("ETag", currentETag)
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"error": err.Error(),
		"code":  "version_mismatch",
		"conflict": map[string]any{
			"retryable":      true,
			"currentVersion": currentVersion,
			"currentEtag":    currentETag,
		},
	})
}

func writeWorkLogErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mcp.ErrWorkLogNotVisible), errors.Is(err, worklog.ErrNotFound), errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errBody(err))
	case errors.Is(err, mcp.ErrWorkLogOwnerRequired), errors.Is(err, mcp.ErrWorkLogProjectScope), errors.Is(err, mcp.ErrWorkLogClusterScope):
		writeJSON(w, http.StatusForbidden, errBody(err))
	case errors.Is(err, mcp.ErrWorkLogScopeRequired):
		writeJSON(w, http.StatusBadRequest, errBody(err))
	case errors.Is(err, mcp.ErrWorkLogExpectedVersionRequired), errors.Is(err, worklog.ErrInvalidWorkLog), errors.Is(err, worklog.ErrInvalidFilter):
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
	default:
		writeErr(w, err)
	}
}
