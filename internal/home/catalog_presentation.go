package home

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

const (
	// CatalogPresentationFilename is the home-global, presentation-only catalog
	// decoration file. It remains separate from home.json so adding an icon never
	// changes an authoritative cluster or project record.
	CatalogPresentationFilename = "catalog-presentation.json"
	// CatalogPresentationVersion is the only supported catalog-presentation schema.
	CatalogPresentationVersion = 1

	maxCatalogPresentationBytes   int64 = 1 << 20
	maxCatalogPresentationEntries       = 4096
)

var (
	// ErrInvalidCatalogPresentation is returned when the independent presentation
	// document is malformed, ambiguous, or contains an unsafe icon token.
	ErrInvalidCatalogPresentation = errors.New("invalid Carbon catalog presentation")
	// ErrFutureCatalogPresentationVersion prevents an older binary from rewriting
	// a document it cannot fully understand.
	ErrFutureCatalogPresentationVersion = errors.New("unsupported future Carbon catalog presentation version")
	// ErrInvalidCatalogIcon identifies a caller-provided icon outside the finite,
	// local token catalog. It deliberately excludes arbitrary paths and rendered
	// content such as URLs, SVG, HTML, data URIs, and base64 data.
	ErrInvalidCatalogIcon = errors.New("invalid Carbon catalog icon")
	// ErrInvalidCatalogPresentationTarget identifies an unknown target kind or an
	// ID that is not a canonical stable Carbon cluster/project ID.
	ErrInvalidCatalogPresentationTarget = errors.New("invalid Carbon catalog presentation target")
)

// CatalogPresentationKind names the stable catalog record addressed by an icon.
// It intentionally uses singular values because it is also the HTTP path segment.
type CatalogPresentationKind string

const (
	CatalogPresentationCluster CatalogPresentationKind = "cluster"
	CatalogPresentationProject CatalogPresentationKind = "project"
)

// Icon is a finite presentation token, never user-provided executable or remote
// content. The UI maps the token to its own shipped glyph/emoji asset.
type Icon struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
}

// CatalogPresentation is the durable v1 wire form. Keys are stable IDs from the
// manifest. Empty maps are meaningful and are always represented as JSON objects.
type CatalogPresentation struct {
	Version  int             `json:"version"`
	Clusters map[string]Icon `json:"clusters"`
	Projects map[string]Icon `json:"projects"`
}

type catalogPresentationWire struct {
	Version  *int            `json:"version"`
	Clusters json.RawMessage `json:"clusters"`
	Projects json.RawMessage `json:"projects"`
}

// ListCatalogPresentation returns a defensive copy of this home's catalog icons.
// A missing presentation file is the normal empty state and never creates metadata.
func ListCatalogPresentation(main string) (CatalogPresentation, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return CatalogPresentation{}, err
	}
	carbonRoot, _, err := catalogPresentationHome(root)
	if err != nil {
		return CatalogPresentation{}, err
	}
	presentation, err := readCatalogPresentation(carbonRoot)
	if err != nil {
		return CatalogPresentation{}, err
	}
	return cloneCatalogPresentation(presentation), nil
}

// CatalogPresentation is the Home-handle equivalent of ListCatalogPresentation.
func (h *Home) CatalogPresentation() (CatalogPresentation, error) {
	if h == nil {
		return CatalogPresentation{}, ErrNotInitialized
	}
	return ListCatalogPresentation(h.Root)
}

// SetCatalogPresentationIcon creates, updates, or clears a presentation icon. A nil
// icon clears the selected target's icon. The home manifest is re-read inside the
// home lock before every mutation, so no icon can be written for a stale or guessed
// stable ID. It never rewrites home.json.
func SetCatalogPresentationIcon(main string, kind CatalogPresentationKind, id string, icon *Icon) (CatalogPresentation, error) {
	if err := validateCatalogPresentationTarget(kind, id); err != nil {
		return CatalogPresentation{}, err
	}
	if icon != nil {
		if err := validateCatalogIcon(*icon); err != nil {
			return CatalogPresentation{}, fmt.Errorf("%w: %v", ErrInvalidCatalogIcon, err)
		}
	}
	root, err := resolveRoot(main)
	if err != nil {
		return CatalogPresentation{}, err
	}

	var result CatalogPresentation
	err = withLock(root, func() error {
		carbonRoot, manifest, err := catalogPresentationHome(root)
		if err != nil {
			return err
		}
		if err := ensureCatalogPresentationTarget(manifest, kind, id); err != nil {
			return err
		}
		presentation, err := readCatalogPresentation(carbonRoot)
		if err != nil {
			return err
		}

		icons := catalogPresentationTargetMap(&presentation, kind)
		changed := false
		if icon == nil {
			if _, exists := icons[id]; exists {
				delete(icons, id)
				changed = true
			}
		} else if existing, exists := icons[id]; !exists || existing != *icon {
			icons[id] = *icon
			changed = true
		}
		if changed {
			if err := writeCatalogPresentation(carbonRoot, presentation); err != nil {
				return err
			}
		}
		result = cloneCatalogPresentation(presentation)
		return nil
	})
	if err != nil {
		return CatalogPresentation{}, err
	}
	return result, nil
}

// SetIcon is the concise Home-package entry point for catalog icon mutations.
// It is kept as an alias for integrations that only need this presentation concern.
func SetIcon(main string, kind CatalogPresentationKind, id string, icon *Icon) (CatalogPresentation, error) {
	return SetCatalogPresentationIcon(main, kind, id, icon)
}

// SetCatalogPresentationIcon is the Home-handle equivalent of the package function.
func (h *Home) SetCatalogPresentationIcon(kind CatalogPresentationKind, id string, icon *Icon) (CatalogPresentation, error) {
	if h == nil {
		return CatalogPresentation{}, ErrNotInitialized
	}
	return SetCatalogPresentationIcon(h.Root, kind, id, icon)
}

// SetIcon is the concise Home-handle equivalent of SetCatalogPresentationIcon.
func (h *Home) SetIcon(kind CatalogPresentationKind, id string, icon *Icon) (CatalogPresentation, error) {
	if h == nil {
		return CatalogPresentation{}, ErrNotInitialized
	}
	return SetCatalogPresentationIcon(h.Root, kind, id, icon)
}

func catalogPresentationHome(root string) (string, Manifest, error) {
	carbonRoot, exists, err := carbonDir(root, false)
	if err != nil {
		return "", Manifest{}, err
	}
	if !exists {
		return "", Manifest{}, ErrNotInitialized
	}
	manifest, exists, err := readManifest(carbonRoot)
	if err != nil {
		return "", Manifest{}, err
	}
	if !exists {
		return "", Manifest{}, ErrNotInitialized
	}
	return carbonRoot, manifest, nil
}

func catalogPresentationPath(carbonRoot string) string {
	return filepath.Join(carbonRoot, CatalogPresentationFilename)
}

func readCatalogPresentation(carbonRoot string) (CatalogPresentation, error) {
	filename := catalogPresentationPath(carbonRoot)
	if _, exists, err := safeRegularFile(carbonRoot, filename, true); err != nil {
		return CatalogPresentation{}, err
	} else if !exists {
		return emptyCatalogPresentation(), nil
	}
	f, err := os.Open(filename)
	if err != nil {
		return CatalogPresentation{}, fmt.Errorf("carbon: open catalog presentation: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCatalogPresentationBytes+1))
	if err != nil {
		return CatalogPresentation{}, fmt.Errorf("carbon: read catalog presentation: %w", err)
	}
	if int64(len(data)) > maxCatalogPresentationBytes {
		return CatalogPresentation{}, fmt.Errorf("%w: catalog presentation exceeds %d bytes", ErrInvalidCatalogPresentation, maxCatalogPresentationBytes)
	}
	return decodeCatalogPresentation(data)
}

func decodeCatalogPresentation(data []byte) (CatalogPresentation, error) {
	if !utf8.Valid(data) {
		return CatalogPresentation{}, fmt.Errorf("%w: JSON is not valid UTF-8", ErrInvalidCatalogPresentation)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return CatalogPresentation{}, fmt.Errorf("%w: ambiguous JSON: %v", ErrInvalidCatalogPresentation, err)
	}
	var wire catalogPresentationWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return CatalogPresentation{}, fmt.Errorf("%w: parse JSON: %v", ErrInvalidCatalogPresentation, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return CatalogPresentation{}, fmt.Errorf("%w: multiple JSON values", ErrInvalidCatalogPresentation)
		}
		return CatalogPresentation{}, fmt.Errorf("%w: parse JSON: %v", ErrInvalidCatalogPresentation, err)
	}
	if wire.Version == nil || len(wire.Clusters) == 0 || len(wire.Projects) == 0 ||
		bytes.Equal(bytes.TrimSpace(wire.Clusters), []byte("null")) ||
		bytes.Equal(bytes.TrimSpace(wire.Projects), []byte("null")) {
		return CatalogPresentation{}, fmt.Errorf("%w: version, clusters, and projects are required", ErrInvalidCatalogPresentation)
	}
	if *wire.Version > CatalogPresentationVersion {
		return CatalogPresentation{}, fmt.Errorf("%w: version %d", ErrFutureCatalogPresentationVersion, *wire.Version)
	}
	if *wire.Version != CatalogPresentationVersion {
		return CatalogPresentation{}, fmt.Errorf("%w: unsupported version %d", ErrInvalidCatalogPresentation, *wire.Version)
	}
	clusters, err := decodeCatalogIconMap(wire.Clusters, "clusters")
	if err != nil {
		return CatalogPresentation{}, err
	}
	projects, err := decodeCatalogIconMap(wire.Projects, "projects")
	if err != nil {
		return CatalogPresentation{}, err
	}
	presentation := CatalogPresentation{Version: *wire.Version, Clusters: clusters, Projects: projects}
	if err := validateCatalogPresentation(presentation); err != nil {
		return CatalogPresentation{}, err
	}
	return presentation, nil
}

func decodeCatalogIconMap(raw json.RawMessage, field string) (map[string]Icon, error) {
	var icons map[string]Icon
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&icons); err != nil {
		return nil, fmt.Errorf("%w: parse %s: %v", ErrInvalidCatalogPresentation, field, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%w: parse %s: multiple JSON values", ErrInvalidCatalogPresentation, field)
		}
		return nil, fmt.Errorf("%w: parse %s: %v", ErrInvalidCatalogPresentation, field, err)
	}
	if icons == nil {
		return nil, fmt.Errorf("%w: %s must be an object", ErrInvalidCatalogPresentation, field)
	}
	return icons, nil
}

func writeCatalogPresentation(carbonRoot string, presentation CatalogPresentation) error {
	if err := validateCatalogPresentation(presentation); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cloneCatalogPresentation(presentation), "", "  ")
	if err != nil {
		return fmt.Errorf("carbon: encode catalog presentation: %w", err)
	}
	data = append(data, '\n')
	return atomicWriteJSON(carbonRoot, CatalogPresentationFilename, data)
}

func validateCatalogPresentation(presentation CatalogPresentation) error {
	if presentation.Version > CatalogPresentationVersion {
		return fmt.Errorf("%w: version %d", ErrFutureCatalogPresentationVersion, presentation.Version)
	}
	if presentation.Version != CatalogPresentationVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrInvalidCatalogPresentation, presentation.Version)
	}
	if presentation.Clusters == nil || presentation.Projects == nil {
		return fmt.Errorf("%w: clusters and projects must be objects", ErrInvalidCatalogPresentation)
	}
	if len(presentation.Clusters) > maxCatalogPresentationEntries || len(presentation.Projects) > maxCatalogPresentationEntries {
		return fmt.Errorf("%w: clusters and projects must contain at most %d entries", ErrInvalidCatalogPresentation, maxCatalogPresentationEntries)
	}
	for id, icon := range presentation.Clusters {
		if !validID(id, "cluster") {
			return fmt.Errorf("%w: invalid cluster id %q", ErrInvalidCatalogPresentation, id)
		}
		if err := validateCatalogIcon(icon); err != nil {
			return fmt.Errorf("%w: invalid cluster icon for %s: %v", ErrInvalidCatalogPresentation, id, err)
		}
	}
	for id, icon := range presentation.Projects {
		if !validID(id, "project") {
			return fmt.Errorf("%w: invalid project id %q", ErrInvalidCatalogPresentation, id)
		}
		if err := validateCatalogIcon(icon); err != nil {
			return fmt.Errorf("%w: invalid project icon for %s: %v", ErrInvalidCatalogPresentation, id, err)
		}
	}
	return nil
}

func validateCatalogIcon(icon Icon) error {
	switch icon.Kind {
	case "builtin":
		if _, ok := builtinCatalogIconKeys[icon.Key]; ok {
			return nil
		}
	case "emoji":
		if _, ok := emojiCatalogIconKeys[icon.Key]; ok {
			return nil
		}
	}
	return fmt.Errorf("kind %q and key %q are not an allowed catalog icon token", icon.Kind, icon.Key)
}

var builtinCatalogIconKeys = map[string]struct{}{
	"folder": {}, "layers": {}, "monitor": {}, "smartphone": {}, "apple": {},
	"globe": {}, "server": {}, "package": {}, "database": {}, "flask": {},
}

var emojiCatalogIconKeys = map[string]struct{}{
	"atom": {}, "rocket": {}, "spark": {}, "puzzle": {}, "shield": {}, "palette": {},
}

func validateCatalogPresentationTarget(kind CatalogPresentationKind, id string) error {
	switch kind {
	case CatalogPresentationCluster:
		if validID(id, "cluster") {
			return nil
		}
	case CatalogPresentationProject:
		if validID(id, "project") {
			return nil
		}
	}
	return fmt.Errorf("%w: kind %q id %q", ErrInvalidCatalogPresentationTarget, kind, id)
}

func ensureCatalogPresentationTarget(manifest Manifest, kind CatalogPresentationKind, id string) error {
	switch kind {
	case CatalogPresentationCluster:
		for _, cluster := range manifest.Clusters {
			if cluster.ID == id {
				return nil
			}
		}
		return fmt.Errorf("%w: %s", ErrClusterNotFound, id)
	case CatalogPresentationProject:
		// A catalog project image is bound only to an immutable project ID. The
		// manifest may hold either a standalone project or a cluster child, and
		// both are valid presentation targets without accepting mutable aliases.
		if projectIDInManifest(manifest, id) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrProjectNotFound, id)
	default:
		return fmt.Errorf("%w: kind %q", ErrInvalidCatalogPresentationTarget, kind)
	}
}

func catalogPresentationTargetMap(presentation *CatalogPresentation, kind CatalogPresentationKind) map[string]Icon {
	switch kind {
	case CatalogPresentationCluster:
		return presentation.Clusters
	case CatalogPresentationProject:
		return presentation.Projects
	default:
		// validateCatalogPresentationTarget runs before this helper. Keep an empty
		// map fallback to avoid a panic if a future internal caller violates that
		// contract; the value is never persisted in that case.
		return map[string]Icon{}
	}
}

func emptyCatalogPresentation() CatalogPresentation {
	return CatalogPresentation{
		Version:  CatalogPresentationVersion,
		Clusters: map[string]Icon{},
		Projects: map[string]Icon{},
	}
}

func cloneCatalogPresentation(presentation CatalogPresentation) CatalogPresentation {
	clone := emptyCatalogPresentation()
	clone.Version = presentation.Version
	for id, icon := range presentation.Clusters {
		clone.Clusters[id] = icon
	}
	for id, icon := range presentation.Projects {
		clone.Projects[id] = icon
	}
	return clone
}
