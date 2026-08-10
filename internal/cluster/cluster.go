// Package cluster owns the small, root-level registry that groups independent Carbon
// projects. A cluster is deliberately not a workspace: its manifest only names project
// roots and never creates, moves, or rewrites any project's .carbon directory.
package cluster

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// ManifestFilename is the cluster-owned manifest kept directly in a cluster root.
	ManifestFilename       = ".carbon-cluster.json"
	LegacyManifestFilename = ".cairn-cluster.json"
	manifestLockName       = ".carbon-cluster.lock"
	migrationReceiptName   = "carbon-cluster-migration-receipt.json"

	// Version is the only currently supported manifest schema version.
	Version = 1

	maxManifestBytes int64 = 1 << 20
)

var (
	// ErrNotInitialized means a cluster root does not yet contain a manifest.
	ErrNotInitialized = errors.New("cluster is not initialized")
	// ErrDuplicateProject means the same canonical project root is already registered.
	ErrDuplicateProject = errors.New("project is already registered")
	// ErrProjectNotFound means no manifest project has the requested stable id.
	ErrProjectNotFound = errors.New("cluster project not found")
	// ErrInvalidProjectID means an id cannot safely be used as a project identifier.
	ErrInvalidProjectID = errors.New("invalid cluster project id")
	// ErrInvalidManifest wraps malformed or unsupported manifest content.
	ErrInvalidManifest = errors.New("invalid cluster manifest")
	// ErrUnsafeManifest means the manifest or its lock is a symlink/reparse point or
	// otherwise escapes the canonical cluster root.
	ErrUnsafeManifest = errors.New("unsafe cluster manifest")
	// ErrInvalidRoot means a requested cluster/project path is not an existing directory.
	ErrInvalidRoot = errors.New("cluster path is not an existing directory")
	// ErrLockTimeout means another process held the cluster registry lock too long.
	ErrLockTimeout = errors.New("cluster registry lock timeout")
)

// Manifest is the on-disk v1 registry. Project paths are canonical absolute paths when
// registered; a path may later disappear, in which case callers report that project as
// offline without rewriting the manifest.
type Manifest struct {
	Version  int       `json:"version"`
	Name     string    `json:"name"`
	Projects []Project `json:"projects"`
}

// Project is one registered project. ID belongs to the cluster registry, not to the
// project's task graph, so task ids from different projects can overlap safely.
type Project struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	AddedAt string `json:"addedAt"`
	Legacy  bool   `json:"legacy,omitempty"`
}

// ResolveRoot returns a canonical, absolute, existing directory. It intentionally accepts
// paths outside any launch root: a cluster is a local registry of independently-owned
// projects, not a containment boundary.
func ResolveRoot(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("%w: path is required", ErrInvalidRoot)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("%w: resolve %q: %v", ErrInvalidRoot, raw, err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%w: no such folder: %s", ErrInvalidRoot, abs)
	}
	fi, err := os.Stat(real)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%w: no such folder: %s", ErrInvalidRoot, abs)
	}
	return filepath.Clean(real), nil
}

// ManifestPath returns the canonical manifest path for an existing cluster root.
func ManifestPath(root string) (string, error) {
	resolved, err := ResolveRoot(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, ManifestFilename), nil
}

// DefaultName derives a human-readable fallback name without touching the filesystem.
func DefaultName(root string) string {
	base := filepath.Base(filepath.Clean(root))
	if base == "" || base == "." || base == string(filepath.Separator) || base == filepath.VolumeName(root) {
		return "Cluster"
	}
	return base
}

// Read reads an existing manifest. exists is false (without an error) when the manifest
// has not been created yet; malformed or unsafe manifests are reported explicitly.
func Read(root string) (manifest Manifest, exists bool, err error) {
	resolved, err := ResolveRoot(root)
	if err != nil {
		return Manifest{}, false, err
	}
	err = withLock(resolved, func() error {
		manifest, exists, err = readManifest(resolved)
		return err
	})
	return manifest, exists, err
}

// Ensure creates a manifest when absent and, when legacy is true, adds the cluster root as
// a legacy project if it is not already registered. It only writes ManifestFilename.
// Callers decide whether the root is a valid legacy Cairn workspace; this package never
// reads or writes a project's .carbon directory.
func Ensure(root, name string, legacy bool) (Manifest, error) {
	resolved, err := ResolveRoot(root)
	if err != nil {
		return Manifest{}, err
	}

	var out Manifest
	err = withLock(resolved, func() error {
		manifest, exists, err := readManifest(resolved)
		if err != nil {
			return err
		}
		changed := false
		if !exists {
			manifest = Manifest{Version: Version, Name: normalizedName(name, resolved), Projects: []Project{}}
			changed = true
		}
		if legacy && !hasPath(manifest.Projects, resolved) {
			project, err := newProject(manifest.Projects, resolved, "", true)
			if err != nil {
				return err
			}
			manifest.Projects = append(manifest.Projects, project)
			changed = true
		}
		if changed {
			if err := writeManifest(resolved, manifest); err != nil {
				return err
			}
		}
		out = manifest
		return nil
	})
	return out, err
}

// AddProject registers an existing project directory without initializing or changing its
// .carbon contents. The returned project has a registry-specific stable id.
func AddProject(root, projectPath, name string) (Manifest, Project, error) {
	resolvedRoot, err := ResolveRoot(root)
	if err != nil {
		return Manifest{}, Project{}, err
	}
	resolvedProject, err := ResolveRoot(projectPath)
	if err != nil {
		return Manifest{}, Project{}, err
	}

	var out Manifest
	var added Project
	err = withLock(resolvedRoot, func() error {
		manifest, exists, err := readManifest(resolvedRoot)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotInitialized
		}
		if hasPath(manifest.Projects, resolvedProject) {
			return fmt.Errorf("%w: %s", ErrDuplicateProject, resolvedProject)
		}
		added, err = newProject(manifest.Projects, resolvedProject, name, false)
		if err != nil {
			return err
		}
		manifest.Projects = append(manifest.Projects, added)
		if err := writeManifest(resolvedRoot, manifest); err != nil {
			return err
		}
		out = manifest
		return nil
	})
	return out, added, err
}

// RemoveProject removes only one registry entry. It never visits or mutates the project's
// .carbon directory (or any other project file).
func RemoveProject(root, id string) (Manifest, error) {
	if !validProjectID(id) {
		return Manifest{}, fmt.Errorf("%w: %q", ErrInvalidProjectID, id)
	}
	resolved, err := ResolveRoot(root)
	if err != nil {
		return Manifest{}, err
	}

	var out Manifest
	err = withLock(resolved, func() error {
		manifest, exists, err := readManifest(resolved)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotInitialized
		}
		index := -1
		for i, project := range manifest.Projects {
			if project.ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("%w: %s", ErrProjectNotFound, id)
		}
		manifest.Projects = append(manifest.Projects[:index], manifest.Projects[index+1:]...)
		if manifest.Projects == nil {
			manifest.Projects = []Project{}
		}
		if err := writeManifest(resolved, manifest); err != nil {
			return err
		}
		out = manifest
		return nil
	})
	return out, err
}

// ProjectByID returns a registry entry without attempting to open its project path. This
// preserves the distinction between an offline registered project and a missing entry.
func ProjectByID(root, id string) (Project, error) {
	if !validProjectID(id) {
		return Project{}, fmt.Errorf("%w: %q", ErrInvalidProjectID, id)
	}
	manifest, exists, err := Read(root)
	if err != nil {
		return Project{}, err
	}
	if !exists {
		return Project{}, ErrNotInitialized
	}
	for _, project := range manifest.Projects {
		if project.ID == id {
			return project, nil
		}
	}
	return Project{}, fmt.Errorf("%w: %s", ErrProjectNotFound, id)
}

func readManifest(root string) (Manifest, bool, error) {
	manifest, exists, _, err := readManifestFile(root, ManifestFilename)
	if err != nil || exists {
		return manifest, exists, err
	}
	_, legacyExists, data, err := readManifestFile(root, LegacyManifestFilename)
	if err != nil || !legacyExists {
		return Manifest{}, legacyExists, err
	}
	// A lock held by the caller serializes this one-time import with Add/Remove/Ensure.
	// The legacy file is read and schema-validated before publishing identical bytes at
	// the canonical name, with a second source digest check immediately before rename.
	if err := importLegacyManifest(root, data); err != nil {
		return Manifest{}, false, err
	}
	canonical, canonicalExists, _, err := readManifestFile(root, ManifestFilename)
	if err != nil || !canonicalExists {
		if err == nil {
			err = fmt.Errorf("%w: canonical manifest missing after import", ErrUnsafeManifest)
		}
		return Manifest{}, false, err
	}
	return canonical, true, nil
}

func readManifestFile(root, filename string) (Manifest, bool, []byte, error) {
	path := filepath.Join(root, filename)
	if _, exists, err := safeRegularFile(root, path, true); err != nil {
		return Manifest{}, false, nil, err
	} else if !exists {
		return Manifest{}, false, nil, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return Manifest{}, false, nil, fmt.Errorf("cluster: open manifest: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return Manifest{}, false, nil, fmt.Errorf("cluster: read manifest: %w", err)
	}
	if int64(len(data)) > maxManifestBytes {
		return Manifest{}, false, nil, fmt.Errorf("%w: manifest exceeds %d bytes", ErrInvalidManifest, maxManifestBytes)
	}
	manifest, err := decodeManifest(data)
	if err != nil {
		return Manifest{}, false, nil, err
	}
	return manifest, true, data, nil
}

type manifestWire struct {
	Version  *int            `json:"version"`
	Name     *string         `json:"name"`
	Projects json.RawMessage `json:"projects"`
}

func decodeManifest(data []byte) (Manifest, error) {
	var wire manifestWire
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return Manifest{}, fmt.Errorf("%w: parse JSON: %v", ErrInvalidManifest, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return Manifest{}, fmt.Errorf("%w: multiple JSON values", ErrInvalidManifest)
		}
		return Manifest{}, fmt.Errorf("%w: parse JSON: %v", ErrInvalidManifest, err)
	}
	if wire.Version == nil || wire.Name == nil || len(wire.Projects) == 0 || bytes.Equal(bytes.TrimSpace(wire.Projects), []byte("null")) {
		return Manifest{}, fmt.Errorf("%w: version, name, and projects are required", ErrInvalidManifest)
	}

	var projects []Project
	projectDec := json.NewDecoder(bytes.NewReader(wire.Projects))
	projectDec.DisallowUnknownFields()
	if err := projectDec.Decode(&projects); err != nil {
		return Manifest{}, fmt.Errorf("%w: parse projects: %v", ErrInvalidManifest, err)
	}
	if err := projectDec.Decode(&extra); err != io.EOF {
		return Manifest{}, fmt.Errorf("%w: parse projects: %v", ErrInvalidManifest, err)
	}
	if projects == nil {
		projects = []Project{}
	}
	manifest := Manifest{Version: *wire.Version, Name: *wire.Name, Projects: projects}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.Version != Version {
		return fmt.Errorf("%w: unsupported version %d", ErrInvalidManifest, manifest.Version)
	}
	if !validName(manifest.Name) {
		return fmt.Errorf("%w: invalid cluster name", ErrInvalidManifest)
	}
	seenIDs := make(map[string]struct{}, len(manifest.Projects))
	for _, project := range manifest.Projects {
		if !validProjectID(project.ID) {
			return fmt.Errorf("%w: invalid project id %q", ErrInvalidManifest, project.ID)
		}
		if !validName(project.Name) {
			return fmt.Errorf("%w: invalid project name for %s", ErrInvalidManifest, project.ID)
		}
		if !filepath.IsAbs(project.Path) || filepath.Clean(project.Path) != project.Path {
			return fmt.Errorf("%w: project %s has a non-canonical path", ErrInvalidManifest, project.ID)
		}
		if _, err := time.Parse(time.RFC3339, project.AddedAt); err != nil {
			return fmt.Errorf("%w: project %s has invalid addedAt: %v", ErrInvalidManifest, project.ID, err)
		}
		if _, exists := seenIDs[project.ID]; exists {
			return fmt.Errorf("%w: duplicate project id %s", ErrInvalidManifest, project.ID)
		}
		seenIDs[project.ID] = struct{}{}

		// Missing paths are valid offline registry entries. Existing paths must still
		// resolve to the exact canonical location stored in the manifest.
		if _, err := os.Stat(project.Path); err == nil {
			resolved, err := ResolveRoot(project.Path)
			if err != nil || !samePath(resolved, project.Path) {
				return fmt.Errorf("%w: project %s path is not canonical", ErrInvalidManifest, project.ID)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: inspect project %s path: %v", ErrInvalidManifest, project.ID, err)
		}
	}
	for i := range manifest.Projects {
		for j := 0; j < i; j++ {
			if samePath(manifest.Projects[i].Path, manifest.Projects[j].Path) {
				return fmt.Errorf("%w: duplicate project path %s", ErrInvalidManifest, manifest.Projects[i].Path)
			}
		}
	}
	return nil
}

func writeManifest(root string, manifest Manifest) error {
	if manifest.Projects == nil {
		manifest.Projects = []Project{}
	}
	if err := validateManifest(manifest); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("cluster: encode manifest: %w", err)
	}
	data = append(data, '\n')
	return writeManifestData(root, data)
}

// writeManifestData publishes already-validated manifest bytes at the canonical name.
// Keeping this separate from writeManifest lets legacy import preserve raw bytes while
// still using the same private-temp, sync, and atomic-replace boundary as new writes.
func writeManifestData(root string, data []byte) error {
	path := filepath.Join(root, ManifestFilename)
	if _, _, err := safeRegularFile(root, path, true); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(root, "carbon-cluster-*.tmp")
	if err != nil {
		return fmt.Errorf("cluster: create manifest temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("cluster: chmod manifest temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("cluster: write manifest temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("cluster: sync manifest temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cluster: close manifest temp: %w", err)
	}
	// Re-check immediately before replace. os.Rename replaces a directory entry rather
	// than following it, but rejecting an introduced symlink/reparse point keeps the
	// intended no-escape invariant explicit and fail-closed.
	if _, _, err := safeRegularFile(root, path, true); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("cluster: replace manifest: %w", err)
	}
	if _, _, err := safeRegularFile(root, path, false); err != nil {
		return err
	}
	// Directory sync is best-effort on Windows (where directory handles are not
	// universally syncable); the required durable boundary is the synced temp file
	// followed by the same-directory atomic rename above.
	if dir, err := os.Open(root); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func importLegacyManifest(root string, data []byte) error {
	legacyDigest := sha256.Sum256(data)
	path := filepath.Join(root, ManifestFilename)
	if _, exists, err := safeRegularFile(root, path, true); err != nil {
		return err
	} else if exists {
		return nil // canonical always wins; never merge or replace it with legacy bytes.
	}
	tmp, err := os.CreateTemp(root, "carbon-cluster-*.tmp")
	if err != nil {
		return fmt.Errorf("cluster: create legacy manifest temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Do not publish a mixed legacy snapshot if an old writer changed the source while
	// it was being copied. readManifestFile also re-applies the strict regular-file and
	// no-reparse checks.
	_, exists, latest, err := readManifestFile(root, LegacyManifestFilename)
	if err != nil || !exists {
		if err == nil {
			err = fmt.Errorf("%w: legacy manifest disappeared during import", ErrUnsafeManifest)
		}
		return err
	}
	if current := sha256.Sum256(latest); current != legacyDigest {
		return fmt.Errorf("%w: legacy manifest changed during import", ErrUnsafeManifest)
	}
	if _, exists, err := safeRegularFile(root, path, true); err != nil {
		return err
	} else if exists {
		return nil
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("cluster: publish legacy manifest import: %w", err)
	}
	_, canonicalExists, canonicalData, err := readManifestFile(root, ManifestFilename)
	if err != nil || !canonicalExists {
		if err == nil {
			err = fmt.Errorf("%w: canonical manifest missing after import", ErrUnsafeManifest)
		}
		return err
	}
	if canonicalDigest := sha256.Sum256(canonicalData); canonicalDigest != legacyDigest {
		return fmt.Errorf("%w: imported manifest digest mismatch", ErrUnsafeManifest)
	}
	if err := writeMigrationReceipt(root, legacyDigest); err != nil {
		return err
	}
	return nil
}

func writeMigrationReceipt(root string, digest [sha256.Size]byte) error {
	path := filepath.Join(root, migrationReceiptName)
	if _, exists, err := safeRegularFile(root, path, true); err != nil {
		return err
	} else if exists {
		return nil
	}
	body := struct {
		Version     int    `json:"version"`
		Source      string `json:"source"`
		Digest      string `json:"digest"`
		CompletedAt string `json:"completedAt"`
	}{
		Version:     Version,
		Source:      LegacyManifestFilename,
		Digest:      hex.EncodeToString(digest[:]),
		CompletedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(root, "carbon-cluster-receipt-*.tmp")
	if err != nil {
		return fmt.Errorf("cluster: create migration receipt temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if _, exists, err := safeRegularFile(root, path, true); err != nil {
		return err
	} else if exists {
		return nil
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("cluster: publish migration receipt: %w", err)
	}
	return nil
}

func newProject(existing []Project, path, name string, legacy bool) (Project, error) {
	name = normalizedName(name, path)
	if !validName(name) {
		return Project{}, fmt.Errorf("%w: invalid project name", ErrInvalidManifest)
	}
	id := projectID(path)
	used := make(map[string]struct{}, len(existing))
	for _, project := range existing {
		used[project.ID] = struct{}{}
	}
	if _, taken := used[id]; taken {
		base := id
		for suffix := 2; ; suffix++ {
			candidate := fmt.Sprintf("%s-%d", base, suffix)
			if _, taken := used[candidate]; !taken {
				id = candidate
				break
			}
		}
	}
	return Project{
		ID:      id,
		Name:    name,
		Path:    path,
		AddedAt: time.Now().UTC().Format(time.RFC3339),
		Legacy:  legacy,
	}, nil
}

func normalizedName(name, fallback string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultName(fallback)
	}
	return name
}

func hasPath(projects []Project, path string) bool {
	for _, project := range projects {
		if samePath(project.Path, path) {
			return true
		}
	}
	return false
}

func projectID(path string) string {
	base := slug(DefaultName(path))
	sum := sha256.Sum256([]byte(pathKey(path)))
	return base + "-" + hex.EncodeToString(sum[:6])
}

func slug(value string) string {
	var out strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
			lastDash = false
		default:
			if out.Len() > 0 && !lastDash {
				out.WriteByte('-')
				lastDash = true
			}
		}
	}
	value = strings.Trim(out.String(), "-")
	if value == "" {
		return "project"
	}
	return value
}

func validName(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validProjectID(id string) bool {
	if id == "" || len(id) > 128 || strings.TrimSpace(id) != id {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func safeRegularFile(root, path string, allowMissing bool) (os.FileInfo, bool, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if allowMissing {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("%w: manifest is missing", ErrUnsafeManifest)
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: inspect %s: %v", ErrUnsafeManifest, path, err)
	}
	if isReparsePoint(path, fi) {
		return nil, false, fmt.Errorf("%w: refusing symlink/reparse point %s", ErrUnsafeManifest, path)
	}
	if !fi.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%w: expected regular file %s", ErrUnsafeManifest, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !pathWithin(root, resolved) {
		return nil, false, fmt.Errorf("%w: manifest escapes cluster root", ErrUnsafeManifest)
	}
	return fi, true, nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func samePath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func pathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}
