package home

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxManifestBytes int64 = 1 << 20

func manifestPath(carbonRoot string) string { return filepath.Join(carbonRoot, ManifestFilename) }

func readManifest(carbonRoot string) (Manifest, bool, error) {
	data, exists, err := readManifestBytes(carbonRoot)
	if err != nil || !exists {
		return Manifest{}, exists, err
	}
	manifest, err := decodeManifest(data)
	if err != nil {
		return Manifest{}, false, err
	}
	return manifest, true, nil
}

func readManifestBytes(carbonRoot string) ([]byte, bool, error) {
	filename := manifestPath(carbonRoot)
	if _, exists, err := safeRegularFile(carbonRoot, filename, true); err != nil {
		return nil, false, err
	} else if !exists {
		return nil, false, nil
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, false, fmt.Errorf("carbon: open home manifest: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("carbon: read home manifest: %w", err)
	}
	if int64(len(data)) > maxManifestBytes {
		return nil, false, fmt.Errorf("%w: home manifest exceeds %d bytes", ErrInvalidManifest, maxManifestBytes)
	}
	return data, true, nil
}

type manifestWire struct {
	Version   *int            `json:"version"`
	ID        *string         `json:"id"`
	CreatedAt *string         `json:"createdAt"`
	Clusters  json.RawMessage `json:"clusters"`
	Projects  json.RawMessage `json:"projects"`
}

func decodeManifest(data []byte) (Manifest, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Manifest{}, err
	}
	var wire manifestWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Manifest{}, fmt.Errorf("%w: parse JSON: %v", ErrInvalidManifest, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Manifest{}, fmt.Errorf("%w: multiple JSON values", ErrInvalidManifest)
		}
		return Manifest{}, fmt.Errorf("%w: parse JSON: %v", ErrInvalidManifest, err)
	}
	if wire.Version == nil || wire.ID == nil || wire.CreatedAt == nil || len(wire.Clusters) == 0 || bytes.Equal(bytes.TrimSpace(wire.Clusters), []byte("null")) {
		return Manifest{}, fmt.Errorf("%w: version, id, createdAt, and clusters are required", ErrInvalidManifest)
	}
	if *wire.Version > Version {
		return Manifest{}, fmt.Errorf("%w: version %d", ErrFutureVersion, *wire.Version)
	}
	if *wire.Version != legacyManifestVersion && *wire.Version != Version {
		return Manifest{}, fmt.Errorf("%w: unsupported version %d", ErrInvalidManifest, *wire.Version)
	}
	var clusters []Cluster
	clustersDecoder := json.NewDecoder(bytes.NewReader(wire.Clusters))
	clustersDecoder.DisallowUnknownFields()
	if err := clustersDecoder.Decode(&clusters); err != nil {
		return Manifest{}, fmt.Errorf("%w: parse clusters: %v", ErrInvalidManifest, err)
	}
	if err := clustersDecoder.Decode(&extra); err != io.EOF {
		return Manifest{}, fmt.Errorf("%w: parse clusters: %v", ErrInvalidManifest, err)
	}
	if clusters == nil {
		clusters = []Cluster{}
	}
	manifest := Manifest{Version: *wire.Version, ID: *wire.ID, CreatedAt: *wire.CreatedAt, Clusters: clusters}
	if manifest.Version == Version {
		if len(wire.Projects) == 0 || bytes.Equal(bytes.TrimSpace(wire.Projects), []byte("null")) {
			return Manifest{}, fmt.Errorf("%w: projects are required for version %d", ErrInvalidManifest, Version)
		}
		var projects []Project
		projectsDecoder := json.NewDecoder(bytes.NewReader(wire.Projects))
		projectsDecoder.DisallowUnknownFields()
		if err := projectsDecoder.Decode(&projects); err != nil {
			return Manifest{}, fmt.Errorf("%w: parse projects: %v", ErrInvalidManifest, err)
		}
		if err := projectsDecoder.Decode(&extra); err != io.EOF {
			return Manifest{}, fmt.Errorf("%w: parse projects: %v", ErrInvalidManifest, err)
		}
		if projects == nil {
			projects = []Project{}
		}
		manifest.Projects = projects
	} else if len(wire.Projects) != 0 {
		return Manifest{}, fmt.Errorf("%w: projects are unsupported in version %d", ErrInvalidManifest, legacyManifestVersion)
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.Version > Version {
		return fmt.Errorf("%w: version %d", ErrFutureVersion, manifest.Version)
	}
	if manifest.Version != legacyManifestVersion && manifest.Version != Version {
		return fmt.Errorf("%w: unsupported version %d", ErrInvalidManifest, manifest.Version)
	}
	if !validID(manifest.ID, "home") {
		return fmt.Errorf("%w: invalid home id %q", ErrInvalidManifest, manifest.ID)
	}
	if !validTimestamp(manifest.CreatedAt) {
		return fmt.Errorf("%w: invalid home createdAt", ErrInvalidManifest)
	}
	if manifest.Clusters == nil {
		return fmt.Errorf("%w: clusters must be an array", ErrInvalidManifest)
	}
	if manifest.Version == Version && manifest.Projects == nil {
		return fmt.Errorf("%w: projects must be an array", ErrInvalidManifest)
	}
	if manifest.Version == legacyManifestVersion && manifest.Projects != nil {
		return fmt.Errorf("%w: projects are unsupported in version %d", ErrInvalidManifest, legacyManifestVersion)
	}

	ids := map[string]struct{}{manifest.ID: {}}
	dataPaths := make(map[string]struct{}, len(manifest.Clusters))
	clusterReferences := make(map[string]string, len(manifest.Clusters)*2)
	nestedProjectReferences := make(map[string]struct{})
	for clusterIndex, cluster := range manifest.Clusters {
		if !validID(cluster.ID, "cluster") {
			return fmt.Errorf("%w: invalid cluster id at index %d", ErrInvalidManifest, clusterIndex)
		}
		if _, exists := ids[cluster.ID]; exists {
			return fmt.Errorf("%w: duplicate id %s", ErrInvalidManifest, cluster.ID)
		}
		ids[cluster.ID] = struct{}{}
		if !validName(cluster.Name) {
			return fmt.Errorf("%w: invalid cluster name for %s", ErrInvalidManifest, cluster.ID)
		}
		if !validDescription(cluster.Description) {
			return fmt.Errorf("%w: invalid cluster description for %s", ErrInvalidManifest, cluster.ID)
		}
		if err := validateSlugReferenceSet(clusterReferences, cluster.ID, "cluster", cluster.Slug, cluster.SlugAliases); err != nil {
			return err
		}
		if !validPrefix(cluster.Prefix) {
			return fmt.Errorf("%w: invalid cluster prefix for %s", ErrInvalidManifest, cluster.ID)
		}
		if !validDataPath(cluster.DataPath) {
			return fmt.Errorf("%w: invalid dataPath for %s", ErrInvalidManifest, cluster.ID)
		}
		if _, exists := dataPaths[cluster.DataPath]; exists {
			return fmt.Errorf("%w: duplicate cluster dataPath %s", ErrInvalidManifest, cluster.DataPath)
		}
		dataPaths[cluster.DataPath] = struct{}{}
		if !validTimestamp(cluster.CreatedAt) {
			return fmt.Errorf("%w: invalid cluster createdAt for %s", ErrInvalidManifest, cluster.ID)
		}
		if cluster.Projects == nil {
			return fmt.Errorf("%w: projects must be an array for %s", ErrInvalidManifest, cluster.ID)
		}
		projectReferences := make(map[string]string, len(cluster.Projects)*2)
		for projectIndex, project := range cluster.Projects {
			if err := validateProject(project, projectIndex, cluster.ID, ids, projectReferences); err != nil {
				return err
			}
			collectSlugReferences(nestedProjectReferences, project.Slug, project.SlugAliases)
		}
	}
	if manifest.Version == Version {
		projectReferences := make(map[string]string, len(manifest.Projects)*2)
		for projectIndex, project := range manifest.Projects {
			if err := validateProject(project, projectIndex, "", ids, projectReferences); err != nil {
				return err
			}
			if err := rejectNestedProjectSlugCollision(project.ID, project.Slug, project.SlugAliases, nestedProjectReferences); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateProject(project Project, index int, clusterID string, ids map[string]struct{}, references map[string]string) error {
	location := fmt.Sprintf("at index %d", index)
	if clusterID != "" {
		location += " in " + clusterID
	}
	if !validID(project.ID, "project") {
		return fmt.Errorf("%w: invalid project id %s", ErrInvalidManifest, location)
	}
	if _, exists := ids[project.ID]; exists {
		return fmt.Errorf("%w: duplicate id %s", ErrInvalidManifest, project.ID)
	}
	ids[project.ID] = struct{}{}
	if !validName(project.Name) {
		return fmt.Errorf("%w: invalid project name for %s", ErrInvalidManifest, project.ID)
	}
	if !validDescription(project.Description) {
		return fmt.Errorf("%w: invalid project description for %s", ErrInvalidManifest, project.ID)
	}
	if err := validateSlugReferenceSet(references, project.ID, "project", project.Slug, project.SlugAliases); err != nil {
		return err
	}
	if !validProjectKind(project.Kind) {
		return fmt.Errorf("%w: invalid project kind for %s", ErrInvalidManifest, project.ID)
	}
	if !validTimestamp(project.CreatedAt) {
		return fmt.Errorf("%w: invalid project createdAt for %s", ErrInvalidManifest, project.ID)
	}
	return validateSource(project.ID, project.Source)
}

func collectSlugReferences(seen map[string]struct{}, slug string, aliases []string) {
	if slug != "" {
		seen[strings.ToLower(slug)] = struct{}{}
	}
	for _, alias := range aliases {
		seen[strings.ToLower(alias)] = struct{}{}
	}
}

// A standalone project is addressable without a cluster. Its slug and aliases must not
// shadow a clustered project, while clustered projects retain their historical
// cluster-local slug namespace for compatibility.
func rejectNestedProjectSlugCollision(id, slug string, aliases []string, nested map[string]struct{}) error {
	for _, value := range append([]string{slug}, aliases...) {
		if value == "" {
			continue
		}
		if _, exists := nested[strings.ToLower(value)]; exists {
			return fmt.Errorf("%w: standalone project slug or alias %q for %s collides with clustered project", ErrInvalidManifest, value, id)
		}
	}
	return nil
}

// validateSlugReferenceSet rejects every collision between a current slug and a
// historical alias. IDs stay the durable preference, but a case-insensitive machine
// reference must map to exactly one entry so discovery cannot silently choose a peer.
// Empty current slugs are accepted for pre-slug v1 metadata; aliases are optional too.
func validateSlugReferenceSet(seen map[string]string, id, kind, slug string, aliases []string) error {
	if slug != "" {
		if !validSlug(slug) {
			return fmt.Errorf("%w: invalid %s slug for %s", ErrInvalidManifest, kind, id)
		}
		if err := addSlugReference(seen, id, kind, slug); err != nil {
			return err
		}
	}
	for _, alias := range aliases {
		if !validSlug(alias) {
			return fmt.Errorf("%w: invalid %s slug alias for %s", ErrInvalidManifest, kind, id)
		}
		if err := addSlugReference(seen, id, kind, alias); err != nil {
			return err
		}
	}
	return nil
}

func addSlugReference(seen map[string]string, id, kind, value string) error {
	key := strings.ToLower(value)
	if previous, exists := seen[key]; exists {
		return fmt.Errorf("%w: duplicate %s slug or alias %q for %s and %s", ErrInvalidManifest, kind, value, previous, id)
	}
	seen[key] = id
	return nil
}

func validateSource(projectID string, source Source) error {
	if !validStoredPath(source.Path) {
		return fmt.Errorf("%w: project %s has a non-canonical source path", ErrInvalidManifest, projectID)
	}
	if source.Aliases == nil || len(source.Aliases) == 0 {
		return fmt.Errorf("%w: project %s must have source aliases", ErrInvalidManifest, projectID)
	}
	seen := make(map[string]struct{}, len(source.Aliases))
	hasCurrent := false
	for _, alias := range source.Aliases {
		if !validStoredPath(alias) {
			return fmt.Errorf("%w: project %s has a non-canonical source alias", ErrInvalidManifest, projectID)
		}
		key := canonicalPathKey(alias)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: project %s has duplicate source alias", ErrInvalidManifest, projectID)
		}
		seen[key] = struct{}{}
		if samePath(alias, source.Path) {
			hasCurrent = true
		}
	}
	if !hasCurrent {
		return fmt.Errorf("%w: project %s source path is not in aliases", ErrInvalidManifest, projectID)
	}
	if !validFingerprint(source.Fingerprint) {
		return fmt.Errorf("%w: project %s has an invalid source fingerprint", ErrInvalidManifest, projectID)
	}
	if !validTimestamp(source.LastSeen) {
		return fmt.Errorf("%w: project %s has invalid source lastSeen", ErrInvalidManifest, projectID)
	}
	// An offline source is valid. If it reappears, it must still be the exact physical
	// canonical path written in metadata; a lexical symlink is never accepted silently.
	if _, err := os.Stat(source.Path); err == nil {
		canonical, err := resolveRoot(source.Path)
		if err != nil || !samePath(canonical, source.Path) {
			return fmt.Errorf("%w: project %s source path is not canonical", ErrInvalidManifest, projectID)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect project %s source path: %v", ErrInvalidManifest, projectID, err)
	}
	return nil
}

func validFingerprint(value string) bool {
	if value == "" || len(value) > 512 || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return strings.HasPrefix(value, "fs:") || strings.HasPrefix(value, "legacy:")
}

func validTimestamp(value string) bool {
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}

func canonicalPathKey(value string) string {
	if samePath("A", "a") {
		return strings.ToLower(value)
	}
	return value
}

func writeManifest(carbonRoot string, manifest Manifest) error {
	// Any mutation publishes the current shape. Reading a v1 manifest itself remains
	// non-mutating, and clustered project entries remain exactly where they were.
	if manifest.Version == legacyManifestVersion {
		manifest.Version = Version
	}
	if manifest.Clusters == nil {
		manifest.Clusters = []Cluster{}
	}
	if manifest.Projects == nil {
		manifest.Projects = []Project{}
	}
	if err := validateManifest(manifest); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("carbon: encode home manifest: %w", err)
	}
	data = append(data, '\n')
	return atomicWriteJSON(carbonRoot, ManifestFilename, data)
}

// atomicWriteJSON writes a direct Carbon metadata child with a synced temporary file and
// same-directory rename. The caller supplies complete JSON bytes including a trailing LF.
func atomicWriteJSON(carbonRoot, name string, data []byte) error {
	if filepath.Base(name) != name || name == "." || name == ".." || name == "" {
		return fmt.Errorf("%w: invalid metadata filename %q", ErrUnsafePath, name)
	}
	if _, _, err := safeRegularFile(carbonRoot, filepath.Join(carbonRoot, name), true); err != nil {
		return err
	}
	temp, err := os.CreateTemp(carbonRoot, ".carbon-*.tmp")
	if err != nil {
		return fmt.Errorf("carbon: create metadata temp: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("carbon: chmod metadata temp: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("carbon: write metadata temp: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("carbon: sync metadata temp: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("carbon: close metadata temp: %w", err)
	}
	target := filepath.Join(carbonRoot, name)
	if _, _, err := safeRegularFile(carbonRoot, target, true); err != nil {
		return err
	}
	if err := os.Rename(tempPath, target); err != nil {
		return fmt.Errorf("carbon: replace metadata: %w", err)
	}
	if _, _, err := safeRegularFile(carbonRoot, target, false); err != nil {
		return err
	}
	if directory, err := os.Open(carbonRoot); err == nil {
		_ = directory.Sync() // best effort on Windows
		_ = directory.Close()
	}
	return nil
}

// rejectDuplicateJSONKeys makes the manifest fail closed for ambiguous JSON object input.
// encoding/json otherwise accepts the final duplicate key, which is undesirable for a
// durable registry that may be inspected by several independently-versioned clients.
func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := consumeJSONValue(decoder); err != nil {
		return fmt.Errorf("%w: parse JSON: %v", ErrInvalidManifest, err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: multiple JSON values", ErrInvalidManifest)
		}
		return fmt.Errorf("%w: parse JSON: %v", ErrInvalidManifest, err)
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
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
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token() // closing }
		return err
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token() // closing ]
		return err
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
}
