package home

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"carbon/internal/config"
	"carbon/internal/repo"
)

// resolveRoot returns a canonical, existing local home directory. On Windows it rejects
// UNC/device namespaces and every symlink/junction/reparse component before resolving;
// Carbon metadata must never be redirected through a network share or user-controlled
// reparse hop. Other platforms retain the existing canonical-symlink behaviour.
func resolveRoot(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("%w: path is required", ErrInvalidRoot)
	}
	if err := validateRootInput(raw); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("%w: resolve %q: %v", ErrInvalidRoot, raw, err)
	}
	// Preserve ErrInvalidRoot for a missing/non-directory selection before applying
	// the stricter Windows component policy below. A present junction still reaches
	// validateCanonicalRoot and is rejected as unsafe rather than being followed.
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: no such folder: %s", ErrInvalidRoot, abs)
	}
	if err := validateCanonicalRoot(abs); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%w: no such folder: %s", ErrInvalidRoot, abs)
	}
	if err := validateCanonicalRoot(resolved); err != nil {
		return "", err
	}
	info, err = os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: no such folder: %s", ErrInvalidRoot, abs)
	}
	return filepath.Clean(resolved), nil
}

// carbonDir ensures (when requested) that .carbon itself is a real directory directly
// under root. We reject even an in-root link: metadata must not have an alternate entry
// point that could turn a later rename into an unexpected target.
func carbonDir(root string, create bool) (string, bool, error) {
	root, err := resolveRoot(root)
	if err != nil {
		return "", false, err
	}
	p := filepath.Join(root, CarbonDirName)
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return "", false, nil
		}
		if err := os.Mkdir(p, 0o700); err != nil {
			return "", false, fmt.Errorf("carbon: create metadata directory: %w", err)
		}
		info, err = os.Lstat(p)
	}
	if err != nil {
		return "", false, fmt.Errorf("%w: inspect %s: %v", ErrUnsafePath, p, err)
	}
	if isReparsePoint(p, info) || !info.IsDir() {
		return "", false, fmt.Errorf("%w: refusing metadata directory %s", ErrUnsafePath, p)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil || !samePath(resolved, p) || !pathWithin(root, resolved) {
		return "", false, fmt.Errorf("%w: metadata directory escapes home root", ErrUnsafePath)
	}
	return filepath.Clean(p), true, nil
}

func safeRegularFile(root, filename string, allowMissing bool) (os.FileInfo, bool, error) {
	info, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		if allowMissing {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("%w: metadata file is missing", ErrUnsafePath)
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: inspect %s: %v", ErrUnsafePath, filename, err)
	}
	if isReparsePoint(filename, info) || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%w: refusing metadata file %s", ErrUnsafePath, filename)
	}
	resolved, err := filepath.EvalSymlinks(filename)
	if err != nil || !samePath(resolved, filename) || !pathWithin(root, resolved) {
		return nil, false, fmt.Errorf("%w: metadata file escapes home root", ErrUnsafePath)
	}
	return info, true, nil
}

// ensureDataRoot creates or validates every relative directory component. DataPath is a
// slash-separated manifest value so it is stable if a home later moves between platforms.
func ensureDataRoot(carbonRoot, relative string) (string, error) {
	if !validDataPath(relative) {
		return "", fmt.Errorf("%w: invalid relative cluster data path %q", ErrInvalidManifest, relative)
	}
	current := carbonRoot
	for _, component := range strings.Split(relative, "/") {
		candidate := filepath.Join(current, component)
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(candidate, 0o700); err != nil {
				return "", fmt.Errorf("carbon: create cluster data directory: %w", err)
			}
			info, err = os.Lstat(candidate)
		}
		if err != nil {
			return "", fmt.Errorf("%w: inspect cluster data directory %s: %v", ErrUnsafePath, candidate, err)
		}
		if isReparsePoint(candidate, info) || !info.IsDir() {
			return "", fmt.Errorf("%w: refusing cluster data directory %s", ErrUnsafePath, candidate)
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil || !samePath(resolved, candidate) || !pathWithin(carbonRoot, resolved) {
			return "", fmt.Errorf("%w: cluster data directory escapes Carbon home", ErrUnsafePath)
		}
		current = filepath.Clean(candidate)
	}
	return current, nil
}

// ensureClusterStore creates the isolated data root and only its private Carbon storage
// scaffold. It never writes project-facing files into a source repository.
func ensureClusterStore(carbonRoot, relative, prefix string) (string, error) {
	root, err := ensureDataRoot(carbonRoot, relative)
	if err != nil {
		return "", err
	}
	if err := repo.InitDataRoot(root, prefix); err != nil {
		return "", fmt.Errorf("carbon: initialize cluster task store: %w", err)
	}
	return root, nil
}

// standaloneProjectDataPath derives the only permitted standalone root. It deliberately
// does not accept a caller-selected path: a stable project ID is the sole path component
// beneath .carbon/projects, which keeps standalone task stores confined to the home.
func standaloneProjectDataPath(projectID string) (string, error) {
	if !validID(projectID, "project") {
		return "", fmt.Errorf("%w: invalid standalone project id %q", ErrInvalidManifest, projectID)
	}
	return path.Join(projectDataDirectory, projectID), nil
}

// ensureStandaloneProjectStore initializes one project-owned private store. Unlike a
// shared cluster root, its config must carry the stable project ID so direct store users
// cannot accidentally create unscoped tasks when no cluster exists.
func ensureStandaloneProjectStore(carbonRoot, projectID, prefix string) (string, error) {
	relative, err := standaloneProjectDataPath(projectID)
	if err != nil {
		return "", err
	}
	root, err := ensureDataRoot(carbonRoot, relative)
	if err != nil {
		return "", err
	}
	if err := repo.InitDataRoot(root, prefix); err != nil {
		return "", fmt.Errorf("carbon: initialize standalone project task store: %w", err)
	}
	filename := filepath.Join(root, repo.CarbonDirName, "config.yaml")
	cfg, err := config.Load(filename)
	if err != nil {
		return "", fmt.Errorf("carbon: load standalone project task-store config: %w", err)
	}
	if cfg.ProjectID != "" && cfg.ProjectID != projectID {
		return "", fmt.Errorf("%w: standalone project store %s belongs to %s", ErrInvalidManifest, projectID, cfg.ProjectID)
	}
	if cfg.ProjectID == projectID {
		return root, nil
	}
	cfg.ProjectID = projectID
	if err := config.Save(filename, cfg); err != nil {
		return "", fmt.Errorf("carbon: set standalone project task-store id: %w", err)
	}
	return root, nil
}

// dataRoot is the read-only sibling of ensureDataRoot. A deleted Carbon data root is an
// operational error rather than an opportunity for a read API to create new state.
func dataRoot(carbonRoot, relative string) (string, error) {
	if !validDataPath(relative) {
		return "", fmt.Errorf("%w: invalid relative cluster data path %q", ErrInvalidManifest, relative)
	}
	current := carbonRoot
	for _, component := range strings.Split(relative, "/") {
		candidate := filepath.Join(current, component)
		info, err := os.Lstat(candidate)
		if err != nil {
			return "", fmt.Errorf("carbon: inspect cluster data directory: %w", err)
		}
		if isReparsePoint(candidate, info) || !info.IsDir() {
			return "", fmt.Errorf("%w: refusing cluster data directory %s", ErrUnsafePath, candidate)
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil || !samePath(resolved, candidate) || !pathWithin(carbonRoot, resolved) {
			return "", fmt.Errorf("%w: cluster data directory escapes Carbon home", ErrUnsafePath)
		}
		current = filepath.Clean(candidate)
	}
	return current, nil
}

func validDataPath(value string) bool {
	if value == "" || strings.Contains(value, `\`) || path.IsAbs(value) || path.Clean(value) != value {
		return false
	}
	if value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func observeSource(raw string) (string, string, error) {
	canonical, err := resolveRoot(raw)
	if err != nil {
		return "", "", err
	}
	fingerprint, err := sourceFingerprint(canonical)
	if err != nil {
		return "", "", err
	}
	if fingerprint == "" {
		return "", "", fmt.Errorf("%w: source fingerprint unavailable", ErrInvalidRoot)
	}
	return canonical, fingerprint, nil
}

func validStoredPath(value string) bool {
	if !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return validLocalStoredPath(value)
}

func pathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func samePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func containsPath(paths []string, candidate string) bool {
	for _, existing := range paths {
		if samePath(existing, candidate) {
			return true
		}
	}
	return false
}

func appendUniquePath(paths []string, candidate string) []string {
	if containsPath(paths, candidate) {
		return paths
	}
	return append(paths, candidate)
}
