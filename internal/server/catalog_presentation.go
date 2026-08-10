package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"carbon/internal/home"
)

// catalogPresentationIconRequest intentionally has no home path. The selected home
// is resolved only from the existing home-only request scope (default, ?home=, or
// X-Carbon-Home), so a UI mutation cannot redirect presentation metadata elsewhere.
type catalogPresentationIconRequest struct {
	Icon json.RawMessage `json:"icon"`
}

// handleGetCatalogPresentation exposes only presentation data for the local human UI.
// It has no task-store dependency and does not create a missing document.
func (s *Server) handleGetCatalogPresentation(w http.ResponseWriter, r *http.Request) {
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	presentation, err := home.ListCatalogPresentation(root)
	if err != nil {
		writeCatalogPresentationErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, presentation)
}

// handlePutCatalogPresentationIcon sets one cluster/project icon or clears it when
// the strict request body carries `"icon": null`. This is intentionally an HTTP UI
// endpoint only; it is not an MCP tool and therefore cannot be written by agents.
func (s *Server) handlePutCatalogPresentationIcon(w http.ResponseWriter, r *http.Request) {
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	icon, ok := decodeStrictCatalogPresentationIcon(w, r)
	if !ok {
		return
	}
	presentation, err := home.SetCatalogPresentationIcon(
		root,
		home.CatalogPresentationKind(r.PathValue("kind")),
		r.PathValue("id"),
		icon,
	)
	if err != nil {
		writeCatalogPresentationErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, presentation)
}

func writeCatalogPresentationErr(w http.ResponseWriter, err error) {
	if errors.Is(err, home.ErrInvalidCatalogIcon) || errors.Is(err, home.ErrInvalidCatalogPresentationTarget) {
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err))
		return
	}
	writeHomeErr(w, err)
}

// decodeStrictCatalogPresentationIcon enforces a deliberately tiny body surface:
// exactly `{ "icon": { "kind": ..., "key": ... } | null }`. json.Decoder alone
// accepts duplicate object keys, so scan first and reject them at every nesting level.
func decodeStrictCatalogPresentationIcon(w http.ResponseWriter, r *http.Request) (*home.Icon, bool) {
	if r.Body == nil {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("catalog presentation icon body is required")))
		return nil, false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errBody(fmt.Errorf("JSON body exceeds %d bytes", maxJSONBodyBytes)))
			return nil, false
		}
		writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("invalid JSON: %w", err)))
		return nil, false
	}
	if len(bytes.TrimSpace(data)) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("catalog presentation icon body is required")))
		return nil, false
	}
	if err := rejectDuplicateCatalogPresentationJSONKeys(data); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("invalid JSON: %w", err)))
		return nil, false
	}

	var request catalogPresentationIconRequest
	if err := decodeStrictCatalogPresentationValue(data, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("invalid JSON: %w", err)))
		return nil, false
	}
	if len(request.Icon) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("catalog presentation icon is required")))
		return nil, false
	}
	if bytes.Equal(bytes.TrimSpace(request.Icon), []byte("null")) {
		return nil, true
	}

	var icon home.Icon
	if err := decodeStrictCatalogPresentationValue(request.Icon, &icon); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("invalid icon: %w", err)))
		return nil, false
	}
	return &icon, true
}

func decodeStrictCatalogPresentationValue(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func rejectDuplicateCatalogPresentationJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := consumeCatalogPresentationJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func consumeCatalogPresentationJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("expected object key")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeCatalogPresentationJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token() // closing }
		return err
	case '[':
		for decoder.More() {
			if err := consumeCatalogPresentationJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token() // closing ]
		return err
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
}
