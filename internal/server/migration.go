package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"carbon/internal/backup"
	"carbon/internal/home"
)

type legacyMigrationReq struct {
	Home           string `json:"home"`
	LegacyCluster  string `json:"legacyCluster"`
	ExpectedDigest string `json:"expectedDigest"`
	ConfigPolicy   string `json:"configPolicy"`
}

// legacyReceiptMeta is intentionally a read-only summary. It gives the desktop UI a
// durable migration audit trail without accepting any client-selected receipt path or
// returning mutable import instructions.
type legacyReceiptMeta struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	AppliedAt  string `json:"appliedAt,omitempty"`
	ClusterID  string `json:"clusterId,omitempty"`
	LegacyRoot string `json:"legacyRoot,omitempty"`
}

func (s *Server) handleLegacyMigrationPreflight(w http.ResponseWriter, r *http.Request) {
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	legacyRoot := strings.TrimSpace(r.URL.Query().Get("legacy_cluster"))
	if legacyRoot == "" {
		legacyRoot = strings.TrimSpace(r.URL.Query().Get("legacyCluster"))
	}
	if legacyRoot == "" {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("legacy_cluster is required")))
		return
	}
	preflight, err := home.PreflightLegacyImport(root, legacyRoot)
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preflight)
}

// handleLegacyMigrationPreview is intentionally read-only. It produces a review digest
// that callers must echo to apply; it never consumes a client-supplied full plan.
func (s *Server) handleLegacyMigrationPreview(w http.ResponseWriter, r *http.Request) {
	var req legacyMigrationReq
	if !decode(w, r, &req) {
		return
	}
	root, err := s.homeRoot(r, req.Home)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	if strings.TrimSpace(req.LegacyCluster) == "" {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("legacyCluster is required")))
		return
	}
	plan, err := home.PlanLegacyImport(root, req.LegacyCluster)
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleLegacyMigrationApply(w http.ResponseWriter, r *http.Request) {
	var req legacyMigrationReq
	if !decode(w, r, &req) {
		return
	}
	root, err := s.homeRoot(r, req.Home)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	if strings.TrimSpace(req.LegacyCluster) == "" || strings.TrimSpace(req.ExpectedDigest) == "" {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("legacyCluster and expectedDigest are required for apply")))
		return
	}
	// Determine whether a reviewed home already exists without writing it. Creating a
	// home just to snapshot a fresh target would change the review digest (HomeExists),
	// invalidating the caller's reviewed plan. Existing homes get a verified pre-import
	// snapshot; fresh homes get a verified post-import baseline instead.
	plan, err := home.PlanLegacyImport(root, req.LegacyCluster)
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	if plan.ReviewDigest != strings.TrimSpace(req.ExpectedDigest) {
		writeHomeErr(w, fmt.Errorf("%w: expected review %s, found %s", home.ErrLegacyChanged, strings.TrimSpace(req.ExpectedDigest), plan.ReviewDigest))
		return
	}
	preExisting := plan.BaseHomeDigest != ""
	var snapshot backup.Snapshot
	if preExisting {
		snapshot, err = createVerifiedHomeSnapshot(r.Context(), root)
		if err != nil {
			writeHomeErr(w, err)
			return
		}
	}
	result, err := home.ApplyLegacyImportRequest(root, home.LegacyImportApplyRequest{
		LegacyRoot: req.LegacyCluster, ExpectedDigest: req.ExpectedDigest, ConfigPolicy: req.ConfigPolicy,
	})
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	if !preExisting {
		snapshot, err = createVerifiedHomeSnapshot(r.Context(), root)
		if err != nil {
			// The import succeeded, so surface that fact rather than claiming it was
			// rolled back. A caller can retry snapshot creation safely.
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "result": result, "snapshotRequired": true})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"result": result, "snapshot": snapshot, "snapshotId": snapshot.ID,
		"snapshotTiming": map[bool]string{true: "pre-import", false: "post-import"}[preExisting],
	})
}

// handleLegacyMigrationReceipts lists durable import receipt metadata from the trusted
// home .carbon directory. It never accepts a filesystem path or follows a receipt
// symlink, so the endpoint cannot be turned into a local file reader.
func (s *Server) handleLegacyMigrationReceipts(w http.ResponseWriter, r *http.Request) {
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	h, err := home.Open(root)
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	dir := filepath.Join(h.CarbonRoot, "receipts")
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusOK, map[string]any{"receipts": []legacyReceiptMeta{}, "latest": nil})
		return
	}
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		writeHomeErr(w, home.ErrUnsafePath)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	metas := make([]legacyReceiptMeta, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		fileInfo, err := os.Lstat(path)
		if err != nil {
			writeHomeErr(w, err)
			return
		}
		if fileInfo.Mode()&os.ModeSymlink != 0 || fileInfo.Size() < 0 || fileInfo.Size() > maxJSONBodyBytes {
			writeHomeErr(w, home.ErrUnsafePath)
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			writeHomeErr(w, err)
			return
		}
		var receipt home.LegacyImportReceipt
		if err := json.Unmarshal(data, &receipt); err != nil {
			writeHomeErr(w, fmt.Errorf("%w: invalid import receipt", home.ErrInvalidManifest))
			return
		}
		if receipt.ID == "" || receipt.Plan.ID != receipt.ID {
			writeHomeErr(w, fmt.Errorf("%w: invalid import receipt", home.ErrInvalidManifest))
			return
		}
		metas = append(metas, legacyReceiptMeta{ID: receipt.ID, Status: receipt.Status, AppliedAt: receipt.AppliedAt, ClusterID: receipt.Plan.ClusterID, LegacyRoot: receipt.Plan.LegacyRoot})
	}
	sort.Slice(metas, func(i, j int) bool {
		if metas[i].AppliedAt != metas[j].AppliedAt {
			return metas[i].AppliedAt > metas[j].AppliedAt
		}
		return metas[i].ID > metas[j].ID
	})
	var latest *legacyReceiptMeta
	if len(metas) > 0 {
		value := metas[0]
		latest = &value
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipts": metas, "latest": latest})
}

func (s *Server) handleHomeDoctor(w http.ResponseWriter, r *http.Request) {
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	repair := r.URL.Query().Get("repair") == "true"
	report, err := home.Doctor(root, home.DoctorOptions{Apply: repair})
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}
