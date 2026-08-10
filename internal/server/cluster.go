package server

import (
	"errors"
	"net/http"
	"strings"

	"carbon/internal/cluster"
	"carbon/internal/mcp"
	"carbon/internal/store"
)

// clusterResp is intentionally separate from the on-disk manifest: it augments each
// registry entry with a read-only view of that project's own workspace. No project data is
// copied into the cluster manifest.
type clusterResp struct {
	Version         int                 `json:"version"`
	Root            string              `json:"root"`
	Name            string              `json:"name"`
	Initialized     bool                `json:"initialized"`
	LegacyAvailable bool                `json:"legacyAvailable"`
	Projects        []clusterProjectDTO `json:"projects"`
}

type clusterProjectDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	AddedAt     string `json:"addedAt"`
	Legacy      bool   `json:"legacy,omitempty"`
	Offline     bool   `json:"offline,omitempty"`
	Initialized bool   `json:"initialized"`
	Prefix      string `json:"prefix,omitempty"`
	Tasks       int    `json:"tasks"`
	Active      int    `json:"active"`
	Stalled     int    `json:"stalled"`
	Review      int    `json:"review"`
	LiveAgents  int    `json:"liveAgents"`
}

type clusterReq struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

type clusterProjectReq struct {
	ClusterPath string `json:"clusterPath"`
	Path        string `json:"path"`
	Name        string `json:"name"`
}

func (s *Server) handleGetCluster(w http.ResponseWriter, r *http.Request) {
	root, err := s.resolveRoot(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	manifest, exists, err := cluster.Read(root)
	if err != nil {
		writeClusterErr(w, err)
		return
	}
	if !exists {
		initialized, _ := projectWorkspace(root)
		writeJSON(w, http.StatusOK, clusterResp{
			Version:         cluster.Version,
			Root:            root,
			Name:            cluster.DefaultName(root),
			Initialized:     false,
			LegacyAvailable: initialized,
			Projects:        []clusterProjectDTO{},
		})
		return
	}
	writeJSON(w, http.StatusOK, s.clusterResponse(root, manifest))
}

func (s *Server) handlePostCluster(w http.ResponseWriter, r *http.Request) {
	var req clusterReq
	if !decode(w, r, &req) {
		return
	}
	root, err := s.resolveRoot(req.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	legacy, _ := projectWorkspace(root)
	manifest, err := cluster.Ensure(root, req.Name, legacy)
	if err != nil {
		writeClusterErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.clusterResponse(root, manifest))
}

func (s *Server) handlePostClusterProject(w http.ResponseWriter, r *http.Request) {
	var req clusterProjectReq
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ClusterPath) == "" || strings.TrimSpace(req.Path) == "" {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("clusterPath and path are required")))
		return
	}
	root, err := s.resolveRoot(req.ClusterPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	projectRoot, err := s.resolveRoot(req.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	manifest, _, err := cluster.AddProject(root, projectRoot, req.Name)
	if err != nil {
		writeClusterErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.clusterResponse(root, manifest))
}

func (s *Server) handleDeleteClusterProject(w http.ResponseWriter, r *http.Request) {
	root, err := s.resolveRoot(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	manifest, err := cluster.RemoveProject(root, r.PathValue("id"))
	if err != nil {
		writeClusterErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.clusterResponse(root, manifest))
}

func (s *Server) clusterResponse(root string, manifest cluster.Manifest) clusterResp {
	resp := clusterResp{
		Version:     manifest.Version,
		Root:        root,
		Name:        manifest.Name,
		Initialized: true,
		Projects:    make([]clusterProjectDTO, 0, len(manifest.Projects)),
	}
	for _, project := range manifest.Projects {
		view := clusterProjectDTO{
			ID:      project.ID,
			Name:    project.Name,
			Path:    project.Path,
			AddedAt: project.AddedAt,
			Legacy:  project.Legacy,
		}
		projectRoot, err := cluster.ResolveRoot(project.Path)
		if err != nil {
			view.Offline = true
			resp.Projects = append(resp.Projects, view)
			continue
		}
		initialized, prefix := projectWorkspace(projectRoot)
		view.Initialized = initialized
		view.Prefix = prefix
		if initialized {
			view.Tasks, view.Active, view.Stalled, view.Review, view.LiveAgents = s.projectSummary(projectRoot)
		}
		resp.Projects = append(resp.Projects, view)
	}
	return resp
}

// projectWorkspace accepts only a valid existing Carbon config as initialized. It is read
// only: cluster operations must never initialize a selected project as a side effect.
func projectWorkspace(root string) (bool, string) {
	cfg, err := store.New(root).Config()
	if err != nil {
		return false, ""
	}
	return true, cfg.Prefix
}

// projectSummary always reads one project root at a time. In particular, its local service
// and actor set are never keyed by task id alone, so duplicate task ids in distinct projects
// cannot be merged into the same aggregate.
func (s *Server) projectSummary(root string) (tasks, active, stalled, review, liveAgents int) {
	cfg, err := store.New(root).Config()
	if err != nil {
		return 0, 0, 0, 0, 0
	}
	views, err := s.service(root, s.actor).ListWithExecution("", "", nil, "")
	if err != nil {
		return 0, 0, 0, 0, 0
	}
	activeActors := make(map[string]struct{})
	for _, view := range views {
		tasks++
		switch view.ExecutionState {
		case mcp.ExecutionActive:
			active++
			if view.Assignee != "" {
				activeActors[view.Assignee] = struct{}{}
			}
		case mcp.ExecutionStalled:
			stalled++
			if view.Assignee != "" {
				activeActors[view.Assignee] = struct{}{}
			}
		case mcp.ExecutionAwaitingReview:
			review++
		default:
			// A human can move a task into the configured review state without a
			// session. It still belongs in the cluster's "awaiting review" count.
			if cfg.Review() != "" && view.Status == cfg.Review() {
				review++
			}
		}
	}
	return tasks, active, stalled, review, len(activeActors)
}

func writeClusterErr(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	switch {
	case errors.Is(err, cluster.ErrNotInitialized), errors.Is(err, cluster.ErrProjectNotFound):
		code = http.StatusNotFound
	case errors.Is(err, cluster.ErrDuplicateProject):
		code = http.StatusConflict
	}
	writeJSON(w, code, errBody(err))
}
