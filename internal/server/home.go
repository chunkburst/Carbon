package server

import (
	"errors"
	"net/http"
	"strings"

	"carbon/internal/home"
	"carbon/internal/repo"
	"carbon/internal/store"
)

// homeResp deliberately returns the public home manifest shape. The manifest holds
// stable IDs while task data stays in each cluster's private data root, so clients
// never need to infer identity from an editable source path.
type homeResp struct {
	CarbonVersion string         `json:"carbonVersion"`
	Capabilities  []string       `json:"capabilities"`
	Root          string         `json:"root"`
	Initialized   bool           `json:"initialized"`
	Manifest      *home.Manifest `json:"manifest,omitempty"`
}

type homeReq struct {
	Home string `json:"home"`
}

type createHomeClusterReq struct {
	Home        string   `json:"home"`
	Name        string   `json:"name"`
	Slug        string   `json:"slug"`
	Description string   `json:"description"`
	SlugAliases []string `json:"slugAliases"`
	Prefix      string   `json:"prefix"`
}

type updateHomeClusterReq struct {
	Home        string    `json:"home"`
	Name        *string   `json:"name"`
	Slug        *string   `json:"slug"`
	Description *string   `json:"description"`
	SlugAliases *[]string `json:"slugAliases"`
}

type addHomeProjectReq struct {
	Home        string           `json:"home"`
	Name        string           `json:"name"`
	Slug        string           `json:"slug"`
	Description string           `json:"description"`
	SlugAliases []string         `json:"slugAliases"`
	Kind        home.ProjectKind `json:"kind"`
	SourcePath  string           `json:"sourcePath"`
}

type relinkHomeProjectReq struct {
	Home       string `json:"home"`
	SourcePath string `json:"sourcePath"`
}

// detachHomeProjectReq keeps the potentially destructive shared-store copy opt-in
// explicit. The default detach path is allowed only when the source cluster has one
// project, and the optional reason is retained in Home's durable receipt.
type detachHomeProjectReq struct {
	Home                 string `json:"home"`
	AllowSharedStoreCopy bool   `json:"allowSharedStoreCopy"`
	Reason               string `json:"reason"`
}

// updateHomeProjectReq intentionally exposes only display metadata. Source binding is
// owned by the explicit relink route so a generic PATCH cannot silently change an
// execution root.
type updateHomeProjectReq struct {
	Home        string            `json:"home"`
	Name        *string           `json:"name"`
	Slug        *string           `json:"slug"`
	Description *string           `json:"description"`
	SlugAliases *[]string         `json:"slugAliases"`
	Kind        *home.ProjectKind `json:"kind"`
}

func (s *Server) homeRoot(r *http.Request, bodyHome string) (string, error) {
	// Home management changes the registry and can import/relink every cluster. It must
	// never inherit a selected task-cluster/project scope from a desktop or MCP launch;
	// callers use a home-only server/request for these administrative endpoints.
	if scopeValue(r, "cluster", "X-Carbon-Cluster") != "" ||
		scopeValue(r, "project", "X-Carbon-Project") != "" ||
		s.defaultCluster != "" || s.defaultProject != "" {
		return "", errors.New("home management requires a home-only scope without cluster or project")
	}
	if strings.TrimSpace(r.URL.Query().Get("path")) != "" || strings.TrimSpace(r.URL.Query().Get("repo")) != "" {
		return "", errors.New("home management cannot use a legacy path/repo scope")
	}
	raw := strings.TrimSpace(bodyHome)
	if raw == "" {
		raw = scopeValue(r, "home", "X-Carbon-Home")
	}
	if raw == "" {
		raw = s.defaultHome
	}
	if raw == "" {
		return "", errors.New("home path is required")
	}
	return s.resolveRoot(raw)
}

func (s *Server) handleGetHome(w http.ResponseWriter, r *http.Request) {
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	h, err := home.Open(root)
	if errors.Is(err, home.ErrNotInitialized) {
		writeJSON(w, http.StatusOK, homeResp{CarbonVersion: "0.4", Capabilities: carbonCapabilities(), Root: root})
		return
	}
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	manifest, err := h.Manifest()
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, homeResp{
		CarbonVersion: "0.4", Capabilities: carbonCapabilities(), Root: root,
		Initialized: true, Manifest: &manifest,
	})
}

func (s *Server) handleEnsureHome(w http.ResponseWriter, r *http.Request) {
	var req homeReq
	if !decode(w, r, &req) {
		return
	}
	root, err := s.homeRoot(r, req.Home)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	h, err := home.Ensure(root)
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	manifest, err := h.Manifest()
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, homeResp{
		CarbonVersion: "0.4", Capabilities: carbonCapabilities(), Root: root,
		Initialized: true, Manifest: &manifest,
	})
}

func (s *Server) handleListHomeClusters(w http.ResponseWriter, r *http.Request) {
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	clusters, err := home.ListClusters(root)
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clusters": clusters})
}

// handleListHomeProjects lists only top-level standalone projects. Projects nested
// under a cluster remain discoverable through their cluster record and retain the
// legacy nested route family below.
func (s *Server) handleListHomeProjects(w http.ResponseWriter, r *http.Request) {
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	projects, err := home.ListProjects(root)
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

// handleCreateHomeProject creates a top-level standalone project. Home owns its
// private data-root initialization so this adapter never writes task metadata into a
// source checkout or turns the new project into a shared cluster pool.
func (s *Server) handleCreateHomeProject(w http.ResponseWriter, r *http.Request) {
	var req addHomeProjectReq
	if !decode(w, r, &req) {
		return
	}
	root, err := s.homeRoot(r, req.Home)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	project, err := home.AddStandaloneProject(root, home.AddProjectRequest{
		Name: req.Name, Slug: req.Slug, Description: req.Description,
		SlugAliases: req.SlugAliases, Kind: req.Kind, SourcePath: req.SourcePath,
	})
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, project)
}

func (s *Server) handleGetHomeProject(w http.ResponseWriter, r *http.Request) {
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	resolved, err := home.ResolveProjectMetadata(root, "", r.PathValue("project"))
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	if !resolved.Standalone {
		writeJSON(w, http.StatusNotFound, errBody(home.ErrProjectNotFound))
		return
	}
	writeJSON(w, http.StatusOK, resolved.Project)
}

func (s *Server) handleUpdateStandaloneHomeProject(w http.ResponseWriter, r *http.Request) {
	var req updateHomeProjectReq
	if !decode(w, r, &req) {
		return
	}
	root, err := s.homeRoot(r, req.Home)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	project, err := home.UpdateStandaloneProject(root, r.PathValue("project"), home.UpdateProjectRequest{
		Name: req.Name, Slug: req.Slug, Description: req.Description,
		SlugAliases: req.SlugAliases, Kind: req.Kind,
	})
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (s *Server) handleRelinkStandaloneHomeProject(w http.ResponseWriter, r *http.Request) {
	var req relinkHomeProjectReq
	if !decode(w, r, &req) {
		return
	}
	root, err := s.homeRoot(r, req.Home)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	project, err := home.RelinkStandaloneProject(root, r.PathValue("project"), req.SourcePath)
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (s *Server) handleCreateHomeCluster(w http.ResponseWriter, r *http.Request) {
	var req createHomeClusterReq
	if !decode(w, r, &req) {
		return
	}
	root, err := s.homeRoot(r, req.Home)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	cluster, err := home.CreateCluster(root, home.CreateClusterRequest{
		Name: req.Name, Slug: req.Slug, Description: req.Description,
		SlugAliases: req.SlugAliases, Prefix: req.Prefix,
	})
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	dataRoot, err := home.ClusterDataRoot(root, cluster.ID)
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	// A Carbon cluster's store is private metadata, not a source repository. Use the
	// data-root initializer so no workflow files, gitignore, or agent instructions
	// are written into the user-selected project source folder.
	if err := initCarbonDataRoot(dataRoot, req.Prefix); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, cluster)
}

// initCarbonDataRoot creates the private physical store without touching a source
// project. A cluster's config must not carry a default project id: `project_id` is a
// per-task label in a shared pool, and an empty label is meaningful for a genuinely
// cluster-wide task.
func initCarbonDataRoot(dataRoot, prefix string) error {
	if err := repo.InitDataRoot(dataRoot, prefix); err != nil {
		return err
	}
	st := store.New(dataRoot)
	cfg, err := st.Config()
	if err != nil {
		return err
	}
	if cfg.ProjectID == "" {
		return nil
	}
	cfg.ProjectID = ""
	return st.SaveConfig(cfg)
}

// validateStandaloneDataRoot confirms the Home-created private store remains bound to
// its stable project id. Unlike initCarbonDataRoot it never clears or recreates the
// binding: a standalone project must not be converted into a shared cluster pool by a
// transport-side auto-initializer.
func validateStandaloneDataRoot(dataRoot, projectID string) error {
	if strings.TrimSpace(projectID) == "" {
		return errors.New("standalone project id is required")
	}
	cfg, err := store.New(dataRoot).Config()
	if err != nil {
		return err
	}
	if cfg.ProjectID != projectID {
		return errors.New("standalone data root project binding does not match selected project")
	}
	return nil
}

func (s *Server) handleGetHomeCluster(w http.ResponseWriter, r *http.Request) {
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	clusters, err := home.ListClusters(root)
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	for _, cluster := range clusters {
		if cluster.ID == r.PathValue("cluster") {
			writeJSON(w, http.StatusOK, cluster)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, errBody(home.ErrClusterNotFound))
}

func (s *Server) handleUpdateHomeCluster(w http.ResponseWriter, r *http.Request) {
	var req updateHomeClusterReq
	if !decode(w, r, &req) {
		return
	}
	root, err := s.homeRoot(r, req.Home)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	cluster, err := home.UpdateCluster(root, r.PathValue("cluster"), home.UpdateClusterRequest{
		Name: req.Name, Slug: req.Slug, Description: req.Description,
		SlugAliases: req.SlugAliases,
	})
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cluster)
}

func (s *Server) handleAddHomeProject(w http.ResponseWriter, r *http.Request) {
	var req addHomeProjectReq
	if !decode(w, r, &req) {
		return
	}
	root, err := s.homeRoot(r, req.Home)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	project, err := home.AddProject(root, r.PathValue("cluster"), home.AddProjectRequest{
		Name: req.Name, Slug: req.Slug, Description: req.Description,
		SlugAliases: req.SlugAliases, Kind: req.Kind, SourcePath: req.SourcePath,
	})
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, project)
}

func (s *Server) handleRelinkHomeProject(w http.ResponseWriter, r *http.Request) {
	var req relinkHomeProjectReq
	if !decode(w, r, &req) {
		return
	}
	root, err := s.homeRoot(r, req.Home)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	project, err := home.Relink(root, r.PathValue("cluster"), r.PathValue("project"), req.SourcePath)
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

// handleDetachHomeProject moves one legacy nested project into an isolated top-level
// store. Home owns the copy, receipt, and source-store safety checks so this transport
// cannot accidentally delete shared task history or infer a review approval.
func (s *Server) handleDetachHomeProject(w http.ResponseWriter, r *http.Request) {
	var req detachHomeProjectReq
	if !decode(w, r, &req) {
		return
	}
	root, err := s.homeRoot(r, req.Home)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	result, err := home.DetachProject(root, r.PathValue("cluster"), r.PathValue("project"), home.DetachProjectOptions{
		AllowSharedStoreCopy: req.AllowSharedStoreCopy,
		Reason:               req.Reason,
	})
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleUpdateHomeProject(w http.ResponseWriter, r *http.Request) {
	var req updateHomeProjectReq
	if !decode(w, r, &req) {
		return
	}
	root, err := s.homeRoot(r, req.Home)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	project, err := home.UpdateProject(root, r.PathValue("cluster"), r.PathValue("project"), home.UpdateProjectRequest{
		Name: req.Name, Slug: req.Slug, Description: req.Description,
		SlugAliases: req.SlugAliases, Kind: req.Kind,
	})
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func writeHomeErr(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	switch {
	case errors.Is(err, home.ErrNotInitialized), errors.Is(err, home.ErrClusterNotFound), errors.Is(err, home.ErrProjectNotFound):
		code = http.StatusNotFound
	case errors.Is(err, home.ErrAmbiguousProject), errors.Is(err, home.ErrAmbiguousProjectReference), errors.Is(err, home.ErrLegacyChanged), errors.Is(err, home.ErrDetachRequiresReview), errors.Is(err, home.ErrDetachSourceChanged), errors.Is(err, home.ErrDetachTargetExists):
		code = http.StatusConflict
	}
	writeJSON(w, code, errBody(err))
}
