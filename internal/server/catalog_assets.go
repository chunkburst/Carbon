package server

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"carbon/internal/home"
)

// handlePutCatalogPresentationAsset accepts exactly one raw image body for a
// target in the selected home-only scope. It deliberately does not accept a JSON
// wrapper, multipart form, client filename, or client path.
func (s *Server) handlePutCatalogPresentationAsset(w http.ResponseWriter, r *http.Request) {
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	contentType, ok := catalogPresentationAssetUploadContentType(w, r)
	if !ok {
		return
	}
	data, ok := readCatalogPresentationAssetUpload(w, r)
	if !ok {
		return
	}
	asset, err := home.PutCatalogPresentationAsset(
		root,
		home.CatalogPresentationKind(r.PathValue("kind")),
		r.PathValue("id"),
		contentType,
		data,
	)
	if err != nil {
		writeCatalogPresentationAssetErr(w, err)
		return
	}
	w.Header().Set("ETag", catalogPresentationAssetETag(asset))
	w.WriteHeader(http.StatusNoContent)
}

// handleGetCatalogPresentationAsset returns only the server-normalized PNG. The
// URL is target-bound and the selected home comes only from the established
// home-only scope, never from a client-provided filesystem path in the body.
func (s *Server) handleGetCatalogPresentationAsset(w http.ResponseWriter, r *http.Request) {
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	data, asset, err := home.GetCatalogPresentationAsset(
		root,
		home.CatalogPresentationKind(r.PathValue("kind")),
		r.PathValue("id"),
	)
	if err != nil {
		if errors.Is(err, home.ErrCatalogAssetNotFound) {
			// A browser may probe this URL before a custom image is uploaded. Do not
			// let that negative result survive the subsequent successful PUT.
			w.Header().Set("Cache-Control", "no-store")
		}
		writeCatalogPresentationAssetErr(w, err)
		return
	}
	etag := catalogPresentationAssetETag(asset)
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Target URLs stay stable when a user replaces an image, so cache entries must
	// revalidate. The strong ETag makes that revalidation cheap without allowing a
	// browser to render a stale custom icon after a successful PUT.
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("ETag", etag)
	if catalogPresentationAssetNotModified(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleDeleteCatalogPresentationAsset clears the target's custom image. It is
// idempotent so a UI can safely use it as the explicit "use token/default" action.
func (s *Server) handleDeleteCatalogPresentationAsset(w http.ResponseWriter, r *http.Request) {
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	if err := home.DeleteCatalogPresentationAsset(root, home.CatalogPresentationKind(r.PathValue("kind")), r.PathValue("id")); err != nil {
		writeCatalogPresentationAssetErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func catalogPresentationAssetUploadContentType(w http.ResponseWriter, r *http.Request) (string, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		writeJSON(w, http.StatusUnsupportedMediaType, errBody(errors.New("catalog asset Content-Type must be image/png, image/jpeg, or image/webp")))
		return "", false
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch mediaType {
	case "image/png", "image/jpeg", "image/webp":
		return mediaType, true
	default:
		writeJSON(w, http.StatusUnsupportedMediaType, errBody(errors.New("catalog asset Content-Type must be image/png, image/jpeg, or image/webp")))
		return "", false
	}
}

func readCatalogPresentationAssetUpload(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Body == nil {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("catalog asset body is required")))
		return nil, false
	}
	if r.ContentLength > home.MaxCatalogPresentationAssetBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errBody(fmt.Errorf("catalog asset exceeds %d bytes", home.MaxCatalogPresentationAssetBytes)))
		return nil, false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, home.MaxCatalogPresentationAssetBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errBody(fmt.Errorf("catalog asset exceeds %d bytes", home.MaxCatalogPresentationAssetBytes)))
			return nil, false
		}
		writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("read catalog asset: %w", err)))
		return nil, false
	}
	if len(data) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("catalog asset body is required")))
		return nil, false
	}
	return data, true
}

func catalogPresentationAssetETag(asset home.CatalogPresentationAsset) string {
	return `"` + asset.SHA256 + `"`
}

func catalogPresentationAssetNotModified(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag {
			return true
		}
	}
	return false
}

func writeCatalogPresentationAssetErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, home.ErrCatalogAssetNotFound):
		writeJSON(w, http.StatusNotFound, errBody(err))
	case errors.Is(err, home.ErrInvalidCatalogAsset),
		errors.Is(err, home.ErrFutureCatalogAssetVersion),
		errors.Is(err, home.ErrInvalidCatalogPresentationTarget):
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
	default:
		writeHomeErr(w, err)
	}
}
