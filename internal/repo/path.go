package repo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrCarbonPathOutsideRoot reports a managed Carbon path that resolves through a
// symlink or reparse point outside its repository. Callers must treat it as a hard
// failure rather than falling back to the lexical path.
var ErrCarbonPathOutsideRoot = errors.New("carbon path escapes repository root")

// ErrCairnPathOutsideRoot is kept for source compatibility. Its behavior is canonical
// Carbon behavior: callers using the historical helper now receive .carbon paths.
var ErrCairnPathOutsideRoot = ErrCarbonPathOutsideRoot

// EnsureCarbonDirs imports legacy state when necessary, then creates and validates
// .carbon plus each requested direct child directory. It returns the physical .carbon
// path so callers that need to watch or write it stay on the canonical store.
func EnsureCarbonDirs(root string, names ...string) (string, error) {
	if err := EnsureCarbonStore(root); err != nil {
		return "", err
	}
	resolvedRoot, err := repositoryRoot(root)
	if err != nil {
		return "", err
	}
	carbon, err := ensureDirWithin(resolvedRoot, CarbonDirName)
	if err != nil {
		return "", err
	}
	for _, name := range names {
		if filepath.Base(name) != name || name == "." || name == ".." || name == "" {
			return "", fmt.Errorf("repo: invalid Carbon directory name %q", name)
		}
		if _, err := ensureDirWithin(carbon, name); err != nil {
			return "", err
		}
	}
	return carbon, nil
}

// EnsureCairnDirs is a compatibility alias that always creates/returns .carbon.
func EnsureCairnDirs(root string, names ...string) (string, error) {
	return EnsureCarbonDirs(root, names...)
}

// ValidateCarbonPath resolves an existing .carbon path and returns its canonical
// location only when it remains inside root. It is suitable for consumers such as
// filesystem watchers after EnsureCarbonDirs has created the needed paths.
func ValidateCarbonPath(root, path string) (string, error) {
	if err := EnsureCarbonStore(root); err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %s", ErrCarbonPathOutsideRoot, path)
	}
	if rel != CarbonDirName && !strings.HasPrefix(rel, CarbonDirName+string(filepath.Separator)) {
		return "", fmt.Errorf("repo: path is not managed by %s: %s", CarbonDirName, path)
	}

	resolvedRoot, err := repositoryRoot(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(pathAbs)
	if err != nil {
		return "", err
	}
	if !pathWithin(resolvedRoot, resolved) {
		return "", fmt.Errorf("%w: %s", ErrCarbonPathOutsideRoot, path)
	}
	return filepath.Clean(resolved), nil
}

// ValidateCairnPath is a compatibility adapter for older callers that still construct
// .cairn lexical paths. It maps that relative suffix onto .carbon after any required
// import, and accepts canonical .carbon paths unchanged.
func ValidateCairnPath(root, path string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %s", ErrCarbonPathOutsideRoot, path)
	}
	if rel == LegacyCairnDirName || strings.HasPrefix(rel, LegacyCairnDirName+string(filepath.Separator)) {
		suffix := strings.TrimPrefix(rel, LegacyCairnDirName)
		suffix = strings.TrimPrefix(suffix, string(filepath.Separator))
		pathAbs = filepath.Join(rootAbs, CarbonDirName, suffix)
	}
	return ValidateCarbonPath(root, pathAbs)
}

func repositoryRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("repo: root is not a directory: %s", root)
	}
	return filepath.Clean(resolved), nil
}

func ensureDirWithin(root, name string) (string, error) {
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0o755); err != nil {
			return "", err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return "", err
	}
	if isRepoReparsePoint(path, info) {
		return "", fmt.Errorf("%w: refusing symlink or reparse directory %s", ErrCarbonPathOutsideRoot, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if !pathWithin(root, resolved) {
		return "", fmt.Errorf("%w: %s", ErrCarbonPathOutsideRoot, path)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("repo: not a directory: %s", path)
	}
	return filepath.Clean(resolved), nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// carbonFilePath returns a physical direct child of .carbon. Existing final reparse
// points are rejected so Init never treats an attacker-controlled external file as
// config or workflow content.
func carbonFilePath(root, name string, createCarbon bool) (string, error) {
	var carbon string
	var err error
	if createCarbon {
		carbon, err = EnsureCarbonDirs(root)
	} else {
		carbon, err = ValidateCarbonPath(root, carbonDir(root))
	}
	if err != nil {
		return "", err
	}
	if filepath.Base(name) != name || name == "." || name == ".." || name == "" {
		return "", fmt.Errorf("repo: invalid Carbon filename %q", name)
	}
	path := filepath.Join(carbon, name)
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	if isRepoReparsePoint(path, fi) {
		return "", fmt.Errorf("repo: refusing symlinked Carbon file: %s", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := repositoryRoot(root)
	if err != nil {
		return "", err
	}
	if !pathWithin(resolvedRoot, resolved) {
		return "", fmt.Errorf("%w: %s", ErrCarbonPathOutsideRoot, path)
	}
	return filepath.Clean(resolved), nil
}
