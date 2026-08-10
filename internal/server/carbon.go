package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"carbon/internal/compat"
	"carbon/internal/home"
	"carbon/internal/lease"
	"carbon/internal/mcp"
	"carbon/internal/search"
	"carbon/internal/stats"
	"carbon/internal/store"
	"carbon/internal/task"
	"carbon/internal/templates"
	"carbon/internal/trash"
	"carbon/internal/views"
)

const maxBulkTasks = 100

var errLeaseClaimExpectedVersionRequired = errors.New("expectedVersion is required for Carbon v2 lease claims")

type leaseClaimReq struct {
	TTLSeconds      int    `json:"ttlSeconds"`
	RequestID       string `json:"requestId"`
	Reason          string `json:"reason"`
	ExpectedVersion string `json:"expectedVersion"`
}

type leaseRenewReq struct {
	LeaseID         string `json:"leaseId"`
	TTLSeconds      int    `json:"ttlSeconds"`
	ExpectedVersion string `json:"expectedVersion"`
}

type leaseReleaseReq struct {
	LeaseID         string `json:"leaseId"`
	Reason          string `json:"reason"`
	KeepAssignee    bool   `json:"keepAssignee"`
	ExpectedVersion string `json:"expectedVersion"`
}

type leaseReassignReq struct {
	Assignee        string `json:"assignee"`
	Force           bool   `json:"force"`
	Reason          string `json:"reason"`
	ExpectedVersion string `json:"expectedVersion"`
}

type leaseApprovalReq struct {
	RequestID       string `json:"requestId"`
	Approve         bool   `json:"approve"`
	Reason          string `json:"reason"`
	ExpectedVersion string `json:"expectedVersion"`
}

func leaseTTL(seconds int) time.Duration {
	if seconds <= 0 {
		return 0 // lease.Manager applies its safe default.
	}
	return time.Duration(seconds) * time.Second
}

func (s *Server) handleLeaseClaim(w http.ResponseWriter, r *http.Request) {
	var req leaseClaimReq
	if !decode(w, r, &req) {
		return
	}
	// Resolve and validate before svcFor: svcFor opportunistically expires leases, so
	// an invalid Carbon claim must not mutate an unrelated expired lease either.
	scope, err := s.resolveScope(r)
	if err != nil {
		writeJSON(w, scopeErrStatus(err), errBody(err))
		return
	}
	if scope.Mode == "carbon" && !scope.Legacy && s.compatibilityFor(scope).RequestedCompatLayer == compat.StableLayer {
		if strings.TrimSpace(req.Reason) == "" {
			writeErr(w, lease.ErrReasonRequired)
			return
		}
		if strings.TrimSpace(expectedVersion(r, req.ExpectedVersion)) == "" {
			writeJSON(w, http.StatusUnprocessableEntity, errBody(errLeaseClaimExpectedVersionRequired))
			return
		}
	}
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	result, err := svc.ClaimLease(r.Context(), mcp.LeaseClaimInput{
		TaskID: r.PathValue("id"), TTL: leaseTTL(req.TTLSeconds), RequestID: req.RequestID,
		Reason: req.Reason, ExpectedVersion: expectedVersion(r, req.ExpectedVersion),
	})
	if errors.Is(err, store.ErrVersionMismatch) {
		writeVersionConflict(w, svc, r.PathValue("id"), err)
		return
	}
	if result.Doc != nil {
		if tag := result.Doc.ETag(); tag != "" {
			w.Header().Set("ETag", tag)
		}
		body := map[string]any{"task": dtoFromDoc(svc, result.Doc), "pending": result.Pending}
		if result.Request != nil {
			body["request"] = result.Request
		}
		if errors.Is(err, lease.ErrApprovalPending) {
			writeJSON(w, http.StatusAccepted, body)
			return
		}
		if err == nil {
			writeJSON(w, http.StatusOK, body)
			return
		}
	}
	if err != nil {
		writeErr(w, err)
	}
}

func (s *Server) handleLeaseRenew(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req leaseRenewReq
	if !decode(w, r, &req) {
		return
	}
	doc, err := svc.RenewLease(r.Context(), r.PathValue("id"), req.LeaseID, leaseTTL(req.TTLSeconds), expectedVersion(r, req.ExpectedVersion))
	if errors.Is(err, store.ErrVersionMismatch) {
		writeVersionConflict(w, svc, r.PathValue("id"), err)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeTaskJSON(w, http.StatusOK, svc, doc)
}

func (s *Server) handleLeaseRelease(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req leaseReleaseReq
	if !decode(w, r, &req) {
		return
	}
	doc, err := svc.ReleaseLease(r.Context(), r.PathValue("id"), req.LeaseID, req.Reason, expectedVersion(r, req.ExpectedVersion), req.KeepAssignee)
	if errors.Is(err, store.ErrVersionMismatch) {
		writeVersionConflict(w, svc, r.PathValue("id"), err)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeTaskJSON(w, http.StatusOK, svc, doc)
}

func (s *Server) handleLeaseReassign(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req leaseReassignReq
	if !decode(w, r, &req) {
		return
	}
	doc, err := svc.ReassignLease(r.Context(), r.PathValue("id"), req.Assignee, req.Reason, expectedVersion(r, req.ExpectedVersion), req.Force)
	if errors.Is(err, store.ErrVersionMismatch) {
		writeVersionConflict(w, svc, r.PathValue("id"), err)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeTaskJSON(w, http.StatusOK, svc, doc)
}

func (s *Server) handleLeaseApproval(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req leaseApprovalReq
	if !decode(w, r, &req) {
		return
	}
	doc, err := svc.ApproveLeaseClaim(r.Context(), r.PathValue("id"), req.RequestID, req.Reason, expectedVersion(r, req.ExpectedVersion), req.Approve)
	if errors.Is(err, store.ErrVersionMismatch) {
		writeVersionConflict(w, svc, r.PathValue("id"), err)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeTaskJSON(w, http.StatusOK, svc, doc)
}

func (s *Server) handleTrashList(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	entries, err := svc.ListTrash(includeCluster(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

type trashReq struct {
	IDs              []string          `json:"ids"`
	Reason           string            `json:"reason"`
	ExpectedVersions map[string]string `json:"expectedVersions"`
}

func (s *Server) handleTrashMany(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req trashReq
	if !decode(w, r, &req) {
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > maxBulkTasks {
		writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("trash requires 1 to %d task ids", maxBulkTasks)))
		return
	}
	entries, err := svc.TrashTasks(r.Context(), req.IDs, req.Reason, mcp.NormalizeExpectedVersions(req.ExpectedVersions))
	if errors.Is(err, store.ErrVersionMismatch) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "version_mismatch", "conflict": map[string]any{"retryable": true}})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

type restoreTrashReq struct {
	ProjectID       *string `json:"projectId"`
	ExpectedVersion string  `json:"expectedVersion"`
}

func (s *Server) handleTrashRestore(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req restoreTrashReq
	if !decode(w, r, &req) {
		return
	}
	doc, err := svc.RestoreTrash(r.Context(), r.PathValue("id"), req.ProjectID, expectedVersion(r, req.ExpectedVersion))
	if errors.Is(err, store.ErrVersionMismatch) {
		writeVersionConflict(w, svc, r.PathValue("id"), err)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeTaskJSON(w, http.StatusOK, svc, doc)
}

func (s *Server) handleTrashEmpty(w http.ResponseWriter, r *http.Request) {
	if confirmed, _ := strconv.ParseBool(r.URL.Query().Get("confirm")); !confirmed {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("permanent trash empty requires confirm=true")))
		return
	}
	_, scope, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	manager := trash.New(store.New(scope.Root), nil)
	var count int
	var err error
	if scope.Mode == "carbon" && scope.ProjectID != "" {
		// This is an atomic filtered primitive, not a list-then-delete sequence. Shared
		// cluster work is deliberately excluded unless the caller supplies both
		// confirm=true and include_cluster=true.
		count, err = manager.EmptyProject(r.Context(), s.actorFor(r), scope.ProjectID, includeCluster(r))
	} else {
		count, err = manager.Empty(r.Context(), s.actorFor(r))
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"purged": count})
}

type searchResultDTO struct {
	ClusterID  string             `json:"clusterId,omitempty"`
	Task       taskDTO            `json:"task"`
	Score      int                `json:"score"`
	Highlights []search.Highlight `json:"highlights,omitempty"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := search.Query{
		Text: q.Get("q"), Type: q.Get("type"), Importance: q.Get("importance"),
		Status: q.Get("status"), Assignee: q.Get("assignee"), Labels: q["label"],
	}
	scope, err := s.resolveScope(r)
	if err != nil {
		writeJSON(w, scopeErrStatus(err), errBody(err))
		return
	}
	var results []search.Result
	var svc *mcp.Service
	if scope.hasStore() {
		svc = s.scopedService(scope, s.actorFor(r))
		if _, err := svc.ExpireLeases(r.Context()); err != nil {
			writeErr(w, err)
			return
		}
		results, err = svc.Search(query, includeCluster(r))
	} else if scope.Mode == "carbon" && scope.Home != "" {
		results, err = s.searchHome(scope, query, q.Get("cluster"), q.Get("project"))
	} else {
		err = errors.New("search requires legacy path/repo or Carbon home/cluster scope")
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]searchResultDTO, 0, len(results))
	for _, result := range results {
		if result.Doc == nil {
			continue
		}
		var taskDTO taskDTO
		if svc != nil {
			taskDTO = dtoFromDoc(svc, result.Doc)
		} else {
			taskDTO = dtoFromTask(result.Doc.Task, false)
			taskDTO.Body = result.Body
		}
		out = append(out, searchResultDTO{ClusterID: result.ClusterID, Task: taskDTO, Score: result.Score, Highlights: result.Highlights})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// searchHome is the intentional home-level global search: it reads only data roots
// listed in the signed/validated home manifest, never arbitrary project source folders.
func (s *Server) searchHome(scope requestScope, query search.Query, clusterID, projectID string) ([]search.Result, error) {
	clusters, err := home.ListClusters(scope.Home)
	if err != nil {
		return nil, err
	}
	standaloneProjects, err := home.ListProjects(scope.Home)
	if err != nil {
		return nil, err
	}
	if clusterID != "" {
		query.ClusterID = clusterID
	}
	if projectID != "" {
		resolved, err := home.ResolveProjectMetadata(scope.Home, clusterID, projectID)
		if err != nil {
			return nil, err
		}
		if clusterID != "" && resolved.Standalone {
			return nil, home.ErrProjectNotFound
		}
		project := resolved.Project.ID
		query.ProjectID = &project
	}
	sources := make([]search.Source, 0, len(clusters)+len(standaloneProjects))
	for _, cluster := range clusters {
		if clusterID != "" && cluster.ID != clusterID {
			continue
		}
		root, err := home.ClusterDataRoot(scope.Home, cluster.ID)
		if err != nil {
			return nil, err
		}
		sources = append(sources, search.Source{Store: store.New(root), ClusterID: cluster.ID})
	}
	if clusterID == "" {
		for _, project := range standaloneProjects {
			root, err := home.ProjectDataRoot(scope.Home, project.ID)
			if err != nil {
				return nil, err
			}
			sources = append(sources, search.Source{Store: store.New(root)})
		}
	}
	return search.SearchSources(sources, query)
}

type bulkUpdateReq struct {
	IDs              []string          `json:"ids"`
	ExpectedVersions map[string]string `json:"expectedVersions"`
	ProjectID        *string           `json:"projectId"`
	Type             *string           `json:"type"`
	Importance       *string           `json:"importance"`
	Priority         *string           `json:"priority"`
	Labels           *[]string         `json:"labels"`
	Assignee         *string           `json:"assignee"`
	Parent           *string           `json:"parent"`
	Status           *string           `json:"status"`
	Force            bool              `json:"force"`
	Reason           string            `json:"reason"`
}

func (s *Server) handleBulkUpdate(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req bulkUpdateReq
	if !decode(w, r, &req) {
		return
	}
	docs, err := svc.BulkUpdate(r.Context(), store.BulkUpdate{
		IDs: req.IDs, ExpectedVersions: mcp.NormalizeExpectedVersions(req.ExpectedVersions), ProjectID: req.ProjectID,
		Type: req.Type, Importance: req.Importance, Priority: req.Priority, Labels: req.Labels,
		Assignee: req.Assignee, Parent: req.Parent, Status: req.Status, Force: req.Force, Reason: req.Reason,
	})
	if err != nil {
		writeBulkErr(w, err)
		return
	}
	writeBulkTasks(w, svc, docs)
}

type bulkMoveReq struct {
	IDs              []string          `json:"ids"`
	ExpectedVersions map[string]string `json:"expectedVersions"`
	ProjectID        string            `json:"projectId"`
	ClusterWide      bool              `json:"clusterWide"`
	Parent           *string           `json:"parent"`
	Force            bool              `json:"force"`
	Reason           string            `json:"reason"`
}

func (s *Server) handleBulkMove(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req bulkMoveReq
	if !decode(w, r, &req) {
		return
	}
	docs, err := svc.BulkMoveWithAuthorization(r.Context(), store.BulkMove{
		IDs: req.IDs, ExpectedVersions: mcp.NormalizeExpectedVersions(req.ExpectedVersions), ProjectID: req.ProjectID,
		ClusterWide: req.ClusterWide, Parent: req.Parent, Reason: req.Reason,
	}, req.Force)
	if err != nil {
		writeBulkErr(w, err)
		return
	}
	writeBulkTasks(w, svc, docs)
}

func writeBulkTasks(w http.ResponseWriter, svc *mcp.Service, docs []*store.Doc) {
	tasks := make([]taskDTO, 0, len(docs))
	etags := make(map[string]string, len(docs))
	for _, doc := range docs {
		tasks = append(tasks, dtoFromDoc(svc, doc))
		etags[doc.Task.ID] = doc.ETag()
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks, "etags": etags})
}

func writeBulkErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrVersionMismatch) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "version_mismatch", "conflict": map[string]any{"retryable": true}})
		return
	}
	writeErr(w, err)
}

type viewReq struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Query           search.Query `json:"query"`
	ExpectedVersion string       `json:"expectedVersion"`
}

func (s *Server) handleViewsList(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	list, err := svc.ListViews()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"views": list})
}

func (s *Server) handleViewCreate(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req viewReq
	if !decode(w, r, &req) {
		return
	}
	view, err := svc.CreateView(r.Context(), views.View{ID: req.ID, Name: req.Name, Query: req.Query}, includeCluster(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if tag := view.ETag(); tag != "" {
		w.Header().Set("ETag", tag)
	}
	writeJSON(w, http.StatusCreated, view)
}

func (s *Server) handleViewGet(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	view, err := svc.GetView(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if tag := view.ETag(); tag != "" {
		w.Header().Set("ETag", tag)
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleViewSave(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req viewReq
	if !decode(w, r, &req) {
		return
	}
	view, err := svc.SaveView(r.Context(), views.View{ID: r.PathValue("id"), Name: req.Name, Query: req.Query}, expectedVersion(r, req.ExpectedVersion), includeCluster(r))
	if errors.Is(err, store.ErrVersionMismatch) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "version_mismatch", "conflict": map[string]any{"retryable": true}})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if tag := view.ETag(); tag != "" {
		w.Header().Set("ETag", tag)
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleViewDelete(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	if err := svc.DeleteView(r.Context(), r.PathValue("id"), expectedVersion(r, "")); err != nil {
		if errors.Is(err, store.ErrVersionMismatch) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "version_mismatch", "conflict": map[string]any{"retryable": true}})
			return
		}
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleViewApply(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	results, err := svc.ApplyView(r.PathValue("id"), includeCluster(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]searchResultDTO, 0, len(results))
	for _, result := range results {
		if result.Doc != nil {
			out = append(out, searchResultDTO{Task: dtoFromDoc(svc, result.Doc), Score: result.Score, Highlights: result.Highlights})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

type templateReq struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Title           string       `json:"title"`
	Body            string       `json:"body"`
	ProjectID       string       `json:"project_id"`
	ClusterWide     bool         `json:"cluster_wide"`
	Type            string       `json:"type"`
	Importance      string       `json:"importance"`
	Priority        string       `json:"priority"`
	Labels          []string     `json:"labels"`
	Deps            []string     `json:"deps"`
	Checks          []task.Check `json:"checks"`
	Parent          string       `json:"parent"`
	ExpectedVersion string       `json:"expectedVersion"`
}

func templateFromReq(req templateReq, id string) templates.Template {
	if id == "" {
		id = req.ID
	}
	return templates.Template{ID: id, Name: req.Name, Title: req.Title, Body: req.Body,
		ProjectID: req.ProjectID, ClusterWide: req.ClusterWide, Type: req.Type, Importance: req.Importance,
		Priority: req.Priority, Labels: req.Labels, Deps: req.Deps, Checks: req.Checks, Parent: req.Parent}
}

func (s *Server) handleTemplatesList(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	list, err := svc.ListTemplates()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": list})
}

func (s *Server) handleTemplateCreate(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req templateReq
	if !decode(w, r, &req) {
		return
	}
	template, err := svc.CreateTemplate(r.Context(), templateFromReq(req, ""))
	if err != nil {
		writeErr(w, err)
		return
	}
	if tag := template.ETag(); tag != "" {
		w.Header().Set("ETag", tag)
	}
	writeJSON(w, http.StatusCreated, template)
}

func (s *Server) handleTemplateGet(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	template, err := svc.GetTemplate(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if tag := template.ETag(); tag != "" {
		w.Header().Set("ETag", tag)
	}
	writeJSON(w, http.StatusOK, template)
}

func (s *Server) handleTemplateSave(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req templateReq
	if !decode(w, r, &req) {
		return
	}
	template, err := svc.SaveTemplate(r.Context(), templateFromReq(req, r.PathValue("id")), expectedVersion(r, req.ExpectedVersion))
	if errors.Is(err, store.ErrVersionMismatch) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "version_mismatch", "conflict": map[string]any{"retryable": true}})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if tag := template.ETag(); tag != "" {
		w.Header().Set("ETag", tag)
	}
	writeJSON(w, http.StatusOK, template)
}

func (s *Server) handleTemplateDelete(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	if err := svc.DeleteTemplate(r.Context(), r.PathValue("id"), expectedVersion(r, "")); err != nil {
		if errors.Is(err, store.ErrVersionMismatch) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "version_mismatch", "conflict": map[string]any{"retryable": true}})
			return
		}
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type instantiateTemplateReq struct {
	ExpectedVersion string        `json:"expectedVersion"`
	Title           *string       `json:"title"`
	Body            *string       `json:"body"`
	ProjectID       *string       `json:"projectId"`
	Type            *string       `json:"type"`
	Importance      *string       `json:"importance"`
	Priority        *string       `json:"priority"`
	Labels          *[]string     `json:"labels"`
	Deps            *[]string     `json:"deps"`
	Parent          *string       `json:"parent"`
	Checks          *[]task.Check `json:"checks"`
}

func (s *Server) handleTemplateInstantiate(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	var req instantiateTemplateReq
	if !decode(w, r, &req) {
		return
	}
	doc, err := svc.InstantiateTemplate(r.Context(), templates.InstantiateInput{
		TemplateID: r.PathValue("id"), Actor: s.actorFor(r), ExpectedVersion: expectedVersion(r, req.ExpectedVersion),
		Title: req.Title, Body: req.Body, ProjectID: req.ProjectID, Type: req.Type, Importance: req.Importance,
		Priority: req.Priority, Labels: req.Labels, Deps: req.Deps, Parent: req.Parent, Checks: req.Checks,
	})
	if errors.Is(err, store.ErrVersionMismatch) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "version_mismatch", "conflict": map[string]any{"retryable": true}})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeTaskJSON(w, http.StatusCreated, svc, doc)
}

func (s *Server) handleWorkerStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope, err := s.resolveScope(r)
	if err != nil {
		writeJSON(w, scopeErrStatus(err), errBody(err))
		return
	}
	var report stats.Report
	if scope.Mode == "carbon" && scope.Home != "" {
		if scope.Standalone && includeCluster(r) {
			writeErr(w, mcp.ErrStandaloneClusterScope)
			return
		}
		// Carbon Worker lifecycle metadata is home-global, so selected cluster and
		// project reports deliberately route through the same home aggregator as an
		// all-cluster report. This keeps reset/delete cutoffs consistent everywhere.
		wantedCluster := scope.ClusterID
		wantedProject := q.Get("project")
		if scope.ProjectID != "" {
			if includeCluster(r) {
				wantedProject = ""
			} else {
				wantedProject = scope.ProjectID
			}
		}
		report, err = s.homeWorkerStats(r.Context(), scope.Home, wantedCluster, wantedProject)
	} else if scope.hasStore() {
		svc := s.scopedService(scope, s.actorFor(r))
		if _, expireErr := svc.ExpireLeases(r.Context()); expireErr != nil {
			err = expireErr
		} else {
			report, err = svc.WorkerStats(includeCluster(r))
		}
	} else {
		err = errors.New("worker stats require legacy path/repo or Carbon home/cluster scope")
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": scopeDTOFrom(scope), "workers": report.Workers, "aggregate": report.Aggregate})
}

func (s *Server) resolveHomeOnlyScope(r *http.Request) (requestScope, error) {
	if strings.TrimSpace(r.URL.Query().Get("path")) != "" || strings.TrimSpace(r.URL.Query().Get("repo")) != "" {
		return requestScope{}, errors.New("legacy path/repo cannot be combined with global Carbon filters")
	}
	homeRoot := scopeValue(r, "home", "X-Carbon-Home")
	if homeRoot == "" {
		homeRoot = s.defaultHome
	}
	if homeRoot == "" {
		return requestScope{}, errors.New("global Carbon filter requires a home path")
	}
	resolved, err := s.resolveRoot(homeRoot)
	if err != nil {
		return requestScope{}, err
	}
	return requestScope{Mode: "carbon", Home: resolved}, nil
}

func (s *Server) homeWorkerStats(ctx context.Context, homeRoot, wantedCluster, wantedProject string) (stats.Report, error) {
	clusters, err := home.ListClusters(homeRoot)
	if err != nil {
		return stats.Report{}, err
	}
	standaloneProjects, err := home.ListProjects(homeRoot)
	if err != nil {
		return stats.Report{}, err
	}
	// Resolve a project through the Home registry before iterating. This prevents a
	// home-wide project filter from accidentally aggregating cluster-wide work from
	// another pool and distinguishes an isolated root from a shared cluster surface.
	wantedStandalone := false
	if wantedProject != "" {
		resolved, err := home.ResolveProjectMetadata(homeRoot, wantedCluster, wantedProject)
		if err != nil {
			return stats.Report{}, err
		}
		if resolved.Standalone {
			if wantedCluster != "" {
				return stats.Report{}, home.ErrProjectNotFound
			}
			wantedStandalone = true
			wantedProject = resolved.Project.ID
		} else {
			wantedCluster = resolved.Cluster.ID
			wantedProject = resolved.Project.ID
		}
	}
	if wantedCluster != "" {
		found := false
		for _, cluster := range clusters {
			if cluster.ID == wantedCluster {
				found = true
				break
			}
		}
		if !found {
			return stats.Report{}, home.ErrClusterNotFound
		}
	}
	type statsSource struct {
		clusterID  string
		projectID  string
		standalone bool
		docs       []*store.Doc
		rules      task.Rules
	}
	sources := make([]statsSource, 0, len(clusters)+len(standaloneProjects))
	activity := map[string]time.Time{}
	loadSource := func(root string, source statsSource) error {
		if _, err := lease.New(store.New(root), nil, 0).Expire(ctx); err != nil {
			return err
		}
		st := store.New(root)
		docs, err := st.ListDocs()
		if err != nil {
			return err
		}
		cfg, err := st.Config()
		if err != nil {
			return err
		}
		for actor, at := range stats.Activities(docs) {
			if previous, exists := activity[actor]; !exists || at.After(previous) {
				activity[actor] = at
			}
		}
		source.docs = docs
		source.rules = task.Rules{Initial: cfg.Initial, Closed: cfg.Closed, States: cfg.States, Review: cfg.Review()}
		sources = append(sources, source)
		return nil
	}
	for _, cluster := range clusters {
		if wantedStandalone || (wantedCluster != "" && cluster.ID != wantedCluster) {
			continue
		}
		root, err := home.ClusterDataRoot(homeRoot, cluster.ID)
		if err != nil {
			return stats.Report{}, err
		}
		if err := loadSource(root, statsSource{clusterID: cluster.ID}); err != nil {
			return stats.Report{}, err
		}
	}
	if wantedCluster == "" {
		for _, project := range standaloneProjects {
			if wantedProject != "" && project.ID != wantedProject {
				continue
			}
			root, err := home.ProjectDataRoot(homeRoot, project.ID)
			if err != nil {
				return stats.Report{}, err
			}
			if err := loadSource(root, statsSource{projectID: project.ID, standalone: true}); err != nil {
				return stats.Report{}, err
			}
		}
	}
	if wantedStandalone && len(sources) == 0 {
		return stats.Report{}, home.ErrProjectNotFound
	}
	registry, err := home.ReconcileWorkerActivity(homeRoot, activity)
	if err != nil {
		return stats.Report{}, err
	}
	cutoffs, err := workerRegistryCutoffs(registry)
	if err != nil {
		return stats.Report{}, err
	}

	workers := map[string]stats.Worker{}
	aggregate := stats.Aggregate{}
	for _, source := range sources {
		filter := stats.Filter{WorkerCutoffs: cutoffs}
		if source.standalone {
			project := source.projectID
			filter.Scope = stats.ScopeProject
			filter.ProjectID = &project
		} else {
			filter.Scope = stats.ScopeCluster
			filter.ClusterID = source.clusterID
			if wantedProject != "" {
				project := wantedProject
				filter.ProjectID = &project
			}
		}
		report := stats.Compute(source.docs, source.rules, filter)
		for _, worker := range report.Workers {
			workers[worker.Actor] = mergeWorkerStats(workers[worker.Actor], worker)
		}
		aggregate = mergeWorkerAggregate(aggregate, report.Aggregate)
	}
	out := make([]stats.Worker, 0, len(workers))
	for _, worker := range workers {
		out = append(out, worker)
	}
	slices.SortFunc(out, func(left, right stats.Worker) int { return strings.Compare(left.Actor, right.Actor) })
	return stats.Report{Workers: out, Aggregate: aggregate}, nil
}

func mergeWorkerStats(left, right stats.Worker) stats.Worker {
	if left.Actor == "" {
		return right
	}
	totalSamples := left.CycleSamples + right.CycleSamples
	cycleSeconds := left.AverageCycleSeconds*float64(left.CycleSamples) + right.AverageCycleSeconds*float64(right.CycleSamples)
	left.Active += right.Active
	left.Completed += right.Completed
	left.Reopened += right.Reopened
	left.CycleSamples = totalSamples
	if left.CompletedByPriority == nil {
		left.CompletedByPriority = map[string]int{}
	}
	for priority, count := range right.CompletedByPriority {
		left.CompletedByPriority[priority] += count
	}
	if totalSamples > 0 {
		left.AverageCycleSeconds = cycleSeconds / float64(totalSamples)
		left.AverageCycle = time.Duration(left.AverageCycleSeconds * float64(time.Second))
	}
	if left.Completed > 0 {
		left.ReopenRate = float64(left.Reopened) / float64(left.Completed)
	}
	if right.LastActivity != nil && (left.LastActivity == nil || right.LastActivity.After(*left.LastActivity)) {
		at := right.LastActivity.UTC()
		left.LastActivity = &at
	}
	left.RecentWork = mergeRecentWorkerWork(left.RecentWork, right.RecentWork)
	return left
}

func mergeWorkerAggregate(left, right stats.Aggregate) stats.Aggregate {
	totalSamples := left.CycleSamples + right.CycleSamples
	cycleSeconds := left.AverageCycleSeconds*float64(left.CycleSamples) + right.AverageCycleSeconds*float64(right.CycleSamples)
	left.TaskCount += right.TaskCount
	left.Active += right.Active
	left.Completed += right.Completed
	left.Open += right.Open
	left.Reopened += right.Reopened
	left.CycleSamples = totalSamples
	if totalSamples > 0 {
		left.AverageCycleSeconds = cycleSeconds / float64(totalSamples)
	}
	if left.Completed > 0 {
		left.ReopenRate = float64(left.Reopened) / float64(left.Completed)
	}
	return left
}

func mergeRecentWorkerWork(left, right []stats.RecentWork) []stats.RecentWork {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	type entry struct {
		item stats.RecentWork
		at   time.Time
	}
	byTask := map[string]entry{}
	add := func(item stats.RecentWork) {
		at, err := time.Parse(time.RFC3339, item.At)
		if err != nil {
			return
		}
		key := item.TaskID + "\x00" + item.ProjectID
		existing, exists := byTask[key]
		if !exists || at.After(existing.at) || (at.Equal(existing.at) && (item.Activity < existing.item.Activity || (item.Activity == existing.item.Activity && item.Title < existing.item.Title))) {
			byTask[key] = entry{item: item, at: at.UTC()}
		}
	}
	for _, item := range left {
		add(item)
	}
	for _, item := range right {
		add(item)
	}
	entries := make([]entry, 0, len(byTask))
	for _, item := range byTask {
		entries = append(entries, item)
	}
	slices.SortFunc(entries, func(left, right entry) int {
		if !left.at.Equal(right.at) {
			if left.at.After(right.at) {
				return -1
			}
			return 1
		}
		if left.item.TaskID != right.item.TaskID {
			return strings.Compare(left.item.TaskID, right.item.TaskID)
		}
		return strings.Compare(left.item.Activity, right.item.Activity)
	})
	if len(entries) > stats.MaxRecentWork {
		entries = entries[:stats.MaxRecentWork]
	}
	out := make([]stats.RecentWork, 0, len(entries))
	for _, item := range entries {
		out = append(out, item.item)
	}
	return out
}

func workerRegistryCutoffs(registry map[string]home.WorkerRecord) (map[string]stats.WorkerCutoff, error) {
	if len(registry) == 0 {
		return nil, nil
	}
	cutoffs := make(map[string]stats.WorkerCutoff, len(registry))
	for actor, record := range registry {
		cutoff := stats.WorkerCutoff{}
		var err error
		if record.ResetAt != "" {
			cutoff.ResetAt, err = time.Parse(time.RFC3339, record.ResetAt)
			if err != nil {
				return nil, fmt.Errorf("parse Worker reset cutoff for %s: %w", actor, err)
			}
		}
		if record.DeletedAt != "" {
			cutoff.DeletedAt, err = time.Parse(time.RFC3339, record.DeletedAt)
			if err != nil {
				return nil, fmt.Errorf("parse Worker delete cutoff for %s: %w", actor, err)
			}
		}
		if !cutoff.ResetAt.IsZero() || !cutoff.DeletedAt.IsZero() {
			cutoffs[actor] = cutoff
		}
	}
	return cutoffs, nil
}

type createTypeReq struct {
	Key         string `json:"key"`
	DisplayName string `json:"displayName"`
}

func (s *Server) handleListTypes(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	keys, custom, err := svc.ListTaskTypes()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"types": keys, "custom": custom})
}

func (s *Server) handleCreateType(w http.ResponseWriter, r *http.Request) {
	_, scope, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	if scope.Mode == "carbon" && scope.ProjectID != "" {
		writeErr(w, mcp.ErrProjectWriteScope)
		return
	}
	var req createTypeReq
	if !decode(w, r, &req) {
		return
	}
	definition, err := store.New(scope.Root).CreateTaskTypeWithDisplayName(r.Context(), s.actorFor(r), strings.TrimSpace(req.Key), strings.TrimSpace(req.DisplayName), time.Now())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"definition": definition})
}
